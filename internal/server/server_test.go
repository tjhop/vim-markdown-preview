package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tjhop/vim-markdown-preview/internal/config"
)

// discardLogger returns a logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// defaultReloadFirstLine is the first line of defaultReloadContent (defined
// in routes.go), extracted once at init time so tests stay in sync with the
// constant rather than hardcoding the string.
var defaultReloadFirstLine = strings.SplitN(defaultReloadContent, "\n", 2)[0]

// testPNG is a minimal valid 1x1 pixel PNG used by image-serving tests.
var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
	0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
	0x00, 0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc,
	0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

// startTestServerWithConfig creates and starts a server with the given config
// modifier applied to DefaultConfig. The server is shut down on test cleanup.
func startTestServerWithConfig(t *testing.T, modify func(*config.Config)) *Server {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Port = 0 // random port
	if modify != nil {
		modify(&cfg)
	}

	srv := New(cfg, discardLogger())
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

	return srv
}

// startTestServer creates and starts a server on a random port for testing.
func startTestServer(t *testing.T) *Server {
	t.Helper()
	return startTestServerWithConfig(t, nil)
}

// decodePagePayload extracts and base64-decodes the data-payload attribute
// from an HTML page body. It calls t.Fatal if the attribute is missing or
// malformed.
func decodePagePayload(t *testing.T, bodyStr string) []byte {
	t.Helper()
	const prefix = `data-payload="`
	idx := strings.Index(bodyStr, prefix)
	if idx == -1 {
		t.Fatal("expected data-payload attribute in response")
	}
	start := idx + len(prefix)
	end := strings.Index(bodyStr[start:], `"`)
	if end == -1 {
		t.Fatal("data-payload attribute not properly terminated")
	}
	encoded := bodyStr[start : start+end]
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("failed to decode base64 payload: %v", err)
	}
	return decoded
}

func TestPageHandler(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	resp, err := http.Get("http://" + addr + "/page/1")
	if err != nil {
		t.Fatalf("GET /page/1 failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected Content-Type text/html, got %q", ct)
	}

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Error("expected Content-Security-Policy header, got empty")
	} else {
		for _, directive := range []string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-eval'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' https: data:",
			"font-src 'self'",
			"connect-src 'self' ws: wss:",
			"object-src 'none'",
			"base-uri 'none'",
			"frame-ancestors 'none'",
		} {
			if !strings.Contains(csp, directive) {
				t.Errorf("CSP missing directive %q; full header: %q", directive, csp)
			}
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if !strings.Contains(string(body), "Markdown Preview") {
		t.Error("response body does not contain expected title")
	}
}

func TestStaticAssetServing(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// Test that a known vendored JS file is served with the correct
	// status, content type, and non-empty body.
	resp, err := http.Get("http://" + addr + "/_static/js/vendor/markdown-it.min.js")
	if err != nil {
		t.Fatalf("GET static JS failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for markdown-it.min.js, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/javascript") && !strings.Contains(ct, "text/javascript") {
		t.Errorf("expected JavaScript Content-Type, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if len(body) == 0 {
		t.Error("expected non-empty JS file")
	}
}

func TestCustomCSSFallback(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// With no custom CSS configured, should serve the embedded fallback.
	resp, err := http.Get("http://" + addr + "/_static/markdown.css")
	if err != nil {
		t.Fatalf("GET markdown.css failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for markdown.css fallback, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("expected Content-Type text/css, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if len(body) == 0 {
		t.Error("expected non-empty CSS body from fallback")
	}
}

func TestHighlightCSSFallback(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	resp, err := http.Get("http://" + addr + "/_static/highlight.css")
	if err != nil {
		t.Fatalf("GET highlight.css failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for highlight.css fallback, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("expected Content-Type text/css, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	css := string(body)
	if !strings.Contains(css, "prefers-color-scheme: dark") {
		t.Error("highlight.css fallback should contain prefers-color-scheme media query")
	}
	if !strings.Contains(css, "[data-theme=\"dark\"]") {
		t.Error("highlight.css fallback should contain data-theme dark override")
	}
}

func TestLocalImageRejectsNonImage(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	resp, err := http.Get("http://" + addr + "/_local_image/etc/passwd")
	if err != nil {
		t.Fatalf("GET /_local_image failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403 for non-image file, got %d", resp.StatusCode)
	}
}

func TestLocalImageRejectsPathTraversal(t *testing.T) {
	// Test the handler directly with httptest to bypass the mux's
	// built-in path cleaning (which issues 301 redirects for ".."
	// components). This verifies the handler-level defense-in-depth.
	srv := startTestServer(t)

	traversalPaths := []string{
		"../../etc/passwd.png",
		"../secret.jpg",
		"foo/../../etc/shadow.gif",
	}

	for _, p := range traversalPaths {
		req := httptest.NewRequest(http.MethodGet, "/_local_image/"+p, nil)
		req.SetPathValue("path", p)
		w := httptest.NewRecorder()

		srv.handleLocalImage().ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("path %q: expected status 403, got %d", p, w.Code)
		}
	}
}

func TestLocalImageServesValidFile(t *testing.T) {
	// Create a temporary PNG file in a temp directory, then start a
	// server whose working directory is that temp directory so the
	// relative path resolves correctly.
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "test.png"), testPNG, 0o644); err != nil {
		t.Fatalf("failed to write test PNG: %v", err)
	}

	// chdir so the relative path "test.png" resolves inside the temp dir.
	// t.Chdir (Go 1.24+) is safe for parallel tests unlike os.Chdir.
	t.Chdir(dir)

	srv := startTestServer(t)
	addr := srv.Addr().String()

	resp, err := http.Get("http://" + addr + "/_local_image/test.png")
	if err != nil {
		t.Fatalf("GET /_local_image/test.png failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for valid image, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected Content-Type image/png, got %q", ct)
	}
}

func TestPageCSS(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	resp, err := http.Get("http://" + addr + "/_static/css/page.css")
	if err != nil {
		t.Fatalf("GET page.css failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for page.css, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if !strings.Contains(string(body), "--bg-color") {
		t.Error("page.css does not contain expected CSS variable")
	}
}

func TestLocalImageWithImagesPath(t *testing.T) {
	// Create a directory structure where the image lives under a
	// dedicated images directory, not the working directory.
	imgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(imgDir, "photo.png"), testPNG, 0o644); err != nil {
		t.Fatalf("failed to write test PNG: %v", err)
	}

	srv := startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.ImagesPath = imgDir
	})

	addr := srv.Addr().String()

	// The image lives at imgDir/photo.png, not cwd/photo.png.
	resp, err := http.Get("http://" + addr + "/_local_image/photo.png")
	if err != nil {
		t.Fatalf("GET /_local_image/photo.png failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for image served via ImagesPath, got %d", resp.StatusCode)
	}
}

func TestLocalImageSymlinkEscape(t *testing.T) {
	// Create an images directory and an outside directory with a real file.
	imgDir := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "secret.png")
	if err := os.WriteFile(outsideFile, testPNG, 0o644); err != nil {
		t.Fatalf("failed to write outside PNG: %v", err)
	}

	// Create a symlink inside the images directory pointing outside it.
	symlinkPath := filepath.Join(imgDir, "escape.png")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	srv := startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.ImagesPath = imgDir
	})
	addr := srv.Addr().String()

	// The handler should reject this because the resolved path
	// (after EvalSymlinks) escapes the images directory.
	resp, err := http.Get("http://" + addr + "/_local_image/escape.png")
	if err != nil {
		t.Fatalf("GET /_local_image/escape.png failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403 for symlink escaping images directory, got %d", resp.StatusCode)
	}

	// Read the body to confirm the rejection came from the symlink-escape
	// guard specifically, not from a different 403 path. http.Error appends
	// a trailing newline, so we trim before comparing.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if got := strings.TrimSpace(string(respBody)); got != "path escapes images directory" {
		t.Errorf("expected body %q for symlink escape rejection, got %q", "path escapes images directory", got)
	}
}

func TestCustomCSSFromDisk(t *testing.T) {
	dir := t.TempDir()
	cssContent := "body { color: red; }"
	cssFile := filepath.Join(dir, "custom.css")
	if err := os.WriteFile(cssFile, []byte(cssContent), 0o644); err != nil {
		t.Fatalf("failed to write custom CSS: %v", err)
	}

	srv := startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MarkdownCSS = cssFile
	})
	addr := srv.Addr().String()

	resp, err := http.Get("http://" + addr + "/_static/markdown.css")
	if err != nil {
		t.Fatalf("GET markdown.css failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("expected Content-Type %q, got %q", "text/css; charset=utf-8", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if string(body) != cssContent {
		t.Errorf("expected custom CSS content %q, got %q", cssContent, string(body))
	}
}

func TestStandaloneRefreshEndpoint(t *testing.T) {
	srv := startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.Standalone = true
	})
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 1)

	// Connect a WebSocket client before the POST so we can verify
	// the broadcast is delivered.
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

	resp, err := http.Post("http://"+addr+"/-/reload", "", nil)
	if err != nil {
		t.Fatalf("POST /-/reload failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if string(body) != `{"ok": true}` {
		t.Errorf("expected body %q, got %q", `{"ok": true}`, string(body))
	}

	// Verify the broadcast was delivered over WebSocket. handleReload
	// broadcasts synchronously before writing the HTTP response, so the
	// message is already enqueued when the POST returns. conn.Read blocks
	// until the message arrives or the 5s context timeout expires.
	msg := readRefreshMessage(t, ctx, conn)
	if msg.Event != "refresh_content" {
		t.Errorf("expected event 'refresh_content', got %q", msg.Event)
	}
	// The Name field must equal standaloneReloadSource ("stdin") -- the
	// synthetic source name set by the reload endpoint. A broadcast from
	// a different source with matching content would have a different Name.
	if msg.Data.Name != standaloneReloadSource {
		t.Errorf("expected name %q, got %q", standaloneReloadSource, msg.Data.Name)
	}
	if len(msg.Data.Content) == 0 {
		t.Fatal("expected non-empty content from default reload")
	}
	if msg.Data.Content[0] != defaultReloadFirstLine {
		t.Errorf("expected first content line %q, got %q",
			defaultReloadFirstLine, msg.Data.Content[0])
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestStandaloneRefreshWithContent(t *testing.T) {
	srv := startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.Standalone = true
	})
	addr := srv.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready := awaitNConnects(srv, 1)

	// Connect a WebSocket client before the POST so we can verify
	// the broadcast delivers the custom content.
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

	payload := bytes.NewBufferString(`{"content": "# Custom"}`)
	resp, err := http.Post("http://"+addr+"/-/reload", "application/json", payload)
	if err != nil {
		t.Fatalf("POST /-/reload with content failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify the broadcast delivers the custom content over WebSocket.
	// handleReload broadcasts synchronously, so the message is already
	// enqueued when the POST returns. conn.Read blocks until it arrives
	// or the 5s context timeout expires.
	msg := readRefreshMessage(t, ctx, conn)
	if msg.Event != "refresh_content" {
		t.Errorf("expected event 'refresh_content', got %q", msg.Event)
	}

	// The handler splits content on newlines, so "# Custom" (no trailing
	// newline) becomes a single-element slice ["# Custom"].
	if len(msg.Data.Content) != 1 || msg.Data.Content[0] != "# Custom" {
		t.Errorf("expected broadcast content [\"# Custom\"], got %v", msg.Data.Content)
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestHandleReloadEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        func() io.Reader
	}{
		{
			name:        "nil body",
			contentType: "",
			body:        func() io.Reader { return nil },
		},
		{
			name:        "malformed JSON",
			contentType: "application/json",
			body:        func() io.Reader { return strings.NewReader(`{invalid json}`) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Each subtest creates its own server to avoid sharing
			// SetOnClientChange state across subtests, which would
			// be racy if subtests ever ran in parallel.
			srv := startTestServerWithConfig(t, func(cfg *config.Config) {
				cfg.Standalone = true
			})
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

			resp, err := http.Post("http://"+addr+"/-/reload", tc.contentType, tc.body())
			if err != nil {
				t.Fatalf("POST /-/reload failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			// Nil body and malformed JSON fall through to default
			// content and return 200.
			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected status 200, got %d", resp.StatusCode)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read response body: %v", err)
			}
			if string(body) != `{"ok": true}` {
				t.Errorf("expected body %q, got %q", `{"ok": true}`, string(body))
			}

			// Verify the handler broadcast default content over WebSocket.
			// handleReload broadcasts synchronously, so the message is
			// already enqueued when the POST returns. conn.Read blocks
			// until it arrives or the 5s context timeout expires.
			msg := readRefreshMessage(t, ctx, conn)
			if msg.Event != "refresh_content" {
				t.Errorf("expected event 'refresh_content', got %q", msg.Event)
			}
			if len(msg.Data.Content) == 0 {
				t.Fatal("expected non-empty content from default reload")
			}
			if msg.Data.Content[0] != defaultReloadFirstLine {
				t.Errorf("expected first content line %q, got %q",
					defaultReloadFirstLine, msg.Data.Content[0])
			}

			_ = conn.Close(websocket.StatusNormalClosure, "")
		})
	}

	t.Run("oversized body returns 413", func(t *testing.T) {
		srv := startTestServerWithConfig(t, func(cfg *config.Config) {
			cfg.Standalone = true
		})
		addr := srv.Addr().String()

		// Body is 2x maxReloadBodySize (2 MB vs 1 MB limit). The
		// MaxBytesReader triggers http.MaxBytesError mid-stream,
		// and the handler returns 413 Request Entity Too Large
		// without broadcasting to any clients.
		oversizedBody := io.MultiReader(
			strings.NewReader(`{"content":"`),
			bytes.NewReader(bytes.Repeat([]byte("a"), 2<<20)),
			strings.NewReader(`"}`),
		)

		resp, err := http.Post("http://"+addr+"/-/reload", "application/json", oversizedBody)
		if err != nil {
			t.Fatalf("POST /-/reload failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("expected status 413, got %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		if got := strings.TrimSpace(string(body)); got != "request body too large" {
			t.Errorf("expected body %q, got %q", "request body too large", got)
		}
	})

	t.Run("wrong content type returns 415", func(t *testing.T) {
		srv := startTestServerWithConfig(t, func(cfg *config.Config) {
			cfg.Standalone = true
		})
		addr := srv.Addr().String()

		resp, err := http.Post("http://"+addr+"/-/reload", "text/plain", strings.NewReader("hello"))
		if err != nil {
			t.Fatalf("POST /-/reload failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("expected status 415, got %d", resp.StatusCode)
		}
	})
}

func TestCustomCSSFallbackOnMissingFile(t *testing.T) {
	srv := startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MarkdownCSS = "/nonexistent/path.css"
	})
	addr := srv.Addr().String()

	resp, err := http.Get("http://" + addr + "/_static/markdown.css")
	if err != nil {
		t.Fatalf("GET markdown.css failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for CSS fallback on missing file, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("expected Content-Type text/css, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if len(body) == 0 {
		t.Error("expected non-empty CSS body from fallback when custom file is missing")
	}
}

func TestNonStandaloneReloadReturns404(t *testing.T) {
	srv := startTestServer(t) // default config has Standalone=false
	addr := srv.Addr().String()

	resp, err := http.Post("http://"+addr+"/-/reload", "", nil)
	if err != nil {
		t.Fatalf("POST /-/reload failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The /-/reload endpoint is only registered in standalone mode.
	// A non-standalone server returns 404 for this path.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for /-/reload on non-standalone server, got %d", resp.StatusCode)
	}
}

func TestPageHandlerWithCachedContent(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// Populate the cache by broadcasting to buffer 1 before any
	// WebSocket clients connect.
	srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
		Content: []string{"# Cached", "", "Hello from cache"},
		Name:    "cached.md",
	})

	resp, err := http.Get("http://" + addr + "/page/1")
	if err != nil {
		t.Fatalf("GET /page/1 failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	bodyStr := string(body)

	// The response should contain the initial-data element.
	if !strings.Contains(bodyStr, `id="initial-data"`) {
		t.Fatal("expected initial-data element in response")
	}

	// Extract the base64 payload and decode it.
	decoded := decodePagePayload(t, bodyStr)

	// Verify the decoded payload matches what was broadcast.
	var msg refreshMessage
	if err := json.Unmarshal(decoded, &msg); err != nil {
		t.Fatalf("failed to unmarshal decoded payload: %v", err)
	}
	if msg.Event != "refresh_content" {
		t.Errorf("expected event 'refresh_content', got %q", msg.Event)
	}
	if msg.Data.Name != "cached.md" {
		t.Errorf("expected name 'cached.md', got %q", msg.Data.Name)
	}
	if len(msg.Data.Content) != 3 || msg.Data.Content[0] != "# Cached" {
		t.Errorf("unexpected content: %v", msg.Data.Content)
	}
}

func TestPageHandlerNoCachedContent(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// No broadcast -- cache is empty.
	resp, err := http.Get("http://" + addr + "/page/1")
	if err != nil {
		t.Fatalf("GET /page/1 failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	bodyStr := string(body)

	// No initial-data element should be present.
	if strings.Contains(bodyStr, `id="initial-data"`) {
		t.Error("expected no initial-data element when cache is empty")
	}

	// The loading skeleton should be present.
	if !strings.Contains(bodyStr, "loading-skeleton") {
		t.Error("expected loading-skeleton markup when no cached content exists")
	}

	// The placeholder comment should have been removed.
	if strings.Contains(bodyStr, "<!-- INITIAL_DATA_PLACEHOLDER -->") {
		t.Error("expected INITIAL_DATA_PLACEHOLDER to be removed from response")
	}
}

func TestPageHandlerDifferentBuffers(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// Broadcast different content to buffers 1 and 2.
	srv.BroadcastToBuffer(1, "refresh_content", config.RefreshData{
		Content: []string{"buffer one"},
		Name:    "buf1.md",
	})
	srv.BroadcastToBuffer(2, "refresh_content", config.RefreshData{
		Content: []string{"buffer two"},
		Name:    "buf2.md",
	})

	for _, tc := range []struct {
		bufnr        string
		expectedName string
		hasData      bool
	}{
		{"1", "buf1.md", true},
		{"2", "buf2.md", true},
		{"3", "", false}, // No cached content for buffer 3.
	} {
		t.Run("buffer_"+tc.bufnr, func(t *testing.T) {
			resp, err := http.Get("http://" + addr + "/page/" + tc.bufnr)
			if err != nil {
				t.Fatalf("GET /page/%s failed: %v", tc.bufnr, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("buffer %s: expected status 200, got %d", tc.bufnr, resp.StatusCode)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read response body: %v", err)
			}

			bodyStr := string(body)
			hasInitialData := strings.Contains(bodyStr, `id="initial-data"`)

			if tc.hasData && !hasInitialData {
				t.Errorf("buffer %s: expected initial-data element", tc.bufnr)
			}
			if !tc.hasData && hasInitialData {
				t.Errorf("buffer %s: expected no initial-data element", tc.bufnr)
			}

			if tc.hasData {
				// Extract and verify the name in the payload.
				decoded := decodePagePayload(t, bodyStr)
				var msg refreshMessage
				if err := json.Unmarshal(decoded, &msg); err != nil {
					t.Fatalf("buffer %s: unmarshal error: %v", tc.bufnr, err)
				}
				if msg.Data.Name != tc.expectedName {
					t.Errorf("buffer %s: expected name %q, got %q", tc.bufnr, tc.expectedName, msg.Data.Name)
				}
			}
		})
	}
}

func TestPageHandlerInvalidBufnr(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr().String()

	// GET /page/abc -- invalid buffer number should still serve the page
	// gracefully (no initial-data, skeleton shown).
	resp, err := http.Get("http://" + addr + "/page/abc")
	if err != nil {
		t.Fatalf("GET /page/abc failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for invalid bufnr, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	bodyStr := string(body)
	if strings.Contains(bodyStr, `id="initial-data"`) {
		t.Error("expected no initial-data element for invalid bufnr")
	}
	if !strings.Contains(bodyStr, "Markdown Preview") {
		t.Error("response body does not contain expected title")
	}
}

func TestShutdownIdempotent(t *testing.T) {
	// Manage the server lifecycle manually so we can call Shutdown
	// multiple times without relying on t.Cleanup.
	cfg := config.DefaultConfig()
	cfg.Port = 0
	srv := New(cfg, discardLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	// Use context.Background -- t.Context() is cancelled when the test
	// function returns, which can race with Shutdown's internal work.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First shutdown should succeed.
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown failed: %v", err)
	}

	// Second shutdown should be a no-op (no panic, no error).
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown returned error: %v", err)
	}
}

func TestLocalImageMissingFile(t *testing.T) {
	imgDir := t.TempDir()

	srv := startTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.ImagesPath = imgDir
	})
	addr := srv.Addr().String()

	// Request an image that does not exist in the images directory.
	// filepath.EvalSymlinks fails on the nonexistent path, causing
	// the handler to respond with 404.
	resp, err := http.Get("http://" + addr + "/_local_image/nonexistent.png")
	if err != nil {
		t.Fatalf("GET /_local_image/nonexistent.png failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for missing image file, got %d", resp.StatusCode)
	}
}

func TestShutdownBeforeStart(t *testing.T) {
	// New starts the dispatch goroutine immediately. Calling Shutdown
	// before Start must cleanly stop that goroutine without panicking
	// or deadlocking, even though s.httpServer is nil.
	cfg := config.DefaultConfig()
	srv := New(cfg, discardLogger())

	// Use context.Background -- t.Context() is cancelled when the test
	// function returns, which can race with Shutdown's internal work.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown before Start returned error: %v", err)
	}

	// Broadcast methods should be safe to call on a pre-Start server
	// (wsClients is initialized in New, not Start).
	srv.BroadcastTransientToBuffer(1, "test", nil)
	srv.BroadcastAll("test", nil)
}
