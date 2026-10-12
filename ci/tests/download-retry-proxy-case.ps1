# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# One download behind a proxy, for ci/tests/download-retry.ps1: runs Invoke-Download of deploy/scripts/install-windows.ps1 or
# Invoke-DownloadWithRetry of ci/release/smoke-windows.ps1 on a fake clock (Get-MonotonicSeconds and Start-Sleep are replaced) and
# prints one line "RESULT <json>" with the final message ('' on success) and the seconds of the fake clock. PowerShell 7 reads
# HTTPS_PROXY once, at the first request of the process, so each case needs a process of its own that inherits the environment.
param(
  [Parameter(Mandatory)][ValidateSet('install', 'smoke')][string]$Mode,
  [Parameter(Mandatory)][string]$Script,
  [Parameter(Mandatory)][string]$Url,
  [Parameter(Mandatory)][string]$Work
)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$script:fakeNow = 0.0
$message = ''
$destination = Join-Path $Work 'proxy-case.bin'
if ($Mode -eq 'install') {
  . $Script
  $ErrorActionPreference = 'Stop'
  Set-StrictMode -Version 2.0
  $Insecure = [switch]$false
  function Get-MonotonicSeconds { return $script:fakeNow }
  function Start-Sleep { param([int]$Seconds, [int]$Milliseconds) $script:fakeNow += $Seconds }
  try { Invoke-Download -Url $Url -Destination $destination } catch { $message = $_.Exception.Message }
} else {
  # smoke-windows.ps1 cannot be dot-sourced (it ends on a non-Windows host before it defines anything): take the functions from its
  # syntax tree
  $tokens = $null; $errs = $null
  $ast = [System.Management.Automation.Language.Parser]::ParseFile($Script, [ref]$tokens, [ref]$errs)
  $wanted = 'Invoke-DownloadWithRetry', 'Get-MonotonicSeconds', 'Get-DownloadStatus', 'Get-DownloadReason', 'Test-TransientDownloadError'
  foreach ($fn in $ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] }, $true)) {
    if ($wanted -contains $fn.Name) { . ([scriptblock]::Create($fn.Extent.Text)) }
  }
  function Stop-Smoke { param([string]$Code, [string]$Message) throw "[$Code] $Message" }
  function Write-Log { param([string]$Message) }
  function Get-MonotonicSeconds { return $script:fakeNow }
  function Start-Sleep { param([int]$Seconds, [int]$Milliseconds) $script:fakeNow += $Seconds }
  try { Invoke-DownloadWithRetry -Url $Url -Destination $destination | Out-Null } catch { $message = $_.Exception.Message }
}
Write-Output ('RESULT ' + (@{ message = $message; seconds = $script:fakeNow } | ConvertTo-Json -Compress))
