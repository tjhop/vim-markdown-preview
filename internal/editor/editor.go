// Package editor defines the interface for communicating with Vim and Neovim
// over their respective RPC protocols. It abstracts away wire-format differences
// so the rest of the application can treat both editors identically.
package editor

import "github.com/tjhop/vim-markdown-preview/internal/config"

// Event name constants used by the editor RPC notification dispatch.
// EventRefreshContent, EventClosePage, and EventOpenBrowser are also
// forwarded over WebSocket to the browser using the same names.
// EventCloseAllPages is only used for RPC dispatch (the browser
// receives EventClosePage for both individual and all-page closes).
const (
	EventRefreshContent = "refresh_content"
	EventClosePage      = "close_page"
	EventCloseAllPages  = "close_all_pages"
	EventOpenBrowser    = "open_browser"
)

// Editor abstracts the RPC protocol differences between Neovim (msgpack-RPC)
// and Vim 8+ (JSON channel protocol). Both implementations communicate over
// stdin/stdout but use different wire formats and calling conventions.
type Editor interface {
	// Serve blocks on the RPC event loop, dispatching incoming
	// notifications to the registered handler. Returns when the
	// connection is closed or an unrecoverable error occurs.
	Serve() error

	// ChannelID returns the RPC channel identifier used by the editor
	// to route messages to this process.
	ChannelID() int

	// SetVar sets a global (g:) variable in the editor.
	SetVar(name string, value any) error

	// FetchConfig reads all g:mkdp_* variables from the editor and
	// returns them as a Config. Called once at startup before the
	// HTTP server is started.
	FetchConfig() (*config.Config, error)

	// FetchBufferData gathers the current buffer content and viewport
	// state for a refresh. Called on each editor notification.
	FetchBufferData(bufnr int) (*config.RefreshData, error)

	// OnNotification registers the callback handler for editor events.
	// Must be called before Serve; no synchronization is provided
	// because the protocol guarantees registration precedes Serve.
	OnNotification(handler NotificationHandler)
}

// NotificationHandler holds callbacks for the editor events that the
// Go binary cares about. All fields are set together before Serve is called.
type NotificationHandler struct {
	// RefreshContent is called when the editor buffer changes or the
	// cursor moves. The bufnr identifies which buffer to refresh.
	RefreshContent func(bufnr int)

	// ClosePage is called when a single buffer's preview should be closed.
	ClosePage func(bufnr int)

	// CloseAllPages is called when all previews should close and the
	// server should shut down (e.g., VimLeave).
	CloseAllPages func()

	// OpenBrowser is called when the browser should be opened/focused
	// for the given buffer's preview page.
	OpenBrowser func(bufnr int)
}
