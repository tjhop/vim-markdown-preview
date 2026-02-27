package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tjhop/vim-markdown-preview/internal/config"
	"github.com/tjhop/vim-markdown-preview/internal/editor"
	"github.com/tjhop/vim-markdown-preview/web"
)

// StandaloneBufferNr is the fixed Vim buffer number used in standalone mode.
// Standalone mode operates on a single virtual buffer; this constant is
// shared between the server (which routes WebSocket clients by buffer number)
// and the main entry point (which broadcasts content and constructs preview URLs).
const StandaloneBufferNr = 1

// contentSecurityPolicy is the Content-Security-Policy header value applied
// to every page response. All scripts and styles are served from the embedded
// /_static/ path ('self'). Several vendored libraries (mermaid, Chart.js,
// Viz.js, webfontloader, raphael) require eval() at runtime, necessitating
// 'unsafe-eval'. Dynamic inline styles in preview.js (error banners) require
// 'unsafe-inline' for style-src. Images may be data: URIs, local files, or
// fetched from external HTTPS servers (e.g. PlantUML). WebSocket connections
// go to the same host:port as the page; CSP Level 3 'self' covers same-origin
// ws: connections (Chrome 44+, Firefox 36+, Safari 10+), so only wss: is
// listed explicitly to permit TLS-proxied deployments. ws: is listed alongside
// wss: because CSP Level 2 and some browsers do not include ws: in the 'self'
// match (MDN confirms this is a cross-browser issue, not spec-guaranteed).
// object-src and base-uri are explicitly denied to prevent plugin loading
// and <base> tag injection in rendered markdown (which may contain raw HTML
// when html:true is set).
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' https: data:; " +
	"font-src 'self'; " +
	"connect-src 'self' ws: wss:; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// setSecurityHeaders applies defense-in-depth HTTP headers to responses
// that serve file content (custom CSS, local images). These complement
// the headers set on the page handler. Referrer-Policy prevents the
// local preview URL from leaking via the Referer header when the
// browser follows external image links.
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// withTimeout wraps an http.Handler with http.TimeoutHandler so slow
// or stalled clients cannot hold a server goroutine indefinitely. This
// is used for all non-WebSocket routes; the WebSocket handler is excluded
// because its connections are long-lived.
func withTimeout(h http.Handler) http.Handler {
	return http.TimeoutHandler(h, httpHandlerTimeout, "request timed out")
}

// registerRoutes wires up all HTTP handlers on the given mux.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Static assets from the embedded filesystem, served at /_static/.
	mux.Handle("GET /_static/markdown.css", withTimeout(s.handleCustomCSS("markdown.css", s.cfg.MarkdownCSS, "css/github-markdown.css")))
	mux.Handle("GET /_static/highlight.css", withTimeout(s.handleCustomCSS("highlight.css", s.cfg.HighlightCSS, "css/highlight-github-combined.css")))
	mux.Handle("GET /_static/", withTimeout(s.handleStatic()))

	// Preview page: /page/{bufnr} serves index.html with optional
	// cached content injected for first-paint rendering.
	mux.Handle("GET /page/{bufnr}", withTimeout(s.handlePage()))

	// Local image proxy: serves files from the local filesystem.
	mux.Handle("GET /_local_image/{path...}", withTimeout(s.handleLocalImage()))

	// WebSocket endpoint: NOT wrapped with withTimeout because
	// WebSocket connections are long-lived.
	mux.HandleFunc("GET /ws", s.handleWebSocket)

	// Standalone reload endpoint: POST sends content to all clients.
	if s.cfg.Standalone {
		mux.Handle("POST /-/reload", withTimeout(http.HandlerFunc(s.handleReload)))
	}
}

// handlePage returns a handler that serves the preview page. The embedded
// index.html template is read once at handler creation time; only
// bytes.Replace runs per request (~3KB haystack, negligible cost).
func (s *Server) handlePage() http.Handler {
	indexTemplate, err := web.Assets.ReadFile("index.html")
	if err != nil {
		s.logger.Error("failed to read embedded index.html", "err", err)
		indexTemplate = []byte("internal error: index.html missing")
	}
	placeholder := []byte("<!-- INITIAL_DATA_PLACEHOLDER -->")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		setSecurityHeaders(w)
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)

		// Inject cached content as a base64-encoded data attribute so
		// the browser can render on first paint, before the WebSocket
		// connects. base64.StdEncoding uses only A-Z, a-z, 0-9, +, /,
		// and = -- none of which is the " character that would break
		// the attribute value. If no cached content exists, the
		// placeholder is simply removed and the loading skeleton shows.
		var replacement []byte
		bufnrStr := r.PathValue("bufnr")
		if bufnr, err := strconv.Atoi(bufnrStr); err == nil && bufnr >= 1 {
			if cached := s.wsClients.getLastMessage(bufnr); cached != nil {
				encoded := base64.StdEncoding.EncodeToString(cached)
				replacement = []byte(`<div id="initial-data" data-payload="` + encoded + `" hidden></div>`)
			}
		}

		page := bytes.Replace(indexTemplate, placeholder, replacement, 1)
		_, _ = w.Write(page)
	})
}

// handleStatic serves embedded web assets under the /_static/ prefix.
// Requests to /_static/js/vendor/foo.js map to web.Assets path js/vendor/foo.js.
//
// Note: vendor-manifest.json is included in the embedded filesystem and
// will be served by this handler. This is informational only and not a
// security concern since it contains no secrets (just library names,
// versions, and public download URLs).
func (s *Server) handleStatic() http.Handler {
	fileServer := http.FileServer(http.FS(web.Assets))
	return http.StripPrefix("/_static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		fileServer.ServeHTTP(w, r)
	}))
}

// handleCustomCSS returns a handler that serves a user-provided CSS file
// if configured, otherwise falls back to an embedded CSS file. The label
// parameter is a human-readable name used in log messages (e.g. "markdown.css").
//
// If customPath is configured, it is resolved and validated once at handler
// creation time, avoiding repeated filepath.Abs and os.Stat syscalls per
// request. customPath is fixed for the server's lifetime (set from config
// at startup).
func (s *Server) handleCustomCSS(label, customPath, fallbackPath string) http.Handler {
	var resolvedPath string
	if customPath != "" {
		absPath, err := filepath.Abs(customPath)
		if err != nil {
			s.logger.Warn("custom CSS path invalid, using fallback",
				"label", label, "path", customPath, "err", err)
		} else if _, err := os.Stat(absPath); err != nil {
			s.logger.Warn("custom CSS file not found, using fallback",
				"label", label, "path", customPath, "err", err)
		} else {
			resolvedPath = absPath
		}
	}

	// Read the embedded fallback CSS once at handler creation time,
	// matching the handlePage pattern (which reads index.html once).
	// This avoids calling web.Assets.ReadFile on every request.
	var fallbackData []byte
	if resolvedPath == "" {
		var err error
		fallbackData, err = web.Assets.ReadFile(fallbackPath)
		if err != nil {
			s.logger.Error("failed to read embedded fallback CSS", "label", label, "err", err)
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if resolvedPath != "" {
			setSecurityHeaders(w)
			// Set Content-Type before ServeFile so it is applied
			// regardless of the file's extension (e.g., .scss, .txt,
			// or no extension). The embedded fallback path sets this
			// header explicitly; keep behavior consistent.
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			http.ServeFile(w, r, resolvedPath)
			return
		}

		if fallbackData == nil {
			http.Error(w, "CSS not found", http.StatusInternalServerError)
			return
		}
		setSecurityHeaders(w)
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(fallbackData)
	})
}

// handleLocalImage returns a handler that serves local images from the
// filesystem. The path is extracted from the URL and must be a local,
// non-traversal path. Only image content types are served as a security
// measure.
//
// If ImagesPath is configured, the base directory is resolved and
// symlinks are expanded once at handler creation time, avoiding repeated
// syscalls per request. If resolution fails at startup (e.g. the directory
// does not yet exist), the error is logged once and every request returns
// a 500 rather than silently skipping the containment check.
//
// Security note: when OpenToTheWorld is enabled, this endpoint is reachable
// from the network. The handler mitigates abuse via extension allowlisting,
// filepath.IsLocal rejection of path traversal, and containment checks when
// ImagesPath is configured. However, without an explicit ImagesPath, images
// are resolved relative to the working directory, which may expose files to
// network clients. Operators using OpenToTheWorld should set ImagesPath to
// restrict the served directory.
func (s *Server) handleLocalImage() http.Handler {
	// Resolve the images base path once at handler creation time.
	// s.cfg.ImagesPath is fixed for the server's lifetime.
	//
	// Three-state machine encoded by (absBase, baseErr):
	//   unconfigured:        absBase == "" && baseErr == nil
	//                        ImagesPath was not set; images are resolved
	//                        relative to the working directory and no
	//                        containment check is applied.
	//   configured-invalid:  absBase == "" && baseErr != nil
	//                        ImagesPath was set but resolution failed at
	//                        startup (e.g. directory does not exist yet);
	//                        every request returns 500 to surface the
	//                        misconfiguration rather than silently skipping
	//                        the containment check.
	//   configured-valid:    absBase != "" && baseErr == nil
	//                        ImagesPath resolved successfully; every request
	//                        is verified to stay within absBase.
	var absBase string
	var baseErr error
	if s.cfg.ImagesPath != "" {
		abs, err := filepath.Abs(s.cfg.ImagesPath)
		if err == nil {
			abs, err = filepath.EvalSymlinks(abs)
		}
		if err != nil {
			s.logger.Warn("images_path resolution failed at startup; image requests will return 500",
				"path", s.cfg.ImagesPath, "err", err)
			baseErr = err
		} else {
			absBase = abs
		}
	} else if s.cfg.OpenToTheWorld {
		// Unconfigured ImagesPath with OpenToTheWorld means image requests
		// are satisfied relative to the working directory with no containment
		// check -- any readable file on the host may be served to network
		// clients. Emit a warning at startup so operators notice.
		s.logger.Warn("images_path not configured; image requests will be served relative to working directory",
			"recommendation", "set ImagesPath to restrict the served directory")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imgPath := r.PathValue("path")
		if imgPath == "" {
			http.Error(w, "missing image path", http.StatusBadRequest)
			return
		}

		// Reject path traversal and absolute paths. filepath.IsLocal returns
		// false for paths containing ".." components, absolute paths, empty
		// paths, and Windows reserved names.
		cleanPath := filepath.Clean(imgPath)
		if !filepath.IsLocal(cleanPath) {
			s.logger.Warn("rejected non-local image path", "raw_path", imgPath, "cleaned", cleanPath)
			http.Error(w, "path must be local", http.StatusForbidden)
			return
		}

		// Only serve files that look like images. SVG files can contain
		// embedded scripts, but the page handler's CSP restricts img-src,
		// so SVG script execution is blocked when loaded via <img> tags.
		ext := strings.ToLower(filepath.Ext(cleanPath))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".bmp", ".ico", ".avif":
			// Allowed.
		default:
			http.Error(w, "not an image", http.StatusForbidden)
			return
		}

		// Startup resolution failed; cannot safely serve images.
		if baseErr != nil {
			http.Error(w, "invalid images path configuration", http.StatusInternalServerError)
			return
		}

		// Resolve against the pre-resolved images base directory if
		// configured, then convert to an absolute path for deterministic
		// behavior. absBase is non-empty only when ImagesPath is set and
		// resolved successfully at startup; using it here avoids re-doing
		// Abs+EvalSymlinks on the base and prevents ambiguity when
		// ImagesPath is a relative path.
		fullPath := cleanPath
		if absBase != "" {
			fullPath = filepath.Join(absBase, cleanPath)
		}

		absPath, err := filepath.Abs(fullPath)
		if err != nil {
			http.Error(w, "invalid image path", http.StatusInternalServerError)
			return
		}

		// Resolve symlinks so the containment check operates on the real
		// filesystem path. Without this, a symlink inside the images
		// directory could point outside it and bypass the prefix check.
		absPath, err = filepath.EvalSymlinks(absPath)
		if err != nil {
			// Not-found is a normal 404; anything else (permission denied,
			// I/O error, etc.) is a server-side problem.
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			s.logger.Warn("symlink resolution failed for image", "path", fullPath, "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// When an explicit images base is configured, verify the resolved
		// path stays within it. filepath.IsLocal already rejects ".." in
		// the URL-supplied component, so this is defense-in-depth against
		// a misconfigured ImagesPath value.
		if absBase != "" {
			if !strings.HasPrefix(absPath, absBase+string(filepath.Separator)) && absPath != absBase {
				s.logger.Warn("image path escapes images directory", "path", absPath, "base", absBase)
				http.Error(w, "path escapes images directory", http.StatusForbidden)
				return
			}
		}

		setSecurityHeaders(w)

		// SVG files can carry embedded scripts, load external resources via
		// CSS @import or <foreignObject>, and embed <iframe> or <object>
		// elements -- all of which execute in a full browsing context when
		// the SVG is opened directly (not via an <img> tag). When
		// OpenToTheWorld is enabled the endpoint is reachable from the
		// network, so a crafted SVG placed in the images directory would be
		// an XSS vector. Lock down the SVG to a strict sandbox that blocks
		// scripts, iframes, object embeds, and CSS imports while still
		// allowing inline presentation attributes and self-referencing images.
		if ext == ".svg" {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'")
		}

		http.ServeFile(w, r, absPath)
	})
}

// standaloneReloadSource is the synthetic content-source name passed to
// NewStandaloneRefreshData by the HTTP reload endpoint. Unlike the file-watch
// path (which uses the real file basename), the reload endpoint has no file;
// "stdin" is a conventional placeholder indicating that content arrived via
// the POST body rather than a named file.
const standaloneReloadSource = "stdin"

// defaultReloadContent is placeholder demo markdown used by handleReload
// when no custom content is provided in the request body. It demonstrates
// that the standalone reload endpoint is working and exercises several
// markdown features (headings, bold, lists, fenced code blocks).
const defaultReloadContent = `# Hello from vim-markdown-preview

This is a **test** of the standalone preview mode.

## Features

- Live markdown preview
- WebSocket relay
- Embedded static assets

` + "```go\nfunc main() {\n    fmt.Println(\"Hello, world!\")\n}\n```\n"

// maxReloadBodySize is the maximum request body size for the standalone
// reload endpoint. Limits memory consumption from untrusted POST bodies.
const maxReloadBodySize = 1 << 20 // 1 MB

// handleReload is a standalone-mode endpoint that broadcasts markdown
// content to all clients for buffer 1. Accepts optional JSON body
// with a "content" field (string); defaults to sample markdown if omitted.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	content := defaultReloadContent

	// Reject non-JSON content types. Empty Content-Type is permitted for
	// backwards compatibility (e.g. curl with no -H flag sends no body).
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, _ := mime.ParseMediaType(ct)
		if mediaType != "application/json" {
			http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
			return
		}
	}

	// Allow overriding the content via JSON body. r.Body is always
	// non-nil for server requests (set to http.NoBody rather than nil
	// by the stdlib). MaxBytesReader caps the body at 1 MB; a request
	// with no body causes Decode to return io.EOF, which is treated as
	// "no override" and keeps the default content.
	r.Body = http.MaxBytesReader(w, r.Body, maxReloadBodySize)

	var payload struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// io.EOF (empty body) or malformed JSON: fall through to default content.
	} else if payload.Content != "" {
		content = payload.Content
	}

	// Normalize Windows \r\n line endings, matching the broadcastFile
	// path in main.go. Without this, content with \r\n endings would
	// have trailing \r in each line, causing inconsistent rendering.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	// Use the configured page title, falling back to a sensible default
	// for standalone mode where no editor provides the value.
	pageTitle := s.cfg.PageTitle
	if pageTitle == "" {
		pageTitle = "Markdown Preview"
	}
	data := config.NewStandaloneRefreshData(StandaloneBufferNr, standaloneReloadSource, pageTitle, s.cfg.Theme, lines)

	// Set Content-Type before the broadcast so that if the handler timeout
	// fires mid-broadcast, the buffered headers include the correct type.
	w.Header().Set("Content-Type", "application/json")

	// Broadcast synchronously so the goroutine participates in the HTTP
	// handler lifecycle. httpServer.Shutdown waits for in-flight handlers,
	// so a synchronous call here guarantees the broadcast completes before
	// shutdown closes WebSocket connections. The handler timeout (30s)
	// bounds the total duration.
	s.BroadcastToBuffer(StandaloneBufferNr, editor.EventRefreshContent, data)

	_, _ = w.Write([]byte(`{"ok": true}`))
}
