" autoload/health/mkdp.vim -- Vim 8 :healthcheck fallback.
" Neovim 0.8+ uses lua/mkdp/health.lua (vim.health API) instead.
" This file is retained for Vim 8 compatibility only; on Neovim the
" lua/ module takes precedence and this function is never called.

function! health#mkdp#check() abort
  " health#report_* functions exist in Neovim and in the optional
  " rhysd/vim-healthcheck polyfill for Vim 8. Guard against vanilla Vim 8
  " where :checkhealth and these functions are absent.
  if !exists('*health#report_start')
    echom '[mkdp] :checkhealth is not available.'
    echom '[mkdp] Install https://github.com/rhysd/vim-healthcheck for Vim 8 support.'
    return
  endif

  call health#report_start('vim-markdown-preview')

  " Detect platform.
  if has('win32') || has('win64')
    let l:platform = 'Windows'
  elseif has('macunix')
    let l:platform = 'macOS'
  else
    let l:platform = 'Linux'
  endif
  call health#report_info('Platform: ' . l:platform)

  " Check Neovim version.
  if has('nvim')
    let l:version = api_info().version
    let l:ver_str = l:version.major . '.' . l:version.minor . '.' . l:version.patch
    call health#report_info('Neovim version: ' . l:ver_str)
  else
    call health#report_info('Vim version: ' . v:version)
  endif

  " Locate binary using the shared search function.
  let l:binary = mkdp#rpc#find_binary()
  if empty(l:binary)
    let l:root = fnamemodify(resolve(expand('<sfile>:p')), ':h:h:h:h')
    call health#report_error('Binary not found', [
          \ 'Run `go install ./cmd/vim-markdown-preview` or build with `make build`',
          \ 'Ensure the binary is in ' . l:root . '/bin/ or on PATH',
          \ ])
    return
  endif

  " Classify the result for diagnostic reporting.
  if exists('g:mkdp_binary') && l:binary ==# g:mkdp_binary
    call health#report_ok('Binary (user override): ' . l:binary)
  elseif l:binary ==# 'vim-markdown-preview'
    call health#report_ok('Binary (PATH): ' . l:binary)
  elseif l:binary =~# '/bin/vim-markdown-preview'
    call health#report_ok('Binary: ' . l:binary)
  else
    call health#report_ok('Binary (repo root): ' . l:binary)
  endif

  " Report binary version.
  let l:version_output = system(shellescape(l:binary) . ' --version')
  if v:shell_error == 0
    call health#report_info('Version: ' . trim(l:version_output))
  else
    call health#report_warn('Could not determine binary version')
  endif
endfunction
