" autoload/mkdp/autocmd.vim -- Per-buffer autocmd management for preview
" refresh. Supports fast mode (refresh on every cursor move) and slow mode
" (refresh on save, insert leave, or cursor hold only).

" mkdp#autocmd#bind_refresh creates autocmds to trigger content refresh
" for the given buffer. The refresh mode is controlled by g:mkdp_refresh_slow.
function! mkdp#autocmd#bind_refresh(bufnr) abort
  let l:group = 'mkdp_refresh_' . a:bufnr
  execute 'augroup ' . l:group
    autocmd!
    if get(g:, 'mkdp_refresh_slow', 0)
      " Slow mode: only refresh on pause, save, or leaving insert.
      execute 'autocmd CursorHold  <buffer=' . a:bufnr . '> call mkdp#rpc#preview_refresh()'
      execute 'autocmd BufWrite    <buffer=' . a:bufnr . '> call mkdp#rpc#preview_refresh()'
      execute 'autocmd InsertLeave <buffer=' . a:bufnr . '> call mkdp#rpc#preview_refresh()'
    else
      " Fast mode (default): refresh on every cursor movement.
      execute 'autocmd CursorHold    <buffer=' . a:bufnr . '> call mkdp#rpc#preview_refresh()'
      execute 'autocmd CursorHoldI   <buffer=' . a:bufnr . '> call mkdp#rpc#preview_refresh()'
      execute 'autocmd CursorMoved   <buffer=' . a:bufnr . '> call mkdp#rpc#preview_refresh()'
      execute 'autocmd CursorMovedI  <buffer=' . a:bufnr . '> call mkdp#rpc#preview_refresh()'
    endif

    " Auto-close: stop preview when the buffer is hidden.
    if get(g:, 'mkdp_auto_close', 1)
      execute 'autocmd BufHidden <buffer=' . a:bufnr . '> call mkdp#util#stop_preview()'
    endif
  augroup END
endfunction

" mkdp#autocmd#unbind_refresh removes the refresh autocmds for the given
" buffer.
function! mkdp#autocmd#unbind_refresh(bufnr) abort
  let l:group = 'mkdp_refresh_' . a:bufnr
  execute 'augroup ' . l:group
    autocmd!
  augroup END
  " Remove the now-empty augroup.
  execute 'augroup! ' . l:group
endfunction
