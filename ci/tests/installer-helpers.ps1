# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of helper functions of deploy/scripts/install-windows.ps1 (dot-sourced; the script does not run its main code
# then): the identity pattern bound to the version, the verifier order, the re-hash after a copy and the fail-closed
# registry ACL of the service key. Windows-only APIs are replaced by functions, so the test also runs with PowerShell on
# Linux and macOS. Called by ci/tests/installer-verify.ps1 in a child process (the dot-sourced script defines variables).
param([Parameter(Mandatory)][string]$Installer)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
. $Installer
$ErrorActionPreference = 'Stop'

$script:failures = 0
function Check { param([string]$Title, [bool]$Ok) if ($Ok) { Write-Host "  ok    $Title" } else { $script:failures++; Write-Host "  FAIL  $Title" } }
function Message-Of { param([scriptblock]$Block) try { & $Block; return '' } catch { return $_.Exception.Message } }

# ---- Identity pattern bound to the requested version ---------------------------------------------------
$id = Get-CertIdentity -Workflow 'base-release' -Version '0.161.0'
$base = 'https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/'
Check 'the identity matches the tag of the requested version' ([regex]::IsMatch($base + 'v0.161.0', $id))
Check 'the identity does not match another tag' (-not [regex]::IsMatch($base + 'v0.160.0', $id))
Check 'the identity is not a prefix pattern (v0.1610, v10.161.0)' (-not [regex]::IsMatch($base + 'v0.1610', $id) -and -not [regex]::IsMatch($base + 'v10.161.0', $id))
Check 'the identity ends with the escaped tag and an end anchor' ($id.EndsWith('@refs/tags/v0\.161\.0$'))
$sid = Get-CertIdentity -Workflow 'base-binary-release' -Version '0.160.0'
Check 'the supervisor identity uses the binary release workflow' ($sid.Contains('/base-binary-release\.yaml@refs/tags/v0\.160\.0$'))

# ---- Verifier order ---------------------------------------------------------------------------------------
$tmp = Join-Path ([IO.Path]::GetTempPath()) ('myriarch-helpers-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
$cosignDir = Join-Path $tmp 'cosign-bin'
New-Item -ItemType Directory -Path $cosignDir | Out-Null
Set-Content -LiteralPath (Join-Path $cosignDir 'cosign.exe') -Value "#!/bin/sh`nexit 0`n" -NoNewline
& chmod 755 (Join-Path $cosignDir 'cosign.exe')
$env:PATH = $cosignDir + [IO.Path]::PathSeparator + $env:PATH
$L = @{ Updater = Join-Path $tmp 'no-installed-updater' }
$script:VerifierPath = ''
$script:UpdaterDownload = Join-Path $tmp 'myriarch-updater.exe'
Resolve-Verifier -L $L
Check 'the checked updater is chosen although cosign.exe is in a user-writable PATH directory' ($script:VerifierPath -eq $script:UpdaterDownload)
$script:VerifierPath = ''
$script:UpdaterDownload = ''
$msg = Message-Of { Resolve-Verifier -L $L }
Check 'cosign.exe from a directory that unprivileged accounts can write is not used' ($msg -match '\[sigstore_verifier_missing\]' -and $script:VerifierPath -eq '')

# ---- Copy with re-hash ---------------------------------------------------------------------------------------
$src = Join-Path $tmp 'src.bin'
Set-Content -LiteralPath $src -Value 'payload' -NoNewline
$good = Get-Sha256 -Path $src
$dst = Join-Path $tmp 'dst.bin'
Copy-VerifiedFile -Source $src -Destination $dst -ExpectedHash $good
Check 'a file with the verified hash is copied' (Test-Path -LiteralPath $dst)
Remove-Item -LiteralPath $dst
$msg = Message-Of { Copy-VerifiedFile -Source $src -Destination $dst -ExpectedHash ('0' * 64) }
Check 'a file that changed after the check stops the installation with checksum_mismatch' ($msg -match '\[checksum_mismatch\]')
Check '... and the copy is removed' (-not (Test-Path -LiteralPath $dst))

# ---- Registry ACL of the service key -----------------------------------------------------------------------------
$script:wrote = $false
$script:allowSids = @('S-1-5-18', 'S-1-5-32-544', 'S-1-5-32-545')   # SYSTEM, Administrators, BUILTIN\Users
function New-ProtectedRegistrySecurity { return 'acl' }
function Set-Acl { param($Path, $AclObject) }
function Get-RegistryAllowSids { param($Path) return $script:allowSids }
function Get-ItemProperty { param($Path, $Name, $ErrorAction) return $null }
function Set-ItemProperty { param($Path, $Name, $Value, $Type) $script:wrote = $true }
$msg = Message-Of { Set-SupervisorEnvironment -TokenValue 'myre_token' }
Check 'an explicit entry for BUILTIN\Users on the service key stops the installation with acl_failed' ($msg -match '\[acl_failed\]')
Check '... before the token is written' (-not $script:wrote)
$script:allowSids = @('S-1-5-18', 'S-1-5-32-544')
$msg = Message-Of { Set-SupervisorEnvironment -TokenValue 'myre_token' }
Check 'with SYSTEM and Administrators only the token is written' ($msg -eq '' -and $script:wrote)

Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
if ($script:failures -gt 0) { exit 1 }
