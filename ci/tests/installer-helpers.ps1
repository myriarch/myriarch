# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of helper functions of deploy/scripts/install-windows.ps1 (dot-sourced; the script does not run its main code
# then): the identity patterns bound to the version, the verifier order and the cosign version floor, the re-hash after a
# copy and the fail-closed registry ACL of the service key. Windows-only APIs are replaced by functions, so the test also runs with PowerShell on
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

# ---- Release identity (checksums.txt of a Myriarch release) bound to the updater version -----------------------
$rid = Get-ReleaseCertIdentity -Version '0.1.0-rc.1'
$rbase = 'https://github.com/myriarch/myriarch/.github/workflows/'
Check 'the release identity is exact: release.yml of myriarch/myriarch at the tag, dots escaped, anchored' ($rid -ceq '^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.1\.0-rc\.1$')
Check 'the release identity matches the tag of the version' ([regex]::IsMatch($rbase + 'release.yml@refs/tags/v0.1.0-rc.1', $rid))
Check 'the release identity does not match another tag, a longer tag or a dot replaced by another character' (
  -not [regex]::IsMatch($rbase + 'release.yml@refs/tags/v0.1.0-rc.10', $rid) -and -not [regex]::IsMatch($rbase + 'release.yml@refs/tags/v0.1.0', $rid) -and
  -not [regex]::IsMatch($rbase + 'release.yml@refs/tags/v0x1x0-rc.1', $rid))
Check 'the release identity does not match another workflow, repository, fork or branch' (
  -not [regex]::IsMatch($rbase + 'ci.yml@refs/tags/v0.1.0-rc.1', $rid) -and
  -not [regex]::IsMatch('https://github.com/evil/myriarch/.github/workflows/release.yml@refs/tags/v0.1.0-rc.1', $rid) -and
  -not [regex]::IsMatch('https://github.com/myriarch/myriarch-fork/.github/workflows/release.yml@refs/tags/v0.1.0-rc.1', $rid) -and
  -not [regex]::IsMatch($rbase + 'release.yml@refs/heads/main', $rid))

# ---- Verifier order and the cosign version floor -----------------------------------------------------------
$tmp = Join-Path ([IO.Path]::GetTempPath()) ('myriarch-helpers-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
function New-CosignStub { param([string]$Dir, [string]$VersionLine)
  New-Item -ItemType Directory -Path $Dir -Force | Out-Null
  Set-Content -LiteralPath (Join-Path $Dir 'cosign.exe') -Value "#!/bin/sh`nif [ `"`$1`" = version ]; then echo '$VersionLine'; fi`nexit 0`n" -NoNewline
  & chmod 755 (Join-Path $Dir 'cosign.exe')
}
$origPath = $env:PATH
$L = @{ Updater = Join-Path $tmp 'no-installed-updater' }
$downloaded = Join-Path $tmp 'myriarch-updater.exe'
$fresh = { $script:VerifierPath = ''; $script:UpdaterDownload = ''; $script:Verifier = ''; $script:UpdaterSha256 = ''; $script:UpdaterChecksumsUrl = ''; $script:VerifySignatures = 'require' }

# cosign.exe in a directory that unprivileged accounts can write is never used (here: every directory on POSIX)
New-CosignStub -Dir (Join-Path $tmp 'cosign-ok') -VersionLine 'GitVersion:    v3.1.3'
$env:PATH = (Join-Path $tmp 'cosign-ok') + [IO.Path]::PathSeparator + $origPath
& $fresh
$msg = Message-Of { Resolve-Verifier -L $L }
Check 'cosign.exe from a directory that unprivileged accounts can write is not used' ($msg -match '\[sigstore_verifier_missing\]' -and $script:VerifierPath -eq '')

# the SHA-256 checked updater counts only with -UpdaterSha256 and without -UpdaterChecksumsUrl
& $fresh; $script:UpdaterDownload = $downloaded; $script:UpdaterSha256 = ('a' * 64)
Resolve-Verifier -L $L
Check 'the hash-checked updater is the verifier with -UpdaterSha256 (although cosign.exe is in a user-writable PATH directory)' ($script:VerifierPath -eq $downloaded)
& $fresh; $script:UpdaterDownload = $downloaded; $script:UpdaterSha256 = ('a' * 64); $script:UpdaterChecksumsUrl = 'https://example.com/checksums.txt'
$msg = Message-Of { Resolve-Verifier -L $L }
Check 'with -UpdaterChecksumsUrl the downloaded updater is never the verifier' ($msg -match '\[sigstore_verifier_missing\]' -and $script:VerifierPath -eq '')
& $fresh; $script:UpdaterDownload = $downloaded
$msg = Message-Of { Resolve-Verifier -L $L }
Check 'an updater without a trusted hash is never the verifier' ($msg -match '\[sigstore_verifier_missing\]' -and $script:VerifierPath -eq '')

# an installed updater comes before cosign.exe
$L2 = @{ Updater = Join-Path $tmp 'installed-updater' }
New-Item -ItemType Directory -Path $L2.Updater | Out-Null
Set-Content -LiteralPath (Join-Path $L2.Updater 'myriarch-updater.exe') -Value 'x' -NoNewline
& $fresh; $script:UpdaterChecksumsUrl = 'https://example.com/checksums.txt'
Resolve-Verifier -L $L2
Check 'an already installed myriarch-updater is the verifier of a new checksums.txt' ($script:VerifierPath -eq (Join-Path $L2.Updater 'myriarch-updater.exe'))

# -Verifier: a cosign must be 3.0.4 or newer
foreach ($case in @(@('v3.0.3', $false), @('v3.0.4', $true), @('v3.1.3', $true), @('v4.0.0', $true), @('v2.4.1', $false))) {
  $dir = Join-Path $tmp ('v-' + $case[0]); New-CosignStub -Dir $dir -VersionLine ('GitVersion:    ' + $case[0])
  & $fresh; $script:Verifier = Join-Path $dir 'cosign.exe'
  $msg = Message-Of { Resolve-Verifier -L $L }
  if ($case[1]) { Check "-Verifier cosign $($case[0]) is accepted" ($msg -eq '' -and $script:VerifierPath -eq $script:Verifier) }
  else { Check "-Verifier cosign $($case[0]) is refused with sigstore_verifier_outdated" ($msg -match '\[sigstore_verifier_outdated\]' -and $script:VerifierPath -eq '') }
}
$dir = Join-Path $tmp 'v-devel'; New-CosignStub -Dir $dir -VersionLine 'GitVersion:    devel'
& $fresh; $script:Verifier = Join-Path $dir 'cosign.exe'
$msg = Message-Of { Resolve-Verifier -L $L }
Check '-Verifier cosign without a usable version is refused' ($msg -match '\[sigstore_verifier_outdated\]')
& $fresh; $script:Verifier = Join-Path $dir 'cosign.exe'; $script:VerifySignatures = 'skip'
Check 'with -VerifySignatures skip no verifier is needed' ((Message-Of { Resolve-Verifier -L $L }) -eq '')

# cosign.exe from an administrator-only directory (Test-AdminOnlyDirectory replaced for the test): version floor and fallback
function Test-AdminOnlyDirectory { param([string]$Path) return $true }
New-CosignStub -Dir (Join-Path $tmp 'admin-old') -VersionLine 'GitVersion:    v3.0.3'
$env:PATH = (Join-Path $tmp 'admin-old') + [IO.Path]::PathSeparator + $origPath
& $fresh
$msg = Message-Of { Resolve-Verifier -L $L }
Check 'cosign.exe 3.0.3 from an administrator-only directory is refused with sigstore_verifier_outdated' ($msg -match '\[sigstore_verifier_outdated\]' -and $script:VerifierPath -eq '')
$env:PATH = (Join-Path $tmp 'cosign-ok') + [IO.Path]::PathSeparator + $origPath
& $fresh
Resolve-Verifier -L $L
Check 'cosign.exe 3.1.3 from an administrator-only directory is accepted' ($script:VerifierPath -eq (Join-Path (Join-Path $tmp 'cosign-ok') 'cosign.exe'))
$env:PATH = (Join-Path $tmp 'admin-old') + [IO.Path]::PathSeparator + $origPath
& $fresh
Resolve-Verifier -L $L2
Check 'an old cosign.exe does not hide an installed myriarch-updater' ($script:VerifierPath -eq (Join-Path $L2.Updater 'myriarch-updater.exe'))
$env:PATH = $origPath

# a pre-release of the minimum is older than the minimum
foreach ($case in @(@('v3.0.4-rc.1', $false), @('v3.0.5-rc.1', $true), @('v3.0.4+build', $true))) {
  $dir = Join-Path $tmp ('pre-' + $case[0]); New-CosignStub -Dir $dir -VersionLine ('GitVersion:    ' + $case[0])
  & $fresh; $script:Verifier = Join-Path $dir 'cosign.exe'
  $msg = Message-Of { Resolve-Verifier -L $L }
  if ($case[1]) { Check "-Verifier cosign $($case[0]) is accepted" ($msg -eq '' -and $script:VerifierPath -eq $script:Verifier) }
  else { Check "-Verifier cosign $($case[0]) is refused with sigstore_verifier_outdated" ($msg -match '\[sigstore_verifier_outdated\]' -and $script:VerifierPath -eq '') }
}

# -Verifier in a directory that unprivileged accounts can change is refused before it is run (the ACL check is replaced here)
function Test-AclEnforced { return $true }
function Test-AdminOnlyDirectory { param([string]$Path) return -not ($Path -like '*unsafe*') }
$dir = Join-Path $tmp 'unsafe-tools'; New-CosignStub -Dir $dir -VersionLine 'GitVersion:    v3.1.3'
& $fresh; $script:Verifier = Join-Path $dir 'cosign.exe'
$msg = Message-Of { Resolve-Verifier -L $L }
Check '-Verifier in a user-writable directory is refused with directory_unsafe' ($msg -match '\[directory_unsafe\]' -and $script:VerifierPath -eq '')
$dir = Join-Path $tmp 'unsafe-updater'; New-Item -ItemType Directory -Path $dir -Force | Out-Null
Set-Content -LiteralPath (Join-Path $dir 'myriarch-updater.exe') -Value 'x' -NoNewline
& $fresh; $script:Verifier = Join-Path $dir 'myriarch-updater.exe'
$msg = Message-Of { Resolve-Verifier -L $L }
Check 'an updater passed as -Verifier from a user-writable directory is refused too' ($msg -match '\[directory_unsafe\]' -and $script:VerifierPath -eq '')
$dir = Join-Path $tmp 'safe-tools'; New-CosignStub -Dir $dir -VersionLine 'GitVersion:    v3.1.3'
& $fresh; $script:Verifier = Join-Path $dir 'cosign.exe'
$msg = Message-Of { Resolve-Verifier -L $L }
Check '-Verifier in an administrator-only directory is accepted' ($msg -eq '' -and $script:VerifierPath -eq $script:Verifier)
& $fresh; $script:Verifier = Join-Path (Join-Path $tmp 'unsafe-tools') 'cosign.exe'; $script:VerifySignatures = 'skip'
Check 'with -VerifySignatures skip no verifier is run, so its location does not matter' ((Message-Of { Resolve-Verifier -L $L }) -eq '')
function Test-AclEnforced { return $false }
function Test-AdminOnlyDirectory { param([string]$Path) return $true }

# tar from the PATH (no tar.exe in the system directory, as on POSIX): only from an administrator-only directory
function Test-AclEnforced { return $true }
function Test-AdminOnlyDirectory { param([string]$Path) return -not ($Path -like '*unsafe*') }
foreach ($d in @('unsafe-tar', 'safe-tar')) {
  New-Item -ItemType Directory -Path (Join-Path $tmp $d) -Force | Out-Null
  Set-Content -LiteralPath (Join-Path (Join-Path $tmp $d) 'tar.exe') -Value "#!/bin/sh`nexit 0`n" -NoNewline
  & chmod 755 (Join-Path (Join-Path $tmp $d) 'tar.exe')
}
$env:PATH = (Join-Path $tmp 'unsafe-tar') + [IO.Path]::PathSeparator + $origPath
$msg = Message-Of { Resolve-TarPath }
Check 'tar from a directory that unprivileged accounts can write is refused with tool_missing' ($msg -match '\[tool_missing\].*unsafe-tar')
$env:PATH = (Join-Path $tmp 'safe-tar') + [IO.Path]::PathSeparator + $origPath
Check 'tar from an administrator-only directory is used' ((Resolve-TarPath) -eq (Join-Path (Join-Path $tmp 'safe-tar') 'tar.exe'))
$env:PATH = (Join-Path $tmp 'unsafe-tar') + [IO.Path]::PathSeparator + $origPath
function Test-AclEnforced { return $false }
Check 'without ACL enforcement (not Windows) the tar of the PATH is used' ((Resolve-TarPath) -eq (Join-Path (Join-Path $tmp 'unsafe-tar') 'tar.exe'))
$env:PATH = $origPath
function Test-AdminOnlyDirectory { param([string]$Path) return $true }

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
