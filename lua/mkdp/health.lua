-- lua/mkdp/health.lua -- Neovim :checkhealth provider for vim-markdown-preview.
-- Uses the vim.health API (Neovim 0.8+), which supersedes the deprecated
-- health#report_* VimScript functions removed in Neovim 0.11.

local M = {}

function M.check()
  vim.health.start('vim-markdown-preview')

  -- Detect platform.
  local platform
  if vim.fn.has('win32') == 1 or vim.fn.has('win64') == 1 then
    platform = 'Windows'
  elseif vim.fn.has('macunix') == 1 then
    platform = 'macOS'
  else
    platform = 'Linux'
  end
  vim.health.info('Platform: ' .. platform)

  -- Report Neovim version.
  local v = vim.version()
  vim.health.info(('Neovim version: %d.%d.%d'):format(v.major, v.minor, v.patch))

  -- Locate binary using the shared VimScript search function.
  local binary = vim.fn['mkdp#rpc#find_binary']()
  if binary == '' then
    local sfile = debug.getinfo(1, 'S').source:sub(2)
    local root = vim.fn.fnamemodify(vim.fn.resolve(sfile), ':h:h:h')
    vim.health.error('Binary not found', {
      'Run `go install ./cmd/vim-markdown-preview` or build with `make build`',
      'Ensure the binary is in ' .. root .. '/bin/ or on PATH',
    })
    return
  end

  -- Classify the binary source for the diagnostic message.
  local msg
  if vim.g.mkdp_binary ~= nil and binary == vim.g.mkdp_binary then
    msg = 'Binary (user override): ' .. binary
  elseif binary == 'vim-markdown-preview' then
    msg = 'Binary (PATH): ' .. binary
  elseif binary:find('/bin/vim%-markdown%-preview') then
    msg = 'Binary: ' .. binary
  else
    msg = 'Binary (repo root): ' .. binary
  end
  vim.health.ok(msg)

  -- Report binary version.
  local version_output = vim.fn.system(binary .. ' --version')
  if vim.v.shell_error == 0 then
    vim.health.info('Version: ' .. vim.trim(version_output))
  else
    vim.health.warn('Could not determine binary version')
  end
end

return M
