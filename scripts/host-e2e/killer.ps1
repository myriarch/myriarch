# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

#Requires -Version 5.1

# Fault injection of the Windows host end-to-end test (no product code), the counterpart of scripts/e2e/ui-updates/killer.sh: it
# repeatedly kills the running collector as long as the active copy <InstallDir>\bin\otelcol-contrib.exe is the binary of
# <Version> (the same size as <InstallDir>\versions\<Version>\otelcol-contrib.exe). On Windows the myriarch-updater activates a
# version by copying it to bin\ (activation copy), so the path of the process never names the version. The newly installed collector
# therefore never stays up during the health check and the updater rolls back by itself; the preflight of the new binary runs from
# a staging directory (versions\<version>.tmp-*) and the old collector has another size, so neither is touched. After the rollback
# the active copy is the old binary again and the loop kills nothing.
#
#   killer.ps1 -InstallDir 'C:\Program Files\Myriarch\collector' -Version 0.162.0 [-Interval 0.3]
#
# It runs until the process is stopped, prints one line "killed otelcol-contrib pid N (path)" per kill and needs the rights of an
# administrator (the collector runs as the virtual service account).
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$InstallDir,
  [Parameter(Mandatory = $true)][string]$Version,
  [double]$Interval = 0.3
)

$ErrorActionPreference = 'Continue'
$active = [IO.Path]::Combine($InstallDir, 'bin', 'otelcol-contrib.exe')
$target = [IO.Path]::Combine($InstallDir, 'versions', $Version, 'otelcol-contrib.exe')
$pause = [int]($Interval * 1000)

while ($true) {
  $newSize = -1
  $activeSize = -2
  if ((Test-Path -LiteralPath $target) -and (Test-Path -LiteralPath $active)) {
    $newSize = (Get-Item -LiteralPath $target).Length
    $activeSize = (Get-Item -LiteralPath $active).Length
  }
  if ($newSize -eq $activeSize) {
    foreach ($p in @(Get-Process -Name 'otelcol-contrib' -ErrorAction SilentlyContinue)) {
      $path = $null
      try { $path = $p.Path } catch { $path = $null }
      if ($path -and ($path -ieq $active)) {
        try {
          Stop-Process -Id $p.Id -Force -ErrorAction Stop
          Write-Output "killed otelcol-contrib pid $($p.Id) ($path)"
        } catch {
          Write-Output "could not kill otelcol-contrib pid $($p.Id): $($_.Exception.Message)"
        }
      }
    }
  }
  Start-Sleep -Milliseconds $pause
}
