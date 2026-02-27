" autoload/mkdp/rpc.vim -- RPC bridge between VimScript and the Go binary.
" Handles server lifecycle (start/stop) and sending notifications for
" content refresh, page close, and browser open events.

let s:mkdp_root_dir = fnamemodify(resolve(expand('<sfile>:p')), ':h:h:h')
let s:mkdp_channel_id = v:null
let s:mkdp_job = v:null
let s:mkdp_server_running = 0

" ---------------------------------------------------------------------------
" Server lifecycle
" ---------------------------------------------------------------------------

" mkdp#rpc#start_server starts the Go binary as an RPC child process.
" Neovim uses msgpack-RPC; Vim 8 uses the JSON channel protocol.
function! mkdp#rpc#start_server() abort
  if s:mkdp_server_running
    return
  endif

  let l:binary = mkdp#rpc#find_binary()
  if empty(l:binary)
    call mkdp#util#echo_messages('Error', ['vim-markdown-preview binary not found'])
    return
  endif

  if has('nvim')
    let s:mkdp_channel_id = jobstart([l:binary, '--mode', 'nvim'], {
          \ 'rpc': v:true,
          \ 'on_stderr': function('s:on_stderr'),
          \ 'on_exit': function('s:on_exit'),
          \ })
    if s:mkdp_channel_id <= 0
      call mkdp#util#echo_messages('Error', ['failed to start server'])
      return
    endif
  else
    let s:mkdp_job = job_start([l:binary, '--mode', 'vim'], {
          \ 'in_mode': 'json',
          \ 'out_mode': 'json',
          \ 'err_cb': function('s:on_stderr_vim'),
          \ 'exit_cb': function('s:on_exit_vim'),
          \ })
    if job_status(s:mkdp_job) !=# 'run'
      call mkdp#util#echo_messages('Error', ['failed to start server'])
      let s:mkdp_job = v:null
      return
    endif
    let s:mkdp_channel_id = job_getchannel(s:mkdp_job)
  endif

  let s:mkdp_server_running = 1
endfunction

" mkdp#rpc#stop_server shuts down the Go binary gracefully.
function! mkdp#rpc#stop_server() abort
  if !s:mkdp_server_running
    return
  endif

  " Send close_all_pages to trigger graceful shutdown. The notification is
  " async, so give the Go binary a moment to process it before we kill the
  " job. Without this, SIGTERM can arrive before the notification is read.
  call mkdp#rpc#notify('close_all_pages', {})
  sleep 50m

  if has('nvim')
    if s:mkdp_channel_id isnot v:null && s:mkdp_channel_id > 0
      try
        call jobstop(s:mkdp_channel_id)
      catch
        " Job may have already exited.
      endtry
    endif
  else
    if s:mkdp_job isnot v:null
      try
        call job_stop(s:mkdp_job)
      catch
        " Job may have already exited.
      endtry
    endif
  endif

  call s:reset_state()
endfunction

" mkdp#rpc#preview_refresh sends a refresh_content notification for the
" current buffer.
function! mkdp#rpc#preview_refresh() abort
  if !s:mkdp_server_running
    return
  endif
  call mkdp#rpc#notify('refresh_content', {'bufnr': bufnr('%')})
endfunction

" mkdp#rpc#preview_close sends a close_page notification for the current
" buffer.
function! mkdp#rpc#preview_close() abort
  if !s:mkdp_server_running
    return
  endif
  call mkdp#rpc#notify('close_page', {'bufnr': bufnr('%')})
endfunction

" mkdp#rpc#open_browser sends an open_browser notification for the current
" buffer.
function! mkdp#rpc#open_browser() abort
  if !s:mkdp_server_running
    return
  endif
  call mkdp#rpc#notify('open_browser', {'bufnr': bufnr('%')})
endfunction

" mkdp#rpc#is_running returns 1 if the server is running.
function! mkdp#rpc#is_running() abort
  return s:mkdp_server_running
endfunction

" ---------------------------------------------------------------------------
" Notification dispatch
" ---------------------------------------------------------------------------

" mkdp#rpc#notify sends a notification to the Go binary. The protocol
" differs between Neovim (rpcnotify) and Vim 8 (ch_sendraw JSON).
function! mkdp#rpc#notify(event, data) abort
  if has('nvim')
    call rpcnotify(s:mkdp_channel_id, a:event, a:data)
  else
    if s:mkdp_channel_id isnot v:null
      call ch_sendraw(s:mkdp_channel_id, json_encode([0, [a:event, a:data]]) . "\n")
    endif
  endif
endfunction

" ---------------------------------------------------------------------------
" Vim 8 data-gathering helpers (called by the Go binary via JSON channel)
" ---------------------------------------------------------------------------

" mkdp#rpc#gather_data collects buffer content and viewport state in a
" single call, avoiding multiple round-trips over the JSON channel.
function! mkdp#rpc#gather_data(bufnr) abort
  return {
        \ 'content':   getbufline(a:bufnr, 1, '$'),
        \ 'cursor':    getpos('.'),
        \ 'winline':   winline(),
        \ 'winheight': winheight(0),
        \ 'options':   get(g:, 'mkdp_preview_options', {}),
        \ 'pageTitle': get(g:, 'mkdp_page_title', '${name}'),
        \ 'theme':     get(g:, 'mkdp_theme', ''),
        \ 'name':      bufname(a:bufnr),
        \ }
endfunction

" mkdp#rpc#gather_config returns g:mkdp_* configuration read by the Go binary.
" Keys consumed only by VimScript (auto_start, auto_close, refresh_slow,
" command_for_global, echo_preview_url, browserfunc, filetypes,
" combine_preview, combine_preview_auto_refresh) are intentionally omitted:
" the Go binary ignores them and sending them is unnecessary overhead.
function! mkdp#rpc#gather_config() abort
  return {
        \ 'open_to_the_world': get(g:, 'mkdp_open_to_the_world', 0),
        \ 'open_ip':           get(g:, 'mkdp_open_ip', ''),
        \ 'browser':           get(g:, 'mkdp_browser', ''),
        \ 'preview_options':   get(g:, 'mkdp_preview_options', {}),
        \ 'markdown_css':      get(g:, 'mkdp_markdown_css', ''),
        \ 'highlight_css':     get(g:, 'mkdp_highlight_css', ''),
        \ 'images_path':       get(g:, 'mkdp_images_path', ''),
        \ 'port':              get(g:, 'mkdp_port', ''),
        \ 'page_title':        get(g:, 'mkdp_page_title', '${name}'),
        \ 'theme':             get(g:, 'mkdp_theme', ''),
        \ }
endfunction

" ---------------------------------------------------------------------------
" Binary lookup
" ---------------------------------------------------------------------------

" mkdp#rpc#find_binary searches for the Go binary in a well-defined order:
" user override (g:mkdp_binary), plugin-local bin/, repo root, then PATH.
" Returns the path to the first executable found, or '' if none.
function! mkdp#rpc#find_binary() abort
  " Check user override.
  if exists('g:mkdp_binary') && executable(g:mkdp_binary)
    return g:mkdp_binary
  endif

  let l:ext = (has('win32') || has('win64')) ? '.exe' : ''

  " Check plugin-local bin directory.
  let l:local = s:mkdp_root_dir . '/bin/vim-markdown-preview' . l:ext
  if executable(l:local)
    return l:local
  endif

  " Check repo root (goreleaser --output . places binary here).
  let l:root = s:mkdp_root_dir . '/vim-markdown-preview' . l:ext
  if executable(l:root)
    return l:root
  endif

  " Fall back to PATH.
  if executable('vim-markdown-preview')
    return 'vim-markdown-preview'
  endif

  return ''
endfunction

" ---------------------------------------------------------------------------
" Internal helpers
" ---------------------------------------------------------------------------

function! s:on_stderr(job_id, data, event) abort
  " Neovim stderr callback -- log output for debugging.
  for l:line in a:data
    if !empty(l:line)
      echom '[mkdp] ' . l:line
    endif
  endfor
endfunction

function! s:on_exit(job_id, exit_code, event) abort
  " Neovim exit callback.
  call s:reset_state()
endfunction

function! s:on_stderr_vim(channel, msg) abort
  " Vim 8 stderr callback.
  echom '[mkdp] ' . a:msg
endfunction

function! s:on_exit_vim(job, status) abort
  " Vim 8 exit callback.
  call s:reset_state()
endfunction

function! s:reset_state() abort
  let s:mkdp_channel_id = v:null
  let s:mkdp_job = v:null
  let s:mkdp_server_running = 0
  " Remove g:mkdp_channel_id so that the startup poll in s:wait_for_server
  " does not terminate early on a fast stop+start cycle. If the variable were
  " left set (to 0 from the previous Vim 8 session), exists('g:mkdp_channel_id')
  " would be true before the new server sets it, causing do_open() to fire
  " before the binary is ready.
  silent! unlet g:mkdp_channel_id
endfunction
