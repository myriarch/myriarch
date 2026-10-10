# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the signature verification of our binaries in deploy/scripts/install-windows.ps1 (staging mode, -Root,
# file:// URLs). Called by ci/tests/installer-verify.sh when pwsh is available. Needs openssl (only to create the
# test keys/signatures). Covers the .NET Core path (ECDsa.Create(ECParameters)); the CNG fallback for Windows
# PowerShell 5.1 with an older .NET only runs on a Windows host. Failures are asserted on the stable error codes
# of the installer ("[<code>]"), never on message prose.
param(
  [Parameter(Mandatory)][string]$Installer,
  [Parameter(Mandatory)][string]$Templates,
  [Parameter(Mandatory)][string]$Work,
  [string]$SignBlob = ''   # contrib/enterprise-mirror/sign-blob.sh: with cosign in PATH a real cosign signature is verified as well
)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$pass = 0; $fail = 0
$pwsh = (Get-Process -Id $PID).Path
New-Item -ItemType Directory -Path $Work -Force | Out-Null
$Work = (Resolve-Path $Work).Path

function Invoke-Openssl { param([string[]]$A) $o = & openssl @A 2>&1; if ($LASTEXITCODE -ne 0) { throw "openssl $($A -join ' '): $o" } }
function Get-Hex { param([string]$P) return (Get-FileHash -Algorithm SHA256 -LiteralPath $P).Hash.ToLowerInvariant() }
function Sign-Blob { param([string]$File, [string]$Key)
  Invoke-Openssl @('dgst', '-sha256', '-sign', $Key, '-out', "$File.der", $File)
  Invoke-Openssl @('base64', '-A', '-in', "$File.der", '-out', "$File.sig")
  Remove-Item "$File.der"
}
function Uri-Of { param([string]$P) return 'file://' + $P }

# Keys
Invoke-Openssl @('ecparam', '-name', 'prime256v1', '-genkey', '-noout', '-out', "$Work/key.pem")
Invoke-Openssl @('ec', '-in', "$Work/key.pem", '-pubout', '-out', "$Work/cosign.pub")
Invoke-Openssl @('ecparam', '-name', 'prime256v1', '-genkey', '-noout', '-out', "$Work/other-key.pem")
Invoke-Openssl @('ec', '-in', "$Work/other-key.pem", '-pubout', '-out', "$Work/other.pub")

# Fake collector mirror (Windows layout)
$mirror = "$Work/mirror"
New-Item -ItemType Directory -Path "$mirror/v0.161.0", "$mirror/cmd/opampsupervisor/v0.161.0", "$Work/stage" -Force | Out-Null
Set-Content -LiteralPath "$Work/stage/otelcol-contrib.exe" -Value "#!/bin/sh`necho `"otelcol-contrib version 0.161.0`"`n" -NoNewline
& chmod 755 "$Work/stage/otelcol-contrib.exe"   # staging test on POSIX only: the fake binary is a shell script
& tar -czf "$mirror/v0.161.0/otelcol-contrib_0.161.0_windows_amd64.tar.gz" -C "$Work/stage" otelcol-contrib.exe
Set-Content -LiteralPath "$mirror/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_windows_amd64.exe" -Value 'MZ-fake-supervisor' -NoNewline
foreach ($f in @("$mirror/v0.161.0/otelcol-contrib_0.161.0_windows_amd64.tar.gz", "$mirror/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_windows_amd64.exe")) {
  Set-Content -LiteralPath "$f.sha256" -Value (Get-Hex $f) -NoNewline
}

# Fake release
$rel = "$Work/release"
New-Item -ItemType Directory -Path $rel -Force | Out-Null
$name = 'myriarch-updater_9.9.9_windows_amd64.exe'
Set-Content -LiteralPath "$rel/$name" -Value 'MZ-fake-updater' -NoNewline
Set-Content -LiteralPath "$rel/myriarch-updater_9.9.9_windows_amd64.zip" -Value 'fake zip' -NoNewline
function New-Checksums { param([string]$Dir, [string[]]$Names)
  $lines = foreach ($n in $Names) { '{0}  {1}' -f (Get-Hex "$Dir/$n"), $n }
  Set-Content -LiteralPath "$Dir/checksums.txt" -Value ($lines -join "`n") -NoNewline
}
New-Checksums -Dir $rel -Names @($name, 'myriarch-updater_9.9.9_windows_amd64.zip')
Sign-Blob -File "$rel/checksums.txt" -Key "$Work/key.pem"
$updaterSha = Get-Hex "$rel/$name"

$n = 0
function Run-Install { param([string[]]$Extra, [string]$UpdaterFile = "$rel/$name")
  $script:n++
  $root = "$Work/root$($script:n)"
  $args = @('-NoProfile', '-NonInteractive', '-File', $Installer, '-Root', $root, '-Arch', 'amd64', '-TemplatesDir', $Templates,
    '-Insecure', '-VerifySignatures', 'skip', '-ReleaseBaseUrl', (Uri-Of $mirror),
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
$signed = @('-UpdaterChecksumsUrl', (Uri-Of "$rel/checksums.txt"), '-SigningKey', "$Work/cosign.pub")

$r = Run-Install $signed
Expect-Ok 'valid signature: updater is installed' $r
if ((Get-Hex "$($r.Root)/ProgramFiles/Myriarch/updater/myriarch-updater.exe") -eq $updaterSha) { $pass++; Write-Host '  ok      ... the installed updater file is the verified one' } else { $fail++; Write-Host '  FAIL  updater file is missing or differs' }
Expect-Ok 'signature AND matching -UpdaterSha256' (Run-Install ($signed + @('-UpdaterSha256', $updaterSha)))
Expect-Ok '-UpdaterSha256 only (previous behavior)' (Run-Install @('-UpdaterSha256', $updaterSha))

# tampered checksums.txt
$t = "$Work/tampered"; New-Item -ItemType Directory -Path $t -Force | Out-Null
Copy-Item "$rel/*" $t
Set-Content -LiteralPath "$t/checksums.txt" -Value ((Get-Content -Raw "$rel/checksums.txt") -replace '^.', '0') -NoNewline
$r = Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$t/checksums.txt"), '-SigningKey', "$Work/cosign.pub")
Expect-Fail 'tampered checksums.txt is rejected' '\[signature_invalid\]' $r
if (-not (Test-Path "$($r.Root)/ProgramData/Myriarch/config/supervisor.yaml")) { $pass++; Write-Host '  ok      ... and nothing was installed' } else { $fail++; Write-Host '  FAIL  something was installed after a tampered checksums.txt' }

Expect-Fail 'wrong key is rejected' '\[signature_invalid\]' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$rel/checksums.txt"), '-SigningKey', "$Work/other.pub"))

# missing line (validly signed)
$m = "$Work/missing"; New-Item -ItemType Directory -Path $m -Force | Out-Null
Copy-Item "$rel/myriarch-updater_9.9.9_windows_amd64.zip" $m
New-Checksums -Dir $m -Names @('myriarch-updater_9.9.9_windows_amd64.zip'); Sign-Blob -File "$m/checksums.txt" -Key "$Work/key.pem"
Expect-Fail 'signed checksums.txt without a line for the updater' '\[checksum_entry_count\].*\b0\b' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$m/checksums.txt"), '-SigningKey', "$Work/cosign.pub"))

Expect-Fail '-UpdaterSha256 contradicts checksums.txt (hash conflict)' '\[checksum_conflict\]' (Run-Install ($signed + @('-UpdaterSha256', ('0' * 64))))

# tampered updater binary
$b = "$Work/bin-tampered"; New-Item -ItemType Directory -Path $b -Force | Out-Null
Copy-Item "$rel/*" $b; Add-Content -LiteralPath "$b/$name" -Value 'evil' -NoNewline
Expect-Fail 'updater binary does not match the signed checksum' '\[checksum_mismatch\]' (Run-Install $signed -UpdaterFile "$b/$name")

$d = "$Work/nosig"; New-Item -ItemType Directory -Path $d -Force | Out-Null; Copy-Item "$rel/checksums.txt" $d
Expect-Fail 'missing signature file' '\[download_failed\]' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$d/checksums.txt"), '-SigningKey', "$Work/cosign.pub"))

$e = "$Work/badb64"; New-Item -ItemType Directory -Path $e -Force | Out-Null; Copy-Item "$rel/checksums.txt" $e; Set-Content -LiteralPath "$e/checksums.txt.sig" -Value '!!!not-base64!!!'
Expect-Fail 'signature is not base64' '\[signature_malformed\]' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$e/checksums.txt"), '-SigningKey', "$Work/cosign.pub"))

Expect-Fail '-UpdaterChecksumsUrl without -SigningKey' '\[option_conflict\].*-SigningKey' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$rel/checksums.txt")))
Expect-Fail '-SigningKey without -UpdaterChecksumsUrl' '\[option_conflict\].*-UpdaterChecksumsUrl' (Run-Install @('-SigningKey', "$Work/cosign.pub", '-UpdaterSha256', $updaterSha))
Expect-Fail 'signing key file is not readable' '\[file_unreadable\]' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$rel/checksums.txt"), '-SigningKey', "$Work/does-not-exist.pub"))
Expect-Fail '-UpdaterSignerThumbprint with an invalid value' '\[option_invalid\].*-UpdaterSignerThumbprint' (Run-Install @('-UpdaterSha256', $updaterSha, '-UpdaterSignerThumbprint', 'xyz'))

# real cosign signature (format contract): contrib/enterprise-mirror/sign-blob.sh creates it, the installer (.NET) verifies it
if ($SignBlob -and (Get-Command cosign -ErrorAction SilentlyContinue) -and (Get-Command bash -ErrorAction SilentlyContinue)) {
  $c = "$Work/cosign-release"; New-Item -ItemType Directory -Path $c -Force | Out-Null
  Copy-Item "$rel/*" $c; Remove-Item "$c/checksums.txt.sig"
  $env:COSIGN_PASSWORD = 'testpw'
  Push-Location $c; & cosign generate-key-pair 2>&1 | Out-Null; Pop-Location
  $env:COSIGN_KEY = "$c/cosign.key"; $env:COSIGN_REPO_PUBLIC_KEY = '/nonexistent'
  & bash $SignBlob "$c/checksums.txt" "$c/checksums.txt.sig" 2>&1 | Out-Null
  Remove-Item Env:COSIGN_KEY, Env:COSIGN_REPO_PUBLIC_KEY, Env:COSIGN_PASSWORD
  Expect-Ok 'a signature produced by cosign (contrib/enterprise-mirror/sign-blob.sh) is accepted' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$c/checksums.txt"), '-SigningKey', "$c/cosign.pub"))
  Expect-Fail 'a cosign signature with a foreign key is rejected' '\[signature_invalid\]' (Run-Install @('-UpdaterChecksumsUrl', (Uri-Of "$c/checksums.txt"), '-SigningKey', "$Work/cosign.pub"))
}

# Mandatory without -Insecure
$out = & $pwsh -NoProfile -NonInteractive -File $Installer -Root "$Work/rootM" -Arch amd64 -TemplatesDir $Templates -ReleaseBaseUrl 'https://mirror.example.com/r' `
  -OpampEndpoint 'wss://x/v1/opamp' -Token myre_x -SiteType office -SiteId 1 -CollectorVersion 0.161.0 -WithUpdater -UpdaterUrl "https://mirror.example.com/$name" 2>&1 | Out-String
$rM = [pscustomobject]@{ Code = $LASTEXITCODE; Out = $out }
Expect-Fail 'a hash source is mandatory without -Insecure' '\[updater_hash_source_missing\]' $rM

# Helper functions (identity pattern, verifier order, re-hash after the copy, registry ACL), in a child process
$helperOut = & $pwsh -NoProfile -NonInteractive -File (Join-Path $PSScriptRoot 'installer-helpers.ps1') -Installer $Installer 2>&1 | Out-String
if ($LASTEXITCODE -eq 0) { $pass++; Write-Host '  ok    installer helper functions (installer-helpers.ps1)' } else { $fail++; Write-Host '  FAIL  installer helper functions (installer-helpers.ps1)'; Write-Host $helperOut }

Write-Host ''
Write-Host "installer-verify.ps1: $pass passed, $fail failed"
if ($fail -gt 0) { exit 1 }
