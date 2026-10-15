# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

#Requires -Version 5.1

<#
.SYNOPSIS
  Myriarch - installs the OpAMP supervisor + OpenTelemetry Collector Contrib (Windows).

.DESCRIPTION
  Run as Administrator (PowerShell 5.1 or newer). Installs idempotently:
    - collector (otelcol-contrib .tar.gz, SHA-256 verified, extracted with tar.exe) into
      C:\Program Files\Myriarch\collector\versions\<v>, active copy in ...\collector\bin
      (myriarch-updater swaps this copy on updates and keeps <name>.previous)
    - OpAMP supervisor (opampsupervisor.exe, SHA-256 verified) into C:\Program Files\Myriarch\supervisor
    - configuration under C:\ProgramData\Myriarch\config (SYSTEM/Administrators only)
    - service "otelcol-supervisor" (automatic, restart on failure) under the virtual service account
      "NT SERVICE\otelcol-supervisor" (not LocalSystem; access only to its config and data directories),
      token as service environment (registry, REG_MULTI_SZ "Environment") - the registry key ACL is
      restricted to SYSTEM/Administrators first, then the value is written
    - optionally the service "myriarch-updater" ("Myriarch Updater", LocalSystem: it replaces binaries)

  Trust model: downloads use https only; collector and supervisor are verified with SHA-256 and a Sigstore
  bundle (-VerifySignatures require, the default; verifier: -Verifier myriarch-updater.exe or cosign.exe, an
  already installed myriarch-updater, cosign.exe 3.0.4 or newer from a directory only administrators can write, or - with
  -UpdaterSha256 and without -UpdaterChecksumsUrl - the updater downloaded with -WithUpdater and checked by that
  hash). With -WithUpdater the myriarch-updater (the project's own binary, runs as SYSTEM) needs a trusted hash
  source, at least one of
    -UpdaterSha256 HEX                      (hash from another trusted source, for example verified on an admin host)
    -UpdaterChecksumsUrl URL                (checksums.txt of the release; its keyless Sigstore bundle, by default
                                             URL.sigstore.json or -UpdaterBundleUrl, is verified against the release
                                             identity of the requested version before the hash is read: the line
                                             whose file name matches the one in -UpdaterUrl)
  The version comes from -UpdaterVersion or from the file name myriarch-updater_<version>_windows_<arch>.exe in
  -UpdaterUrl; when both are given they must agree. The verifier of checksums.txt is never the binary that is being
  installed. Both hash sources together are allowed (then both must match); the check of checksums.txt also applies
  with -Insecure, but not with -VerifySignatures skip. Optional -UpdaterSignerThumbprint: the Authenticode signature
  of the updater must be valid and come from this certificate. A pin of a thumbprint breaks at every certificate
  renewal; it fits only a private PKI with a certificate that you control.
  Downloads that fail transiently (a timeout, a failed connection, HTTP 408, 429, 500, 502, 503 or 504) are retried with
  pauses of 1, 2, 4, ... seconds, and no attempt starts later than 2 minutes after the first one; any other failure,
  a 404 for example, ends the installation at once. Under PowerShell 7.5 or later (the .NET 9 runtime reports the status of the
  proxy) behind an HTTPS_PROXY that refuses the connection, the status of the proxy decides (proxy HTTP 503 is retried, proxy HTTP
  407 is not); PowerShell 7.4 retries a refused connection like a failed connection.
  -Insecure disables the remaining checks for lab/tests only (http/file, no bundle, ws://).
  The directories under C:\ProgramData\Myriarch are never "adopted": if an existing directory is not owned by
  SYSTEM/Administrators or is a reparse point, the installation aborts; otherwise the ACL of the whole tree is
  reset and verified.

  Do NOT use the contrib MSI: it installs a service of its own, "otelcol-contrib", which starts immediately. The supervisor
  MSI installs a service "opampsupervisor". When one of these services exists the installer stops with conflicting_install
  before it stops a service, downloads or writes anything, names the service and removes nothing.
  The supervisor storage directory (C:\ProgramData\Myriarch\supervisor) is only created empty:
  persistent_state.yaml holds the instance UID - golden images must empty it before cloning.

  Errors: every fatal error prints exactly one line to stderr and exits with status 1:
    [myriarch-install] ERROR [<code>] <message>
  Warnings: [myriarch-install] [<code>] <message> (PowerShell prefixes "WARNING: "). The codes are a stable
  interface shared with install-linux.sh; see the catalog at the top of that script and the Windows-only
  codes listed below the parameter block here.

.EXAMPLE
  # In an elevated Windows PowerShell, from the unpacked release archive that you verified (docs/tasks/connect-a-host.md has the
  # steps: $Base, $Version and $Arch are the variables of its step 2). The token file lies in a setup folder that only SYSTEM and
  # the administrators can read and is deleted after the run. The script starts in a new PowerShell process with the execution
  # policy for that process only (never Bypass, never a change of the machine), after Unblock-File on the verified copy. The
  # verifier is a cosign.exe 3.0.4 or newer in a folder that only administrators can write, or the myriarch-updater.exe of the
  # verified archive:
  Unblock-File .\deploy\scripts\install-windows.ps1
  powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\deploy\scripts\install-windows.ps1 `
      -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile 'C:\Program Files\myriarch-setup\enrollment-token.txt' `
      -SiteType datacenter -SiteId dc-01 -SiteName "Data center 1" -SiteCountry NL -SiteRegion north `
      -SiteTimezone Europe/Amsterdam -CollectorVersion 0.162.0 -Verifier 'C:\Program Files\cosign\cosign.exe' -WithUpdater `
      -UpdaterUrl "$Base/myriarch-updater_${Version}_windows_${Arch}.exe" -UpdaterChecksumsUrl "$Base/checksums.txt"
  Remove-Item 'C:\Program Files\myriarch-setup\enrollment-token.txt'
#>
[CmdletBinding()]
param(
  [string]$OpampEndpoint,
  [string]$Token,
  [string]$TokenFile,
  [string]$SiteType,
  [string]$SiteId,
  [string]$SiteName,
  [string]$SiteCountry,
  [string]$SiteRegion,
  [string]$SiteTimezone,
  [string]$Environment = 'prod',
  [string]$CollectorVersion,
  [string]$SupervisorVersion,
  [string]$ReleaseBaseUrl = 'https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download',
  [string]$UpdaterUrl,
  [string]$UpdaterSha256,
  [string]$UpdaterVersion,
  [string]$UpdaterChecksumsUrl,
  [string]$UpdaterBundleUrl,
  # removed (fail with option_removed and name the replacement)
  [string]$UpdaterChecksumsSigUrl,
  [string]$SigningKey,
  [string]$UpdaterSignerThumbprint,
  [ValidateSet('require', 'skip')]
  [string]$VerifySignatures = 'require',
  [string]$Verifier,
  [switch]$Insecure,
  [switch]$WithUpdater,
  [string]$CaFile,
  [string]$TrustedRoot,
  [string]$Arch,
  [string]$TemplatesDir,
  [string]$Root,
  [switch]$Uninstall,
  [switch]$Purge
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$script:SupervisorService = 'otelcol-supervisor'
$script:UpdaterService = 'myriarch-updater'
$script:CollectorBinary = 'otelcol-contrib.exe'
$script:StagingMode = $false
# the parameters that were passed (inside a function $PSBoundParameters is the function's own); the removed options are
# detected by their presence, not by their value
$script:BoundParameters = $PSBoundParameters
# SIDs instead of names (language independent)
$script:SidSystem = 'S-1-5-18'
$script:SidAdmins = 'S-1-5-32-544'
$script:SidTrustedInstaller = 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464'
$script:CertIssuer = 'https://token.actions.githubusercontent.com'
$script:VerifierPath = ''
$script:UpdaterDownload = ''
$script:UpdaterDownloadHash = ''
$script:CaRendered = ''
# (script-scope variables share their names with the parameters: the resolved values use other names)
$script:UpdaterBundle = ''
$script:UpdaterResolvedVersion = ''
# Keyless identity of the Myriarch release workflow (ADR-0029, frozen from v0.1.0 on: release.yml is never renamed).
# The repository must equal internal/brand.Repository; a Go test compares both.
$script:ReleaseRepository = 'myriarch/myriarch'
$script:MinCosignVersion = [Version]'3.0.4'

# ---- Error codes (stable interface) -------------------------------------------------------------------
# The full catalog with the shared codes (usage_error, option_missing, option_invalid, option_conflict,
# updater_hash_source_missing, insecure_required, https_required, file_unreadable, tool_missing,
# sigstore_verifier_missing, sigstore_verifier_outdated, option_removed, privilege_required,
# unsupported_platform, conflicting_install, download_failed, template_missing, template_placeholder, checksum_invalid,
# checksum_mismatch, checksum_entry_count, checksum_conflict, sigstore_verification_failed, archive_invalid,
# collector_start_failed, collector_version_mismatch, supervisor_missing, service_not_running, token_on_command_line,
# updater_unauthenticated) is at the top of install-linux.sh.
# Windows-only codes:
#   authenticode_invalid       the Authenticode signature of the updater is invalid or from another certificate
#   directory_unsafe           reparse point, wrong owner, unexpected ACE, or not a directory; also a -Verifier in a directory
#                              that unprivileged accounts can change
#   acl_failed                 icacls failed
#   service_config_failed      sc.exe failed
#   updater_install_failed     "myriarch-updater install" failed
#   group_membership_failed    WARNING: the service account could not be added to a local group
#   language_mode_unsupported  PowerShell runs the script in Constrained Language Mode (App Control for Business) or another
#                              mode below FullLanguage; it is the first check of the run, before anything else happens
#   unexpected_error           a failure that is not classified above (the original message follows the code)
# service_not_running is fatal here (a warning on Linux). When the service is not running because the file of its binPath is gone,
# supervisor_missing is raised in its place.
# Every fatal error goes through Stop-Install; the top-level catch prints
#   [myriarch-install] ERROR [<code>] <message>

function Write-Log { param([string]$Message) Write-Host "[myriarch-install] $Message" }
# Write-Warn <code> <message>: PowerShell adds the "WARNING: " prefix itself.
function Write-Warn { param([string]$Code, [string]$Message) Write-Warning "[myriarch-install] [$Code] $Message" }

# Stop-Install <code> <message>: the only way to raise a fatal error (see the error code catalog above).
function Stop-Install { param([string]$Code, [string]$Message) throw "[$Code] $Message" }

# The installer needs the FullLanguage mode: it calls .NET methods for hashes, ACLs, the registry and services. App Control for
# Business with script enforcement runs a script that no rule allows, an unsigned one among them, in Constrained Language Mode,
# where the first method call fails with a PowerShell error that does not name the cause. This is the first action of Invoke-Main
# (a test pins the order) and it uses a property, cmdlets and a throw only, nothing that a restricted mode refuses. It changes
# nothing; the message names the file and the rule that lets it run.
function Assert-FullLanguageMode {
  $mode = $ExecutionContext.SessionState.LanguageMode
  if ($mode -eq 'FullLanguage') { return }
  $file = Get-Variable -Name PSCommandPath -ValueOnly -ErrorAction SilentlyContinue
  if (-not $file) { $file = 'install-windows.ps1' }
  Stop-Install 'language_mode_unsupported' "PowerShell runs $file in the language mode $mode instead of FullLanguage. App Control for Business with script enforcement does that to a script that no rule allows, and the installer cannot work in this mode; it changed nothing. Allow the file with a hash rule (create the rule from the copy that you verified, one rule per release; a publisher rule works once the Windows files are Authenticode-signed), then start the installer again; deploy\windows\README.md, section Language mode and App Control, has the details"
}

function Test-Administrator {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($id)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# -Root: staging mode (image build/tests) - all paths under the prefix, no services, no ACLs.
function Get-Layout {
  param([string]$Prefix)
  if ($Prefix) {
    $pf = [IO.Path]::Combine($Prefix, 'ProgramFiles', 'Myriarch')
    $pd = [IO.Path]::Combine($Prefix, 'ProgramData', 'Myriarch')
  } else {
    $pf = 'C:\Program Files\Myriarch'
    $pd = 'C:\ProgramData\Myriarch'
  }
  return @{
    ProgramFiles = $pf
    ProgramData  = $pd
    Collector    = [IO.Path]::Combine($pf, 'collector')
    Versions     = [IO.Path]::Combine($pf, 'collector', 'versions')
    Bin          = [IO.Path]::Combine($pf, 'collector', 'bin')
    Supervisor   = [IO.Path]::Combine($pf, 'supervisor')
    Updater      = [IO.Path]::Combine($pf, 'updater')
    Config       = [IO.Path]::Combine($pd, 'config')
    SupState     = [IO.Path]::Combine($pd, 'supervisor')
    Storage      = [IO.Path]::Combine($pd, 'storage')
    UpdaterState = [IO.Path]::Combine($pd, 'updater')
    Logs         = [IO.Path]::Combine($pd, 'logs')
  }
}

# Run native programs without the NativeCommandError pitfalls of Windows PowerShell 5.1
# ($ErrorActionPreference = 'Stop' + stderr). Result: ExitCode + output (stdout and stderr).
function Invoke-Native {
  param([string]$File, [string[]]$Arguments)
  $old = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  try {
    $text = (& $File @Arguments 2>&1 | Out-String)
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $old
  }
  return [pscustomobject]@{ ExitCode = $code; Output = $text.Trim() }
}

function Get-Sha256 {
  param([string]$Path)
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

# https only (also after redirects); -Insecure allows lab sources (http, file).
function Assert-TransportAllowed {
  param([string]$Url, [string]$What)
  if ($Url -match '^https://\S+$') { return }
  if ($Insecure) { return }
  Stop-Install 'https_required' "$What must be an https:// URL (-Insecure only for lab/tests): $Url"
}

# ---- Download retry policy (the same in install-linux.sh, ci/release/smoke.sh and ci/release/smoke-windows.ps1) ----------
# Only a transient failure is retried: a timeout, a failed connection (refused, reset, name resolution) or the HTTP status 408,
# 429, 500, 502, 503 or 504. Everything else (404, 403, a TLS or certificate error, a refused redirect, a local write error)
# fails at once. The pause starts at 1 s and doubles; no attempt starts later than 120 s after the first one. Under PowerShell 7.5 or
# later (the .NET 9 runtime reports the status of the proxy) behind an HTTPS_PROXY that refuses the CONNECT, the status of the proxy
# decides (503 is retried, 407 is not), as in the bash scripts; the message says "proxy HTTP <n>". PowerShell 7.4 (.NET 8) reports no
# status for a refused CONNECT, so it is retried like a failed connection, with the .NET message. The code runs on Windows
# PowerShell 5.1 as well as on PowerShell 7: it uses no parameter of Invoke-WebRequest that only 7 has.

# Seconds on a monotonic clock (the retry window is measured with it).
function Get-MonotonicSeconds {
  return [Diagnostics.Stopwatch]::GetTimestamp() / [Diagnostics.Stopwatch]::Frequency
}

# The HTTP status of a failed request, 0 without one. Windows PowerShell 5.1: WebException.Response (an HttpWebResponse);
# PowerShell 7: HttpResponseException.Response (an HttpResponseMessage). Both have StatusCode. PowerShell 7.5 and later report a proxy
# that refuses the CONNECT of an https request as an HttpRequestException without a response, with the status of the proxy in
# StatusCode (7.4 leaves StatusCode empty).
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

# True when a failed request is worth another attempt. The types are compared by name: System.Net.Http is not loaded in 5.1.
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

function Invoke-Download {
  param([string]$Url, [string]$Destination)
  Assert-TransportAllowed -Url $Url -What 'download URL'
  Write-Log "downloading $Url"
  if ($Url -like 'file://*') {
    # local mirror/tests (only with -Insecure)
    $local = ([Uri]$Url).LocalPath
    try {
      Copy-Item -LiteralPath $local -Destination $Destination -Force -ErrorAction Stop
    } catch {
      Stop-Install 'download_failed' "download failed: $Url ($($_.Exception.Message))"
    }
    return
  }
  try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  } catch { }
  $window = 120
  $pause = 1
  $start = Get-MonotonicSeconds
  $attempt = 0
  while ($true) {
    $attempt++
    try {
      $resp = Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $Destination -PassThru
      if (-not $Insecure) {
        $final = $null
        try { $final = $resp.BaseResponse.ResponseUri } catch { }
        if (-not $final) { try { $final = $resp.BaseResponse.RequestMessage.RequestUri } catch { } }
        if ($final -and $final.Scheme -ne 'https') {
          Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
          Stop-Install 'https_required' "redirect to $($final.Scheme) refused: $Url"
        }
      }
      return
    } catch {
      # a refused redirect is final (matched by its code, not by message text)
      if ($_.Exception.Message.Contains('[https_required]')) { throw }
      $reason = Get-DownloadReason $_.Exception
      $noun = if ($attempt -eq 1) { 'attempt' } else { 'attempts' }
      if (-not (Test-TransientDownloadError $_.Exception)) {
        Stop-Install 'download_failed' "download failed: $Url ($reason; not retried, $attempt $noun)"
      }
      $elapsed = [int][Math]::Floor((Get-MonotonicSeconds) - $start)
      if ($elapsed -ge $window) {
        Stop-Install 'download_failed' "download failed: $Url ($reason; retry window of $window s used up, $attempt $noun)"
      }
      # the last pause ends exactly at the end of the window
      if ($elapsed + $pause -gt $window) { $pause = $window - $elapsed }
      Write-Log "attempt $attempt for $Url failed ($reason), next attempt in $pause s"
      Start-Sleep -Seconds $pause
      $pause = $pause * 2
    }
  }
}

# Certificate identity (SAN) of the Sigstore bundle of one release asset, bound to the requested version: the
# tag in the identity must equal v<version>, so that a genuine but older release served from a mirror is not
# accepted as the requested version. Workflow: base-release (collector) or base-binary-release (supervisor).
function Get-CertIdentity {
  param([string]$Workflow, [string]$Version)
  return '^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/' + $Workflow + '\.yaml@refs/tags/v' + [regex]::Escape($Version) + '$'
}

# Certificate identity (SAN) of the Sigstore bundle of checksums.txt of one Myriarch release, bound to the version: the
# workflow release.yml of this repository, run for the tag v<version>. The tag is escaped and the pattern is anchored at
# both ends, so that v0.1.0 does not accept v0.1.01, v10.1.0 or another workflow or repository.
function Get-ReleaseCertIdentity {
  param([string]$Version)
  return '^https://github\.com/' + [regex]::Escape($script:ReleaseRepository) + '/\.github/workflows/release\.yml@refs/tags/v' + [regex]::Escape($Version) + '$'
}

# True when only SYSTEM, Administrators and TrustedInstaller can change what the directory contains: a program
# found there is not replaceable by an unprivileged account. Anything that cannot be established (no ACL
# support, errors) counts as not protected.
function Test-AdminOnlyDirectory {
  param([string]$Path)
  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { return $false }
  try {
    $item = Get-Item -LiteralPath $Path -Force
    if (-not $item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { return $false }
    $trusted = @($script:SidSystem, $script:SidAdmins, $script:SidTrustedInstaller)
    if ($trusted -notcontains (Get-OwnerSid -Path $Path)) { return $false }
    $write = [System.Security.AccessControl.FileSystemRights]::WriteData -bor [System.Security.AccessControl.FileSystemRights]::AppendData -bor
      [System.Security.AccessControl.FileSystemRights]::WriteExtendedAttributes -bor [System.Security.AccessControl.FileSystemRights]::WriteAttributes -bor
      [System.Security.AccessControl.FileSystemRights]::Delete -bor [System.Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor
      [System.Security.AccessControl.FileSystemRights]::ChangePermissions -bor [System.Security.AccessControl.FileSystemRights]::TakeOwnership
    $acl = Get-Acl -LiteralPath $Path
    foreach ($ace in $acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])) {
      if ($ace.AccessControlType -ne 'Allow') { continue }
      $sid = $ace.IdentityReference.Value
      if (($trusted + 'S-1-3-0') -contains $sid) { continue }   # S-1-3-0 (CREATOR OWNER) only applies to children
      if (($ace.FileSystemRights -band $write) -ne 0) { return $false }
    }
    return $true
  } catch {
    return $false
  }
}

# True where directory ACLs exist (Windows). Other platforms only run the test suites, which replace this function.
function Test-AclEnforced {
  return [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
}

# Returns an empty string when the cosign binary reports MinCosignVersion or newer, otherwise the reason.
function Get-CosignVersionProblem {
  param([string]$Path)
  $r = Invoke-Native -File $Path -Arguments @('version')
  if ($r.ExitCode -ne 0) { return "cosign `"$Path`" failed to report its version" }
  $found = $null; $pre = ''
  foreach ($line in ($r.Output -split "`r?`n")) {
    if ($line -match '^GitVersion:\s+v?(\d+\.\d+\.\d+)(-[^+\s]+)?') { $found = [Version]$Matches[1]; $pre = $Matches[2]; break }
  }
  if (-not $found) { return "cosign `"$Path`" reports no usable version" }
  # a pre-release of the minimum (3.0.4-rc.1) sorts before it
  if ($pre -and $found -eq $script:MinCosignVersion) { return "cosign $found$pre at `"$Path`" is too old (a pre-release of the minimum)" }
  if ($found -lt $script:MinCosignVersion) { return "cosign $found at `"$Path`" is too old" }
  return ''
}

function Stop-CosignOutdated {
  param([string]$Reason)
  Stop-Install 'sigstore_verifier_outdated' "${Reason}: cosign $($script:MinCosignVersion) or newer is required (older versions have known verification flaws); update cosign, pass -Verifier <installed myriarch-updater.exe>, or verify checksums.txt on an admin host and pass -UpdaterSha256"
}

# Sigstore verifier, in this order: -Verifier (in a directory only Administrators can write; a cosign must be 3.0.4 or newer), the SHA-256 checked updater from
# -WithUpdater (only with -UpdaterSha256 and without -UpdaterChecksumsUrl: with a checksums.txt the verifier is resolved
# BEFORE the updater is downloaded and that updater is never used, because its hash comes from the file that this verifier
# has to check), an already installed myriarch-updater, cosign.exe 3.0.4 or newer from PATH - but only from a directory that
# unprivileged accounts cannot write (PATH may contain such directories, and this script runs elevated). Without a verifier
# the installation aborts.
function Resolve-Verifier {
  param($L)
  if ($VerifySignatures -eq 'skip') { return }
  if ($Verifier) {
    # an elevated installer must not run a verifier that unprivileged accounts can replace (the verifier decides whether
    # checksums.txt, and with it the SYSTEM updater, is trusted)
    $verifierDir = Split-Path -Parent ([IO.Path]::GetFullPath($Verifier))
    if ((Test-AclEnforced) -and -not (Test-AdminOnlyDirectory -Path $verifierDir)) {
      Stop-Install 'directory_unsafe' "-Verifier is in $verifierDir, which unprivileged accounts can change; copy it into a directory only Administrators can write (for example C:\Program Files\cosign) and pass that path"
    }
    if ((Split-Path -Leaf $Verifier) -like 'cosign*') {
      $problem = Get-CosignVersionProblem -Path $Verifier
      if ($problem) { Stop-CosignOutdated -Reason $problem }
    }
    $script:VerifierPath = $Verifier
    return
  }
  if ($script:UpdaterDownload -and -not $UpdaterChecksumsUrl -and $UpdaterSha256) { $script:VerifierPath = $script:UpdaterDownload; return }
  $installed = Join-Path $L.Updater 'myriarch-updater.exe'
  if (Test-Path -LiteralPath $installed) { $script:VerifierPath = $installed; return }
  $outdated = ''
  $cosign = Get-Command cosign.exe -ErrorAction SilentlyContinue
  if ($cosign -and (Test-AdminOnlyDirectory -Path (Split-Path -Parent $cosign.Source))) {
    $outdated = Get-CosignVersionProblem -Path $cosign.Source
    if (-not $outdated) { $script:VerifierPath = $cosign.Source; return }
  } elseif ($cosign) {
    Write-Warn 'sigstore_verifier_missing' "cosign.exe found in $(Split-Path -Parent $cosign.Source) is not used: the directory can be changed by unprivileged accounts; pass -Verifier or install cosign.exe into a directory only Administrators can write"
  }
  if ($outdated) { Stop-CosignOutdated -Reason $outdated }
  Stop-Install 'sigstore_verifier_missing' 'no Sigstore verifier found: pass -Verifier <installed myriarch-updater.exe or cosign.exe>, install cosign.exe 3.0.4 or newer into a directory only Administrators can write, or use -WithUpdater with -UpdaterSha256 (verify checksums.txt on an admin host first)'
}

function Confirm-Signature {
  param([string]$File, [string]$BundleUrl, [string]$IdentityRegexp, [string]$Version, [string]$TmpDir)
  if ($VerifySignatures -eq 'skip') { return }
  $bundle = Join-Path $TmpDir ((Split-Path -Leaf $File) + '.sigstore.json')
  Invoke-Download -Url $BundleUrl -Destination $bundle
  $v = $script:VerifierPath
  if ((Split-Path -Leaf $v) -like 'cosign*') {
    $vargs = @('verify-blob', '--bundle', $bundle, '--certificate-identity-regexp', $IdentityRegexp, '--certificate-oidc-issuer', $script:CertIssuer)
    if ($TrustedRoot) { $vargs += @('--trusted-root', $TrustedRoot) }
    $vargs += $File
  } else {
    $vargs = @('verify-bundle', '--bundle', $bundle, '--artifact', $File, '--version', "v$Version", '--identity-regexp', $IdentityRegexp, '--issuer', $script:CertIssuer)
    if ($TrustedRoot) { $vargs += @('--trusted-root', $TrustedRoot) }
  }
  $r = Invoke-Native -File $v -Arguments $vargs
  if ($r.ExitCode -ne 0) { Stop-Install 'sigstore_verification_failed' "Sigstore verification of $(Split-Path -Leaf $File) failed: $($r.Output)" }
  Write-Log "Sigstore signature of $(Split-Path -Leaf $File) ok"
}

# Downloads the asset and <asset>.sha256 (64 hex characters) and checks the hash.
function Get-VerifiedAsset {
  param([string]$Url, [string]$Destination)
  Invoke-Download -Url $Url -Destination $Destination
  Invoke-Download -Url "$Url.sha256" -Destination "$Destination.sha256"
  $want = ((Get-Content -LiteralPath "$Destination.sha256" -Raw) -replace '\s', '')
  if ($want.Length -lt 64) { Stop-Install 'checksum_invalid' "invalid .sha256 file for $Url" }
  $want = $want.Substring(0, 64).ToLowerInvariant()
  if ($want -notmatch '^[0-9a-f]{64}$') { Stop-Install 'checksum_invalid' "invalid .sha256 file for $Url" }
  $got = Get-Sha256 -Path $Destination
  if ($got -ne $want) { Stop-Install 'checksum_mismatch' "SHA-256 mismatch: $Url (expected $want, got $got)" }
  Write-Log "SHA-256 ok: $(Split-Path -Leaf $Destination)"
  return $got
}

# Copies a file and re-hashes the copy: the hash that was verified earlier must still be the hash of the file at its
# final location (the source can be changed between the check and the copy). A mismatch removes the copy.
function Copy-VerifiedFile {
  param([string]$Source, [string]$Destination, [string]$ExpectedHash)
  Copy-Item -LiteralPath $Source -Destination $Destination -Force
  if ((Get-Sha256 -Path $Destination) -ne $ExpectedHash) {
    Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
    Stop-Install 'checksum_mismatch' "$(Split-Path -Leaf $Destination) changed after its SHA-256 was verified"
  }
}

# Escape a value for YAML double quotes (\ and ")
function ConvertTo-YamlEscaped {
  param([string]$Value)
  return $Value.Replace('\', '\\').Replace('"', '\"')
}

# Renders a template: replaces @@KEY@@ from $Values; lines with a placeholder whose value is empty
# are removed. If CA_FILE is empty, the line "  tls:" is dropped as well.
function Format-Template {
  param([string]$TemplatePath, [string]$OutPath, [hashtable]$Values)
  if (-not (Test-Path -LiteralPath $TemplatePath)) { Stop-Install 'template_missing' "template not found: $TemplatePath" }
  $caEmpty = $Values.ContainsKey('CA_FILE') -and [string]::IsNullOrEmpty([string]$Values['CA_FILE'])
  $out = New-Object System.Collections.Generic.List[string]
  foreach ($line in [System.IO.File]::ReadAllLines($TemplatePath)) {
    if ($caEmpty -and $line -match '^\s{2}tls:\s*$') { continue }
    $skip = $false
    foreach ($key in $Values.Keys) {
      $marker = "@@$key@@"
      if ($line.Contains($marker)) {
        $val = [string]$Values[$key]
        if ([string]::IsNullOrEmpty($val)) { $skip = $true; break }
        $line = $line.Replace($marker, (ConvertTo-YamlEscaped -Value $val))
      }
    }
    if (-not $skip) { $out.Add($line) }
  }
  $text = ($out -join "`n") + "`n"
  $left = [regex]::Matches($text, '@@[A-Z_]+@@') | ForEach-Object { $_.Value } | Sort-Object -Unique
  if ($left) { Stop-Install 'template_placeholder' "unreplaced placeholders in ${TemplatePath}: $($left -join ' ')" }
  $dir = Split-Path -Parent $OutPath
  if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
  # UTF-8 without BOM
  [System.IO.File]::WriteAllText($OutPath, $text, (New-Object System.Text.UTF8Encoding($false)))
}

function Get-OwnerSid {
  param([string]$Path)
  $acl = Get-Acl -LiteralPath $Path
  return $acl.GetOwner([System.Security.Principal.SecurityIdentifier]).Value
}

function Assert-NoReparsePoint {
  param([string]$Path)
  foreach ($entry in [System.IO.Directory]::EnumerateFileSystemEntries($Path)) {
    $attr = [System.IO.File]::GetAttributes($entry)
    if (($attr -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { Stop-Install 'directory_unsafe' "reparse point (junction/symlink) not allowed in the data directory: $entry" }
    if (($attr -band [System.IO.FileAttributes]::Directory) -ne 0) { Assert-NoReparsePoint -Path $entry }
  }
}

function Invoke-Icacls {
  param([string[]]$Arguments)
  $r = Invoke-Native -File 'icacls.exe' -Arguments $Arguments
  if ($r.ExitCode -ne 0) { Stop-Install 'acl_failed' "icacls $($Arguments -join ' ') failed (exit $($r.ExitCode)): $($r.Output)" }
}

# Adopts a data directory only if it is owned by SYSTEM/Administrators/TrustedInstaller and is not a
# reparse point (otherwise abort: a local user might have created the directory beforehand). Then the ACL
# is replaced atomically by a protected DACL (SYSTEM + Administrators only, inherited) and all contents
# are reset (inherited ACL, owner Administrators).
function Protect-DirectoryTree {
  param([string]$Path)
  $item = Get-Item -LiteralPath $Path -Force
  if (-not $item.PSIsContainer) { Stop-Install 'directory_unsafe' "$Path is not a directory" }
  if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { Stop-Install 'directory_unsafe' "$Path is a reparse point" }
  $owner = Get-OwnerSid -Path $Path
  if (@($script:SidSystem, $script:SidAdmins, $script:SidTrustedInstaller) -notcontains $owner) {
    Stop-Install 'directory_unsafe' "$Path is owned by $owner instead of SYSTEM/Administrators; inspect and remove the directory, then install again"
  }
  $sec = New-Object System.Security.AccessControl.DirectorySecurity
  $sec.SetAccessRuleProtection($true, $false)
  foreach ($sid in @($script:SidSystem, $script:SidAdmins)) {
    $id = New-Object System.Security.Principal.SecurityIdentifier($sid)
    $sec.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($id, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')))
  }
  (New-Object System.IO.DirectoryInfo($Path)).SetAccessControl($sec)
  Assert-NoReparsePoint -Path $Path
  $first = [System.IO.Directory]::EnumerateFileSystemEntries($Path) | Select-Object -First 1
  if ($first) {
    Invoke-Icacls -Arguments @("$Path\*", '/reset', '/T', '/C', '/Q')
    Invoke-Icacls -Arguments @("$Path\*", '/setowner', "*$($script:SidAdmins)", '/T', '/C', '/Q')
  }
}

# Check (fail-closed): trusted owner, ACEs only for allowed principals - for the whole tree.
function Assert-TreeAcl {
  param([string]$Path, [string[]]$AllowedSids)
  $paths = @($Path) + @([System.IO.Directory]::EnumerateFileSystemEntries($Path, '*', [System.IO.SearchOption]::AllDirectories))
  foreach ($p in $paths) {
    $owner = Get-OwnerSid -Path $p
    if (@($script:SidSystem, $script:SidAdmins, $script:SidTrustedInstaller) -notcontains $owner) { Stop-Install 'directory_unsafe' "unexpected owner $owner on $p" }
    $acl = Get-Acl -LiteralPath $p
    foreach ($ace in $acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])) {
      if ($ace.AccessControlType -eq 'Allow' -and $AllowedSids -notcontains $ace.IdentityReference.Value) {
        Stop-Install 'directory_unsafe' "unexpected ACL entry for $($ace.IdentityReference.Value) on $p"
      }
    }
  }
}

function Initialize-DataDirectories {
  param($L, [bool]$Staging)
  if (-not $Staging) {
    if (-not (Test-Path -LiteralPath $L.ProgramData)) { New-Item -ItemType Directory -Path $L.ProgramData -Force | Out-Null }
    Protect-DirectoryTree -Path $L.ProgramData
  }
  foreach ($d in @($L.ProgramData, $L.Config, $L.SupState, $L.Storage, $L.UpdaterState, $L.Logs)) {
    New-Item -ItemType Directory -Path $d -Force | Out-Null
  }
}

# Virtual service account: the supervisor (and thereby the collector, which applies remote config) does not
# run as SYSTEM. Access only to its config (supervisor.yaml, fallback.yaml: read) and its data directories
# (supervisor storage, collector queue: modify); token, updater.yaml and updater state stay SYSTEM/Admins.
function Get-ServiceAccountName { return "NT SERVICE\$($script:SupervisorService)" }

function Get-ServiceAccountSid {
  $nt = New-Object System.Security.Principal.NTAccount((Get-ServiceAccountName))
  return $nt.Translate([System.Security.Principal.SecurityIdentifier]).Value
}

function Grant-ServiceAccountAccess {
  param($L)
  $sid = Get-ServiceAccountSid
  Invoke-Icacls -Arguments @($L.SupState, '/grant', "*${sid}:(OI)(CI)M")
  Invoke-Icacls -Arguments @($L.Storage, '/grant', "*${sid}:(OI)(CI)M")
  Invoke-Icacls -Arguments @($L.Config, '/grant', "*${sid}:(RX)")
  foreach ($f in @('supervisor.yaml', 'fallback.yaml', 'ca.pem')) {
    if (Test-Path -LiteralPath (Join-Path $L.Config $f)) {
      Invoke-Icacls -Arguments @((Join-Path $L.Config $f), '/grant', "*${sid}:(R)")
    }
  }
  # Event log and performance counters for the host receivers (best effort)
  foreach ($groupSid in @('S-1-5-32-573', 'S-1-5-32-558')) {
    try {
      $g = Get-LocalGroup -SID $groupSid -ErrorAction Stop
      $r = Invoke-Native -File 'net.exe' -Arguments @('localgroup', $g.Name, (Get-ServiceAccountName), '/add')
      if ($r.ExitCode -ne 0 -and $r.Output -notmatch '1378') { Write-Warn 'group_membership_failed' "group $($g.Name): $($r.Output)" }
    } catch {
      Write-Warn 'group_membership_failed' "group membership ($groupSid) could not be set: $($_.Exception.Message)"
    }
  }
  return $sid
}

function Set-FileText {
  param([string]$Path, [string]$Text)
  [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

function Copy-FileIfDifferent {
  param([string]$Source, [string]$Destination)
  if ((Test-Path -LiteralPath $Destination) -and ((Get-Sha256 -Path $Source) -eq (Get-Sha256 -Path $Destination))) { return $false }
  Copy-Item -LiteralPath $Source -Destination $Destination -Force
  return $true
}

function Test-ServiceExists {
  param([string]$Name)
  if ($script:StagingMode) { return $false }
  return [bool](Get-Service -Name $Name -ErrorAction SilentlyContinue)
}

function Stop-ServiceAndWait {
  param([string]$Name, [int]$TimeoutSeconds = 60)
  if ($script:StagingMode) { return }
  $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
  if (-not $svc) { return }
  if ($svc.Status -ne 'Stopped') {
    Stop-Service -Name $Name -Force -ErrorAction Stop
    $svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds($TimeoutSeconds))
  }
}

# The MSI packages of the upstream project register services of their own: the contrib MSI installs "otelcol-contrib" and starts it
# at once, the supervisor MSI installs "opampsupervisor" with manual start (docs/design/upstream-notes.md, "Packages and what they
# install"). This installer uses neither package: it creates "otelcol-supervisor" with the supervisor in a folder of its own, so
# each of those services would be a second collector on the host that myriarch does not manage. The run stops before it stops a
# service, downloads or writes anything, and removes nothing: which package goes is the decision of the operator. Staging mode
# (-Root) touches no service and reads none.
function Assert-NoConflictingInstall {
  if ($script:StagingMode) { return }
  $found = @()
  foreach ($name in @('opampsupervisor', 'otelcol-contrib')) {
    $svc = Get-Service -Name $name -ErrorAction SilentlyContinue
    if ($svc) { $found += "service $name ($($svc.Status))" }
  }
  if ($found.Count -eq 0) { return }
  Stop-Install 'conflicting_install' "an installation of the upstream project is present: $($found -join ', '); it provides a second collector next to the one that myriarch manages, and the installer removes nothing - uninstall its MSI (Settings, Apps, or msiexec /x with the MSI file, or sc.exe delete for a service that you created yourself) and run the installer again"
}

# Working directory for downloads and staging. It is created with a protected DACL (SYSTEM and Administrators only,
# owner Administrators) before anything is written into it: the installer runs elevated, and a directory that
# inherits the ACL of the invoking account's temporary directory could be changed by a non-elevated process of that
# account between a check and the use of a file.
function New-WorkDirectory {
  param([bool]$Staging)
  $dir = Join-Path ([IO.Path]::GetTempPath()) ('myriarch-install-' + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  if (-not $Staging) {
    $sec = New-Object System.Security.AccessControl.DirectorySecurity
    $sec.SetAccessRuleProtection($true, $false)
    $sec.SetOwner((New-Object System.Security.Principal.SecurityIdentifier($script:SidAdmins)))
    foreach ($sid in @($script:SidSystem, $script:SidAdmins)) {
      $id = New-Object System.Security.Principal.SecurityIdentifier($sid)
      $sec.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($id, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')))
    }
    (New-Object System.IO.DirectoryInfo($dir)).SetAccessControl($sec)
    Assert-TreeAcl -Path $dir -AllowedSids @($script:SidSystem, $script:SidAdmins)
  }
  return $dir
}

function Test-Inputs {
  if ($script:OpampEndpoint -notmatch '^(wss?|https?)://\S+$') { Stop-Install 'option_invalid' '-OpampEndpoint must start with wss:// or https://' }
  if ($script:OpampEndpoint -match '^(ws|http)://' -and -not $Insecure) {
    Stop-Install 'insecure_required' '-OpampEndpoint without TLS (ws://, http://) is not allowed: the token would cross the network in clear text (-Insecure only for lab/tests)'
  }
  if (-not $SiteType) { Stop-Install 'option_missing' '-SiteType is missing' }
  if (-not $SiteId) { Stop-Install 'option_missing' '-SiteId is missing' }
  if (-not $CollectorVersion) { Stop-Install 'option_missing' '-CollectorVersion is missing' }
  $verRe = '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'
  $script:CollectorVersion = $CollectorVersion.TrimStart('v')
  if ($script:CollectorVersion -notmatch $verRe) { Stop-Install 'option_invalid' "invalid -CollectorVersion: $CollectorVersion" }
  $script:SupervisorVersion = $(if ($SupervisorVersion) { $SupervisorVersion.TrimStart('v') } else { $script:CollectorVersion })
  if ($script:SupervisorVersion -notmatch $verRe) { Stop-Install 'option_invalid' "invalid -SupervisorVersion: $SupervisorVersion" }
  foreach ($v in @($SiteType, $SiteId, $SiteName, $SiteCountry, $SiteRegion, $SiteTimezone, $Environment, $OpampEndpoint, $CaFile)) {
    if ($v -and $v -match '[\x00-\x1f]') { Stop-Install 'option_invalid' 'control character in a parameter value' }
  }
  if ($Token -and $TokenFile) { Stop-Install 'option_conflict' '-Token and -TokenFile are mutually exclusive' }
  $tok = $Token
  if ($TokenFile) {
    if (-not (Test-Path -LiteralPath $TokenFile)) { Stop-Install 'file_unreadable' "token file is not readable: $TokenFile" }
    $tok = ((Get-Content -LiteralPath $TokenFile -Raw) -replace '\s', '')
  }
  if (-not $tok) { Stop-Install 'option_missing' '-Token or -TokenFile is missing' }
  if ($Token) { Write-Warn 'token_on_command_line' '-Token is visible in the process command line (process logs, EDR); prefer -TokenFile' }
  if ($tok -notmatch '^[A-Za-z0-9_.~+/=-]+$') { Stop-Install 'option_invalid' 'the token contains forbidden characters' }
  $script:EffectiveToken = $tok
  if ($CaFile -and -not [IO.Path]::IsPathRooted($CaFile)) { Stop-Install 'option_invalid' '-CaFile must be an absolute path' }
  if ($CaFile -and -not (Test-Path -LiteralPath $CaFile)) { Stop-Install 'file_unreadable' "CA file is not readable: $CaFile" }
  if ($TrustedRoot -and -not (Test-Path -LiteralPath $TrustedRoot)) { Stop-Install 'file_unreadable' "trusted_root.json is not readable: $TrustedRoot" }
  if ($VerifySignatures -eq 'skip' -and -not $Insecure) { Stop-Install 'insecure_required' '-VerifySignatures skip is only allowed together with -Insecure (lab/tests)' }
  if ($Verifier -and -not (Test-Path -LiteralPath $Verifier)) { Stop-Install 'file_unreadable' "-Verifier not found: $Verifier" }
  if (-not $WithUpdater -and ($UpdaterVersion -or $UpdaterChecksumsUrl -or $UpdaterBundleUrl -or $UpdaterSignerThumbprint)) {
    Stop-Install 'option_conflict' '-UpdaterVersion/-UpdaterChecksumsUrl/-UpdaterBundleUrl/-UpdaterSignerThumbprint only apply together with -WithUpdater'
  }
  if ($WithUpdater) {
    if (-not $UpdaterUrl) { Stop-Install 'option_missing' '-WithUpdater requires -UpdaterUrl' }
    Assert-TransportAllowed -Url $UpdaterUrl -What '-UpdaterUrl'
    $asset = (($UpdaterUrl -replace '[?#].*$', '') -split '[/\\]') | Select-Object -Last 1
    if (-not $asset) { Stop-Install 'option_invalid' "no file name in -UpdaterUrl: $UpdaterUrl" }
    $urlVersion = ''
    if ($asset -cmatch '^myriarch-updater_([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?)_windows_(amd64|arm64)\.exe$') { $urlVersion = $Matches[1] }
    $script:UpdaterResolvedVersion = $urlVersion
    if ($UpdaterVersion) {
      $given = $UpdaterVersion.TrimStart('v')
      if ($given -notmatch $verRe) { Stop-Install 'option_invalid' "invalid -UpdaterVersion: $UpdaterVersion" }
      if ($urlVersion -and $urlVersion -cne $given) {
        Stop-Install 'option_conflict' "-UpdaterVersion $given and the version in -UpdaterUrl ($urlVersion) differ"
      }
      $script:UpdaterResolvedVersion = $given
    }
    if ($UpdaterBundleUrl -and -not $UpdaterChecksumsUrl) { Stop-Install 'option_conflict' '-UpdaterBundleUrl requires -UpdaterChecksumsUrl' }
    if ($UpdaterChecksumsUrl) {
      if ($VerifySignatures -ne 'require') {
        Stop-Install 'option_conflict' '-UpdaterChecksumsUrl cannot be combined with -VerifySignatures skip: checksums.txt would not be authenticated (pass -UpdaterSha256 instead)'
      }
      if (-not $script:UpdaterResolvedVersion) {
        Stop-Install 'option_missing' '-UpdaterVersion is missing: the file name in -UpdaterUrl does not carry the version (myriarch-updater_<version>_windows_<arch>.exe) and the identity of the release is bound to it'
      }
      Assert-TransportAllowed -Url $UpdaterChecksumsUrl -What '-UpdaterChecksumsUrl'
      if ($UpdaterBundleUrl) { $script:UpdaterBundle = $UpdaterBundleUrl } else { $script:UpdaterBundle = "$UpdaterChecksumsUrl.sigstore.json" }
      Assert-TransportAllowed -Url $script:UpdaterBundle -What '-UpdaterBundleUrl'
    }
    if (-not $UpdaterSha256 -and -not $UpdaterChecksumsUrl -and -not $Insecure) {
      Stop-Install 'updater_hash_source_missing' '-WithUpdater requires -UpdaterSha256 or -UpdaterChecksumsUrl - the binary runs as SYSTEM and needs a trusted hash source (checksums.txt of the release, verified with its Sigstore bundle)'
    }
    if ($UpdaterSha256 -and $UpdaterSha256 -notmatch '^[0-9a-fA-F]{64}$') { Stop-Install 'option_invalid' '-UpdaterSha256 is not a SHA-256 (64 hex characters)' }
    if ($UpdaterSignerThumbprint -and (($UpdaterSignerThumbprint -replace '\s', '') -notmatch '^[0-9a-fA-F]{40}$')) {
      Stop-Install 'option_invalid' '-UpdaterSignerThumbprint is not a certificate thumbprint (40 hex characters)'
    }
  }
  if ($ReleaseBaseUrl -notmatch '^(https?|file)://\S+$') { Stop-Install 'option_invalid' '-ReleaseBaseUrl must be an https:// URL' }
  Assert-TransportAllowed -Url $ReleaseBaseUrl -What '-ReleaseBaseUrl'
  $script:ReleaseBaseUrl = $ReleaseBaseUrl.TrimEnd('/')
}

function Resolve-Arch {
  $a = $Arch
  if (-not $a) {
    $pa = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) { $pa = $env:PROCESSOR_ARCHITEW6432 }
    switch ($pa) {
      'AMD64' { $a = 'amd64' }
      'ARM64' { $a = 'arm64' }
      default { Stop-Install 'unsupported_platform' "unsupported architecture: $pa" }
    }
  }
  if ($a -ne 'amd64' -and $a -ne 'arm64') { Stop-Install 'option_invalid' '-Arch must be amd64 or arm64' }
  return $a
}

# The upstream project publishes the supervisor for Windows amd64 only (opampsupervisor_<v>_windows_amd64.exe). On ARM64 the x64
# build runs under emulation, which -Arch amd64 asks for. Invoke-Main checks it as soon as the architecture is known, before a
# service is stopped or a file is fetched; Install-SupervisorBinary keeps the check for the case that it is called on its own.
function Assert-SupervisorPlatform {
  param([string]$Arch)
  if ($Arch -ne 'amd64') {
    Stop-Install 'unsupported_platform' 'upstream publishes only opampsupervisor_<v>_windows_amd64.exe for Windows; on ARM64 install with -Arch amd64 (x64 emulation)'
  }
}

function Install-Collector {
  param($L, [string]$Arch, [string]$TmpDir)
  $version = $script:CollectorVersion
  $asset = "otelcol-contrib_${version}_windows_${Arch}.tar.gz"
  $url = "$($script:ReleaseBaseUrl)/v$version/$asset"
  $vdir = Join-Path $L.Versions $version
  $marker = Join-Path $vdir '.myriarch-install.json'
  if ((Test-Path -LiteralPath (Join-Path $vdir $script:CollectorBinary)) -and (Test-Path -LiteralPath $marker)) {
    Write-Log "Collector v$version is already installed"
    return
  }
  $archive = Join-Path $TmpDir $asset
  $null = Get-VerifiedAsset -Url $url -Destination $archive
  Confirm-Signature -File $archive -BundleUrl "$url.sigstore.json" -IdentityRegexp (Get-CertIdentity -Workflow 'base-release' -Version $version) -Version $version -TmpDir $TmpDir
  $stage = Join-Path $TmpDir 'collector-stage'
  if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
  New-Item -ItemType Directory -Path $stage -Force | Out-Null
  # extract only the binary from the archive root (tar.exe: Windows 10 1803+ / Server 2019+)
  $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
  if (-not $tar) { $tar = Get-Command tar -ErrorAction SilentlyContinue }
  if (-not $tar) { Stop-Install 'tool_missing' 'tar.exe not found (requires Windows 10 1803 / Server 2019 or newer)' }
  $r = Invoke-Native -File $tar.Source -Arguments @('-xzf', $archive, '-C', $stage, $script:CollectorBinary)
  if ($r.ExitCode -ne 0) {
    $r = Invoke-Native -File $tar.Source -Arguments @('-xzf', $archive, '-C', $stage, "./$($script:CollectorBinary)")
    if ($r.ExitCode -ne 0) { Stop-Install 'archive_invalid' "$($script:CollectorBinary) not found in the archive root: $($r.Output)" }
  }
  $exe = Join-Path $stage $script:CollectorBinary
  if (-not (Test-Path -LiteralPath $exe)) { Stop-Install 'archive_invalid' "the archive contains no $($script:CollectorBinary)" }
  $item = Get-Item -LiteralPath $exe
  if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { Stop-Install 'archive_invalid' "the archive contains no regular file $($script:CollectorBinary)" }
  $vr = Invoke-Native -File $exe -Arguments @('--version')
  $vout = $vr.Output
  if ($vr.ExitCode -ne 0) { Stop-Install 'collector_start_failed' "the new collector binary cannot be started: $vout" }
  if ($vout -notmatch ('version ' + [regex]::Escape($version) + '(\s|$)')) { Stop-Install 'collector_version_mismatch' "collector reports `"$vout`", expected version $version" }
  $binaryHash = Get-Sha256 -Path $exe
  $json = [ordered]@{
    version     = $version
    contentHash = (Get-Sha256 -Path $archive)
    binaryHash  = $binaryHash
    installedAt = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
  } | ConvertTo-Json
  Set-FileText -Path (Join-Path $stage '.myriarch-install.json') -Text ($json + "`n")
  if (Test-Path -LiteralPath $vdir) { Remove-Item -LiteralPath $vdir -Recurse -Force }
  New-Item -ItemType Directory -Path $L.Versions -Force | Out-Null
  Move-Item -LiteralPath $stage -Destination $vdir
  if (-not $script:StagingMode) {
    # a moved directory keeps its security descriptor: inherit the one of the installation directory instead
    Invoke-Icacls -Arguments @($vdir, '/reset', '/T', '/C', '/Q')
  }
  if ((Get-Sha256 -Path (Join-Path $vdir $script:CollectorBinary)) -ne $binaryHash) {
    Remove-Item -LiteralPath $vdir -Recurse -Force -ErrorAction SilentlyContinue
    Stop-Install 'checksum_mismatch' "$($script:CollectorBinary) changed after its SHA-256 was recorded"
  }
  Write-Log "Collector v$version installed into $vdir"
}

# Active copy in bin\ (like myriarch-updater, "copy" activation): old file -> .previous, write current-version.
# The service must be stopped (a running .exe is locked).
function Enable-CollectorVersion {
  param($L)
  $version = $script:CollectorVersion
  $verFile = Join-Path $L.Bin 'current-version'
  $current = ''
  if (Test-Path -LiteralPath $verFile) { $current = (Get-Content -LiteralPath $verFile -Raw).Trim() }
  $active = Join-Path $L.Bin $script:CollectorBinary
  if ($current -eq $version -and (Test-Path -LiteralPath $active)) { return $false }
  New-Item -ItemType Directory -Path $L.Bin -Force | Out-Null
  $src = [IO.Path]::Combine($L.Versions, $version, $script:CollectorBinary)
  $new = "$active.new"
  $marker = [IO.Path]::Combine($L.Versions, $version, '.myriarch-install.json')
  $expected = ''
  if (Test-Path -LiteralPath $marker) { $expected = [string](Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json).binaryHash }
  if (-not $expected) { $expected = Get-Sha256 -Path $src }
  Copy-VerifiedFile -Source $src -Destination $new -ExpectedHash $expected
  if (Test-Path -LiteralPath $active) {
    $prev = "$active.previous"
    if (Test-Path -LiteralPath $prev) { Remove-Item -LiteralPath $prev -Force }
    Move-Item -LiteralPath $active -Destination $prev
  }
  Move-Item -LiteralPath $new -Destination $active
  Set-FileText -Path $verFile -Text "$version`n"
  Write-Log "active version: v$version"
  return $true
}

# What to do when the supervisor file is gone: security software removes or quarantines an unsigned binary after it was written, and
# the service then fails to start with an error (1053) that names neither the file nor the cause.
function Get-SupervisorMissingHint {
  param([string]$Path)
  return "look at the protection history of your antivirus or endpoint protection for a quarantined opampsupervisor.exe (Microsoft Defender: Windows Security, Protection history, or Get-MpThreatDetection), allow the folder $(Split-Path -Parent $Path), then run the installer again (it fetches and verifies the file again)"
}

# After the installation step the supervisor must be a regular file at the path of the service and, when the file was just
# copied, the file whose SHA-256 was verified. The copy itself was hashed again (Copy-VerifiedFile); this check catches what
# happens after it, and an old file in a state that a re-run would not repair. The message names the path and the next step.
function Assert-SupervisorBinary {
  param([string]$Path, [string]$ExpectedHash)
  $next = Get-SupervisorMissingHint -Path $Path
  $item = Get-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
  if (-not $item) { Stop-Install 'supervisor_missing' "the supervisor binary does not exist: $Path; $next" }
  if ($item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    Stop-Install 'supervisor_missing' "the supervisor binary is not a regular file: $Path; $next"
  }
  if ($ExpectedHash -and (Get-Sha256 -Path $Path) -ne $ExpectedHash) {
    Stop-Install 'supervisor_missing' "the supervisor binary is not the file that was verified (its SHA-256 differs): $Path; $next"
  }
}

# The executable of a service from its registry entry (ImagePath): the quoted path at the start, or the unquoted path up to ".exe".
function Get-ServiceBinaryPath {
  param([string]$Name, [string]$Default)
  $props = Get-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\$Name" -Name ImagePath -ErrorAction SilentlyContinue
  if (-not $props -or -not $props.PSObject.Properties['ImagePath']) { return $Default }
  $raw = [string]$props.ImagePath
  if ($raw -match '^\s*"([^"]+)"') { return $Matches[1] }
  if ($raw -match '(?i)^\s*(.+?\.exe)(\s|$)') { return $Matches[1] }
  return $Default
}

# For a service that is not running: the file of its binPath has to exist, otherwise that is the reason and the error says so
# (supervisor_missing) in place of the status. Returns the path that was checked.
function Assert-ServiceBinaryPresent {
  param([string]$Name, [string]$Default)
  $path = Get-ServiceBinaryPath -Name $Name -Default $Default
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    Stop-Install 'supervisor_missing' "the service $Name is not running because the file of its binPath does not exist: $path; $(Get-SupervisorMissingHint -Path $path)"
  }
  return $path
}

function Install-SupervisorBinary {
  param($L, [string]$Arch, [string]$TmpDir)
  Assert-SupervisorPlatform -Arch $Arch
  $version = $script:SupervisorVersion
  $asset = "opampsupervisor_${version}_windows_amd64.exe"
  $url = "$($script:ReleaseBaseUrl)/cmd/opampsupervisor/v$version/$asset"
  $target = Join-Path $L.Supervisor 'opampsupervisor.exe'
  $verFile = Join-Path $L.Supervisor 'VERSION'
  if ((Test-Path -LiteralPath $target) -and (Test-Path -LiteralPath $verFile) -and ((Get-Content -LiteralPath $verFile -Raw).Trim() -eq $version)) {
    Write-Log "Supervisor v$version is already installed"
    Assert-SupervisorBinary -Path $target
    return $false
  }
  $tmp = Join-Path $TmpDir $asset
  $hash = Get-VerifiedAsset -Url $url -Destination $tmp
  Confirm-Signature -File $tmp -BundleUrl "$url.sigstore.json" -IdentityRegexp (Get-CertIdentity -Workflow 'base-binary-release' -Version $version) -Version $version -TmpDir $TmpDir
  New-Item -ItemType Directory -Path $L.Supervisor -Force | Out-Null
  Stop-ServiceAndWait -Name $script:SupervisorService
  Copy-VerifiedFile -Source $tmp -Destination $target -ExpectedHash $hash
  Set-FileText -Path $verFile -Text "$version`n"
  Assert-SupervisorBinary -Path $target -ExpectedHash $hash
  Write-Log "Supervisor v$version installed"
  return $true
}

# The CA bundle of myriarchd is copied into the protected configuration directory: the supervisor and the updater
# (SYSTEM) trust it for the connection that carries the enrollment token, so it must not stay at a location that
# other accounts can write. Result: $script:CaRendered, the path used in the configuration files.
function Install-CaFile {
  param($L)
  if (-not $CaFile) { return }
  Copy-Item -LiteralPath $CaFile -Destination (Join-Path $L.Config 'ca.pem') -Force
  $script:CaRendered = 'C:\ProgramData\Myriarch\config\ca.pem'
}

function Resolve-TemplatesDir {
  if ($TemplatesDir) { return $TemplatesDir }
  if ($PSScriptRoot) { return (Split-Path -Parent $PSScriptRoot) }
  return (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path))
}

# Fresh security descriptor of the service key: only SYSTEM and Administrators (owner Administrators). It is built
# from scratch, not from the current ACL, so that no explicit entry of an earlier state survives.
function New-ProtectedRegistrySecurity {
  $acl = New-Object System.Security.AccessControl.RegistrySecurity
  $acl.SetAccessRuleProtection($true, $false)
  $acl.SetOwner((New-Object System.Security.Principal.SecurityIdentifier($script:SidAdmins)))
  foreach ($sid in @($script:SidSystem, $script:SidAdmins)) {
    $id = New-Object System.Security.Principal.SecurityIdentifier($sid)
    $acl.AddAccessRule((New-Object System.Security.AccessControl.RegistryAccessRule($id, 'FullControl', 'ContainerInherit', 'None', 'Allow')))
  }
  return $acl
}

# SIDs that hold an allow entry (explicit or inherited) on a registry key.
function Get-RegistryAllowSids {
  param([string]$Path)
  $acl = Get-Acl -Path $Path
  return @($acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier]) |
    Where-Object { $_.AccessControlType -eq 'Allow' } | ForEach-Object { $_.IdentityReference.Value })
}

# Service environment: MYRIARCH_ENROLLMENT_TOKEN (+ existing entries are kept, e.g. OTEL_GATEWAY_TOKEN).
# Order: first replace the registry ACL by SYSTEM/Administrators only and read it back (fail-closed: any other
# allow entry stops the installation), then write - the key is readable by local users by default.
function Set-SupervisorEnvironment {
  param([string]$TokenValue)
  $key = "HKLM:\SYSTEM\CurrentControlSet\Services\$($script:SupervisorService)"
  $acl = New-ProtectedRegistrySecurity
  Set-Acl -Path $key -AclObject $acl
  $extra = @(Get-RegistryAllowSids -Path $key | Where-Object { @($script:SidSystem, $script:SidAdmins) -notcontains $_ })
  if ($extra.Count -gt 0) { Stop-Install 'acl_failed' "the service key still grants access to $($extra -join ', '): the enrollment token is not written" }
  $list = New-Object System.Collections.Generic.List[string]
  $existing = Get-ItemProperty -Path $key -Name Environment -ErrorAction SilentlyContinue
  if ($existing -and $existing.PSObject.Properties['Environment']) {
    foreach ($e in @($existing.Environment)) {
      if ($e -and -not $e.StartsWith('MYRIARCH_ENROLLMENT_TOKEN=')) { $list.Add($e) }
    }
  }
  $list.Add("MYRIARCH_ENROLLMENT_TOKEN=$TokenValue")
  Set-ItemProperty -Path $key -Name Environment -Value ([string[]]$list.ToArray()) -Type MultiString
}

function Install-SupervisorService {
  param($L)
  $name = $script:SupervisorService
  $exe = Join-Path $L.Supervisor 'opampsupervisor.exe'
  $cfg = Join-Path $L.Config 'supervisor.yaml'
  $binPath = "`"$exe`" --config `"$cfg`""
  if (-not (Test-ServiceExists -Name $name)) {
    New-Service -Name $name -BinaryPathName $binPath -DisplayName 'Myriarch Collector (OpAMP Supervisor)' `
      -Description 'OpenTelemetry Collector under the OpAMP supervisor, managed by myriarch' -StartupType Automatic | Out-Null
    Write-Log "service $name created"
  } else {
    $r = Invoke-Native -File 'sc.exe' -Arguments @('config', $name, 'binPath=', $binPath, 'start=', 'auto')
    if ($r.ExitCode -ne 0) { Stop-Install 'service_config_failed' "sc.exe config $name failed (exit $($r.ExitCode)): $($r.Output)" }
  }
  # Virtual service account instead of LocalSystem (no password needed): supervisor and collector run unprivileged
  $r = Invoke-Native -File 'sc.exe' -Arguments @('config', $name, 'obj=', (Get-ServiceAccountName))
  if ($r.ExitCode -ne 0) { Stop-Install 'service_config_failed' "sc.exe config $name obj= failed (exit $($r.ExitCode)): $($r.Output)" }
  # Recovery: restart twice after 5 s, then after 30 s; also for non-zero exit codes
  $r = Invoke-Native -File 'sc.exe' -Arguments @('failure', $name, 'reset=', '86400', 'actions=', 'restart/5000/restart/5000/restart/30000')
  if ($r.ExitCode -ne 0) { Stop-Install 'service_config_failed' "sc.exe failure $name failed (exit $($r.ExitCode)): $($r.Output)" }
  $null = Invoke-Native -File 'sc.exe' -Arguments @('failureflag', $name, '1')
}

# SHA-256 of the line "<hex>  <file name>" in a goreleaser checksums.txt; exactly one match.
function Get-ChecksumFromFile {
  param([string]$Path, [string]$Name)
  $hits = @()
  foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
    if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+?)\s*$' -and $Matches[2] -ceq $Name) { $hits += $Matches[1].ToLowerInvariant() }
  }
  if ($hits.Count -ne 1) { Stop-Install 'checksum_entry_count' "checksums.txt contains $($hits.Count) lines for $Name (expected exactly 1): -UpdaterUrl and the release do not match" }
  return [string]$hits[0]
}

# Downloads checksums.txt and its keyless Sigstore bundle, verifies the bundle against the release identity of
# the updater version (the verifier is resolved before) and returns the expected SHA-256 of the updater.
function Get-UpdaterSha256FromChecksums {
  param([string]$TmpDir)
  $asset = (($UpdaterUrl -replace '[?#].*$', '') -split '[/\\]') | Select-Object -Last 1
  if (-not $asset) { Stop-Install 'option_invalid' "no file name in -UpdaterUrl: $UpdaterUrl" }
  $sums = Join-Path $TmpDir 'checksums.txt'
  Invoke-Download -Url $UpdaterChecksumsUrl -Destination $sums
  Confirm-Signature -File $sums -BundleUrl $script:UpdaterBundle -IdentityRegexp (Get-ReleaseCertIdentity -Version $script:UpdaterResolvedVersion) -Version $script:UpdaterResolvedVersion -TmpDir $TmpDir
  return (Get-ChecksumFromFile -Path $sums -Name $asset)
}

# Authenticode signature of the updater (Windows only): valid and from the expected certificate.
function Confirm-UpdaterAuthenticode {
  param([string]$Path)
  if (-not $UpdaterSignerThumbprint) { return }
  $cmd = Get-Command Get-AuthenticodeSignature -ErrorAction SilentlyContinue
  if (-not $cmd) { Stop-Install 'tool_missing' '-UpdaterSignerThumbprint: Get-AuthenticodeSignature is not available on this system' }
  $sig = Get-AuthenticodeSignature -FilePath $Path
  if ($sig.Status -ne 'Valid') { Stop-Install 'authenticode_invalid' "the Authenticode signature of myriarch-updater is not valid (status $($sig.Status))" }
  $want = ($UpdaterSignerThumbprint -replace '\s', '').ToUpperInvariant()
  $got = $sig.SignerCertificate.Thumbprint.ToUpperInvariant()
  if ($got -ne $want) { Stop-Install 'authenticode_invalid' "the Authenticode signature of myriarch-updater comes from another certificate ($got), expected $want" }
  Write-Log 'Authenticode signature of myriarch-updater ok'
}

# Downloads the myriarch-updater and checks its SHA-256 before anything is executed (with -UpdaterSha256 and without
# -UpdaterChecksumsUrl it also serves as the Sigstore verifier for collector and supervisor). The expected hash
# comes from -UpdaterSha256 and/or the verified checksums.txt.
function Get-UpdaterDownload {
  param([string]$TmpDir)
  if (-not $WithUpdater) { return }
  $expected = ''
  if ($UpdaterSha256) { $expected = $UpdaterSha256.ToLowerInvariant() }
  if ($UpdaterChecksumsUrl) {
    $fromSums = Get-UpdaterSha256FromChecksums -TmpDir $TmpDir
    if ($expected -and $expected -ne $fromSums) {
      Stop-Install 'checksum_conflict' "-UpdaterSha256 ($expected) contradicts the signed checksums.txt ($fromSums): hash conflict"
    }
    $expected = $fromSums
  }
  $tmp = Join-Path $TmpDir 'myriarch-updater.exe'
  Invoke-Download -Url $UpdaterUrl -Destination $tmp
  if ($expected) {
    if ((Get-Sha256 -Path $tmp) -ne $expected) { Stop-Install 'checksum_mismatch' 'SHA-256 of myriarch-updater does not match' }
    Write-Log 'SHA-256 ok: myriarch-updater'
  } else {
    Write-Warn 'updater_unauthenticated' 'neither -UpdaterSha256 nor -UpdaterChecksumsUrl given (-Insecure): the updater binary is not authenticated'
  }
  Confirm-UpdaterAuthenticode -Path $tmp
  $script:UpdaterDownload = $tmp
  $script:UpdaterDownloadHash = Get-Sha256 -Path $tmp
}

function Install-UpdaterService {
  param($L, [string]$TmpDir, [bool]$Staging)
  $target = Join-Path $L.Updater 'myriarch-updater.exe'
  $tmp = $script:UpdaterDownload
  New-Item -ItemType Directory -Path $L.Updater -Force | Out-Null
  $trustedValue = ''
  if ($TrustedRoot) {
    Copy-Item -LiteralPath $TrustedRoot -Destination (Join-Path $L.Config 'trusted_root.json') -Force
    $trustedValue = 'C:\ProgramData\Myriarch\config\trusted_root.json'
  }
  $templates = Resolve-TemplatesDir
  Format-Template -TemplatePath ([IO.Path]::Combine($templates, 'updater', 'windows', 'updater.yaml')) -OutPath (Join-Path $L.Config 'updater.yaml') -Values @{
    OPAMP_ENDPOINT = $OpampEndpoint
    CA_FILE = $script:CaRendered
    TRUSTED_ROOT_FILE = $trustedValue
  }
  if ($Staging) {
    Copy-VerifiedFile -Source $tmp -Destination $target -ExpectedHash $script:UpdaterDownloadHash
    return
  }
  $existing = Test-ServiceExists -Name $script:UpdaterService
  if ($existing) { Stop-ServiceAndWait -Name $script:UpdaterService -TimeoutSeconds 300 }
  Copy-VerifiedFile -Source $tmp -Destination $target -ExpectedHash $script:UpdaterDownloadHash
  if (-not $existing) {
    $r = Invoke-Native -File $target -Arguments @('install', '--config', (Join-Path $L.Config 'updater.yaml'))
    if ($r.ExitCode -ne 0) { Stop-Install 'updater_install_failed' "myriarch-updater install failed (exit $($r.ExitCode)): $($r.Output)" }
  }
  Start-Service -Name $script:UpdaterService
  Write-Log 'myriarch-updater installed and started'
}

function Invoke-Uninstall {
  param($L, [bool]$Staging)
  if (-not $Staging) {
    foreach ($n in @($script:UpdaterService, $script:SupervisorService)) {
      if (Test-ServiceExists -Name $n) {
        Stop-ServiceAndWait -Name $n -TimeoutSeconds 120
        $null = Invoke-Native -File 'sc.exe' -Arguments @('delete', $n)
      }
    }
  }
  foreach ($d in @($L.ProgramFiles, $L.Config, $L.Logs)) {
    if (Test-Path -LiteralPath $d) { Remove-Item -LiteralPath $d -Recurse -Force }
  }
  if ($Purge) {
    foreach ($d in @($L.SupState, $L.Storage, $L.UpdaterState, $L.ProgramData)) {
      if (Test-Path -LiteralPath $d) { Remove-Item -LiteralPath $d -Recurse -Force }
    }
  } else {
    Write-Log "data under $($L.ProgramData) (supervisor identity, queue) is kept (-Purge removes it)"
  }
  Write-Log 'uninstalled'
}

function Invoke-Main {
  Assert-FullLanguageMode
  if ($script:BoundParameters.ContainsKey('SigningKey')) {
    Stop-Install 'option_removed' '-SigningKey was removed: releases are signed keyless. Pass -UpdaterChecksumsUrl (its Sigstore bundle is verified against the release identity, verifier: cosign 3.0.4 or newer or an installed myriarch-updater) or -UpdaterSha256'
  }
  if ($script:BoundParameters.ContainsKey('UpdaterChecksumsSigUrl')) {
    Stop-Install 'option_removed' '-UpdaterChecksumsSigUrl was removed: use -UpdaterBundleUrl (default: <checksums-url>.sigstore.json) with -UpdaterChecksumsUrl'
  }
  $staging = [bool]$Root
  $script:StagingMode = $staging
  $L = Get-Layout -Prefix $Root
  if (-not $staging -and -not (Test-Administrator)) { Stop-Install 'privilege_required' 'run as Administrator' }
  if ($Uninstall) { Invoke-Uninstall -L $L -Staging $staging; return }

  $script:OpampEndpoint = $OpampEndpoint
  Test-Inputs
  $arch = Resolve-Arch
  Assert-SupervisorPlatform -Arch $arch
  $templates = Resolve-TemplatesDir
  if (-not (Test-Path -LiteralPath ([IO.Path]::Combine($templates, 'supervisor', 'windows')))) {
    Stop-Install 'template_missing' "templates not found under $templates (-TemplatesDir)"
  }
  # Before the work directory, a download, a verifier or the stop of a service
  Assert-NoConflictingInstall
  $tmpDir = New-WorkDirectory -Staging $staging
  try {
    Write-Log "installing: collector v$($script:CollectorVersion), supervisor v$($script:SupervisorVersion), windows/$arch, site $SiteType/$SiteId"
    # Authenticated updater + Sigstore verifier before any download/execution of collector/supervisor. With a
    # checksums.txt the verifier comes first (it checks the file that carries the hash of the updater).
    if ($WithUpdater -and $UpdaterChecksumsUrl) {
      Resolve-Verifier -L $L
      Get-UpdaterDownload -TmpDir $tmpDir
    } else {
      Get-UpdaterDownload -TmpDir $tmpDir
      Resolve-Verifier -L $L
    }
    # Stop a running supervisor before ACLs are reset (it is started again below)
    if (-not $staging) { Stop-ServiceAndWait -Name $script:SupervisorService }
    foreach ($d in @($L.ProgramFiles, $L.Collector, $L.Versions, $L.Bin, $L.Supervisor, $L.Updater)) {
      New-Item -ItemType Directory -Path $d -Force | Out-Null
    }
    # Data directories: never adopt a foreign directory; create the supervisor storage empty only
    Initialize-DataDirectories -L $L -Staging $staging

    Install-Collector -L $L -Arch $arch -TmpDir $tmpDir
    $supervisorChanged = Install-SupervisorBinary -L $L -Arch $arch -TmpDir $tmpDir

    # Configuration
    Install-CaFile -L $L
    $supYaml = Join-Path $L.Config 'supervisor.yaml'
    $newYaml = Join-Path $tmpDir 'supervisor.yaml'
    Format-Template -TemplatePath ([IO.Path]::Combine($templates, 'supervisor', 'windows', 'supervisor.yaml')) -OutPath $newYaml -Values @{
      OPAMP_ENDPOINT = $OpampEndpoint
      CA_FILE = $script:CaRendered
      SITE_TYPE = $SiteType
      SITE_ID = $SiteId
      SITE_NAME = $SiteName
      SITE_COUNTRY = $SiteCountry
      SITE_REGION = $SiteRegion
      SITE_TIMEZONE = $SiteTimezone
      ENVIRONMENT = $Environment
    }
    $configChanged = Copy-FileIfDifferent -Source $newYaml -Destination $supYaml
    $fallback = Join-Path $L.Config 'fallback.yaml'
    if (-not (Test-Path -LiteralPath $fallback)) {
      Copy-Item -LiteralPath ([IO.Path]::Combine($templates, 'supervisor', 'windows', 'fallback.yaml')) -Destination $fallback
      Write-Log 'fallback.yaml created'
    }
    Set-FileText -Path (Join-Path $L.Config 'enrollment-token') -Text ($script:EffectiveToken + "`n")

    if ($staging) {
      Write-Log 'staging mode: services, registry and ACLs are not touched'
      if ($WithUpdater) { Install-UpdaterService -L $L -TmpDir $tmpDir -Staging $true }
      Copy-Item -LiteralPath ([IO.Path]::Combine($L.Versions, $script:CollectorVersion, $script:CollectorBinary)) -Destination (Join-Path $L.Bin $script:CollectorBinary) -Force
      Set-FileText -Path (Join-Path $L.Bin 'current-version') -Text "$($script:CollectorVersion)`n"
      return
    }

    # Service (stop before the locked .exe is replaced)
    Install-SupervisorService -L $L
    $svcSid = Grant-ServiceAccountAccess -L $L
    Assert-TreeAcl -Path $L.ProgramData -AllowedSids @($script:SidSystem, $script:SidAdmins, $svcSid)
    Stop-ServiceAndWait -Name $script:SupervisorService
    $collectorChanged = Enable-CollectorVersion -L $L
    Set-SupervisorEnvironment -TokenValue $script:EffectiveToken
    Start-Service -Name $script:SupervisorService
    if ($supervisorChanged -or $configChanged -or $collectorChanged) { Write-Log 'otelcol-supervisor (re)started' }

    if ($WithUpdater) {
      Install-UpdaterService -L $L -TmpDir $tmpDir -Staging $false
      Assert-TreeAcl -Path $L.ProgramData -AllowedSids @($script:SidSystem, $script:SidAdmins, $svcSid)
    }

    Start-Sleep -Seconds 2
    $svc = Get-Service -Name $script:SupervisorService
    if ($svc.Status -ne 'Running') {
      # a binary that has gone (security software) explains the status better than the status does
      $binary = Assert-ServiceBinaryPresent -Name $script:SupervisorService -Default (Join-Path $L.Supervisor 'opampsupervisor.exe')
      Write-Warn 'service_not_running' "otelcol-supervisor is not running (status $($svc.Status), its binary exists: $binary); Event Viewer -> Windows Logs -> Application, source otelcol-supervisor"
      Stop-Install 'service_not_running' 'the service otelcol-supervisor is not running'
    }
    Write-Log "done. The host reports to $OpampEndpoint as an agent"
  } finally {
    if (Test-Path -LiteralPath $tmpDir) { Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue }
  }
}

# Do not run when the script is dot-sourced (tests of the helper functions).
if ($MyInvocation.InvocationName -ne '.') {
  try {
    Invoke-Main
  } catch {
    # Fatal errors carry a code ("[<code>] <message>", see Stop-Install); anything else is unexpected.
    $msg = $_.Exception.Message
    if ($msg -notmatch '^\[[a-z][a-z0-9_]*\] ') { $msg = "[unexpected_error] $msg" }
    if ($ExecutionContext.SessionState.LanguageMode -eq 'FullLanguage') {
      # Through the host (not [Console]::Error): the line also reaches callers in PowerShell remoting and ISE;
      # in a console host it still goes to stderr.
      $Host.UI.WriteErrorLine("[myriarch-install] ERROR $msg")
    } else {
      # A restricted language mode refuses a method call on the host, and language_mode_unsupported is the error that gets here
      # in it: Write-Host prints the same line without one (on the console, not on stderr).
      Write-Host "[myriarch-install] ERROR $msg"
    }
    exit 1
  }
}
