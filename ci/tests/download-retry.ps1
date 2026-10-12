# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests the retry policy of the downloads in deploy/scripts/install-windows.ps1 (Invoke-Download) against a local HTTP server that answers scripted faults
# (ci/tests/http-fault-server.py, 127.0.0.1, ephemeral port, started and stopped here). Counterpart of
# ci/tests/download-retry-test.sh, which tests install-linux.sh; run-all.sh calls this file in the group pwsh.
#
# The policy: a transient failure (a timeout, a failed connection, HTTP 408, 429, 500, 502, 503, 504) is retried with pauses of
# 1, 2, 4, ... s, no attempt starts later than 120 s after the first one; every other failure ends the download at once; the
# final message names the number of attempts. Most cases run on a fake clock (Get-MonotonicSeconds and Start-Sleep are replaced
# by functions), W0 runs on the real clock. The logic runs with PowerShell 7; the classification of the exceptions of Windows
# PowerShell 5.1 (WebException with a Status) is covered by W8 with constructed exceptions.
param(
  [Parameter(Mandatory)][string]$Installer,
  [Parameter(Mandatory)][string]$Server,
  [Parameter(Mandatory)][string]$Work
)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$pass = 0; $fail = 0
function Check {
  param([string]$Title, [bool]$Ok, [string]$Detail = '')
  if ($Ok) { $script:pass++; Write-Host "  ok    $Title" } else { $script:fail++; Write-Host "  FAIL  $Title"; if ($Detail) { Write-Host "        $Detail" } }
}

$python = Get-Command python3 -ErrorAction SilentlyContinue
if (-not $python) { Write-Host 'download-retry.ps1: python3 is not available, skipped'; exit 0 }

New-Item -ItemType Directory -Path $Work -Force | Out-Null
$Work = (Resolve-Path $Work).Path
$served = Join-Path $Work 'served'
New-Item -ItemType Directory -Path $served -Force | Out-Null
Set-Content -LiteralPath (Join-Path $served 'payload.bin') -Value 'payload of the retry tests' -NoNewline

$rules = [ordered]@{
  '/w0/payload.bin' = @(502, 503)
  '/w1/payload.bin' = @(504, 504, 504, 504)
  '/w2/payload.bin' = @(408, 429, 500, 502, 503)
  '/w3/payload.bin' = 504
  '/w5/payload.bin' = 403
}
$rulesFile = Join-Path $Work 'rules.json'
Set-Content -LiteralPath $rulesFile -Value ($rules | ConvertTo-Json -Compress) -NoNewline
$logFile = Join-Path $Work 'server.log'
$portFile = Join-Path $Work 'port'
Set-Content -LiteralPath $logFile -Value '' -NoNewline
$srv = Start-Process -FilePath $python.Source -PassThru -RedirectStandardOutput (Join-Path $Work 'server.out') -RedirectStandardError (Join-Path $Work 'server.err') `
  -ArgumentList @('-I', $Server, '--rules', $rulesFile, '--log', $logFile, '--port-file', $portFile, '--root', $served)

function Get-Requests { param([string]$Path) return @(Get-Content -LiteralPath $logFile | Where-Object { $_ -like "$Path *" }).Count }
function Get-Failure { param([scriptblock]$Block) try { & $Block; return '' } catch { return $_.Exception.Message } }

try {
  for ($i = 0; $i -lt 100 -and -not (Test-Path -LiteralPath $portFile); $i++) { Start-Sleep -Milliseconds 100 }
  if (-not (Test-Path -LiteralPath $portFile)) { throw "the fault server did not start: $(Get-Content -LiteralPath (Join-Path $Work 'server.err') -Raw)" }
  $port = [int](Get-Content -LiteralPath $portFile -Raw).Trim()
  # a port that nothing listens on
  $probe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $probe.Start(); $closedPort = $probe.LocalEndpoint.Port; $probe.Stop()
  $base = "http://127.0.0.1:$port"
  Set-Location -LiteralPath $Work

  # ---- install-windows.ps1: Invoke-Download ----------------------------------------------------------------------------
  . $Installer
  $ErrorActionPreference = 'Stop'
  Set-StrictMode -Version 2.0
  $Insecure = [switch]$true   # the server speaks plain http
  function Invoke-Dl { param([string]$Url) Get-Failure { Invoke-Download -Url $Url -Destination (Join-Path $Work 'out.bin') } }

  Write-Host '== install-windows.ps1 Invoke-Download =='
  # W0: the real clock and the real Start-Sleep (502, 503, then 200: pauses of 1 and 2 s)
  $watch = [Diagnostics.Stopwatch]::StartNew()
  $msg = Invoke-Dl "$base/w0/payload.bin"
  $wall = $watch.Elapsed.TotalSeconds
  Check 'W0 real clock: 502, 503, then 200 succeeds' ($msg -eq '') $msg
  Check 'W0 three requests' ((Get-Requests '/w0/payload.bin') -eq 3)
  Check 'W0 the pauses were real (at least 3 s)' ($wall -ge 2.9) "$wall s"

  # the fake clock
  $script:fakeNow = 0.0
  function Get-MonotonicSeconds { return $script:fakeNow }
  function Start-Sleep { param([int]$Seconds, [int]$Milliseconds) $script:fakeNow += $Seconds }

  $script:fakeNow = 0.0; $msg = Invoke-Dl "$base/w1/payload.bin"
  Check 'W1 504 four times, then 200: success' ($msg -eq '') $msg
  Check 'W1 five requests' ((Get-Requests '/w1/payload.bin') -eq 5)
  Check 'W1 the payload arrived intact' ((Get-Content -LiteralPath (Join-Path $Work 'out.bin') -Raw) -eq 'payload of the retry tests')
  Check 'W1 the pauses add up to 15 s' ($script:fakeNow -eq 15) "$($script:fakeNow)"

  $script:fakeNow = 0.0; $msg = Invoke-Dl "$base/w2/payload.bin"
  Check 'W2 408 429 500 502 503, then 200: success' ($msg -eq '') $msg
  Check 'W2 six requests' ((Get-Requests '/w2/payload.bin') -eq 6)

  $script:fakeNow = 0.0; $msg = Invoke-Dl "$base/w3/payload.bin"
  Check 'W3 always 504: [download_failed] with the status, the window and the attempts' ($msg -match '^\[download_failed\] download failed: http://127\.0\.0\.1:\d+/w3/payload\.bin \(.*504.*; retry window of 120 s used up, 8 attempts\)$') $msg
  Check 'W3 exactly eight requests' ((Get-Requests '/w3/payload.bin') -eq 8)
  Check 'W3 the window is used up, not exceeded' ($script:fakeNow -ge 120 -and $script:fakeNow -le 122) "$($script:fakeNow)"

  $script:fakeNow = 0.0; $msg = Invoke-Dl "$base/w4/missing.bin"
  Check 'W4 404: [download_failed], not retried, one attempt' ($msg -match '^\[download_failed\] .*not retried, 1 attempt\)$') $msg
  Check 'W4 exactly one request' ((Get-Requests '/w4/missing.bin') -eq 1)
  Check 'W4 no pause' ($script:fakeNow -eq 0)

  $script:fakeNow = 0.0; $msg = Invoke-Dl "$base/w5/payload.bin"
  Check 'W5 403: not retried' ($msg -match 'not retried, 1 attempt\)$' -and (Get-Requests '/w5/payload.bin') -eq 1) $msg

  $script:fakeNow = 0.0; $msg = Invoke-Dl "http://127.0.0.1:$closedPort/w6/payload.bin"
  Check 'W6 refused connection: retried for the whole window' ($msg -match '^\[download_failed\] .*retry window of 120 s used up, 8 attempts\)$') $msg
  Check 'W6 the window is used up, not exceeded' ($script:fakeNow -ge 120 -and $script:fakeNow -le 122) "$($script:fakeNow)"

  # W7: a refused redirect is final (a regression guard: the https check stays outside the policy)
  $script:calls = 0
  function Invoke-WebRequest {
    param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile, [switch]$PassThru)
    $script:calls++
    return [pscustomobject]@{ BaseResponse = [pscustomobject]@{ ResponseUri = [Uri]'http://mirror.example.test/x' } }
  }
  $Insecure = [switch]$false
  $script:fakeNow = 0.0; $msg = Invoke-Dl 'https://mirror.example.test/x'
  Check 'W7 a redirect to http is refused with [https_required]' ($msg -match '^\[https_required\] redirect to http refused') $msg
  Check 'W7 and not retried (one call)' ($script:calls -eq 1)
  Remove-Item Function:\Invoke-WebRequest
  $Insecure = [switch]$true

  # W8: the classification of exceptions, including the ones of Windows PowerShell 5.1 (WebException.Status)
  if (-not (Get-Command Test-TransientDownloadError -ErrorAction SilentlyContinue)) {
    Check 'W8 Test-TransientDownloadError exists' $false
  } else {
    function Test-Transient { param($Exception) return [bool](Test-TransientDownloadError $Exception) }
    foreach ($s in 'Timeout', 'ConnectFailure', 'NameResolutionFailure', 'ReceiveFailure', 'SendFailure', 'ConnectionClosed') {
      Check "W8 WebException $s is transient" (Test-Transient ([System.Net.WebException]::new('x', [System.Net.WebExceptionStatus]$s)))
    }
    foreach ($s in 'TrustFailure', 'SecureChannelFailure', 'ProtocolError') {
      Check "W8 WebException $s is permanent" (-not (Test-Transient ([System.Net.WebException]::new('x', [System.Net.WebExceptionStatus]$s))))
    }
    $sock = [System.Net.Sockets.SocketException]::new()
    Check 'W8 HttpRequestException(SocketException) is transient' (Test-Transient ([System.Net.Http.HttpRequestException]::new('x', $sock)))
    Check 'W8 HttpRequestException(AuthenticationException) is permanent' (-not (Test-Transient ([System.Net.Http.HttpRequestException]::new('x', [System.Security.Authentication.AuthenticationException]::new('tls')))))
    Check 'W8 TaskCanceledException is transient' (Test-Transient ([System.Threading.Tasks.TaskCanceledException]::new('x')))
    Check 'W8 TimeoutException is transient' (Test-Transient ([System.TimeoutException]::new('x')))
    Check 'W8 InvalidOperationException is permanent' (-not (Test-Transient ([System.InvalidOperationException]::new('x'))))
    Check 'W8 UriFormatException is permanent' (-not (Test-Transient ([System.UriFormatException]::new('x'))))
    Check 'W8 IOException is permanent' (-not (Test-Transient ([System.IO.IOException]::new('x'))))
    foreach ($code in 408, 429, 500, 502, 503, 504) {
      $e = [Microsoft.PowerShell.Commands.HttpResponseException]::new('x', [System.Net.Http.HttpResponseMessage]::new([System.Net.HttpStatusCode]$code))
      Check "W8 HTTP $code is transient" (Test-Transient $e)
    }
    foreach ($code in 400, 401, 403, 404, 501) {
      $e = [Microsoft.PowerShell.Commands.HttpResponseException]::new('x', [System.Net.Http.HttpResponseMessage]::new([System.Net.HttpStatusCode]$code))
      Check "W8 HTTP $code is permanent" (-not (Test-Transient $e))
    }
  }
} finally {
  if ($srv -and -not $srv.HasExited) { Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue }
}

Write-Host ''
Write-Host "download-retry.ps1: $pass passed, $fail failed"
if ($fail -gt 0) { exit 1 }
