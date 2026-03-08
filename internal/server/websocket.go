package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// maxBufnr caps the buffer number accepted from WebSocket query parameters.
// Vim buffer numbers are monotonically increasing ints; this generous upper
// bound prevents unbounded map growth from malicious clients when
// OpenToTheWorld is enabled.
const maxBufnr = 1 << 20 // 1,048,576

// wsClient represents a connected WebSocket browser client.
type wsClient struct {
	mu    sync.Mutex // defense-in-depth write serialization (coder/websocket internally serializes)
	conn  *websocket.Conn
	bufnr int
}

// clientManager tracks WebSocket connections grouped by buffer number.
type clientManager struct {
	mu      sync.RWMutex
	clients map[int][]*wsClient
	logger  *slog.Logger

	// lastMessage stores the most recently broadcast message per buffer.
	// When a new WebSocket client connects, the stored message is
	// replayed immediately so the browser does not sit idle waiting
	// for the next cursor-movement event. This eliminates the race
	// between browser page load and the initial refresh broadcast.
	lastMessage map[int][]byte
}

func newClientManager(logger *slog.Logger) *clientManager {
	return &clientManager{
		clients:     make(map[int][]*wsClient),
		lastMessage: make(map[int][]byte),
		logger:      logger,
	}
}

// addAndReplay registers a client for the given buffer number and atomically
// returns a copy of the most recently broadcast message for that buffer (or
// nil). Combining the add and the lastMessage read in a single lock scope
// prevents a race where a broadcast could slip between add and lastMessage,
// causing the client to receive both the replay and the live broadcast.
func (cm *clientManager) addAndReplay(bufnr int, c *wsClient) []byte {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.clients[bufnr] = append(cm.clients[bufnr], c)
	return bytes.Clone(cm.lastMessage[bufnr])
}

// removeLocked splices a client out of the buffer's client slice by pointer
// identity and cleans up the clients map entry when the slice becomes empty.
// lastMessage is preserved so future connecting clients still receive a
// replay without requiring the editor to re-send. The caller MUST hold
// cm.mu (write lock).
func (cm *clientManager) removeLocked(bufnr int, c *wsClient) {
	clients := cm.clients[bufnr]
	for i, candidate := range clients {
		if candidate == c {
			cm.clients[bufnr] = append(clients[:i], clients[i+1:]...)
			break
		}
	}
	if len(cm.clients[bufnr]) == 0 {
		delete(cm.clients, bufnr)
		// lastMessage is intentionally preserved: cached content
		// remains available for replay to future connecting clients
		// without requiring the editor to re-send. closeAll clears
		// both maps on shutdown.
	}
}

// remove unregisters a client from its buffer number and returns true
// if any clients remain globally, across all buffers. The check is
// performed within the same lock scope as the removal to avoid TOCTOU races.
func (cm *clientManager) remove(bufnr int, c *wsClient) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.removeLocked(bufnr, c)
	return len(cm.clients) > 0
}

// getLastMessage returns a copy of the most recently broadcast message for
// the given buffer, or nil if none exists. Safe for concurrent use.
func (cm *clientManager) getLastMessage(bufnr int) []byte {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return bytes.Clone(cm.lastMessage[bufnr])
}

// clientTarget pairs a wsClient with its buffer number for broadcast and
// eviction tracking. Used by the shared write helpers to log which buffer
// a failed client belonged to.
type clientTarget struct {
	client *wsClient
	bufnr  int
}

// snapshotLocked returns a copy of the client list for bufnr as clientTargets.
// The caller must hold cm.mu at read level or higher: both cm.mu.RLock() and
// cm.mu.Lock() satisfy the requirement. Called under RLock in broadcastAll and
// under Lock in sendToBuffer.
func (cm *clientManager) snapshotLocked(bufnr int) []clientTarget {
	src := cm.clients[bufnr]
	targets := make([]clientTarget, len(src))
	for i, c := range src {
		targets[i] = clientTarget{client: c, bufnr: bufnr}
	}
	return targets
}

// broadcastAndStore sends msg to all clients for bufnr and stores it as the
// replay message for late-connecting clients. Use for refresh_content events
// where late clients should see the most recent content immediately.
//
// The replay store and client snapshot are taken under a single write lock.
// Combining these prevents the window where a new client connects between
// lastMessage storage and the snapshot, which would cause that client to
// receive both the replay and the live broadcast.
func (cm *clientManager) broadcastAndStore(timeout time.Duration, bufnr int, msg wsMessage) (evicted bool, hasClients bool) {
	return cm.sendToBuffer(timeout, bufnr, msg, true)
}

// broadcastOnly sends msg to all clients for bufnr without updating replay
// storage. Use for non-refresh events (e.g. close_page) where late-connecting
// clients should not receive a stale close event.
func (cm *clientManager) broadcastOnly(timeout time.Duration, bufnr int, msg wsMessage) (evicted bool, hasClients bool) {
	return cm.sendToBuffer(timeout, bufnr, msg, false)
}

// marshalMessage marshals a WebSocket message into JSON. Returns the
// marshaled bytes and true on success. On failure, logs the error and
// returns nil, false.
func marshalMessage(logger *slog.Logger, msg wsMessage) ([]byte, bool) {
	data, err := json.Marshal(msg)
	if err != nil {
		logger.Error("failed to marshal ws message", "err", err)
		return nil, false
	}
	return data, true
}

// sendToBuffer is the shared implementation for broadcastAndStore and
// broadcastOnly. It marshals msg, snapshots clients for bufnr under
// the write lock, and writes to all targets. When persist is true, the
// marshaled data is stored as the replay message for late-connecting clients.
func (cm *clientManager) sendToBuffer(timeout time.Duration, bufnr int, msg wsMessage, persist bool) (evicted bool, hasClients bool) {
	data, ok := marshalMessage(cm.logger, msg)
	if !ok {
		return false, false
	}
	cm.mu.Lock()
	if persist {
		cm.lastMessage[bufnr] = data
	}
	targets := cm.snapshotLocked(bufnr)
	cm.mu.Unlock()
	return cm.writeAndCleanup(targets, data, timeout)
}

// broadcastAll sends a message to all connected clients regardless of buffer.
// The client snapshot is taken under a read lock. See writeAndCleanup for
// return value semantics.
//
// Note: broadcastAll intentionally does not update lastMessage. It is only used
// for close_page events where replay to late-connecting clients is
// unnecessary (and harmless if stale -- the next refresh_content broadcast
// from sendToBuffer overwrites lastMessage).
func (cm *clientManager) broadcastAll(timeout time.Duration, msg wsMessage) (evicted bool, hasClients bool) {
	data, ok := marshalMessage(cm.logger, msg)
	if !ok {
		return false, false
	}

	// Snapshot all clients under read lock, then release before writing.
	cm.mu.RLock()
	var targets []clientTarget
	for bufnr, clients := range cm.clients {
		for _, c := range clients {
			targets = append(targets, clientTarget{client: c, bufnr: bufnr})
		}
	}
	cm.mu.RUnlock()

	return cm.writeAndCleanup(targets, data, timeout)
}

// writeAndCleanup writes data to each target and removes any that fail.
// Each client gets an independent write timeout so a single slow browser
// cannot block other clients. Failed clients are removed and their
// connections closed. A single write lock is acquired at the end to evict
// failed clients (if any) and read the final hasClients count.
//
// Between the write loop and the eviction lock, concurrent broadcasts may
// snapshot the same failed client and attempt a second write. The second
// write also fails, producing a duplicate eviction attempt. This is safe:
// removeLocked is a no-op when the client is already removed, and
// coder/websocket's CloseNow is safe to call multiple times.
//
// Return values:
//   - evicted is true when at least one write failed and the client was removed.
//   - hasClients is only meaningful when evicted is true; it reflects the global
//     client count after evictions complete. When evicted is false, hasClients is
//     false (callers should not read it without checking evicted first).
func (cm *clientManager) writeAndCleanup(targets []clientTarget, data []byte, timeout time.Duration) (evicted bool, hasClients bool) {
	var failed []clientTarget
	for _, target := range targets {
		if err := cm.writeToClient(target.client, data, timeout); err != nil {
			cm.logger.Warn("broadcast write failed", "bufnr", target.bufnr, "err", err)
			failed = append(failed, target)
		}
	}

	if len(failed) == 0 {
		return false, false
	}

	cm.mu.Lock()
	for _, target := range failed {
		cm.removeLocked(target.bufnr, target.client)
	}
	hasClients = len(cm.clients) > 0
	cm.mu.Unlock()

	// Close connections outside the lock to avoid holding cm.mu during
	// I/O, matching the pattern in closeAll.
	for _, target := range failed {
		_ = target.client.conn.CloseNow()
		cm.logger.Info("removed failed client", "bufnr", target.bufnr)
	}

	return true, hasClients
}

// writeToClient writes data to a single client with a per-client timeout.
// The client mutex serializes writes as defense-in-depth even though
// coder/websocket internally serializes concurrent Conn.Write calls.
func (cm *clientManager) writeToClient(c *wsClient, data []byte, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return c.conn.Write(ctx, websocket.MessageText, data)
}

// closeAll closes all connected WebSocket clients. It is intended to be called
// during server shutdown. The maps are cleared under the write lock so that
// concurrent operations see a consistent empty state, and then connections are
// closed outside the lock so that the deferred remove calls in handleWebSocket
// goroutines are not blocked waiting for the lock to be released. Those remove
// calls become no-ops because the maps were already cleared.
//
// A graceful Close with StatusGoingAway is attempted first so that browsers
// receive close code 1001 and suppress reconnection (preview.js suppresses
// codes 1000, 1001, and 4001). If the close handshake fails, CloseNow forces
// the connection closed. The deferred CloseNow in handleWebSocket acts as a
// final safety net.
//
// A concurrent addAndReplay that races with closeAll may insert a new client
// into the freshly cleared map. This is harmless: httpServer.Shutdown (called
// immediately after closeAll in Server.Shutdown) drains all in-flight handlers,
// ensuring that any late-arriving client is cleaned up.
func (cm *clientManager) closeAll() {
	cm.mu.Lock()
	var allClients []*wsClient
	for _, clients := range cm.clients {
		allClients = append(allClients, clients...)
	}
	cm.clients = make(map[int][]*wsClient)
	cm.lastMessage = make(map[int][]byte)
	cm.mu.Unlock()

	for _, c := range allClients {
		// Attempt a graceful close so the browser receives code 1001
		// (Going Away) instead of 1006 (abnormal closure). Close has
		// an internal timeout for the close handshake; if it fails,
		// CloseNow forces the connection shut.
		if err := c.conn.Close(websocket.StatusGoingAway, "server shutdown"); err != nil {
			_ = c.conn.CloseNow()
		}
	}
	cm.logger.Info("closed all WebSocket clients", "count", len(allClients))
}

// websocketAcceptOptions builds the WebSocket accept options based on the
// server configuration. OpenToTheWorld restricts origins to localhost variants
// plus the configured OpenIP and the request's Host header; localhost-only
// mode skips verification entirely since network access is already restricted.
// IPv6 brackets require "\\[" escaping because coder/websocket uses
// path.Match against url.Parse(origin).Host, not r.Host.
func (s *Server) websocketAcceptOptions(r *http.Request) *websocket.AcceptOptions {
	opts := &websocket.AcceptOptions{}
	if s.cfg.OpenToTheWorld {
		opts.OriginPatterns = []string{
			"localhost", "localhost:*",
			"127.0.0.1", "127.0.0.1:*",
			"\\[::1\\]", "\\[::1\\]:*",
		}
		if ip := s.cfg.OpenIP; ip != "" {
			if strings.Contains(ip, ":") {
				escaped := "\\[" + ip + "\\]"
				opts.OriginPatterns = append(opts.OriginPatterns, escaped, escaped+":*")
			} else {
				opts.OriginPatterns = append(opts.OriginPatterns, ip, ip+":*")
			}
		}
		// Also allow the request's Host header so users accessing via
		// a hostname (e.g., http://myhost:8080) can establish WebSocket
		// connections without needing to set OpenIP explicitly.
		if host := r.Host; host != "" {
			// Strip port if present so the bare hostname matches too.
			// net.SplitHostPort handles IPv6 bracket notation correctly
			// (e.g. "[::1]:8080" -> "::1"), unlike strings.LastIndex
			// which would corrupt bare IPv6 addresses without brackets.
			bareHost := host
			if hostname, _, err := net.SplitHostPort(bareHost); err == nil {
				bareHost = hostname
			}
			opts.OriginPatterns = append(opts.OriginPatterns, bareHost, bareHost+":*")
		}
	} else {
		opts.InsecureSkipVerify = true
	}
	return opts
}

// handleWebSocket upgrades an HTTP connection to WebSocket and manages
// the client lifecycle. The buffer number is taken from the ?bufnr query param.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	bufnrStr := r.URL.Query().Get("bufnr")
	bufnr, err := strconv.Atoi(bufnrStr)
	if err != nil {
		http.Error(w, "invalid bufnr", http.StatusBadRequest)
		return
	}
	if bufnr < 1 || bufnr > maxBufnr {
		http.Error(w, "bufnr out of range", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, s.websocketAcceptOptions(r))
	if err != nil {
		s.logger.Error("websocket accept failed", "err", err)
		return
	}
	// CloseNow is a no-op after a graceful Close, so this defer acts
	// as a safety net ensuring the connection is always released even
	// if the graceful Close in the lifecycle defer blocks or is skipped.
	defer func() { _ = conn.CloseNow() }()

	client := &wsClient{conn: conn, bufnr: bufnr}
	replay := s.wsClients.addAndReplay(bufnr, client)
	s.logger.Info("ws client connected", "bufnr", bufnr)
	s.fireClientChange(true) // add always means clients exist

	// Replay the most recent broadcast for this buffer so the browser
	// renders content immediately instead of showing "Waiting for content...".
	// If the write fails the connection is likely dead; remove the client
	// now and correct the hasClients state before returning. The lifecycle
	// defer below has not been registered yet, so the manual remove and
	// fireClientChange here are intentional -- they will not be duplicated.
	if replay != nil {
		if err := s.wsClients.writeToClient(client, replay, writeTimeout); err != nil {
			s.logger.Warn("replay write failed, removing client", "bufnr", bufnr, "err", err)
			hasClients := s.wsClients.remove(bufnr, client)
			s.logger.Info("ws client disconnected", "bufnr", bufnr)
			s.fireClientChange(hasClients)
			return
		}
	}

	defer func() {
		hasClients := s.wsClients.remove(bufnr, client)
		// conn.Close is intentionally omitted here. By the time
		// <-ctx.Done() returns (below), CloseRead's background
		// goroutine has already closed the connection, making
		// conn.Close a no-op. The CloseNow defer above provides
		// a safety net for the remaining edge cases.
		s.logger.Info("ws client disconnected", "bufnr", bufnr)
		s.fireClientChange(hasClients)
	}()

	// CloseRead is the idiomatic coder/websocket pattern for write-only
	// connections. It starts a background goroutine that handles control
	// frames (ping/pong/close) and returns a context that is cancelled
	// when the connection closes. Block on that context to keep the
	// handler alive until the client disconnects.
	//
	// context.Background() is used instead of r.Context() because
	// coder/websocket explicitly warns against r.Context() after Accept():
	// the HTTP server may cancel r.Context() unpredictably after hijacking,
	// which would terminate the CloseRead goroutine and prematurely
	// disconnect an active browser client. The shutdown path closes
	// connections directly via closeAll(), which cancels the returned ctx.
	ctx := conn.CloseRead(context.Background())
	<-ctx.Done()
}
