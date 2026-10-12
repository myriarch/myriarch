# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

#Requires -Version 7.0

<#
.SYNOPSIS
  Smoke tests of the Windows files of a Myriarch release (ADR-0035, ADR-0034). Counterpart of ci/release/smoke.sh.

.DESCRIPTION
  -Mode Assets  (job verify-windows, BEFORE the release is published)
    -AssetsDir DIR   the release files (assets/ of release-assets.tar)
    -Version X.Y.Z   the release version (without "v")
    -Signer FILE     signer.json of the signing hook (windows-signed.tar)
    For amd64 and arm64: myriarch-updater_<v>_windows_<arch>.exe, myrctl.exe inside myrctl_<v>_windows_<arch>.zip and
    myriarch-updater.exe inside the updater zip carry the version resources (ProductName "Myriarch", ProductVersion and
    FileVersion = the version, OriginalFilename <program>.exe); the bare updater equals the member of its zip; the updater zips
    hold deploy/scripts/install-windows.ps1 and deploy/windows/Set-ServiceEnvironment.ps1; every executable and both scripts
    equal the hashes of signer.json; Get-AuthenticodeSignature says NotSigned when signer.json says "signed": false, otherwise
    Valid with a timestamp and the signer subject of signer.json. The amd64 executables run ("version" prints the version); the
    arm64 ones are checked statically only.

  -Mode Install  (job smoke-windows, AFTER publication)
    -Tag vX.Y.Z -Version X.Y.Z -Cosign FILE -WindowsSigning off|signpath|kms [-Commit SHA]
    Downloads checksums.txt, its bundle, myriarch-updater_<v>_windows_amd64.zip and the bare .exe without credentials (a transient
    failure is retried with pauses of 1, 2, 4, ... s for up to 120 s, any other failure ends the script at once), copies
    cosign.exe to a directory that only administrators can write, verifies checksums.txt with the identity of the tag (with -Commit
    also the commit), checks the two files with Get-FileHash against checksums.txt, expands the zip, runs Unblock-File (only after
    those checks) and runs the install-windows.ps1 of the zip in staging mode (-Root) with the keyless verification of the updater
    and the real upstream collector. The staged updater must equal the downloaded .exe. With -WindowsSigning off every executable
    and both scripts must report NotSigned, with a provider mode Valid (the subject check against deploy/release/authenticode/
    arrives with the provider pull request).

  Errors: one line "[smoke-windows] ERROR [<code>] <message>" on stderr, exit 1. Usage errors "[smoke-windows] usage: ...", exit 2.
  The tests of the parameter handling run on every platform (ci/tests/release-smoke-test.sh); the rest needs Windows.
#>
[CmdletBinding()]
param(
  [string]$Mode,
  [string]$AssetsDir,
  [string]$Version,
  [string]$Signer,
  [string]$Tag,
  [string]$Cosign,
  [string]$WindowsSigning,
  [string]$Commit = ''
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Write-Log { param([string]$Message) [Console]::Error.WriteLine("[smoke-windows] $Message") }
function Stop-Smoke { param([string]$Code, [string]$Message) [Console]::Error.WriteLine("[smoke-windows] ERROR [$Code] $Message"); exit 1 }
function Stop-Usage { param([string]$Message) [Console]::Error.WriteLine("[smoke-windows] usage: $Message"); exit 2 }

$usageAssets = 'smoke-windows.ps1 -Mode Assets -AssetsDir DIR -Version X.Y.Z -Signer FILE'
$usageInstall = 'smoke-windows.ps1 -Mode Install -Tag vX.Y.Z -Version X.Y.Z -Cosign FILE -WindowsSigning off|signpath|kms [-Commit SHA]'
$versionRe = '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'

# ---- parameters ----------------------------------------------------------------------------------------------------------
# Every value is matched as a whole string, and a control character is refused first.
function Test-Value {
  param([string]$Name, [string]$Value, [string]$Pattern)
  if ($Value -match '[\x00-\x1f\x7f]') { Stop-Usage "$Name contains a control character" }
  if ($Value -cnotmatch $Pattern) { Stop-Usage "$Name is not valid: '$Value'" }
}

switch -CaseSensitive ($Mode) {
  'Assets' {
    if (-not $AssetsDir -or -not $Version -or -not $Signer) { Stop-Usage $usageAssets }
    Test-Value 'Version' $Version $versionRe
  }
  'Install' {
    if (-not $Tag -or -not $Version -or -not $Cosign -or -not $WindowsSigning) { Stop-Usage $usageInstall }
    Test-Value 'Version' $Version $versionRe
    Test-Value 'Tag' $Tag '^v[0-9.]+(-rc\.[0-9]+)?$'
    if ($Tag -cne "v$Version") { Stop-Usage "Tag '$Tag' and Version '$Version' do not belong together" }
    Test-Value 'WindowsSigning' $WindowsSigning '^(off|signpath|kms)$'
    if ($Commit) { Test-Value 'Commit' $Commit '^[0-9a-f]{40}$' }
  }
  default { Stop-Usage "-Mode is Assets or Install. $usageAssets | $usageInstall" }
}

if (-not $IsWindows) { Stop-Smoke 'unsupported_platform' 'the Windows smoke tests run on Windows only (Authenticode, version resources, Program Files)' }

# ---- shared helpers ------------------------------------------------------------------------------------------------------
function Get-EnvValue {
  param([string]$File, [string]$Key)
  if (-not (Test-Path -LiteralPath $File)) { Stop-Smoke 'file_missing' "$File not found" }
  $line = Select-String -LiteralPath $File -Pattern "^$([regex]::Escape($Key))=" -List | Select-Object -First 1
  if (-not $line) { return '' }
  return $line.Line.Substring($Key.Length + 1)
}

function Get-Sha256 {
  param([string]$Path)
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Get-FirstWord {
  param([string]$Text)
  $t = ($Text -split "`r?`n" | Select-Object -First 1)
  if (-not $t) { return '' }
  return ($t.Trim() -split '\s+')[0]
}

# Runs a native program and returns its output (stdout and stderr) and exit code.
function Invoke-Program {
  param([string]$File, [string[]]$Arguments = @())
  $text = (& $File @Arguments 2>&1 | Out-String)
  return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Output = $text.Trim() }
}

# The version of a program is its result, so it must be on stdout (ci/release/smoke.sh enforces the same for the images, see "CLI output"
# in docs/development.md). stderr is read apart from it: a program that prints the version on the wrong stream fails here as well,
# which the merged output of Invoke-Program could not detect (myriarch-updater.exe did so in v0.1.0-rc.2).
function Assert-Version {
  param([string]$Exe, [string]$Label)
  $errFile = [IO.Path]::GetTempFileName()
  try {
    $stdout = (& $Exe version 2>$errFile | Out-String).Trim()
    $code = $LASTEXITCODE
    $stderr = ''
    $raw = Get-Content -LiteralPath $errFile -Raw
    if ($raw) { $stderr = $raw.Trim() }
  } finally {
    Remove-Item -LiteralPath $errFile -Force -ErrorAction SilentlyContinue
  }
  $firstErr = ($stderr -split "`r?`n" | Select-Object -First 1)
  if ($code -ne 0) { Stop-Smoke 'program_failed' "$Label does not run (exit code ${code}): $stdout $stderr" }
  if ((Get-FirstWord $stdout) -cne $Version) {
    Stop-Smoke 'program_version' "$Label reports '$(Get-FirstWord $stdout)' on stdout, expected $Version (stderr: '$firstErr')"
  }
  Write-Log "$Label : version $Version"
}

# Authenticode state of a file: NotSigned when the release is unsigned, otherwise Valid with a timestamp (and the expected subject).
function Assert-Authenticode {
  param([string]$Path, [bool]$Signed, [string]$Subject, [string]$Label)
  $sig = Get-AuthenticodeSignature -LiteralPath $Path
  if (-not $Signed) {
    if ($sig.Status.ToString() -ne 'NotSigned') { Stop-Smoke 'authenticode_state' "$Label has the Authenticode state $($sig.Status), expected NotSigned (the release is unsigned)" }
    return
  }
  if ($sig.Status.ToString() -ne 'Valid') { Stop-Smoke 'authenticode_state' "$Label has the Authenticode state $($sig.Status), expected Valid" }
  if (-not $sig.TimeStamperCertificate) { Stop-Smoke 'authenticode_timestamp' "$Label has no RFC 3161 timestamp" }
  if ($Subject) {
    $got = ($sig.SignerCertificate.Subject -replace '\s*,\s*', ',').ToLowerInvariant()
    $want = ($Subject -replace '\s*,\s*', ',').ToLowerInvariant()
    if ($got -ne $want) { Stop-Smoke 'authenticode_subject' "$Label is signed by '$($sig.SignerCertificate.Subject)', signer.json says '$Subject'" }
  }
}

# Expands a zip into a new directory after checking that no entry leaves it.
function Expand-SafeZip {
  param([string]$Zip, [string]$Destination)
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $archive = [IO.Compression.ZipFile]::OpenRead($Zip)
  try {
    foreach ($entry in $archive.Entries) {
      $n = $entry.FullName
      if ($n.StartsWith('/') -or $n.Contains('\') -or ($n -split '/') -contains '..' -or $n -match '^[A-Za-z]:') {
        Stop-Smoke 'archive_invalid' "$(Split-Path -Leaf $Zip) has the unsafe entry '$n'"
      }
    }
  } finally {
    $archive.Dispose()
  }
  New-Item -ItemType Directory -Path $Destination -Force | Out-Null
  Expand-Archive -LiteralPath $Zip -DestinationPath $Destination -Force
}

# ---- Mode Assets ---------------------------------------------------------------------------------------------------------
function Invoke-Assets {
  if (-not (Test-Path -LiteralPath $AssetsDir -PathType Container)) { Stop-Smoke 'asset_missing' "AssetsDir $AssetsDir is not a directory" }
  if (-not (Test-Path -LiteralPath $Signer -PathType Leaf)) { Stop-Smoke 'signer_json' "signer.json $Signer not found" }
  $signerDoc = Get-Content -LiteralPath $Signer -Raw | ConvertFrom-Json
  if ($signerDoc.schema -ne 1 -or $null -eq $signerDoc.signed -or -not $signerDoc.files) { Stop-Smoke 'signer_json' 'signer.json is not a schema 1 document' }
  $signed = [bool]$signerDoc.signed
  $subject = ''
  if ($signed) {
    $subject = [string]$signerDoc.subject
    if (-not $subject) { Stop-Smoke 'signer_json' 'signer.json says signed but names no subject' }
  }
  $work = Join-Path ([IO.Path]::GetTempPath()) ('myriarch-smoke-' + [guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $work | Out-Null
  try {
    foreach ($arch in @('amd64', 'arm64')) {
      $bareName = "myriarch-updater_${Version}_windows_${arch}.exe"
      $updaterZip = Join-Path $AssetsDir "myriarch-updater_${Version}_windows_${arch}.zip"
      $ctlZip = Join-Path $AssetsDir "myrctl_${Version}_windows_${arch}.zip"
      $bare = Join-Path $AssetsDir $bareName
      foreach ($f in @($bare, $updaterZip, $ctlZip)) {
        if (-not (Test-Path -LiteralPath $f -PathType Leaf)) { Stop-Smoke 'asset_missing' "$(Split-Path -Leaf $f) not found in $AssetsDir" }
      }
      $up = Join-Path $work "updater-$arch"
      $ctl = Join-Path $work "myrctl-$arch"
      Expand-SafeZip -Zip $updaterZip -Destination $up
      Expand-SafeZip -Zip $ctlZip -Destination $ctl
      $zipUpdater = Join-Path $up 'myriarch-updater.exe'
      $zipCtl = Join-Path $ctl 'myrctl.exe'
      $install = Join-Path $up 'deploy/scripts/install-windows.ps1'
      $service = Join-Path $up 'deploy/windows/Set-ServiceEnvironment.ps1'
      foreach ($f in @($zipUpdater, $zipCtl, $install, $service)) {
        if (-not (Test-Path -LiteralPath $f -PathType Leaf)) { Stop-Smoke 'zip_contents' "windows/$arch : $f is missing from its zip" }
      }
      # the bare updater is the member of its zip
      if ((Get-Sha256 $bare) -ne (Get-Sha256 $zipUpdater)) { Stop-Smoke 'zip_contents' "$bareName differs from myriarch-updater.exe in its zip" }

      # the files are the ones the signing hook reported
      $expect = @{
        "myriarch-updater_windows_$arch.exe" = $zipUpdater
        "myrctl_windows_$arch.exe"           = $zipCtl
      }
      if ($arch -eq 'amd64') {
        $expect['install-windows.ps1'] = $install
        $expect['Set-ServiceEnvironment.ps1'] = $service
      }
      foreach ($name in $expect.Keys) {
        $entry = $signerDoc.files.PSObject.Properties[$name]
        if (-not $entry) { Stop-Smoke 'signer_json' "signer.json has no entry for $name" }
        if ((Get-Sha256 $expect[$name]) -ne [string]$entry.Value.sha256) { Stop-Smoke 'signer_json' "$name in the zip is not the file that signer.json reports" }
      }
      # the scripts are the same in the other zip
      if ($arch -eq 'arm64') {
        foreach ($pair in @(@('install-windows.ps1', $install), @('Set-ServiceEnvironment.ps1', $service))) {
          $entry = $signerDoc.files.PSObject.Properties[$pair[0]]
          if ((Get-Sha256 $pair[1]) -ne [string]$entry.Value.sha256) { Stop-Smoke 'signer_json' "$($pair[0]) in the arm64 zip is not the file that signer.json reports" }
        }
      }

      # version resources
      foreach ($item in @(@($bare, 'myriarch-updater'), @($zipUpdater, 'myriarch-updater'), @($zipCtl, 'myrctl'))) {
        $vi = (Get-Item -LiteralPath $item[0]).VersionInfo
        $label = "windows/$arch $(Split-Path -Leaf $item[0])"
        if ($vi.ProductName -cne 'Myriarch') { Stop-Smoke 'version_info' "$label : ProductName is '$($vi.ProductName)', expected Myriarch" }
        if ($vi.ProductVersion -cne $Version) { Stop-Smoke 'version_info' "$label : ProductVersion is '$($vi.ProductVersion)', expected $Version" }
        if ($vi.FileVersion -cne $Version) { Stop-Smoke 'version_info' "$label : FileVersion is '$($vi.FileVersion)', expected $Version" }
        if ($vi.OriginalFilename -cne "$($item[1]).exe") { Stop-Smoke 'version_info' "$label : OriginalFilename is '$($vi.OriginalFilename)', expected $($item[1]).exe" }
      }

      # Authenticode
      foreach ($item in @(@($bare, $bareName), @($zipUpdater, 'myriarch-updater.exe (zip)'), @($zipCtl, 'myrctl.exe (zip)'), @($install, 'install-windows.ps1'), @($service, 'Set-ServiceEnvironment.ps1'))) {
        Assert-Authenticode -Path $item[0] -Signed $signed -Subject $subject -Label "windows/$arch $($item[1])"
      }

      # amd64 executables run, arm64 ones are checked statically only
      if ($arch -eq 'amd64') {
        Assert-Version -Exe $zipUpdater -Label 'windows/amd64 myriarch-updater.exe'
        Assert-Version -Exe $zipCtl -Label 'windows/amd64 myrctl.exe'
      }
      Write-Log "windows/$arch : version resources, zip contents, hashes of signer.json and Authenticode state ok"
    }
    Write-Log "assets: ok ($(if ($signed) { 'signed by ' + $subject } else { 'unsigned' }))"
  } finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
  }
}

# ---- Mode Install --------------------------------------------------------------------------------------------------------
function Get-ReleaseUrl {
  param([string]$Name)
  return "https://github.com/$script:Repository/releases/download/$Tag/$Name"
}

# ---- Download retry policy (the same in install-windows.ps1, install-linux.sh and ci/release/smoke.sh) ----------------------
# Only a transient failure is retried: a timeout, a failed connection (refused, reset, name resolution) or the HTTP status 408,
# 429, 500, 502, 503 or 504. Everything else (404, 403, a TLS or certificate error, a local write error) fails at once. The pause
# starts at 1 s and doubles; no attempt starts later than 120 s after the first one. Under PowerShell 7.5 or later (the .NET 9 runtime
# reports the status of the proxy) behind an HTTPS_PROXY that refuses the CONNECT, the status of the proxy decides (503 is retried,
# 407 is not); the message says "proxy HTTP <n>". PowerShell 7.4 reports no status and retries it like a failed connection. The
# four helper functions are the same text as in deploy/scripts/install-windows.ps1 (ci/tests/download-retry-test.sh checks it).
# Tests: ci/tests/download-retry.ps1.

function Get-MonotonicSeconds {
  return [Diagnostics.Stopwatch]::GetTimestamp() / [Diagnostics.Stopwatch]::Frequency
}

function Get-DownloadStatus {
  param($Exception)
  $props = $Exception.PSObject.Properties
  if ($props['Response'] -and $Exception.Response) {
    try { return [int]$Exception.Response.StatusCode } catch { return 0 }
  }
  if ($props['StatusCode'] -and $Exception.StatusCode) {
    try { return [int]$Exception.StatusCode } catch { return 0 }
  }
  return 0
}

# The reason of a failed request for the log line and the final message: "proxy HTTP <n>" when a proxy refused the CONNECT of an
# https request (HttpRequestError ProxyTunnelError), "HTTP <n>" for any other status, else the first line of the message.
function Get-DownloadReason {
  param($Exception)
  $status = Get-DownloadStatus $Exception
  if ($status -eq 0) { return ($Exception.Message -split "`r?`n")[0] }
  if ($Exception.PSObject.Properties['HttpRequestError'] -and $Exception.HttpRequestError.ToString() -eq 'ProxyTunnelError') {
    return "proxy HTTP $status"
  }
  return "HTTP $status"
}

function Test-TransientDownloadError {
  param($Exception)
  $transient = @(408, 429, 500, 502, 503, 504)
  $status = Get-DownloadStatus $Exception
  if ($status -ne 0) { return ($transient -contains $status) }
  $names = @()
  for ($e = $Exception; $e; $e = $e.InnerException) { $names += $e.GetType().FullName }
  if ($names -contains 'System.Security.Authentication.AuthenticationException') { return $false }
  if ($Exception -is [System.Net.WebException]) {
    return (@('Timeout', 'ConnectFailure', 'NameResolutionFailure', 'ProxyNameResolutionFailure', 'ConnectionClosed',
        'ReceiveFailure', 'SendFailure', 'KeepAliveFailure', 'PipelineFailure') -contains $Exception.Status.ToString())
  }
  foreach ($n in @('System.Net.Http.HttpRequestException', 'System.Net.Http.HttpIOException', 'System.Net.Sockets.SocketException',
      'System.TimeoutException', 'System.Threading.Tasks.TaskCanceledException')) {
    if ($names -contains $n) { return $true }
  }
  return $false
}

# Downloads Url to Destination with the retry policy above and returns the response. A permanent error and a used-up window end
# the script with [download_failed].
function Invoke-DownloadWithRetry {
  param([string]$Url, [string]$Destination)
  $window = 120
  $pause = 1
  $start = Get-MonotonicSeconds
  $attempt = 0
  while ($true) {
    $attempt++
    try {
      return (Invoke-WebRequest -Uri $Url -OutFile $Destination -PassThru -MaximumRedirection 5 -TimeoutSec 600)
    } catch {
      $reason = Get-DownloadReason $_.Exception
      $noun = if ($attempt -eq 1) { 'attempt' } else { 'attempts' }
      if (-not (Test-TransientDownloadError $_.Exception)) {
        Stop-Smoke 'download_failed' "download of $Url failed ($reason; not retried, $attempt $noun)"
      }
      $elapsed = [int][Math]::Floor((Get-MonotonicSeconds) - $start)
      if ($elapsed -ge $window) {
        Stop-Smoke 'download_failed' "download of $Url failed ($reason; retry window of $window s used up, $attempt $noun)"
      }
      # the last pause ends exactly at the end of the window
      if ($elapsed + $pause -gt $window) { $pause = $window - $elapsed }
      Write-Log "attempt $attempt for $Url failed ($reason), next attempt in $pause s"
      Start-Sleep -Seconds $pause
      $pause = $pause * 2
    }
  }
}

# Anonymous download over https (also after redirects), with the retry policy above.
function Get-ReleaseFile {
  param([string]$Name, [string]$Directory)
  $url = Get-ReleaseUrl $Name
  $dest = Join-Path $Directory $Name
  $r = Invoke-DownloadWithRetry -Url $url -Destination $dest
  $final = $r.BaseResponse.RequestMessage.RequestUri
  if ($final.Scheme -ne 'https') { Stop-Smoke 'download_failed' "the download of $url ended on a non-https address" }
  return $dest
}

# The SHA-256 that checksums.txt lists for a file; exactly one line must name it.
function Get-ListedHash {
  param([string]$Sums, [string]$Name)
  $lines = @(Get-Content -LiteralPath $Sums | Where-Object { $_ -cmatch ('^[0-9a-f]{64}  ' + [regex]::Escape($Name) + '$') })
  if ($lines.Count -ne 1) { Stop-Smoke 'checksum_entry' "checksums.txt must list $Name exactly once" }
  return $lines[0].Substring(0, 64)
}

function Invoke-Install {
  $ciDir = $PSScriptRoot
  $identityFile = Join-Path $ciDir 'identity.env'
  $versionsFile = Join-Path (Split-Path -Parent $ciDir) 'versions.env'
  $script:Repository = Get-EnvValue $identityFile 'RELEASE_REPOSITORY'
  $workflow = Get-EnvValue $identityFile 'RELEASE_WORKFLOW'
  $issuer = Get-EnvValue $identityFile 'RELEASE_OIDC_ISSUER'
  $trigger = Get-EnvValue $identityFile 'RELEASE_TRIGGER'
  $cosignVersion = Get-EnvValue $versionsFile 'COSIGN_VERSION'
  $collectorVersion = Get-EnvValue $versionsFile 'OTELCOL_VERSION'
  if ($script:Repository -cnotmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { Stop-Smoke 'identity_invalid' 'RELEASE_REPOSITORY of identity.env is not owner/name' }
  if ($workflow -cnotmatch '^\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml$') { Stop-Smoke 'identity_invalid' 'RELEASE_WORKFLOW of identity.env is not a workflow path' }
  if ($issuer -cnotmatch '^https://[A-Za-z0-9._:/-]+$') { Stop-Smoke 'identity_invalid' 'RELEASE_OIDC_ISSUER of identity.env is not an https URL' }
  if ($trigger -cnotmatch '^[a-z_]+$') { Stop-Smoke 'identity_invalid' 'RELEASE_TRIGGER of identity.env is not an event name' }
  if ($cosignVersion -cnotmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { Stop-Smoke 'versions_invalid' 'COSIGN_VERSION of ci/versions.env is missing or malformed' }
  if ($collectorVersion -cnotmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { Stop-Smoke 'versions_invalid' 'OTELCOL_VERSION of ci/versions.env is missing or malformed' }

  if (-not (Test-Path -LiteralPath $Cosign -PathType Leaf)) { Stop-Smoke 'tool_missing' "cosign.exe $Cosign not found" }
  $work = Join-Path ([IO.Path]::GetTempPath()) ('myriarch-smoke-' + [guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $work | Out-Null
  try {
    # cosign.exe in a directory that only administrators can write (the installer refuses a verifier anywhere else)
    $cosignDir = Join-Path $env:ProgramFiles 'cosign'
    New-Item -ItemType Directory -Path $cosignDir -Force | Out-Null
    # Make the owner and the ACL what an administrator would set up, whatever the runner image or the token of the job gives a new
    # directory (the installer's Test-AdminOnlyDirectory accepts only SYSTEM, Administrators or TrustedInstaller as owner and no other
    # account with write access). Best effort: when icacls fails, the installer decides on the state that the directory has.
    & icacls.exe $cosignDir /setowner '*S-1-5-32-544' | Out-Null
    $ownerCode = $LASTEXITCODE
    & icacls.exe $cosignDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-32-545:(OI)(CI)RX' | Out-Null
    if ($ownerCode -ne 0 -or $LASTEXITCODE -ne 0) { Write-Log "warning: icacls could not set the owner and the ACL of $cosignDir (exit codes $ownerCode and $LASTEXITCODE)" }
    $cosignExe = Join-Path $cosignDir 'cosign.exe'
    Copy-Item -LiteralPath $Cosign -Destination $cosignExe -Force
    $v = Invoke-Program -File $cosignExe -Arguments @('version')
    $gotVersion = ''
    foreach ($l in ($v.Output -split "`r?`n")) { if ($l -match '^GitVersion:\s+(\S+)') { $gotVersion = $Matches[1]; break } }
    if ($gotVersion -cne $cosignVersion) { Stop-Smoke 'tool_version' "cosign.exe must be $cosignVersion (ci/versions.env), found '$gotVersion'" }

    $dl = Join-Path $work 'download'
    New-Item -ItemType Directory -Path $dl | Out-Null
    $zipName = "myriarch-updater_${Version}_windows_amd64.zip"
    $exeName = "myriarch-updater_${Version}_windows_amd64.exe"
    foreach ($n in @('checksums.txt', 'checksums.txt.sigstore.json', $zipName, $exeName)) { Get-ReleaseFile -Name $n -Directory $dl | Out-Null }
    Write-Log "downloaded checksums.txt, its bundle, $zipName and $exeName without credentials"

    # the signature of checksums.txt: the identity of this tag (and commit)
    $sums = Join-Path $dl 'checksums.txt'
    $bundle = Join-Path $dl 'checksums.txt.sigstore.json'
    $identity = "https://github.com/$script:Repository/$workflow@refs/tags/$Tag"
    $verifyArgs = @('verify-blob', '--bundle', $bundle, '--certificate-identity', $identity, '--certificate-oidc-issuer', $issuer,
      '--certificate-github-workflow-repository', $script:Repository, '--certificate-github-workflow-ref', "refs/tags/$Tag",
      '--certificate-github-workflow-trigger', $trigger)
    if ($Commit) { $verifyArgs += @('--certificate-github-workflow-sha', $Commit) }
    $verifyArgs += $sums
    $r = Invoke-Program -File $cosignExe -Arguments $verifyArgs
    if ($r.ExitCode -ne 0) { Stop-Smoke 'signature_invalid' "cosign verify-blob of checksums.txt failed: $(($r.Output -split "`r?`n" | Select-Object -Last 1))" }
    Write-Log 'checksums.txt: signature ok'

    # the documented Get-FileHash check
    foreach ($n in @($zipName, $exeName)) {
      $want = Get-ListedHash -Sums $sums -Name $n
      $got = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $dl $n)).Hash.ToLowerInvariant()
      if ($got -ne $want) { Stop-Smoke 'checksum_mismatch' "$n does not match checksums.txt (expected $want, got $got)" }
    }
    Write-Log "$zipName and $exeName match checksums.txt"

    # only now: expand and unblock
    $pkg = Join-Path $work 'package'
    Expand-SafeZip -Zip (Join-Path $dl $zipName) -Destination $pkg
    Get-ChildItem -LiteralPath $pkg -Recurse -File | Unblock-File
    $installer = Join-Path $pkg 'deploy/scripts/install-windows.ps1'
    $service = Join-Path $pkg 'deploy/windows/Set-ServiceEnvironment.ps1'
    $zipExe = Join-Path $pkg 'myriarch-updater.exe'
    foreach ($f in @($installer, $service, $zipExe)) {
      if (-not (Test-Path -LiteralPath $f -PathType Leaf)) { Stop-Smoke 'archive_invalid' "$zipName has no $(Split-Path -Leaf $f) at the expected place" }
    }

    # staging install with keyless verification: the verifier is cosign.exe, the updater is checked through checksums.txt
    $tokenFile = Join-Path $work 'enrollment-token'
    $bytes = New-Object byte[] 24
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    Set-Content -LiteralPath $tokenFile -Value ('myre_' + (($bytes | ForEach-Object { $_.ToString('x2') }) -join '')) -NoNewline
    $root = Join-Path $work 'root'
    New-Item -ItemType Directory -Path $root | Out-Null
    $pwsh = (Get-Process -Id $PID).Path
    $installArgs = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'RemoteSigned', '-File', $installer,
      '-Root', $root, '-OpampEndpoint', 'wss://fleet.example.com/v1/opamp', '-TokenFile', $tokenFile,
      '-SiteType', 'datacenter', '-SiteId', 'smoke-01', '-CollectorVersion', $collectorVersion, '-WithUpdater',
      '-UpdaterUrl', (Get-ReleaseUrl $exeName), '-UpdaterChecksumsUrl', (Get-ReleaseUrl 'checksums.txt'), '-Verifier', $cosignExe)
    Write-Log "installing from the zip into a staging root (keyless verification, collector $collectorVersion)"
    & $pwsh @installArgs
    if ($LASTEXITCODE -ne 0) { Stop-Smoke 'install_failed' "install-windows.ps1 of $zipName failed (exit code $LASTEXITCODE)" }

    $staged = Join-Path $root 'ProgramFiles/Myriarch/updater/myriarch-updater.exe'
    if (-not (Test-Path -LiteralPath $staged -PathType Leaf)) { Stop-Smoke 'install_failed' 'the installer left no updater below ProgramFiles/Myriarch/updater' }
    if ((Get-Sha256 $staged) -ne (Get-Sha256 (Join-Path $dl $exeName))) { Stop-Smoke 'install_failed' "the staged updater is not the downloaded $exeName" }
    Assert-Version -Exe $staged -Label 'staged myriarch-updater.exe'

    # Authenticode: the state of this release
    $signed = $WindowsSigning -cne 'off'
    foreach ($item in @(@((Join-Path $dl $exeName), $exeName), @($zipExe, 'myriarch-updater.exe (zip)'), @($staged, 'staged myriarch-updater.exe'),
        @($installer, 'install-windows.ps1'), @($service, 'Set-ServiceEnvironment.ps1'))) {
      Assert-Authenticode -Path $item[0] -Signed $signed -Subject '' -Label $item[1]
    }
    Write-Log "install: ok (Authenticode: $(if ($signed) { 'Valid' } else { 'NotSigned' }), as expected for WindowsSigning=$WindowsSigning)"
  } finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
  }
}

try {
  if ($Mode -ceq 'Assets') { Invoke-Assets } else { Invoke-Install }
} catch {
  Stop-Smoke 'unexpected_error' $_.Exception.Message
}
