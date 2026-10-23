# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

#Requires -Version 5.1

# The part of the test of the Windows host end-to-end test (scripts/host-e2e/windows.ps1) that needs no Windows host: the helpers of the
# script, run in PowerShell 7 on any system after the script has been dot-sourced (it does nothing then). ci/tests/host-e2e-test.sh
# starts it and turns its lines "ok ..." and "FAIL ..." into results. The scenario itself runs on a runner of GitHub Actions only.
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Script,
  [Parameter(Mandatory = $true)][string]$Work
)

$ErrorActionPreference = 'Stop'
$env:HOST_E2E_DIR = $Work
# a stand-in, so that the helpers that restrict the ACL of a secret file run on a system without icacls
function icacls.exe { }

. $Script

function Pass { param([string]$Name) Write-Output "ok $Name" }
function Fail { param([string]$Name, [string]$Why = '') Write-Output "FAIL $Name $Why" }
function Check { param([string]$Name, $Expected, $Got) if ([string]$Expected -ceq [string]$Got) { Pass $Name } else { Fail $Name "(expected '$Expected', got '$Got')" } }

# ---- the JSON that Windows PowerShell 5.1 cannot read: an empty property name -------------------------------------------------------
$j = ConvertFrom-ApiJson -Text '{"agent":{"packageStatus":{"packages":{"":{"status":"installed"}}},"health":{"healthy":true},"version":"0.161.0"},"items":[{"id":"a1"}]}'
Check 'the package status under the empty name is read' installed (Format-Value (Get-Field $j 'agent.packageStatus.packages._empty.status'))
Check 'a boolean is true and false, not True and False' 'true/false' ((Format-Value $true) + '/' + (Format-Value $false))
Check 'a missing path is empty' '' (Format-Value (Get-Field $j 'agent.nope.x'))
Check 'a value that ends in an empty string is no key' '' (Format-Value (Get-Field (ConvertFrom-ApiJson -Text '{"a":"","b":"c"}') 'a'))

# ---- results and waiting -------------------------------------------------------------------------------------------------------------
$script:ResultsFile = ''
$null = Test-Eq 'x' 'a' 'a'
$null = Test-Eq 'x' 'a' 'b'
Check 'one pass and one fail are counted' '1/1' "$($script:Passed)/$($script:Failed)"
$script:Passed = 0; $script:Failed = 0
[void](Wait-Settled 'settled at once' 3 'ok' { 'ok' })
[void](Wait-Settled 'never settles' 1 'ok' { 'no' })
Check 'a state that settles passes and one that does not fails' '1/1' "$($script:Passed)/$($script:Failed)"
Check 'Wait-For gives up after the time' 'False' (Wait-For 1 { $false })
Check 'Wait-For takes a condition that throws for a false one' 'False' (Wait-For 1 { throw 'no' })

# ---- the scrubber and the allow-list of the artifacts ------------------------------------------------------------------------------
$scrubbed = Invoke-Scrub -Text "start myre_abcdefghijklmnop12345 end`nAuthorization: Bearer abcdefgh12345`npostgres://u:p@h/db`nMYRIARCH_ENROLLMENT_TOKEN=zzzz`nSet-Cookie: myriarch_session=abc"
foreach ($secret in @('myre_abcdefghijklmnop12345', 'abcdefgh12345', 'u:p@h', 'zzzz', 'myriarch_session=abc')) {
  if ($scrubbed.Contains($secret)) { Fail "the scrubber removes $secret" } else { Pass "the scrubber removes $secret" }
}
Initialize-Scenario -Name 'one-command'
$secretToken = 'myre_SECRETSECRETSECRETSECRETSECRET1234'
Write-Utf8 -Path (Join-Path $script:Dir 'enrollment-token') -Text $secretToken
Write-Utf8 -Path (Join-Path $script:Dir 'command.ps1') -Text "x $secretToken"
Write-Utf8 -Path (Join-Path $script:Dir 'commands-response.json') -Text "{`"command`":`"$secretToken`"}"
Write-Utf8 -Path (Join-Path $script:Dir 'cookies') -Text 'jar'
Write-Utf8 -Path (Join-Path $script:Dir 'overlay.yaml') -Text 'server:'
Write-Utf8 -Path (Join-Path $script:Dir 'myriarchd.log') -Text "log $secretToken"
Write-Utf8 -Path (Join-Path $script:Dir 'myriarchd.out.log') -Text 'out'
Write-Utf8 -Path (Join-Path $script:Dir 'install-command.log') -Text 'installed'
Write-Utf8 -Path (Join-Path $script:Dir 'diag\status-otelcol-supervisor.txt') -Text 'RUNNING'
Write-Utf8 -Path (Join-Path $script:Dir 'diag\environment.txt') -Text "MYRIARCH_ENROLLMENT_TOKEN=$secretToken"
Write-Utf8 -Path (Join-Path $script:Dir 'diag\supervisor.yaml') -Text 'server:'
Write-Utf8 -Path (Join-Path $script:Dir 'api\agents.json') -Text '[]'
New-Item -ItemType Directory -Force -Path (Join-Path $script:Dir 'data\postgres') | Out-Null
Write-Utf8 -Path (Join-Path $script:Dir 'data\postgres\PG_VERSION') -Text '17'
Export-Artifacts
$got = (Get-ChildItem -LiteralPath $script:UploadDir -Recurse -File | ForEach-Object { $_.FullName.Substring($script:UploadDir.Length).TrimStart('\', '/').Replace('\', '/') } | Sort-Object) -join ' '
Check 'the upload directory holds the allow-listed files only' 'api/agents.json diag/environment.txt diag/status-otelcol-supervisor.txt diag/supervisor.yaml install-command.log myriarchd.log myriarchd.out.log results.txt' $got
$all = (Get-ChildItem -LiteralPath $script:UploadDir -Recurse -File | ForEach-Object { [IO.File]::ReadAllText($_.FullName) }) -join "`n"
Check 'no copy of the token is in the upload directory' 'False' $all.Contains($secretToken)
foreach ($never in @('enrollment-token', 'command.ps1', 'commands-response.json', 'cookies', 'overlay.yaml', 'data')) {
  if (Test-Path -LiteralPath (Join-Path $script:UploadDir $never)) { Fail "$never is never uploaded" } else { Pass "$never is never uploaded" }
}
# a secret that gets into the directory removes it
[IO.File]::WriteAllText((Join-Path $script:UploadDir 'results.txt'), "raw $secretToken")
$null = Test-NoSecretsIn -Dir $script:UploadDir 3>&1 6>&1
Check 'a secret in the upload directory removes it' 'False' (Test-Path -LiteralPath $script:UploadDir)
Export-Artifacts
Export-Artifacts
Check 'collecting twice gives the same directory' 8 @(Get-ChildItem -LiteralPath $script:UploadDir -Recurse -File).Count

# ---- the guard ----------------------------------------------------------------------------------------------------------------
$env:CI = ''
$env:HOST_E2E = ''
foreach ($case in @(@('', ''), @('true', ''), @('false', '1'))) {
  $env:CI = $case[0]; $env:HOST_E2E = $case[1]
  $r = & pwsh -NoProfile -NonInteractive -File $Script one-command 2>&1 | Out-String
  $want = $(if ($case[0] -ne 'true') { 'CI=true is not set' } else { 'HOST_E2E=1 is not set' })
  if ($LASTEXITCODE -ne 0 -and $r.Contains("refusing to run: $want")) { Pass "the script refuses without CI=$($case[0]) HOST_E2E=$($case[1])" } else { Fail "the script refuses without CI=$($case[0]) HOST_E2E=$($case[1])" $r }
}
$env:CI = 'true'; $env:HOST_E2E = '1'
$r = & pwsh -NoProfile -NonInteractive -File $Script one-command 2>&1 | Out-String
$onWindows = [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
if (-not $onWindows) {
  if ($LASTEXITCODE -ne 0 -and $r.Contains('refusing to run: this is not Windows')) { Pass 'the script refuses on a system other than Windows' } else { Fail 'the script refuses on a system other than Windows' $r }
}
$r = & pwsh -NoProfile -NonInteractive -File $Script 2>&1 | Out-String
if ($LASTEXITCODE -eq 2 -and $r.Contains('usage: scripts/host-e2e/windows.ps1')) { Pass 'a run without a scenario prints the usage and exits 2' } else { Fail 'a run without a scenario prints the usage and exits 2' "exit $LASTEXITCODE" }
$r = & pwsh -NoProfile -NonInteractive -File $Script nothing 2>&1 | Out-String
if ($LASTEXITCODE -ne 0 -and $r.Contains("unknown scenario 'nothing'")) { Pass 'an unknown scenario is refused' } else { Fail 'an unknown scenario is refused' $r }
Check 'a run with the guard but no scenario work changed no work directory' 'False' (Test-Path -LiteralPath (Join-Path $Work 'tls'))

# ---- the guard on a runner that is not GitHub-hosted: checked before the platform, so it is reached on any system -----------
$env:CI = 'true'; $env:HOST_E2E = '1'; $env:GITHUB_ACTIONS = 'true'; $env:RUNNER_ENVIRONMENT = 'self-hosted'
$r = & pwsh -NoProfile -NonInteractive -File $Script one-command 2>&1 | Out-String
if ($LASTEXITCODE -ne 0 -and $r.Contains('not github-hosted')) { Pass 'the script refuses on a runner that is not GitHub-hosted' } else { Fail 'the script refuses on a runner that is not GitHub-hosted' $r }
$env:GITHUB_ACTIONS = ''; $env:RUNNER_ENVIRONMENT = ''

# ---- a scenario that ends early, and the throwaway CA that stays --------------------------------------------------------------------
# a child process, because Stop-He exits: the stand-ins replace everything that needs a Windows host
$childWork = Join-Path $Work 'child'
$stubs = @'
function Assert-CiHost { }
function Assert-FreshHost { }
function Initialize-Shared { }
function Stop-Fault { }
function Stop-Tracked { }
function Save-Diagnostics { }
function Get-HostLeftovers { return @() }
function Remove-CaTrust { return ($env:CHILD_CA_LEFT -ne '1') }
'@
$childScript = Join-Path $Work 'child.ps1'
[IO.File]::WriteAllText($childScript, "`$env:GITHUB_STEP_SUMMARY = ''`n. '$Script'`n$stubs`nif (`$env:CHILD_MODE -eq 'exit') { Invoke-Scenario -Name 'child' -Body { Stop-He 'boom' } } else { Invoke-Scenario -Name 'child' -Body { } }`nexit `$(if (`$script:Failed -eq 0 -and -not `$script:Aborted) { 0 } else { 1 })`n")
$env:HOST_E2E_DIR = $childWork
# the child runs must not append to the job summary of the real run: a probe file stands in for it
$oldSummary = $env:GITHUB_STEP_SUMMARY
$probe = Join-Path $Work 'step-summary.md'
[IO.File]::WriteAllText($probe, '')
$env:GITHUB_STEP_SUMMARY = $probe
foreach ($case in @(@('exit', '0', 'scenario child|ended early'), @('ok', '1', 'the throwaway CA is out of the machine store'))) {
  $env:CHILD_MODE = $case[0]; $env:CHILD_CA_LEFT = $case[1]
  $null = & pwsh -NoProfile -NonInteractive -File $childScript 2>&1 | Out-String
  $rc = $LASTEXITCODE
  $res = ''
  $resFile = Join-Path $childWork 'child\results.txt'
  if (Test-Path -LiteralPath $resFile) { $res = [IO.File]::ReadAllText($resFile) }
  $want = 'FAIL|' + $case[2]
  $label = $(if ($case[0] -eq 'exit') { 'a scenario that ends with exit counts as failed and writes the FAIL' } else { 'a throwaway CA that stays in the machine store counts as failed' })
  if ($rc -ne 0 -and $res.Contains($want)) { Pass $label } else { Fail $label "(exit $rc, results: $res)" }
}
$env:CHILD_MODE = ''; $env:CHILD_CA_LEFT = ''
Check 'the child runs left the job summary untouched' '0' ([string](Get-Item -LiteralPath $probe).Length)
$env:GITHUB_STEP_SUMMARY = $oldSummary
$env:HOST_E2E_DIR = $Work
Write-Output 'END helpers'
