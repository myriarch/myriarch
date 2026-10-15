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
	out, n := runPwshChecks(t, `
$script:Calls = New-Object System.Collections.Generic.List[string]
function Get-Service {
  [CmdletBinding()] param([string]$Name)
  if ($Name -eq 'opampsupervisor') { return [pscustomobject]@{ Name = $Name; Status = 'Running' } }
}
function Test-Administrator { return $true }
foreach ($name in @('New-WorkDirectory', 'Invoke-Download', 'Stop-ServiceAndWait', 'Resolve-Verifier', 'Get-UpdaterDownload', 'Initialize-DataDirectories',
    'Install-Collector', 'Install-SupervisorBinary', 'Install-SupervisorService')) {
  Set-Item -Path "function:$name" -Value ([scriptblock]::Create("param() [void]`+"`"+`$script:Calls.Add('$name'); throw ('unexpected call of $name')"))
}
Check 'a normal run is not in staging mode' (-not [bool]$Root)
$msg = Message-Of { Invoke-Main }
Check 'the run stops with conflicting_install' ($msg -match '^\[conflicting_install\] ' -and $msg.Contains('service opampsupervisor (Running)'))
Check 'no work directory, download, service stop, verifier or install step was reached' ($script:Calls.Count -eq 0)
Check 'nothing was written to the working directory' (@(Get-ChildItem -LiteralPath $Work -Force).Count -eq 2)   # the token file and the verifier file of this script
`)
	assert.GreaterOrEqual(t, n, 4, out)
}
