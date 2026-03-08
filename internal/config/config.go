// Package config defines configuration types that map to g:mkdp_* Vim variables.
package config

// PreviewOptions maps to the g:mkdp_preview_options dictionary.
//
// Several fields use int instead of bool for JS wire compatibility: the
// original markdown-preview.nvim project uses 0/1 integers in VimScript
// and the browser JS checks truthiness, so we preserve that convention.
type PreviewOptions struct {
	// MarkdownIt holds markdown-it parser options (e.g. html, linkify, breaks).
	MarkdownIt map[string]any `json:"mkit"`
	// KaTeX holds KaTeX math rendering options.
	KaTeX map[string]any `json:"katex"`
	// UML holds PlantUML diagram rendering options.
	UML map[string]any `json:"uml"`
	// Mermaid holds mermaid diagram rendering options.
	Mermaid           map[string]any `json:"maid"`
	DisableSyncScroll int            `json:"disable_sync_scroll"`
	SyncScrollType    string         `json:"sync_scroll_type"`
	HideYAMLMeta      int            `json:"hide_yaml_meta"`
	SequenceDiagrams  map[string]any `json:"sequence_diagrams"`
	FlowchartDiagrams map[string]any `json:"flowchart_diagrams"`
	ContentEditable   int            `json:"content_editable"`
	DisableFilename   int            `json:"disable_filename"`
	TOC               map[string]any `json:"toc"`
}

// Config holds the full set of plugin configuration derived from g:mkdp_* variables.
//
// Fields that exist as g:mkdp_* variables but are only relevant to VimScript
// (auto_start, auto_close, refresh_slow, command_for_global, echo_preview_url,
// browserfunc, filetypes, combine_preview, combine_preview_auto_refresh) are
// intentionally omitted. They are fetched by gather_config() on the VimScript
// side but never consumed by the Go binary.
type Config struct {
	OpenToTheWorld bool           `json:"open_to_the_world"`
	OpenIP         string         `json:"open_ip"`
	Browser        string         `json:"browser"`
	PreviewOptions PreviewOptions `json:"preview_options"`
	MarkdownCSS    string         `json:"markdown_css"`
	HighlightCSS   string         `json:"highlight_css"`
	Port           int            `json:"port"`
	PageTitle      string         `json:"page_title"`
	ImagesPath     string         `json:"images_path"`
	Theme          string         `json:"theme"`

	// Standalone is a runtime flag, not a Vim variable. When true, the
	// server runs without an editor connection and enables the standalone
	// reload endpoint (/-/reload).
	Standalone bool `json:"-"`
}

// RefreshData carries the content and metadata sent on each refresh.
// The editor gathers this data via RPC on every buffer change and
// sends it through the WebSocket relay to the browser for rendering.
//
// JSON tag casing is mixed (camelCase for pageTitle, lowercase for winline,
// etc.) because it is inherited from the original markdown-preview.nvim
// project's browser JS, which expects these exact key names.
type RefreshData struct {
	// Content is the buffer text split into lines. The browser joins
	// them with newlines before passing to the markdown renderer.
	Content []string `json:"content"`

	// Cursor holds the editor cursor position as [bufnum, lnum, col, off]
	// (the Vim getpos(".") tuple). Used by the browser for scroll sync.
	Cursor []int `json:"cursor"`

	// WinLine is the screen line of the cursor within the visible window
	// (Vim winline()). Combined with WinHeight it tells the browser where
	// the viewport sits relative to the full document.
	WinLine int `json:"winline"`

	// WinHeight is the total number of visible lines in the editor window
	// (Vim winheight()). Used together with WinLine for scroll sync.
	WinHeight int `json:"winheight"`

	// Options is the g:mkdp_preview_options dictionary forwarded from
	// the editor. Keys include mkit, katex, toc, sync_scroll_type,
	// hide_yaml_meta, content_editable, and others.
	//
	// This is map[string]any rather than the typed PreviewOptions struct
	// because the browser JS expects specific JSON key names that match
	// the VimScript dictionary. The editor RPC paths (Neovim batch and
	// Vim gather_data) already produce a raw map, and the standalone
	// constructor uses a literal map for the same wire format.
	Options map[string]any `json:"options"`

	// PageTitle is the g:mkdp_page_title template string. The browser
	// replaces "${name}" with the buffer basename before setting
	// document.title.
	PageTitle string `json:"pageTitle"`

	// Theme is "dark" or "light" (from g:mkdp_theme). The browser sets
	// a data-theme attribute on <main> so CSS can switch color schemes.
	Theme string `json:"theme"`

	// Name is the buffer's file name (may include a relative path).
	// The browser extracts the basename for the page header and title.
	Name string `json:"name"`
}

// Wire-format key names for PreviewOptions fields that appear in
// multiple callsites: NewStandaloneRefreshData (which builds the
// map[string]any for standalone mode) and editor.mapToPreviewOptions
// (which reads the map from the editor). Other PreviewOptions keys
// are only accessed via struct JSON tags and do not need constants.
const (
	OptKeySyncScrollType = "sync_scroll_type"
	OptKeyHideYAMLMeta   = "hide_yaml_meta"
)

// Default values for PreviewOptions fields. Used by both DefaultConfig and
// NewStandaloneRefreshData so the browser-side defaults have a single source
// of truth.
const (
	defaultSyncScrollType = "middle"
	defaultHideYAMLMeta   = 1
	// DefaultWinHeight is a placeholder terminal height used in standalone
	// mode where there is no real editor window. The value (40 lines)
	// represents a typical terminal height and keeps the browser's scroll sync
	// and rendering logic from treating the viewport as empty. Exported so
	// tests can reference it instead of hardcoding the value.
	DefaultWinHeight = 40
)

// DefaultConfig returns a Config with defaults matching the original plugin.
func DefaultConfig() Config {
	return Config{
		PageTitle: "${name}",
		PreviewOptions: PreviewOptions{
			SyncScrollType: defaultSyncScrollType,
			HideYAMLMeta:   defaultHideYAMLMeta,
		},
	}
}

// NewStandaloneRefreshData constructs a RefreshData suitable for standalone
// mode, where there is no real editor cursor or viewport. The cursor,
// winLine, winHeight, and options fields are populated with sensible
// defaults that keep the browser's scroll sync and rendering logic happy.
// bufnr is the standalone buffer number (server.StandaloneBufferNr); it is
// accepted as a parameter to avoid a circular import from config to server.
func NewStandaloneRefreshData(bufnr int, name, pageTitle, theme string, lines []string) *RefreshData {
	return &RefreshData{
		Content: lines,
		// Cursor: [bufnum, lnum, col, off] per Vim getpos(".").
		// bufnum is the standalone buffer number; lnum=1 places the virtual cursor at the first line.
		Cursor:    []int{bufnr, 1, 0, 0},
		WinLine:   1,
		WinHeight: DefaultWinHeight,
		Options: map[string]any{
			OptKeySyncScrollType: defaultSyncScrollType,
			OptKeyHideYAMLMeta:   defaultHideYAMLMeta,
		},
		PageTitle: pageTitle,
		Theme:     theme,
		Name:      name,
	}
}
