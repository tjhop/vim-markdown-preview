// Package server implements the HTTP and WebSocket server for the
// markdown preview. It serves embedded browser assets and relays
// content from the editor to connected browser clients.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tjhop/vim-markdown-preview/internal/config"
)

// writeTimeout bounds how long we wait for a single WebSocket write
// before giving up. This prevents a stalled browser tab from blocking
// broadcast goroutines indefinitely.
const writeTimeout = 5 * time.Second

// httpHandlerTimeout bounds how long non-WebSocket HTTP handlers may
// take to write their response. Global http.Server WriteTimeout and
// ReadTimeout are not set: coder/websocket does not clear deadlines
// after hijacking the net.Conn, so any server-level read or write
// deadline would kill idle WebSocket connections. Non-WebSocket routes
// are individually wrapped with http.TimeoutHandler instead.
const httpHandlerTimeout = 30 * time.Second

// clientChangeChanCapacity is the buffer capacity for the clientChangeCh channel.
// The buffer absorbs bursts of rapid connect/disconnect events (e.g. a page
// reload) without blocking fireClientChange callers. 16 is generous headroom
// given that client changes arrive at human-interaction speeds.
const clientChangeChanCapacity = 16

// Server manages the HTTP listener, route handlers, and WebSocket clients.
// Create with New, which starts a background goroutine; Shutdown must be
// called even if Start was never called or failed, to prevent a leak.
type Server struct {
	listener   net.Listener
	httpServer *http.Server
	cfg        config.Config
	logger     *slog.Logger

	// wsClients tracks WebSocket connections grouped by buffer number.
	wsClients *clientManager

	// onClientChange is called whenever a WebSocket client connects or
	// disconnects. The argument is true if any clients remain connected.
	// Typically set via SetOnClientChange before Start, but protected
	// by onClientChangeMu for safe updates during the server lifetime.
	onClientChange   func(hasClients bool)
	onClientChangeMu sync.RWMutex

	// clientChangeCh serializes delivery of hasClients values to the
	// onClientChange callback. A single dispatch goroutine reads from
	// this channel, ensuring callbacks execute in order and preventing
	// out-of-order delivery that could leave the editor's state stale.
	clientChangeCh chan bool
	dispatchDone   chan struct{}  // closed in Shutdown to signal the dispatch goroutine to drain and exit
	dispatchWg     sync.WaitGroup // waited on in Shutdown to let the dispatch goroutine finish

	// started is set to true by the first Start call and prevents a
	// second call from creating a new listener, overwriting s.httpServer,
	// and leaking the original net.Listener. Mirrors the shutdownOnce guard.
	started atomic.Bool

	// shutdownOnce ensures Shutdown is idempotent. The CloseAllPages
	// notification handler calls Shutdown directly, and the deferred
	// cleanup in runEditor calls it again when the editor disconnects.
	// Without this guard, the second call produces spurious
	// "http: Server closed" error logs.
	shutdownOnce sync.Once
}

// New creates a Server with the given configuration and logger.
// The cfg value is shallow-copied; map fields in PreviewOptions share
// underlying data with the caller. Callers must not mutate cfg after
// passing it to New. It starts a background dispatch goroutine that
// serializes delivery of client-change callbacks; Shutdown must be
// called to stop it and prevent a goroutine leak.
func New(cfg config.Config, logger *slog.Logger) *Server {
	s := &Server{
		cfg:            cfg,
		logger:         logger,
		wsClients:      newClientManager(logger),
		clientChangeCh: make(chan bool, clientChangeChanCapacity),
		dispatchDone:   make(chan struct{}),
	}

	// Dispatch goroutine serializes callback delivery so that
	// hasClients values are delivered in order. Exits when
	// dispatchDone is closed during Shutdown, after draining
	// any buffered values from clientChangeCh.
	s.dispatchWg.Add(1)
	go func() {
		defer s.dispatchWg.Done()
		for {
			select {
			case hasClients := <-s.clientChangeCh:
				s.dispatchClientChange(hasClients)
			case <-s.dispatchDone:
				// Drain any remaining buffered values so
				// callbacks enqueued before shutdown are
				// still delivered.
				for {
					select {
					case hasClients := <-s.clientChangeCh:
						s.dispatchClientChange(hasClients)
					default:
						return
					}
				}
			}
		}
	}()

	return s
}

// SetOnClientChange registers a callback that fires whenever a WebSocket
// client connects or disconnects. Safe to call at any time, but typically
// called before Start.
func (s *Server) SetOnClientChange(fn func(hasClients bool)) {
	s.onClientChangeMu.Lock()
	s.onClientChange = fn
	s.onClientChangeMu.Unlock()
}

// Start binds the server to a port and begins serving HTTP requests.
// It returns once the listener is ready (serving happens in the background).
// Start may only be called once; a second call returns an error immediately
// without modifying any state.
func (s *Server) Start() error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("server already started")
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	host := "127.0.0.1"
	if s.cfg.OpenToTheWorld {
		if s.cfg.OpenIP != "" {
			host = s.cfg.OpenIP
		} else {
			host = "0.0.0.0"
		}
	}

	addr := fmt.Sprintf("%s:%d", host, s.cfg.Port)

	var err error
	s.listener, err = net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.logger.Info("server listening", "addr", s.listener.Addr().String())

	go func() {
		if err := s.httpServer.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("http serve error", "err", err)
		}
	}()

	return nil
}

// Addr returns the listener address, or nil if Start has not been called.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Shutdown gracefully shuts down the server. WebSocket clients are closed
// first, then the HTTP server is drained so all in-flight handlers
// (including deferred fireClientChange calls) complete before the
// dispatch goroutine is signaled to exit. Safe to call multiple times;
// only the first call performs the shutdown.
func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.shutdownOnce.Do(func() {
		// Ordering matters here:
		//
		// 1. When s.httpServer is non-nil (Start was called): closeAll
		//    closes WebSocket connections, triggering deferred cleanups
		//    in handleWebSocket that call fireClientChange.
		//    httpServer.Shutdown then waits for all in-flight HTTP
		//    handlers to return, guaranteeing that every fireClientChange
		//    send completes while the dispatch goroutine is still
		//    accepting. If dispatchDone were closed before
		//    httpServer.Shutdown, deferred fireClientChange calls from
		//    in-flight handlers could race against the closed channel
		//    and silently drop values.
		//
		// 2. dispatchDone and dispatchWg always run, even when
		//    s.httpServer is nil (Shutdown called before Start). Without
		//    this, the dispatch goroutine started in New would leak forever.
		if s.httpServer != nil {
			s.logger.Info("shutting down server")
			s.wsClients.closeAll()
			err = s.httpServer.Shutdown(ctx)
		}
		close(s.dispatchDone)
		s.dispatchWg.Wait()
	})
	return err
}

// dispatchClientChange reads the current callback under the read lock
// and invokes it with the given value. Called exclusively from the
// dispatch goroutine started in New.
func (s *Server) dispatchClientChange(hasClients bool) {
	s.onClientChangeMu.RLock()
	cb := s.onClientChange
	s.onClientChangeMu.RUnlock()
	if cb != nil {
		cb(hasClients)
	}
}

// fireClientChange invokes the onClientChange callback via the dispatch
// goroutine started in New so the caller is not blocked by the editor
// RPC round-trip. Delivery is serialized through that goroutine to preserve
// ordering: without this, concurrent goroutines could deliver hasClients
// values out of order, leaving the editor's state variable stale.
// The hasClients value should be computed atomically with the mutation
// that triggered the change (within the same lock scope) to avoid
// TOCTOU races.
func (s *Server) fireClientChange(hasClients bool) {
	select {
	case s.clientChangeCh <- hasClients:
	case <-s.dispatchDone:
	}
}

// BroadcastToBuffer sends a WebSocket message to all clients for the given
// buffer and stores it for replay to late-connecting clients. Use for
// refresh-content events where browsers should receive up-to-date content
// immediately on connect.
func (s *Server) BroadcastToBuffer(bufnr int, event string, data any) {
	msg := wsMessage{Event: event, Data: data}
	evicted, hasClients := s.wsClients.broadcastAndStore(writeTimeout, bufnr, msg)
	if evicted {
		s.fireClientChange(hasClients)
	}
}

// BroadcastTransientToBuffer sends a WebSocket message to all clients for the
// given buffer without updating replay storage. Use for lifecycle events
// (e.g. close_page) where late-connecting clients should not receive a stale
// close notification on connect.
func (s *Server) BroadcastTransientToBuffer(bufnr int, event string, data any) {
	msg := wsMessage{Event: event, Data: data}
	evicted, hasClients := s.wsClients.broadcastOnly(writeTimeout, bufnr, msg)
	if evicted {
		s.fireClientChange(hasClients)
	}
}

// BroadcastAll sends a WebSocket message to all connected clients.
// Unlike BroadcastToBuffer, BroadcastAll does not persist the message
// for replay to late-connecting clients.
func (s *Server) BroadcastAll(event string, data any) {
	evicted, hasClients := s.wsClients.broadcastAll(writeTimeout, wsMessage{
		Event: event,
		Data:  data,
	})
	if evicted {
		s.fireClientChange(hasClients)
	}
}
