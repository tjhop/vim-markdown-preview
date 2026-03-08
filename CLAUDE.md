# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go reimplementation of [markdown-preview.nvim](https://github.com/iamcco/markdown-preview.nvim) -- a Vim/Neovim plugin that provides live, synchronized markdown preview in a browser. The Go binary replaces the original Node.js server, eliminating the fragile npm/yarn dependency chain in favor of a single statically-compiled binary.

## Architecture

Three-tier system: Editor <-> Go Binary <-> Browser.

- **Editor layer** (VimScript): Thin plugin that launches the Go binary, fires autocmds on cursor movement, and sends RPC notifications. Supports both Neovim (msgpack-RPC) and Vim 8+ (JSON channel protocol) via `--mode` flag.
- **Go binary** (`cmd/vim-markdown-preview/`): HTTP server (static assets via `embed`), WebSocket relay (content from editor to browser), and editor RPC client. All browser assets are embedded at compile time. Also supports `--standalone` mode (HTTP server only, no editor connection).
- **Browser layer** (`web/`): Vanilla HTML/JS page (no React/Next.js). Renders markdown client-side using markdown-it with 18+ plugins. Diagram rendering (mermaid, Chart.js, Graphviz, etc.) happens in-browser via vendored JS libraries.

Key design decisions:
- Markdown rendering is **client-side only** -- the Go server relays raw markdown text, the browser renders it
- Native WebSocket replaces Socket.IO (no Socket.IO on either side)
- Browser JS libraries are vendored and embedded in the binary -- no CDN, no internet required
- `autoload/nvim/api.vim` compatibility shim must be preserved for Vim 8 support

## Build Commands

**Always use the Makefile for builds, tests, linting, and other project operations.** Do not run `go build`, `go test`, `golangci-lint`, etc. directly.

```bash
# Build a binary (runs fmt, tidy, lint, test first)
make build

# Run tests (runs fmt, tidy first)
make test

# Run linters
make lint

# Format code
make fmt

# Tidy modules
make tidy

# Build for all targets (snapshot release)
make build-all

# Download vendored browser JS/CSS/font dependencies
make web-vendor

# Verify all vendored web dependencies are present (CI guard)
make web-check

# See all available targets
make help
```

## Project Structure

```
cmd/vim-markdown-preview/ # Entry point, CLI flags
internal/
  browser/                # Cross-platform browser opening
  config/                 # Config types mapping to g:mkdp_* Vim variables
  editor/                 # Editor interface + Neovim/Vim 8 implementations
  server/                 # HTTP + WebSocket server, routes, client management
  version/                # Build version info (set by goreleaser ldflags)
web/                      # Browser assets (go:embed target)
  web.go                  # go:embed directive
  vendor-manifest.json    # Vendored lib versions and download URLs
  index.html              # Preview page
  js/plugins/             # Custom markdown-it plugins (adapted from original)
  js/vendor/              # Third-party browser libs (mermaid, katex, etc.)
  js/preview.js           # Main preview logic (WebSocket, rendering, scroll sync)
  css/                    # Stylesheets + KaTeX fonts
plugin/mkdp.vim           # Commands, autocmds, config defaults
autoload/mkdp/            # rpc.vim, util.vim, autocmd.vim
autoload/nvim/api.vim     # Vim 8 compatibility shim (do not remove)
autoload/health/mkdp.vim  # Neovim :checkhealth provider
scripts/                  # Build/CI helper scripts (web dependency vendoring)
```

## Git: VimScript Files

`*.vim` is globally gitignored (`~/.gitignore`). All `.vim` files in this repo require `git add -f` to stage.

## Go Dependencies

- `github.com/neovim/go-client/nvim` -- official Neovim msgpack-RPC client
- `github.com/coder/websocket` -- WebSocket server (formerly `nhooyr.io/websocket`)
- Vim 8 JSON channel protocol is implemented with stdlib `encoding/json`
- Static assets embedded with stdlib `embed`
- Logging via stdlib `log/slog`

## Key Interfaces and Patterns

The `Editor` interface (`internal/editor/editor.go`) abstracts the protocol differences between Neovim and Vim 8. Both implementations communicate over stdin/stdout but use different wire formats. The Neovim implementation uses batched RPC calls for performance; the Vim 8 implementation uses a single VimScript helper function (`mkdp#rpc#gather_data()`) to minimize round-trips.

Shared event name constants (`EventRefreshContent`, `EventClosePage`, `EventCloseAllPages`, `EventOpenBrowser`) are defined in `editor.go` and used by both the RPC notification dispatch and the WebSocket relay to the browser. Similarly, `StandaloneBufferNr` in `internal/server/routes.go` is the exported fixed buffer number for standalone mode.

The `Server.SetOnClientChange` setter registers a callback that fires when WebSocket clients connect/disconnect. It must be called before `Start()`. In `cmd/vim-markdown-preview/main.go`, the editor mode uses `atomic.Pointer[server.Server]` and `atomic.Pointer[config.Config]` to share references between the main goroutine and the RPC notification dispatch goroutine.

## Browser Plugin Adaptation

Custom markdown-it plugins from the original project are wrapped in IIFEs and exposed as globals (e.g., `window.metaPlugin`) since we use vanilla `<script>` tags instead of webpack/Next.js bundling. Script load order matters -- vendor libraries must load before the plugins that depend on them. See `web/index.html` for the current load order.

## Configuration

All user-facing config is via `g:mkdp_*` Vim variables defined in `plugin/mkdp.vim`. The Go binary receives these values from the editor via RPC on each refresh. See `internal/config/config.go` for the Go-side mapping.

## Linting

Uses golangci-lint v2 (`.golangci.yml`). Enabled linters: errcheck, errorlint, exptostd, fatcontext, gocritic, godot, govet, misspell, nilnesserr, nolintlint, perfsprint, predeclared, sloglint, unconvert, unused, usestdlibvars, whitespace. Run via `make lint`. Some things to be aware of:

- `sloglint` requires `slog.DiscardHandler` instead of `slog.NewTextHandler(io.Discard, nil)` in tests
- `errcheck` requires `_ =` prefix for intentionally-ignored error returns (e.g., `io.Pipe` Close calls)
- `godot` requires comments to end with a period

## Testing

Three packages have test coverage: `internal/browser`, `internal/editor`, `internal/server`.

Test infrastructure lives in `internal/server/server_test.go`:
- `discardLogger()` -- creates a `slog.DiscardHandler` logger for tests
- `startTestServer(t)` -- creates a server on a random port with cleanup
- `startTestServerWithConfig(t, modify func(*config.Config))` -- same, but applies a config modifier before starting (used for standalone, open-to-world, custom CSS, images-path tests)
- `testPNG` -- package-level minimal 1x1 PNG byte slice shared across image tests

Important test patterns:
- `t.Context()` is cancelled when the test function returns, **before** `t.Cleanup` runs. Shutdown operations in `t.Cleanup` must use `context.WithTimeout(context.Background(), ...)` instead.
- `config.PreviewOptions` contains `map[string]any` fields, making it incomparable with `==`/`!=`. Use field-by-field assertions.
- VimClient `sendRequest` blocks on `<-ch` or `<-c.done`. Tests that exercise valid-name paths without a full Vim mock must start `Serve()` and close the pipe to trigger EOF shutdown, which closes `c.done` and unblocks `sendRequest`.

## Code Quality Notes

Deferred informational findings from quality review are documented in `QUALITY.md` at the project root. Covers architecture decisions, known limitations, design debt, and testing gaps that are acceptable as-is but worth tracking.

## Docs References

- [Go `flag` package](https://pkg.go.dev/flag) -- CLI argument parsing
- [Go `embed` package](https://pkg.go.dev/embed) -- static asset embedding
- [Go `log/slog` package](https://pkg.go.dev/log/slog) -- structured logging
- [coder/websocket](https://pkg.go.dev/github.com/coder/websocket) -- WebSocket server library (v1.8.14)
- [neovim/go-client](https://pkg.go.dev/github.com/neovim/go-client/nvim) -- Neovim msgpack-RPC client (v1.2.1)
- [Vim channel docs](https://vimhelp.org/channel.txt.html) -- Vim 8 JSON channel protocol spec
- [markdown-it](https://github.com/markdown-it/markdown-it) -- browser-side markdown parser
- [golangci-lint v2 docs](https://golangci-lint.run/) -- linter framework configuration
