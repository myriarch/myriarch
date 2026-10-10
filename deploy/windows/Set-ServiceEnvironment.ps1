# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

#Requires -Version 5.1

<#
.SYNOPSIS
  Sets or removes environment variables (collector secrets) of the otelcol-supervisor service.

.DESCRIPTION
  The supervisor and the collector it starts inherit the service environment from the registry
  (HKLM\SYSTEM\CurrentControlSet\Services\<service>, REG_MULTI_SZ "Environment"). Blueprint configs
  reference secrets as ${env:NAME} (for example OTEL_GATEWAY_TOKEN). The registry key is restricted to
  SYSTEM/Administrators so that local users cannot read the values.
  After the change the service is restarted (-NoRestart prevents that).

  Prefer passing values via -EnvFile (a file with NAME=VALUE lines, delete it afterwards) over -Set: command lines
  end up in process logs and the PowerShell history.

.EXAMPLE
  .\Set-ServiceEnvironment.ps1 -EnvFile C:\Temp\secrets.env
  .\Set-ServiceEnvironment.ps1 -Set @{ OTEL_GATEWAY_TOKEN = 'xyz' }
  .\Set-ServiceEnvironment.ps1 -Remove OTEL_GATEWAY_TOKEN
  .\Set-ServiceEnvironment.ps1 -List     # shows only the names, never the values
#>
[CmdletBinding()]
param(
  [hashtable]$Set,
  [string]$EnvFile,
  [string[]]$Remove,
  [switch]$List,
  [switch]$NoRestart,
  [string]$ServiceName = 'otelcol-supervisor'
)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$id = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw 'run as Administrator'
}
$key = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName"
if (-not (Test-Path -Path $key)) { throw "service $ServiceName is not installed" }

$map = [ordered]@{}
$existing = Get-ItemProperty -Path $key -Name Environment -ErrorAction SilentlyContinue
if ($existing -and $existing.PSObject.Properties['Environment']) {
  foreach ($e in @($existing.Environment)) {
    if (-not $e) { continue }
    $i = $e.IndexOf('=')
    if ($i -gt 0) { $map[$e.Substring(0, $i)] = $e.Substring($i + 1) }
  }
}

if ($EnvFile) {
  if (-not (Test-Path -LiteralPath $EnvFile)) { throw "EnvFile is not readable: $EnvFile" }
  if (-not $Set) { $Set = @{} }
  foreach ($line in [System.IO.File]::ReadAllLines($EnvFile)) {
    $t = $line.Trim()
    if (-not $t -or $t.StartsWith('#')) { continue }
    $i = $t.IndexOf('=')
    if ($i -le 0) { throw 'EnvFile: expected NAME=VALUE per line' }
    $Set[$t.Substring(0, $i).Trim()] = $t.Substring($i + 1)
  }
}

if ($List) {
  foreach ($k in $map.Keys) { Write-Host $k }
  return
}
if (-not $Set -and -not $Remove) { throw 'nothing to do: specify -Set, -EnvFile, -Remove or -List' }

if ($Set) {
  foreach ($k in $Set.Keys) {
    if ($k -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { throw "invalid variable name: $k" }
    if ([string]$Set[$k] -match '[\x00-\x1f]') { throw "control character in the value of $k" }
    $map[$k] = [string]$Set[$k]
  }
}
if ($Remove) {
  foreach ($k in $Remove) {
    if ($k -eq 'MYRIARCH_ENROLLMENT_TOKEN') { throw 'MYRIARCH_ENROLLMENT_TOKEN must not be removed' }
    $map.Remove($k)
  }
}

# First replace the registry ACL by SYSTEM/Administrators only (a fresh descriptor, owner Administrators), read it
# back and stop if any other principal still has access, then write (fail-closed).
$acl = New-Object System.Security.AccessControl.RegistrySecurity
$acl.SetAccessRuleProtection($true, $false)
$acl.SetOwner((New-Object System.Security.Principal.SecurityIdentifier('S-1-5-32-544')))
foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) {
  $who = New-Object System.Security.Principal.SecurityIdentifier($sid)
  $acl.AddAccessRule((New-Object System.Security.AccessControl.RegistryAccessRule($who, 'FullControl', 'ContainerInherit', 'None', 'Allow')))
}
Set-Acl -Path $key -AclObject $acl
$others = @((Get-Acl -Path $key).GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier]) |
  Where-Object { $_.AccessControlType -eq 'Allow' -and @('S-1-5-18', 'S-1-5-32-544') -notcontains $_.IdentityReference.Value })
if ($others.Count -gt 0) { throw "[acl_failed] the service key still grants access to $(($others | ForEach-Object { $_.IdentityReference.Value }) -join ', '): nothing was written" }

$lines = @()
foreach ($k in $map.Keys) { $lines += "$k=$($map[$k])" }
Set-ItemProperty -Path $key -Name Environment -Value ([string[]]$lines) -Type MultiString
Write-Host "Environment of $ServiceName updated ($($map.Count) variables)."

if (-not $NoRestart) {
  Restart-Service -Name $ServiceName -Force
  Write-Host "Service $ServiceName restarted."
}
