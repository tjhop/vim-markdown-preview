" plugin/mkdp.vim -- Entry point for the vim-markdown-preview plugin.
" Defines user-facing commands, default configuration, and autocmd groups
" that activate preview commands for configured filetypes.

if exists('g:loaded_mkdp')
  finish
endif
let g:loaded_mkdp = 1

" ---------------------------------------------------------------------------
" Configuration defaults (g:mkdp_*)
" ---------------------------------------------------------------------------

let g:mkdp_auto_start                  = get(g:, 'mkdp_auto_start', 0)
let g:mkdp_auto_close                  = get(g:, 'mkdp_auto_close', 1)
let g:mkdp_refresh_slow                = get(g:, 'mkdp_refresh_slow', 0)
let g:mkdp_command_for_global          = get(g:, 'mkdp_command_for_global', 0)
let g:mkdp_open_to_the_world           = get(g:, 'mkdp_open_to_the_world', 0)
let g:mkdp_open_ip                     = get(g:, 'mkdp_open_ip', '')
let g:mkdp_echo_preview_url            = get(g:, 'mkdp_echo_preview_url', 0)
let g:mkdp_browserfunc                 = get(g:, 'mkdp_browserfunc', '')
let g:mkdp_browser                     = get(g:, 'mkdp_browser', '')
let g:mkdp_preview_options             = get(g:, 'mkdp_preview_options', {
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
      \ })
let g:mkdp_markdown_css                = get(g:, 'mkdp_markdown_css', '')
let g:mkdp_highlight_css               = get(g:, 'mkdp_highlight_css', '')
let g:mkdp_images_path                 = get(g:, 'mkdp_images_path', '')
let g:mkdp_port                        = get(g:, 'mkdp_port', '')
let g:mkdp_page_title                  = get(g:, 'mkdp_page_title', '${name}')
let g:mkdp_filetypes                   = get(g:, 'mkdp_filetypes', ['markdown'])
let g:mkdp_theme                       = get(g:, 'mkdp_theme', '')
let g:mkdp_combine_preview             = get(g:, 'mkdp_combine_preview', 0)
let g:mkdp_combine_preview_auto_refresh = get(g:, 'mkdp_combine_preview_auto_refresh', 1)

" Runtime state: tracks whether any browser clients are connected.
" Updated by the Go binary via SetVar on WebSocket connect/disconnect.
let g:mkdp_clients_active = 0

" Plug mappings for user key binding.
nnoremap <silent> <Plug>MarkdownPreview       :<C-u>MarkdownPreview<CR>
nnoremap <silent> <Plug>MarkdownPreviewStop   :<C-u>MarkdownPreviewStop<CR>
nnoremap <silent> <Plug>MarkdownPreviewToggle :<C-u>MarkdownPreviewToggle<CR>

inoremap <silent> <Plug>MarkdownPreview       <Esc>:<C-u>MarkdownPreview<CR>a
inoremap <silent> <Plug>MarkdownPreviewStop   <Esc>:<C-u>MarkdownPreviewStop<CR>a
inoremap <silent> <Plug>MarkdownPreviewToggle <Esc>:<C-u>MarkdownPreviewToggle<CR>a

" ---------------------------------------------------------------------------
" Command registration
" ---------------------------------------------------------------------------

" s:RegisterCommands creates buffer-local preview commands. Called via
" autocmd when entering a buffer whose filetype matches g:mkdp_filetypes.
function! s:RegisterCommands() abort
  command! -buffer MarkdownPreview       call mkdp#util#open_preview_page()
  command! -buffer MarkdownPreviewStop   call mkdp#util#stop_preview()
  command! -buffer MarkdownPreviewToggle call mkdp#util#toggle_preview()
endfunction

" Register commands globally if configured.
if g:mkdp_command_for_global
  command! MarkdownPreview       call mkdp#util#open_preview_page()
  command! MarkdownPreviewStop   call mkdp#util#stop_preview()
  command! MarkdownPreviewToggle call mkdp#util#toggle_preview()
endif

" ---------------------------------------------------------------------------
" Autocmd groups
" ---------------------------------------------------------------------------

augroup mkdp_register_commands
  autocmd!
  for s:ft in g:mkdp_filetypes
    execute 'autocmd FileType ' . s:ft . ' call s:RegisterCommands()'
  endfor
  unlet s:ft
augroup END

" Auto-start preview when entering a markdown buffer.
if g:mkdp_auto_start
  augroup mkdp_auto_start
    autocmd!
    for s:ft in g:mkdp_filetypes
      execute 'autocmd FileType ' . s:ft . ' call mkdp#util#open_preview_page()'
    endfor
    unlet s:ft
  augroup END
endif

" Combine preview mode: switch preview to the active buffer when switching
" between markdown buffers.
if g:mkdp_combine_preview
  augroup mkdp_combine_preview
    autocmd!
    for s:ft in g:mkdp_filetypes
      execute 'autocmd BufEnter * if &ft ==# ''' . s:ft . ''' | call mkdp#util#refresh_combined_preview() | endif'
    endfor
    unlet s:ft
  augroup END
endif

" Clean up server on Vim exit.
augroup mkdp_vim_leave
  autocmd!
  autocmd VimLeave * call mkdp#rpc#stop_server()
augroup END
