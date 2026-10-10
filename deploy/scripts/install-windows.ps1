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
  bundle (-VerifySignatures require, the default; verifier: -Verifier myriarch-updater.exe or cosign.exe or the
  updater downloaded with -WithUpdater and checked by -UpdaterSha256). With -WithUpdater the myriarch-updater
  (the project's own binary, runs as SYSTEM) needs a trusted hash source, at least one of
    -UpdaterSha256 HEX                                      (hash from another trusted source)
    -UpdaterChecksumsUrl URL + -SigningKey cosign.pub       (checksums.txt of the release; its signature URL.sig - base64 of
                                                             an ASN.1 DER ECDSA signature (P-256) over SHA-256 - is verified
                                                             with the public key, without external tools; the hash is the
                                                             line whose file name matches the one in -UpdaterUrl)
  Both together are allowed (then both must match); the signature check also applies with -Insecure. Optional
  -UpdaterSignerThumbprint: the Authenticode signature of the updater must be valid and come from this certificate.
  -Insecure disables the remaining checks for lab/tests only (http/file, no bundle, ws://).
  The directories under C:\ProgramData\Myriarch are never "adopted": if an existing directory is not owned by
  SYSTEM/Administrators or is a reparse point, the installation aborts; otherwise the ACL of the whole tree is
  reset and verified.

  Do NOT use the contrib MSI: it installs a service of its own, "otelcol-contrib", which starts immediately.
  The supervisor storage directory (C:\ProgramData\Myriarch\supervisor) is only created empty:
  persistent_state.yaml holds the instance UID - golden images must empty it before cloning.

  Errors: every fatal error prints exactly one line to stderr and exits with status 1:
    [myriarch-install] ERROR [<code>] <message>
  Warnings: [myriarch-install] [<code>] <message> (PowerShell prefixes "WARNING: "). The codes are a stable
  interface shared with install-linux.sh; see the catalog at the top of that script and the Windows-only
  codes listed below the parameter block here.

.EXAMPLE
  .\install-windows.ps1 -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile C:\Temp\enrollment-token.txt `
      -SiteType datacenter -SiteId dc-01 -SiteName "Data center 1" -SiteCountry NL -SiteRegion north `
      -SiteTimezone Europe/Amsterdam -CollectorVersion 0.162.0 -WithUpdater `
      -UpdaterUrl https://fleet.example.com/downloads/myriarch-updater_windows_amd64.exe -UpdaterSha256 <hex>
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
  [string]$UpdaterChecksumsUrl,
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
# SIDs instead of names (language independent)
$script:SidSystem = 'S-1-5-18'
$script:SidAdmins = 'S-1-5-32-544'
$script:SidTrustedInstaller = 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464'
$script:CertIssuer = 'https://token.actions.githubusercontent.com'
$script:VerifierPath = ''
$script:UpdaterDownload = ''
$script:UpdaterDownloadHash = ''
$script:CaRendered = ''
$script:UpdaterChecksumsSigUrl = ''

# ---- Error codes (stable interface) -------------------------------------------------------------------
# The full catalog with the shared codes (usage_error, option_missing, option_invalid, option_conflict,
# updater_hash_source_missing, insecure_required, https_required, file_unreadable, tool_missing,
# sigstore_verifier_missing, signature_verifier_missing, privilege_required, unsupported_platform,
# download_failed, template_missing, template_placeholder, checksum_invalid, checksum_mismatch,
# checksum_entry_count, checksum_conflict, signature_malformed, signature_invalid,
# sigstore_verification_failed, archive_invalid, collector_start_failed, collector_version_mismatch,
# service_not_running, token_on_command_line, updater_unauthenticated) is at the top of install-linux.sh.
# Windows-only codes:
#   signing_key_invalid       the -SigningKey file is not PEM / not an ECDSA P-256 public key
#   authenticode_invalid      the Authenticode signature of the updater is invalid or from another certificate
#   directory_unsafe          reparse point, wrong owner, unexpected ACE, or not a directory
#   acl_failed                icacls failed
#   service_config_failed     sc.exe failed
#   updater_install_failed    "myriarch-updater install" failed
#   group_membership_failed   WARNING: the service account could not be added to a local group
#   unexpected_error          a failure that is not classified above (the original message follows the code)
# service_not_running is fatal here (a warning on Linux).
# Every fatal error goes through Stop-Install; the top-level catch prints
#   [myriarch-install] ERROR [<code>] <message>

function Write-Log { param([string]$Message) Write-Host "[myriarch-install] $Message" }
# Write-Warn <code> <message>: PowerShell adds the "WARNING: " prefix itself.
function Write-Warn { param([string]$Code, [string]$Message) Write-Warning "[myriarch-install] [$Code] $Message" }

# Stop-Install <code> <message>: the only way to raise a fatal error (see the error code catalog above).
function Stop-Install { param([string]$Code, [string]$Message) throw "[$Code] $Message" }

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
      # a refused redirect is final (matched by its code, not by message text); everything else is retried
      if ($_.Exception.Message.Contains('[https_required]')) { throw }
      if ($attempt -ge 3) { Stop-Install 'download_failed' "download failed: $Url ($($_.Exception.Message))" }
      Start-Sleep -Seconds (2 * $attempt)
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

# Sigstore verifier, in this order: -Verifier, the SHA-256 checked updater (-WithUpdater), an already installed
# myriarch-updater, cosign.exe from PATH - but only from a directory that unprivileged accounts cannot write
# (PATH may contain such directories, and this script runs elevated). Without a verifier the installation aborts.
function Resolve-Verifier {
  param($L)
  if ($VerifySignatures -eq 'skip') { return }
  if ($Verifier) { $script:VerifierPath = $Verifier; return }
  if ($script:UpdaterDownload) { $script:VerifierPath = $script:UpdaterDownload; return }
  $installed = Join-Path $L.Updater 'myriarch-updater.exe'
  if (Test-Path -LiteralPath $installed) { $script:VerifierPath = $installed; return }
  $cosign = Get-Command cosign.exe -ErrorAction SilentlyContinue
  if ($cosign -and (Test-AdminOnlyDirectory -Path (Split-Path -Parent $cosign.Source))) { $script:VerifierPath = $cosign.Source; return }
  if ($cosign) { Write-Warn 'sigstore_verifier_missing' "cosign.exe found in $(Split-Path -Parent $cosign.Source) is not used: the directory can be changed by unprivileged accounts; pass -Verifier or use -WithUpdater" }
  Stop-Install 'sigstore_verifier_missing' 'no Sigstore verifier found: pass -Verifier <myriarch-updater.exe or cosign.exe>, use -WithUpdater with -UpdaterSha256, or install cosign.exe (v3) into a directory only Administrators can write'
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
  $verRe = '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
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
  if (-not $WithUpdater -and ($UpdaterChecksumsUrl -or $UpdaterChecksumsSigUrl -or $SigningKey -or $UpdaterSignerThumbprint)) {
    Stop-Install 'option_conflict' '-UpdaterChecksumsUrl/-UpdaterChecksumsSigUrl/-SigningKey/-UpdaterSignerThumbprint only apply together with -WithUpdater'
  }
  if ($WithUpdater) {
    if (-not $UpdaterUrl) { Stop-Install 'option_missing' '-WithUpdater requires -UpdaterUrl' }
    Assert-TransportAllowed -Url $UpdaterUrl -What '-UpdaterUrl'
    if ($UpdaterChecksumsSigUrl -and -not $UpdaterChecksumsUrl) { Stop-Install 'option_conflict' '-UpdaterChecksumsSigUrl requires -UpdaterChecksumsUrl' }
    if ($UpdaterChecksumsUrl) {
      if (-not $SigningKey) { Stop-Install 'option_conflict' '-UpdaterChecksumsUrl requires -SigningKey (public key cosign.pub): without a signature check checksums.txt is not trustworthy' }
      Assert-TransportAllowed -Url $UpdaterChecksumsUrl -What '-UpdaterChecksumsUrl'
      if (-not $UpdaterChecksumsSigUrl) { $script:UpdaterChecksumsSigUrl = "$UpdaterChecksumsUrl.sig" } else { $script:UpdaterChecksumsSigUrl = $UpdaterChecksumsSigUrl }
      Assert-TransportAllowed -Url $script:UpdaterChecksumsSigUrl -What '-UpdaterChecksumsSigUrl'
    } elseif ($SigningKey) {
      Stop-Install 'option_conflict' '-SigningKey requires -UpdaterChecksumsUrl'
    }
    if ($SigningKey -and -not (Test-Path -LiteralPath $SigningKey)) { Stop-Install 'file_unreadable' "signing key file is not readable: $SigningKey" }
    if (-not $UpdaterSha256 -and -not $UpdaterChecksumsUrl -and -not $Insecure) {
      Stop-Install 'updater_hash_source_missing' '-WithUpdater requires -UpdaterSha256 or (-UpdaterChecksumsUrl and -SigningKey) - the binary runs as SYSTEM and needs a trusted hash source (signed checksums.txt of the release)'
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

function Install-SupervisorBinary {
  param($L, [string]$Arch, [string]$TmpDir)
  if ($Arch -ne 'amd64') {
    Stop-Install 'unsupported_platform' 'upstream publishes only opampsupervisor_<v>_windows_amd64.exe for Windows; on ARM64 install with -Arch amd64 (x64 emulation)'
  }
  $version = $script:SupervisorVersion
  $asset = "opampsupervisor_${version}_windows_amd64.exe"
  $url = "$($script:ReleaseBaseUrl)/cmd/opampsupervisor/v$version/$asset"
  $target = Join-Path $L.Supervisor 'opampsupervisor.exe'
  $verFile = Join-Path $L.Supervisor 'VERSION'
  if ((Test-Path -LiteralPath $target) -and (Test-Path -LiteralPath $verFile) -and ((Get-Content -LiteralPath $verFile -Raw).Trim() -eq $version)) {
    Write-Log "Supervisor v$version is already installed"
    return $false
  }
  $tmp = Join-Path $TmpDir $asset
  $hash = Get-VerifiedAsset -Url $url -Destination $tmp
  Confirm-Signature -File $tmp -BundleUrl "$url.sigstore.json" -IdentityRegexp (Get-CertIdentity -Workflow 'base-binary-release' -Version $version) -Version $version -TmpDir $TmpDir
  New-Item -ItemType Directory -Path $L.Supervisor -Force | Out-Null
  Stop-ServiceAndWait -Name $script:SupervisorService
  Copy-VerifiedFile -Source $tmp -Destination $target -ExpectedHash $hash
  Set-FileText -Path $verFile -Text "$version`n"
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

# --- Signature verification of our releases (ECDSA P-256, without external tools) --------------------------
# <file>.sig holds the base64 encoding of an ASN.1 DER ECDSA signature over the SHA-256 of the file
# (cosign sign-blob without transparency log; also verifiable with openssl).

# DER signature (SEQUENCE { INTEGER r, INTEGER s }) -> r||s with 32 bytes each (IEEE P1363), as .NET expects it.
function ConvertFrom-DerEcdsaSignature {
  param([byte[]]$Der)
  $bad = 'the signature is not a valid ASN.1 DER ECDSA signature (P-256)'
  if ($Der.Length -lt 8 -or $Der[0] -ne 0x30) { Stop-Install 'signature_malformed' $bad }
  $i = 2
  if (($Der[1] -band 0x80) -ne 0) { $i = 2 + ($Der[1] -band 0x7f) }
  $rs = New-Object byte[] 64
  for ($k = 0; $k -lt 2; $k++) {
    if ($i + 1 -ge $Der.Length -or $Der[$i] -ne 0x02) { Stop-Install 'signature_malformed' $bad }
    $l = [int]$Der[$i + 1]
    $i += 2
    if ($i + $l -gt $Der.Length) { Stop-Install 'signature_malformed' $bad }
    $start = $i
    $count = $l
    while ($count -gt 0 -and $Der[$start] -eq 0) { $start++; $count-- }
    if ($count -gt 32 -or $count -lt 1) { Stop-Install 'signature_malformed' $bad }
    [Array]::Copy($Der, $start, $rs, ($k * 32) + (32 - $count), $count)
    $i += $l
  }
  return , $rs
}

# PEM "PUBLIC KEY" (P-256, SubjectPublicKeyInfo) -> ECDsa object for verification.
# .NET Core / .NET Framework >= 4.7: ECDsa.Create(ECParameters); older (Windows PowerShell 5.1 with .NET 4.6.x):
# ECDsaCng via a CNG public blob (ECS1 magic, length 32, X, Y).
function New-EcdsaVerifier {
  param([string]$PemPath)
  $pem = [System.IO.File]::ReadAllText($PemPath)
  $b64 = (($pem -split "`r?`n") | Where-Object { $_ -and $_ -notmatch '^-----' }) -join ''
  try { $spki = [Convert]::FromBase64String($b64) } catch { Stop-Install 'signing_key_invalid' "key file is not PEM: $PemPath" }
  $prefix = [byte[]](0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00)
  $ok = ($spki.Length -eq 91) -and ($spki[26] -eq 0x04)
  if ($ok) { for ($n = 0; $n -lt $prefix.Length; $n++) { if ($spki[$n] -ne $prefix[$n]) { $ok = $false; break } } }
  if (-not $ok) { Stop-Install 'signing_key_invalid' "the key is not an ECDSA P-256 public key (SubjectPublicKeyInfo): $PemPath" }
  $x = New-Object byte[] 32
  $y = New-Object byte[] 32
  [Array]::Copy($spki, 27, $x, 0, 32)
  [Array]::Copy($spki, 59, $y, 0, 32)
  try {
    $params = New-Object System.Security.Cryptography.ECParameters
    $params.Curve = [System.Security.Cryptography.ECCurve]::CreateFromValue('1.2.840.10045.3.1.7')  # nistP256
    $point = New-Object System.Security.Cryptography.ECPoint
    $point.X = $x
    $point.Y = $y
    $params.Q = $point
    return [System.Security.Cryptography.ECDsa]::Create($params)
  } catch {
    $firstError = $_.Exception.Message
    try {
      $blob = [byte[]](0x45, 0x43, 0x53, 0x31, 0x20, 0x00, 0x00, 0x00) + $x + $y
      $cng = [System.Security.Cryptography.CngKey]::Import($blob, [System.Security.Cryptography.CngKeyBlobFormat]::EccPublicBlob)
      return (New-Object System.Security.Cryptography.ECDsaCng($cng))
    } catch {
      Stop-Install 'signature_verifier_missing' "ECDSA verification is not possible on this system: $firstError / $($_.Exception.Message)"
    }
  }
}

function Confirm-BlobSignature {
  param([string]$File, [string]$SigFile, [string]$KeyFile)
  $name = Split-Path -Leaf $File
  $sigText = ([System.IO.File]::ReadAllText($SigFile)).Trim()
  try { $der = [Convert]::FromBase64String($sigText) } catch { Stop-Install 'signature_malformed' "signature of $name is not valid base64" }
  $rs = ConvertFrom-DerEcdsaSignature -Der $der
  $ecdsa = New-EcdsaVerifier -PemPath $KeyFile
  $sha = [System.Security.Cryptography.SHA256]::Create()
  $stream = [System.IO.File]::OpenRead($File)
  try { $hash = $sha.ComputeHash($stream) } finally { $stream.Dispose(); $sha.Dispose() }
  $valid = $false
  try { $valid = $ecdsa.VerifyHash($hash, $rs) } finally { $ecdsa.Dispose() }
  if (-not $valid) { Stop-Install 'signature_invalid' "signature of $name is invalid (wrong key or tampered file)" }
  Write-Log "signature of $name ok"
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

# Downloads and verifies checksums.txt (+ signature) and returns the expected SHA-256 of the updater.
function Get-UpdaterSha256FromChecksums {
  param([string]$TmpDir)
  $asset = (($UpdaterUrl -replace '[?#].*$', '') -split '[/\\]') | Select-Object -Last 1
  if (-not $asset) { Stop-Install 'option_invalid' "no file name in -UpdaterUrl: $UpdaterUrl" }
  $sums = Join-Path $TmpDir 'checksums.txt'
  $sig = Join-Path $TmpDir 'checksums.txt.sig'
  Invoke-Download -Url $UpdaterChecksumsUrl -Destination $sums
  Invoke-Download -Url $script:UpdaterChecksumsSigUrl -Destination $sig
  Confirm-BlobSignature -File $sums -SigFile $sig -KeyFile $SigningKey
  return (Get-ChecksumFromFile -Path $sums -Name $asset)
}

# Authenticode signature of the updater (Windows only): valid and from the expected certificate.
function Confirm-UpdaterAuthenticode {
  param([string]$Path)
  if (-not $UpdaterSignerThumbprint) { return }
  $cmd = Get-Command Get-AuthenticodeSignature -ErrorAction SilentlyContinue
  if (-not $cmd) { Stop-Install 'signature_verifier_missing' '-UpdaterSignerThumbprint: Get-AuthenticodeSignature is not available on this system' }
  $sig = Get-AuthenticodeSignature -FilePath $Path
  if ($sig.Status -ne 'Valid') { Stop-Install 'authenticode_invalid' "the Authenticode signature of myriarch-updater is not valid (status $($sig.Status))" }
  $want = ($UpdaterSignerThumbprint -replace '\s', '').ToUpperInvariant()
  $got = $sig.SignerCertificate.Thumbprint.ToUpperInvariant()
  if ($got -ne $want) { Stop-Install 'authenticode_invalid' "the Authenticode signature of myriarch-updater comes from another certificate ($got), expected $want" }
  Write-Log 'Authenticode signature of myriarch-updater ok'
}

# Downloads the myriarch-updater and checks its SHA-256 before anything is executed (it also serves as the
# Sigstore verifier for collector and supervisor). The expected hash comes from -UpdaterSha256 and/or the
# signed checksums.txt.
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
  $staging = [bool]$Root
  $script:StagingMode = $staging
  $L = Get-Layout -Prefix $Root
  if (-not $staging -and -not (Test-Administrator)) { Stop-Install 'privilege_required' 'run as Administrator' }
  if ($Uninstall) { Invoke-Uninstall -L $L -Staging $staging; return }

  $script:OpampEndpoint = $OpampEndpoint
  Test-Inputs
  $arch = Resolve-Arch
  $templates = Resolve-TemplatesDir
  if (-not (Test-Path -LiteralPath ([IO.Path]::Combine($templates, 'supervisor', 'windows')))) {
    Stop-Install 'template_missing' "templates not found under $templates (-TemplatesDir)"
  }
  $tmpDir = New-WorkDirectory -Staging $staging
  try {
    Write-Log "installing: collector v$($script:CollectorVersion), supervisor v$($script:SupervisorVersion), windows/$arch, site $SiteType/$SiteId"
    # Authenticated updater + Sigstore verifier before any download/execution of collector/supervisor
    Get-UpdaterDownload -TmpDir $tmpDir
    Resolve-Verifier -L $L
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
      Write-Warn 'service_not_running' "otelcol-supervisor is not running (status $($svc.Status)); Event Viewer -> Windows Logs -> Application, source otelcol-supervisor"
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
    # Through the host (not [Console]::Error): the line also reaches callers in PowerShell remoting and ISE;
    # in a console host it still goes to stderr.
    $Host.UI.WriteErrorLine("[myriarch-install] ERROR $msg")
    exit 1
  }
}
