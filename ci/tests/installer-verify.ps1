# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the verification of the keyless release bundle (updater) and of the upstream bundles (collector, supervisor) in
# deploy/scripts/install-windows.ps1 (staging mode, -Root, file:// URLs). Called by ci/tests/installer-verify.sh when pwsh
# is available. The verifier is the stub ci/tests/stub-verifier.sh (identity regexp against the certificate identity of
# the bundle, digest of the signed file, version of cosign); with cosign in PATH the real cosign is run offline against the
# committed upstream fixtures. The logic is exercised with the .NET Core path of PowerShell 7; the Windows-only parts
# (ACLs, services, Authenticode) only run on a Windows host. Failures are asserted on the stable error codes of the
# installer ("[<code>]"), never on message prose.
param(
  [Parameter(Mandatory)][string]$Installer,
  [Parameter(Mandatory)][string]$Templates,
  [Parameter(Mandatory)][string]$Work
)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$pass = 0; $fail = 0
$pwsh = (Get-Process -Id $PID).Path
New-Item -ItemType Directory -Path $Work -Force | Out-Null
$Work = (Resolve-Path $Work).Path
$ciTests = $PSScriptRoot

function Get-Hex { param([string]$P) return (Get-FileHash -Algorithm SHA256 -LiteralPath $P).Hash.ToLowerInvariant() }
function Uri-Of { param([string]$P) return 'file://' + $P }

# Fake collector mirror (Windows layout)
$mirror = "$Work/mirror"
New-Item -ItemType Directory -Path "$mirror/v0.161.0", "$mirror/cmd/opampsupervisor/v0.161.0", "$Work/stage" -Force | Out-Null
Set-Content -LiteralPath "$Work/stage/otelcol-contrib.exe" -Value "#!/bin/sh`necho `"otelcol-contrib version 0.161.0`"`n" -NoNewline
& chmod 755 "$Work/stage/otelcol-contrib.exe"   # staging test on POSIX only: the fake binary is a shell script
& tar -czf "$mirror/v0.161.0/otelcol-contrib_0.161.0_windows_amd64.tar.gz" -C "$Work/stage" otelcol-contrib.exe
Set-Content -LiteralPath "$mirror/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_windows_amd64.exe" -Value 'MZ-fake-supervisor' -NoNewline
foreach ($f in @("$mirror/v0.161.0/otelcol-contrib_0.161.0_windows_amd64.tar.gz", "$mirror/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_windows_amd64.exe")) {
  Set-Content -LiteralPath "$f.sha256" -Value (Get-Hex $f) -NoNewline
  Set-Content -LiteralPath "$f.sigstore.json" -Value '{}' -NoNewline   # upstream bundles: the stub accepts them
}

# Fake release
$rel = "$Work/release"
New-Item -ItemType Directory -Path $rel -Force | Out-Null
$version = '9.9.9'
$name = "myriarch-updater_${version}_windows_amd64.exe"
$plain = 'myriarch-updater-plain-name.exe'
Set-Content -LiteralPath "$rel/$name" -Value 'MZ-fake-updater' -NoNewline
Copy-Item "$rel/$name" "$rel/$plain"
Set-Content -LiteralPath "$rel/myriarch-updater_${version}_windows_amd64.zip" -Value 'fake zip' -NoNewline
function New-Checksums { param([string]$Dir, [string[]]$Names)
  $lines = foreach ($n in $Names) { '{0}  {1}' -f (Get-Hex "$Dir/$n"), $n }
  Set-Content -LiteralPath "$Dir/checksums.txt" -Value ($lines -join "`n") -NoNewline
}
New-Checksums -Dir $rel -Names @($name, "myriarch-updater_${version}_windows_amd64.zip", $plain)
Set-Content -LiteralPath "$rel/checksums.txt.sigstore.json" -Value '{"bundle":"checksums.txt"}' -NoNewline
$updaterSha = Get-Hex "$rel/$name"
$goodSumsSha = Get-Hex "$rel/checksums.txt"

# Test verifiers (see ci/tests/stub-verifier.sh): "cosign" is handled like cosign, "myriarch-updater.exe" like verify-bundle
$stubs = "$Work/stubs"
New-Item -ItemType Directory -Path $stubs, "$stubs/updater" -Force | Out-Null
Copy-Item "$ciTests/stub-verifier.sh" "$stubs/cosign"
Copy-Item "$ciTests/stub-verifier.sh" "$stubs/updater/myriarch-updater.exe"
& chmod 755 "$stubs/cosign" "$stubs/updater/myriarch-updater.exe"
$stubLog = "$Work/verifier.log"
$env:STUB_LOG = $stubLog
$env:STUB_GOOD_SHA = $goodSumsSha
$goodSan = "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v$version"
$env:STUB_SAN = $goodSan
function Reset-Log { Set-Content -LiteralPath $stubLog -Value '' -NoNewline }
function Get-Log { return (Get-Content -LiteralPath $stubLog -Raw) }

$n = 0
function Run-Install { param([string[]]$Extra, [string]$UpdaterFile = "$rel/$name", [string]$Verifier = "$stubs/cosign", [switch]$Lab)
  $script:n++
  $root = "$Work/root$($script:n)"
  $sig = if ($Lab) { @('-VerifySignatures', 'skip') } else { @('-VerifySignatures', 'require') }
  $vf = if ($Verifier) { @('-Verifier', $Verifier) } else { @() }
  $args = @('-NoProfile', '-NonInteractive', '-File', $Installer, '-Root', $root, '-Arch', 'amd64', '-TemplatesDir', $Templates,
    '-Insecure') + $sig + $vf + @('-ReleaseBaseUrl', (Uri-Of $mirror),
    '-OpampEndpoint', 'wss://fleet.example.com/v1/opamp', '-Token', 'myre_abc123', '-SiteType', 'office', '-SiteId', 'OFFICE1',
    '-CollectorVersion', '0.161.0', '-WithUpdater', '-UpdaterUrl', (Uri-Of $UpdaterFile)) + $Extra
  $out = & $pwsh @args 2>&1 | Out-String
  return [pscustomobject]@{ Code = $LASTEXITCODE; Out = $out; Root = $root }
}
function Expect-Ok { param([string]$Title, $R)
  if ($R.Code -eq 0) { $script:pass++; Write-Host "  ok    $Title" } else { $script:fail++; Write-Host "  FAIL  $Title (exit $($R.Code))"; Write-Host $R.Out }
}
function Expect-Fail { param([string]$Title, [string]$Pattern, $R)
  if ($R.Code -ne 0 -and $R.Out -match $Pattern) { $script:pass++; Write-Host "  ok    $Title" }
  else { $script:fail++; Write-Host "  FAIL  $Title (exit $($R.Code), message does not match /$Pattern/)"; Write-Host $R.Out }
}
function Expect-True { param([string]$Title, [bool]$Ok, [string]$Detail = '')
  if ($Ok) { $script:pass++; Write-Host "  ok    $Title" } else { $script:fail++; Write-Host "  FAIL  $Title"; if ($Detail) { Write-Host $Detail } }
}
function Test-NothingInstalled { param($R)
  return (-not (Test-Path "$($R.Root)/ProgramData/Myriarch/config/supervisor.yaml")) -and (-not (Test-Path "$($R.Root)/ProgramFiles/Myriarch/updater/myriarch-updater.exe"))
}
$sums = @('-UpdaterChecksumsUrl', (Uri-Of "$rel/checksums.txt"))

Write-Host 'install-windows.ps1: keyless bundle of checksums.txt (release path)'
Reset-Log
$r = Run-Install $sums
Expect-Ok 'valid bundle: updater is installed' $r
Expect-True '... the installed updater file is the verified one' ((Test-Path "$($r.Root)/ProgramFiles/Myriarch/updater/myriarch-updater.exe") -and (Get-Hex "$($r.Root)/ProgramFiles/Myriarch/updater/myriarch-updater.exe") -eq $updaterSha)
$log = Get-Log
$firstCall = ($log -split "`n" | Where-Object { $_ })[0]
$wantId = '--certificate-identity-regexp ^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v9\.9\.9$ --certificate-oidc-issuer https://token.actions.githubusercontent.com'
Expect-True 'cosign receives the exact release identity of the updater version with GitHub''s issuer' ($firstCall.Contains($wantId) -and $firstCall.Contains('checksums.txt.sigstore.json')) $log
Expect-True '... checksums.txt is verified first, then the collector and the supervisor' (($log -split "`n" | Where-Object { $_ }).Count -eq 3 -and $firstCall.TrimEnd().EndsWith('checksums.txt'))

Expect-Ok 'verified checksums.txt AND matching -UpdaterSha256' (Run-Install ($sums + @('-UpdaterSha256', $updaterSha)))
Copy-Item "$rel/checksums.txt.sigstore.json" "$Work/elsewhere.json"
Expect-Ok '-UpdaterBundleUrl (bundle at a different location)' (Run-Install ($sums + @('-UpdaterBundleUrl', (Uri-Of "$Work/elsewhere.json"))))
Expect-Ok '-UpdaterVersion for a file name without a version' (Run-Install ($sums + @('-UpdaterVersion', $version)) -UpdaterFile "$rel/$plain")
Expect-Ok '-UpdaterVersion accepts a leading v' (Run-Install ($sums + @('-UpdaterVersion', "v$version")) -UpdaterFile "$rel/$plain")
Expect-Ok '-UpdaterVersion agrees with the version in -UpdaterUrl' (Run-Install ($sums + @('-UpdaterVersion', $version)))
Expect-Fail '-UpdaterVersion differs from the version in -UpdaterUrl' '\[option_conflict\].*9\.9\.8.*9\.9\.9' (Run-Install ($sums + @('-UpdaterVersion', '9.9.8')))
Expect-Fail 'no version in the file name and no -UpdaterVersion' '\[option_missing\].*-UpdaterVersion' (Run-Install $sums -UpdaterFile "$rel/$plain")
Expect-Fail '-UpdaterVersion is not a version' '\[option_invalid\].*-UpdaterVersion' (Run-Install ($sums + @('-UpdaterVersion', 'latest')))

# a bundle with another identity must not pass, however genuine it is
$base = 'https://github.com/myriarch/myriarch/.github/workflows'
$foreign = [ordered]@{
  'another tag' = "$base/release.yml@refs/tags/v9.9.8"
  'a longer tag (prefix of the version)' = "$base/release.yml@refs/tags/v9.9.9.1"
  'an rc of the version' = "$base/release.yml@refs/tags/v9.9.9-rc.1"
  'a branch instead of a tag' = "$base/release.yml@refs/heads/main"
  'another workflow' = "$base/ci.yml@refs/tags/v9.9.9"
  'another repository' = "https://github.com/evil/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9"
  'a repository that only starts like ours' = "https://github.com/myriarch/myriarch-fork/.github/workflows/release.yml@refs/tags/v9.9.9"
  'the upstream collector workflow' = 'https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v9.9.9'
}
foreach ($k in $foreign.Keys) {
  $env:STUB_SAN = $foreign[$k]
  $r = Run-Install $sums
  Expect-Fail "bundle of $k is rejected" '\[sigstore_verification_failed\].*checksums\.txt' $r
  Expect-True '  ... and nothing was installed' (Test-NothingInstalled $r)
}
$env:STUB_SAN = $goodSan

# tampered checksums.txt
$t = "$Work/tampered"; New-Item -ItemType Directory -Path $t -Force | Out-Null
Copy-Item "$rel/*" $t
Set-Content -LiteralPath "$t/checksums.txt" -Value ((Get-Content -Raw "$rel/checksums.txt") -replace '^.', '0') -NoNewline
$r = Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$t/checksums.txt"))
Expect-Fail 'tampered checksums.txt is rejected' '\[sigstore_verification_failed\].*checksums\.txt' $r
Expect-True '  ... and nothing was installed' (Test-NothingInstalled $r)

$d = "$Work/nobundle"; New-Item -ItemType Directory -Path $d -Force | Out-Null; Copy-Item "$rel/checksums.txt" $d
Expect-Fail 'missing bundle file' '\[download_failed\]' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$d/checksums.txt")))

# genuine checksums.txt without a line for the updater
$m = "$Work/missing"; New-Item -ItemType Directory -Path $m -Force | Out-Null
Copy-Item "$rel/myriarch-updater_${version}_windows_amd64.zip", "$rel/checksums.txt.sigstore.json" $m
New-Checksums -Dir $m -Names @("myriarch-updater_${version}_windows_amd64.zip")
$env:STUB_GOOD_SHA = Get-Hex "$m/checksums.txt"
Expect-Fail 'genuine checksums.txt without a line for the updater' '\[checksum_entry_count\].*\b0\b' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$m/checksums.txt")))
$env:STUB_GOOD_SHA = $goodSumsSha

Expect-Fail '-UpdaterSha256 contradicts checksums.txt (hash conflict)' '\[checksum_conflict\]' (Run-Install ($sums + @('-UpdaterSha256', ('0' * 64))))

# tampered updater binary
$b = "$Work/bin-tampered"; New-Item -ItemType Directory -Path $b -Force | Out-Null
Copy-Item "$rel/*" $b; Add-Content -LiteralPath "$b/$name" -Value 'evil' -NoNewline
Expect-Fail 'updater binary does not match the verified checksum' '\[checksum_mismatch\]' (Run-Install $sums -UpdaterFile "$b/$name")

Write-Host 'install-windows.ps1: verifier (cosign 3.0.4 or newer, or an installed myriarch-updater), never the downloaded updater'
Reset-Log
$r = Run-Install $sums -Verifier "$stubs/updater/myriarch-updater.exe"
$log = Get-Log
Expect-True 'an installed myriarch-updater as -Verifier gets the identity, the issuer and the version' ($r.Code -eq 0 -and $log.Contains('verify-bundle') -and $log.Contains('--issuer https://token.actions.githubusercontent.com') -and $log.Contains('--version v9.9.9') -and $log.Contains('release\.yml@refs/tags/v9\.9\.9$')) $r.Out
foreach ($v in @(@('v3.0.4', $true), @('v3.1.3', $true), @('v4.0.0', $true), @('v3.0.3', $false), @('v2.4.1', $false), @('devel', $false))) {
  $env:STUB_COSIGN_VERSION = $v[0]
  $r = Run-Install $sums
  if ($v[1]) { Expect-Ok "cosign $($v[0]) is accepted" $r }
  else {
    Expect-Fail "cosign $($v[0]) is refused" '\[sigstore_verifier_outdated\]' $r
    Expect-True '  ... and nothing was installed' (Test-NothingInstalled $r)
  }
}
Remove-Item Env:STUB_COSIGN_VERSION

# no verifier at all: -Verifier absent, no installed updater, cosign.exe only from an administrator-only directory (never on POSIX)
$r = Run-Install $sums -Verifier ''
Expect-Fail 'without a verifier: sigstore_verifier_missing' '\[sigstore_verifier_missing\]' $r
Expect-True '  ... the downloaded updater was neither installed nor used' (Test-NothingInstalled $r)
Expect-True '  ... the message names the -UpdaterSha256 alternative' ($r.Out -match '-UpdaterSha256')

# -UpdaterSha256 only, on a host without a verifier: the hash-checked updater is the verifier (fake updater: not executable here)
$r = Run-Install @('-UpdaterSha256', $updaterSha) -Verifier ''
Expect-True '-UpdaterSha256 without -UpdaterChecksumsUrl may use the hash-checked updater as a verifier (reaches the execution)' ($r.Out -match 'SHA-256 ok: myriarch-updater')
Expect-Ok '-UpdaterSha256 only (lab mode)' (Run-Install @('-UpdaterSha256', $updaterSha) -Lab)

Write-Host 'install-windows.ps1: removed and conflicting options'
$r = Run-Install ($sums + @('-SigningKey', "$Work/cosign.pub"))
Expect-Fail '-SigningKey was removed and names the replacement' '\[option_removed\].*-UpdaterChecksumsUrl' $r
Expect-True '  ... and nothing was installed' (Test-NothingInstalled $r)
Expect-Fail '-UpdaterChecksumsSigUrl was removed and names the replacement' '\[option_removed\].*-UpdaterBundleUrl' (Run-Install ($sums + @('-UpdaterChecksumsSigUrl', (Uri-Of "$rel/checksums.txt.sig"))))
Expect-Fail '-UpdaterBundleUrl without -UpdaterChecksumsUrl' '\[option_conflict\].*-UpdaterChecksumsUrl' (Run-Install @('-UpdaterBundleUrl', (Uri-Of "$rel/checksums.txt.sigstore.json"), '-UpdaterSha256', $updaterSha))
Expect-Fail '-UpdaterChecksumsUrl with -VerifySignatures skip' '\[option_conflict\].*-VerifySignatures skip' (Run-Install $sums -Lab)
Expect-Fail '-UpdaterSignerThumbprint with an invalid value' '\[option_invalid\].*-UpdaterSignerThumbprint' (Run-Install @('-UpdaterSha256', $updaterSha, '-UpdaterSignerThumbprint', 'xyz'))

# -UpdaterChecksumsUrl without -WithUpdater
$out = & $pwsh -NoProfile -NonInteractive -File $Installer -Root "$Work/rootW" -Arch amd64 -TemplatesDir $Templates -Insecure -VerifySignatures skip -ReleaseBaseUrl (Uri-Of $mirror) `
  -OpampEndpoint 'wss://x/v1/opamp' -Token myre_x -SiteType office -SiteId 1 -CollectorVersion 0.161.0 -UpdaterChecksumsUrl (Uri-Of "$rel/checksums.txt") 2>&1 | Out-String
$rW = [pscustomobject]@{ Code = $LASTEXITCODE; Out = $out }
Expect-Fail '-UpdaterChecksumsUrl without -WithUpdater' '\[option_conflict\].*-WithUpdater' $rW

# Mandatory without -Insecure
$out = & $pwsh -NoProfile -NonInteractive -File $Installer -Root "$Work/rootM" -Arch amd64 -TemplatesDir $Templates -ReleaseBaseUrl 'https://mirror.example.com/r' `
  -OpampEndpoint 'wss://x/v1/opamp' -Token myre_x -SiteType office -SiteId 1 -CollectorVersion 0.161.0 -WithUpdater -UpdaterUrl "https://mirror.example.com/$name" 2>&1 | Out-String
$rM = [pscustomobject]@{ Code = $LASTEXITCODE; Out = $out }
Expect-Fail 'a hash source is mandatory without -Insecure' '\[updater_hash_source_missing\]' $rM

# checksums.txt and bundle only over https
$out = & $pwsh -NoProfile -NonInteractive -File $Installer -Root "$Work/rootH" -Arch amd64 -TemplatesDir $Templates -ReleaseBaseUrl 'https://mirror.example.com/r' `
  -OpampEndpoint 'wss://x/v1/opamp' -Token myre_x -SiteType office -SiteId 1 -CollectorVersion 0.161.0 -WithUpdater -UpdaterUrl "https://mirror.example.com/$name" `
  -UpdaterChecksumsUrl 'http://mirror.example.com/checksums.txt' 2>&1 | Out-String
Expect-Fail 'checksums.txt only over https (without -Insecure)' '\[https_required\]' ([pscustomobject]@{ Code = $LASTEXITCODE; Out = $out })
$out = & $pwsh -NoProfile -NonInteractive -File $Installer -Root "$Work/rootB" -Arch amd64 -TemplatesDir $Templates -ReleaseBaseUrl 'https://mirror.example.com/r' `
  -OpampEndpoint 'wss://x/v1/opamp' -Token myre_x -SiteType office -SiteId 1 -CollectorVersion 0.161.0 -WithUpdater -UpdaterUrl "https://mirror.example.com/$name" `
  -UpdaterChecksumsUrl 'https://mirror.example.com/checksums.txt' -UpdaterBundleUrl 'http://mirror.example.com/checksums.txt.sigstore.json' 2>&1 | Out-String
Expect-Fail 'the bundle only over https (without -Insecure)' '\[https_required\]' ([pscustomobject]@{ Code = $LASTEXITCODE; Out = $out })

# Real cosign, offline, against the committed upstream fixtures: the upstream bundle is genuine but cannot carry the Myriarch identity
if (Get-Command cosign -ErrorAction SilentlyContinue) {
  Write-Host 'install-windows.ps1: real cosign against the upstream fixtures (offline)'
  $fix = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'internal/updater/testdata/sigstore'
  $fixBlob = 'otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256'
  $real = "$Work/real-release"; New-Item -ItemType Directory -Path $real -Force | Out-Null
  Copy-Item "$fix/$fixBlob" "$real/checksums.txt"; Copy-Item "$fix/$fixBlob.sigstore.json" "$real/checksums.txt.sigstore.json"
  $cosignPath = (Get-Command cosign).Source
  $r = Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$real/checksums.txt"), '-TrustedRoot', "$fix/trusted_root.json") -Verifier $cosignPath
  Expect-Fail 'real cosign: a genuine bundle with another identity is rejected' '\[sigstore_verification_failed\].*checksums\.txt' $r
  Expect-True '  ... and nothing was installed' (Test-NothingInstalled $r)
  $out = & cosign verify-blob --bundle "$fix/$fixBlob.sigstore.json" --trusted-root "$fix/trusted_root.json" `
    --certificate-identity-regexp '^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v0\.162\.0$' `
    --certificate-oidc-issuer https://token.actions.githubusercontent.com "$fix/$fixBlob" 2>&1 | Out-String
  Expect-True 'positive control: cosign verifies the upstream bundle with the upstream identity' ($LASTEXITCODE -eq 0) $out
}

# Helper functions (identity pattern, verifier order, re-hash after the copy, registry ACL), in a child process
$helperOut = & $pwsh -NoProfile -NonInteractive -File (Join-Path $PSScriptRoot 'installer-helpers.ps1') -Installer $Installer 2>&1 | Out-String
if ($LASTEXITCODE -eq 0) { $pass++; Write-Host '  ok    installer helper functions (installer-helpers.ps1)' } else { $fail++; Write-Host '  FAIL  installer helper functions (installer-helpers.ps1)'; Write-Host $helperOut }

Write-Host ''
Write-Host "installer-verify.ps1: $pass passed, $fail failed"
if ($fail -gt 0) { exit 1 }
