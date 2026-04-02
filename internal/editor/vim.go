package editor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tjhop/vim-markdown-preview/internal/config"
)

// requestTimeout bounds how long sendRequest waits for a response from
// the editor before giving up. This prevents indefinite hangs when the
// editor process stalls or the connection is half-open.
const requestTimeout = 30 * time.Second

// notifyChanCapacity is the buffer size for the VimClient content
// notification channel. Content refresh notifications are delivered via
// a non-blocking send; when the buffer is full, excess notifications are
// dropped rather than blocking the read loop. 64 is generous headroom
// given that notifications arrive at human-interaction speeds (cursor
// moves, edits).
const notifyChanCapacity = 64

// lifecycleChanCapacity is the buffer size for the VimClient lifecycle
// event channel. Lifecycle events (close_page, open_browser) use a
// separate channel from content refreshes so that a full content
// notification queue does not delay them. The send is still non-blocking
// with a drop path, but because these events arrive at process-lifetime
// frequency the buffer never fills under normal use.
//
// close_all_pages is NOT routed through this channel; it has its own
// dedicated channel (closeAllCh) to guarantee delivery.
const lifecycleChanCapacity = 8

// errUnsafeVarName is returned by SetVar when the variable name contains
// characters that are not safe for VimScript interpolation.
var errUnsafeVarName = errors.New("unsafe variable name")

// ErrEditorClosed is returned by sendRequest when the editor connection
// has been shut down (c.done is closed) before a response arrives.
// Exported so callers (e.g. the RefreshContent notification handler in main)
// can distinguish a clean shutdown from an unexpected RPC failure.
var ErrEditorClosed = errors.New("editor connection closed")

// validVarName matches safe Vim global variable name characters.
// Names must be non-empty and contain only alphanumeric characters or underscores.
var validVarName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// VimClient implements the Editor interface for Vim 8+ using the JSON
// channel protocol. Vim sends and receives newline-delimited JSON arrays
// over stdin/stdout.
//
// Vim's JSON channel protocol supports two transport modes: "channel"
// (TCP socket) and "job" (stdin/stdout of a child process). This
// implementation uses job mode -- the Go binary is launched by Vim via
// job_start() and communicates over the job's stdin/stdout pipes. The
// JSON framing and message semantics are identical in both modes; only
// the underlying transport differs (see :help channel-use).
//
// Protocol format (see :help channel-commands):
//   - Notification (Vim -> channel): [0, ["event_name", {data}]]
//   - Request (channel -> Vim):      ["call", "funcname", [{args}], -msgid]
//   - Request (channel -> Vim):      ["expr", "expression", -msgid]
//   - Response (Vim -> channel):     [-msgid, result]
//
// Negative message IDs avoid collisions with Vim-initiated IDs.
type VimClient struct {
	reader   *json.Decoder
	writer   io.Writer
	writerMu sync.Mutex   // protects concurrent writes to writer
	msgID    atomic.Int64 // monotonic request counter; negative values used as message IDs

	pending   map[int]chan json.RawMessage
	pendingMu sync.Mutex

	// done is closed when Serve() exits, unblocking any goroutines
	// waiting on a pending response channel.
	done chan struct{}

	handler NotificationHandler
	logger  *slog.Logger
}

// NewVimClient creates a VimClient connected over the given streams.
func NewVimClient(r io.Reader, w io.Writer, logger *slog.Logger) *VimClient {
	return &VimClient{
		reader:  json.NewDecoder(r),
		writer:  w,
		pending: make(map[int]chan json.RawMessage),
		done:    make(chan struct{}),
		logger:  logger,
	}
}

// OnNotification registers the callback handler for editor events.
func (c *VimClient) OnNotification(handler NotificationHandler) {
	c.handler = handler
}

// vimNotification holds a Vim notification's event name and parameter map,
// produced by the read loop and consumed by the dispatch goroutines.
type vimNotification struct {
	event  string
	params map[string]any
}

// dispatchWorkers groups the channels and stop function returned by
// startDispatchWorkers, keeping the Serve call site clean as the
// dispatch topology grows.
type dispatchWorkers struct {
	notifyCh    chan vimNotification
	lifecycleCh chan vimNotification
	closeAllCh  chan struct{}
	stop        func()
}

// startDispatchWorkers creates the three notification dispatch channels,
// launches their consumer goroutines, and returns a dispatchWorkers.
// The stop function closes all channels (signalling goroutines to drain)
// and blocks until they exit. Callers must close c.done before invoking
// stop so that any sendRequest callers blocked on a response are unblocked
// before the goroutines finish draining.
func (c *VimClient) startDispatchWorkers() dispatchWorkers {
	dw := dispatchWorkers{
		notifyCh:    make(chan vimNotification, notifyChanCapacity),
		lifecycleCh: make(chan vimNotification, lifecycleChanCapacity),
		closeAllCh:  make(chan struct{}, 1),
	}

	var wg sync.WaitGroup

	// startWorker launches a goroutine that drains ch and dispatches each
	// notification. The c.done guard skips dispatch for notifications
	// queued before close(c.done) ran (LIFO defer order: close(c.done)
	// runs before stop), avoiding spurious "editor connection closed"
	// errors from handlers that make round-trip RPC calls.
	startWorker := func(ch <-chan vimNotification) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range ch {
				select {
				case <-c.done:
					return
				default:
				}
				c.dispatchNotification(n.event, n.params)
			}
		}()
	}
	startWorker(dw.notifyCh)
	startWorker(dw.lifecycleCh)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for range dw.closeAllCh {
			if c.handler.CloseAllPages != nil {
				c.handler.CloseAllPages()
			}
		}
	}()

	dw.stop = func() {
		close(dw.notifyCh)
		close(dw.lifecycleCh)
		close(dw.closeAllCh)
		wg.Wait()
	}
	return dw
}

// Serve runs the read loop, dispatching incoming messages to either pending
// response channels or notification handlers. Blocks until EOF or error.
//
// Notifications are dispatched via dedicated goroutines to avoid deadlock:
// the RefreshContent handler makes round-trip RPC calls back to Vim, which
// require the read loop to be free to receive the response. Without this
// decoupling, the read loop would block inside the handler waiting for a
// response that only the read loop itself can deliver.
//
// Three channels are used:
//   - notifyCh: droppable content refreshes (non-blocking send; excess dropped)
//   - lifecycleCh: close_page and open_browser (non-blocking with drop path)
//   - closeAllCh: close_all_pages only (capacity 1, guaranteed delivery)
//
// close_all_pages gets a dedicated channel because it signals process
// shutdown. A dropped close_all_pages would leave the server running after
// Vim closes. The channel's capacity-1 buffer ensures exactly one signal is
// queued; duplicate sends hit the default branch (idempotent for shutdown).
func (c *VimClient) Serve() error {
	workers := c.startDispatchWorkers()

	// Shutdown order (LIFO defers):
	// 1. close(c.done) runs first, unblocking any pending sendRequest calls.
	// 2. workers.stop runs second, closing dispatch channels and waiting for
	//    goroutines to drain.
	defer workers.stop()
	defer close(c.done)

	for {
		var msg json.RawMessage
		if err := c.reader.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode vim message: %w", err)
		}

		// All messages are JSON arrays: [msgid, payload]
		var arr []json.RawMessage
		if err := json.Unmarshal(msg, &arr); err != nil {
			c.logger.Warn("malformed message", "err", err)
			continue
		}
		if len(arr) < 2 {
			c.logger.Warn("message too short", "msg_len", len(arr))
			continue
		}

		var msgID int
		if err := json.Unmarshal(arr[0], &msgID); err != nil {
			c.logger.Warn("bad msgid", "err", err)
			continue
		}

		if msgID == 0 {
			// Parse the notification payload once here instead
			// of deferring it to the dispatch goroutine.
			parsed, ok := c.parseNotification(arr[1])
			if !ok {
				continue
			}
			switch parsed.event {
			case EventCloseAllPages:
				// Dedicated channel guarantees delivery: a dropped
				// close_all_pages would leave the server running after
				// Vim exits. Capacity 1 means a duplicate signal
				// (if any) is silently discarded -- idempotent for shutdown.
				select {
				case workers.closeAllCh <- struct{}{}:
				default:
					// Signal already queued; duplicate is safe to drop.
				}
			case EventClosePage, EventOpenBrowser:
				// Lifecycle events use a dedicated channel so
				// a full content queue cannot delay them. These
				// handlers are fast (no RPC round trips) and
				// arrive at process-lifetime frequency.
				select {
				case workers.lifecycleCh <- parsed:
				default:
					c.logger.Error("lifecycle notification dropped",
						"event", parsed.event)
				}
			default:
				// Content refreshes are droppable: they
				// self-replace on the next cursor move.
				select {
				case workers.notifyCh <- parsed:
				default:
					c.logger.Warn("notification dropped, handler backlog full",
						"event", parsed.event)
				}
			}
		} else {
			// Response to a pending request.
			c.pendingMu.Lock()
			ch, ok := c.pending[msgID]
			if ok {
				delete(c.pending, msgID)
			}
			c.pendingMu.Unlock()

			if ok {
				ch <- arr[1]
			} else {
				c.logger.Warn("unexpected response", "msgid", msgID)
			}
		}
	}
}

// ChannelID returns the Vim channel ID for this connection. For Vim 8,
// this always returns 0 because the JSON channel protocol has no numeric
// channel ID -- the real channel handle is s:mkdp_channel_id (the channel
// object returned by job_getchannel). The return value of 0 serves as an
// existence signal: the Go binary sets g:mkdp_channel_id to trigger the
// VimScript startup polling loop, but the actual value is never used as
// a channel ID by Vim 8.
func (c *VimClient) ChannelID() int {
	return 0
}

// SetVar sets a global (g:) variable in Vim.
//
// Uses extend(g:, ...) with json_decode() to safely set the value without
// interpolating raw content into VimScript commands. Variable names are
// validated to contain only safe characters.
func (c *VimClient) SetVar(name string, value any) error {
	if !validVarName.MatchString(name) {
		return fmt.Errorf("%w: %q", errUnsafeVarName, name)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal var %s: %w", name, err)
	}
	// Escape single quotes for VimScript single-quoted string embedding.
	escaped := strings.ReplaceAll(string(encoded), "'", "''")
	_, err = c.callExpr(fmt.Sprintf("extend(g:, {'%s': json_decode('%s')})", name, escaped))
	return err
}

// vimConfigDecoder wraps the JSON map returned by mkdp#rpc#gather_config()
// and provides typed accessors for each config field. Each method returns
// (value, true) when the key is present and decodes successfully, or
// (zero, false) when the key is absent or decoding fails. Decode errors
// are logged so the distinction from absent keys is observable.
type vimConfigDecoder struct {
	m      map[string]json.RawMessage
	logger *slog.Logger
}

// decodeInt returns the integer value for key, or (0, false) if the key is
// absent or cannot be unmarshaled as an int.
func (d *vimConfigDecoder) decodeInt(key string) (int, bool) {
	v, ok := d.m[key]
	if !ok {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		d.logger.Warn("config decode failed", "key", key, "err", err)
		return 0, false
	}
	return n, true
}

// decodeStr returns the string value for key, or ("", false) if the key is
// absent or cannot be unmarshaled as a string.
func (d *vimConfigDecoder) decodeStr(key string) (string, bool) {
	v, ok := d.m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		d.logger.Warn("config decode failed", "key", key, "err", err)
		return "", false
	}
	return s, true
}

// decodePreviewOptions returns parsed PreviewOptions for key, or
// (PreviewOptions{}, false) if the key is absent, decodes to nil, or cannot
// be unmarshaled as a JSON object.
func (d *vimConfigDecoder) decodePreviewOptions(key string) (config.PreviewOptions, bool) {
	v, ok := d.m[key]
	if !ok {
		return config.PreviewOptions{}, false
	}
	var opts map[string]any
	if err := json.Unmarshal(v, &opts); err != nil {
		d.logger.Warn("config decode failed", "key", key, "err", err)
		return config.PreviewOptions{}, false
	}
	if opts == nil {
		return config.PreviewOptions{}, false
	}
	return mapToPreviewOptions(opts, d.logger), true
}

// decodePort returns the port value for key, or (0, false) if the key is
// absent, cannot be unmarshaled, or cannot be converted to a valid port number
// by parsePort.
func (d *vimConfigDecoder) decodePort(key string) (int, bool) {
	v, ok := d.m[key]
	if !ok {
		return 0, false
	}
	var port any
	if err := json.Unmarshal(v, &port); err != nil {
		d.logger.Warn("config decode failed", "key", key, "err", err)
		return 0, false
	}
	return parsePort(port)
}

// FetchConfig reads all g:mkdp_* variables from Vim by calling the
// VimScript helper mkdp#rpc#gather_config(), which batches all variable
// reads into a single round-trip.
func (c *VimClient) FetchConfig() (*config.Config, error) {
	raw, err := c.callFunc("mkdp#rpc#gather_config", []any{})
	if err != nil {
		return nil, fmt.Errorf("fetch config: %w", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	d := &vimConfigDecoder{m: m, logger: c.logger}
	cfg := config.DefaultConfig()

	if n, ok := d.decodeInt("open_to_the_world"); ok {
		cfg.OpenToTheWorld = n != 0
	}
	if s, ok := d.decodeStr("open_ip"); ok {
		cfg.OpenIP = s
	}
	if s, ok := d.decodeStr("browser"); ok {
		cfg.Browser = s
	}
	if s, ok := d.decodeStr("markdown_css"); ok {
		cfg.MarkdownCSS = s
	}
	if s, ok := d.decodeStr("highlight_css"); ok {
		cfg.HighlightCSS = s
	}
	if s, ok := d.decodeStr("images_path"); ok {
		cfg.ImagesPath = s
	}
	// Only override the default ("${name}" template) when a non-empty
	// page_title is explicitly configured. An empty string from Vim means
	// the variable was not set by the user; preserving the default avoids
	// a blank browser tab title.
	if s, ok := d.decodeStr("page_title"); ok && s != "" {
		cfg.PageTitle = s
	}
	if s, ok := d.decodeStr("theme"); ok && s != "" {
		cfg.Theme = s
	}
	if opts, ok := d.decodePreviewOptions("preview_options"); ok {
		cfg.PreviewOptions = opts
	}
	if p, ok := d.decodePort("port"); ok {
		cfg.Port = p
	}

	return &cfg, nil
}

// FetchBufferData gathers buffer content and viewport state by calling
// the VimScript helper mkdp#rpc#gather_data(bufnr), which batches all
// data collection into a single round-trip.
func (c *VimClient) FetchBufferData(bufnr int) (*config.RefreshData, error) {
	raw, err := c.callFunc("mkdp#rpc#gather_data", []any{bufnr})
	if err != nil {
		return nil, fmt.Errorf("fetch buffer data for %d: %w", bufnr, err)
	}

	var data config.RefreshData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode buffer data: %w", err)
	}

	return &data, nil
}

// callFunc calls a VimScript function via the Vim 8 JSON channel protocol.
// Message format: ["call", "funcname", [args], -msgid].
// See :help channel-commands for the wire format Vim expects.
func (c *VimClient) callFunc(funcName string, args []any) (json.RawMessage, error) {
	return c.sendRequest(func(id int) []any {
		return []any{"call", funcName, args, id}
	})
}

// callExpr evaluates a VimScript expression via the "expr" command.
// Message format: ["expr", "expression", -msgid].
// See :help channel-commands for the wire format Vim expects.
func (c *VimClient) callExpr(expr string) (json.RawMessage, error) {
	return c.sendRequest(func(id int) []any {
		return []any{"expr", expr, id}
	})
}

// sendRequest handles the shared request/response plumbing for both callFunc
// and callExpr. It allocates a message ID, registers a pending response
// channel, sends the message (built by makeMsg with the assigned ID), and
// waits for either the response or connection close.
func (c *VimClient) sendRequest(makeMsg func(id int) []any) (json.RawMessage, error) {
	// Add returns the new value; negate to match the protocol convention
	// of using negative IDs for channel-initiated requests.
	id := int(-c.msgID.Add(1))

	ch := make(chan json.RawMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	// removePending cleans up the pending request entry. It is called on
	// the send-failure, timeout, and connection-close paths. On the success
	// path the Serve loop removes the entry itself before delivering the
	// response to ch. If removePending races with a late Serve-loop
	// delivery, the Serve loop sees ok=false and logs "unexpected response";
	// ch (capacity 1) is GC'd when sendRequest returns.
	removePending := func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}

	if err := c.send(makeMsg(id)); err != nil {
		removePending()
		return nil, err
	}

	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()

	select {
	case result := <-ch:
		return result, nil
	case <-c.done:
		// c.done may fire at the same instant a response arrives in ch.
		// Go's select picks uniformly among ready cases, so drain ch
		// before declaring the connection lost.
		select {
		case result := <-ch:
			return result, nil
		default:
		}
		removePending()
		return nil, ErrEditorClosed
	case <-timer.C:
		// Like the c.done case above, a response may have arrived at
		// the same instant the timer fired. Drain ch before declaring
		// a timeout.
		select {
		case result := <-ch:
			return result, nil
		default:
		}
		removePending()
		return nil, fmt.Errorf("request timed out after %s", requestTimeout)
	}
}

// send encodes and writes a JSON message to the Vim channel.
func (c *VimClient) send(msg any) error {
	// Marshal and newline-append outside the lock: both operate on a
	// local slice and need no write serialization. The lock covers only
	// the Write call to prevent interleaved output to c.writer.
	marshaled, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	// Vim expects newline-delimited JSON. Allocate a new slice rather
	// than appending to the marshal output, which has no capacity
	// guarantee from the standard library.
	data := make([]byte, len(marshaled)+1)
	copy(data, marshaled)
	data[len(marshaled)] = '\n'

	c.writerMu.Lock()
	defer c.writerMu.Unlock()

	// io.Writer implementations must return a non-nil error for short
	// writes (n < len(p)), making a separate short-write check redundant.
	_, err = c.writer.Write(data)
	if err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// parseNotification extracts the event name and optional data map from a
// raw Vim notification payload (format: ["event_name", {data}]). Returns
// false if the payload is malformed.
func (c *VimClient) parseNotification(raw json.RawMessage) (vimNotification, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		c.logger.Warn("bad notification payload", "err", err)
		return vimNotification{}, false
	}
	if len(arr) < 1 {
		return vimNotification{}, false
	}

	var event string
	if err := json.Unmarshal(arr[0], &event); err != nil {
		c.logger.Warn("bad event name", "err", err)
		return vimNotification{}, false
	}

	var params map[string]any
	if len(arr) > 1 {
		if err := json.Unmarshal(arr[1], &params); err != nil {
			c.logger.Warn("bad notification data", "event", event, "err", err)
			return vimNotification{}, false
		}
	}

	return vimNotification{event: event, params: params}, true
}

// dispatchNotification routes a pre-parsed notification to the appropriate
// handler callback.
func (c *VimClient) dispatchNotification(event string, params map[string]any) {
	switch event {
	case EventRefreshContent:
		c.dispatchFromMap(event, c.handler.RefreshContent, params)
	case EventClosePage:
		c.dispatchFromMap(event, c.handler.ClosePage, params)
	// EventCloseAllPages is not handled here: Serve() routes it to
	// the dedicated closeAllCh channel for guaranteed delivery.
	case EventOpenBrowser:
		c.dispatchFromMap(event, c.handler.OpenBrowser, params)
	default:
		c.logger.Debug("unknown notification", "event", event)
	}
}

// dispatchFromMap is a generic dispatcher for notifications that carry a bufnr
// in their params map. It nil-checks the callback, extracts the bufnr, and
// invokes the callback. This avoids repeating the same pattern across
// RefreshContent, ClosePage, and OpenBrowser.
// The Neovim equivalent is NeovimClient.dispatchFromArgs in neovim.go.
func (c *VimClient) dispatchFromMap(event string, fn func(int), params map[string]any) {
	if fn == nil {
		return
	}
	bufnr, err := extractBufnrFromMap(params)
	if err != nil {
		c.logger.Warn(event+": bad args", "err", err)
		return
	}
	fn(bufnr)
}
