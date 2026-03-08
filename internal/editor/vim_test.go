package editor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// discardLogger returns a logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// startVimTestClient creates a VimClient wired to two io.Pipe pairs, starts
// Serve() in a background goroutine, and registers a t.Cleanup that closes
// the input pipe (triggering EOF shutdown) and waits for Serve() to return.
// Returns the client, the input writer (for injecting fake Vim responses),
// and the output reader (for reading requests the client sends).
func startVimTestClient(t *testing.T) (client *VimClient, inputW *io.PipeWriter, outputR *io.PipeReader) {
	t.Helper()

	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()

	client = NewVimClient(inputR, outputW, discardLogger())

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Serve()
	}()

	t.Cleanup(func() {
		_ = inputW.Close()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected nil error from Serve on EOF, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve did not return within timeout")
		}
	})

	return client, inputW, outputR
}

// TestVimClientServeAndShutdown verifies that Serve returns nil on a clean
// EOF from the reader, which is the normal shutdown path (Vim closes the
// channel, the Go side sees EOF and exits cleanly).
func TestVimClientServeAndShutdown(t *testing.T) {
	r, w := io.Pipe()
	client := NewVimClient(r, io.Discard, discardLogger())

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Serve()
	}()

	// Close the writer to send EOF to the reader.
	_ = w.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on EOF, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within timeout")
	}
}

// TestVimClientServeNotification verifies that a notification message from
// Vim is dispatched to the registered handler callback. The test sends a
// refresh_content notification with bufnr=1 and confirms the handler
// receives the correct buffer number.
func TestVimClientServeNotification(t *testing.T) {
	r, inputW := io.Pipe()
	client := NewVimClient(r, io.Discard, discardLogger())

	gotBufnr := make(chan int, 1)

	client.OnNotification(NotificationHandler{
		RefreshContent: func(bufnr int) {
			select {
			case gotBufnr <- bufnr:
			default:
			}
		},
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Serve()
	}()

	// Write a notification: [0, ["refresh_content", {"bufnr": 1}]]
	msg := `[0, ["refresh_content", {"bufnr": 1}]]` + "\n"
	if _, err := io.WriteString(inputW, msg); err != nil {
		t.Fatalf("failed to write notification: %v", err)
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

	// Clean shutdown.
	_ = inputW.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on EOF, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within timeout")
	}
}

// intPtr returns a pointer to a copy of v. Used by test cases that need a
// *int32 sentinel value to signal "assert this bufnr" vs nil for "no assertion".
func intPtr(v int32) *int32 { return &v }

// TestVimClientLifecycleNotifications verifies that close_page, close_all_pages,
// and open_browser notifications are dispatched to the registered handlers.
// These events are routed to the dedicated lifecycleCh (separate from the
// droppable notifyCh used for content refreshes), so this test uses polling
// to wait for dispatch.
func TestVimClientLifecycleNotifications(t *testing.T) {
	tests := []struct {
		name    string
		message string
		setup   func(*NotificationHandler, chan<- struct{}, *atomic.Int32)
		wantBuf *int32 // nil means no bufnr assertion (e.g. close_all_pages)
	}{
		{
			name:    "close_page",
			message: "[0, [\"close_page\", {\"bufnr\": 5}]]\n",
			setup: func(h *NotificationHandler, done chan<- struct{}, bufnr *atomic.Int32) {
				h.ClosePage = func(b int) {
					bufnr.Store(int32(b))
					select {
					case done <- struct{}{}:
					default:
					}
				}
			},
			wantBuf: intPtr(5),
		},
		{
			name:    "open_browser",
			message: "[0, [\"open_browser\", {\"bufnr\": 3}]]\n",
			setup: func(h *NotificationHandler, done chan<- struct{}, bufnr *atomic.Int32) {
				h.OpenBrowser = func(b int) {
					bufnr.Store(int32(b))
					select {
					case done <- struct{}{}:
					default:
					}
				}
			},
			wantBuf: intPtr(3),
		},
		{
			name:    "close_all_pages",
			message: "[0, [\"close_all_pages\"]]\n",
			setup: func(h *NotificationHandler, done chan<- struct{}, _ *atomic.Int32) {
				h.CloseAllPages = func() {
					select {
					case done <- struct{}{}:
					default:
					}
				}
			},
			wantBuf: nil, // close_all_pages has no bufnr
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, inputW := io.Pipe()
			client := NewVimClient(r, io.Discard, discardLogger())

			done := make(chan struct{}, 1)
			var receivedBufnr atomic.Int32
			var spurious atomic.Int32
			handler := NotificationHandler{}
			tt.setup(&handler, done, &receivedBufnr)

			// Fill in any nil handler fields with a spurious counter
			// so cross-dispatch bugs are detected rather than masked
			// by nil no-ops.
			if handler.RefreshContent == nil {
				handler.RefreshContent = func(int) { spurious.Add(1) }
			}
			if handler.ClosePage == nil {
				handler.ClosePage = func(int) { spurious.Add(1) }
			}
			if handler.OpenBrowser == nil {
				handler.OpenBrowser = func(int) { spurious.Add(1) }
			}
			if handler.CloseAllPages == nil {
				handler.CloseAllPages = func() { spurious.Add(1) }
			}
			client.OnNotification(handler)

			errCh := make(chan error, 1)
			go func() {
				errCh <- client.Serve()
			}()

			if _, err := io.WriteString(inputW, tt.message); err != nil {
				t.Fatalf("failed to write notification: %v", err)
			}

			// Wait for the handler to signal via the channel.
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s handler was not called within timeout", tt.name)
			}

			if tt.wantBuf != nil {
				if got := receivedBufnr.Load(); got != *tt.wantBuf {
					t.Errorf("expected bufnr=%d, got %d", *tt.wantBuf, got)
				}
			}
			if s := spurious.Load(); s != 0 {
				t.Errorf("expected no spurious handler calls, got %d", s)
			}

			_ = inputW.Close()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("expected nil error on EOF, got: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Serve did not return within timeout")
			}
		})
	}
}

// TestVimClientChannelID verifies that ChannelID always returns 0 for a
// Vim 8 JSON channel client. Unlike Neovim, the Vim 8 protocol has no
// numeric channel identifier.
func TestVimClientChannelID(t *testing.T) {
	client := NewVimClient(strings.NewReader(""), io.Discard, discardLogger())
	if id := client.ChannelID(); id != 0 {
		t.Errorf("expected ChannelID() = 0, got %d", id)
	}
}

// TestVimClientSendRequest exercises the full request/response cycle by
// calling SetVar, which sends an "expr" request and waits for a response.
// A goroutine simulates Vim by reading the request from the output pipe,
// extracting the message ID, and sending back a matching response.
func TestVimClientSendRequest(t *testing.T) {
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()

	client := NewVimClient(inputR, outputW, discardLogger())

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Serve()
	}()

	// Fake Vim goroutine: read the request, parse the message ID,
	// and send back a response. Errors are collected in a channel
	// and asserted on the main goroutine to avoid calling t.Errorf
	// from a background goroutine (which panics if the test exits
	// before the goroutine finishes).
	fakeErrs := make(chan string, 10)
	var fakeWg sync.WaitGroup
	fakeWg.Add(1)
	go func() {
		defer fakeWg.Done()

		dec := json.NewDecoder(outputR)
		var msg []json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			fakeErrs <- fmt.Sprintf("failed to decode request: %v", err)
			return
		}

		// Expect: ["expr", "extend(g:, {'test_var': json_decode('42')})", -1]
		if len(msg) < 3 {
			fakeErrs <- fmt.Sprintf("expected at least 3 elements in request, got %d", len(msg))
			return
		}

		// Verify the command type is "expr".
		var cmdType string
		if err := json.Unmarshal(msg[0], &cmdType); err != nil {
			fakeErrs <- fmt.Sprintf("failed to unmarshal command type: %v", err)
			return
		}
		if cmdType != "expr" {
			fakeErrs <- fmt.Sprintf("expected command type 'expr', got %q", cmdType)
		}

		// Verify the expression contains the variable name and value.
		var expr string
		if err := json.Unmarshal(msg[1], &expr); err != nil {
			fakeErrs <- fmt.Sprintf("failed to unmarshal expression: %v", err)
			return
		}
		if !strings.Contains(expr, "test_var") {
			fakeErrs <- fmt.Sprintf("expected expression to contain 'test_var', got %q", expr)
		}
		if !strings.Contains(expr, "42") {
			fakeErrs <- fmt.Sprintf("expected expression to contain '42', got %q", expr)
		}

		// Extract the message ID (negative integer).
		var msgID int
		if err := json.Unmarshal(msg[2], &msgID); err != nil {
			fakeErrs <- fmt.Sprintf("failed to unmarshal msgid: %v", err)
			return
		}
		if msgID >= 0 {
			fakeErrs <- fmt.Sprintf("expected negative msgid, got %d", msgID)
		}

		// Send back the response: [msgid, "0"]
		// Use []json.RawMessage encoding consistent with fakeVimResponder.
		resp, _ := json.Marshal([]json.RawMessage{
			json.RawMessage(strconv.Itoa(msgID)),
			json.RawMessage(`"0"`),
		})
		resp = append(resp, '\n')
		if _, err := inputW.Write(resp); err != nil {
			fakeErrs <- fmt.Sprintf("failed to write response: %v", err)
		}
	}()

	// Call SetVar, which sends a request and blocks until the response
	// is received from the fake Vim goroutine. Use t.Errorf (not Fatalf)
	// so that fake-goroutine errors below are still reported -- if the
	// goroutine failed to write a response, its errors explain the
	// SetVar timeout rather than the other way around.
	if err := client.SetVar("test_var", 42); err != nil {
		t.Errorf("SetVar failed: %v", err)
	}

	fakeWg.Wait()
	close(fakeErrs)
	for errMsg := range fakeErrs {
		t.Errorf("fake Vim goroutine: %s", errMsg)
	}

	// Clean shutdown.
	_ = inputW.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on EOF, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within timeout")
	}
}

// TestVimClientNotificationDrop verifies the backpressure behavior of the
// content notification channel (notifyCh). When the handler is blocked and
// more than notifyChanCapacity refresh_content notifications arrive, excess
// notifications are dropped rather than blocking the read loop (which would
// prevent RPC responses from being delivered).
//
// The drop guarantee is deterministic, not timing-dependent:
//   - The handler blocks on a gate channel, so at most 1 message is in-flight.
//   - notifyCh has capacity notifyChanCapacity (64), so 64 more can buffer.
//   - We send notifyChanCapacity+16 (80) total, guaranteeing at least 15 drops.
//   - The gate is not released until all 80 are sent, so no slot can free up early.
//
// A close_all_pages probe is sent after the flood and its callback is awaited.
// This confirms the dispatch goroutine is alive (not just the OS pipe buffer),
// because the callback only fires when the goroutine ranging over closeAllCh
// actually executes it.
func TestVimClientNotificationDrop(t *testing.T) {
	r, inputW := io.Pipe()
	client := NewVimClient(r, io.Discard, discardLogger())

	// Register a handler that blocks indefinitely until released.
	// This simulates a slow handler that causes backpressure.
	// Count invocations to verify that some notifications were dropped.
	// closeAllFired is signalled when the dispatch goroutine executes the
	// CloseAllPages callback, confirming that dispatch (not just pipe
	// buffer delivery) is still alive.
	gate := make(chan struct{})
	closeAllFired := make(chan struct{}, 1)
	var processed atomic.Int32
	client.OnNotification(NotificationHandler{
		RefreshContent: func(bufnr int) {
			<-gate
			processed.Add(1)
		},
		CloseAllPages: func() {
			select {
			case closeAllFired <- struct{}{}:
			default:
			}
		},
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Serve()
	}()

	// Send more than notifyChanCapacity refresh_content notifications.
	// These are routed to notifyCh (the droppable content channel) via
	// the default case in Serve's event switch. The first one will be
	// picked up by the dispatch goroutine and block on the gate. The
	// next notifyChanCapacity will fill the buffered channel. Beyond
	// that, notifications are silently dropped via non-blocking select.
	const total = notifyChanCapacity + 16
	for i := range total {
		msg := fmt.Sprintf("[0, [\"refresh_content\", {\"bufnr\": %d}]]\n", i+1)
		if _, err := io.WriteString(inputW, msg); err != nil {
			t.Fatalf("failed to write notification %d: %v", i, err)
		}
	}

	// Write a probe close_all_pages message after the flood and wait for its
	// callback to fire. This confirms the dispatch goroutine (not just the OS
	// pipe buffer) is still alive. close_all_pages is routed to the dedicated
	// closeAllCh (capacity 1, separate from the full notifyCh), so it is not
	// blocked by content channel backpressure.
	probe := `[0, ["close_all_pages"]]` + "\n"
	if _, err := io.WriteString(inputW, probe); err != nil {
		t.Fatalf("failed to write probe: %v", err)
	}

	select {
	case <-closeAllFired:
		// The callback fired, confirming the dispatch goroutine is alive.
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch goroutine appears deadlocked -- probe close_all_pages not processed")
	}

	// Release the gate and shut down cleanly.
	close(gate)
	_ = inputW.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on EOF, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within timeout")
	}

	// After Serve returns (which calls workers.stop() -- closing all
	// dispatch channels and waiting on the internal WaitGroup for the
	// goroutines to drain), verify that some notifications were
	// dropped AND that at least one was delivered. The processed count
	// is stable here because Serve blocks until all dispatch goroutines
	// finish. A processed count of 0 would indicate the dispatch
	// goroutine never ran.
	got := processed.Load()
	// The channel capacity bounds how many notifications can be buffered.
	// The handler processes at most notifyChanCapacity+1 messages: the
	// capacity worth of buffered items plus one that was being actively
	// received when the flood started.
	maxDelivered := int32(notifyChanCapacity + 1)
	if got > maxDelivered {
		t.Errorf("expected at most %d notifications delivered (channel capacity + 1), got %d", maxDelivered, got)
	}
	if got >= total {
		t.Errorf("expected some notifications to be dropped: handler processed %d of %d", got, total)
	}
	if got == 0 {
		t.Errorf("expected at least one notification delivered, got 0 (dispatch goroutine may not have run)")
	}
}

// TestSetVarNameValidation verifies that SetVar rejects variable names
// containing characters that could lead to VimScript injection, while
// accepting names that use only safe characters (letters, digits,
// underscores).
func TestSetVarNameValidation(t *testing.T) {
	// Use a client with no active Serve loop. Validation happens
	// before any I/O, so we don't need a running read loop.
	client := NewVimClient(strings.NewReader(""), io.Discard, discardLogger())

	t.Run("rejected names", func(t *testing.T) {
		badNames := []string{
			"",
			"foo bar",
			"foo;evil",
			"foo'bar",
			"foo\"bar",
			"foo.bar",
			"foo-bar",
		}
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

	t.Run("accepted names -- regex direct", func(t *testing.T) {
		// Test the validVarName regex directly (it is package-level and
		// accessible within this package). This is the strongest possible
		// assertion: it confirms that the regex MatchString call returns
		// true for each good name, independent of any I/O or Serve state.
		// If validVarName were accidentally deleted, this subtest would
		// fail to compile; if the regex were incorrectly narrowed, these
		// names would fail the match.
		goodNames := []string{
			"valid_name",
			"MixedCase123",
			"x",
			"UPPER",
			"_leading_underscore",
		}
		for _, name := range goodNames {
			if !validVarName.MatchString(name) {
				t.Errorf("validVarName regex rejected %q, expected it to match", name)
			}
		}
	})

	t.Run("accepted names -- SetVar path", func(t *testing.T) {
		// Also verify the full SetVar code path does not return
		// "unsafe variable name" for these names. The error path after
		// validation (sendRequest) returns "editor connection closed"
		// once Serve has exited, which is distinct from a validation
		// rejection. Using a post-shutdown client ensures any failure
		// here is from the validation branch, not from I/O.
		r, w := io.Pipe()
		c := NewVimClient(r, io.Discard, discardLogger())
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			_ = c.Serve()
		}()
		_ = w.Close() // EOF triggers Serve() shutdown, closing c.done.
		<-serveDone   // Wait for Serve() to fully exit before calling SetVar.

		goodNames := []string{
			"valid_name",
			"MixedCase123",
			"x",
			"UPPER",
			"_leading_underscore",
		}
		for _, name := range goodNames {
			err := c.SetVar(name, 1)
			switch {
			case err == nil:
				// Unreachable: Serve has been stopped, so sendRequest always
				// returns an error. Log if this assumption ever breaks.
				t.Logf("name %q: unexpected nil error (sendRequest succeeded after shutdown)", name)
			case errors.Is(err, errUnsafeVarName):
				// Validation rejected a name that should have been accepted.
				t.Errorf("name %q should pass validation, but got: %v", name, err)
			case errors.Is(err, ErrEditorClosed):
				// Expected: Serve() has exited, sendRequest returns this error.
			default:
				// Any other error is unexpected -- fail explicitly.
				t.Errorf("name %q: unexpected error (want nil or 'editor connection closed'): %v", name, err)
			}
		}
	})
}

// runFakeVimResponder launches fakeVimResponder in a background goroutine
// and returns a function that waits for it to finish, then reports any
// errors on t. Call the returned function after the code under test has
// completed its RPC round-trip.
func runFakeVimResponder(t *testing.T, outputR io.Reader, inputW io.Writer, responsePayload json.RawMessage, expectFuncName string, expectCallArgs json.RawMessage) func() {
	t.Helper()

	fakeErrs := make(chan string, 10)
	var fakeWg sync.WaitGroup
	fakeWg.Add(1)
	go func() {
		defer fakeWg.Done()
		fakeVimResponder(outputR, inputW, responsePayload, expectFuncName, expectCallArgs, fakeErrs)
	}()

	return func() {
		t.Helper()
		fakeWg.Wait()
		close(fakeErrs)
		for errMsg := range fakeErrs {
			t.Errorf("fake Vim goroutine: %s", errMsg)
		}
	}
}

// fakeVimResponder reads a single "call" or "expr" request from the output
// pipe, extracts the message ID, and sends back the given response payload
// on the input pipe. If expectFuncName is non-empty, verifies that the
// request's function name matches (catches accidental renames). If
// expectCallArgs is non-nil and the request is a "call", verifies that the
// arguments at index 2 match the expected JSON (catches dropped or altered
// arguments such as bufnr). Errors are collected in fakeErrs for assertion
// on the main goroutine.
func fakeVimResponder(outputR io.Reader, inputW io.Writer, responsePayload json.RawMessage, expectFuncName string, expectCallArgs json.RawMessage, fakeErrs chan<- string) {
	dec := json.NewDecoder(outputR)
	var msg []json.RawMessage
	if err := dec.Decode(&msg); err != nil {
		fakeErrs <- fmt.Sprintf("failed to decode request: %v", err)
		return
	}

	if len(msg) < 3 {
		fakeErrs <- fmt.Sprintf("expected at least 3 elements in request, got %d", len(msg))
		return
	}

	// Verify the command type and function name. "call" messages have
	// the function name at index 1; "expr" messages have the expression.
	var cmdType string
	if err := json.Unmarshal(msg[0], &cmdType); err != nil {
		fakeErrs <- fmt.Sprintf("failed to unmarshal command type: %v", err)
	}

	if expectFuncName != "" && cmdType == "call" {
		var funcName string
		if err := json.Unmarshal(msg[1], &funcName); err != nil {
			fakeErrs <- fmt.Sprintf("failed to unmarshal function name: %v", err)
		} else if funcName != expectFuncName {
			fakeErrs <- fmt.Sprintf("expected function %q, got %q", expectFuncName, funcName)
		}
	}

	// Verify call arguments when expected. For "call" messages the wire
	// format is ["call", funcName, args, msgID], so args live at index 2.
	if expectCallArgs != nil && cmdType == "call" && len(msg) >= 4 {
		if string(msg[2]) != string(expectCallArgs) {
			fakeErrs <- fmt.Sprintf("expected call args %s, got %s", expectCallArgs, msg[2])
		}
	}

	// The message ID is always the last element (negative integer).
	var msgID int
	if err := json.Unmarshal(msg[len(msg)-1], &msgID); err != nil {
		fakeErrs <- fmt.Sprintf("failed to unmarshal msgid: %v", err)
		return
	}

	resp, _ := json.Marshal([]json.RawMessage{
		json.RawMessage(strconv.Itoa(msgID)),
		responsePayload,
	})
	resp = append(resp, '\n')
	if _, err := inputW.Write(resp); err != nil {
		fakeErrs <- fmt.Sprintf("failed to write response: %v", err)
	}
}

// TestVimClientFetchConfig exercises the full FetchConfig round-trip by
// simulating a Vim response to the mkdp#rpc#gather_config() call.
func TestVimClientFetchConfig(t *testing.T) {
	client, inputW, outputR := startVimTestClient(t)

	// The response payload simulates what mkdp#rpc#gather_config() returns.
	configResponse := json.RawMessage(`{
		"open_to_the_world": 1,
		"open_ip": "192.168.1.100",
		"browser": "firefox",
		"markdown_css": "/tmp/custom.css",
		"highlight_css": "/tmp/highlight.css",
		"images_path": "/tmp/images",
		"page_title": "My Preview",
		"theme": "dark",
		"port": "8080",
		"preview_options": {
			"sync_scroll_type": "top",
			"hide_yaml_meta": 0,
			"disable_sync_scroll": 1
		}
	}`)

	collectFakeErrs := runFakeVimResponder(t, outputR, inputW, configResponse, "mkdp#rpc#gather_config", nil)

	cfg, err := client.FetchConfig()
	if err != nil {
		t.Fatalf("FetchConfig failed: %v", err)
	}

	collectFakeErrs()

	// Verify parsed config fields.
	if !cfg.OpenToTheWorld {
		t.Error("expected OpenToTheWorld=true")
	}
	if cfg.OpenIP != "192.168.1.100" {
		t.Errorf("expected OpenIP='192.168.1.100', got %q", cfg.OpenIP)
	}
	if cfg.Browser != "firefox" {
		t.Errorf("expected Browser='firefox', got %q", cfg.Browser)
	}
	if cfg.MarkdownCSS != "/tmp/custom.css" {
		t.Errorf("expected MarkdownCSS='/tmp/custom.css', got %q", cfg.MarkdownCSS)
	}
	if cfg.HighlightCSS != "/tmp/highlight.css" {
		t.Errorf("expected HighlightCSS='/tmp/highlight.css', got %q", cfg.HighlightCSS)
	}
	if cfg.ImagesPath != "/tmp/images" {
		t.Errorf("expected ImagesPath='/tmp/images', got %q", cfg.ImagesPath)
	}
	if cfg.PageTitle != "My Preview" {
		t.Errorf("expected PageTitle='My Preview', got %q", cfg.PageTitle)
	}
	if cfg.Theme != "dark" {
		t.Errorf("expected Theme='dark', got %q", cfg.Theme)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected Port=8080, got %d", cfg.Port)
	}
	if cfg.PreviewOptions.SyncScrollType != "top" {
		t.Errorf("expected SyncScrollType='top', got %q", cfg.PreviewOptions.SyncScrollType)
	}
	if cfg.PreviewOptions.HideYAMLMeta != 0 {
		t.Errorf("expected HideYAMLMeta=0, got %d", cfg.PreviewOptions.HideYAMLMeta)
	}
	if cfg.PreviewOptions.DisableSyncScroll != 1 {
		t.Errorf("expected DisableSyncScroll=1, got %d", cfg.PreviewOptions.DisableSyncScroll)
	}
	if cfg.Standalone {
		t.Error("Standalone should not be set by FetchConfig")
	}
}

// TestVimClientFetchConfigDefaults verifies that FetchConfig preserves
// DefaultConfig values when the Vim response contains an empty/partial map.
// This mirrors TestNeovimFetchConfigDefaults for the Vim side.
func TestVimClientFetchConfigDefaults(t *testing.T) {
	client, inputW, outputR := startVimTestClient(t)

	// Respond with an empty map to simulate missing/unset variables.
	configResponse := json.RawMessage(`{}`)

	collectFakeErrs := runFakeVimResponder(t, outputR, inputW, configResponse, "mkdp#rpc#gather_config", nil)

	cfg, err := client.FetchConfig()
	if err != nil {
		t.Fatalf("FetchConfig failed: %v", err)
	}

	collectFakeErrs()

	// Verify that DefaultConfig non-zero values survive when the
	// response has no corresponding keys. A naive implementation
	// that unconditionally assigns zero values would fail these.
	if cfg.PageTitle != "${name}" {
		t.Errorf("expected default PageTitle=${name}, got %q", cfg.PageTitle)
	}
	if cfg.PreviewOptions.SyncScrollType != "middle" {
		t.Errorf("expected default SyncScrollType=%q, got %q", "middle", cfg.PreviewOptions.SyncScrollType)
	}
	if cfg.PreviewOptions.HideYAMLMeta != 1 {
		t.Errorf("expected default HideYAMLMeta=1, got %d", cfg.PreviewOptions.HideYAMLMeta)
	}

	// The following assertions are deliberately weak: the default values for
	// Port (0), Browser (""), and OpenToTheWorld (false) coincide with Go
	// zero values, so these checks cannot distinguish "FetchConfig correctly
	// preserved the DefaultConfig value" from "FetchConfig silently zeroed
	// out the field." They are retained as a minimum sanity check only.
	if cfg.Port != 0 {
		t.Errorf("expected default Port=0, got %d", cfg.Port)
	}
	if cfg.Browser != "" {
		t.Errorf("expected default Browser=\"\", got %q", cfg.Browser)
	}
	if cfg.OpenToTheWorld {
		t.Error("expected OpenToTheWorld=false by default (weak: default equals zero value)")
	}
}

// TestVimClientFetchConfigEmptyPageTitle verifies that an explicit empty
// page_title in the Vim response does not overwrite the DefaultConfig value
// ("${name}" template). An empty string means the variable was not set by
// the user; the default must be preserved so the browser tab title is not blank.
func TestVimClientFetchConfigEmptyPageTitle(t *testing.T) {
	client, inputW, outputR := startVimTestClient(t)

	// Simulate Vim sending page_title as an explicit empty string.
	configResponse := json.RawMessage(`{"page_title": ""}`)

	collectFakeErrs := runFakeVimResponder(t, outputR, inputW, configResponse, "mkdp#rpc#gather_config", nil)

	cfg, err := client.FetchConfig()
	if err != nil {
		t.Fatalf("FetchConfig failed: %v", err)
	}

	collectFakeErrs()

	// Empty string must not overwrite the default template.
	if cfg.PageTitle != "${name}" {
		t.Errorf("expected PageTitle=${name} (default), got %q", cfg.PageTitle)
	}
}

// TestVimClientFetchConfigTheme exercises the non-empty guard before
// assignment pattern in FetchConfig for the theme field. Sends a non-empty
// theme and verifies it is applied. The empty-theme case cannot be
// meaningfully tested because DefaultConfig().Theme is already the zero
// value, making it impossible to distinguish preserved from overwritten.
func TestVimClientFetchConfigTheme(t *testing.T) {
	client, inputW, outputR := startVimTestClient(t)

	// Simulate Vim sending a non-empty theme.
	configResponse := json.RawMessage(`{"theme": "dark"}`)

	collectFakeErrs := runFakeVimResponder(t, outputR, inputW, configResponse, "mkdp#rpc#gather_config", nil)

	cfg, err := client.FetchConfig()
	if err != nil {
		t.Fatalf("FetchConfig failed: %v", err)
	}

	collectFakeErrs()

	if cfg.Theme != "dark" {
		t.Errorf("expected Theme=%q, got %q", "dark", cfg.Theme)
	}
}

// TestVimClientFetchBufferData exercises the full FetchBufferData round-trip
// by simulating a Vim response to the mkdp#rpc#gather_data(bufnr) call.
func TestVimClientFetchBufferData(t *testing.T) {
	client, inputW, outputR := startVimTestClient(t)

	// The response payload simulates what mkdp#rpc#gather_data() returns.
	bufferResponse := json.RawMessage(`{
		"content": ["# Hello", "", "World"],
		"cursor": [1, 5, 3, 0],
		"winline": 5,
		"winheight": 30,
		"options": {"sync_scroll_type": "middle", "hide_yaml_meta": 1},
		"pageTitle": "test.md",
		"theme": "light",
		"name": "test.md"
	}`)

	collectFakeErrs := runFakeVimResponder(t, outputR, inputW, bufferResponse, "mkdp#rpc#gather_data", json.RawMessage(`[1]`))

	data, err := client.FetchBufferData(1)
	if err != nil {
		t.Fatalf("FetchBufferData failed: %v", err)
	}

	collectFakeErrs()

	// Verify parsed fields.
	if len(data.Content) != 3 || data.Content[0] != "# Hello" {
		t.Errorf("unexpected Content: %v", data.Content)
	}
	if len(data.Cursor) != 4 || data.Cursor[0] != 1 || data.Cursor[1] != 5 || data.Cursor[2] != 3 || data.Cursor[3] != 0 {
		t.Errorf("unexpected cursor: %v", data.Cursor)
	}
	if data.WinLine != 5 {
		t.Errorf("expected WinLine=5, got %d", data.WinLine)
	}
	if data.WinHeight != 30 {
		t.Errorf("expected WinHeight=30, got %d", data.WinHeight)
	}
	if data.PageTitle != "test.md" {
		t.Errorf("expected PageTitle='test.md', got %q", data.PageTitle)
	}
	if data.Theme != "light" {
		t.Errorf("expected Theme='light', got %q", data.Theme)
	}
	if data.Name != "test.md" {
		t.Errorf("expected Name='test.md', got %q", data.Name)
	}
	if data.Options == nil {
		t.Fatal("expected non-nil Options")
	}
	if data.Options["sync_scroll_type"] != "middle" {
		t.Errorf("expected Options[sync_scroll_type]='middle', got %v", data.Options["sync_scroll_type"])
	}
	if data.Options["hide_yaml_meta"] != float64(1) {
		t.Errorf("expected Options[hide_yaml_meta]=1, got %v", data.Options["hide_yaml_meta"])
	}
}

// TestVimClientFetchBufferDataError exercises FetchBufferData's error
// handling when the Vim response is valid JSON (delivered successfully via
// the wire protocol) but cannot be unmarshaled into a RefreshData struct.
func TestVimClientFetchBufferDataError(t *testing.T) {
	tests := []struct {
		name     string
		response json.RawMessage
	}{
		{
			// A JSON number cannot be unmarshaled into a struct.
			name:     "number response",
			response: json.RawMessage(`42`),
		},
		{
			// A JSON array cannot be unmarshaled into a struct.
			name:     "array response",
			response: json.RawMessage(`[1, 2, 3]`),
		},
		{
			// A JSON string cannot be unmarshaled into a struct.
			name:     "string response",
			response: json.RawMessage(`"not an object"`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, inputW, outputR := startVimTestClient(t)

			collectFakeErrs := runFakeVimResponder(t, outputR, inputW, tt.response, "mkdp#rpc#gather_data", json.RawMessage(`[1]`))

			data, err := client.FetchBufferData(1)
			if err == nil {
				t.Error("expected error from FetchBufferData, got nil")
			} else if !strings.Contains(err.Error(), "unmarshal") {
				t.Errorf("expected unmarshal error, got: %v", err)
			}
			if data != nil {
				t.Errorf("expected nil data on error, got %+v", data)
			}

			collectFakeErrs()
		})
	}
}
