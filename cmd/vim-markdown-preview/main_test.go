package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tjhop/vim-markdown-preview/internal/config"
	"github.com/tjhop/vim-markdown-preview/internal/server"
)

// discardLogger returns a logger that discards all output.
// Duplicated from internal/server/server_test.go and
// internal/editor/vim_test.go. Go test infrastructure does not share
// helpers across packages, so this is intentional duplication.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// refreshMessage mirrors the WebSocket message format for tests.
// Duplicated in internal/server/websocket_test.go; Go test packages
// cannot share helpers, so this is intentional.
type refreshMessage struct {
	Event string             `json:"event"`
	Data  config.RefreshData `json:"data"`
}

// startStandaloneServer creates and starts a standalone server on a random port.
// The optional modify callback is applied to the config before creating the
// server, matching the startTestServerWithConfig pattern in server_test.go.
func startStandaloneServer(t *testing.T, modify ...func(*config.Config)) (*server.Server, config.Config) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Port = 0
	cfg.Standalone = true
	for _, fn := range modify {
		fn(&cfg)
	}

	srv := server.New(cfg, discardLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown error: %v", err)
		}
	})

	return srv, cfg
}

// connectWS dials a WebSocket connection to the server on buffer 1
// and waits for the server to register the client.
//
// Warning: this replaces the server's OnClientChange callback. Do not
// call multiple times on the same server unless the prior call's
// registration has already completed.
func connectWS(t *testing.T, ctx context.Context, srv *server.Server) *websocket.Conn {
	t.Helper()

	addr := srv.Addr().String()

	ready := make(chan struct{}, 1)
	srv.SetOnClientChange(func(hasClients bool) {
		// Only signal on connect (hasClients == true). The callback also
		// fires on disconnect (hasClients == false), which can race with
		// a subsequent connectWS call: the disconnect of a previous client
		// would signal the new ready channel before the new client connects,
		// causing the caller to proceed before registration is complete.
		if hasClients {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})

	conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://%s/ws?bufnr=%d", addr, server.StandaloneBufferNr), nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for client registration")
	}

	return conn
}

// readRefreshMessage reads a single refresh message from the WebSocket.
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

func TestBroadcastFile(t *testing.T) {
	t.Run("custom page title", func(t *testing.T) {
		// Use a page title distinct from the filename so the assertion
		// can distinguish config-sourced title from filepath.Base fallback.
		srv, cfg := startStandaloneServer(t, func(c *config.Config) {
			c.PageTitle = "My Custom Title"
		})

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		conn := connectWS(t, ctx, srv)

		content := []byte("# Hello\n\nWorld\n")
		file := filepath.Join(t.TempDir(), "test.md")

		broadcastFile(srv, cfg, file, content)

		msg := readRefreshMessage(t, ctx, conn)

		if msg.Event != "refresh_content" {
			t.Errorf("expected event 'refresh_content', got %q", msg.Event)
		}
		if msg.Data.Name != "test.md" {
			t.Errorf("expected name 'test.md', got %q", msg.Data.Name)
		}
		if msg.Data.PageTitle != "My Custom Title" {
			t.Errorf("expected page title 'My Custom Title', got %q", msg.Data.PageTitle)
		}
		// Assert on specific named lines rather than total count so the test
		// is not coupled to how trailing newlines are split. The input is
		// "# Hello\n\nWorld\n"; at minimum we expect all three meaningful
		// elements (heading, blank, paragraph) to be present.
		if len(msg.Data.Content) < 3 {
			t.Fatalf("expected at least 3 content lines, got %d: %v", len(msg.Data.Content), msg.Data.Content)
		}
		if msg.Data.Content[0] != "# Hello" {
			t.Errorf("expected first line '# Hello', got %q", msg.Data.Content[0])
		}
		if msg.Data.Content[1] != "" {
			t.Errorf("expected second line (blank separator) to be empty, got %q", msg.Data.Content[1])
		}
		if msg.Data.Content[2] != "World" {
			t.Errorf("expected third line 'World', got %q", msg.Data.Content[2])
		}
		if msg.Data.Theme != "" {
			t.Errorf("expected empty theme from DefaultConfig, got %q", msg.Data.Theme)
		}

		// Scroll-sync fields set by NewStandaloneRefreshData.
		// Cursor is [bufnum, lnum, col, off] per Vim getpos(".").
		if len(msg.Data.Cursor) != 4 || msg.Data.Cursor[0] != 1 || msg.Data.Cursor[1] != 1 ||
			msg.Data.Cursor[2] != 0 || msg.Data.Cursor[3] != 0 {
			t.Errorf("expected cursor [1,1,0,0], got %v", msg.Data.Cursor)
		}
		if msg.Data.WinLine != 1 {
			t.Errorf("expected WinLine=1, got %d", msg.Data.WinLine)
		}
		// Pin to the literal value (config.defaultWinHeight, config/config.go:122)
		// so a regression that changes the constant is caught rather than
		// silently accepted.
		if msg.Data.WinHeight != 40 {
			t.Errorf("expected WinHeight=40 (config.defaultWinHeight), got %d", msg.Data.WinHeight)
		}

		// Options are forwarded from DefaultConfig().PreviewOptions.
		// JSON decodes map numeric values as float64, so hide_yaml_meta
		// arrives as float64(1) even though it was set as int(1).
		if msg.Data.Options == nil {
			t.Fatal("expected Options to be non-nil")
		}
		if v, ok := msg.Data.Options["sync_scroll_type"].(string); !ok || v != "middle" {
			t.Errorf("expected Options[sync_scroll_type]=%q, got %v", "middle", msg.Data.Options["sync_scroll_type"])
		}
		if v, ok := msg.Data.Options["hide_yaml_meta"].(float64); !ok || v != 1 {
			t.Errorf("expected Options[hide_yaml_meta]=float64(1), got %v (%T)", msg.Data.Options["hide_yaml_meta"], msg.Data.Options["hide_yaml_meta"])
		}
	})

	t.Run("default page title passthrough", func(t *testing.T) {
		// PageTitle is intentionally left as DefaultConfig's "${name}" to
		// verify that the template string passes through to the broadcast
		// unchanged. The browser is responsible for substituting ${name}
		// with the buffer basename; the Go layer must not alter it.
		srv, cfg := startStandaloneServer(t)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		conn := connectWS(t, ctx, srv)

		content := []byte("# Hello\n")
		file := filepath.Join(t.TempDir(), "default.md")

		broadcastFile(srv, cfg, file, content)

		msg := readRefreshMessage(t, ctx, conn)

		if msg.Event != "refresh_content" {
			t.Errorf("expected event 'refresh_content', got %q", msg.Event)
		}
		// The default PageTitle template must reach the browser verbatim.
		// If broadcastFile were to resolve "${name}" server-side, the
		// browser's own substitution logic would receive a pre-resolved
		// value and document.title would show the wrong string.
		if msg.Data.PageTitle != "${name}" {
			t.Errorf("expected page title %q (default template), got %q", "${name}", msg.Data.PageTitle)
		}
	})
}

func TestWatchFileDetectsChange(t *testing.T) {
	srv, cfg := startStandaloneServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Create the file with initial content.
	dir := t.TempDir()
	file := filepath.Join(dir, "watch.md")
	if err := os.WriteFile(file, []byte("# Initial\n"), 0o644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	// Stat the file before starting the watcher to establish the
	// baseline mod time (matching runStandalone behavior, which stats
	// after broadcastFile so the watcher and broadcaster share the same
	// baseline and cannot race).
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("failed to stat initial file: %v", err)
	}
	initialMod := info.ModTime()

	conn := connectWS(t, ctx, srv)

	// Broadcast initial content (matching runStandalone behavior).
	broadcastFile(srv, cfg, file, []byte("# Initial\n"))

	// Drain the initial broadcast so we only check for the change.
	_ = readRefreshMessage(t, ctx, conn)

	// Start the watcher with the pre-determined baseline.
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watchFile(watchCtx, discardLogger(), srv, cfg, file, initialMod)
	}()

	// Write to a temp file, bump its mtime, then rename atomically.
	// This avoids a race where the watcher could poll between WriteFile
	// and Chtimes, seeing the file's natural mtime before the forced
	// future time is applied.
	tmpFile := file + ".tmp"
	if err := os.WriteFile(tmpFile, []byte("# Updated\n"), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	// On filesystems with 1-second mtime granularity (ext3, HFS+), the
	// initial stat and write may land in the same second, making the
	// mtime indistinguishable. Bump the mtime 2 seconds into the future
	// to guarantee the watcher detects the change.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(tmpFile, future, future); err != nil {
		t.Fatalf("failed to bump mtime: %v", err)
	}
	if err := os.Rename(tmpFile, file); err != nil {
		t.Fatalf("failed to rename temp file: %v", err)
	}

	// Read the broadcast triggered by the file change.
	msg := readRefreshMessage(t, ctx, conn)

	if msg.Event != "refresh_content" {
		t.Errorf("expected event 'refresh_content', got %q", msg.Event)
	}
	if len(msg.Data.Content) == 0 || msg.Data.Content[0] != "# Updated" {
		t.Errorf("expected updated content '# Updated', got %v", msg.Data.Content)
	}

	// Clean up: cancel the watcher and wait for it to exit.
	watchCancel()
	select {
	case <-watchDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for watcher to stop")
	}
}

func TestWatchFileSkipsOversizeFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "oversize.md")

	// Write maxFileSize+1 bytes so the watcher's size check triggers.
	content := make([]byte, maxFileSize+1)
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatalf("failed to write oversize file: %v", err)
	}

	srv, cfg := startStandaloneServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	conn := connectWS(t, ctx, srv)

	// broadcastReceived is closed if any WebSocket message arrives.
	broadcastReceived := make(chan struct{})
	go func() {
		_, _, err := conn.Read(ctx)
		if err != nil {
			return // connection closed or context cancelled
		}
		close(broadcastReceived)
	}()

	// Use a zero baseline so the watcher sees the file as changed on the
	// first poll and exercises the size check immediately.
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	go func() { watchFile(watchCtx, discardLogger(), srv, cfg, file, time.Time{}) }()

	// Allow 3 poll intervals for the watcher to pick up the file change and
	// confirm it did not broadcast. If a broadcast were going to occur, it
	// would within this window.
	noBroadcastCtx, noBroadcastCancel := context.WithTimeout(context.Background(), 3*fileWatchInterval)
	defer noBroadcastCancel()
	select {
	case <-broadcastReceived:
		t.Error("watchFile broadcast oversize file content; expected skip due to size limit")
	case <-noBroadcastCtx.Done():
		// No broadcast -- watcher correctly skipped the oversize file.
	}
}

// fakeAddr is a non-TCP net.Addr implementation used to test the
// fallback branch of portFromAddr.
type fakeAddr struct{}

func (fakeAddr) Network() string { return "unix" }
func (fakeAddr) String() string  { return "/tmp/fake.sock" }

func TestPortFromAddr(t *testing.T) {
	tests := []struct {
		name string
		addr net.Addr
		want int
	}{
		{
			name: "TCP addr with port 8080",
			addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080},
			want: 8080,
		},
		{
			name: "TCP addr with port 0",
			addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
			want: 0,
		},
		{
			name: "nil addr",
			addr: nil,
			want: 0,
		},
		{
			name: "non-TCP addr",
			addr: fakeAddr{},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := portFromAddr(tt.addr)
			if got != tt.want {
				t.Errorf("portFromAddr(%v) = %d, want %d", tt.addr, got, tt.want)
			}
		})
	}
}

func TestWatchFileExitsOnDeletion(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "delete-me.md")
	if err := os.WriteFile(file, []byte("# Temporary\n"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Stat before starting the watcher so the baseline is the file's
	// known mod time, not a racing independent stat inside watchFile.
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	initialMod := info.ModTime()

	srv, cfg := startStandaloneServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Connect a WebSocket client to observe any broadcasts from the watcher.
	// No broadcasts should occur between watcher start and file deletion;
	// this assertion guards against regressions where the watcher broadcasts
	// stale content before exiting.
	conn := connectWS(t, ctx, srv)

	// broadcastReceived is closed if any WebSocket message arrives.
	broadcastReceived := make(chan struct{})
	go func() {
		_, _, err := conn.Read(ctx)
		if err != nil {
			// Connection closed or context cancelled -- not a broadcast.
			return
		}
		close(broadcastReceived)
		// The broadcastReceived goroutine exits when conn.CloseNow() fires
		// in t.Cleanup. The 200ms assertion window below is the authoritative
		// check; the goroutine's lifetime extends beyond it but cannot
		// produce false positives because broadcastReceived is unbuffered
		// and only checked within the window.
	}()

	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watchFile(ctx, discardLogger(), srv, cfg, file, initialMod)
	}()

	if err := os.Remove(file); err != nil {
		t.Fatalf("failed to delete file: %v", err)
	}

	// The watcher should exit on its own after detecting the deletion.
	select {
	case <-watchDone:
		// Success -- watcher exited.
	case <-ctx.Done():
		t.Fatal("timed out waiting for watcher to exit after file deletion")
	}

	// Verify that no broadcast occurred while the watcher was running.
	// Allow a brief window after watchDone to catch any in-flight messages.
	noBroadcastCtx, noBroadcastCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer noBroadcastCancel()

	select {
	case <-broadcastReceived:
		t.Error("watchFile broadcast content after file deletion; expected no broadcast on deletion")
	case <-noBroadcastCtx.Done():
		// No broadcast within the window -- correct behavior.
	}
}
