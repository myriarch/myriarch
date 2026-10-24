# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests the retry policy of the downloads in deploy/scripts/install-windows.ps1 (Invoke-Download) and
# ci/release/smoke-windows.ps1 (Invoke-DownloadWithRetry) against a local HTTP server that answers scripted faults
# (ci/tests/http-fault-server.py, 127.0.0.1, ephemeral port, started and stopped here). Counterpart of
# ci/tests/download-retry-test.sh, which tests the two bash scripts; run-all.sh calls this file in the group pwsh.
#
# The policy: a transient failure (a timeout, a failed connection, HTTP 408, 429, 500, 502, 503, 504) is retried with pauses of
# 1, 2, 4, ... s, no attempt starts later than 120 s after the first one; every other failure ends the download at once; the
# final message names the number of attempts. Most cases run on a fake clock (Get-MonotonicSeconds and Start-Sleep are replaced
# by functions), W0 runs on the real clock. The file runs under PowerShell 7 and under Windows PowerShell 5.1 (the CI job on
# Windows runs it under both): the cases that need PowerShell 7 types (HttpResponseException, HttpRequestException) and
# smoke-windows.ps1 itself, which requires PowerShell 7, are left out on 5.1 and the report says so. The cases behind a proxy
# that refuses CONNECT (HTTPS_PROXY, ci/tests/download-retry-proxy-case.ps1) run under PowerShell 7 only: Windows PowerShell 5.1
# takes its proxy from the system settings, not from HTTPS_PROXY.
param(
  [Parameter(Mandatory)][string]$Installer,
  [Parameter(Mandatory)][string]$SmokeWindows,
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
if (-not $python) { $python = Get-Command python -ErrorAction SilentlyContinue }
if (-not $python) { Write-Host 'download-retry.ps1: python3 is not available, skipped'; exit 0 }
$isCore = $PSVersionTable.PSEdition -eq 'Core'
Write-Host "download-retry.ps1: PowerShell $($PSVersionTable.PSVersion) ($($PSVersionTable.PSEdition))"

# install-windows.ps1 has a parameter -Server of its own: dot-sourcing it below sets $Server to $null in this scope. The fault server
# is therefore taken from a variable that no script under test uses, and it must exist before anything is started.
if (-not (Test-Path -LiteralPath $Server -PathType Leaf)) { throw "download-retry.ps1: -Server must be the file of the fault server, there is no file '$Server'" }
$FaultServer = (Resolve-Path -LiteralPath $Server).Path

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
  '/sw1/payload.bin' = @(504, 504, 504, 504)
  '/sw2/payload.bin' = 504
}
$rulesFile = Join-Path $Work 'rules.json'
Set-Content -LiteralPath $rulesFile -Value ($rules | ConvertTo-Json -Compress) -NoNewline
$logFile = Join-Path $Work 'server.log'
$portFile = Join-Path $Work 'port'
Set-Content -LiteralPath $logFile -Value '' -NoNewline

# One argument string, every argument in double quotes (the arguments here hold paths and fixed words, never a quote): Start-Process
# joins an array of arguments in Windows PowerShell 5.1 and PowerShell 7 alike, but a single string is the same in both.
function ConvertTo-ArgumentString { param([string[]]$Arguments) return (($Arguments | ForEach-Object { '"' + $_ + '"' }) -join ' ') }

# Starts the fault server with rules, a log and a port file; the output of the process goes to NAME.out and NAME.err.
function Start-FaultServerProcess {
  param([string]$Name, [string]$RulesPath, [string]$LogPath, [string]$PortPath)
  $outPath = Join-Path $Work "$Name.out"
  $errPath = Join-Path $Work "$Name.err"
  $arguments = ConvertTo-ArgumentString @('-I', $FaultServer, '--rules', $RulesPath, '--log', $LogPath, '--port-file', $PortPath, '--root', $served)
  $p = Start-Process -FilePath $python.Source -PassThru -RedirectStandardOutput $outPath -RedirectStandardError $errPath -ArgumentList $arguments
  $null = $p.Handle   # keeps the handle, so that ExitCode is readable after the exit (Windows PowerShell 5.1)
  return [pscustomobject]@{ Name = $Name; Process = $p; Out = $outPath; Err = $errPath; PortPath = $PortPath; Command = "$($python.Source) $arguments" }
}

function Read-ProcessOutput {
  param([string]$Path)
  try { $t = [IO.File]::ReadAllText($Path) } catch { return "(not readable: $($_.Exception.Message))" }
  if ([string]::IsNullOrWhiteSpace($t)) { return '(empty)' }
  return $t.Trim()
}

# Waits for the port file. A slow start of python on a loaded runner (several seconds) is not a failure; a process that has exited
# is, at once. The message holds the exit code, stderr and stdout of the process, so it is never empty.
function Wait-FaultServer {
  param($Started, [int]$TimeoutSeconds = 90)
  $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
  while (-not (Test-Path -LiteralPath $Started.PortPath)) {
    if ($Started.Process.HasExited -or [DateTime]::UtcNow -ge $deadline) {
      $state = $(if ($Started.Process.HasExited) { "exited with code $($Started.Process.ExitCode)" } else { "still running after $TimeoutSeconds s" })
      throw ("the fault server $($Started.Name) did not start: the process $state; command: $($Started.Command); stderr: $(Read-ProcessOutput $Started.Err); stdout: $(Read-ProcessOutput $Started.Out)")
    }
    [System.Threading.Thread]::Sleep(100)   # not Start-Sleep: the cases below replace it by a fake clock
  }
}

$firstServer = Start-FaultServerProcess -Name 'server' -RulesPath $rulesFile -LogPath $logFile -PortPath $portFile
$srv = $firstServer.Process

function Get-Requests { param([string]$Path, [string]$Log = $logFile) return @(Get-Content -LiteralPath $Log | Where-Object { $_ -like "$Path *" }).Count }
function Get-Failure { param([scriptblock]$Block) try { & $Block; return '' } catch { return $_.Exception.Message } }

# a second and third instance of the fault server, one per proxy rule: Start-FaultServer NAME STATUS returns the process, the port
# and the log of an instance that answers every CONNECT with STATUS
$proxyServers = @()
$savedEnv = $null
function Start-FaultServer {
  param([string]$Name, [int]$ConnectStatus)
  $rulesPath = Join-Path $Work "rules-$Name.json"
  $log = Join-Path $Work "$Name.log"
  $portPath = Join-Path $Work "$Name.port"
  Set-Content -LiteralPath $rulesPath -Value (@{ '/connect' = $ConnectStatus } | ConvertTo-Json -Compress) -NoNewline
  Set-Content -LiteralPath $log -Value '' -NoNewline
  $started = Start-FaultServerProcess -Name $Name -RulesPath $rulesPath -LogPath $log -PortPath $portPath
  $script:proxyServers += $started.Process
  Wait-FaultServer -Started $started
  return [pscustomobject]@{ Port = [int](Get-Content -LiteralPath $portPath -Raw).Trim(); Log = $log }
}

try {
  Wait-FaultServer -Started $firstServer
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
    if ($isCore) {
      $sock = [System.Net.Sockets.SocketException]::new()
      Check 'W8 HttpRequestException(SocketException) is transient' (Test-Transient ([System.Net.Http.HttpRequestException]::new('x', $sock)))
      Check 'W8 HttpRequestException(AuthenticationException) is permanent' (-not (Test-Transient ([System.Net.Http.HttpRequestException]::new('x', [System.Security.Authentication.AuthenticationException]::new('tls')))))
    } else {
      Write-Host '  skip  W8 HttpRequestException cases (PowerShell 7 only)'
    }
    Check 'W8 TaskCanceledException is transient' (Test-Transient ([System.Threading.Tasks.TaskCanceledException]::new('x')))
    Check 'W8 TimeoutException is transient' (Test-Transient ([System.TimeoutException]::new('x')))
    Check 'W8 InvalidOperationException is permanent' (-not (Test-Transient ([System.InvalidOperationException]::new('x'))))
    Check 'W8 UriFormatException is permanent' (-not (Test-Transient ([System.UriFormatException]::new('x'))))
    Check 'W8 IOException is permanent' (-not (Test-Transient ([System.IO.IOException]::new('x'))))
    if ($isCore) {
      foreach ($code in 408, 429, 500, 502, 503, 504) {
        $e = [Microsoft.PowerShell.Commands.HttpResponseException]::new('x', [System.Net.Http.HttpResponseMessage]::new([System.Net.HttpStatusCode]$code))
        Check "W8 HTTP $code is transient" (Test-Transient $e)
      }
      foreach ($code in 400, 401, 403, 404, 501) {
        $e = [Microsoft.PowerShell.Commands.HttpResponseException]::new('x', [System.Net.Http.HttpResponseMessage]::new([System.Net.HttpStatusCode]$code))
        Check "W8 HTTP $code is permanent" (-not (Test-Transient $e))
      }
    } else {
      Write-Host '  skip  W8 HttpResponseException cases (PowerShell 7 only)'
    }
    # a proxy that refuses CONNECT: PowerShell 7.5 and later (.NET 9) report an HttpRequestException without a response, with
    # StatusCode and HttpRequestError ProxyTunnelError; 7.4 (.NET 8) sets the error but no StatusCode. The constructor with the
    # error and the status exists from .NET 8 on, so the synthetic exceptions below run there too.
    if ($isCore) {
      $proxyError = { param([int]$Code) try { [System.Net.Http.HttpRequestException]::new([System.Net.Http.HttpRequestError]::ProxyTunnelError, 'tunnel', $null, [System.Net.HttpStatusCode]$Code) } catch { $null } }
      $e407 = & $proxyError 407
      if (-not $e407) {
        Write-Host '  skip  W8 proxy tunnel cases (HttpRequestError needs .NET 8)'
      } else {
        $e503 = & $proxyError 503
        Check 'W8 proxy CONNECT 407 (ProxyTunnelError) is permanent' (-not (Test-Transient $e407))
        Check 'W8 proxy CONNECT 503 (ProxyTunnelError) is transient' (Test-Transient $e503)
        Check 'W8 the status of a proxy refusal is read from StatusCode' ((Get-DownloadStatus $e407) -eq 407 -and (Get-DownloadStatus $e503) -eq 503)
        if (-not (Get-Command Get-DownloadReason -ErrorAction SilentlyContinue)) {
          Check 'W8 Get-DownloadReason exists' $false
        } else {
          Check 'W8 reason of a proxy refusal: proxy HTTP 407' ((Get-DownloadReason $e407) -eq 'proxy HTTP 407') (Get-DownloadReason $e407)
          Check 'W8 reason of a proxy refusal: proxy HTTP 503' ((Get-DownloadReason $e503) -eq 'proxy HTTP 503') (Get-DownloadReason $e503)
          $plain = [System.Net.Http.HttpRequestException]::new('plain', $null, [System.Net.HttpStatusCode]404)
          Check 'W8 reason of a status without a proxy error: HTTP 404' ((Get-DownloadReason $plain) -eq 'HTTP 404') (Get-DownloadReason $plain)
          $resp = [Microsoft.PowerShell.Commands.HttpResponseException]::new('x', [System.Net.Http.HttpResponseMessage]::new([System.Net.HttpStatusCode]503))
          Check 'W8 reason of a response: HTTP 503' ((Get-DownloadReason $resp) -eq 'HTTP 503') (Get-DownloadReason $resp)
          Check 'W8 reason without a status: the first line of the message' ((Get-DownloadReason ([System.TimeoutException]::new("first`nsecond"))) -eq 'first')
        }
        # the shape of .NET 8: ProxyTunnelError without a status; no status is known, so it is transient with the message as reason
        $e8 = try { [System.Net.Http.HttpRequestException]::new([System.Net.Http.HttpRequestError]::ProxyTunnelError, "tunnel`nsecond", $null, $null) } catch { $null }
        if ($e8) {
          Check 'W8 proxy tunnel error without a status (.NET 8) is transient' (Test-Transient $e8)
          Check 'W8 proxy tunnel error without a status (.NET 8) has no status' ((Get-DownloadStatus $e8) -eq 0)
          if (Get-Command Get-DownloadReason -ErrorAction SilentlyContinue) {
            Check 'W8 proxy tunnel error without a status (.NET 8): the message is the reason' ((Get-DownloadReason $e8) -eq 'tunnel') (Get-DownloadReason $e8)
          }
        }
      }
    }
  }

  # ---- smoke-windows.ps1: Invoke-DownloadWithRetry ---------------------------------------------------------------------
  # The script cannot be dot-sourced (it ends on a non-Windows host before it defines anything), so the functions are taken
  # from its syntax tree.
  Write-Host ''
  Write-Host '== smoke-windows.ps1 Invoke-DownloadWithRetry =='
  if (-not $isCore) {
    Write-Host '  skip  smoke-windows.ps1 requires PowerShell 7'
  } else {
    $tokens = $null; $errs = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($SmokeWindows, [ref]$tokens, [ref]$errs)
    $wanted = 'Invoke-DownloadWithRetry', 'Get-MonotonicSeconds', 'Get-DownloadStatus', 'Test-TransientDownloadError', 'Get-ReleaseUrl', 'Get-ReleaseFile'
    $found = @{}
    foreach ($fn in $ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] }, $true)) {
      if ($wanted -contains $fn.Name) { $found[$fn.Name] = $fn.Extent.Text }
    }
    Check 'SW0 smoke-windows.ps1 defines the download functions' ($found.Count -eq $wanted.Count) "found: $($found.Keys -join ', ')"
    if ($found.Count -eq $wanted.Count) {
      foreach ($name in $wanted) { . ([scriptblock]::Create($found[$name])) }
      function Stop-Smoke { param([string]$Code, [string]$Message) throw "[$Code] $Message" }
      function Write-Log { param([string]$Message) }
      $script:fakeNow = 0.0
      function Get-MonotonicSeconds { return $script:fakeNow }
      function Start-Sleep { param([int]$Seconds, [int]$Milliseconds) $script:fakeNow += $Seconds }
      function Invoke-Sw { param([string]$Url) Get-Failure { Invoke-DownloadWithRetry -Url $Url -Destination (Join-Path $Work 'sw.bin') | Out-Null } }

      $script:fakeNow = 0.0; $msg = Invoke-Sw "$base/sw1/payload.bin"
      Check 'SW1 504 four times, then 200: success' ($msg -eq '') $msg
      Check 'SW1 five requests' ((Get-Requests '/sw1/payload.bin') -eq 5)
      Check 'SW1 the pauses add up to 15 s' ($script:fakeNow -eq 15) "$($script:fakeNow)"
      $script:fakeNow = 0.0; $msg = Invoke-Sw "$base/sw2/payload.bin"
      Check 'SW2 always 504: [download_failed] after the window, eight attempts' ($msg -match '^\[download_failed\] download of http://127\.0\.0\.1:\d+/sw2/payload\.bin failed \(.*504.*; retry window of 120 s used up, 8 attempts\)$') $msg
      Check 'SW2 exactly eight requests' ((Get-Requests '/sw2/payload.bin') -eq 8)
      $script:fakeNow = 0.0; $msg = Invoke-Sw "$base/sw3/missing.bin"
      Check 'SW3 404: [download_failed], not retried, one attempt' ($msg -match '^\[download_failed\] .*not retried, 1 attempt\)$') $msg
      Check 'SW3 exactly one request' ((Get-Requests '/sw3/missing.bin') -eq 1)

      # SW4: the https check of Get-ReleaseFile stays outside the loop: a download that ended on http is final
      $script:Repository = 'example/repo'; $Tag = 'v9.9.9'
      function Invoke-DownloadWithRetry {
        param([string]$Url, [string]$Destination)
        return [pscustomobject]@{ BaseResponse = [pscustomobject]@{ RequestMessage = [pscustomobject]@{ RequestUri = [Uri]'http://mirror.example.test/x' } } }
      }
      $msg = Get-Failure { Get-ReleaseFile -Name 'checksums.txt' -Directory $Work | Out-Null }
      Check 'SW4 a download that ended on http is refused with [download_failed]' ($msg -match '^\[download_failed\] .*non-https') $msg
    }
  }

  # ---- behind a proxy that refuses CONNECT ----------------------------------------------------------------------------------
  # The bash scripts and PowerShell 7.5 or later (.NET 9) classify a refused CONNECT by the status of the proxy (cases X1 to X4 of
  # download-retry-test.sh); PowerShell 7.4 (.NET 8) reports no status for it and retries it like a failed connection.
  # Each case runs in a process of its own (download-retry-proxy-case.ps1) because PowerShell 7 reads HTTPS_PROXY once per process.
  Write-Host ''
  Write-Host '== a proxy that refuses CONNECT =='
  if (-not $isCore) {
    Write-Host '  skip  proxy cases (PowerShell 7 only: Windows PowerShell 5.1 takes the proxy from the system settings, not from HTTPS_PROXY)'
  } elseif ([Environment]::Version.Major -lt 9) {
    Write-Host '  skip  proxy cases (the runtime reports the proxy status from .NET 9 / PowerShell 7.5 on)'
  } else {
    $pwshExe = [Environment]::ProcessPath
    $caseScript = Join-Path $PSScriptRoot 'download-retry-proxy-case.ps1'
    $proxy503 = Start-FaultServer 'proxy503' 503
    $proxy407 = Start-FaultServer 'proxy407' 407
    $envNames = 'HTTPS_PROXY', 'https_proxy', 'NO_PROXY', 'no_proxy'
    $savedEnv = [System.Collections.Generic.Dictionary[string,string]]::new([StringComparer]::Ordinal)
    foreach ($n in $envNames) { $savedEnv[$n] = [Environment]::GetEnvironmentVariable($n) }
    function Invoke-ProxyCase {
      param([string]$Mode, [string]$ScriptPath, $ProxyServer)
      $url = 'https://example.invalid/payload.bin'
      try {
        foreach ($n in 'HTTPS_PROXY', 'https_proxy') { [Environment]::SetEnvironmentVariable($n, "http://127.0.0.1:$($ProxyServer.Port)") }
        foreach ($n in 'NO_PROXY', 'no_proxy') { [Environment]::SetEnvironmentVariable($n, $null) }
        $out = & $pwshExe -NoProfile -NonInteractive -File $caseScript -Mode $Mode -Script $ScriptPath -Url $url -Work $Work 2>&1
      } finally {
        foreach ($n in $envNames) { [Environment]::SetEnvironmentVariable($n, $savedEnv[$n]) }
      }
      $line = @($out | ForEach-Object { "$_" } | Where-Object { $_ -like 'RESULT *' }) | Select-Object -Last 1
      if (-not $line) { return [pscustomobject]@{ Message = "no result: $($out -join ' / ')"; Seconds = -1 } }
      $r = $line.Substring(7) | ConvertFrom-Json
      return [pscustomobject]@{ Message = [string]$r.message; Seconds = [double]$r.seconds }
    }
    $cases = @(
      @{ Id = 'X1'; Name = 'install-windows.ps1'; Mode = 'install'; Path = $Installer; Prefix = '^\[download_failed\] download failed: https://example\.invalid/payload\.bin \('; Status = 503; Server = $proxy503 },
      @{ Id = 'X2'; Name = 'install-windows.ps1'; Mode = 'install'; Path = $Installer; Prefix = '^\[download_failed\] download failed: https://example\.invalid/payload\.bin \('; Status = 407; Server = $proxy407 },
      @{ Id = 'X3'; Name = 'smoke-windows.ps1'; Mode = 'smoke'; Path = $SmokeWindows; Prefix = '^\[download_failed\] download of https://example\.invalid/payload\.bin failed \('; Status = 503; Server = $proxy503 },
      @{ Id = 'X4'; Name = 'smoke-windows.ps1'; Mode = 'smoke'; Path = $SmokeWindows; Prefix = '^\[download_failed\] download of https://example\.invalid/payload\.bin failed \('; Status = 407; Server = $proxy407 }
    )
    foreach ($c in $cases) {
      $before = Get-Requests '/connect' $c.Server.Log
      $r = Invoke-ProxyCase $c.Mode $c.Path $c.Server
      $count = (Get-Requests '/connect' $c.Server.Log) - $before
      $prefix = $c.Prefix
      if ($c.Status -eq 503) {
        Check "$($c.Id) $($c.Name): CONNECT 503 is retried for the whole window" ($r.Message -match ($prefix + 'proxy HTTP 503; retry window of 120 s used up, 8 attempts\)$')) $r.Message
        Check "$($c.Id) $($c.Name): eight CONNECT requests" ($count -eq 8) "$count"
        Check "$($c.Id) $($c.Name): the window is used up, not exceeded" ($r.Seconds -ge 120 -and $r.Seconds -le 122) "$($r.Seconds)"
      } else {
        Check "$($c.Id) $($c.Name): CONNECT 407 is not retried" ($r.Message -match ($prefix + 'proxy HTTP 407; not retried, 1 attempt\)$')) $r.Message
        Check "$($c.Id) $($c.Name): one CONNECT request" ($count -eq 1) "$count"
        Check "$($c.Id) $($c.Name): no pause" ($r.Seconds -eq 0) "$($r.Seconds)"
      }
    }
  }
} finally {
  if ($srv -and -not $srv.HasExited) { Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue }
  foreach ($p in $proxyServers) { if ($p -and -not $p.HasExited) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } }
  if ($savedEnv) { foreach ($n in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($n, $savedEnv[$n]) } }
}

Write-Host ''
Write-Host "download-retry.ps1: $pass passed, $fail failed"
if ($fail -gt 0) { exit 1 }
