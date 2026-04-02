package editor

import (
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neovim/go-client/nvim"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// skipIfNoNvim skips the calling test if nvim is not available on the PATH.
func skipIfNoNvim(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not found in PATH, skipping integration test")
	}
}

// newTestNeovim spawns a headless embedded Neovim child process for
// integration testing. The process is cleaned up when the test finishes.
// Serve is NOT started -- callers that need the event loop must start it
// themselves (some tests need to register handlers before Serve).
func newTestNeovim(t *testing.T) *nvim.Nvim {
	t.Helper()
	skipIfNoNvim(t)

	v, err := nvim.NewChildProcess(
		nvim.ChildProcessCommand("nvim"),
		nvim.ChildProcessArgs("--embed", "--headless", "--clean"),
		nvim.ChildProcessServe(false),
	)
	if err != nil {
		t.Fatalf("failed to create nvim child process: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

// ---------------------------------------------------------------------------
// Category 1: Notification handler tests (no real nvim required)
// ---------------------------------------------------------------------------

// TestNeovimHandleBufnrDispatchers verifies that the three single-bufnr
// notification handlers (refresh_content, close_page, open_browser) each
// extract the bufnr from their args map and dispatch it to the registered
// callback exactly once.
//
// Dispatch path: Neovim sends rpcnotify(chan, event, {'bufnr': N}). The
// go-client library decodes the notification args via msgpack into a []any
// where args[0] is the decoded dict -- a map[string]any with int64 values
// for integers (msgpack fixed integers are decoded as int64 by the library).
// The handler functions (handleRefreshContent, etc.) receive this as a
// variadic ...any, so args[0] is the map. The test replicates this exactly:
// tt.invoke(c, map[string]any{"bufnr": tt.bufnr}) passes the map as args[0]
// with int64 for the bufnr value, matching go-client's msgpack decoder output.
func TestNeovimHandleBufnrDispatchers(t *testing.T) {
	tests := []struct {
		name        string
		bufnr       int64 // int64 matches go-client's msgpack decoder output for integer values
		makeHandler func(fn func(int)) NotificationHandler
		invoke      func(c *NeovimClient, args ...any)
	}{
		{
			name:  "refresh_content",
			bufnr: 5,
			makeHandler: func(fn func(int)) NotificationHandler {
				return NotificationHandler{RefreshContent: fn}
			},
			invoke: func(c *NeovimClient, args ...any) { c.handleRefreshContent(args...) },
		},
		{
			name:  "close_page",
			bufnr: 3,
			makeHandler: func(fn func(int)) NotificationHandler {
				return NotificationHandler{ClosePage: fn}
			},
			invoke: func(c *NeovimClient, args ...any) { c.handleClosePage(args...) },
		},
		{
			name:  "open_browser",
			bufnr: 7,
			makeHandler: func(fn func(int)) NotificationHandler {
				return NotificationHandler{OpenBrowser: fn}
			},
			invoke: func(c *NeovimClient, args ...any) { c.handleOpenBrowser(args...) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &NeovimClient{logger: discardLogger()}

			var called atomic.Int32
			var receivedBufnr atomic.Int32
			var spurious atomic.Int32

			// Build a handler that populates the target callback with
			// the real counter, and all other bufnr-based callbacks
			// with a spurious counter so cross-dispatch bugs are
			// detected rather than masked by nil no-ops.
			h := tt.makeHandler(func(bufnr int) {
				receivedBufnr.Store(int32(bufnr))
				called.Add(1)
			})
			if h.RefreshContent == nil {
				h.RefreshContent = func(int) { spurious.Add(1) }
			}
			if h.ClosePage == nil {
				h.ClosePage = func(int) { spurious.Add(1) }
			}
			if h.OpenBrowser == nil {
				h.OpenBrowser = func(int) { spurious.Add(1) }
			}
			c.OnNotification(h)

			tt.invoke(c, map[string]any{"bufnr": tt.bufnr})

			if called.Load() != 1 {
				t.Fatalf("expected handler to be called once, got %d", called.Load())
			}
			if got := receivedBufnr.Load(); got != int32(tt.bufnr) {
				t.Errorf("expected bufnr=%d, got %d", tt.bufnr, got)
			}
			if s := spurious.Load(); s != 0 {
				t.Errorf("expected no spurious handler calls, got %d", s)
			}
		})
	}
}

// TestNeovimHandleCloseAllPages verifies that handleCloseAllPages dispatches
// to the registered CloseAllPages callback (no bufnr argument).
func TestNeovimHandleCloseAllPages(t *testing.T) {
	c := &NeovimClient{logger: discardLogger()}

	var called atomic.Int32

	c.OnNotification(NotificationHandler{
		CloseAllPages: func() {
			called.Add(1)
		},
	})

	c.handleCloseAllPages()

	if called.Load() != 1 {
		t.Fatalf("expected handler to be called once, got %d", called.Load())
	}
}

// TestNeovimHandlerNilCallbacks verifies that calling each handler method
// on a zero-value NeovimClient (all callbacks nil) does not panic and does
// not invoke any callback. The handlers check for nil callbacks before
// invoking them, and this test exercises that guard with well-formed
// arguments.
func TestNeovimHandlerNilCallbacks(t *testing.T) {
	// c is a zero-value NeovimClient: all callbacks are nil.
	c := &NeovimClient{logger: discardLogger()}

	// Track whether any callback fires via a shared counter. If the
	// nil guard is accidentally removed, the test will panic (nil
	// function call) rather than silently pass. The counter catches
	// the subtler case where a non-nil default is introduced.
	var called atomic.Int32

	// Exercise the nil-callback client: should not panic and should
	// not invoke any callback.
	t.Run("refresh_content", func(t *testing.T) {
		c.handleRefreshContent(map[string]any{"bufnr": int64(1)})
	})
	t.Run("close_page", func(t *testing.T) {
		c.handleClosePage(map[string]any{"bufnr": int64(1)})
	})
	t.Run("close_all_pages", func(t *testing.T) {
		c.handleCloseAllPages()
	})
	t.Run("open_browser", func(t *testing.T) {
		c.handleOpenBrowser(map[string]any{"bufnr": int64(1)})
	})

	// Now set real callbacks and verify dispatch works, proving the
	// nil-callback path above is meaningfully different.
	c.handler = NotificationHandler{
		RefreshContent: func(int) { called.Add(1) },
		ClosePage:      func(int) { called.Add(1) },
		CloseAllPages:  func() { called.Add(1) },
		OpenBrowser:    func(int) { called.Add(1) },
	}
	c.handleRefreshContent(map[string]any{"bufnr": int64(1)})
	c.handleClosePage(map[string]any{"bufnr": int64(1)})
	c.handleCloseAllPages()
	c.handleOpenBrowser(map[string]any{"bufnr": int64(1)})

	if got := called.Load(); got != 4 {
		t.Errorf("expected 4 callback invocations with non-nil handlers, got %d", got)
	}
}

// TestNeovimHandlerBadArgs verifies that handlers with malformed arguments
// do not panic and do not invoke the callback.
func TestNeovimHandlerBadArgs(t *testing.T) {
	tests := []struct {
		name     string
		handler  func(c *NeovimClient, args ...any)
		wantCall bool // true if the callback fires regardless of args
	}{
		{
			name:    "refresh_content",
			handler: func(c *NeovimClient, args ...any) { c.handleRefreshContent(args...) },
		},
		{
			name:    "close_page",
			handler: func(c *NeovimClient, args ...any) { c.handleClosePage(args...) },
		},
		{
			name:    "open_browser",
			handler: func(c *NeovimClient, args ...any) { c.handleOpenBrowser(args...) },
		},
		{
			// close_all_pages ignores its args entirely, so the
			// callback always fires regardless of what is passed.
			name:     "close_all_pages",
			handler:  func(c *NeovimClient, args ...any) { c.handleCloseAllPages(args...) },
			wantCall: true,
		},
	}

	badArgSets := []struct {
		desc string
		args []any
	}{
		{desc: "no args", args: nil},
		{desc: "empty args", args: []any{}},
		{desc: "missing bufnr key", args: []any{map[string]any{"other": 1}}},
		{desc: "wrong type for bufnr", args: []any{map[string]any{"bufnr": "not_a_number"}}},
		{desc: "non-map first arg", args: []any{"not a map"}},
	}

	for _, tt := range tests {
		if tt.wantCall {
			// Handler ignores args entirely and always fires. Running
			// bad-arg subtests would only re-verify the always-fires
			// behavior without covering argument validation. The
			// standalone TestNeovimHandleCloseAllPages test covers this.
			continue
		}
		for _, ba := range badArgSets {
			t.Run(tt.name+"/"+ba.desc, func(t *testing.T) {
				var refreshCalled, closeCalled, openCalled, closeAllCalled atomic.Int32
				c := &NeovimClient{logger: discardLogger()}
				c.OnNotification(NotificationHandler{
					RefreshContent: func(_ int) { refreshCalled.Add(1) },
					ClosePage:      func(_ int) { closeCalled.Add(1) },
					OpenBrowser:    func(_ int) { openCalled.Add(1) },
					CloseAllPages:  func() { closeAllCalled.Add(1) },
				})

				// Should not panic.
				tt.handler(c, ba.args...)

				// Build a map of which handler was actually invoked.
				counts := map[string]int32{
					"refresh_content": refreshCalled.Load(),
					"close_page":      closeCalled.Load(),
					"open_browser":    openCalled.Load(),
					"close_all_pages": closeAllCalled.Load(),
				}

				// tt.wantCall entries are skipped by the outer loop's
				// continue, so only the no-call assertion applies here.
				if counts[tt.name] != 0 {
					t.Errorf("%s callback should not have been called with %s", tt.name, ba.desc)
				}

				// Verify no other handler was called.
				for name, count := range counts {
					if name != tt.name && count != 0 {
						t.Errorf("unexpected call to %s handler (count=%d) when testing %s", name, count, tt.name)
					}
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Category 2: Integration tests (require real nvim child process)
// ---------------------------------------------------------------------------

// startServe starts the Serve loop for a NeovimClient and registers a cleanup
// that waits for it to exit after the underlying nvim process is closed. This
// must be called BEFORE newTestNeovim's cleanup so that the Serve-wait runs
// AFTER the Close cleanup (t.Cleanup is LIFO).
func startServe(t *testing.T, client *NeovimClient) {
	t.Helper()

	errCh := make(chan error, 1)
	t.Cleanup(func() {
		if t.Skipped() {
			return
		}
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Serve exited with unexpected error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not exit within 5 seconds after Close")
		}
	})

	go func() { errCh <- client.Serve() }()
}

// TestNeovimSetVar verifies that SetVar writes a variable that can be read
// back from the Neovim instance.
func TestNeovimSetVar(t *testing.T) {
	v := newTestNeovim(t)
	client := &NeovimClient{nvim: v, logger: discardLogger()}
	startServe(t, client)

	if err := client.SetVar("test_var", 42); err != nil {
		t.Fatalf("SetVar failed: %v", err)
	}

	var result int
	if err := v.Var("test_var", &result); err != nil {
		t.Fatalf("failed to read back variable: %v", err)
	}
	if result != 42 {
		t.Errorf("expected test_var=42, got %d", result)
	}

	// Verify string values too.
	if err := client.SetVar("test_str", "hello"); err != nil {
		t.Fatalf("SetVar (string) failed: %v", err)
	}

	var strResult string
	if err := v.Var("test_str", &strResult); err != nil {
		t.Fatalf("failed to read back string variable: %v", err)
	}
	if strResult != "hello" {
		t.Errorf("expected test_str=%q, got %q", "hello", strResult)
	}

	// NeovimClient validates names against the same safe-character regex as
	// VimClient. This subtest confirms that valid underscore names are
	// accepted and round-trip correctly through the RPC.
	t.Run("underscore variable name", func(t *testing.T) {
		if err := client.SetVar("my_test_var", "value"); err != nil {
			t.Fatalf("SetVar with underscore name failed: %v", err)
		}

		var underscoreResult string
		if err := v.Var("my_test_var", &underscoreResult); err != nil {
			t.Fatalf("failed to read back underscore variable: %v", err)
		}
		if underscoreResult != "value" {
			t.Errorf("expected my_test_var=%q, got %q", "value", underscoreResult)
		}
	})

	// Validation fires before any RPC call, so this subtest works with
	// the real client -- no I/O is performed for rejected names.
	t.Run("invalid name rejected", func(t *testing.T) {
		badNames := []string{"", "foo bar", "foo;evil", "foo.bar", "foo-bar"}
		for _, name := range badNames {
			err := client.SetVar(name, 1)
			if err == nil {
				t.Errorf("expected error for name %q, got nil", name)
				continue
			}
			if !errors.Is(err, errUnsafeVarName) {
				t.Errorf("expected errUnsafeVarName for %q, got: %v", name, err)
			}
		}
	})
}

// TestNeovimChannelID verifies that ChannelID returns a positive integer
// and caches the result across calls.
func TestNeovimChannelID(t *testing.T) {
	v := newTestNeovim(t)
	client := &NeovimClient{nvim: v, logger: discardLogger()}
	startServe(t, client)

	id := client.ChannelID()
	if id <= 0 {
		t.Fatalf("expected positive channel ID, got %d", id)
	}

	// Second call should return the same value (caching).
	id2 := client.ChannelID()
	if id2 != id {
		t.Errorf("expected cached channel ID %d, got %d", id, id2)
	}
}

// TestNeovimFetchConfig verifies that FetchConfig correctly reads all
// g:mkdp_* variables from Neovim when they are explicitly set.
func TestNeovimFetchConfig(t *testing.T) {
	v := newTestNeovim(t)
	client := &NeovimClient{nvim: v, logger: discardLogger()}
	startServe(t, client)

	// Set all the config variables that FetchConfig reads.
	vars := map[string]any{
		"mkdp_open_to_the_world": 1,
		"mkdp_open_ip":           "192.168.1.1",
		"mkdp_browser":           "firefox",
		"mkdp_markdown_css":      "/path/to/css",
		"mkdp_highlight_css":     "/path/to/highlight.css",
		"mkdp_images_path":       "/images",
		"mkdp_page_title":        "Test Page",
		"mkdp_theme":             "dark",
		"mkdp_preview_options": map[string]any{
			"disable_sync_scroll": 1,
			"sync_scroll_type":    "middle",
		},
		"mkdp_port": "8080",
	}

	for name, val := range vars {
		if err := v.SetVar(name, val); err != nil {
			t.Fatalf("failed to set %s: %v", name, err)
		}
	}

	cfg, err := client.FetchConfig()
	if err != nil {
		t.Fatalf("FetchConfig failed: %v", err)
	}

	if !cfg.OpenToTheWorld {
		t.Error("expected OpenToTheWorld=true")
	}
	if cfg.OpenIP != "192.168.1.1" {
		t.Errorf("expected OpenIP=%q, got %q", "192.168.1.1", cfg.OpenIP)
	}
	if cfg.Browser != "firefox" {
		t.Errorf("expected Browser=%q, got %q", "firefox", cfg.Browser)
	}
	if cfg.MarkdownCSS != "/path/to/css" {
		t.Errorf("expected MarkdownCSS=%q, got %q", "/path/to/css", cfg.MarkdownCSS)
	}
	if cfg.HighlightCSS != "/path/to/highlight.css" {
		t.Errorf("expected HighlightCSS=%q, got %q", "/path/to/highlight.css", cfg.HighlightCSS)
	}
	if cfg.ImagesPath != "/images" {
		t.Errorf("expected ImagesPath=%q, got %q", "/images", cfg.ImagesPath)
	}
	if cfg.PageTitle != "Test Page" {
		t.Errorf("expected PageTitle=%q, got %q", "Test Page", cfg.PageTitle)
	}
	if cfg.Theme != "dark" {
		t.Errorf("expected Theme=%q, got %q", "dark", cfg.Theme)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected Port=8080, got %d", cfg.Port)
	}

	// PreviewOptions contains maps, so compare field-by-field.
	if cfg.PreviewOptions.DisableSyncScroll != 1 {
		t.Errorf("expected DisableSyncScroll=1, got %d", cfg.PreviewOptions.DisableSyncScroll)
	}
	if cfg.PreviewOptions.SyncScrollType != "middle" {
		t.Errorf("expected SyncScrollType=%q, got %q", "middle", cfg.PreviewOptions.SyncScrollType)
	}
}

// TestNeovimFetchConfigDefaults verifies that FetchConfig returns sensible
// defaults when no g:mkdp_* variables are set in Neovim. The batch call
// partially fails because the variables do not exist, exercising the
// BatchError recovery path. FetchConfig should fall back to DefaultConfig
// values for all fields.
//
// These assertions intentionally verify default-equals-default: the value
// under test is that FetchConfig's partial-failure handling preserves the
// DefaultConfig values rather than overwriting them with zero values from
// the failed batch results. A naive implementation that always assigns
// batch results (even on failure) would produce zero/empty values here.
//
// Caveat: not all assertions here have equal discriminating power.
//
//   - PageTitle and PreviewOptions have secondary guards in FetchConfig
//     (pageTitle != "" and previewOptions != nil, respectively) that
//     independently prevent overwriting with zero values. So even if the
//     completed-index guard were removed, these defaults would survive.
//     The assertions still document the expected outcome, but they test
//     the combined effect of both guards rather than the completed guard
//     in isolation.
//
//   - OpenToTheWorld, Port, and Browser have DefaultConfig() values that
//     equal their Go zero values (false, 0, ""). The assertions verify
//     the values are correct, but cannot distinguish "default preserved"
//     from "zero silently written over the default." A stronger test
//     would require injecting a partial batch failure at a specific index
//     (so some vars succeed while others fail), which is complex and
//     fragile for the marginal gain.
func TestNeovimFetchConfigDefaults(t *testing.T) {
	v := newTestNeovim(t)
	client := &NeovimClient{nvim: v, logger: discardLogger()}
	startServe(t, client)

	cfg, err := client.FetchConfig()
	if err != nil {
		t.Fatalf("FetchConfig failed: %v", err)
	}

	// --- Strong assertions: non-zero defaults with secondary guards ---
	// PageTitle has secondary guard (pageTitle != "") and a non-zero
	// default ("${name}"). Both guards must fail for this to break.
	if cfg.PageTitle != "${name}" {
		t.Errorf("expected default PageTitle=${name}, got %q", cfg.PageTitle)
	}
	// PreviewOptions has secondary guard (previewOptions != nil) and
	// non-zero defaults. Both guards must fail for these to break.
	if cfg.PreviewOptions.SyncScrollType != "middle" {
		t.Errorf("expected default SyncScrollType=%q, got %q", "middle", cfg.PreviewOptions.SyncScrollType)
	}
	if cfg.PreviewOptions.HideYAMLMeta != 1 {
		t.Errorf("expected default HideYAMLMeta=1, got %d", cfg.PreviewOptions.HideYAMLMeta)
	}

	// --- Weak assertions: default == zero value ---
	// These verify correctness but cannot distinguish "default preserved"
	// from "zero written over default" because DefaultConfig() uses the
	// Go zero value for these fields.
	if cfg.OpenToTheWorld {
		t.Error("expected OpenToTheWorld=false by default")
	}
	if cfg.Port != 0 {
		t.Errorf("expected default Port=0, got %d", cfg.Port)
	}
	if cfg.Browser != "" {
		t.Errorf("expected default Browser=\"\", got %q", cfg.Browser)
	}
}

// TestNeovimFetchBufferData verifies that FetchBufferData retrieves buffer
// content and metadata from a real Neovim instance.
func TestNeovimFetchBufferData(t *testing.T) {
	v := newTestNeovim(t)
	client := &NeovimClient{nvim: v, logger: discardLogger()}
	startServe(t, client)

	// Write content to buffer 1 (the default buffer).
	if err := v.SetBufferLines(nvim.Buffer(1), 0, -1, true, [][]byte{
		[]byte("# Hello"),
		[]byte("World"),
	}); err != nil {
		t.Fatalf("failed to set buffer lines: %v", err)
	}

	// Set the variables that FetchBufferData reads.
	if err := v.SetVar("mkdp_preview_options", map[string]any{
		"sync_scroll_type": "middle",
	}); err != nil {
		t.Fatalf("failed to set mkdp_preview_options: %v", err)
	}
	if err := v.SetVar("mkdp_page_title", "Test"); err != nil {
		t.Fatalf("failed to set mkdp_page_title: %v", err)
	}
	if err := v.SetVar("mkdp_theme", "light"); err != nil {
		t.Fatalf("failed to set mkdp_theme: %v", err)
	}

	data, err := client.FetchBufferData(1)
	if err != nil {
		t.Fatalf("FetchBufferData failed: %v", err)
	}

	// Verify content.
	if len(data.Content) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(data.Content))
	}
	if data.Content[0] != "# Hello" {
		t.Errorf("expected line 0=%q, got %q", "# Hello", data.Content[0])
	}
	if data.Content[1] != "World" {
		t.Errorf("expected line 1=%q, got %q", "World", data.Content[1])
	}

	// Verify metadata fields are populated.
	if data.PageTitle != "Test" {
		t.Errorf("expected PageTitle=%q, got %q", "Test", data.PageTitle)
	}
	if data.Theme != "light" {
		t.Errorf("expected Theme=%q, got %q", "light", data.Theme)
	}
	if data.Options == nil {
		t.Error("expected Options to be non-nil")
	}

	// WinLine and WinHeight should be populated (headless nvim has a window).
	// Exact values depend on the nvim terminal size, but a headless nvim
	// always has a 24-line internal terminal, so zero would indicate an
	// unusable window.
	if data.WinLine <= 0 {
		t.Errorf("expected positive WinLine, got %d", data.WinLine)
	}
	if data.WinHeight <= 0 {
		t.Errorf("expected positive WinHeight, got %d", data.WinHeight)
	}
}

// TestNeovimServeWithNotifications verifies the full notification dispatch
// path: Neovim sends an rpcnotify, the go-client library delivers it to
// the registered handler, and the NeovimClient dispatches it to the
// application callback.
func TestNeovimServeWithNotifications(t *testing.T) {
	v := newTestNeovim(t)
	client := &NeovimClient{nvim: v, logger: discardLogger()}

	gotBufnr := make(chan int, 1)
	var refreshCount atomic.Int32

	fenceDone := make(chan struct{})
	client.OnNotification(NotificationHandler{
		RefreshContent: func(bufnr int) {
			refreshCount.Add(1)
			select {
			case gotBufnr <- bufnr:
			default:
			}
		},
		ClosePage: func(_ int) {
			close(fenceDone)
		},
	})

	startServe(t, client)

	chanID := client.ChannelID()
	if chanID <= 0 {
		t.Fatalf("expected positive channel ID, got %d", chanID)
	}

	// Send a notification from Neovim to our channel.
	cmd := fmt.Sprintf("call rpcnotify(%d, 'refresh_content', {'bufnr': 1})", chanID)
	if err := v.Command(cmd); err != nil {
		t.Fatalf("rpcnotify command failed: %v", err)
	}

	// Wait for the handler to fire via the channel instead of polling.
	select {
	case bufnr := <-gotBufnr:
		if bufnr != 1 {
			t.Errorf("expected bufnr=1, got %d", bufnr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification handler was not called within timeout")
	}

	// Send a fence notification to detect spurious duplicate deliveries.
	// The go-client dispatches notifications serially on a single goroutine,
	// so when the fence arrives, all prior notifications have been delivered.
	fenceCmd := fmt.Sprintf("call rpcnotify(%d, 'close_page', {'bufnr': 99})", chanID)
	if err := v.Command(fenceCmd); err != nil {
		t.Fatalf("fence rpcnotify failed: %v", err)
	}
	select {
	case <-fenceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fence notification was not delivered within timeout")
	}
	if got := refreshCount.Load(); got != 1 {
		t.Errorf("expected handler to be called exactly once, got %d", got)
	}
}

// TestNeovimServeAllNotificationTypes verifies that all four notification
// types (refresh_content, close_page, open_browser, close_all_pages) are
// dispatched correctly through a real Neovim RPC channel.
func TestNeovimServeAllNotificationTypes(t *testing.T) {
	v := newTestNeovim(t)
	client := &NeovimClient{nvim: v, logger: discardLogger()}

	var refreshCount atomic.Int32
	var closeCount atomic.Int32
	var openCount atomic.Int32
	var closeAllCount atomic.Int32

	// allFired signals when all four initial handlers have been called.
	allFired := make(chan struct{})
	var remaining atomic.Int32
	remaining.Store(4)
	markDone := func() {
		if remaining.Add(-1) == 0 {
			close(allFired)
		}
	}

	// fenceDone signals when the fence notification arrives.
	fenceDone := make(chan struct{})

	client.OnNotification(NotificationHandler{
		RefreshContent: func(_ int) {
			if refreshCount.Add(1) == 1 {
				markDone()
			} else {
				// Second call is the fence.
				close(fenceDone)
			}
		},
		ClosePage: func(_ int) {
			closeCount.Add(1)
			markDone()
		},
		OpenBrowser: func(_ int) {
			openCount.Add(1)
			markDone()
		},
		CloseAllPages: func() {
			closeAllCount.Add(1)
			markDone()
		},
	})

	startServe(t, client)

	chanID := client.ChannelID()

	// Send one of each notification type.
	cmds := []string{
		fmt.Sprintf("call rpcnotify(%d, 'refresh_content', {'bufnr': 1})", chanID),
		fmt.Sprintf("call rpcnotify(%d, 'close_page', {'bufnr': 2})", chanID),
		fmt.Sprintf("call rpcnotify(%d, 'open_browser', {'bufnr': 3})", chanID),
		fmt.Sprintf("call rpcnotify(%d, 'close_all_pages')", chanID),
	}

	for _, cmd := range cmds {
		if err := v.Command(cmd); err != nil {
			t.Fatalf("command %q failed: %v", cmd, err)
		}
	}

	// Wait for all four handlers to fire.
	select {
	case <-allFired:
	case <-time.After(2 * time.Second):
		t.Fatalf("not all handlers called within timeout: refresh=%d close=%d open=%d closeAll=%d",
			refreshCount.Load(), closeCount.Load(), openCount.Load(), closeAllCount.Load())
	}

	// Send a fence notification to detect spurious duplicate deliveries.
	// The go-client dispatches notifications serially on a single goroutine,
	// so when the fence arrives, all prior notifications have been delivered.
	// One rpcnotify was sent per handler; each should have fired exactly once.
	fenceCmd := fmt.Sprintf("call rpcnotify(%d, 'refresh_content', {'bufnr': 99})", chanID)
	if err := v.Command(fenceCmd); err != nil {
		t.Fatalf("fence rpcnotify failed: %v", err)
	}
	select {
	case <-fenceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fence notification was not delivered within timeout")
	}
	if got := closeCount.Load(); got != 1 {
		t.Errorf("close_page: expected 1 call, got %d", got)
	}
	if got := openCount.Load(); got != 1 {
		t.Errorf("open_browser: expected 1 call, got %d", got)
	}
	if got := closeAllCount.Load(); got != 1 {
		t.Errorf("close_all_pages: expected 1 call, got %d", got)
	}
	if got := refreshCount.Load(); got != 2 {
		t.Errorf("refresh_content: expected 2 calls (1 real + 1 fence), got %d", got)
	}
}
