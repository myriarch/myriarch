// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The preflight checks of deploy/scripts/install-windows.ps1: the language mode, the services of the upstream packages, the
// supervisor file and the architecture. They need PowerShell, which the tests find like the staging tests of
// deploy_pwsh_test.go (MYRIARCH_PWSH or pwsh in PATH) and skip without. The checks that need Windows services or the
// registry run against functions that stand in for Get-Service and Get-ItemProperty, so they also run on Linux and macOS.

// runPwshCommand runs a PowerShell command text in a new process and returns the combined output. The values go in through
// environment variables, so that no quoting of a path can change the command.
func runPwshCommand(t *testing.T, command string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(pwshPath(t), "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// pwshChecksPrologue starts every test script of runPwshChecks: it dot-sources the installer (which then runs no main code) with
// the arguments of a complete installation, and offers Check and Message-Of. The test scripts replace the functions of the
// installer that need Windows with functions of their own; nothing here touches a service, the registry or a path outside
// $Work.
const pwshChecksPrologue = `
param([Parameter(Mandatory)][string]$Installer, [Parameter(Mandatory)][string]$Templates, [Parameter(Mandatory)][string]$Work)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$tokenFile = Join-Path $Work 'enrollment-token'
Set-Content -LiteralPath $tokenFile -Value 'myre_abc123' -NoNewline
$verifierFile = Join-Path $Work 'verifier'
Set-Content -LiteralPath $verifierFile -Value 'x' -NoNewline
$installerArgs = @{ OpampEndpoint = 'wss://fleet.example.com/v1/opamp'; TokenFile = $tokenFile; SiteType = 'edge'; SiteId = '1'
  CollectorVersion = '0.162.0'; Arch = 'amd64'; TemplatesDir = $Templates; Verifier = $verifierFile }
. $Installer @installerArgs
$ErrorActionPreference = 'Stop'
$script:failures = 0
$script:passes = 0
function Check { param([string]$Title, [bool]$Ok) if ($Ok) { $script:passes++; Write-Host "  ok    $Title" } else { $script:failures++; Write-Host "  FAIL  $Title" } }
function Message-Of { param([scriptblock]$Block) try { & $Block; return '' } catch { return $_.Exception.Message } }
`

// pwshChecksEpilogue ends the script with the counts that the Go test reads.
const pwshChecksEpilogue = `
Write-Host "checks: $($script:passes) ok, $($script:failures) failed"
if ($script:failures -gt 0) { exit 1 }
`

// runPwshChecks runs the body of a PowerShell test script behind pwshChecksPrologue and fails the test when a Check failed. It
// returns the output and the number of checks that passed, so that a test can see that its checks ran.
func runPwshChecks(t *testing.T, body string) (string, int) {
	t.Helper()
	pwsh := pwshPath(t)
	installer, err := filepath.Abs(filepath.Join(deployDir, "scripts", "install-windows.ps1"))
	require.NoError(t, err)
	templates, err := filepath.Abs(deployDir)
	require.NoError(t, err)
	work := t.TempDir()
	script := filepath.Join(t.TempDir(), "checks.ps1")
	require.NoError(t, os.WriteFile(script, []byte(pwshChecksPrologue+body+pwshChecksEpilogue), 0o600))
	cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", script, "-Installer", installer, "-Templates", templates, "-Work", work)
	cmd.Dir = work
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	t.Log("\n" + out.String())
	require.NoError(t, runErr, "the checks ran:\n%s", out.String())
	assert.NotContains(t, out.String(), "FAIL", out.String())
	m := regexp.MustCompile(`checks: (\d+) ok, 0 failed`).FindStringSubmatch(out.String())
	require.NotNil(t, m, "the script reached its end:\n%s", out.String())
	n, convErr := strconv.Atoi(m[1])
	require.NoError(t, convErr)
	return out.String(), n
}

// pwshFlowSpies makes Invoke-Main run as an administrator on a host where nothing is installed (Get-Service finds no service
// unless the script defines it) and replaces every function that creates, fetches, stops or installs something by one that
// records its call and fails the run. A check that comes too late shows as "unexpected call of <function>".
const pwshFlowSpies = `
$script:Calls = New-Object System.Collections.Generic.List[string]
function Test-Administrator { return $true }
foreach ($name in @('New-WorkDirectory', 'Invoke-Download', 'Stop-ServiceAndWait', 'Resolve-Verifier', 'Get-UpdaterDownload', 'Initialize-DataDirectories',
    'Install-Collector', 'Install-SupervisorBinary', 'Install-SupervisorService')) {
  Set-Item -Path "function:$name" -Value ([scriptblock]::Create("param() [void]` + "`" + `$script:Calls.Add('$name'); throw ('unexpected call of $name')"))
}
`

// installerLines returns the lines of the output that carry the prefix of an installer message.
func installerLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(l, "[myriarch-install]") {
			lines = append(lines, l)
		}
	}
	return lines
}

// A session that was switched to Constrained Language Mode runs every script in that mode, which is what App Control for
// Business does to a script that no rule allows. The installer has to say so before its first method call fails, and it has to
// print the message although the host object refuses method calls in that mode.
func TestInstallWindowsScriptStopsFirstInConstrainedLanguageMode(t *testing.T) {
	script, err := filepath.Abs(filepath.Join(deployDir, "scripts", "install-windows.ps1"))
	require.NoError(t, err)
	templates, err := filepath.Abs(deployDir)
	require.NoError(t, err)
	relDir, _, _ := newFakeWindowsRelease(t, "0.162.0")
	root := t.TempDir()

	// the arguments are complete and point at a working mirror: without the check the run would install into root
	command := "$ExecutionContext.SessionState.LanguageMode = 'ConstrainedLanguage'; " +
		"& $env:MYRIARCH_TEST_SCRIPT -Root $env:MYRIARCH_TEST_ROOT -Arch amd64 -TemplatesDir $env:MYRIARCH_TEST_TEMPLATES " +
		"-ReleaseBaseUrl $env:MYRIARCH_TEST_RELEASE -Insecure -VerifySignatures skip " +
		"-OpampEndpoint wss://fleet.example.com/v1/opamp -Token myre_abc123 -SiteType edge -SiteId 1 -CollectorVersion 0.162.0; " +
		"exit $LASTEXITCODE"
	out, err := runPwshCommand(t, command,
		"MYRIARCH_TEST_SCRIPT="+script, "MYRIARCH_TEST_ROOT="+root, "MYRIARCH_TEST_TEMPLATES="+templates,
		"MYRIARCH_TEST_RELEASE=file://"+filepath.ToSlash(relDir))
	require.Error(t, err, "the installer stops: %s", out)

	lines := installerLines(out)
	require.Len(t, lines, 1, "exactly one prefixed line: %s", out)
	assert.Regexp(t, `^\[myriarch-install\] ERROR \[language_mode_unsupported\] \S`, lines[0])
	assert.Contains(t, lines[0], "install-windows.ps1", "the message names the file")
	assert.Contains(t, lines[0], "ConstrainedLanguage", "the message names the mode")
	assert.Contains(t, lines[0], "hash rule", "the message names the rule that lets the file run")
	assert.NotContains(t, out, "Cannot invoke method", "the check comes before the first method call, and the message needs none")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was installed or created")
	assert.NotContains(t, out, "downloading")
}

// The services that the MSI packages of the upstream project register (opampsupervisor, otelcol-contrib) stop the installer
// before it does anything. The tests run with a Get-Service that lists the services of a made-up host.
func TestInstallWindowsScriptStopsAtServicesOfTheUpstreamPackages(t *testing.T) {
	out, n := runPwshChecks(t, `
$script:Existing = @{}
$script:GetServiceCalls = 0
function Get-Service {
  [CmdletBinding()] param([string]$Name)
  $script:GetServiceCalls++
  if ($script:Existing.ContainsKey($Name)) { return [pscustomobject]@{ Name = $Name; Status = $script:Existing[$Name] } }
}
$script:StagingMode = $false

$script:Existing = @{ 'otelcol-supervisor' = 'Running'; 'myriarch-updater' = 'Running' }
Check 'the services of myriarch itself are no conflict' ((Message-Of { Assert-NoConflictingInstall }) -eq '')

$script:Existing = @{}
Check 'a host without any service is no conflict' ((Message-Of { Assert-NoConflictingInstall }) -eq '')

$script:Existing = @{ 'opampsupervisor' = 'Stopped' }
$msg = Message-Of { Assert-NoConflictingInstall }
Check 'the supervisor MSI service stops the run with conflicting_install' ($msg -match '^\[conflicting_install\] ')
Check 'the message names the service and its state' ($msg.Contains('service opampsupervisor (Stopped)'))
Check 'the message does not name the other service' (-not $msg.Contains('otelcol-contrib'))
Check 'the message names the next step and that nothing is removed' ($msg.Contains('msiexec /x') -and $msg.Contains('removes nothing'))

$script:Existing = @{ 'otelcol-contrib' = 'Running' }
$msg = Message-Of { Assert-NoConflictingInstall }
Check 'the contrib MSI service stops the run with conflicting_install' ($msg -match '^\[conflicting_install\] ' -and $msg.Contains('service otelcol-contrib (Running)'))

$script:Existing = @{ 'otelcol-contrib' = 'Running'; 'opampsupervisor' = 'Stopped'; 'otelcol-supervisor' = 'Running' }
$msg = Message-Of { Assert-NoConflictingInstall }
Check 'both services are named in one message' ($msg.Contains('service opampsupervisor (Stopped), service otelcol-contrib (Running)'))

# a staging root touches no service and reads none
$script:StagingMode = $true
$before = $script:GetServiceCalls
Check 'staging mode neither reads nor reports services' ((Message-Of { Assert-NoConflictingInstall }) -eq '' -and $script:GetServiceCalls -eq $before)
`)
	assert.GreaterOrEqual(t, n, 9, out)
}

// The whole run stops at the services of an upstream package before it creates the work directory, downloads, stops a service
// or writes a file: the functions that do those things are replaced by ones that fail the run when they are called.
func TestInstallWindowsScriptStopsAtAnUpstreamServiceBeforeItDoesAnything(t *testing.T) {
	out, n := runPwshChecks(t, pwshFlowSpies+`
function Get-Service {
  [CmdletBinding()] param([string]$Name)
  if ($Name -eq 'opampsupervisor') { return [pscustomobject]@{ Name = $Name; Status = 'Running' } }
}
Check 'a normal run is not in staging mode' (-not [bool]$Root)
$msg = Message-Of { Invoke-Main }
Check 'the run stops with conflicting_install' ($msg -match '^\[conflicting_install\] ' -and $msg.Contains('service opampsupervisor (Running)'))
Check 'no work directory, download, service stop, verifier or install step was reached' ($script:Calls.Count -eq 0)
Check 'nothing was written to the working directory' (@(Get-ChildItem -LiteralPath $Work -Force).Count -eq 2)   # the token file and the verifier file of this script
`)
	assert.GreaterOrEqual(t, n, 4, out)
}

// The supervisor must be a regular file with the verified hash at the path of the service after its installation step. The
// installation step is run with stand-ins for the download and the copy; the copy removes the file, as a security tool would.
func TestInstallWindowsScriptReportsAMissingSupervisorFile(t *testing.T) {
	out, n := runPwshChecks(t, `
$dir = Join-Path $Work 'supervisor'
New-Item -ItemType Directory -Path $dir | Out-Null
$exe = Join-Path $dir 'opampsupervisor.exe'
$good = ('b' * 64)

# Assert-SupervisorBinary
$msg = Message-Of { Assert-SupervisorBinary -Path $exe -ExpectedHash $good }
Check 'a missing file is supervisor_missing' ($msg -match '^\[supervisor_missing\] ')
Check 'the message says that the file does not exist and names the path' ($msg.Contains('the supervisor binary does not exist: ' + $exe))
Check 'the message names the next step: the protection history of the antivirus, and the folder to allow' ($msg.Contains('protection history') -and $msg.Contains('allow the folder ' + $dir))

New-Item -ItemType Directory -Path $exe | Out-Null
$msg = Message-Of { Assert-SupervisorBinary -Path $exe }
Check 'a directory is no regular file' ($msg -match '^\[supervisor_missing\] ' -and $msg.Contains('is not a regular file: ' + $exe))
Remove-Item -LiteralPath $exe -Force

if (-not $IsWindows) {
  $target = Join-Path $dir 'target'
  Set-Content -LiteralPath $target -Value 'x' -NoNewline
  New-Item -ItemType SymbolicLink -Path $exe -Target $target | Out-Null
  $msg = Message-Of { Assert-SupervisorBinary -Path $exe }
  Check 'a link is no regular file' ($msg -match '^\[supervisor_missing\] ' -and $msg.Contains('is not a regular file'))
  Remove-Item -LiteralPath $exe -Force
}

Set-Content -LiteralPath $exe -Value 'MZ-fake' -NoNewline
$hash = Get-Sha256 -Path $exe
Check 'a file with the verified hash passes' ((Message-Of { Assert-SupervisorBinary -Path $exe -ExpectedHash $hash }) -eq '')
Check 'a file passes when no hash is known (an installed supervisor)' ((Message-Of { Assert-SupervisorBinary -Path $exe }) -eq '')
$msg = Message-Of { Assert-SupervisorBinary -Path $exe -ExpectedHash $good }
Check 'a file with another hash is not the verified supervisor' ($msg -match '^\[supervisor_missing\] ' -and $msg.Contains('is not the file that was verified'))
Remove-Item -LiteralPath $exe -Force

# Install-SupervisorBinary with stand-ins for the download, the signature check and the copy
$root = Join-Path $Work 'root'
$L = Get-Layout -Prefix $root
$script:StagingMode = $true
$script:SupervisorVersion = '0.162.0'
$script:ReleaseBaseUrl = 'https://mirror.example.com/r'
$tmp = Join-Path $Work 'tmp'
New-Item -ItemType Directory -Path $tmp | Out-Null
$target = Join-Path $L.Supervisor 'opampsupervisor.exe'
function Get-VerifiedAsset { param($Url, $Destination) Set-Content -LiteralPath $Destination -Value 'MZ-fake-supervisor' -NoNewline; return (Get-Sha256 -Path $Destination) }
function Confirm-Signature { param($File, $BundleUrl, $IdentityRegexp, $Version, $TmpDir) }
$script:Quarantine = $true
function Copy-VerifiedFile {
  param($Source, $Destination, $ExpectedHash)
  Copy-Item -LiteralPath $Source -Destination $Destination -Force
  if ($script:Quarantine) { Remove-Item -LiteralPath $Destination -Force }
}
$msg = Message-Of { [void](Install-SupervisorBinary -L $L -Arch 'amd64' -TmpDir $tmp) }
Check 'a file that is gone right after the copy stops the installation step' ($msg -match '^\[supervisor_missing\] ' -and $msg.Contains($target))

$script:Quarantine = $false
$changed = Install-SupervisorBinary -L $L -Arch 'amd64' -TmpDir $tmp
Check 'an intact installation step reports a change and leaves the file' ($changed -eq $true -and (Test-Path -LiteralPath $target -PathType Leaf))
$changed = Install-SupervisorBinary -L $L -Arch 'amd64' -TmpDir $tmp
Check 'a repeated step finds the installed supervisor' ($changed -eq $false)
Remove-Item -LiteralPath $target -Force
New-Item -ItemType Directory -Path $target | Out-Null
$msg = Message-Of { [void](Install-SupervisorBinary -L $L -Arch 'amd64' -TmpDir $tmp) }
Check 'an installed supervisor that is no file is reported too' ($msg -match '^\[supervisor_missing\] ' -and $msg.Contains('is not a regular file: ' + $target))
`)
	assert.GreaterOrEqual(t, n, 11, out)
}

// A service that is not running is explained by the file of its binPath when that is gone: the path comes from the registry
// entry of the service (a stand-in for Get-ItemProperty here), not from the layout of the installer.
func TestInstallWindowsScriptLooksAtTheBinaryOfAServiceThatIsNotRunning(t *testing.T) {
	out, n := runPwshChecks(t, `
$script:ImagePath = $null
function Get-ItemProperty {
  [CmdletBinding()] param([string]$Path, [string]$Name)
  if ($null -ne $script:ImagePath) { return [pscustomobject]@{ ImagePath = $script:ImagePath } }
}
$dir = Join-Path $Work 'bin'
New-Item -ItemType Directory -Path $dir | Out-Null
$exe = Join-Path $dir 'opampsupervisor.exe'
$fallback = Join-Path $dir 'fallback.exe'
Set-Content -LiteralPath $fallback -Value 'x' -NoNewline

$script:ImagePath = '"' + $exe + '" --config "C:\ProgramData\Myriarch\config\supervisor.yaml"'
Check 'a quoted binPath is read from the registry entry' ((Get-ServiceBinaryPath -Name 'otelcol-supervisor' -Default $fallback) -eq $exe)
$msg = Message-Of { [void](Assert-ServiceBinaryPresent -Name 'otelcol-supervisor' -Default $fallback) }
Check 'a missing file of the binPath is supervisor_missing' ($msg -match '^\[supervisor_missing\] ')
Check 'the message names the service, the path of the binPath (not the default) and the next step' ($msg.Contains('the service otelcol-supervisor is not running') -and $msg.Contains($exe) -and -not $msg.Contains($fallback) -and $msg.Contains('protection history'))

Set-Content -LiteralPath $exe -Value 'MZ-fake' -NoNewline
Check 'an existing file is returned and raises nothing' ((Assert-ServiceBinaryPresent -Name 'otelcol-supervisor' -Default $fallback) -eq $exe)

$script:ImagePath = $exe + ' --config C:\x.yaml'
Check 'an unquoted binPath is read up to .exe' ((Get-ServiceBinaryPath -Name 'otelcol-supervisor' -Default $fallback) -eq $exe)
$script:ImagePath = $null
Check 'without a registry entry the default is used' ((Get-ServiceBinaryPath -Name 'otelcol-supervisor' -Default $fallback) -eq $fallback)
Check 'and it is checked like any other' ((Assert-ServiceBinaryPresent -Name 'otelcol-supervisor' -Default $fallback) -eq $fallback)
`)
	assert.GreaterOrEqual(t, n, 7, out)
}

// An ARM64 host without -Arch amd64 stops as soon as the architecture is known: before the work directory, a download, the
// verifier and the stop of a service. The installer of v0.1.0 stopped at the supervisor step, after it had stopped the service
// and installed the ARM64 collector.
func TestInstallWindowsScriptStopsOnArm64BeforeItDoesAnything(t *testing.T) {
	out, n := runPwshChecks(t, pwshFlowSpies+`
function Get-Service { [CmdletBinding()] param([string]$Name) }

# the architecture of the host decides when -Arch is not given
$Arch = ''
$env:PROCESSOR_ARCHITEW6432 = ''
$env:PROCESSOR_ARCHITECTURE = 'ARM64'
$msg = Message-Of { Invoke-Main }
Check 'an ARM64 host without -Arch stops with unsupported_platform' ($msg -match '^\[unsupported_platform\] ')
Check 'the message names the way out' ($msg.Contains('-Arch amd64'))
Check 'no work directory, download, service stop, verifier or install step was reached' ($script:Calls.Count -eq 0)

$Arch = 'arm64'
$env:PROCESSOR_ARCHITECTURE = 'AMD64'
$msg = Message-Of { Invoke-Main }
Check 'an explicit -Arch arm64 stops the same way' ($msg -match '^\[unsupported_platform\] ' -and $script:Calls.Count -eq 0)

# amd64, given or detected, gets past the check: the run reaches the work directory, where the spy stops it
foreach ($case in @(@('amd64', 'AMD64'), @('', 'AMD64'))) {
  $Arch = $case[0]
  $env:PROCESSOR_ARCHITECTURE = $case[1]
  $msg = Message-Of { Invoke-Main }
  Check "amd64 passes the check (-Arch '$($case[0])', host $($case[1]))" ($msg -eq 'unexpected call of New-WorkDirectory')
}
`)
	assert.GreaterOrEqual(t, n, 6, out)
}

// The same through the real script in staging mode, where the fake mirror has no arm64 collector: the run ends at the
// architecture, not at a download that fails, and the install root stays empty.
func TestInstallWindowsScriptStagingRunStopsOnArm64WithoutDownloading(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, _, _ := newFakeWindowsRelease(t, "0.162.0")
	root := t.TempDir()
	args := append([]string{"-NoProfile", "-NonInteractive", "-File", filepath.Join(deployDir, "scripts", "install-windows.ps1"),
		"-Root", root, "-Arch", "arm64", "-ReleaseBaseUrl", "file://" + relDir, "-TemplatesDir", deployDir, "-Insecure", "-VerifySignatures", "skip"},
		windowsGoodArgs()...)
	cmd := exec.Command(pwshPath(t), args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	require.Error(t, err, out)
	assert.Regexp(t, `(?m)^\[myriarch-install\] ERROR \[unsupported_platform\] \S`, out)
	assert.Contains(t, out, "-Arch amd64")
	assert.NotContains(t, out, "downloading")
	entries, rerr := os.ReadDir(root)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "nothing is created below the root")
}
