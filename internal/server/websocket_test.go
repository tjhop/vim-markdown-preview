package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tjhop/vim-markdown-preview/internal/config"
)

// refreshMessage is a typed wsMessage for tests that carry config.RefreshData.
// It avoids the double-unmarshal pattern (unmarshal -> re-marshal msg.Data -> unmarshal again).
// Duplicated in cmd/vim-markdown-preview/main_test.go; Go test packages
// cannot share helpers, so this is intentional.
type refreshMessage struct {
	Event string             `json:"event"`
	Data  config.RefreshData `json:"data"`
}

// awaitNConnects sets the OnClientChange callback to signal after n connect
// events (callbacks where hasClients is true). Disconnect callbacks are
// ignored so stale disconnects from earlier in the test cannot trigger a
// false-positive ready signal in multi-client scenarios.
//
// Warning: each call replaces the server's OnClientChange callback. If a
// prior awaitNConnects channel has not yet been signaled, it will hang
// forever. Only call after the previous awaiter has completed.
func awaitNConnects(srv *Server, n int) <-chan struct{} {
	ready := make(chan struct{}, 1)
	var count atomic.Int32
	srv.SetOnClientChange(func(hasClients bool) {
		if !hasClients {
			return
		}
		if count.Add(1) >= int32(n) {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})
	return ready
}

// readRefreshMessage reads a single WebSocket message from conn and
// unmarshals it into a refreshMessage. Fails the test on any error.
func readRefreshMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) refreshMessage {
	t.Helper()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("websocket read failed: %v", err)
	}

	var msg refreshMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("failed to unmarshal refresh message: %v", err)
	}
	return msg
}

func TestWebSocketConnectAndBroadcast(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 1)

	// Connect a WebSocket client for buffer 1.
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	// Wait for the server to register the client.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	// Broadcast a message to buffer 1.
	srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
		Content: []string{"# Test", "", "Hello world"},
		Name:    "test.md",
	})

	// Read the message from the WebSocket.
	msg := readRefreshMessage(t, ctx, conn)

	if msg.Event != "refresh_content" {
		t.Errorf("expected event 'refresh_content', got %q", msg.Event)
	}
	if msg.Data.Name != "test.md" {
		t.Errorf("expected name 'test.md', got %q", msg.Data.Name)
	}
	if len(msg.Data.Content) != 3 {
		t.Errorf("expected 3 content lines, got %d", len(msg.Data.Content))
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocketBroadcastToBufferClosePage(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 1)

	// Connect a WebSocket client for buffer 1.
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	// Wait for the server to register the client.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	// Broadcast a close_page event to buffer 1 with nil data.
	srv.BroadcastTransientToBuffer(1, "close_page", nil)

	// Read the message and verify the event.
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("websocket read failed: %v", err)
	}
	var msg wsMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	if msg.Event != "close_page" {
		t.Errorf("expected event 'close_page', got %q", msg.Event)
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocketBroadcastToCorrectBuffer(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 2)

	// Connect to buffer 1 and buffer 2.
	conn1, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial buf1 failed: %v", err)
	}
	defer func() { _ = conn1.CloseNow() }()

	conn2, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=2", nil)
	if err != nil {
		t.Fatalf("websocket dial buf2 failed: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()

	// Wait for the server to register both clients.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	// Broadcast only to buffer 2.
	srv.BroadcastToBuffer(2, "refresh_content", config.RefreshData{
		Content: []string{"buffer 2 content"},
		Name:    "buf2.md",
	})

	// conn2 should receive the message.
	msg := readRefreshMessage(t, ctx, conn2)
	if msg.Event != "refresh_content" {
		t.Errorf("expected 'refresh_content', got %q", msg.Event)
	}
	if msg.Data.Name != "buf2.md" {
		t.Errorf("expected name 'buf2.md', got %q", msg.Data.Name)
	}

	// conn1 should NOT receive anything (short timeout). Negative
	// assertions via timeout are inherently imperfect: they can only
	// prove absence within the window, not absolute absence. 100ms
	// provides reasonable headroom for slow CI runners while keeping
	// the test suite fast.
	readCtx, readCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer readCancel()
	_, _, err = conn1.Read(readCtx)
	if err == nil {
		t.Error("conn1 should not have received a message for buffer 2")
	}

	_ = conn1.Close(websocket.StatusNormalClosure, "")
	_ = conn2.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocketBroadcastAll(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 2)

	conn1, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial buf1 failed: %v", err)
	}
	defer func() { _ = conn1.CloseNow() }()

	conn2, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=2", nil)
	if err != nil {
		t.Fatalf("websocket dial buf2 failed: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()

	// Wait for the server to register both clients.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	// BroadcastAll should reach both clients.
	srv.BroadcastAll("close_page", nil)

	for i, conn := range []*websocket.Conn{conn1, conn2} {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("conn%d read failed: %v", i+1, err)
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("conn%d unmarshal failed: %v", i+1, err)
		}
		if msg.Event != "close_page" {
			t.Errorf("conn%d expected 'close_page', got %q", i+1, msg.Event)
		}
	}

	_ = conn1.Close(websocket.StatusNormalClosure, "")
	_ = conn2.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocketInvalidBufnr(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// Attempt to connect with a non-numeric bufnr.
	resp, err := http.Get("http://" + addr + "/ws?bufnr=abc")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid bufnr, got %d", resp.StatusCode)
	}
}

// startOpenServer creates a server with OpenToTheWorld enabled so that
// origin checking is active (OriginPatterns instead of InsecureSkipVerify).
func startOpenServer(t *testing.T) *Server {
	t.Helper()
	return startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.OpenToTheWorld = true
	})
}

func TestWebSocketOriginRejected(t *testing.T) {
	srv := startOpenServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Dial with a foreign origin -- the server should reject it.
	_, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Origin": []string{"http://evil.example.com"},
		},
	})
	if err == nil {
		t.Fatal("expected dial with evil origin to fail, but it succeeded")
	}
}

func TestWebSocketDisconnectCleanup(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Track connect and disconnect callbacks via separate channels.
	// The connect channel receives the hasClients value from the first
	// callback (client added), and the disconnect channel receives the
	// value from the second callback (client removed).
	connectCh := make(chan bool, 1)
	disconnectCh := make(chan bool, 1)
	unexpectedCh := make(chan string, 10)
	var callCount atomic.Int32
	srv.SetOnClientChange(func(hasClients bool) {
		n := callCount.Add(1)
		switch n {
		case 1:
			connectCh <- hasClients
		case 2:
			disconnectCh <- hasClients
		default:
			unexpectedCh <- fmt.Sprintf("unexpected client-change callback #%d (hasClients=%v)", n, hasClients)
		}
	})

	// Connect a WebSocket client for buffer 1.
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}

	// Wait for the connect callback and verify hasClients is true.
	select {
	case has := <-connectCh:
		if !has {
			t.Error("expected hasClients=true on connect, got false")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for connect callback")
	}

	// Close the client connection. This causes the server's read loop
	// to break and the deferred cleanup to fire.
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	// Wait for the disconnect callback and verify hasClients is false.
	select {
	case has := <-disconnectCh:
		if has {
			t.Error("expected hasClients=false after disconnect, got true")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for disconnect callback")
	}

	// Drain any unexpected callback errors.
	for len(unexpectedCh) > 0 {
		t.Error(<-unexpectedCh)
	}
}

func TestWebSocketShutdownClosesClients(t *testing.T) {
	// Manage the server lifecycle manually so we can call Shutdown
	// mid-test without relying on t.Cleanup ordering.
	cfg := config.DefaultConfig()
	cfg.Port = 0
	srv := New(cfg, discardLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	addr := srv.Addr().String()

	ctx := t.Context()

	ready := awaitNConnects(srv, 1)

	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	// Wait for the server to register the client.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	// Start reading control frames on the client so the graceful close
	// handshake completes promptly. In production, the browser handles
	// this automatically; in tests we must call CloseRead explicitly.
	// The returned context cancels when the connection closes.
	clientCtx := conn.CloseRead(ctx)

	// Use context.Background for Shutdown -- t.Context() is cancelled
	// when the test function returns, which can race with Shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// Shut down the server, which should close all WebSocket clients.
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	// The client context should be cancelled because the server sent
	// a close frame (StatusGoingAway) during shutdown.
	select {
	case <-clientCtx.Done():
		// Connection closed as expected.
	case <-shutdownCtx.Done():
		t.Fatal("client connection was not closed after server shutdown")
	}
}

func TestWebSocketMissingBufnr(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// Send GET to /ws with no bufnr query param. strconv.Atoi("")
	// returns an error, so the handler should respond with 400.
	resp, err := http.Get("http://" + addr + "/ws")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for missing bufnr, got %d", resp.StatusCode)
	}
}

func TestWebSocketReplayOnConnect(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Broadcast content before any client is connected. The message
	// should be stored for replay.
	srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
		Content: []string{"# Replayed", "", "Hello from before connect"},
		Name:    "replay.md",
	})

	ready := awaitNConnects(srv, 1)

	// Now connect -- the client should immediately receive the
	// replayed message without a new broadcast.
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	msg := readRefreshMessage(t, ctx, conn)

	if msg.Event != "refresh_content" {
		t.Errorf("expected event 'refresh_content', got %q", msg.Event)
	}
	if msg.Data.Name != "replay.md" {
		t.Errorf("expected name 'replay.md', got %q", msg.Data.Name)
	}
	if len(msg.Data.Content) != 3 {
		t.Errorf("expected 3 content lines, got %d", len(msg.Data.Content))
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocketReplayUpdatesOnNewBroadcast(t *testing.T) {
	// pre-connect: both broadcasts happen before any client connects;
	// the connecting client should receive only the latest.
	t.Run("pre-connect replacement", func(t *testing.T) {
		srv := startTestServer(t)
		addr := srv.Addr().String()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		// Broadcast twice -- only the latest should be replayed.
		srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
			Content: []string{"old content"},
			Name:    "old.md",
		})
		srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
			Content: []string{"new content"},
			Name:    "new.md",
		})

		ready := awaitNConnects(srv, 1)

		conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
		if err != nil {
			t.Fatalf("websocket dial failed: %v", err)
		}
		defer func() { _ = conn.CloseNow() }()

		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("timed out waiting for client registration")
		}

		msg := readRefreshMessage(t, ctx, conn)

		// The replayed message should be the latest broadcast, not the first.
		if msg.Data.Name != "new.md" {
			t.Errorf("expected replayed name 'new.md', got %q", msg.Data.Name)
		}
		if len(msg.Data.Content) != 1 || msg.Data.Content[0] != "new content" {
			t.Errorf("expected replayed content [\"new content\"], got %v", msg.Data.Content)
		}

		// Verify no second message (old content) was replayed.
		noMsgCtx, noMsgCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer noMsgCancel()
		_, _, err = conn.Read(noMsgCtx)
		if err == nil {
			t.Error("expected no second replay message, but received one")
		}

		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	// mid-session: a first client is connected during a second broadcast;
	// a late-connecting second client should receive the second broadcast,
	// not the first.
	t.Run("mid-session replacement", func(t *testing.T) {
		srv := startTestServer(t)
		addr := srv.Addr().String()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		// First broadcast: send initial content.
		srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
			Content: []string{"first content"},
			Name:    "first.md",
		})

		// Connect first client and wait for registration.
		ready1 := awaitNConnects(srv, 1)
		conn1, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
		if err != nil {
			t.Fatalf("websocket dial buf1 failed: %v", err)
		}
		defer func() { _ = conn1.CloseNow() }()
		select {
		case <-ready1:
		case <-ctx.Done():
			t.Fatal("timed out waiting for first client registration")
		}

		// Drain the replay received by conn1 (from the first broadcast).
		msg1 := readRefreshMessage(t, ctx, conn1)
		if msg1.Data.Name != "first.md" {
			t.Errorf("first client: expected replay 'first.md', got %q", msg1.Data.Name)
		}

		// Second broadcast while conn1 is connected: this updates lastMessage.
		srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
			Content: []string{"second content"},
			Name:    "second.md",
		})

		// BroadcastToBuffer is synchronous: by the time it returned above,
		// both the lastMessage store and the client writes are complete.
		// Drain the live message from conn1 to keep the connection clean.
		msg1live := readRefreshMessage(t, ctx, conn1)
		if msg1live.Data.Name != "second.md" {
			t.Errorf("first client: expected live message 'second.md', got %q", msg1live.Data.Name)
		}

		// Connect a second (late) client. It should receive a replay of
		// the second broadcast, not the first.
		ready2 := awaitNConnects(srv, 1)
		conn2, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
		if err != nil {
			t.Fatalf("websocket dial buf2 failed: %v", err)
		}
		defer func() { _ = conn2.CloseNow() }()
		select {
		case <-ready2:
		case <-ctx.Done():
			t.Fatal("timed out waiting for second client registration")
		}

		msg2 := readRefreshMessage(t, ctx, conn2)
		if msg2.Data.Name != "second.md" {
			t.Errorf("late client: expected replay 'second.md', got %q", msg2.Data.Name)
		}
		if len(msg2.Data.Content) != 1 || msg2.Data.Content[0] != "second content" {
			t.Errorf("late client: expected content [\"second content\"], got %v", msg2.Data.Content)
		}

		_ = conn1.Close(websocket.StatusNormalClosure, "")
		_ = conn2.Close(websocket.StatusNormalClosure, "")
	})
}

func TestConcurrentBroadcast(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 1)

	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	const n = 10
	// Build a lookup map of name -> expected content so the assertion
	// below can verify consistency without fragile string slicing.
	expectedContent := make(map[string]string, n)
	for i := range n {
		expectedContent[fmt.Sprintf("msg%d.md", i)] = fmt.Sprintf("message %d", i)
	}

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
				Content: []string{fmt.Sprintf("message %d", i)},
				Name:    fmt.Sprintf("msg%d.md", i),
			})
		}()
	}
	wg.Wait()

	// Read messages until the context deadline or the connection is closed
	// (e.g., because a write timeout caused the server to evict the client).
	// On a heavily loaded CI machine the per-client writeTimeout (5s) may
	// expire before all n messages are delivered, causing the client to be
	// silently evicted and conn.Read to return an error. Rather than asserting
	// exactly n messages, we accept whatever the server managed to deliver and
	// verify that:
	//   1. At least one message arrived (confirms concurrent broadcast works at all).
	//   2. Every received message has consistent name/content pairing (catches
	//      data-swap bugs where concurrent goroutines intermix field values).
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readCancel()

	received := make(map[string]refreshMessage)
	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			// Context deadline or connection closed -- stop reading.
			break
		}
		var msg refreshMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		received[msg.Data.Name] = msg
		if len(received) == n {
			// All messages delivered; no need to wait for the deadline.
			break
		}
	}

	if len(received) == 0 {
		t.Fatal("no messages received -- concurrent broadcast appears broken")
	}

	// Verify name/content consistency for every message that did arrive.
	// Each BroadcastToBuffer call pairs "msg<i>.md" with "message <i>".
	// A data-swap bug would cause a mismatch between the name and content.
	for name, msg := range received {
		want, ok := expectedContent[name]
		if !ok {
			t.Errorf("unexpected message name %q", name)
			continue
		}
		if len(msg.Data.Content) != 1 || msg.Data.Content[0] != want {
			t.Errorf("message %q: expected content [%q], got %v", name, want, msg.Data.Content)
		}
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestFailedWriteRemovesClient(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	connectCh := make(chan bool, 1)
	disconnectCh := make(chan bool, 1)
	unexpectedCh := make(chan string, 10)
	var callCount atomic.Int32
	srv.SetOnClientChange(func(hasClients bool) {
		n := callCount.Add(1)
		switch n {
		case 1:
			connectCh <- hasClients
		case 2:
			disconnectCh <- hasClients
		default:
			unexpectedCh <- fmt.Sprintf("unexpected client-change callback #%d (hasClients=%v)", n, hasClients)
		}
	})

	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}

	select {
	case has := <-connectCh:
		if !has {
			t.Error("expected hasClients=true on connect")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for connect callback")
	}

	// Force-close without a graceful close frame. The server detects the
	// broken connection via its read loop and removes the client.
	_ = conn.CloseNow()

	select {
	case has := <-disconnectCh:
		if has {
			t.Error("expected hasClients=false after force-close")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for disconnect callback")
	}

	// Broadcasting after removal should be a silent no-op.
	srv.BroadcastTransientToBuffer(1, "test", nil)

	// Drain any unexpected callback errors.
	for len(unexpectedCh) > 0 {
		t.Error(<-unexpectedCh)
	}
}

func TestBroadcastAllNoReplay(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 1)

	// Connect a client to receive the BroadcastAll.
	conn1, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn1.CloseNow() }()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	// BroadcastAll does not store messages for replay.
	srv.BroadcastAll("close_page", nil)

	// conn1 should receive the message; verify the event type.
	_, data, err := conn1.Read(ctx)
	if err != nil {
		t.Fatalf("conn1 read failed: %v", err)
	}
	var msg struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if msg.Event != "close_page" {
		t.Errorf("expected event %q, got %q", "close_page", msg.Event)
	}

	// Connect a late client. awaitNConnects replaces the callback
	// and starts a fresh counter, so n=1 waits for the next connect.
	ready2 := awaitNConnects(srv, 1)

	conn2, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()

	select {
	case <-ready2:
	case <-ctx.Done():
		t.Fatal("timed out waiting for second client registration")
	}

	// conn2 should NOT receive any replay. Negative assertions via
	// timeout are inherently imperfect: they can only prove absence
	// within the window, not absolute absence. 100ms provides
	// reasonable headroom for slow CI runners. The replay would be
	// synchronous with registration, so even a short window suffices.
	readCtx, readCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer readCancel()
	_, _, err = conn2.Read(readCtx)
	if err == nil {
		t.Error("late client should not receive replay from BroadcastAll")
	}

	_ = conn1.Close(websocket.StatusNormalClosure, "")
	_ = conn2.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocketBufnrOutOfRange(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	for _, tt := range []struct {
		name  string
		bufnr string
	}{
		{"zero", "0"},
		{"negative", "-1"},
		{"exceeds upper bound", "1048577"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get("http://" + addr + "/ws?bufnr=" + tt.bufnr)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected status 400 for bufnr=%s, got %d", tt.bufnr, resp.StatusCode)
			}
		})
	}
}

func TestWebSocketLocalhostOriginsAccepted(t *testing.T) {
	srv := startOpenServer(t)
	addr := srv.Addr().String()

	origins := []string{
		"http://localhost",
		"http://127.0.0.1",
		"http://[::1]",
		"http://[::1]:8080", // explicit port; exercises the \[::1\]:* pattern
	}

	for _, origin := range origins {
		t.Run(origin, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", &websocket.DialOptions{
				HTTPHeader: http.Header{
					"Origin": []string{origin},
				},
			})
			if err != nil {
				t.Fatalf("expected dial with origin %q to succeed: %v", origin, err)
			}
			defer func() { _ = conn.CloseNow() }()
			_ = conn.Close(websocket.StatusNormalClosure, "")
		})
	}
}

// TestWebSocketOpenIPv6OriginAccepted verifies that a server configured with
// OpenIP="::1" accepts WebSocket connections whose Origin header contains the
// matching IPv6 address and rejects connections from a different address. This
// exercises the strings.Contains(ip, ":") bracket-escape code path in
// websocketAcceptOptions. httptest.NewServer is used because "::1" is not a
// valid net.Listen address without brackets. The test registers routes via
// registerRoutes to exercise the production mux rather than calling
// handleWebSocket directly.
func TestWebSocketOpenIPv6OriginAccepted(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.OpenToTheWorld = true
	cfg.OpenIP = "::1"
	srv := New(cfg, discardLogger())

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	testSrv := httptest.NewServer(mux)
	t.Cleanup(func() {
		testSrv.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})
	addr := testSrv.Listener.Addr().String()

	// Origin: http://[::1] must be accepted -- exercises the bracket-escape path.
	t.Run("matching IPv6 origin accepted", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", &websocket.DialOptions{
			HTTPHeader: http.Header{
				"Origin": []string{"http://[::1]"},
			},
		})
		if err != nil {
			t.Fatalf("expected Origin http://[::1] to be accepted: %v", err)
		}
		defer func() { _ = conn.CloseNow() }()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	// Origin: http://[::2] must be rejected -- different address, no pattern match.
	t.Run("different IPv6 address rejected", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?bufnr=1", &websocket.DialOptions{
			HTTPHeader: http.Header{
				"Origin": []string{"http://[::2]"},
			},
		})
		if err == nil {
			t.Fatal("expected Origin http://[::2] to be rejected, but connection succeeded")
		}
	})
}

// TestIPv6OriginPatternEscaping verifies that the path.Match patterns used
// for IPv6 origin checking correctly match bracketed IPv6 literals. The
// patterns use \\[ and \\] in Go source (producing the two-char sequences
// \[ and \]) which path.Match interprets as escaped literal brackets.
func TestIPv6OriginPatternEscaping(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{`\[::1\]`, "[::1]", true},
		{`\[::1\]:*`, "[::1]:8080", true},
		{`\[::1\]:*`, "[::1]:0", true},
		{`\[::1\]`, "::1", false},   // unbracketed must not match
		{`\[::1\]`, "[::2]", false}, // different address must not match
	}
	for _, tt := range cases {
		t.Run(tt.pattern+"/"+tt.host, func(t *testing.T) {
			got, err := path.Match(tt.pattern, tt.host)
			if err != nil {
				t.Fatalf("path.Match(%q, %q) error: %v", tt.pattern, tt.host, err)
			}
			if got != tt.want {
				t.Errorf("path.Match(%q, %q) = %v, want %v", tt.pattern, tt.host, got, tt.want)
			}
		})
	}
}
