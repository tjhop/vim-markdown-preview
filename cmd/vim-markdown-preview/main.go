// vim-markdown-preview is a Go reimplementation of markdown-preview.nvim.
// It provides a live, synchronized markdown preview in a browser, communicating
// with Vim/Neovim over RPC and relaying content to browsers via WebSocket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tjhop/vim-markdown-preview/internal/browser"
	"github.com/tjhop/vim-markdown-preview/internal/config"
	"github.com/tjhop/vim-markdown-preview/internal/editor"
	"github.com/tjhop/vim-markdown-preview/internal/server"
	"github.com/tjhop/vim-markdown-preview/internal/version"
)

// shutdownTimeout is the grace period for server shutdown. Used in both
// standalone (signal handler) and editor (CloseAllPages + deferred) paths.
const shutdownTimeout = 10 * time.Second

// fileWatchInterval is the polling interval for watching a file in
// standalone mode. One stat() syscall per tick is negligible overhead
// for a single file, and 200ms latency is imperceptible for preview.
const fileWatchInterval = 200 * time.Millisecond

// previewURLFmt is the format string for the browser preview URL.
// Arguments: port (int), buffer number (int).
const previewURLFmt = "http://127.0.0.1:%d/page/%d"

func main() {
	var (
		showVersion = flag.Bool("version", false, "print version information and exit")
		mode        = flag.String("mode", "nvim", "editor mode: nvim or vim")
		standalone  = flag.Bool("standalone", false, "run in standalone mode (no editor, HTTP-only)")
		port        = flag.Int("port", 0, "port to listen on (0 = random)")
		file        = flag.String("file", "", "markdown file to preview (standalone mode only)")
	)

	flag.Parse()

	if *showVersion {
		fmt.Printf("vim-markdown-preview %s (commit: %s, built: %s)\n",
			version.Version(), version.Commit(), version.BuildTime())
		os.Exit(0)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	if *file != "" && !*standalone {
		logger.Error("-file requires -standalone")
		os.Exit(1)
	}

	if *standalone {
		runStandalone(logger, *port, *file)
	} else {
		runEditor(logger, *mode, *port)
	}
}

// runStandalone starts the HTTP server without an editor connection.
// When file is non-empty, the server reads the file, broadcasts its
// content, and polls for changes to re-broadcast automatically.
// Blocks until SIGINT/SIGTERM (or file deletion when watching).
func runStandalone(logger *slog.Logger, port int, file string) {
	cfg := config.DefaultConfig()
	cfg.Port = port
	cfg.Standalone = true

	// When watching a file, use its basename as the page title.
	if file != "" {
		cfg.PageTitle = filepath.Base(file)
	}

	srv := server.New(cfg, logger)
	if err := srv.Start(); err != nil {
		logger.Error("failed to start server", "err", err)
		os.Exit(1)
	}

	logger.Info("standalone mode", "addr", srv.Addr().String())

	previewURL := fmt.Sprintf(previewURLFmt, portFromAddr(srv.Addr()), server.StandaloneBufferNr)
	fmt.Fprintf(os.Stderr, "Preview at: %s\n", previewURL)

	// When watching a file, broadcast initial content and open browser.
	// Stat the file after broadcasting to obtain the modification-time
	// baseline for the watcher. Performing the stat here (rather than
	// inside watchFile) eliminates the gap between broadcastFile's read
	// and watchFile's independent stat: if the file were deleted in that
	// gap, the old approach caused watchFile to return immediately on a
	// stat error, triggering premature shutdown while the browser was
	// showing valid content.
	var initialMod time.Time
	// fatalShutdown logs an error, shuts down the server, and exits.
	fatalShutdown := func(msg string, args ...any) {
		logger.Error(msg, args...)
		if shutdownErr := srv.Shutdown(context.Background()); shutdownErr != nil {
			logger.Error("shutdown error", "err", shutdownErr)
		}
		os.Exit(1)
	}

	if file != "" {
		info, err := os.Stat(file)
		if err != nil {
			fatalShutdown("failed to stat file", "file", file, "err", err)
		}
		content, err := readFileWithLimit(file, server.MaxContentSize)
		if err != nil {
			fatalShutdown("failed to read file", "file", file, "err", err)
		}
		broadcastFile(srv, cfg, file, content)
		initialMod = info.ModTime()

		logger.Info("watching file", "file", file, "interval", fileWatchInterval)

		if err := browser.Open(previewURL, cfg.Browser, logger); err != nil {
			logger.Error("failed to open browser", "err", err, "url", previewURL)
		}
	}

	// Block until signal (or file watcher exit).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// watchDone is closed when the file watcher goroutine exits.
	// When no file is given, the goroutine is never started and the
	// channel is never closed, so only ctx.Done() can unblock the
	// select. This avoids duplicating the shutdown logic in two branches.
	watchDone := make(chan struct{})
	if file != "" {
		// File watcher runs until the context is cancelled or the
		// file is deleted. If it returns on its own (file deleted),
		// we proceed to shutdown.
		go func() {
			defer close(watchDone)
			watchFile(ctx, logger, srv, cfg, file, initialMod)
		}()
	}

	select {
	case <-ctx.Done():
	case <-watchDone:
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
	}
}

// watchFile polls file for changes and broadcasts new content when the
// modification time changes. Returns when ctx is cancelled or the file
// is deleted. initialMod is the modification time of the file at the
// time the caller last read it (i.e. the broadcast baseline); the watcher
// only broadcasts when it sees a strictly newer mod time.
func watchFile(ctx context.Context, logger *slog.Logger, srv *server.Server, cfg config.Config, file string, initialMod time.Time) {
	lastMod := initialMod

	ticker := time.NewTicker(fileWatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(file)
			if err != nil {
				logger.Error("watched file disappeared, exiting", "file", file, "err", err)
				return
			}

			mod := info.ModTime()
			if !mod.After(lastMod) {
				continue
			}
			lastMod = mod

			content, err := readFileWithLimit(file, server.MaxContentSize)
			if err != nil {
				logger.Warn("failed to read changed file", "file", file, "err", err)
				continue
			}

			logger.Info("file changed, broadcasting", "file", file)
			broadcastFile(srv, cfg, file, content)
		}
	}
}

// broadcastFile sends file content to all connected browser clients
// for the standalone buffer. Persisted so late-connecting clients
// receive a replay of the most recent content on connect.
func broadcastFile(srv *server.Server, cfg config.Config, file string, content []byte) {
	// Normalize Windows \r\n line endings. Vim/Neovim strip these in
	// editor mode, but standalone file reads preserve them.
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	data := config.NewStandaloneRefreshData(server.StandaloneBufferNr, filepath.Base(file), cfg.PageTitle, cfg.Theme, lines)
	srv.BroadcastToBuffer(server.StandaloneBufferNr, editor.EventRefreshContent, data)
}

// runEditor starts the server with an editor RPC connection. The editor
// provides configuration, buffer content, and lifecycle events. This is the
// normal mode when launched by the VimScript plugin.
func runEditor(logger *slog.Logger, mode string, portOverride int) {
	// Create the appropriate editor client based on --mode.
	client, err := newEditorClient(mode, logger)
	if err != nil {
		logger.Error("failed to create editor client", "err", err, "mode", mode)
		os.Exit(1)
	}

	// Wire notification handlers before starting Serve() so the handler
	// references are visible by the time the event loop dispatches
	// notifications. Setting them after Serve() creates a race window
	// where early notifications would be silently dropped.
	//
	// The handlers reference srvPtr and cfgPtr which are populated below.
	// Atomic pointers ensure race-free access between the main goroutine
	// (which stores the values) and the notification dispatch goroutine
	// (which loads them). The editor won't send notifications until we
	// set g:mkdp_channel_id further down, but atomics make the safety
	// explicit rather than relying on protocol-level ordering.
	var srvPtr atomic.Pointer[server.Server]
	var cfgPtr atomic.Pointer[config.Config]
	client.OnNotification(makeNotificationHandler(client, &srvPtr, &cfgPtr, logger))

	// Start the RPC read loop in the background so that callFunc/callExpr
	// requests issued during initialization (FetchConfig, SetVar) can
	// receive their responses. Without this, the main goroutine deadlocks
	// waiting on a response channel that nothing is feeding.
	serveErr := make(chan error, 1)
	go func() { serveErr <- client.Serve() }()

	// Fetch configuration from the editor's g:mkdp_* variables.
	cfg, err := client.FetchConfig()
	if err != nil {
		logger.Error("failed to fetch config from editor", "err", err)
		os.Exit(1)
	}

	// CLI port flag overrides the editor config.
	if portOverride != 0 {
		cfg.Port = portOverride
	}

	srv := server.New(*cfg, logger)

	// Publish to atomic pointers so notification handler closures
	// (running on the RPC dispatch goroutine) can access them safely.
	cfgPtr.Store(cfg)
	srvPtr.Store(srv)

	// Set the client-change callback before Start() so it is visible
	// by the time the HTTP handler goroutine fires on WebSocket connect.
	srv.SetOnClientChange(func(hasClients bool) {
		active := 0
		if hasClients {
			active = 1
		}
		if err := client.SetVar("mkdp_clients_active", active); err != nil {
			logger.Warn("failed to set clients_active var", "err", err)
		}
	})

	if err := srv.Start(); err != nil {
		logger.Error("failed to start server", "err", err)
		// Honour the New() contract: Shutdown must be called even if
		// Start failed, to stop the dispatch goroutine started in New.
		_ = srv.Shutdown(context.Background())
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown error", "err", err)
		}
	}()

	logger.Info("editor mode", "mode", mode, "addr", srv.Addr().String())

	// Tell the editor our channel ID so VimScript can send us notifications.
	if err := client.SetVar("mkdp_channel_id", client.ChannelID()); err != nil {
		logger.Warn("failed to set channel id var", "err", err)
	}

	// Block until the editor disconnects (Serve returns on EOF/error).
	if err := <-serveErr; err != nil {
		logger.Error("editor serve error", "err", err)
	}
}

// newEditorClient creates the editor client for the given mode.
// The logger is decorated with an "editor" attribute so that all
// log messages from the client carry structured editor attribution
// instead of ad-hoc message prefixes.
func newEditorClient(mode string, logger *slog.Logger) (editor.Editor, error) {
	editorLogger := logger.With("editor", mode)
	switch mode {
	case "nvim":
		// os.Stdout is the writer; a NopCloser wrapping os.Stdout is used
		// for the closer so the go-client's shutdown Close call is a no-op
		// instead of closing file descriptor 1.
		return editor.NewNeovimClient(os.Stdin, os.Stdout, io.NopCloser(os.Stdout), editorLogger)
	case "vim":
		return editor.NewVimClient(os.Stdin, os.Stdout, editorLogger), nil
	default:
		return nil, fmt.Errorf("unknown editor mode: %s", mode)
	}
}

// makeNotificationHandler builds the editor notification callbacks for
// editor mode. Each callback loads the server and config from atomic
// pointers, which are populated after the server starts but before the
// editor can send notifications (gated by g:mkdp_channel_id).
func makeNotificationHandler(
	client editor.Editor,
	srvPtr *atomic.Pointer[server.Server],
	cfgPtr *atomic.Pointer[config.Config],
	logger *slog.Logger,
) editor.NotificationHandler {
	// loadServer loads the server from the atomic pointer, returning the
	// server and true on success. On failure it logs an error with the
	// given event name and optional attrs, then returns nil and false.
	loadServer := func(event string, attrs ...any) (*server.Server, bool) {
		srv := srvPtr.Load()
		if srv == nil {
			logger.Error(event+" before server initialized", attrs...)
			return nil, false
		}
		return srv, true
	}

	// loadConfig loads the config from the atomic pointer, returning the
	// config and true on success. On failure it logs an error with the
	// given event name and optional attrs, then returns nil and false.
	loadConfig := func(event string, attrs ...any) (*config.Config, bool) {
		cfg := cfgPtr.Load()
		if cfg == nil {
			logger.Error(event+" before server initialized", attrs...)
			return nil, false
		}
		return cfg, true
	}

	return editor.NotificationHandler{
		RefreshContent: func(bufnr int) {
			srv, ok := loadServer("refresh_content", "bufnr", bufnr)
			if !ok {
				return
			}
			data, err := client.FetchBufferData(bufnr)
			if err != nil {
				if errors.Is(err, editor.ErrEditorClosed) {
					return // editor is shutting down; not an error
				}
				logger.Error("fetch buffer data failed", "bufnr", bufnr, "err", err)
				return
			}
			// Persisted: late-connecting clients replay the latest content.
			srv.BroadcastToBuffer(bufnr, editor.EventRefreshContent, data)
		},
		ClosePage: func(bufnr int) {
			srv, ok := loadServer("close_page", "bufnr", bufnr)
			if !ok {
				return
			}
			// Transient: close_page is a lifecycle event; a late client should
			// not receive a stale close notification on connect.
			srv.BroadcastTransientToBuffer(bufnr, editor.EventClosePage, nil)
		},
		CloseAllPages: func() {
			srv, ok := loadServer("close_all_pages")
			if !ok {
				return
			}
			srv.BroadcastAll(editor.EventClosePage, nil)
			// Run shutdown in a separate goroutine so the notification
			// dispatch goroutine is not blocked for up to shutdownTimeout.
			// Shutdown is idempotent (via shutdownOnce) so the deferred
			// shutdown in runEditor is still safe to call.
			go func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer shutdownCancel()
				if err := srv.Shutdown(shutdownCtx); err != nil {
					logger.Error("shutdown error", "err", err)
				}
			}()
		},
		OpenBrowser: func(bufnr int) {
			srv, ok := loadServer("open_browser", "bufnr", bufnr)
			if !ok {
				return
			}
			cfg, ok := loadConfig("open_browser", "bufnr", bufnr)
			if !ok {
				return
			}
			url := fmt.Sprintf(previewURLFmt, portFromAddr(srv.Addr()), bufnr)
			if err := browser.Open(url, cfg.Browser, logger); err != nil {
				logger.Error("failed to open browser", "err", err, "url", url)
			}
		},
	}
}

// readFileWithLimit reads a file, returning an error if it exceeds maxSize.
// It stats first to reject obviously large files, then re-checks after
// reading to handle TOCTOU races where the file grows between stat and read.
func readFileWithLimit(path string, maxSize int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("file size %d exceeds limit %d", info.Size(), maxSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("file size %d exceeds limit %d after read", len(data), maxSize)
	}
	return data, nil
}

// portFromAddr extracts the port number from a net.Addr.
// Returns 0 if the type assertion to *net.TCPAddr fails.
func portFromAddr(addr net.Addr) int {
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.Port
	}
	return 0
}
