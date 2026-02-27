" autoload/mkdp/util.vim -- High-level orchestration for preview lifecycle.
" Provides open, stop, toggle, and echo_messages for the plugin commands.

let s:waiting_for_server = 0

" mkdp#util#open_preview_page starts the server (if needed), opens the
" browser, sends an initial refresh, and binds buffer autocmds.
"
" Uses timer_start() instead of a synchronous sleep loop to wait for the
" Go binary to become ready. This is necessary because the Go binary's
" first action is FetchConfig(), which sends a channel callback to Vim.
" Vim 8 cannot process channel callbacks during sleep -- the main loop
" must be running. A timer returns control to the main loop between
" firings, allowing the callback to complete and avoiding a deadlock.
function! mkdp#util#open_preview_page() abort
  if s:waiting_for_server
    return
  endif

  if mkdp#rpc#is_running() && exists('g:mkdp_channel_id')
    call s:do_open()
    return
  endif

  call mkdp#rpc#start_server()

  " Bail early if the job failed to start (binary not found, etc.)
  " rather than polling for 5 seconds.
  if !mkdp#rpc#is_running()
    return
  endif

  " Poll via timer_start() -- 100ms interval, up to 50 ticks (5s).
  let s:waiting_for_server = 1
  let s:wait_bufnr = bufnr('%')
  let s:wait_ticks = 0
  let s:wait_timer = timer_start(100, function('s:wait_for_server'), {'repeat': 50})
endfunction

function! s:wait_for_server(timer) abort
  let s:wait_ticks += 1

  if exists('g:mkdp_channel_id')
    call timer_stop(a:timer)
    let s:waiting_for_server = 0
    call s:do_open()
    return
  endif

  " Check if the server died while we were waiting.
  if !mkdp#rpc#is_running()
    call timer_stop(a:timer)
    let s:waiting_for_server = 0
    call mkdp#util#echo_messages('Error', ['server exited unexpectedly'])
    return
  endif

  if s:wait_ticks >= 50
    call timer_stop(a:timer)
    let s:waiting_for_server = 0
    call s:cleanup_wait_state()
    call mkdp#util#echo_messages('Error', ['server did not start in time'])
  endif
endfunction

function! s:cleanup_wait_state() abort
  " Remove the transient polling variables created by open_preview_page.
  " Called from both s:wait_for_server (timeout or success) and s:do_open.
  if exists('s:wait_bufnr') | unlet s:wait_bufnr | endif
  if exists('s:wait_ticks') | unlet s:wait_ticks | endif
  if exists('s:wait_timer') | unlet s:wait_timer | endif
endfunction

function! s:do_open() abort
  call mkdp#rpc#open_browser()
  call mkdp#rpc#preview_refresh()
  call mkdp#autocmd#bind_refresh(exists('s:wait_bufnr') ? s:wait_bufnr : bufnr('%'))

  " Clean up polling state so it does not leak into subsequent calls.
  call s:cleanup_wait_state()

  " PascalCase name preserved from the original markdown-preview.nvim plugin
  " for compatibility with user scripts that may check this variable.
  let b:MarkdownPreviewToggleBool = 1
endfunction

" mkdp#util#stop_preview closes the preview page, unbinds autocmds, and
" stops the server.
function! mkdp#util#stop_preview() abort
  " Cancel any pending startup timer.
  if s:waiting_for_server && exists('s:wait_timer')
    call timer_stop(s:wait_timer)
    let s:waiting_for_server = 0
  endif

  call mkdp#rpc#preview_close()
  call mkdp#autocmd#unbind_refresh(bufnr('%'))
  call mkdp#rpc#stop_server()

  let b:MarkdownPreviewToggleBool = 0
  let g:mkdp_clients_active = 0

  if exists('g:mkdp_channel_id')
    unlet g:mkdp_channel_id
  endif
endfunction

" mkdp#util#toggle_preview toggles the preview on or off for the current
" buffer based on the buffer-local toggle state.
function! mkdp#util#toggle_preview() abort
  if get(b:, 'MarkdownPreviewToggleBool', 0)
    call mkdp#util#stop_preview()
  else
    call mkdp#util#open_preview_page()
  endif
endfunction

" mkdp#util#refresh_combined_preview sends a refresh for the current buffer
" when using combine_preview mode (single preview window for all buffers).
function! mkdp#util#refresh_combined_preview() abort
  if !g:mkdp_clients_active
    return
  endif
  if !get(g:, 'mkdp_combine_preview_auto_refresh', 1)
    return
  endif
  call mkdp#rpc#preview_refresh()
endfunction

" mkdp#util#echo_messages displays messages to the user with appropriate
" highlighting. Type should be 'Error', 'Warning', or 'Info'.
function! mkdp#util#echo_messages(type, msgs) abort
  if a:type ==# 'Error'
    echohl ErrorMsg
  elseif a:type ==# 'Warning'
    echohl WarningMsg
  else
    echohl None
  endif

  for l:msg in a:msgs
    echom '[mkdp] ' . l:msg
  endfor

  echohl None
endfunction
