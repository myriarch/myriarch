# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

#Requires -Version 5.1

<#
.SYNOPSIS
  Host end-to-end test, Windows: the installer and the myriarch-updater on a real Windows host (services, registry, ACLs), against a
  myriarchd built from this checkout.

.DESCRIPTION
  It runs on a throwaway runner of GitHub Actions (.github/workflows/host-e2e.yml, job windows) and nowhere else: it installs two
  services, files below C:\Program Files and C:\ProgramData, registry values below HKLM and, for the installer process, the trust of a
  throwaway CA in the machine store (removed again at the end). It therefore refuses to run unless CI=true and HOST_E2E=1 are set, on
  Windows, as an administrator, on a GitHub-hosted runner when it runs in GitHub Actions, and on a host without a trace of an
  installation. Build the binaries first (bin\myriarchd.exe, bin\myriarch-updater.exe, bin\myrctl.exe; the workflow does).

    scripts/host-e2e/windows.ps1 one-command

  Scenario "one-command" (ADR-0046; the board's acceptance of a release candidate on real runners uses it with HOST_E2E_RELEASE_TAG).
  Every step prints PASS, FAIL or SKIP with evidence; the exit status is 0 only without a FAIL:
    1. the installer bundle (zip): built from this checkout in the layout of the release archive, or, with HOST_E2E_RELEASE_TAG, the
       published archive of that release after the keyless check of its checksums.txt
    2. myriarchd (dev mode, https with a throwaway CA, the GitOps fixture with the fleet windows-hosts, a mirror of the real release
       files of the collector and supervisor in 0.161.0 and 0.162.0 as artifact source, verified offline) serving that bundle
    3. an enrollment token created through the API at run time (a file; masked in the log, never printed)
    4. the command rendered through POST /api/v1/install/commands, as the dialog does; the response holds no token
    5. that command run unchanged in an administrator session of this PowerShell (Windows PowerShell 5.1 or PowerShell 7, whichever
       runs this script)
    6. the host registers: a healthy collector in the fleet windows-hosts at the target version of that fleet, the updater linked;
       the services, the account of the supervisor, the registry key and the ACLs are what the documentation promises
    7. a configuration rollout: a changed blueprint in Git reaches the collector
    8. a version update through the updater (the new collector is killed during the health check: rollback by the updater; then the
       retry succeeds)
    9. install-windows.ps1 -Uninstall -Purge, and nothing is left: no service, no file, no registry key, no process, no listener,
       no working directory of the command, and the throwaway CA is out of the machine store
  The token is masked in the log; the upload takes the files of an allow-list only, never the token file or the rendered command.
  After a failure the logs, the state of both services and of the files and the Application event log are collected as artifacts
  (an allow-list, scrubbed).

  Environment: CI, HOST_E2E (guard), HOST_E2E_DIR (work directory, default .cache\host-e2e), HOST_E2E_BIN_DIR (default bin),
  HOST_E2E_RELEASE_TAG, HOST_E2E_PORT_SERVER, _PG, _MIRROR (ports, defaults 18443, 54341, 19443).
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [string]$Scenario = ''
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
# Windows PowerShell 5.1 negotiates TLS 1.0 and 1.1 unless it is told otherwise
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$script:Scenarios = @('one-command')
$script:Root = (Resolve-Path ([IO.Path]::Combine($PSScriptRoot, '..', '..'))).Path
$script:Lib = $PSScriptRoot
$script:Work = $(if ($env:HOST_E2E_DIR) { $env:HOST_E2E_DIR } else { [IO.Path]::Combine($script:Root, '.cache', 'host-e2e') })
$script:Bin = $(if ($env:HOST_E2E_BIN_DIR) { $env:HOST_E2E_BIN_DIR } else { [IO.Path]::Combine($script:Root, 'bin') })
$script:Deploy = [IO.Path]::Combine($script:Root, 'deploy')
$script:Tls = Join-Path $script:Work 'tls'
$script:MirrorBase = Join-Path $script:Work 'mirror'
$script:MirrorDir = [IO.Path]::Combine($script:MirrorBase, 'srv', 'mirror')
$script:Bundles = Join-Path $script:Work 'bundles'

$script:PortServer = $(if ($env:HOST_E2E_PORT_SERVER) { [int]$env:HOST_E2E_PORT_SERVER } else { 18443 })
$script:PortPg = $(if ($env:HOST_E2E_PORT_PG) { [int]$env:HOST_E2E_PORT_PG } else { 54341 })
$script:PortMirror = $(if ($env:HOST_E2E_PORT_MIRROR) { [int]$env:HOST_E2E_PORT_MIRROR } else { 19443 })
$script:Base = "https://127.0.0.1:$($script:PortServer)"

# The two collector versions of the update. They are tied to the GitOps fixture (the frame of gitops-windows/fleets/windows-hosts.yaml),
# to the component list of examples/sim and to the release fixtures of internal/release/testdata; ci/tests/host-e2e-test.sh keeps
# them in step with OTELCOL_VERSION of ci/versions.env.
$script:VersionOld = '0.161.0'
$script:VersionNew = '0.162.0'

$script:ProgramFiles = $(if ($env:ProgramFiles) { $env:ProgramFiles } else { 'C:\Program Files' })
$script:ProgramData = $(if ($env:ProgramData) { $env:ProgramData } else { 'C:\ProgramData' })
$script:InstallRoot = [IO.Path]::Combine($script:ProgramFiles, 'Myriarch')
$script:DataRoot = [IO.Path]::Combine($script:ProgramData, 'Myriarch')
$script:CollectorDir = [IO.Path]::Combine($script:InstallRoot, 'collector')
$script:ConfigDir = [IO.Path]::Combine($script:DataRoot, 'config')
$script:Services = @('otelcol-supervisor', 'myriarch-updater')
# services of other installation methods of the same programs: the installer refuses a host that has one (conflicting_install)
$script:ConflictServices = @('otelcol-contrib', 'opampsupervisor')
$script:ServiceKeyRoot = 'HKLM:\SYSTEM\CurrentControlSet\Services'

# State of the run
$script:Name = ''
$script:Dir = ''
$script:UploadDir = ''
$script:ResultsFile = ''
$script:Tracked = New-Object System.Collections.ArrayList
$script:Session = $null
$script:Csrf = ''
$script:Active = $false
$script:Aborted = $false
$script:Passed = 0
$script:Failed = 0
$script:Skipped = 0
$script:TrustThumbprint = ''
$script:FaultProcess = $null
$script:ReleaseTag = $(if ($env:HOST_E2E_RELEASE_TAG) { $env:HOST_E2E_RELEASE_TAG } else { '' })
$script:BundleFile = ''
$script:BundleSha = ''
# the PowerShell that runs this script is the administrator session in which the rendered command runs
$script:HostExe = (Get-Process -Id $PID).Path

# ------------------------------------------------------------------------------------------------------ output ---
function Write-HeLog { param([string]$Message) Write-Host ("{0} [host-e2e] {1}" -f (Get-Date -Format 'HH:mm:ss'), $Message) }
function Write-HeWarn { param([string]$Message) Write-Host ("{0} [host-e2e] WARNING: {1}" -f (Get-Date -Format 'HH:mm:ss'), $Message) }
function Stop-He { param([string]$Message) Write-Host ("{0} [host-e2e] ERROR: {1}" -f (Get-Date -Format 'HH:mm:ss'), $Message); exit 1 }
function Write-HeSection { param([string]$Message) Write-Host ''; Write-Host "== $Message ==" }

# ----------------------------------------------------------------------------------------------------- results ---
# Evidence must never contain a secret: the callers pass names, counts and status words.
function Add-Result {
  param([string]$Kind, [string]$Name, [string]$Evidence = '')
  $ev = ($Evidence -replace '\s+', ' ').Trim()
  if ($ev.Length -gt 300) { $ev = $ev.Substring(0, 300) }
  if ($script:ResultsFile) {
    [IO.File]::AppendAllText($script:ResultsFile, "$Kind|$Name|$ev`n")
  }
  switch ($Kind) {
    'PASS' { $script:Passed++ }
    'FAIL' { $script:Failed++ }
    default { $script:Skipped++ }
  }
  $suffix = $(if ($ev) { "  [$ev]" } else { '' })
  Write-Host ("  {0,-4} {1}{2}" -f $Kind, $Name, $suffix)
}
function Add-Pass { param([string]$Name, [string]$Evidence = '') Add-Result 'PASS' $Name $Evidence }
function Add-Fail { param([string]$Name, [string]$Evidence = '') Add-Result 'FAIL' $Name $Evidence }
function Add-Skip { param([string]$Name, [string]$Evidence = '') Add-Result 'SKIP' $Name $Evidence }
function Test-Eq {
  param([string]$Name, $Expected, $Got)
  if ([string]$Expected -ceq [string]$Got) { Add-Pass $Name ([string]$Got) } else { Add-Fail $Name "expected '$Expected', got '$Got'" }
}
function Test-Ge {
  param([string]$Name, [int]$Min, $Got)
  $n = 0
  if ([int]::TryParse([string]$Got, [ref]$n) -and $n -ge $Min) { Add-Pass $Name "$n >= $Min" } else { Add-Fail $Name "expected >= $Min, got '$Got'" }
}
function Test-Contains {
  param([string]$Name, [string]$Needle, [string]$Haystack)
  if ($Haystack.Contains($Needle)) { Add-Pass $Name "contains '$Needle'" } else { Add-Fail $Name ("'{0}' not found in: {1}" -f $Needle, $Haystack.Substring(0, [Math]::Min(200, $Haystack.Length))) }
}
function Test-NotContains {
  param([string]$Name, [string]$Needle, [string]$Haystack)
  if ($Haystack.Contains($Needle)) { Add-Fail $Name "'$Needle' found" } else { Add-Pass $Name "absent '$Needle'" }
}
function Test-True {
  param([string]$Name, [scriptblock]$Condition)
  $ok = $false
  try { $ok = [bool](& $Condition) } catch { $ok = $false }
  if ($ok) { Add-Pass $Name } else { Add-Fail $Name 'the condition did not hold' }
}

# Table of the results for the job summary of GitHub Actions and the console.
function Write-Summary {
  Write-Host ''
  Write-Host ("{0} passed, {1} failed, {2} skipped" -f $script:Passed, $script:Failed, $script:Skipped)
  if ($script:ResultsFile -and (Test-Path -LiteralPath $script:ResultsFile)) {
    $lines = @([IO.File]::ReadAllLines($script:ResultsFile))
    if ($script:Failed -gt 0) {
      Write-Host ''
      Write-Host 'Failed checks:'
      foreach ($l in $lines) {
        if ($l.StartsWith('FAIL|')) {
          $p = $l.Split('|')
          $ev = $(if ($p.Length -gt 2) { ($p[2..($p.Length - 1)] -join '/') } else { '' })
          Write-Host ("  - {0} [{1}]" -f $p[1], $ev)
        }
      }
    }
    if ($env:GITHUB_STEP_SUMMARY) {
      $sb = New-Object System.Text.StringBuilder
      [void]$sb.AppendLine("### Host end-to-end test: $($script:Name) (Windows, PowerShell $($PSVersionTable.PSVersion))")
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine("$($script:Passed) passed, $($script:Failed) failed, $($script:Skipped) skipped.")
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('| Result | Check | Evidence |')
      [void]$sb.AppendLine('|---|---|---|')
      foreach ($l in $lines) {
        $p = $l.Split('|')
        if ($p.Length -lt 2) { continue }
        $ev = $(if ($p.Length -gt 2) { ($p[2..($p.Length - 1)] -join '/') } else { '' })
        [void]$sb.AppendLine(("| {0} | {1} | {2} |" -f $p[0], ($p[1] -replace '`', ''), ($ev -replace '`', '')))
      }
      try { [IO.File]::AppendAllText($env:GITHUB_STEP_SUMMARY, $sb.ToString()) } catch { Write-HeWarn 'could not write the job summary' }
    }
  }
}

# --------------------------------------------------------------------------------------------------- identifiers ---
# Product identifiers come from internal/brand (AGENTS.md): the value of one constant of internal/brand/brand.go.
function Get-Brand {
  param([string]$Constant)
  $text = [IO.File]::ReadAllText([IO.Path]::Combine($script:Root, 'internal', 'brand', 'brand.go'))
  $m = [regex]::Match($text, '(?m)^\s*' + [regex]::Escape($Constant) + '\s*=\s*"(.*)"\s*$')
  if (-not $m.Success) { return '' }
  return $m.Groups[1].Value
}

function Get-Sha256Hex {
  param([string]$Path)
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

# Native programs without the NativeCommandError pitfalls of Windows PowerShell 5.1. Result: ExitCode and Output (stdout and stderr).
function Invoke-Native {
  param([string]$File, [string[]]$Arguments = @())
  $old = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  try {
    $global:LASTEXITCODE = 0
    $text = (& $File @Arguments 2>&1 | Out-String)
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $old
  }
  return [pscustomobject]@{ ExitCode = $code; Output = $text.Trim() }
}

function Write-Utf8 {
  param([string]$Path, [string]$Text)
  [IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

# ----------------------------------------------------------------------------------------------------- the guard ---
function Test-Administrator {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($id)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# The script installs services and files below C:\Program Files and C:\ProgramData. It runs on a throwaway runner of CI, nowhere else.
function Assert-CiHost {
  if ($env:CI -ne 'true') {
    Stop-He 'refusing to run: CI=true is not set. This script installs two services and files below C:\Program Files and C:\ProgramData; it runs on a throwaway CI runner only (docs/development.md, Host end-to-end test)'
  }
  if ($env:HOST_E2E -ne '1') {
    Stop-He 'refusing to run: HOST_E2E=1 is not set. It confirms that this machine is a throwaway runner (docs/development.md, Host end-to-end test)'
  }
  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    Stop-He "refusing to run: this is not Windows ($([Environment]::OSVersion.Platform))"
  }
  if ($env:GITHUB_ACTIONS -eq 'true' -and $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    Stop-He "refusing to run: the runner is '$($env:RUNNER_ENVIRONMENT)', not github-hosted. A runner that outlives the job keeps what this script installs"
  }
  if (-not (Test-Administrator)) {
    Stop-He 'refusing to run: the installer needs an elevated session (the runner user of GitHub-hosted Windows runners is an administrator)'
  }
}

function Test-PortListening {
  param([int]$Port)
  $r = Invoke-Native -File 'netstat.exe' -Arguments @('-ano', '-p', 'TCP')
  foreach ($line in ($r.Output -split "`r?`n")) {
    if ($line -match ('^\s*TCP\s+\S+:' + $Port + '\s+\S+\s+LISTENING')) { return $true }
  }
  return $false
}

# One line for every trace of an installation (files, services, registry keys, processes, listeners); nothing means clean. Also the
# check of the reset ("nothing is left") and, before a scenario, the proof that the host is fresh.
function Get-HostLeftovers {
  $left = New-Object System.Collections.ArrayList
  foreach ($p in @($script:InstallRoot, $script:DataRoot)) {
    if (Test-Path -LiteralPath $p) { [void]$left.Add("path $p") }
  }
  $setup = @(Get-ChildItem -LiteralPath $script:ProgramFiles -Force -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'myriarch-setup*' })
  foreach ($d in $setup) { [void]$left.Add("path $($d.FullName)") }
  foreach ($n in ($script:Services + $script:ConflictServices)) {
    if (Get-Service -Name $n -ErrorAction SilentlyContinue) { [void]$left.Add("service $n") }
    if (Test-Path -LiteralPath (Join-Path $script:ServiceKeyRoot $n)) { [void]$left.Add("registry $($script:ServiceKeyRoot)\$n") }
  }
  foreach ($n in @('opampsupervisor', 'otelcol-contrib', 'myriarch-updater')) {
    $procs = @(Get-Process -Name $n -ErrorAction SilentlyContinue)
    if ($procs.Count -gt 0) { [void]$left.Add("process $n (pid $($procs[0].Id))") }
  }
  foreach ($port in @(13133, 13134)) {
    if (Test-PortListening -Port $port) { [void]$left.Add("listener port $port") }
  }
  return $left.ToArray()
}

function Assert-FreshHost {
  $left = @(Get-HostLeftovers)
  if ($left.Count -gt 0) {
    Stop-He ("refusing to run: this host is not fresh. A scenario starts on a machine without any trace of an installation, found: " + ($left -join ';'))
  }
}

# ------------------------------------------------------------------------------------------------------ processes ---
function Wait-For {
  param([int]$Seconds, [scriptblock]$Condition)
  $end = (Get-Date).AddSeconds($Seconds)
  while ((Get-Date) -lt $end) {
    $ok = $false
    try { $ok = [bool](& $Condition) } catch { $ok = $false }
    if ($ok) { return $true }
    Start-Sleep -Seconds 1
  }
  return $false
}

# Waits until the output of the command is Wanted, then records PASS (with the time it took) or, when the time is up, FAIL (with what
# the command printed last). For state that the server does not compute when it is asked: the engine of the rollouts derives the
# phase and the steps of a target on its tick, after the last message of the updater, and the API returns the stored copy
# (docs/design/version-selection.md, "Target progress", "Derivation"); a package status is a message of its own.
function Wait-Settled {
  param([string]$Name, [int]$Seconds, [string]$Wanted, [scriptblock]$Getter)
  $from = Get-Date
  $got = ''
  while ($true) {
    try { $got = [string](& $Getter) } catch { $got = '' }
    if ($got -ceq $Wanted) {
      Add-Pass $Name ("{0}, after {1} s" -f $Wanted, [int]((Get-Date) - $from).TotalSeconds)
      return $true
    }
    if (((Get-Date) - $from).TotalSeconds -ge $Seconds) { break }
    Start-Sleep -Seconds 1
  }
  Add-Fail $Name "wanted '$Wanted', found '$got' after $Seconds s"
  return $false
}

function ConvertTo-ArgumentLine {
  param([string[]]$Arguments)
  return (($Arguments | ForEach-Object { '"' + $_ + '"' }) -join ' ')
}

# Starts a background process of this run (stopped by Stop-Tracked). Standard error goes to the log, standard output next to it
# (<name>.out.log); both are in the allow-list of the artifacts.
function Start-Tracked {
  param([string]$Name, [string]$File, [string[]]$Arguments, [string]$Log)
  $out = $Log -replace '\.log$', '.out.log'
  $p = Start-Process -FilePath $File -ArgumentList (ConvertTo-ArgumentLine $Arguments) -RedirectStandardOutput $out -RedirectStandardError $Log -PassThru -WindowStyle Hidden
  $null = $p.Handle
  [void]$script:Tracked.Add([pscustomobject]@{ Name = $Name; Process = $p })
  return $p
}

function Stop-ProcessTree {
  param([int]$ProcessId)
  $null = Invoke-Native -File 'taskkill.exe' -Arguments @('/PID', [string]$ProcessId, '/T', '/F')
}

# Stops the processes of this run in the reverse order of their start. Windows has no SIGTERM for a console-less process: the tree
# is ended (taskkill /T), which takes the embedded PostgreSQL of myriarchd with it.
function Stop-Tracked {
  for ($i = $script:Tracked.Count - 1; $i -ge 0; $i--) {
    $t = $script:Tracked[$i]
    if (-not $t.Process.HasExited) { Stop-ProcessTree -ProcessId $t.Process.Id }
  }
  foreach ($t in $script:Tracked) { [void]$t.Process.WaitForExit(30000) }
  $script:Tracked.Clear()
  # an embedded PostgreSQL that outlived its parent
  $pm = [IO.Path]::Combine($script:Dir, 'data', 'postgres', 'data', 'postmaster.pid')
  if (Test-Path -LiteralPath $pm) {
    $first = (Get-Content -LiteralPath $pm -TotalCount 1)
    $pgPid = 0
    if ([int]::TryParse([string]$first, [ref]$pgPid) -and $pgPid -gt 0) {
      if (Get-Process -Id $pgPid -ErrorAction SilentlyContinue) { Stop-ProcessTree -ProcessId $pgPid }
    }
  }
}

function Resolve-Python {
  foreach ($n in @('python', 'python3')) {
    $c = Get-Command $n -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($c) {
      $r = Invoke-Native -File $c.Source -Arguments @('--version')
      if ($r.ExitCode -eq 0 -and $r.Output -match 'Python 3') { return $c.Source }
    }
  }
  return $null
}

function Find-OpenSsl {
  $c = Get-Command openssl -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($c) { return $c.Source }
  foreach ($p in @("$($script:ProgramFiles)\Git\usr\bin\openssl.exe", "$($script:ProgramFiles)\Git\mingw64\bin\openssl.exe")) {
    if (Test-Path -LiteralPath $p) { return $p }
  }
  return $null
}

# --------------------------------------------------------------------------------------------------- scenario state ---
function Initialize-Scenario {
  param([string]$Name)
  $script:Name = $Name
  $script:Dir = Join-Path $script:Work $Name
  $script:UploadDir = [IO.Path]::Combine($script:Work, 'upload', $Name)
  $script:ResultsFile = Join-Path $script:Dir 'results.txt'
  New-Item -ItemType Directory -Force -Path $script:Work | Out-Null
  foreach ($d in @($script:Dir, $script:UploadDir)) {
    if (Test-Path -LiteralPath $d) { Remove-Item -LiteralPath $d -Recurse -Force }
  }
  foreach ($d in @($script:Dir, (Join-Path $script:Dir 'diag'), (Join-Path $script:Dir 'api'))) {
    New-Item -ItemType Directory -Force -Path $d | Out-Null
  }
  Write-Utf8 -Path $script:ResultsFile -Text ''
  $script:Passed = 0
  $script:Failed = 0
  $script:Skipped = 0
  $script:Tracked.Clear()
}

# A file for a secret: only SYSTEM and the administrators can read it.
function Protect-SecretFile {
  param([string]$Path)
  $null = Invoke-Native -File 'icacls.exe' -Arguments @($Path, '/inheritance:r', '/grant:r', '*S-1-5-32-544:F', '*S-1-5-18:F')
}

# --------------------------------------------------------------------------------------------------------- TLS ---
# A throwaway CA (EC P-256, two days) and a server certificate for 127.0.0.1 and localhost. The installer needs an https:// URL and the
# server certificate is checked against the certificate store of the machine (the command of the server has no CA option on Windows),
# so the CA is put into the store of the machine for the duration of the scenario (Add-CaTrust) and taken out again by the reset.
# A configuration file of its own keeps the result independent of the openssl.cnf of the machine. The keys stay in the work directory
# and are never uploaded.
function New-ThrowawayCa {
  $ssl = Find-OpenSsl
  if (-not $ssl) { return $false }
  New-Item -ItemType Directory -Force -Path $script:Tls | Out-Null
  $d = $script:Tls
  Write-Utf8 -Path (Join-Path $d 'ca.cnf') -Text @"
[req]
distinguished_name = dn
prompt = no
x509_extensions = ext
[dn]
CN = myriarch host-e2e throwaway CA
[ext]
basicConstraints = critical,CA:TRUE,pathlen:0
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
"@
  Write-Utf8 -Path (Join-Path $d 'server.cnf') -Text @"
[req]
distinguished_name = dn
prompt = no
[dn]
CN = localhost
"@
  Write-Utf8 -Path (Join-Path $d 'server.ext') -Text "subjectAltName=IP:127.0.0.1,DNS:localhost`nbasicConstraints=critical,CA:FALSE`nkeyUsage=critical,digitalSignature`nextendedKeyUsage=serverAuth`n"
  $r = Invoke-Native -File $ssl -Arguments @('req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes',
    '-keyout', (Join-Path $d 'ca.key'), '-out', (Join-Path $d 'ca.pem'), '-days', '2', '-config', (Join-Path $d 'ca.cnf'))
  if ($r.ExitCode -ne 0) { Write-HeWarn "openssl req (CA): $($r.Output)"; return $false }
  $r = Invoke-Native -File $ssl -Arguments @('req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes',
    '-keyout', (Join-Path $d 'server.key'), '-out', (Join-Path $d 'server.csr'), '-config', (Join-Path $d 'server.cnf'))
  if ($r.ExitCode -ne 0) { Write-HeWarn "openssl req (server): $($r.Output)"; return $false }
  $r = Invoke-Native -File $ssl -Arguments @('x509', '-req', '-in', (Join-Path $d 'server.csr'), '-CA', (Join-Path $d 'ca.pem'), '-CAkey', (Join-Path $d 'ca.key'),
    '-CAcreateserial', '-out', (Join-Path $d 'server.pem'), '-days', '2', '-extfile', (Join-Path $d 'server.ext'))
  if ($r.ExitCode -ne 0) { Write-HeWarn "openssl x509 (server): $($r.Output)"; return $false }
  foreach ($f in @('server.csr', 'server.ext', 'ca.srl', 'ca.cnf', 'server.cnf')) { Remove-Item -LiteralPath (Join-Path $d $f) -Force -ErrorAction SilentlyContinue }
  foreach ($f in @('ca.key', 'server.key')) { Protect-SecretFile -Path (Join-Path $d $f) }
  $r = Invoke-Native -File $ssl -Arguments @('x509', '-in', (Join-Path $d 'ca.pem'), '-noout', '-fingerprint', '-sha1')
  $m = [regex]::Match($r.Output, '(?i)fingerprint=([0-9A-F:]+)')
  if ($r.ExitCode -ne 0 -or -not $m.Success) { Write-HeWarn 'cannot read the fingerprint of the CA'; return $false }
  $script:TrustThumbprint = ($m.Groups[1].Value -replace ':', '').ToUpperInvariant()
  return $true
}

function Test-CaTrusted {
  if (-not $script:TrustThumbprint) { return $false }
  return [bool](Get-ChildItem -Path 'Cert:\LocalMachine\Root' -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $script:TrustThumbprint })
}

function Add-CaTrust {
  $r = Invoke-Native -File 'certutil.exe' -Arguments @('-addstore', '-f', 'Root', (Join-Path $script:Tls 'ca.pem'))
  if ($r.ExitCode -ne 0) { Write-HeWarn "certutil -addstore: $($r.Output)"; return $false }
  return (Test-CaTrusted)
}

function Remove-CaTrust {
  if ($script:TrustThumbprint -and (Test-CaTrusted)) {
    $null = Invoke-Native -File 'certutil.exe' -Arguments @('-delstore', 'Root', $script:TrustThumbprint)
  }
  return (-not (Test-CaTrusted))
}

# ------------------------------------------------------------------------------------------------ https file server ---
# any HTTP answer, a 404 for the root included, tells that the server is up and that the certificate is trusted
function Test-HttpsAnswers {
  param([int]$Port)
  try {
    $null = Invoke-WebRequest -UseBasicParsing -Uri "https://127.0.0.1:$Port/" -TimeoutSec 5 -ErrorAction Stop
    return $true
  } catch {
    return ($null -ne $_.Exception.Response)
  }
}

function Start-HttpsServer {
  param([string]$Name, [string]$DocRoot, [int]$Port)
  $py = Resolve-Python
  if (-not $py) { Write-HeWarn 'python 3 is needed for the https file server'; return $false }
  if (Test-PortListening -Port $Port) { Write-HeWarn "port $Port is in use"; return $false }
  $null = Start-Tracked -Name $Name -File $py -Arguments @('-I', (Join-Path $script:Lib 'httpsd.py'), '--directory', $DocRoot, '--port', [string]$Port,
    '--cert', (Join-Path $script:Tls 'server.pem'), '--key', (Join-Path $script:Tls 'server.key')) -Log (Join-Path $script:Dir "$Name.log")
  return (Wait-For 30 { Test-HttpsAnswers -Port $Port })
}

# ----------------------------------------------------------------------------------------------------- the mirror ---
# The collector and supervisor files of both versions in the layout of the upstream releases (scripts/build-mirror-image-crane.sh
# builds the same directory on Linux). Every file is checked before it is served: SHA-256 against the published .sha256, and the
# Sigstore bundle offline against a trusted root fetched through TUF, with `myriarch-updater verify-bundle` (the same code and the
# same identity policy as myriarchd and the updater), so the test runs real upstream files and not copies of them.
function Save-Url {
  param([string]$Url, [string]$Destination)
  $parent = Split-Path -Parent $Destination
  New-Item -ItemType Directory -Force -Path $parent | Out-Null
  $last = $null
  foreach ($attempt in 1..3) {
    try {
      Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $Destination -TimeoutSec 600
      return
    } catch {
      $last = $_.Exception.Message
      Start-Sleep -Seconds (2 * $attempt)
    }
  }
  throw "download of $Url failed: $last"
}

function Get-MirrorAsset { # relative directory, asset name, version, identity regexp (empty: the default of verify-bundle); prints the SHA256SUMS line
  param([string]$RelDir, [string]$Asset, [string]$Version, [string]$Identity)
  $src = 'https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download'
  $dir = Join-Path $script:MirrorDir $RelDir
  foreach ($f in @($Asset, "$Asset.sha256", "$Asset.sigstore.json")) {
    Write-HeLog "download $RelDir/$f"
    Save-Url -Url "$src/$($RelDir -replace '\\', '/')/$f" -Destination (Join-Path $dir $f)
  }
  $want = ([IO.File]::ReadAllText((Join-Path $dir "$Asset.sha256")).Trim() -split '\s+')[0].ToLowerInvariant()
  $got = Get-Sha256Hex -Path (Join-Path $dir $Asset)
  if ($want -notmatch '^[0-9a-f]{64}$' -or $want -ne $got) { throw "SHA-256 of $Asset does not match its .sha256 file (published $want, downloaded $got)" }
  $verifier = Join-Path $script:Bin "$(Get-Brand UpdaterBinary).exe"
  $vargs = @('verify-bundle', '--bundle', (Join-Path $dir "$Asset.sigstore.json"), '--artifact', (Join-Path $dir $Asset), '--version', "v$Version",
    '--trusted-root', (Join-Path $script:MirrorDir 'trusted_root.json'))
  if ($Identity) { $vargs += @('--identity-regexp', $Identity) }
  $r = Invoke-Native -File $verifier -Arguments $vargs
  if ($r.ExitCode -ne 0) { throw "Sigstore verification of $Asset failed (offline): $($r.Output)" }
  Write-HeLog "verified ${Asset}: sha256:$got + Sigstore bundle (offline)"
  return "$got  $($RelDir -replace '\\', '/')/$Asset"
}

function New-Mirror {
  $sums = Join-Path $script:MirrorDir 'SHA256SUMS'
  if ((Test-Path -LiteralPath $sums) -and (Get-Item -LiteralPath $sums).Length -gt 0) {
    Write-HeLog "the mirror is already there ($($script:MirrorDir))"
  } else {
    Write-HeLog "building the mirror of the collector and supervisor files (windows/amd64, $($script:VersionOld) and $($script:VersionNew))"
    New-Item -ItemType Directory -Force -Path $script:MirrorDir | Out-Null
    $verifier = Join-Path $script:Bin "$(Get-Brand UpdaterBinary).exe"
    $r = Invoke-Native -File $verifier -Arguments @('fetch-trusted-root', '--out', (Join-Path $script:MirrorDir 'trusted_root.json'), '--tuf-cache', (Join-Path $script:Work 'tuf-cache'))
    if ($r.ExitCode -ne 0) { throw "cannot fetch the Sigstore trusted root via TUF: $($r.Output)" }
    $lines = New-Object System.Collections.ArrayList
    foreach ($v in @($script:VersionOld, $script:VersionNew)) {
      $esc = [regex]::Escape($v)
      $identity = '^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-binary-release\.yaml@refs/tags/v' + $esc + '$'
      [void]$lines.Add((Get-MirrorAsset -RelDir "v$v" -Asset "otelcol-contrib_${v}_windows_amd64.tar.gz" -Version $v -Identity ''))
      [void]$lines.Add((Get-MirrorAsset -RelDir "cmd\opampsupervisor\v$v" -Asset "opampsupervisor_${v}_windows_amd64.exe" -Version $v -Identity $identity))
    }
    Write-Utf8 -Path $sums -Text ((($lines.ToArray() | Sort-Object) -join "`n") + "`n")
  }
}

# -------------------------------------------------------------------------------------------------- the server ---
# myriarchd in dev mode on 127.0.0.1 with TLS (the dev guard asks for --allow-nonlocal for that), the GitOps fixture, the release
# fixtures of the tests, and the mirror as artifact source with offline Sigstore verification against the trusted root of the mirror.
function Start-Server {
  $gitops = Join-Path $script:Dir 'gitops'
  $fixtures = Join-Path $script:Dir 'fixtures'
  $overlay = Join-Path $script:Dir 'overlay.yaml'
  $data = Join-Path $script:Dir 'data'
  $srv = Join-Path $script:Bin "$(Get-Brand ServerBinary).exe"
  if (-not (Test-Path -LiteralPath $srv)) { Add-Fail 'myriarchd binary' "$srv is missing"; return $false }
  foreach ($d in @($gitops, $fixtures, $data)) { if (Test-Path -LiteralPath $d) { Remove-Item -LiteralPath $d -Recurse -Force } }
  New-Item -ItemType Directory -Force -Path $gitops | Out-Null
  # the blueprint and the policies are those of the Linux test; the fleet is the Windows one
  foreach ($sub in @('blueprints', 'policies')) {
    Copy-Item -LiteralPath ([IO.Path]::Combine($script:Lib, 'gitops', $sub)) -Destination (Join-Path $gitops $sub) -Recurse
  }
  Copy-Item -LiteralPath ([IO.Path]::Combine($script:Lib, 'gitops-windows', 'fleets')) -Destination (Join-Path $gitops 'fleets') -Recurse
  Copy-Item -LiteralPath ([IO.Path]::Combine($script:Root, 'internal', 'release', 'testdata', 'fixtures')) -Destination $fixtures -Recurse
  $ctl = Join-Path $script:Bin "$(Get-Brand CLIBinary).exe"
  $r = Invoke-Native -File $ctl -Arguments @('validate', '--strict', '--components', ([IO.Path]::Combine($script:Root, 'examples', 'sim', 'components-otelcol-contrib-0.162.0.json')), $gitops)
  Write-Utf8 -Path (Join-Path $script:Dir 'validate.log') -Text $r.Output
  if ($r.ExitCode -eq 0) {
    Add-Pass 'myrctl validate --strict of the GitOps fixture' (($r.Output -split "`r?`n") | Select-Object -Last 1)
  } else {
    Add-Fail 'myrctl validate --strict of the GitOps fixture' ((($r.Output -split "`r?`n") | Select-Object -Last 3) -join ' ')
    return $false
  }
  # YAML single quotes: a Windows path stays literal
  $tlsDir = $script:Tls
  $overlayText = @"
server:
  publicUrl: $($script:Base)
  tls:
    certFile: '$tlsDir\server.pem'
    keyFile: '$tlsDir\server.key'
dev:
  postgresPort: $($script:PortPg)
rollouts:
  tickInterval: 1s
outbound:
  caFile: '$tlsDir\ca.pem'
artifacts:
  sourceBaseUrl: https://127.0.0.1:$($script:PortMirror)
  prefetchPlatforms: [windows/amd64]
  verify:
    requireChecksum: true
    requireSignature: true
    trustedRootFile: '$($script:MirrorDir)\trusted_root.json'
install:
  platforms: [windows/amd64]
"@
  Write-Utf8 -Path $overlay -Text $overlayText
  $proc = Start-Tracked -Name 'myriarchd' -File $srv -Log (Join-Path $script:Dir 'myriarchd.log') -Arguments @('dev', '--allow-nonlocal', '--listen', "127.0.0.1:$($script:PortServer)",
    '--gitops-dir', $gitops, '--data-dir', $data, '-c', $overlay, '--releases-fixtures', $fixtures, '--replica-id', 'host-e2e', '--install-bundle-dir', $script:Bundles)
  # the first start downloads the PostgreSQL binaries; a server that has died is not waited for
  $end = (Get-Date).AddSeconds(300)
  $ready = $false
  while ((Get-Date) -lt $end) {
    if ($proc.HasExited) { break }
    try {
      $null = Invoke-WebRequest -UseBasicParsing -Uri "$($script:Base)/readyz" -TimeoutSec 5 -ErrorAction Stop
      $ready = $true
      break
    } catch { Start-Sleep -Seconds 1 }
  }
  if (-not $ready) {
    $tail = ''
    foreach ($f in @('myriarchd.log', 'myriarchd.out.log')) {
      $p = Join-Path $script:Dir $f
      if (Test-Path -LiteralPath $p) { $tail += ((Get-Content -LiteralPath $p -Tail 3) -join ' ') + ' ' }
    }
    Add-Fail 'myriarchd /readyz' ("no answer (process exited: {0}); the last lines of myriarchd.log: {1}" -f $proc.HasExited, $tail.Substring(0, [Math]::Min(250, $tail.Length)))
    return $false
  }
  Add-Pass 'myriarchd /readyz over https' $script:Base
  return $true
}

# -------------------------------------------------------------------------------------------------------- REST ---
# A body that ConvertFrom-Json of Windows PowerShell 5.1 can read: it refuses a property with an empty name, and the package status
# of an agent is keyed by the empty name of the collector package.
function ConvertFrom-ApiJson {
  param([string]$Text)
  if (-not $Text) { return $null }
  $fixed = [regex]::Replace($Text, '([{,]\s*)""(\s*:)', '$1"_empty"$2')
  return ($fixed | ConvertFrom-Json)
}

function Invoke-Api { # METHOD PATH [JSON body]: the response of the server; the session of Connect-Admin
  param([string]$Method, [string]$Path, [string]$Body = '')
  $params = @{ Uri = "$($script:Base)/api/v1$Path"; Method = $Method; UseBasicParsing = $true; WebSession = $script:Session; TimeoutSec = 60 }
  if ($Method -ne 'GET' -and $Method -ne 'HEAD') {
    if (-not $Body) { $Body = '{}' }
    $params['Headers'] = @{ 'X-CSRF-Token' = $script:Csrf }
    $params['ContentType'] = 'application/json'
    $params['Body'] = $Body
  }
  return (Invoke-WebRequest @params)
}

# Why the last call failed: the message and, when the server sent one, the beginning of its problem document (no secret is in it).
$script:LastApiError = ''
function Save-ApiError {
  param($ErrorRecord)
  $detail = ''
  if ($ErrorRecord.ErrorDetails -and $ErrorRecord.ErrorDetails.Message) {
    $detail = $ErrorRecord.ErrorDetails.Message
  } elseif ($ErrorRecord.Exception.Response -and $ErrorRecord.Exception.Response.GetType().GetMethod('GetResponseStream')) {
    try { $detail = (New-Object IO.StreamReader($ErrorRecord.Exception.Response.GetResponseStream())).ReadToEnd() } catch { $detail = '' }
  }
  $text = ($ErrorRecord.Exception.Message + ' ' + $detail) -replace '\s+', ' '
  $script:LastApiError = $text.Substring(0, [Math]::Min(250, $text.Length))
}

function Get-ApiJson { # PATH: the parsed body of a GET, or $null
  param([string]$Path)
  try { return (ConvertFrom-ApiJson -Text ((Invoke-Api -Method 'GET' -Path $Path).Content)) } catch { Save-ApiError $_; return $null }
}

function Send-Api { # METHOD PATH JSON: the parsed answer of a POST, or $null
  param([string]$Method, [string]$Path, $Body = $null)
  $text = '{}'
  if ($null -ne $Body) { $text = ($Body | ConvertTo-Json -Depth 6 -Compress) }
  try { return (ConvertFrom-ApiJson -Text ((Invoke-Api -Method $Method -Path $Path -Body $text).Content)) } catch { Save-ApiError $_; return $null }
}

# The well-known login of dev mode (admin/admin); the cookies and the CSRF token of the session are in Session and Csrf.
function Connect-Admin {
  $script:Session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
  try {
    $null = Invoke-WebRequest -UseBasicParsing -Uri "$($script:Base)/api/v1/auth/dev-login" -Method POST -ContentType 'application/json' `
      -Body '{"username":"admin","password":"admin"}' -WebSession $script:Session -TimeoutSec 30
  } catch {
    Write-HeWarn "dev login failed: $($_.Exception.Message)"
    return $false
  }
  $name = Get-Brand CSRFCookie
  $cookie = $script:Session.Cookies.GetCookies([Uri]$script:Base) | Where-Object { $_.Name -eq $name } | Select-Object -First 1
  if (-not $cookie) { return $false }
  $script:Csrf = $cookie.Value
  return $true
}

# The ::add-mask:: command of GitHub Actions for a secret in a file, so that the runner hides it in every log.
function Write-Mask {
  param([string]$Path)
  if ($env:GITHUB_ACTIONS -eq 'true') { Write-Host ("::add-mask::" + ([IO.File]::ReadAllText($Path).Trim())) }
}

# Creates an enrollment token through the API and writes the secret to a file; the secret is never printed.
function New-EnrollmentToken {
  param([string]$Name, [string]$Path)
  $prefix = Get-Brand EnrollmentTokenPrefix
  $resp = Send-Api -Method 'POST' -Path '/enrollment-tokens' -Body @{ name = $Name }
  $secret = ''
  if ($resp -and $resp.secret) { $secret = [string]$resp.secret }
  if (-not $secret) { return $false }
  Write-Utf8 -Path $Path -Text $secret
  Protect-SecretFile -Path $Path
  if ($secret -notmatch ('^' + [regex]::Escape($prefix) + '[A-Za-z0-9_-]{32,}$')) { return $false }
  Write-Mask -Path $Path
  return $true
}

# ---- queries used by the scenario
function Get-Field { # object, dotted path: the value, or $null
  param($Object, [string]$Path)
  $cur = $Object
  foreach ($part in $Path.Split('.')) {
    if ($null -eq $cur) { return $null }
    $prop = $cur.PSObject.Properties[$part]
    if (-not $prop) { return $null }
    $cur = $prop.Value
  }
  return $cur
}
function Format-Value {
  param($Value)
  if ($Value -is [bool]) { return $(if ($Value) { 'true' } else { 'false' }) }
  if ($null -eq $Value) { return '' }
  return [string]$Value
}
function Get-AgentId { # attribute-key attribute-value
  param([string]$Key, [string]$Value, [string]$Kind = 'collector')
  $r = Get-ApiJson -Path ("/agents?attr={0}%3D{1}&kind={2}" -f $Key, $Value, $Kind)
  if ($r -and $r.items) { return [string](@($r.items)[0].id) }
  return ''
}
function Get-UpdaterId { # collector-agent-id
  param([string]$Collector)
  return (Format-Value (Get-Field (Get-ApiJson -Path "/agents/$Collector") 'agent.updater.agentId'))
}
function Get-AgentValue { # agent-id, dotted path of the response
  param([string]$Id, [string]$Path)
  return (Format-Value (Get-Field (Get-ApiJson -Path "/agents/$Id") $Path))
}
function Test-AgentIs { # a predicate for Wait-For
  param([string]$Id, [string]$Path, [string]$Wanted)
  return ((Get-AgentValue -Id $Id -Path $Path) -ceq $Wanted)
}
function Test-RolloutFinal { # rollout-id: done, paused or ended some other way
  param([string]$Id)
  $s = Format-Value (Get-Field (Get-ApiJson -Path "/rollouts/$Id") 'status')
  return (@('completed', 'paused', 'failed', 'aborted', 'rolled_back', 'superseded') -contains $s)
}
function Get-RolloutSteps { # rollout-id: "name:status,..." of the first target
  param([string]$Id)
  $t = Get-ApiJson -Path "/rollouts/$Id/targets"
  if (-not $t -or -not $t.items) { return '' }
  $first = @($t.items)[0]
  if (-not $first.progress -or -not $first.progress.steps) { return '' }
  return ((@($first.progress.steps) | ForEach-Object { "$($_.name):$($_.status)" }) -join ',')
}
# The phase and the steps of a target are derived by the engine on its tick and stored: read them after Wait-Settled, never at a
# fixed moment after the updater has reported (the engine needs up to rollouts.tickInterval for it).
function Get-TargetPhase {
  param([string]$Id)
  $t = Get-ApiJson -Path "/rollouts/$Id/targets"
  if (-not $t -or -not $t.items) { return '' }
  return (Format-Value (Get-Field (@($t.items)[0]) 'progress.phase'))
}
function Get-PackageStatus { # updater-agent-id: the OpAMP package status of the collector package; "none" before the first report
  param([string]$Id)
  $v = Format-Value (Get-Field (Get-ApiJson -Path "/agents/$Id") 'agent.packageStatus.packages._empty.status')
  return $(if ($v) { $v } else { 'none' })
}
function Start-VersionSelection { # fleet version reason expected-target-version: the id of the rollout the UI action starts
  param([string]$Fleet, [string]$Version, [string]$Reason, [string]$Expected)
  $r = Send-Api -Method 'POST' -Path "/fleets/$Fleet/actions/select-version" -Body @{ version = $Version; reason = $Reason; expectedTargetVersion = $Expected }
  if ($r -and $r.rolloutId) { return [string]$r.rolloutId }
  return ''
}
function Test-GitopsOk { return ((Format-Value (Get-Field (Get-ApiJson -Path '/gitops/status') 'state')) -ceq 'ok') }
function Test-CollectorRegistered { param([string]$Site) return ([bool](Get-AgentId -Key 'site.id' -Value $Site)) }
function Test-UpdaterLinked { param([string]$Collector) return ([bool](Get-UpdaterId -Collector $Collector)) }
function Test-UpdateEnded { # updater-agent-id: the updater has ended its attempt
  param([string]$Id)
  $a = Get-ApiJson -Path "/agents/$Id"
  $comp = Get-Field $a 'agent.health.components'
  if (-not $comp) { return $false }
  $prop = $comp.PSObject.Properties[(Get-Brand UpdateHealthComponent)]
  if (-not $prop) { return $false }
  return (@('rolled_back', 'failed') -contains [string]$prop.Value.status)
}
function Get-ConfigRolloutCount {
  param([string]$Fleet)
  $r = Get-ApiJson -Path "/rollouts?kind=config&fleet=$Fleet&limit=50"
  if ($r -and $r.items) { return @($r.items).Count }
  return 0
}
function Test-EffectiveConfigHas {
  param([string]$Id, [string]$Text)
  $r = Get-ApiJson -Path "/agents/$Id/effective-config"
  return ($r -and $r.body -and ([string]$r.body).Contains($Text))
}
function Get-CollectorProcess { return (Get-Process -Name 'otelcol-contrib' -ErrorAction SilentlyContinue | Select-Object -First 1) }
function Test-CollectorHealthEndpoint {
  try { return ((Invoke-WebRequest -UseBasicParsing -Uri 'http://localhost:13133/' -TimeoutSec 5 -ErrorAction Stop).StatusCode -eq 200) } catch { return $false }
}
function Get-ServiceStatus { # service name: Running, Stopped, ... or "absent"
  param([string]$Name)
  $s = Get-Service -Name $Name -ErrorAction SilentlyContinue
  return $(if ($s) { [string]$s.Status } else { 'absent' })
}
function Get-ServiceConfig { # service name, field of `sc.exe qc` (START_TYPE, SERVICE_START_NAME)
  param([string]$Name, [string]$Field)
  $r = Invoke-Native -File 'sc.exe' -Arguments @('qc', $Name)
  $m = [regex]::Match($r.Output, '(?m)^\s*' + [regex]::Escape($Field) + '\s*:\s*(.*?)\s*$')
  return $(if ($m.Success) { $m.Groups[1].Value } else { '' })
}
# The SIDs of the allow entries of the ACL of a file, a directory or a registry key
function Get-AllowSids {
  param([string]$Path)
  $acl = Get-Acl -LiteralPath $Path
  $sids = @()
  foreach ($ace in $acl.Access) {
    if ($ace.AccessControlType -ne 'Allow') { continue }
    $sids += $ace.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
  }
  return @($sids | Sort-Object -Unique)
}

# ------------------------------------------------------------------------------------------------- the host side ---
# The installer of the checkout, as an administrator, in the PowerShell that runs this script, the way the documentation starts it.
function Invoke-HostScript { # log, arguments after -File: the exit code
  param([string]$Log, [string[]]$Arguments, [int]$TimeoutSeconds = 900)
  Assert-CiHost
  $out = $Log -replace '\.log$', '.stdout.tmp'
  $err = $Log -replace '\.log$', '.stderr.tmp'
  $argLine = '-NoProfile -NonInteractive ' + (($Arguments | ForEach-Object { if ($_ -like '-*' -and $_ -notmatch '\s') { $_ } else { '"' + $_ + '"' } }) -join ' ')
  $p = Start-Process -FilePath $script:HostExe -ArgumentList $argLine -RedirectStandardOutput $out -RedirectStandardError $err -PassThru -WindowStyle Hidden
  $null = $p.Handle
  $code = 124
  if ($p.WaitForExit($TimeoutSeconds * 1000)) { $code = $p.ExitCode } else { Stop-ProcessTree -ProcessId $p.Id }
  $text = ''
  foreach ($f in @($out, $err)) {
    if (Test-Path -LiteralPath $f) { $text += [IO.File]::ReadAllText($f); Remove-Item -LiteralPath $f -Force -ErrorAction SilentlyContinue }
  }
  Write-Utf8 -Path $Log -Text $text
  return $code
}

function Invoke-Installer {
  param([string]$Log, [string[]]$InstallerArguments)
  $script = [IO.Path]::Combine($script:Deploy, 'scripts', 'install-windows.ps1')
  return (Invoke-HostScript -Log $Log -Arguments (@('-ExecutionPolicy', 'RemoteSigned', '-File', $script) + $InstallerArguments))
}

# `-Uninstall -Purge`, then the proof that nothing is left. With Quiet it only cleans up (the exit path).
function Reset-Host {
  param([switch]$Quiet)
  Write-HeLog 'uninstall -Purge'
  $log = Join-Path $script:Dir ("uninstall-{0}.log" -f (Get-Date -Format 'HHmmss'))
  $code = Invoke-Installer -Log $log -InstallerArguments @('-Uninstall', '-Purge')
  if (-not $Quiet) {
    if ($code -eq 0) { Add-Pass 'install-windows.ps1 -Uninstall -Purge' } else { Add-Fail 'install-windows.ps1 -Uninstall -Purge' ("exit $code; " + ((Get-Content -LiteralPath $log -Tail 2) -join ' ')) }
  }
  # a deleted service stays in the registry until the last handle to it is closed
  [void](Wait-For 60 { @(Get-HostLeftovers).Count -eq 0 })
  $left = @(Get-HostLeftovers)
  if ($Quiet) {
    if ($left.Count -gt 0) { Write-HeWarn ("left on the host after the reset: " + ($left -join ';')) }
    return
  }
  if ($left.Count -eq 0) {
    Add-Pass 'nothing is left on the host (files, services, registry keys, processes, ports)'
  } else {
    $s = $left -join ';'
    Add-Fail 'nothing is left on the host' $s.Substring(0, [Math]::Min(280, $s.Length))
  }
}

# Fault injection (scripts/host-e2e/killer.ps1): kills the collector while the active copy is the binary of the new version.
function Start-Fault {
  param([string]$Version)
  $shell = $script:HostExe
  $script:FaultProcess = Start-Process -FilePath $shell -ArgumentList (ConvertTo-ArgumentLine @('-NoProfile', '-NonInteractive', '-File', (Join-Path $script:Lib 'killer.ps1'), '-InstallDir', $script:CollectorDir, '-Version', $Version, '-Interval', '0.3')) `
    -RedirectStandardOutput (Join-Path $script:Dir 'fault.log') -RedirectStandardError (Join-Path $script:Dir 'fault.err.tmp') -PassThru -WindowStyle Hidden
  $null = $script:FaultProcess.Handle
  return (-not $script:FaultProcess.HasExited)
}
function Stop-Fault {
  if ($script:FaultProcess -and -not $script:FaultProcess.HasExited) { Stop-ProcessTree -ProcessId $script:FaultProcess.Id }
  $script:FaultProcess = $null
}

# The wait times of the installed updater, shortened for the test (the same values as the Linux test): the file is edited and the
# updater restarted.
function Set-UpdaterTuning {
  $py = Resolve-Python
  if (-not $py) { return $false }
  $r = Invoke-Native -File $py -Arguments @('-I', (Join-Path $script:Lib 'tune-updater.py'), (Join-Path $script:ConfigDir 'updater.yaml'))
  if ($r.ExitCode -ne 0) { Write-HeWarn "tune-updater.py: $($r.Output)"; return $false }
  Restart-Service -Name 'myriarch-updater' -Force
  return $true
}

# ---------------------------------------------------------------------------------- installer bundle and command ---
# The server serves the installer bundle (the updater archive of a release: scripts, templates and myriarch-updater.exe) and renders
# the command that downloads it. In the test the server is a development server that reads the bundle from a directory
# (myriarchd dev --install-bundle-dir), because a commit has no signed release: the bundle is built from the checkout in the layout of
# the release archive (deploy/release/goreleaser.yaml, id myriarch-updater, zip for Windows), or, with HOST_E2E_RELEASE_TAG, it is the
# published archive of that release, whose checksums.txt is verified keyless first (the check the server itself makes for a bundle of
# its release).
function New-BundleFromCheckout {
  $version = ([IO.File]::ReadAllText((Join-Path $script:Root 'VERSION'))).Trim()
  $updater = Get-Brand UpdaterBinary
  $name = "${updater}_${version}_windows_amd64.zip"
  $stage = Join-Path $script:Work 'bundle-stage'
  foreach ($d in @($stage, $script:Bundles)) { if (Test-Path -LiteralPath $d) { Remove-Item -LiteralPath $d -Recurse -Force } }
  New-Item -ItemType Directory -Force -Path (Join-Path $stage 'deploy'), $script:Bundles | Out-Null
  New-Item -ItemType Directory -Force -Path ([IO.Path]::Combine($stage, 'deploy', 'scripts')) | Out-Null
  Copy-Item -LiteralPath ([IO.Path]::Combine($script:Deploy, 'scripts', 'install-windows.ps1')) -Destination ([IO.Path]::Combine($stage, 'deploy', 'scripts'))
  foreach ($sub in @('supervisor', 'updater', 'windows')) {
    Copy-Item -LiteralPath (Join-Path $script:Deploy $sub) -Destination ([IO.Path]::Combine($stage, 'deploy', $sub)) -Recurse
  }
  Copy-Item -LiteralPath (Join-Path $script:Bin "$updater.exe") -Destination (Join-Path $stage "$updater.exe")
  Copy-Item -LiteralPath (Join-Path $script:Root 'LICENSE') -Destination $stage
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  [IO.Compression.ZipFile]::CreateFromDirectory($stage, (Join-Path $script:Bundles $name))
  Remove-Item -LiteralPath $stage -Recurse -Force
  $script:BundleFile = $name
}

# The published bundle of a release: the archive, checksums.txt and its keyless Sigstore bundle from the release page; checksums.txt is
# verified with the updater of this checkout against the release workflow of the tag (identity, issuer and tag are fixed here), and
# the archive against its line.
function Get-BundleFromRelease {
  param([string]$Tag)
  if ($Tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$') { Write-HeWarn "the release tag '$Tag' is not of the form vX.Y.Z or vX.Y.Z-rc.N"; return $false }
  $version = $Tag.Substring(1)
  $updater = Get-Brand UpdaterBinary
  $repo = Get-Brand Repository
  $name = "${updater}_${version}_windows_amd64.zip"
  $base = "$repo/releases/download/$Tag"
  $dir = Join-Path $script:Work 'bundle-download'
  foreach ($d in @($dir, $script:Bundles)) { if (Test-Path -LiteralPath $d) { Remove-Item -LiteralPath $d -Recurse -Force } }
  New-Item -ItemType Directory -Force -Path $dir, $script:Bundles | Out-Null
  foreach ($f in @($name, 'checksums.txt', 'checksums.txt.sigstore.json')) {
    try { Save-Url -Url "$base/$f" -Destination (Join-Path $dir $f) } catch { Write-HeWarn "cannot download $base/$f"; return $false }
  }
  $identity = '^' + [regex]::Escape($repo) + '/\.github/workflows/release\.yml@refs/tags/' + [regex]::Escape($Tag) + '$'
  $r = Invoke-Native -File (Join-Path $script:Bin "$updater.exe") -Arguments @('verify-bundle', '--bundle', (Join-Path $dir 'checksums.txt.sigstore.json'),
    '--artifact', (Join-Path $dir 'checksums.txt'), '--version', $Tag, '--identity-regexp', $identity, '--issuer', 'https://token.actions.githubusercontent.com',
    '--tuf-cache', (Join-Path $script:Work 'tuf-cache'))
  if ($r.ExitCode -ne 0) { Write-HeWarn "checksums.txt of $Tag is not signed by the release workflow: $($r.Output)"; return $false }
  $want = ''
  foreach ($line in [IO.File]::ReadAllLines((Join-Path $dir 'checksums.txt'))) {
    $m = [regex]::Match($line, '^([0-9a-fA-F]{64})\s+\*?(.+?)\s*$')
    if ($m.Success -and $m.Groups[2].Value -ceq $name) { $want = $m.Groups[1].Value.ToLowerInvariant() }
  }
  $got = Get-Sha256Hex -Path (Join-Path $dir $name)
  if (-not $want -or $want -ne $got) { Write-HeWarn "$name does not match its line in the signed checksums.txt"; return $false }
  Move-Item -LiteralPath (Join-Path $dir $name) -Destination (Join-Path $script:Bundles $name)
  $script:BundleFile = $name
  return $true
}

function Initialize-Bundle {
  if ($script:ReleaseTag) {
    if (-not (Get-BundleFromRelease -Tag $script:ReleaseTag)) { return $false }
  } else {
    New-BundleFromCheckout
  }
  $script:BundleSha = Get-Sha256Hex -Path (Join-Path $script:Bundles $script:BundleFile)
  return $true
}

# Renders the command for this host through the API, as the dialog does: the variant with the token in the command (a placeholder,
# which the dialog replaces in the browser), for windows/amd64, with the fleet and the site of the scenario. Writes the response to
# commands-response.json (not in the allow-list of the artifacts: it holds the rendered command) and the command with the token put
# in to a file, which is never uploaded either.
function New-InstallCommand {
  param([string]$Fleet, [string]$SiteId, [string]$TokenFile, [string]$OutFile)
  $platform = 'windows/amd64'
  $body = @{
    variant = 'token_inline'; platforms = @($platform); fleet = $Fleet
    attributes = @{ 'site.type' = 'edge'; 'site.id' = $SiteId; 'site.name' = 'Host end-to-end test'; 'site.country' = 'NL'; 'site.timezone' = 'Europe/Amsterdam' }
  } | ConvertTo-Json -Depth 5 -Compress
  $resp = Join-Path $script:Dir 'commands-response.json'
  try { $content = (Invoke-Api -Method 'POST' -Path '/install/commands' -Body $body).Content } catch { Save-ApiError $_; Write-Utf8 -Path $resp -Text $script:LastApiError; return $false }
  Write-Utf8 -Path $resp -Text $content
  $parsed = ConvertFrom-ApiJson -Text $content
  $placeholder = [string]$parsed.tokenPlaceholder
  $cmd = ''
  foreach ($c in @($parsed.commands)) { if ($c.platform -ceq $platform) { $cmd = [string]$c.command; break } }
  if (-not $placeholder -or -not $cmd) { return $false }
  $token = ([IO.File]::ReadAllText($TokenFile)).Trim()
  if (-not $token) { return $false }
  # String.Replace is literal: the token appears in no argument list and in no pattern
  Write-Utf8 -Path $OutFile -Text ($cmd.Replace($placeholder, $token) + "`n")
  Protect-SecretFile -Path $OutFile
  return $true
}

# Runs the rendered command unchanged, the way a person pastes it into an elevated PowerShell: its text is evaluated as it stands, in
# a new session of the PowerShell that runs this script. The output goes to a log; the token is neither in the command line nor in the
# output.
function Invoke-InstallCommand {
  param([string]$CommandFile, [string]$Log)
  $expr = "Invoke-Expression ([IO.File]::ReadAllText('" + $CommandFile.Replace("'", "''") + "'))"
  return (Invoke-HostScript -Log $Log -Arguments @('-Command', $expr))
}

# ------------------------------------------------------------------------------------------------------ diagnostics ---
# What the host looks like, for the artifacts of a failed run. Names, modes and logs; never the registry value of the service
# environment (it holds the enrollment token) or the content of the token file.
function Save-Diagnostics {
  $d = Join-Path $script:Dir 'diag'
  Write-HeLog 'collecting diagnostics'
  foreach ($n in $script:Services) {
    $text = ''
    foreach ($sc in @(@('query', $n), @('qc', $n), @('qfailure', $n))) { $text += (Invoke-Native -File 'sc.exe' -Arguments $sc).Output + "`n" }
    Write-Utf8 -Path (Join-Path $d "status-$n.txt") -Text $text
    try {
      $events = Get-WinEvent -FilterHashtable @{ LogName = 'Application'; ProviderName = $n } -MaxEvents 200 -ErrorAction Stop | Format-List TimeCreated, Id, LevelDisplayName, Message | Out-String
      Write-Utf8 -Path (Join-Path $d "events-$n.txt") -Text $events
    } catch {
      Write-Utf8 -Path (Join-Path $d "events-$n.txt") -Text "no events: $($_.Exception.Message)"
    }
  }
  try {
    $files = ''
    foreach ($root in @($script:InstallRoot, $script:DataRoot)) {
      if (Test-Path -LiteralPath $root) {
        $files += (Get-ChildItem -LiteralPath $root -Recurse -Depth 4 -Force -ErrorAction SilentlyContinue | ForEach-Object { "{0,-8} {1,12} {2}" -f $_.Mode, $_.Length, $_.FullName }) -join "`n"
        $files += "`n"
      }
    }
    Write-Utf8 -Path (Join-Path $d 'files.txt') -Text $files
  } catch { Write-HeWarn "file listing failed: $($_.Exception.Message)" }
  $acls = ''
  foreach ($p in @($script:InstallRoot, $script:DataRoot, $script:ConfigDir)) {
    if (Test-Path -LiteralPath $p) { $acls += (Invoke-Native -File 'icacls.exe' -Arguments @($p)).Output + "`n" }
  }
  foreach ($n in $script:Services) {
    $key = Join-Path $script:ServiceKeyRoot $n
    if (Test-Path -LiteralPath $key) {
      try { $acls += "registry key $n`n" + (((Get-Acl -LiteralPath $key).Access | Format-Table IdentityReference, AccessControlType, RegistryRights -AutoSize | Out-String)) + "`n" } catch { $acls += "registry key ${n}: $($_.Exception.Message)`n" }
    }
  }
  Write-Utf8 -Path (Join-Path $d 'acl.txt') -Text $acls
  foreach ($f in @('supervisor.yaml', 'updater.yaml')) {
    $p = Join-Path $script:ConfigDir $f
    if (Test-Path -LiteralPath $p) { Copy-Item -LiteralPath $p -Destination (Join-Path $d $f) -Force }
  }
  $log = [IO.Path]::Combine($script:DataRoot, 'logs', 'myriarch-updater.log')
  if (Test-Path -LiteralPath $log) { Write-Utf8 -Path (Join-Path $d 'updater-log.txt') -Text ((Get-Content -LiteralPath $log -Tail 600) -join "`n") }
  $procs = Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Name -match 'opampsupervisor|otelcol|myriarch|python|postgres' } | ForEach-Object {
    $path = ''
    try { $path = $_.Path } catch { $path = '' }
    "{0,6} {1} {2}" -f $_.Id, $_.Name, $path
  }
  Write-Utf8 -Path (Join-Path $d 'processes.txt') -Text ($procs -join "`n")
  Write-Utf8 -Path (Join-Path $d 'listeners.txt') -Text ((Invoke-Native -File 'netstat.exe' -Arguments @('-ano', '-p', 'TCP')).Output)
  if ($script:Session -and $script:Csrf) {
    try {
      $agents = Get-ApiJson -Path '/agents?limit=50'
      if ($agents) {
        $slim = @($agents.items) | ForEach-Object { [pscustomobject]@{ id = $_.id; kind = $_.kind; hostname = $_.hostname; version = $_.version; status = $_.connection.status; healthy = $_.health.healthy; configSync = $_.configSync } }
        Write-Utf8 -Path ([IO.Path]::Combine($script:Dir, 'api', 'agents.json')) -Text ($slim | ConvertTo-Json -Depth 4)
      }
      foreach ($pair in @(@('rollouts.json', '/rollouts?limit=20'), @('gitops-status.json', '/gitops/status'))) {
        $resp = Invoke-Api -Method 'GET' -Path $pair[1]
        Write-Utf8 -Path ([IO.Path]::Combine($script:Dir, 'api', $pair[0])) -Text $resp.Content
      }
    } catch { Write-HeWarn "API snapshot failed: $($_.Exception.Message)" }
  }
}

# ------------------------------------------------------------------------------------------------------ artifacts ---
# What leaves the runner is an allow-list, assembled here and scrubbed: the workflow uploads the upload directory and nothing else.
# These files are never in the list, so that they cannot be uploaded by accident: the token file, the rendered command and the
# response that holds it, the cookies, the keys of the CA, the database (data), the mirror, the bundle and every file of the host that
# holds a secret (the token file in the configuration directory, the registry value of the service).
$script:ArtifactFiles = @('results.txt', 'validate.log', 'myriarchd*.log', 'mirror*.log', 'fault.log', 'install-*.log', 'uninstall-*.log', 'diag\*.txt', 'diag\*.yaml', 'api\*.json')

# Removes what must not appear in a log that anyone who is signed in to GitHub can download: enrollment, API and download tokens,
# bearer credentials, DSNs, session cookies and private keys.
function Invoke-Scrub {
  param([string]$Text)
  $cookies = (Get-Brand SessionCookie) + '|' + (Get-Brand CSRFCookie)
  $t = $Text
  $t = [regex]::Replace($t, '-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----', '[private key removed]', [Text.RegularExpressions.RegexOptions]::Singleline)
  $t = [regex]::Replace($t, 'myr[aed]_[A-Za-z0-9_.~-]+', 'myrX_[removed]')
  $t = [regex]::Replace($t, '(?i)\b(MYRIARCH_ENROLLMENT_TOKEN=)\S+', '$1[removed]')
  $t = [regex]::Replace($t, '(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}', '$1 [removed]')
  $t = [regex]::Replace($t, '(?i)\b(authorization\s*[:=]\s*)\S.*', '$1[removed]')
  $t = [regex]::Replace($t, 'postgres(?:ql)?://[^\s"<>]+', 'postgres://[removed]')
  $t = [regex]::Replace($t, '(?i)\b((?:__Host-)?(?:' + $cookies + ')=)[^;\s"]+', '$1[removed]')
  $t = [regex]::Replace($t, '(?im)\b(set-cookie|cookie)\s*:.*', '$1: [removed]')
  return $t
}

# Fails (and removes the directory) when a secret of this run, or something shaped like one, is in a directory that is to be uploaded.
function Test-NoSecretsIn {
  param([string]$Dir)
  if (-not (Test-Path -LiteralPath $Dir)) { return $true }
  $secrets = @()
  foreach ($f in @((Join-Path $script:Dir 'enrollment-token'), (Join-Path $script:Tls 'ca.key'), (Join-Path $script:Tls 'server.key'))) {
    if (Test-Path -LiteralPath $f) {
      foreach ($line in [IO.File]::ReadAllLines($f)) { if ($line.Trim() -and -not $line.StartsWith('-----')) { $secrets += $line.Trim() } }
    }
  }
  $hits = ''
  foreach ($file in @(Get-ChildItem -LiteralPath $Dir -Recurse -File -Force -ErrorAction SilentlyContinue)) {
    $text = [IO.File]::ReadAllText($file.FullName)
    foreach ($s in $secrets) { if ($text.Contains($s)) { $hits += ' content-of-a-secret' } }
    if ($text -match 'myr[aed]_[A-Za-z0-9]{16,}' -or $text -match '-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----') { $hits += ' token-or-key-pattern' }
  }
  if ($hits) {
    Remove-Item -LiteralPath $Dir -Recurse -Force
    Write-HeWarn "a secret was found in the upload directory ($($hits.Trim())): the directory was removed"
    return $false
  }
  return $true
}

# Assembles the upload directory from the allow-list. Every call starts from an empty directory and ends with the same check, so it
# can be repeated: Complete-Scenario calls it after the diagnostics of a failure and again at the end.
function Export-Artifacts {
  if (-not $script:Dir -or -not (Test-Path -LiteralPath $script:Dir)) { return }
  if (Test-Path -LiteralPath $script:UploadDir) { Remove-Item -LiteralPath $script:UploadDir -Recurse -Force }
  New-Item -ItemType Directory -Force -Path $script:UploadDir | Out-Null
  foreach ($pattern in $script:ArtifactFiles) {
    foreach ($f in @(Get-ChildItem -Path (Join-Path $script:Dir $pattern) -File -ErrorAction SilentlyContinue)) {
      $rel = $f.FullName.Substring($script:Dir.Length).TrimStart('\')
      $out = Join-Path $script:UploadDir $rel
      New-Item -ItemType Directory -Force -Path (Split-Path -Parent $out) | Out-Null
      Write-Utf8 -Path $out -Text (Invoke-Scrub -Text ([IO.File]::ReadAllText($f.FullName)))
    }
  }
  $build = Join-Path $script:Work 'mirror-build.log'
  if (Test-Path -LiteralPath $build) { Write-Utf8 -Path (Join-Path $script:UploadDir 'mirror-build.log') -Text (Invoke-Scrub -Text ([IO.File]::ReadAllText($build))) }
  if (-not (Test-NoSecretsIn -Dir $script:UploadDir)) { return }
  Write-HeLog ("artifacts for the upload: {0} ({1} files)" -f $script:UploadDir, @(Get-ChildItem -LiteralPath $script:UploadDir -Recurse -File -ErrorAction SilentlyContinue).Count)
}

# ---------------------------------------------------------------------------------------------------- scenarios ---
# What every scenario needs once: binaries, the CA and its trust, the mirror (and its server).
function Initialize-Shared {
  foreach ($b in @((Get-Brand ServerBinary), (Get-Brand UpdaterBinary), (Get-Brand CLIBinary))) {
    if (-not (Test-Path -LiteralPath (Join-Path $script:Bin "$b.exe"))) { Stop-He "$($script:Bin)\$b.exe is missing: build the binaries first" }
  }
  if (-not (Resolve-Python)) { Stop-He 'python 3 is needed (the https file server and the tuning of the updater)' }
  if (-not (Find-OpenSsl)) { Stop-He 'openssl is needed for the throwaway CA (Git for Windows ships one)' }
  if (-not (New-ThrowawayCa)) { Stop-He 'cannot create the throwaway CA (openssl)' }
  Write-HeLog "throwaway CA created ($($script:Tls))"
  if (-not (Add-CaTrust)) { Stop-He 'cannot put the throwaway CA into the machine store (certutil)' }
  Write-HeLog 'the throwaway CA is trusted by this machine for the duration of the scenario'
  try { New-Mirror } catch { Stop-He "the mirror could not be built: $($_.Exception.Message)" }
  if (-not (Start-HttpsServer -Name 'mirror' -DocRoot $script:MirrorDir -Port $script:PortMirror)) { Stop-He "the mirror server does not answer on port $($script:PortMirror) (see $($script:Dir)\mirror.log)" }
  Add-Pass "mirror: collector and supervisor files of $($script:VersionOld) and $($script:VersionNew), verified offline" "https://127.0.0.1:$($script:PortMirror)"
  try {
    $code = (Invoke-WebRequest -UseBasicParsing -Uri "https://127.0.0.1:$($script:PortMirror)/cmd/opampsupervisor/v$($script:VersionNew)/opampsupervisor_$($script:VersionNew)_windows_amd64.exe.sha256" -TimeoutSec 30).StatusCode
  } catch { $code = 0 }
  Test-Eq 'mirror serves the supervisor in the layout cmd/opampsupervisor/v<version>/' 200 $code
}

function Complete-Scenario {
  if (-not $script:Active) { return }
  $script:Active = $false
  Stop-Fault
  if ($script:Failed -gt 0 -or $script:Aborted) {
    try { Save-Diagnostics } catch { Write-HeWarn "diagnostics failed: $($_.Exception.Message)" }
    try { Export-Artifacts } catch { Write-HeWarn "artifacts failed: $($_.Exception.Message)" }
  }
  try { Stop-Tracked } catch { Write-HeWarn "stopping the processes failed: $($_.Exception.Message)" }
  try {
    if (@(Get-HostLeftovers).Count -gt 0) { Reset-Host -Quiet }
  } catch { Write-HeWarn "reset failed: $($_.Exception.Message)" }
  if (-not (Remove-CaTrust)) { Write-HeWarn 'the throwaway CA is still in the machine store' }
  try { Export-Artifacts } catch { Write-HeWarn "artifacts failed: $($_.Exception.Message)" }
  Write-Summary
}

function Invoke-Scenario {
  param([string]$Name, [scriptblock]$Body)
  Initialize-Scenario -Name $Name
  Write-HeSection "scenario $Name"
  Assert-CiHost
  Assert-FreshHost
  $script:Active = $true
  $script:Aborted = $false
  try {
    Initialize-Shared
    $null = & $Body
  } catch {
    $script:Aborted = $true
    Add-Fail "scenario $Name" ("stopped: " + $_.Exception.Message)
  } finally {
    Complete-Scenario
  }
}

# ---- scenario "one-command" ----------------------------------------------------------------------------------------------
function Invoke-UpdateWithRollback { # fleet collector-agent-id updater-agent-id old-version new-version n1 n2
  param([string]$Fleet, [string]$Col, [string]$Upd, [string]$VOld, [string]$VNew, [int]$N1, [int]$N2)
  $versions = Join-Path $script:CollectorDir 'versions'
  $bin = Join-Path $script:CollectorDir 'bin'
  Write-HeSection "$N1. update to $VNew as the UI starts it: the new collector dies during the health check, the updater rolls back"
  if (-not (Start-Fault -Version $VNew)) { Write-HeWarn 'the fault injector did not start' }
  $r1 = Start-VersionSelection -Fleet $Fleet -Version $VNew -Reason 'host-e2e failure case' -Expected $VOld
  if (-not $r1) { Add-Fail 'select-version started a rollout' $script:LastApiError; return $false }
  Test-Eq 'rollout origin' 'ui' (Format-Value (Get-Field (Get-ApiJson -Path "/rollouts/$r1") 'origin'))
  $fl = Get-ApiJson -Path "/fleets/$Fleet"
  Test-Eq 'target of the fleet is set by the UI' "ui/$VNew" ("{0}/{1}" -f (Get-Field $fl 'status.versionTarget.source'), (Get-Field $fl 'status.versionTarget.version'))
  [void](Wait-For 300 { Test-RolloutFinal -Id $r1 })
  if (Wait-For 200 { Test-UpdateEnded -Id $Upd }) {
    Add-Pass 'the updater ended the attempt'
  } else {
    Add-Fail 'the updater ended the attempt' 'no rolled_back or failed status within 200 s'
  }
  Stop-Fault
  [void](Wait-For 60 { Test-AgentIs -Id $Col -Path 'agent.version' -Wanted $VOld })
  [void](Wait-For 30 { Test-CollectorHealthEndpoint })
  # The checks below read what the server derived from the last reports of the updater and the collector (see Wait-Settled).
  [void](Wait-Settled 'the engine derived the phase of the target' 30 'rolled_back' { Get-TargetPhase -Id $r1 })
  [void](Wait-Settled 'package status of the updater reports the failed install' 30 'install_failed' { Get-PackageStatus -Id $Upd })
  Test-Eq 'rollout paused after the failed target (policy onFailure: pause)' 'paused' (Format-Value (Get-Field (Get-ApiJson -Path "/rollouts/$r1") 'status'))
  Test-Eq "collector reports $VOld again" $VOld (Get-AgentValue -Id $Col -Path 'agent.version')
  Test-Eq "current-version is $VOld again" $VOld (([IO.File]::ReadAllText((Join-Path $bin 'current-version'))).Trim())
  Test-Eq "the active copy is the binary of versions\$VOld again" (Get-Sha256Hex -Path ([IO.Path]::Combine($versions, $VOld, 'otelcol-contrib.exe'))) (Get-Sha256Hex -Path (Join-Path $bin 'otelcol-contrib.exe'))
  $steps = Get-RolloutSteps -Id $r1
  Test-Contains 'step rolling_back is done' 'rolling_back:done' $steps
  Test-Contains 'step health_check failed' 'health_check:failed' $steps
  $targets = Get-ApiJson -Path "/rollouts/$r1/targets"
  Test-Eq 'target status' 'failed' (Format-Value (Get-Field (@($targets.items)[0]) 'status'))
  Test-Eq 'the rolled back version is removed from versions\' 'no' $(if (Test-Path -LiteralPath (Join-Path $versions $VNew)) { 'yes' } else { 'no' })
  $kills = 0
  if (Test-Path -LiteralPath (Join-Path $script:Dir 'fault.log')) { $kills = @(Select-String -LiteralPath (Join-Path $script:Dir 'fault.log') -Pattern '^killed otelcol-contrib' -ErrorAction SilentlyContinue).Count }
  Test-Ge 'the fault injector killed the new collector at least once' 1 $kills
  Test-Eq 'otelcol-supervisor is running again' 'Running' (Get-ServiceStatus 'otelcol-supervisor')

  Write-HeSection "$N2. retry-failed: the update succeeds"
  Write-HeLog 'waiting 20 s (failedRetryBackoff 15 s) before retry-failed'
  Start-Sleep -Seconds 20
  [void](Wait-For 120 { Test-AgentIs -Id $Col -Path 'agent.health.healthy' -Wanted 'true' })
  [void](Send-Api -Method 'POST' -Path "/rollouts/$r1/actions/retry-failed" -Body @{ reason = 'host-e2e retry' })
  [void](Wait-For 420 { Test-RolloutFinal -Id $r1 })
  Test-Eq 'rollout completed' 'completed' (Format-Value (Get-Field (Get-ApiJson -Path "/rollouts/$r1") 'status'))
  $steps = Get-RolloutSteps -Id $r1
  foreach ($s in @('downloading', 'verifying', 'installing', 'restarting', 'health_check')) { Test-Contains "step $s is done" "${s}:done" $steps }
  Test-NotContains 'no rolling_back step in the successful attempt' 'rolling_back' $steps
  [void](Wait-For 60 { Test-AgentIs -Id $Col -Path 'agent.version' -Wanted $VNew })
  Test-Eq "collector reports $VNew" $VNew (Get-AgentValue -Id $Col -Path 'agent.version')
  Test-Eq "current-version is $VNew" $VNew (([IO.File]::ReadAllText((Join-Path $bin 'current-version'))).Trim())
  Test-Eq "the active copy is the binary of versions\$VNew" (Get-Sha256Hex -Path ([IO.Path]::Combine($versions, $VNew, 'otelcol-contrib.exe'))) (Get-Sha256Hex -Path (Join-Path $bin 'otelcol-contrib.exe'))
  $running = Get-CollectorProcess
  $runPath = ''
  if ($running) { try { $runPath = $running.Path } catch { $runPath = '' } }
  Test-Eq 'the running collector is the active copy' (Join-Path $bin 'otelcol-contrib.exe').ToLowerInvariant() $runPath.ToLowerInvariant()
  [void](Wait-For 60 { Test-AgentIs -Id $Col -Path 'agent.health.healthy' -Wanted 'true' })
  Test-Eq 'collector healthy' 'true' (Get-AgentValue -Id $Col -Path 'agent.health.healthy')
  Test-Eq 'package status installed' 'installed' (Get-PackageStatus -Id $Upd)
  Test-Eq 'the old version is kept for the step back (keepVersions 2)' 'yes' $(if (Test-Path -LiteralPath (Join-Path $versions $VOld)) { 'yes' } else { 'no' })
  $fl = Get-ApiJson -Path "/fleets/$Fleet"
  Test-Eq 'selection recorded: who and why' 'admin/host-e2e failure case' ("{0}/{1}" -f (Get-Field $fl 'status.versionTarget.selection.selectedBy'), (Get-Field $fl 'status.versionTarget.selection.reason'))
  $audit = Get-ApiJson -Path "/audit?action=fleet.version.select&targetType=fleet&targetId=$Fleet"
  Test-Ge 'audit: fleet.version.select' 1 $(if ($audit -and $audit.items) { @($audit.items).Count } else { 0 })
  return $true
}

function Invoke-OneCommand {
  $fleet = 'windows-hosts'
  $siteId = 'host-e2e-win-01'
  $vOld = $script:VersionOld
  $vNew = $script:VersionNew
  $tokenFile = Join-Path $script:Dir 'enrollment-token'
  $cmdFile = Join-Path $script:Dir 'command.ps1'
  $ilog = Join-Path $script:Dir 'install-command.log'
  $blueprint = [IO.Path]::Combine($script:Dir, 'gitops', 'blueprints', 'host-e2e.yaml')
  $supKey = Join-Path $script:ServiceKeyRoot 'otelcol-supervisor'

  # ---- 1. the bundle
  $releaseNote = $(if ($script:ReleaseTag) { " of the release $($script:ReleaseTag)" } else { '' })
  Write-HeSection "1. the installer bundle$releaseNote"
  if (Initialize-Bundle) {
    $published = $(if ($script:ReleaseTag) { ", published with $($script:ReleaseTag), checksums.txt verified keyless" } else { '' })
    Add-Pass 'installer bundle ready' "$($script:BundleFile)$published"
  } else {
    Add-Fail 'installer bundle ready' $(if ($script:ReleaseTag) { "release $($script:ReleaseTag): see the log above" } else { 'see the log above' })
    return
  }

  # ---- 2. the server
  Write-HeSection '2. myriarchd in dev mode over https, serving the bundle from a directory'
  if (-not (Start-Server)) { return }
  if (-not (Connect-Admin)) { Add-Fail 'dev login as admin'; return }
  [void](Wait-For 60 { Test-GitopsOk })
  $gs = Get-ApiJson -Path '/gitops/status'
  Test-Eq 'GitOps status' 'ok/0' ("{0}/{1}" -f (Get-Field $gs 'state'), $(if ($gs -and $gs.issues) { @($gs.issues).Count } else { 0 }))
  $fleets = Get-ApiJson -Path '/fleets'
  Test-Eq "fleet $fleet loaded" $fleet ((@($fleets.items) | ForEach-Object { $_.fleet.name }) -join ',')
  $target = Format-Value (Get-Field (Get-ApiJson -Path "/fleets/$fleet") 'status.versionTarget.version')
  Test-Eq 'target version of the fleet' $vOld $target
  $bl = Get-ApiJson -Path '/install/bundles'
  $item = @($bl.items) | Where-Object { $_.filename -ceq $script:BundleFile } | Select-Object -First 1
  Test-Eq 'the server serves the bundle: source/status/file/SHA-256' ("directory/ready/{0}/{1}" -f $script:BundleFile, $script:BundleSha) `
    ("{0}/{1}/{2}/{3}" -f $bl.source, $item.status, $item.filename, $item.sha256)

  # ---- 3. the token
  Write-HeSection '3. enrollment token created at run time'
  if (New-EnrollmentToken -Name 'host-e2e-one-command' -Path $tokenFile) {
    Add-Pass 'enrollment token created through the API' 'secret only in a file, masked in the log, not printed'
  } else {
    Add-Fail 'enrollment token created through the API' $script:LastApiError
    return
  }
  $token = ([IO.File]::ReadAllText($tokenFile)).Trim()

  # ---- 4. the command
  Write-HeSection '4. the command, rendered through POST /api/v1/install/commands'
  if (New-InstallCommand -Fleet $fleet -SiteId $siteId -TokenFile $tokenFile -OutFile $cmdFile) {
    Add-Pass 'command rendered for windows/amd64' "variant token_inline, fleet $fleet, site edge/$siteId"
  } else {
    $resp = Join-Path $script:Dir 'commands-response.json'
    $head = $(if (Test-Path -LiteralPath $resp) { (Get-Content -LiteralPath $resp -TotalCount 1) } else { '' })
    Add-Fail 'command rendered for windows/amd64' ([string]$head).Substring(0, [Math]::Min(300, ([string]$head).Length))
    return
  }
  $respText = [IO.File]::ReadAllText((Join-Path $script:Dir 'commands-response.json'))
  if ($respText.Contains($token)) { Add-Fail 'the response of the server holds no token' 'the token is in the response' } else { Add-Pass 'the response of the server holds no token' }
  $rendered = ConvertFrom-ApiJson -Text $respText
  $shown = [string]@($rendered.commands)[0].command
  Test-Contains 'the command pins the SHA-256 of the bundle' $script:BundleSha $shown
  Test-Contains 'the command names the bundle' $script:BundleFile $shown

  # ---- 5. the command, unchanged
  Write-HeSection "5. the command, unchanged, in an administrator session of PowerShell $($PSVersionTable.PSVersion)"
  $code = Invoke-InstallCommand -CommandFile $cmdFile -Log $ilog
  Remove-Item -LiteralPath $cmdFile -Force -ErrorAction SilentlyContinue
  $log = ''
  if (Test-Path -LiteralPath $ilog) { $log = [IO.File]::ReadAllText($ilog) }
  if ($code -eq 0) {
    Add-Pass 'the command ran to the end' (($log -split "`r?`n" | Where-Object { $_.Trim() } | Select-Object -Last 1))
  } else {
    $errs = ($log -split "`r?`n" | Where-Object { $_ -match 'ERROR|WARNING' } | Select-Object -Last 2) -join ' '
    Add-Fail 'the command ran to the end' "exit $code; $errs"
    return
  }
  if ($log.Contains($token)) { Add-Fail 'the token is not in the output of the command' 'it is' } else { Add-Pass 'the token is not in the output of the command' }
  Test-Contains 'the bundle was checked against the pinned SHA-256 before it was unpacked' 'the SHA-256 of the installer bundle is the one in the command' $log
  Test-Contains 'the plan names the fleet and the version' "plan: fleet $fleet, collector v$vOld and supervisor v$vOld (version from git), updater: true" $log
  Test-Contains 'the installer verified the collector against its Sigstore bundle' "Sigstore signature of otelcol-contrib_${vOld}_windows_amd64.tar.gz ok" $log
  Test-Contains 'the installer verified the supervisor against its Sigstore bundle' "Sigstore signature of opampsupervisor_${vOld}_windows_amd64.exe ok" $log
  Test-Eq 'otelcol-supervisor is running' 'Running' (Get-ServiceStatus 'otelcol-supervisor')
  Test-Eq 'myriarch-updater is running' 'Running' (Get-ServiceStatus 'myriarch-updater')
  Test-Eq 'both services start automatically' 'AUTO_START/AUTO_START' ((((Get-ServiceConfig 'otelcol-supervisor' 'START_TYPE') -split '\s+')[1]) + '/' + (((Get-ServiceConfig 'myriarch-updater' 'START_TYPE') -split '\s+')[1]))
  Test-Eq 'the supervisor runs as its virtual service account, not as LocalSystem' 'NT SERVICE\otelcol-supervisor' (Get-ServiceConfig 'otelcol-supervisor' 'SERVICE_START_NAME')
  Test-Eq 'the updater runs as LocalSystem' 'LocalSystem' (Get-ServiceConfig 'myriarch-updater' 'SERVICE_START_NAME')
  Test-Eq 'current-version is the target version of the fleet' $vOld (([IO.File]::ReadAllText([IO.Path]::Combine($script:CollectorDir, 'bin', 'current-version'))).Trim())
  Test-Eq 'the supervisor version file' $vOld (([IO.File]::ReadAllText([IO.Path]::Combine($script:InstallRoot, 'supervisor', 'VERSION'))).Trim())
  $envValue = @((Get-ItemProperty -Path $supKey -Name Environment -ErrorAction SilentlyContinue).Environment)
  Test-Eq 'the token is in the service environment (registry value; never printed)' 1 @($envValue | Where-Object { $_ -like 'MYRIARCH_ENROLLMENT_TOKEN=*' }).Count
  Test-Eq 'the registry key of the supervisor grants SYSTEM and the administrators only' 'S-1-5-18,S-1-5-32-544' ((Get-AllowSids -Path $supKey) -join ',')
  $broad = @('S-1-1-0', 'S-1-5-11', 'S-1-5-32-545')
  $configSids = @(Get-AllowSids -Path $script:ConfigDir)
  $dataSids = @(Get-AllowSids -Path $script:DataRoot)
  Test-Eq 'no everyone, authenticated users or users entry on the configuration and data directories' '' ((@($configSids + $dataSids) | Where-Object { $broad -contains $_ }) -join ',')
  Test-Eq 'the token file of the host is not readable by users' '' ((@(Get-AllowSids -Path (Join-Path $script:ConfigDir 'enrollment-token')) | Where-Object { $broad -contains $_ }) -join ',')
  $setup = @(Get-ChildItem -LiteralPath $script:ProgramFiles -Force -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'myriarch-setup*' })
  Test-Eq 'the working directory of the command below Program Files is gone' 0 $setup.Count
  [void](Wait-For 60 { [bool](Get-CollectorProcess) })
  Test-True 'the collector runs' { [bool](Get-CollectorProcess) }

  # ---- 6. the host registers
  Write-HeSection '6. the host registers: a healthy collector in the expected fleet at its target version'
  [void](Wait-For 240 { Test-CollectorRegistered -Site $siteId })
  $col = Get-AgentId -Key 'site.id' -Value $siteId
  if (-not $col) { Add-Fail 'collector registered' "no agent with site.id=$siteId"; return }
  [void](Wait-For 240 { Test-AgentIs -Id $col -Path 'configSync' -Wanted 'in_sync' })
  [void](Wait-For 120 { Test-AgentIs -Id $col -Path 'agent.health.healthy' -Wanted 'true' })
  $target = Format-Value (Get-Field (Get-ApiJson -Path "/fleets/$fleet") 'status.versionTarget.version')
  $a = Get-ApiJson -Path "/agents/$col"
  Test-Eq 'collector registered: fleet / version / configuration / health' "$fleet/$target/in_sync/true" `
    ("{0}/{1}/{2}/{3}" -f (Format-Value (Get-Field $a 'agent.assignment.fleetName')), (Format-Value (Get-Field $a 'agent.version')), (Format-Value (Get-Field $a 'configSync')), (Format-Value (Get-Field $a 'agent.health.healthy')))
  Test-True 'health endpoint of the collector' { Test-CollectorHealthEndpoint }
  [void](Wait-For 120 { Test-UpdaterLinked -Collector $col })
  $upd = Get-UpdaterId -Collector $col
  if (-not $upd) { Add-Fail 'myriarch-updater linked to the collector' 'no updater agent'; return }
  Add-Pass 'myriarch-updater linked to the collector' $upd

  # The templates of the bundle are the ones of the product (health timeout 2 minutes, a failed version retried after 10 minutes). An
  # operator may shorten the waits in the updater.yaml of the configuration directory, and the test does so, so that the rollback and
  # the retry take minutes.
  if (Set-UpdaterTuning) { Add-Pass 'waits of the installed updater shortened for the test (updater.yaml)' } else { Add-Fail 'waits of the installed updater shortened for the test'; return }
  [void](Wait-For 120 { Test-AgentIs -Id $upd -Path 'agent.connection.status' -Wanted 'online' })
  Test-Eq 'myriarch-updater is running after the restart' 'Running' (Get-ServiceStatus 'myriarch-updater')

  # ---- 7. a configuration rollout
  Write-HeSection '7. a configuration rollout: a changed blueprint in Git reaches the collector'
  $before = Get-ConfigRolloutCount -Fleet $fleet
  $bpText = [IO.File]::ReadAllText($blueprint)
  Write-Utf8 -Path $blueprint -Text ($bpText.Replace('timeout: 5s', 'timeout: 7s'))
  Test-Eq 'the blueprint of the GitOps fixture was changed' 1 ([regex]::Matches([IO.File]::ReadAllText($blueprint), 'timeout: 7s').Count)
  [void](Send-Api -Method 'POST' -Path '/gitops/actions/sync')
  [void](Wait-For 90 { (Get-ConfigRolloutCount -Fleet $fleet) -gt $before })
  $rollouts = Get-ApiJson -Path "/rollouts?kind=config&fleet=$fleet&limit=50"
  $r2 = ''
  if ($rollouts -and $rollouts.items) { $r2 = [string](@($rollouts.items) | Sort-Object { $_.createdAt } | Select-Object -Last 1).id }
  if (-not $r2) { Add-Fail 'a configuration rollout started' 'none after the sync'; return }
  [void](Wait-For 180 { Test-RolloutFinal -Id $r2 })
  Test-Eq 'configuration rollout completed' 'completed' (Format-Value (Get-Field (Get-ApiJson -Path "/rollouts/$r2") 'status'))
  [void](Wait-For 90 { Test-AgentIs -Id $col -Path 'configSync' -Wanted 'in_sync' })
  Test-Eq 'collector in sync again' 'in_sync' (Get-AgentValue -Id $col -Path 'configSync')
  [void](Wait-For 90 { Test-EffectiveConfigHas -Id $col -Text 'timeout: 7s' })
  Test-True 'the collector reports the changed configuration' { Test-EffectiveConfigHas -Id $col -Text 'timeout: 7s' }
  [void](Wait-For 60 { Test-AgentIs -Id $col -Path 'agent.health.healthy' -Wanted 'true' })
  Test-Eq 'collector healthy after the rollout' 'true' (Get-AgentValue -Id $col -Path 'agent.health.healthy')

  # ---- 8. the update and the rollback
  if (-not (Invoke-UpdateWithRollback -Fleet $fleet -Col $col -Upd $upd -VOld $vOld -VNew $vNew -N1 8 -N2 9)) { return }

  # ---- 10. uninstall
  Write-HeSection '10. uninstall -Purge, and nothing is left'
  Reset-Host
  Test-Eq 'no registry key of the services is left' 0 @($script:Services | Where-Object { Test-Path -LiteralPath (Join-Path $script:ServiceKeyRoot $_) }).Count
  Test-Eq 'no working directory of the command is left below Program Files' 0 @(Get-ChildItem -LiteralPath $script:ProgramFiles -Force -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'myriarch-setup*' }).Count
  Test-Eq 'the rendered command is gone' 'no' $(if (Test-Path -LiteralPath $cmdFile) { 'yes' } else { 'no' })
}

# ---- main ----------------------------------------------------------------------------------------------------------------
function Show-Usage {
  Write-Host 'usage: scripts/host-e2e/windows.ps1 <scenario>'
  Write-Host ("scenarios: " + ($script:Scenarios -join ' '))
  Write-Host 'Runs on a throwaway CI runner only (CI=true, HOST_E2E=1); see docs/development.md, "Host end-to-end test".'
}

function Invoke-Main {
  if (-not $Scenario) { Show-Usage; exit 2 }
  if ($Scenario -in @('-h', '--help', 'help')) { Show-Usage; exit 0 }
  if ($script:Scenarios -notcontains $Scenario) { Show-Usage; Stop-He "unknown scenario '$Scenario'" }
  Assert-CiHost
  switch ($Scenario) {
    'one-command' { Invoke-Scenario -Name 'one-command' -Body { Invoke-OneCommand } }
  }
  # the counters decide, never what a function happened to print
  if ($script:Failed -eq 0 -and -not $script:Aborted) { exit 0 }
  exit 1
}

# Do not run when the script is dot-sourced (tests of the helper functions).
if ($MyInvocation.InvocationName -ne '.') { Invoke-Main }
