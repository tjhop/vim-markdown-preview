" autoload/nvim/api.vim -- Neovim API compatibility shim for Vim 8+.
" Implements a subset of nvim_* functions using Vim 8 native equivalents.
" This allows the Go binary to use a consistent API surface when calling
" back into the editor, regardless of whether it is Neovim or Vim 8.
"
" Only loaded in Vim 8+; Neovim has these functions built in.

if has('nvim')
  finish
endif

" ---------------------------------------------------------------------------
" Buffer functions
" ---------------------------------------------------------------------------

function! nvim_buf_get_lines(bufnr, start, end, strict_indexing) abort
  " Vim's getbufline() uses 1-based line numbers. Neovim's
  " nvim_buf_get_lines uses 0-based start (inclusive) and end (exclusive).
  "
  " Note: negative end values other than -1 are not correctly handled.
  " Neovim treats any negative end as counting from the end of the buffer,
  " but this shim only maps -1 to '$'. In practice only -1 is used (by
  " the Go binary's BufferLines call), so this limitation is acceptable.
  let l:start = a:start >= 0 ? a:start + 1 : a:start
  let l:end = a:end >= 0 ? a:end : '$'
  return getbufline(a:bufnr, l:start, l:end)
endfunction

function! nvim_buf_line_count(bufnr) abort
  if bufloaded(a:bufnr)
    return len(getbufline(a:bufnr, 1, '$'))
  endif
  return 0
endfunction

function! nvim_buf_get_name(bufnr) abort
  return bufname(a:bufnr)
endfunction

function! nvim_buf_get_var(bufnr, name) abort
  return getbufvar(a:bufnr, a:name)
endfunction

function! nvim_buf_set_var(bufnr, name, value) abort
  call setbufvar(a:bufnr, a:name, a:value)
endfunction

function! nvim_buf_del_var(bufnr, name) abort
  " Vim 8 has no direct equivalent of nvim_buf_del_var. Use unlet on the
  " buffer-local variable to fully remove it so exists('b:name') returns 0.
  if a:bufnr == bufnr('%')
    execute 'silent! unlet b:' . a:name
  else
    " For non-current buffers, setting to v:null is the best we can do
    " since unlet only works on the current buffer's variables.
    call setbufvar(a:bufnr, a:name, v:null)
  endif
endfunction

" ---------------------------------------------------------------------------
" Window functions
" ---------------------------------------------------------------------------

function! nvim_win_get_cursor(winid) abort
  " Returns [row, col] (1-based row, 0-based col) matching Neovim behavior.
  let l:pos = getcurpos()
  return [l:pos[1], l:pos[2] - 1]
endfunction

function! nvim_win_get_height(winid) abort
  if a:winid == 0
    return winheight(0)
  endif
  return winheight(win_id2win(a:winid))
endfunction

function! nvim_win_get_width(winid) abort
  if a:winid == 0
    return winwidth(0)
  endif
  return winwidth(win_id2win(a:winid))
endfunction

function! nvim_win_get_var(winid, name) abort
  if a:winid == 0
    return getwinvar(0, a:name)
  endif
  return getwinvar(win_id2win(a:winid), a:name)
endfunction

function! nvim_win_set_var(winid, name, value) abort
  if a:winid == 0
    call setwinvar(0, a:name, a:value)
  else
    call setwinvar(win_id2win(a:winid), a:name, a:value)
  endif
endfunction

" ---------------------------------------------------------------------------
" Global variable functions
" ---------------------------------------------------------------------------

function! nvim_get_var(name) abort
  return get(g:, a:name)
endfunction

function! nvim_set_var(name, value) abort
  let g:[a:name] = a:value
endfunction

function! nvim_del_var(name) abort
  if has_key(g:, a:name)
    unlet g:[a:name]
  endif
endfunction

" ---------------------------------------------------------------------------
" Execution functions
" ---------------------------------------------------------------------------

function! nvim_call_function(fn, args) abort
  return call(a:fn, a:args)
endfunction

function! nvim_eval(expr) abort
  return eval(a:expr)
endfunction

function! nvim_command(cmd) abort
  execute a:cmd
endfunction

function! nvim_out_write(text) abort
  echon a:text
endfunction

function! nvim_err_write(text) abort
  echoerr a:text
endfunction

" ---------------------------------------------------------------------------
" Current state functions
" ---------------------------------------------------------------------------

function! nvim_get_current_buf() abort
  return bufnr('%')
endfunction

function! nvim_get_current_win() abort
  return win_getid()
endfunction

function! nvim_get_current_line() abort
  return getline('.')
endfunction

function! nvim_list_bufs() abort
  return filter(range(1, bufnr('$')), 'buflisted(v:val)')
endfunction

function! nvim_get_mode() abort
  return {'mode': mode(), 'blocking': v:false}
endfunction
