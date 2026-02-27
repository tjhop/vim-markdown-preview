# vim-markdown-preview

A Go reimplementation of [markdown-preview.nvim](https://github.com/iamcco/markdown-preview.nvim) -- live, synchronized markdown preview in a browser for Vim 8+ and Neovim.

A single statically-compiled binary with every dependency embedded. No Node.js, no Python, no external daemons, no runtime downloads. Just a binary that works.

## Why This Plugin?

There are many markdown preview plugins for Vim and Neovim. They all share a common problem: they aren't self-contained.

**Plugins that require an external service** (e.g., [instant-markdown-vim](https://github.com/instant-markdown/vim-instant-markdown) + [instant-markdown-d](https://github.com/instant-markdown/instant-markdown-d), or Python-based plugins that shell out to `grip` or `markdown`) force you to install and manage a separate daemon or CLI tool. That's two moving parts that can break independently, and the external service often brings its own dependency tree.

**Plugins with built-in servers** (e.g., the original [markdown-preview.nvim](https://github.com/iamcco/markdown-preview.nvim), [peek.nvim](https://github.com/toppair/peek.nvim)) eliminate the external daemon but still rely on fragile build environments. `markdown-preview.nvim` requires `npm`/`yarn` to build a Node.js server at install time -- a step that routinely breaks when Node versions change, native modules fail to compile, or lockfiles drift. `peek.nvim` requires the Deno runtime. In both cases, the plugin's install step is pulling down a large dependency tree and hoping nothing goes wrong.

**This plugin takes a different approach.** The preview server is a single Go binary with all browser assets (JavaScript libraries, CSS, fonts) embedded at compile time via `go:embed`. There is no runtime dependency on Node.js, Python, Deno, or any other language runtime. There is no install-time `npm install` or `yarn build`. There is no external daemon to start. The binary is the server, and the server has everything it needs.

| | External service needed | Runtime deps | Install-time build step | Fully self-contained |
|---|:---:|:---:|:---:|:---:|
| **vim-markdown-preview** (this plugin) | No | None | No (pre-built binary) | Yes |
| markdown-preview.nvim | No | Node.js | `npm install` / `yarn` | No |
| peek.nvim | No | Deno | Deno fetch | No |
| instant-markdown-vim | Yes (`instant-markdown-d`) | Node.js | `npm install` | No |
| Python-based plugins | Yes (grip, markdown, etc.) | Python | pip install | No |

The result is the most portable, most reliable markdown preview available for Vim and Neovim. It works on air-gapped machines, in containers, on any OS -- anywhere you can run a static binary and open a browser.

## Features

- **Live preview** -- edits appear in the browser as you type, with cursor-synchronized scrolling
- **Zero runtime dependencies** -- single binary with all browser assets embedded at compile time
- **Vim 8+ and Neovim support** -- automatic protocol detection (msgpack-RPC for Neovim, JSON channels for Vim 8)
- **Works offline** -- no CDN, no internet required; all JavaScript libraries are vendored and embedded
- **Cross-platform** -- Linux, macOS, and Windows (including WSL)

### Markdown Extensions

All extensions from the original plugin are supported:

| Extension | Syntax |
|---|---|
| KaTeX math | `$...$` (inline), `$$...$$` (block) |
| Mermaid diagrams | `` ```mermaid `` fenced code |
| PlantUML | `@startuml...@enduml` blocks, `` ```plantuml `` fenced code |
| Chart.js | `` ```chart `` fenced code with JSON config |
| Sequence diagrams | `` ```sequence-diagrams `` fenced code |
| Flowcharts | `` ```flowchart `` fenced code |
| Graphviz/dot | `` ```dot `` or `` ```graphviz `` fenced code |
| Emoji | `:shortcode:` (e.g., `:smile:`) |
| Task lists | `- [x]` / `- [ ]` checkboxes |
| Footnotes | `[^1]` references |
| Definition lists | `term` / `: definition` |
| Table of contents | `[[toc]]`, `[toc]`, `${toc}` |
| YAML front-matter | Automatically hidden in preview |
| Image sizing | `![alt](url =WxH)` |
| Syntax highlighting | Fenced code blocks with language tags |

## Architecture

Three-tier system: **Editor** <-> **Go Binary** <-> **Browser**.

```
+------------------+           +---------------------+           +------------------+
|                  |  msgpack  |                     | websocket |                  |
|     Neovim       |<--------->|                     |<--------->|    Browser       |
|                  |   RPC     |                     |           |                  |
+------------------+  stdin/   |    Go Binary        |           | - markdown-it    |
                      stdout   |                     |           | - diagram libs   |
+------------------+           | - HTTP server       |           | - scroll sync    |
|                  |   JSON    | - WebSocket relay   |           | - theme toggle   |
|     Vim 8+       |<--------->| - embedded assets   |           +------------------+
|                  |  channel  |                     |
+------------------+  stdin/   +---------------------+
                      stdout
```

- The **VimScript plugin** (`plugin/`, `autoload/`) launches the Go binary, fires autocmds on cursor movement, and sends RPC notifications
- The **Go binary** serves static assets (via `embed`), relays markdown content from the editor to browsers via WebSocket, and handles editor RPC
- The **browser** renders markdown client-side using markdown-it with 18+ plugins; diagram rendering (mermaid, Chart.js, Graphviz, etc.) also happens in-browser

## Quick Start

1. Install the plugin with your plugin manager (see [Installation](#installation) below)
2. Open a markdown file in Vim/Neovim
3. Run `:MarkdownPreview` -- a browser tab opens with a live preview
4. Edit the file -- the preview updates in real time with synchronized scrolling
5. Run `:MarkdownPreviewStop` when done (or just close the buffer if `g:mkdp_auto_close` is on, which it is by default)

## Installation

### Requirements

- **Vim 8.1+** or **Neovim 0.5+**
- A modern web browser

Installation has two parts: the **Go binary** (the server) and the **VimScript plugin** (the editor integration). The recommended approach is to install a pre-built release binary and then add the plugin to your editor.

### Step 1: Install the binary

#### Release builds (recommended)

Download the appropriate archive from the [releases page](https://github.com/tjhop/vim-markdown-preview/releases), extract the binary, and place it somewhere on your `$PATH`. No build tools required.

Release archives follow goreleaser naming: `vim-markdown-preview_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows). Available platforms: `linux`, `darwin` (macOS), `windows`. Available architectures: `amd64`, `arm64`.

```sh
# Example for Linux amd64 -- adjust the version, OS, and arch as needed
VERSION=0.1.0
curl -LO "https://github.com/tjhop/vim-markdown-preview/releases/download/v${VERSION}/vim-markdown-preview_${VERSION}_linux_amd64.tar.gz"
tar xzf "vim-markdown-preview_${VERSION}_linux_amd64.tar.gz"
chmod +x vim-markdown-preview
mv vim-markdown-preview ~/.local/bin/
```

#### `go install`

If you have Go 1.25+ installed, you can build from source without any other tools:

```sh
go install github.com/tjhop/vim-markdown-preview/cmd/vim-markdown-preview@latest
```

The binary is placed in `$GOPATH/bin` (or `$GOBIN` if set). Make sure that directory is on your `$PATH`. All vendored browser assets are embedded at compile time, so the resulting binary is fully self-contained. Version info (`--version`) will report `dev` since goreleaser ldflags are not applied, but the binary is otherwise identical to a release build.

### Step 2: Install the plugin

The VimScript plugin files use the standard Vim plugin layout (`plugin/`, `autoload/`) at the repository root. Install using your preferred plugin manager -- it will find the VimScript files automatically. Since the binary is already on your `$PATH` from Step 1, no post-install build step is needed.

#### Neovim (lazy.nvim)

```lua
{
  'tjhop/vim-markdown-preview',
  ft = { 'markdown' },
}
```

#### Vim 8+ (vim-plug)

```vim
Plug 'tjhop/vim-markdown-preview'
```

#### Vim 8+ (native packages)

```sh
mkdir -p ~/.vim/pack/plugins/start
cd ~/.vim/pack/plugins/start
git clone https://github.com/tjhop/vim-markdown-preview.git
```

#### Verifying the installation

In Neovim, run `:checkhealth mkdp` to verify that the binary is found and report its version. This is the quickest way to diagnose installation problems.

### Alternative: Build from source

If you prefer to compile the binary as part of plugin installation -- for example, to track `main` or a development branch -- you can use a plugin manager post-install hook to build automatically. This requires Go 1.25+.

#### Neovim (lazy.nvim)

```lua
{
  'tjhop/vim-markdown-preview',
  ft = { 'markdown' },
  build = 'go build -o bin/vim-markdown-preview ./cmd/vim-markdown-preview',
}
```

#### Vim 8+ (vim-plug)

```vim
Plug 'tjhop/vim-markdown-preview', { 'do': 'go build -o bin/vim-markdown-preview ./cmd/vim-markdown-preview' }
```

#### Vim 8+ (native packages)

```sh
mkdir -p ~/.vim/pack/plugins/start
cd ~/.vim/pack/plugins/start
git clone https://github.com/tjhop/vim-markdown-preview.git

cd vim-markdown-preview
go build -o bin/vim-markdown-preview ./cmd/vim-markdown-preview
```

### Binary lookup order

The plugin searches for the `vim-markdown-preview` binary in this order:

1. `g:mkdp_binary` (user override, if set)
2. `<plugin-root>/bin/vim-markdown-preview` (plugin-local)
3. `<plugin-root>/vim-markdown-preview` (repo root, where `make build` outputs)
4. `vim-markdown-preview` on `$PATH`

## Usage

### Commands

| Command | Description |
|---|---|
| `:MarkdownPreview` | Open the markdown preview in a browser |
| `:MarkdownPreviewStop` | Close the preview and stop the server |
| `:MarkdownPreviewToggle` | Toggle the preview on or off |

### Key Mappings

The plugin provides `<Plug>` mappings for custom key bindings:

```vim
" Example mappings
nmap <C-p> <Plug>MarkdownPreview
nmap <M-s> <Plug>MarkdownPreviewStop
nmap <C-s> <Plug>MarkdownPreviewToggle
```

## Configuration

All configuration is through `g:mkdp_*` Vim global variables. Set these in your `vimrc` or `init.vim`/`init.lua` before the plugin loads.

### General

```vim
" Auto-open preview when entering a markdown buffer (default: 0)
let g:mkdp_auto_start = 0

" Auto-close preview when the buffer is hidden -- e.g., switching to another
" file, closing the split, or quitting. The server always shuts down on
" VimLeave regardless of this setting. (default: 1)
let g:mkdp_auto_close = 1

" Only refresh preview on save, insert leave, or cursor hold
" instead of every cursor movement (default: 0)
let g:mkdp_refresh_slow = 0

" Make preview commands available in all filetypes, not just markdown (default: 0)
let g:mkdp_command_for_global = 0

" Filetypes that activate the plugin (default: ['markdown'])
let g:mkdp_filetypes = ['markdown']
```

### Browser

```vim
" Browser command to use (empty = system default) (default: '')
let g:mkdp_browser = ''

" Custom Vim function to open the URL (overrides g:mkdp_browser) (default: '')
let g:mkdp_browserfunc = ''

" Echo the preview URL in the command line (default: 0)
let g:mkdp_echo_preview_url = 0
```

#### Platform-specific `g:mkdp_browser` behavior

The behavior of `g:mkdp_browser` varies by platform:

| Platform | Behavior when empty | Behavior when set |
|---|---|---|
| **Linux** | Opens with `xdg-open` | Value is used as the executable. Multi-word values are split on whitespace (e.g., `'firefox --private-window'`). |
| **macOS** | Opens with `open` (system default) | Passed to `open -a` as the application name (e.g., `'Google Chrome'`, `'Firefox'`). Flags after the name are forwarded via `--args`. |
| **Windows** | Opens with `cmd.exe /c start` | Value is used as the executable. |
| **WSL** | Auto-detected; opens browser on the Windows side via `cmd.exe /c start` | Value is used as the executable (e.g., `'wslview'` from the `wslu` package). |

WSL detection works by checking `/proc/version` for the string "microsoft". If detection fails, set `g:mkdp_browser` explicitly.

#### Custom browser function (`g:mkdp_browserfunc`)

**Not yet implemented.** This variable is declared for config compatibility with the original `markdown-preview.nvim` but is currently a no-op. Use `g:mkdp_browser` to override the browser command instead.

### Server

```vim
" Port for the preview server (empty = random) (default: '')
let g:mkdp_port = ''

" Listen on 0.0.0.0 instead of 127.0.0.1 to expose to the network (default: 0)
let g:mkdp_open_to_the_world = 0

" Custom IP for the preview URL (default: '')
let g:mkdp_open_ip = ''
```

### Images

```vim
" Path for serving local images in the preview (default: '')
" When set, the server mounts this directory so relative image paths in your
" markdown resolve correctly in the browser.
let g:mkdp_images_path = ''
```

### Appearance

```vim
" Browser tab title template -- ${name} is replaced with the filename (default: '${name}')
let g:mkdp_page_title = '${name}'

" Force a theme: 'dark', 'light', or '' for system default (default: '')
let g:mkdp_theme = ''

" Path to a custom markdown CSS file (default: '')
let g:mkdp_markdown_css = ''

" Path to a custom syntax highlight CSS file (default: '')
let g:mkdp_highlight_css = ''
```

### Preview Options

```vim
" Detailed preview rendering options
let g:mkdp_preview_options = {
    \ 'mkit': {},
    \ 'katex': {},
    \ 'uml': {},
    \ 'maid': {},
    \ 'disable_sync_scroll': 0,
    \ 'sync_scroll_type': 'middle',
    \ 'hide_yaml_meta': 1,
    \ 'sequence_diagrams': {},
    \ 'flowchart_diagrams': {},
    \ 'content_editable': 0,
    \ 'disable_filename': 0,
    \ 'toc': {}
    \ }
```

| Key | Description |
|---|---|
| `mkit` | Options passed to the markdown-it parser |
| `katex` | Options passed to KaTeX for math rendering |
| `uml` | Options for PlantUML diagrams |
| `maid` | Options for Mermaid diagrams |
| `disable_sync_scroll` | Set to `1` to disable scroll synchronization |
| `sync_scroll_type` | Scroll sync mode: `'middle'`, `'top'`, or `'relative'` |
| `hide_yaml_meta` | Set to `1` to hide YAML front-matter in the preview |
| `sequence_diagrams` | Options for sequence diagram rendering |
| `flowchart_diagrams` | Options for flowchart rendering |
| `content_editable` | Make the preview content editable in the browser |
| `disable_filename` | Set to `1` to hide the filename header |
| `toc` | Options for table of contents generation |

### Combined Preview

```vim
" Reuse a single preview window for all markdown buffers (default: 0)
let g:mkdp_combine_preview = 0

" Auto-refresh combined preview when switching buffers (default: 1)
let g:mkdp_combine_preview_auto_refresh = 1
```

### Binary Override

```vim
" Path to a custom vim-markdown-preview binary (default: auto-detected)
let g:mkdp_binary = '/path/to/vim-markdown-preview'
```

### Runtime Variables

These variables are set by the plugin at runtime. They are read-only from the user's perspective but can be checked in scripts or statusline expressions.

| Variable | Scope | Description |
|---|---|---|
| `g:mkdp_clients_active` | Global | `1` when at least one browser client is connected to the WebSocket server, `0` otherwise. Updated by the Go binary on connect/disconnect. |
| `b:MarkdownPreviewToggleBool` | Buffer-local | `1` when the preview is active for the current buffer, `0` otherwise. Used internally by `:MarkdownPreviewToggle`. |

### Lua Configuration (Neovim)

All `g:mkdp_*` variables can be set via `vim.g` in `init.lua` or a lazy.nvim `opts`/`config` function. You only need to set the values you want to change from their defaults.

```lua
-- General
vim.g.mkdp_auto_start = 0
vim.g.mkdp_auto_close = 1
vim.g.mkdp_refresh_slow = 0
vim.g.mkdp_command_for_global = 0
vim.g.mkdp_filetypes = { 'markdown' }

-- Browser
vim.g.mkdp_browser = ''
vim.g.mkdp_browserfunc = ''
vim.g.mkdp_echo_preview_url = 0

-- Server
vim.g.mkdp_port = ''
vim.g.mkdp_open_to_the_world = 0
vim.g.mkdp_open_ip = ''

-- Images
vim.g.mkdp_images_path = ''

-- Appearance
vim.g.mkdp_page_title = '${name}'
vim.g.mkdp_theme = 'dark'
vim.g.mkdp_markdown_css = ''
vim.g.mkdp_highlight_css = ''

-- Combined preview
vim.g.mkdp_combine_preview = 0
vim.g.mkdp_combine_preview_auto_refresh = 1

-- Preview options
vim.g.mkdp_preview_options = {
  mkit = {},
  katex = {},
  uml = {},
  maid = {},
  disable_sync_scroll = 0,
  sync_scroll_type = 'middle',
  hide_yaml_meta = 1,
  sequence_diagrams = {},
  flowchart_diagrams = {},
  content_editable = 0,
  disable_filename = 0,
  toc = {},
}
```

## Vim vs Neovim

Both editors are first-class. The plugin auto-detects which one you're running and uses the appropriate protocol. Here are the practical differences:

| | Neovim | Vim 8+ |
|---|---|---|
| **Protocol** | msgpack-RPC (automatic) | JSON channels (automatic) |
| **Minimum version** | 0.5+ | 8.1+ |
| **Config syntax** | `vim.g.mkdp_*` (Lua) or `let g:mkdp_*` (VimScript) | `let g:mkdp_*` (VimScript) |
| **`:checkhealth mkdp`** | Built-in (uses `lua/mkdp/health.lua`) | Requires [rhysd/vim-healthcheck](https://github.com/rhysd/vim-healthcheck) polyfill (optional) |
| **Compatibility shim** | Not used | `autoload/nvim/api.vim` provides API compatibility (loaded automatically) |

You do not need to configure the protocol -- the plugin detects whether it's running under Neovim or Vim and selects the right one. The `--mode` flag passed to the Go binary is set automatically by `autoload/mkdp/rpc.vim`.

## Standalone Mode

The binary can run without an editor connection -- useful for quick previews, CI, or development. In this mode, the HTTP server starts without any editor RPC connection.

### File watching (`-file`)

The easiest way to use standalone mode. Point it at a markdown file and a browser opens automatically. The preview updates whenever the file changes on disk (polled every 200ms).

```sh
vim-markdown-preview --standalone --file README.md
# Opens browser automatically, watches for changes, exits when the file is deleted
```

You can combine it with `--port` to use a fixed port:

```sh
vim-markdown-preview --standalone --file README.md --port 8080
```

The server shuts down on `SIGINT`/`SIGTERM` (Ctrl-C), or automatically if the watched file is deleted.

### Manual content posting

Without `-file`, the server exposes a `POST /-/reload` endpoint for pushing markdown content to the browser programmatically.

```sh
vim-markdown-preview --standalone --port 8080
# Preview at: http://127.0.0.1:8080/page/1
```

Send a `POST` request to `/-/reload` with a JSON body containing a `content` field. The content is broadcast to all connected browser clients.

```sh
# Push custom markdown content
curl -X POST http://127.0.0.1:8080/-/reload \
  -H 'Content-Type: application/json' \
  -d '{"content": "# My Document\n\nHello, **world**!\n"}'
```

```sh
# Push the contents of a file
curl -X POST http://127.0.0.1:8080/-/reload \
  -H 'Content-Type: application/json' \
  -d "$(jq -Rs '{content: .}' < README.md)"
```

```sh
# Push with no body to render the built-in sample markdown
curl -X POST http://127.0.0.1:8080/-/reload
```

The endpoint returns `{"ok": true}` on success.

### CLI flags

| Flag | Description |
|---|---|
| `--standalone` | Run without an editor connection |
| `--file <path>` | Markdown file to watch (standalone only; auto-opens browser, auto-reloads on changes) |
| `--port <n>` | Port to listen on (`0` = random) |
| `--mode <nvim\|vim>` | Editor protocol (only used in editor mode, not standalone) |
| `--version` | Print version information and exit |

## Troubleshooting

### `:checkhealth mkdp` (Neovim)

Run `:checkhealth mkdp` in Neovim to verify the binary is found, report its version, and show platform info. This is the first thing to try if the plugin isn't working.

### "Binary not found" error

The plugin couldn't locate the `vim-markdown-preview` executable. Check:

1. Did the plugin manager's post-install command succeed? Look for errors during plugin installation. The `go build` step requires Go 1.25+.
2. Is the binary in one of the expected locations? See [Binary lookup order](#binary-lookup-order).
3. If you installed the binary separately (via `go install` or a release download), is its location on your `$PATH`?
4. You can always set `g:mkdp_binary` to the exact path as a workaround.

### Preview doesn't open / no browser tab

- Check that a browser is available. On headless systems or WSL, set `g:mkdp_browser` to the browser command (e.g., `'wslview'` on WSL, or `'firefox'`).
- If `g:mkdp_echo_preview_url` is set to `1`, the preview URL is printed in the command line -- you can open it manually.
- Make sure the port isn't blocked by a firewall (or use the default random port).

### Preview opens but content is blank

- Verify the server is running: `:MarkdownPreview` should start it. Check `:messages` for `[mkdp]` log lines.
- Try a simple markdown file first to rule out content-specific rendering issues.

### WSL: browser doesn't open

WSL is auto-detected by checking `/proc/version` for "microsoft". When detected, the plugin opens the browser on the Windows side via `cmd.exe /c start`. If auto-detection fails:

- Install `wslu` and set `let g:mkdp_browser = 'wslview'`.
- Or set `let g:mkdp_browser = 'cmd.exe /c start'` as a direct fallback.
- Or set `let g:mkdp_echo_preview_url = 1` and open the printed URL manually.

### Images not loading

Set `g:mkdp_images_path` to the directory containing your images (often the directory of the markdown file). Relative image paths in the markdown are resolved against this directory by the Go binary's HTTP server.

```vim
let g:mkdp_images_path = expand('%:p:h')
```

### Preview feels laggy

Set `g:mkdp_refresh_slow = 1` to only refresh on save, insert leave, or cursor hold instead of every cursor movement. This reduces the volume of content pushed through the WebSocket.

### Server logs / debugging

The Go binary writes structured logs to stderr, which the editor captures. In Neovim or Vim, check `:messages` for `[mkdp]` prefixed log lines.

### Port conflicts

Leave `g:mkdp_port` empty (default) to let the OS assign a random available port. Set it to a specific number only if you need a stable preview URL (e.g., for a bookmarked browser tab).

### Exposing the preview to the network

By default the server listens on `127.0.0.1` (localhost only). To expose it:

```vim
let g:mkdp_open_to_the_world = 1  " listen on 0.0.0.0
let g:mkdp_open_ip = '10.0.0.5'   " optional: customize the IP in the preview URL
```

This is useful behind a reverse proxy, inside a container, or when previewing on a different device on the same network.

### Migrating from `markdown-preview.nvim`

This plugin is a drop-in replacement for the original [markdown-preview.nvim](https://github.com/iamcco/markdown-preview.nvim):

- **Same config namespace** -- all `g:mkdp_*` variables are compatible. Your existing config works as-is.
- **Same commands** -- `:MarkdownPreview`, `:MarkdownPreviewStop`, `:MarkdownPreviewToggle`, and the `<Plug>` mappings are identical.
- **Migration steps**: remove the original plugin, install this one + the binary. No config changes needed.
- **Cannot coexist** -- both plugins define the same commands and variables. Remove one before installing the other.
- **Behavioral note**: both render markdown client-side in the browser. This plugin uses native WebSocket instead of Socket.IO, but the end result is the same. All markdown extensions (KaTeX, mermaid, etc.) are supported.

## Development

### Build

```sh
make build       # Build a binary (runs fmt, tidy, lint, test first)
make test        # Run tests
make lint        # Run linters
make fmt         # Format code
make tidy        # Tidy modules
make build-all   # Build for all platforms (snapshot release)
```

### Vendored Browser Dependencies

Browser-side JavaScript libraries (markdown-it, mermaid, KaTeX, etc.) are vendored into `web/` and embedded into the binary at compile time via `go:embed`. This means no CDN fetches or internet access at runtime.

```sh
make web-vendor  # Download/update vendored browser dependencies
make web-check   # Verify all vendored dependencies are present (CI guard)
```

Library versions are tracked in `web/vendor-manifest.json`.

### Project Structure

```
cmd/vim-markdown-preview/   Entry point, CLI flags
internal/
  editor/                   Editor interface + Neovim/Vim 8 implementations
  server/                   HTTP + WebSocket server, routes, client management
  browser/                  Cross-platform browser opening
  config/                   Config types mapping to g:mkdp_* Vim variables
  version/                  Build version info
web/                        Browser assets (go:embed target)
  js/plugins/               Custom markdown-it plugins
  js/vendor/                Third-party browser libraries
  css/                      Stylesheets
plugin/mkdp.vim             Commands, autocmds, config defaults
autoload/
  mkdp/                     rpc.vim, util.vim, autocmd.vim
  nvim/api.vim              Vim 8 compatibility shim
  health/mkdp.vim           Neovim :checkhealth provider
```

### Go Dependencies

| Library | Purpose |
|---|---|
| [`github.com/neovim/go-client`](https://github.com/neovim/go-client) | Neovim msgpack-RPC |
| [`github.com/coder/websocket`](https://github.com/coder/websocket) | WebSocket server |
| `encoding/json` (stdlib) | Vim 8 JSON channel protocol |
| `embed` (stdlib) | Static asset embedding |
| `log/slog` (stdlib) | Structured logging |

## Credits

This project is a Go reimplementation of [markdown-preview.nvim](https://github.com/iamcco/markdown-preview.nvim) by [iamcco](https://github.com/iamcco). The original browser-side rendering plugins and the VimScript compatibility shim (`autoload/nvim/api.vim`) are adapted from that project. :heart:

## License

Apache License 2.0. See [LICENSE](LICENSE) for the full text.
