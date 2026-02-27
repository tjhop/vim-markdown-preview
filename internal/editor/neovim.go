package editor

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/neovim/go-client/nvim"
	"github.com/tjhop/vim-markdown-preview/internal/config"
)

// NeovimClient implements the Editor interface using Neovim's msgpack-RPC
// protocol via the official go-client library.
type NeovimClient struct {
	nvim    *nvim.Nvim
	handler NotificationHandler
	logger  *slog.Logger
}

// NewNeovimClient creates a NeovimClient connected over the given streams.
// The streams are typically stdin/stdout when launched by Neovim as an RPC host.
func NewNeovimClient(reader io.Reader, writer io.Writer, closer io.Closer, logger *slog.Logger) (*NeovimClient, error) {
	conn, err := nvim.New(reader, writer, closer, nil)
	if err != nil {
		return nil, fmt.Errorf("create nvim client: %w", err)
	}

	return &NeovimClient{
		nvim:   conn,
		logger: logger,
	}, nil
}

// OnNotification registers the callback handler for editor events.
func (c *NeovimClient) OnNotification(handler NotificationHandler) {
	c.handler = handler
}

// Serve registers RPC notification handlers and blocks on the event loop.
// The go-client library dispatches notification handlers on a dedicated
// goroutine (via its internal runNotifications loop), so handler callbacks
// run concurrently with the main event loop.
func (c *NeovimClient) Serve() error {
	if err := c.nvim.RegisterHandler(EventRefreshContent, c.handleRefreshContent); err != nil {
		return fmt.Errorf("register %s handler: %w", EventRefreshContent, err)
	}
	if err := c.nvim.RegisterHandler(EventClosePage, c.handleClosePage); err != nil {
		return fmt.Errorf("register %s handler: %w", EventClosePage, err)
	}
	if err := c.nvim.RegisterHandler(EventCloseAllPages, c.handleCloseAllPages); err != nil {
		return fmt.Errorf("register %s handler: %w", EventCloseAllPages, err)
	}
	if err := c.nvim.RegisterHandler(EventOpenBrowser, c.handleOpenBrowser); err != nil {
		return fmt.Errorf("register %s handler: %w", EventOpenBrowser, err)
	}

	return c.nvim.Serve()
}

// ChannelID returns the Neovim RPC channel ID for this connection.
func (c *NeovimClient) ChannelID() int {
	return c.nvim.ChannelID()
}

// SetVar sets a global (g:) variable in Neovim. The name is validated against
// validVarName before the RPC call to prevent VimScript injection via unsafe
// characters, matching VimClient's behaviour.
func (c *NeovimClient) SetVar(name string, value any) error {
	if !validVarName.MatchString(name) {
		return fmt.Errorf("%w: %q", errUnsafeVarName, name)
	}
	return c.nvim.SetVar(name, value)
}

// FetchConfig reads all g:mkdp_* variables from Neovim using a batched RPC
// call (single round-trip) and returns a populated Config.
func (c *NeovimClient) FetchConfig() (*config.Config, error) {
	cfg := config.DefaultConfig()

	var (
		openToTheWorld int
		openIP         string
		browser        string
		previewOptions map[string]any
		markdownCSS    string
		highlightCSS   string
		imagesPath     string
		port           any
		pageTitle      string
		theme          string
	)

	// Batch call indices. These must match the order of b.Var calls
	// below. On partial failure, failedIdx is set to BatchError.Index
	// (the zero-based index of the failing call), and guards of the form
	// `failedIdx > idx` skip variables whose batch call did not complete.
	// If you add, remove, or reorder a batch call, update these constants
	// to match.
	const (
		cfgIdxOpenToTheWorld = iota
		cfgIdxOpenIP
		cfgIdxBrowser
		cfgIdxPreviewOptions
		cfgIdxMarkdownCSS
		cfgIdxHighlightCSS
		cfgIdxImagesPath
		cfgIdxPort
		cfgIdxPageTitle
		cfgIdxTheme
		cfgIdxCount // sentinel: equals the total number of batch calls
	)

	b := c.nvim.NewBatch()
	b.Var("mkdp_open_to_the_world", &openToTheWorld)
	b.Var("mkdp_open_ip", &openIP)
	b.Var("mkdp_browser", &browser)
	b.Var("mkdp_preview_options", &previewOptions)
	b.Var("mkdp_markdown_css", &markdownCSS)
	b.Var("mkdp_highlight_css", &highlightCSS)
	b.Var("mkdp_images_path", &imagesPath)
	b.Var("mkdp_port", &port)
	b.Var("mkdp_page_title", &pageTitle)
	b.Var("mkdp_theme", &theme)

	// On partial failure, BatchError.Index is the zero-based index of the
	// failing call; all results before that index are populated. Variables
	// at and beyond failedIdx retain zero values and must not overwrite the
	// defaults from DefaultConfig(). On full success, failedIdx is set to
	// cfgIdxCount so all guards pass.
	failedIdx := cfgIdxCount
	if err := b.Execute(); err != nil {
		var batchErr *nvim.BatchError
		if errors.As(err, &batchErr) {
			c.logger.Warn("partial batch failure fetching config", "index", batchErr.Index, "err", batchErr)
			failedIdx = batchErr.Index
		} else {
			return nil, fmt.Errorf("fetch config: %w", err)
		}
	}

	if failedIdx > cfgIdxOpenToTheWorld {
		cfg.OpenToTheWorld = openToTheWorld != 0
	}
	if failedIdx > cfgIdxOpenIP {
		cfg.OpenIP = openIP
	}
	if failedIdx > cfgIdxBrowser {
		cfg.Browser = browser
	}
	if failedIdx > cfgIdxPreviewOptions && previewOptions != nil {
		cfg.PreviewOptions = mapToPreviewOptions(previewOptions, c.logger)
	}
	if failedIdx > cfgIdxMarkdownCSS {
		cfg.MarkdownCSS = markdownCSS
	}
	if failedIdx > cfgIdxHighlightCSS {
		cfg.HighlightCSS = highlightCSS
	}
	if failedIdx > cfgIdxImagesPath {
		cfg.ImagesPath = imagesPath
	}
	if failedIdx > cfgIdxPort && port != nil {
		if p, ok := parsePort(port); ok {
			cfg.Port = p
		}
	}
	if failedIdx > cfgIdxPageTitle && pageTitle != "" {
		cfg.PageTitle = pageTitle
	}
	if failedIdx > cfgIdxTheme && theme != "" {
		cfg.Theme = theme
	}

	return &cfg, nil
}

// FetchBufferData gathers buffer content and viewport state in a single
// batched RPC call.
//
// Note: winline() and winheight(0) operate on the current window, which may
// differ from the window displaying bufnr if the user has switched focus.
// This is inherited from the upstream markdown-preview.nvim behavior and is
// acceptable because the preview refresh is triggered by cursor-move autocmds
// that fire in the context of the active window.
func (c *NeovimClient) FetchBufferData(bufnr int) (*config.RefreshData, error) {
	var (
		lines          [][]byte
		cursor         [4]int
		winLine        int
		winHeight      int
		previewOptions map[string]any
		pageTitle      string
		theme          string
		bufferName     string
	)

	// Batch call indices. See FetchConfig for a full explanation of
	// this pattern. If you add, remove, or reorder a batch call,
	// update these constants to match.
	const (
		bufIdxLines = iota
		bufIdxWinLine
		bufIdxWinHeight
		bufIdxCursor
		bufIdxOptions
		bufIdxPageTitle
		bufIdxTheme
		bufIdxName
		bufIdxCount // sentinel: equals the total number of batch calls
	)

	b := c.nvim.NewBatch()
	b.BufferLines(nvim.Buffer(bufnr), 0, -1, true, &lines)
	b.Call("winline", &winLine)
	b.Call("winheight", &winHeight, 0)
	b.Call("getpos", &cursor, ".")
	b.Var("mkdp_preview_options", &previewOptions)
	b.Var("mkdp_page_title", &pageTitle)
	b.Var("mkdp_theme", &theme)
	b.BufferName(nvim.Buffer(bufnr), &bufferName)

	// See FetchConfig for an explanation of the failedIdx pattern.
	failedIdx := bufIdxCount
	if err := b.Execute(); err != nil {
		var batchErr *nvim.BatchError
		if errors.As(err, &batchErr) {
			c.logger.Warn("partial batch failure fetching buffer data", "bufnr", bufnr, "index", batchErr.Index, "err", batchErr)
			failedIdx = batchErr.Index
			// BufferLines is the essential data. If it failed,
			// we have no content to send to the browser.
			if failedIdx == bufIdxLines {
				return nil, fmt.Errorf("fetch buffer data for %d: buffer lines failed: %w", bufnr, err)
			}
		} else {
			return nil, fmt.Errorf("fetch buffer data for %d: %w", bufnr, err)
		}
	}

	data := &config.RefreshData{
		Content: bytesToStrings(lines),
	}

	// Guard viewport fields against partial batch failure. These calls
	// may have failed while BufferLines succeeded. Viewport data is
	// nice-to-have for scroll sync; content is essential.
	if failedIdx > bufIdxWinLine {
		data.WinLine = winLine
	} else {
		c.logger.Warn("winline call failed, using zero", "bufnr", bufnr)
	}
	if failedIdx > bufIdxWinHeight {
		data.WinHeight = winHeight
	} else {
		c.logger.Warn("winheight call failed, using zero", "bufnr", bufnr)
	}
	if failedIdx > bufIdxCursor {
		data.Cursor = cursor[:]
	} else {
		c.logger.Warn("getpos call failed, using zero", "bufnr", bufnr)
	}

	// Options, PageTitle, Theme, and Name do not get warn logs on failure
	// because the browser degrades gracefully without them (it renders
	// content and uses default styling). The viewport fields above are
	// logged because their absence noticeably affects scroll sync.
	if failedIdx > bufIdxOptions {
		data.Options = previewOptions
	}
	if failedIdx > bufIdxPageTitle {
		data.PageTitle = pageTitle
	}
	if failedIdx > bufIdxTheme {
		data.Theme = theme
	}
	if failedIdx > bufIdxName {
		data.Name = bufferName
	}

	return data, nil
}

// dispatchFromArgs is a generic dispatcher for notifications that carry a bufnr
// argument. It nil-checks the callback, extracts the bufnr from the variadic
// args slice, and invokes the callback. This avoids repeating the same pattern
// across handleRefreshContent, handleClosePage, and handleOpenBrowser.
// The Vim 8 equivalent is VimClient.dispatchFromMap in vim.go.
func (c *NeovimClient) dispatchFromArgs(event string, fn func(int), args []any) {
	if fn == nil {
		return
	}
	bufnr, err := extractBufnrFromArgs(args)
	if err != nil {
		c.logger.Warn(event+": bad args", "err", err)
		return
	}
	fn(bufnr)
}

// handleRefreshContent dispatches the refresh_content notification.
func (c *NeovimClient) handleRefreshContent(args ...any) {
	c.dispatchFromArgs(EventRefreshContent, c.handler.RefreshContent, args)
}

// handleClosePage dispatches the close_page notification.
func (c *NeovimClient) handleClosePage(args ...any) {
	c.dispatchFromArgs(EventClosePage, c.handler.ClosePage, args)
}

// handleCloseAllPages dispatches the close_all_pages notification.
// This handler has no bufnr argument and stays separate.
func (c *NeovimClient) handleCloseAllPages(_ ...any) {
	if c.handler.CloseAllPages != nil {
		c.handler.CloseAllPages()
	}
}

// handleOpenBrowser dispatches the open_browser notification.
func (c *NeovimClient) handleOpenBrowser(args ...any) {
	c.dispatchFromArgs(EventOpenBrowser, c.handler.OpenBrowser, args)
}
