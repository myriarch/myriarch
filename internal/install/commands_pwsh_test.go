// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
)

// These tests run the command for Windows in PowerShell 7 on the machine of the test, with stand-ins for the natives
// (whoami.exe, icacls.exe, powershell.exe) and for the cmdlets that need Windows (Get-ExecutionPolicy, Unblock-File,
// Invoke-WebRequest, Expand-Archive). They check the order of the steps, what is passed to the natives, what is left,
// and that the command runs in Constrained Language Mode, which App Control puts a typed command in. Windows PowerShell
// 5.1 has the same cmdlets and parameters; its Constrained Language Mode is the one that Windows enforces, and the
// end-to-end test on a Windows host (scripts/host-e2e) runs the command there.

// findPwsh finds PowerShell 7: MYRIARCH_PWSH, the pinned tool of the repository (make tools), then the PATH.
func findPwsh(t *testing.T) string {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("the stand-ins of the test are for a Unix host with PowerShell 7; the command runs on Windows in the end-to-end test of the hosts")
	}
	if p := os.Getenv("MYRIARCH_PWSH"); p != "" {
		return p
	}
	for _, dir := range []string{os.Getenv("TOOLS_BIN"), filepath.Join("..", "..", ".cache", "bin")} {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, "pwsh"); fileExists(p) {
			return p
		}
	}
	p, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is not installed: the PowerShell command is not run (make tools installs it)")
	}
	return p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// pwshHarness is the PowerShell part of a test: it defines the stand-ins, optionally locks the session into
// Constrained Language Mode, and runs the command that the test wrote to a file.
const pwshHarness = `
$ErrorActionPreference = 'Stop'
$Log = $env:PS_LOG
function Log([string]$line) { Add-Content -LiteralPath $Log -Value $line }
# a path in the form of Windows (C:\Secrets\token.txt) is a file below $env:PS_WINROOT on the machine of the test
function Map-Path([string]$p) {
  if ($p -like 'C:\*') { return (Join-Path $env:PS_WINROOT ($p.Substring(3) -replace '\\', '/')) }
  return $p
}
function Test-Path {
  param($LiteralPath, $PathType)
  if ($PathType) { return (Microsoft.PowerShell.Management\Test-Path -LiteralPath (Map-Path $LiteralPath) -PathType $PathType) }
  return (Microsoft.PowerShell.Management\Test-Path -LiteralPath (Map-Path $LiteralPath))
}
function Get-Content {
  param($LiteralPath, [switch]$Raw)
  return (Microsoft.PowerShell.Management\Get-Content -LiteralPath (Map-Path $LiteralPath) -Raw:$Raw)
}
# the groups of the token as whoami.exe /groups lists them; the sessions differ in the mandatory label (MOCK_INTEGRITY: high,
# the default, is an elevated administrator; system is a session of SYSTEM; medium is an administrator who is not elevated,
# whose group Administrators is for deny only; low)
function whoami.exe {
  Log ('whoami ' + ($args -join ' '))
  'Everyone                       Well-known group S-1-1-0     Mandatory group, Enabled by default, Enabled group'
  switch ($env:MOCK_INTEGRITY) {
    'system' {
      'NT AUTHORITY\SYSTEM            Well-known group S-1-5-18    Mandatory group, Enabled by default, Enabled group, Group owner'
      'BUILTIN\Administrators         Alias            S-1-5-32-544 Mandatory group, Enabled by default, Enabled group, Group owner'
      'Mandatory Label\System Mandatory Level  Label  S-1-16-16384'
    }
    'medium' {
      'BUILTIN\Administrators         Alias            S-1-5-32-544 Group used for deny only'
      'Mandatory Label\Medium Mandatory Level  Label  S-1-16-8192'
    }
    'low' { 'Mandatory Label\Low Mandatory Level  Label  S-1-16-4096' }
    default {
      'BUILTIN\Administrators         Alias            S-1-5-32-544 Mandatory group, Enabled by default, Enabled group, Group owner'
      'Mandatory Label\High Mandatory Level  Label  S-1-16-12288'
    }
  }
}
function Get-ExecutionPolicy {
  param([switch]$List)
  Log 'Get-ExecutionPolicy -List'
  $m = 'Undefined'; if ($env:MOCK_GPO) { $m = $env:MOCK_GPO }
  $u = 'Undefined'; if ($env:MOCK_GPO_USER) { $u = $env:MOCK_GPO_USER }
  @(
    [pscustomobject]@{ Scope = 'MachinePolicy'; ExecutionPolicy = $m },
    [pscustomobject]@{ Scope = 'UserPolicy'; ExecutionPolicy = $u },
    [pscustomobject]@{ Scope = 'Process'; ExecutionPolicy = 'Restricted' },
    [pscustomobject]@{ Scope = 'LocalMachine'; ExecutionPolicy = 'Restricted' }
  )
}
function icacls.exe {
  Log ('icacls ' + ($args -join ' '))
  $global:LASTEXITCODE = 0
  if ($env:MOCK_ICACLS_FAIL -eq '1') { $global:LASTEXITCODE = 5 }
  if ($env:MOCK_SETOWNER_FAIL -eq '1' -and $args -contains '/setowner') { $global:LASTEXITCODE = 5 }
}
function Set-Content {
  param($LiteralPath, $Value, [switch]$NoNewline)
  Log ('set-content ' + $LiteralPath)
  Microsoft.PowerShell.Management\Set-Content -LiteralPath $LiteralPath -Value $Value -NoNewline:$NoNewline
}
function Invoke-WebRequest {
  param($Uri, $Headers, $OutFile, $MaximumRedirection, [switch]$UseBasicParsing)
  Log ('iwr ' + $Uri + ' basic=' + $UseBasicParsing.IsPresent + ' redirects=' + $MaximumRedirection)
  Add-Content -LiteralPath ($Log + '.auth') -Value $Headers.Authorization
  if ($env:MOCK_IWR_FAIL -eq '1') { throw 'The remote server returned an error: (401) Unauthorized.' }
  Copy-Item -LiteralPath $env:PS_FIXTURE -Destination $OutFile
}
function Expand-Archive {
  param($LiteralPath, $DestinationPath)
  Log ('expand ' + $LiteralPath + ' -> ' + $DestinationPath)
  New-Item -ItemType Directory -Path $DestinationPath | Out-Null
}
function Unblock-File { param($LiteralPath) Log ('unblock ' + $LiteralPath) }
function powershell.exe {
  Log ('powershell ' + ($args -join ' | '))
  for ($i = 0; $i -lt $args.Count - 1; $i++) {
    if ($args[$i] -eq '-TokenFile') {
      Copy-Item -LiteralPath (Map-Path $args[$i + 1]) -Destination ($Log + '.token')
      Log ('token-file-dir ' + (Split-Path -Parent $args[$i + 1]))
    }
  }
  $global:LASTEXITCODE = 0
  if ($env:MOCK_INSTALLER_EXIT) { $global:LASTEXITCODE = [int]$env:MOCK_INSTALLER_EXIT }
}
if ($env:PS_CLM -eq '1') { $ExecutionContext.SessionState.LanguageMode = 'ConstrainedLanguage' }
Log ('mode ' + $ExecutionContext.SessionState.LanguageMode)
. $env:PS_BODY
`

type pwshRun struct {
	out  string
	code int
	log  []string
}

type pwshEnv struct {
	t        *testing.T
	pwsh     string
	dir      string
	programs string // $env:ProgramFiles
	fixture  []byte
}

func newPwshEnv(t *testing.T) *pwshEnv {
	t.Helper()
	e := &pwshEnv{t: t, pwsh: findPwsh(t), dir: t.TempDir(), fixture: []byte("PK-fake-zip-of-the-installer-bundle")}
	e.programs = filepath.Join(e.dir, "ProgramFiles")
	require.NoError(t, os.MkdirAll(e.programs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(e.dir, "harness.ps1"), []byte(pwshHarness), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(e.dir, "fixture.zip"), e.fixture, 0o644))
	return e
}

func windowsName() string { return BundleFilename("0.2.0", Platform{"windows", "amd64"}) }

// command renders the command for windows/amd64 with the bundle that is the fixture, and puts the token in.
func (e *pwshEnv) command(req CommandsRequest, token string, mut ...func(d *Deps, s *stubBundles)) string {
	e.t.Helper()
	ce := newCommandsEnv(e.t, func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "windows" {
				s.list.Items[i].SHA256 = sumHex(e.fixture)
			}
		}
		for _, m := range mut {
			m(d, s)
		}
	})
	req.Platforms = []string{"windows/amd64"}
	c, err := ce.render(req)
	require.NoError(e.t, err)
	require.Len(e.t, c.Commands, 1)
	return strings.ReplaceAll(c.Commands[0].Command, brand.EnrollmentTokenPlaceholder, token)
}

func (e *pwshEnv) run(command string, clm bool, env ...string) pwshRun {
	e.t.Helper()
	body := filepath.Join(e.dir, "body.ps1")
	require.NoError(e.t, os.WriteFile(body, []byte(command), 0o644))
	logFile := filepath.Join(e.dir, "log.txt")
	for _, f := range []string{logFile, logFile + ".auth", logFile + ".token"} {
		_ = os.Remove(f)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.pwsh, "-NoProfile", "-NonInteractive", "-File", filepath.Join(e.dir, "harness.ps1"))
	clmFlag := "0"
	if clm {
		clmFlag = "1"
	}
	cmd.Env = append(os.Environ(), "PS_LOG="+logFile, "PS_BODY="+body, "PS_CLM="+clmFlag, "PS_FIXTURE="+filepath.Join(e.dir, "fixture.zip"), "ProgramFiles="+e.programs, "PS_WINROOT="+filepath.Join(e.dir, "winroot"), "NO_COLOR=1")
	cmd.Env = append(cmd.Env, env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		require.ErrorAs(e.t, err, &ee, out.String())
		code = ee.ExitCode()
	}
	var lines []string
	if b, err := os.ReadFile(logFile); err == nil {
		lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
	return pwshRun{out: stripANSI(out.String()), code: code, log: lines}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

func (e *pwshEnv) file(name string) string {
	b, err := os.ReadFile(filepath.Join(e.dir, "log.txt"+name))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(e.t, err)
	return string(b)
}

// workDirs lists what is left of the private directories below ProgramFiles.
func (e *pwshEnv) workDirs() []string {
	entries, err := os.ReadDir(e.programs)
	require.NoError(e.t, err)
	var out []string
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

func (r pwshRun) called(prefix string) []string {
	var out []string
	for _, l := range r.log {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (r pwshRun) index(prefix string) int {
	for i, l := range r.log {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

// theTokenIsNowhere checks that the token is in no log line (which includes the arguments of every native), in the
// output, or in a file that is left behind.
func (e *pwshEnv) theTokenIsNowhere(token string, r pwshRun) {
	e.t.Helper()
	assert.NotContains(e.t, strings.Join(r.log, "\n"), token, "the log: arguments of the natives and of the cmdlets")
	assert.NotContains(e.t, r.out, token, "the output")
	assert.Empty(e.t, e.workDirs(), "the working directory is removed with the token file")
}

func TestPowerShellCommandInstallsTheHost(t *testing.T) {
	for _, clm := range []bool{false, true} {
		name := "full language"
		if clm {
			name = "constrained language"
		}
		t.Run(name, func(t *testing.T) {
			e := newPwshEnv(t)
			r := e.run(e.command(fullRequest(VariantTokenInline), testToken), clm)
			require.Equal(t, 0, r.code, r.out)
			if clm {
				assert.Contains(t, r.log, "mode ConstrainedLanguage", "the command ran in Constrained Language Mode")
			} else {
				assert.Contains(t, r.log, "mode FullLanguage")
			}

			// the steps, in order: the checks first, the directory restricted before anything is written or downloaded
			order := []string{"whoami /groups", "Get-ExecutionPolicy -List", "icacls ", "set-content ", "iwr ", "expand ", "unblock ", "powershell "}
			last := -1
			for _, prefix := range order {
				i := r.index(prefix)
				require.Greater(t, i, last, "%s comes after the step before it: %v", prefix, r.log)
				last = i
			}
			work := strings.Fields(r.called("icacls ")[0])[1]
			assert.True(t, strings.HasPrefix(work, filepath.Join(e.programs, brand.InstallSetupDir+"-")), work)
			assert.Equal(t, []string{
				"icacls " + work + " /inheritance:r /grant:r *S-1-5-18:(OI)(CI)F *S-1-5-32-544:(OI)(CI)F",
				"icacls " + work + " /setowner *S-1-5-32-544",
			}, r.called("icacls "), "the directory is restricted and owned by the administrators before anything is made in it")
			assert.Less(t, r.index("icacls "), r.index("set-content "))
			assert.Equal(t, []string{"iwr https://fleet.example.com/api/v1/install/bundles/" + windowsName() + " basic=True redirects=0"}, r.called("iwr "))
			assert.Equal(t, "Bearer "+testToken+"\n", e.file(".auth"), "the token travels in a header")

			// the installer is started for one process with RemoteSigned, never with Bypass, and with the token file
			ps := r.called("powershell ")
			require.Len(t, ps, 1)
			args := strings.Split(strings.TrimPrefix(ps[0], "powershell "), " | ")
			assert.Equal(t, []string{"-NoProfile", "-ExecutionPolicy", "RemoteSigned", "-File"}, args[:4])
			assert.True(t, strings.HasSuffix(args[4], "install-windows.ps1"), args[4])
			assert.Equal(t, []string{"-Server", "https://fleet.example.com", "-TokenFile"}, args[5:8])
			assert.Equal(t, filepath.Join(work, "enrollment-token.txt"), args[8])
			assert.Equal(t, []string{"-Fleet", "linux-hosts", "-CollectorVersion", "0.162.0", "-SiteType", "edge", "-SiteId", "042", "-SiteName", "Edge site 42 (north)",
				"-SiteCountry", "NL", "-SiteRegion", "north", "-SiteTimezone", "Europe/Amsterdam", "-Environment", "prod"}, args[9:])
			assert.NotContains(t, ps[0], "Bypass")
			assert.Equal(t, testToken, e.file(".token"), "the file holds the token, without a newline")
			assert.Contains(t, r.out, "the SHA-256 of the installer bundle is the one in the command")

			// the downloaded script is unblocked after the hash was checked, and only that one
			assert.Len(t, r.called("unblock "), 1)
			e.theTokenIsNowhere(testToken, r)
		})
	}
}

func TestPowerShellCommandStopsWhenTheHashIsNotTheOneInTheCommand(t *testing.T) {
	e := newPwshEnv(t)
	cmd := e.command(site(), testToken, func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "windows" {
				s.list.Items[i].SHA256 = strings.Repeat("0", 64)
			}
		}
	})
	r := e.run(cmd, false)
	assert.NotEqual(t, 0, r.code)
	assert.Contains(t, r.out, "[myriarch-install] ERROR [checksum_mismatch]")
	assert.Contains(t, r.out, "nothing was unpacked")
	assert.Empty(t, r.called("expand "), "nothing is unpacked")
	assert.Empty(t, r.called("unblock "))
	assert.Empty(t, r.called("powershell "), "and nothing runs")
	e.theTokenIsNowhere(testToken, r)
}

func TestPowerShellCommandChecksBeforeItTouchesTheHost(t *testing.T) {
	cases := map[string]struct {
		env  []string
		code string
	}{
		"not elevated":             {[]string{"MOCK_INTEGRITY=medium"}, "privilege_required"},
		"low integrity":            {[]string{"MOCK_INTEGRITY=low"}, "privilege_required"},
		"Group Policy: AllSigned":  {[]string{"MOCK_GPO=AllSigned"}, "execution_policy_blocked"},
		"Group Policy: Restricted": {[]string{"MOCK_GPO=Restricted"}, "execution_policy_blocked"},
		"Group Policy of the user": {[]string{"MOCK_GPO_USER=AllSigned"}, "execution_policy_blocked"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newPwshEnv(t)
			r := e.run(e.command(site(), testToken), false, c.env...)
			assert.NotEqual(t, 0, r.code)
			assert.Contains(t, r.out, "[myriarch-install] ERROR ["+c.code+"]")
			assert.Empty(t, r.called("icacls "), "no directory is made, no token is written")
			assert.Empty(t, r.called("iwr "))
			assert.Empty(t, e.workDirs())
		})
	}
	// a Group Policy that allows what the command needs does not stop it; the policies of the process and of the machine are no Group Policy
	for _, gpo := range []string{"RemoteSigned", "Unrestricted", "Bypass", "Undefined"} {
		t.Run("Group Policy: "+gpo, func(t *testing.T) {
			e := newPwshEnv(t)
			r := e.run(e.command(site(), testToken), false, "MOCK_GPO="+gpo)
			assert.Equal(t, 0, r.code, r.out)
		})
	}
}

// A session of SYSTEM has the integrity level System, above High: it is the context of scheduled tasks, software
// distribution and management agents, which run the variant with the token in a file. It has more rights than an elevated
// administrator and installs the host the same way; only the levels below High are refused.
func TestPowerShellCommandRunsAsSystemToo(t *testing.T) {
	for _, clm := range []bool{false, true} {
		for _, variant := range []string{VariantTokenInline, VariantTokenFile} {
			t.Run(fmt.Sprintf("%s clm=%v", variant, clm), func(t *testing.T) {
				e := newPwshEnv(t)
				req := fullRequest(variant)
				token := testToken
				if variant == VariantTokenFile {
					e.writeWindowsFile(windowsTokenFile, testToken)
					req.TokenFiles = &TokenFiles{Windows: windowsTokenFile}
					token = ""
				}
				r := e.run(e.command(req, token), clm, "MOCK_INTEGRITY=system")
				require.Equal(t, 0, r.code, r.out)
				assert.Len(t, r.called("powershell "), 1, "the installer was started")
				assert.NotContains(t, r.out, "privilege_required")
				assert.NotContains(t, strings.Join(r.log, "\n"), testToken)
			})
		}
	}
	// the other levels keep out, whatever the groups say: an administrator who is not elevated is in the group
	// Administrators, "for deny only"
	for _, level := range []string{"medium", "low"} {
		t.Run("not "+level, func(t *testing.T) {
			e := newPwshEnv(t)
			r := e.run(e.command(site(), testToken), false, "MOCK_INTEGRITY="+level)
			assert.NotEqual(t, 0, r.code)
			assert.Contains(t, r.out, "[myriarch-install] ERROR [privilege_required]")
			assert.Contains(t, r.out, "or as SYSTEM")
			assert.Empty(t, r.called("icacls "))
		})
	}
}

func TestPowerShellCommandStopsWithoutAUsableToken(t *testing.T) {
	cases := map[string]struct{ token, code string }{
		"nothing":         {"", "option_missing"},
		"the placeholder": {brand.EnrollmentTokenPlaceholder, "option_missing"},
		"a space":         {"myre_abc def", "option_invalid"},
		"a dollar":        {"myre_$env:Path", "option_invalid"},
		"a semicolon":     {"myre_abc;id", "option_invalid"},
		"a backtick":      {"myre_`n", "option_invalid"},
		"a double quote":  {`"myre_abc"`, "option_invalid"},
		"a newline":       {"myre_abc\nmyre_def", "option_invalid"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if strings.ContainsAny(c.token, "'") {
				t.Skip("a quote cannot be in a value of the command")
			}
			e := newPwshEnv(t)
			r := e.run(e.command(site(), c.token), false)
			assert.NotEqual(t, 0, r.code, r.out)
			assert.Contains(t, r.out, "[myriarch-install] ERROR ["+c.code+"]")
			assert.Empty(t, r.called("icacls "), "the token is checked before a directory is made")
			assert.Empty(t, r.called("iwr "))
			assert.Empty(t, e.workDirs())
			if c.token != "" && c.token != brand.EnrollmentTokenPlaceholder {
				assert.NotContains(t, r.out, c.token)
			}
		})
	}
}

func TestPowerShellCommandRemovesTheDirectoryWhenAStepFails(t *testing.T) {
	cases := map[string]struct {
		env   []string
		code  string
		after string // the step that must not have run
	}{
		"icacls fails":        {[]string{"MOCK_ICACLS_FAIL=1"}, "acl_failed", "set-content "},
		"setowner fails":      {[]string{"MOCK_SETOWNER_FAIL=1"}, "acl_failed", "set-content "},
		"the download fails":  {[]string{"MOCK_IWR_FAIL=1"}, "download_failed", "expand "},
		"the installer fails": {[]string{"MOCK_INSTALLER_EXIT=7"}, "installer_failed", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newPwshEnv(t)
			r := e.run(e.command(site(), testToken), false, c.env...)
			assert.NotEqual(t, 0, r.code, r.out)
			assert.Contains(t, r.out, "[myriarch-install] ERROR ["+c.code+"]")
			if c.after != "" {
				assert.Empty(t, r.called(c.after))
			}
			e.theTokenIsNowhere(testToken, r)
		})
	}
	// the exit code of the installer is named in the message
	e := newPwshEnv(t)
	r := e.run(e.command(site(), testToken), false, "MOCK_INSTALLER_EXIT=7")
	assert.Contains(t, r.out, "exit code 7")
}

// windowsTokenFile is a path in the form of Windows. On the machine of the test it is a file below PS_WINROOT.
const windowsTokenFile = `C:\Secrets\token.txt`

// writeWindowsFile creates the file for a path in the form of Windows. PowerShell on a Unix system has no drive C:, so
// the harness maps such a path to a file below PS_WINROOT.
func (e *pwshEnv) writeWindowsFile(name, content string) string {
	e.t.Helper()
	path := filepath.Join(e.dir, "winroot", filepath.FromSlash(strings.ReplaceAll(strings.TrimPrefix(name, `C:\`), `\`, "/")))
	require.NoError(e.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(e.t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestPowerShellCommandReadsTheTokenFromAFile(t *testing.T) {
	for _, clm := range []bool{false, true} {
		for name, content := range map[string]string{"plain": testToken, "newline": testToken + "\r\n", "spaces": "  " + testToken + " \n"} {
			t.Run(fmt.Sprintf("%s clm=%v", name, clm), func(t *testing.T) {
				e := newPwshEnv(t)
				path := e.writeWindowsFile(windowsTokenFile, content)
				req := fullRequest(VariantTokenFile)
				req.TokenFiles = &TokenFiles{Windows: windowsTokenFile}
				r := e.run(e.command(req, ""), clm)
				require.Equal(t, 0, r.code, r.out)
				assert.Empty(t, r.called("set-content "), "no copy of the token is written")
				ps := r.called("powershell ")
				require.Len(t, ps, 1)
				assert.Contains(t, ps[0], " | -TokenFile | "+windowsTokenFile+" | ")
				assert.Equal(t, "Bearer "+testToken+"\n", e.file(".auth"))
				assert.Equal(t, content, e.file(".token"), "the installer reads the file of the operator")
				assert.FileExists(t, path, "which stays where it is")
				assert.NotContains(t, strings.Join(r.log, "\n"), testToken)
			})
		}
	}
	t.Run("a file that is not there", func(t *testing.T) {
		e := newPwshEnv(t)
		req := fullRequest(VariantTokenFile)
		req.TokenFiles = &TokenFiles{Windows: `C:\missing.txt`}
		r := e.run(e.command(req, ""), false)
		assert.NotEqual(t, 0, r.code)
		assert.Contains(t, r.out, "[myriarch-install] ERROR [file_unreadable]")
		assert.Empty(t, r.called("icacls "))
	})
	for name, content := range map[string]string{"empty": "", "a semicolon": "myre_abc;id", "the placeholder": brand.EnrollmentTokenPlaceholder, "a command": "$(id)"} {
		t.Run("a file with "+name, func(t *testing.T) {
			e := newPwshEnv(t)
			path := e.writeWindowsFile(windowsTokenFile, content)
			req := fullRequest(VariantTokenFile)
			req.TokenFiles = &TokenFiles{Windows: windowsTokenFile}
			r := e.run(e.command(req, ""), false)
			assert.NotEqual(t, 0, r.code, r.out)
			assert.Contains(t, r.out, "[myriarch-install] ERROR [option_")
			assert.Empty(t, r.called("icacls "))
			assert.FileExists(t, path)
		})
	}
}

func TestPowerShellCommandOfTheLabVariant(t *testing.T) {
	e := newPwshEnv(t)
	cmd := e.command(site(func(r *CommandsRequest) { r.Variant = VariantLab }), testToken, func(d *Deps, s *stubBundles) { d.DevMode, d.PublicURL = true, "http://localhost:8080" })
	r := e.run(cmd, true)
	require.Equal(t, 0, r.code, r.out)
	assert.Equal(t, []string{"iwr http://localhost:8080/api/v1/install/bundles/" + windowsName() + " basic=True redirects=0"}, r.called("iwr "))
	ps := r.called("powershell ")
	require.Len(t, ps, 1)
	assert.True(t, strings.HasSuffix(ps[0], " | -Insecure"), ps[0])
	e.theTokenIsNowhere(testToken, r)
}

// The command uses cmdlets and native programs only, so that Constrained Language Mode, which App Control enforces for
// a typed command, lets it run. The tests above run it in that mode; this one keeps a change from adding what that
// mode forbids, and what a careful reviewer would not want in a command that runs as an administrator.
func TestPowerShellScriptUsesCmdletsAndNativesOnly(t *testing.T) {
	script := scriptMarkers.Replace(windowsScript)

	cmdlets := map[string]bool{}
	for _, m := range regexp.MustCompile(`\b[A-Z][a-z]+-[A-Z][A-Za-z]+\b`).FindAllString(script, -1) {
		cmdlets[m] = true
	}
	allowed := []string{
		"Select-String", "Get-ExecutionPolicy", "Where-Object", "Test-Path", "Get-Content", "Join-Path", "New-Guid", "New-Item", "Out-Null",
		"Set-Content", "Write-Host", "Invoke-WebRequest", "Get-FileHash", "Expand-Archive", "Unblock-File", "Remove-Item", "Write-Warning",
	}
	var got []string
	for c := range cmdlets {
		got = append(got, c)
	}
	sort.Strings(got)
	sort.Strings(allowed)
	assert.Equal(t, allowed, got, "the cmdlets of the command")

	natives := regexp.MustCompile(`\b[a-z]+\.exe\b`).FindAllString(script, -1)
	sort.Strings(natives)
	assert.Equal(t, []string{"icacls.exe", "powershell.exe", "whoami.exe"}, uniq(natives), "the native programs")

	for _, bad := range []string{
		"::", "New-Object", "Add-Type", "Invoke-Expression", "iex ", "Start-Process", "[System", "[Net", "[IO", "[Convert", "[Environment", "[Reflection", "[Text",
		"-EncodedCommand", "-Command", "Bypass", "Set-ExecutionPolicy", "Unrestricted", "ConstrainedLanguage", "LanguageMode", "Invoke-Command", "Invoke-RestMethod",
		"curl", "wget", "Start-BitsTransfer", "Set-Alias", "Import-Module",
	} {
		assert.NotContains(t, script, bad)
	}
	assert.NotRegexp(t, regexp.MustCompile(`\.[A-Za-z]+\(`), script, "no method call: Constrained Language Mode allows them on core types only")
	for _, m := range regexp.MustCompile(`\[[A-Za-z][A-Za-z.]*(?:\[\])?\]`).FindAllString(script, -1) {
		assert.Equal(t, "[hashtable]", m, "a type literal other than the type of the parameter")
	}
	// the working directory (the only place for the token and the bundle) is made directly below %ProgramFiles%, where
	// standard users cannot write, and restricted and owned by the administrators before anything is written to it
	assert.Regexp(t, regexp.MustCompile(`(?s)Join-Path \$env:ProgramFiles .*?New-Item -ItemType Directory -Path \$d.*?icacls\.exe \$d /inheritance:r.*?icacls\.exe \$d /setowner '\*S-1-5-32-544'.*?\$TokenFile = Join-Path \$d`), script)
	assert.NotContains(t, script, "ProgramData")
	assert.Equal(t, 1, strings.Count(script, "New-Item -ItemType Directory -Path $d"), "the working directory is made once")
	// the policy is set for the process that runs the installer, to RemoteSigned and nowhere else
	assert.Equal(t, 1, strings.Count(script, "'-ExecutionPolicy', 'RemoteSigned'"))
	assert.NotContains(t, script, "-Scope")
	// the token is a parameter, never part of an argument vector of a native program
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "$Token") && (strings.Contains(line, "icacls") || strings.Contains(line, "powershell.exe") || strings.Contains(line, "whoami")) {
			t.Errorf("the token reaches a native program: %s", line)
		}
	}
}

func uniq(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// The harness is a real test of Constrained Language Mode: a command that calls a method of a type the mode does not
// permit fails in it, and runs without it.
func TestPowerShellHarnessEnforcesConstrainedLanguage(t *testing.T) {
	e := newPwshEnv(t)
	forbidden := "[System.IO.File]::Exists('/')\n"
	r := e.run(forbidden, false)
	assert.Equal(t, 0, r.code, r.out)
	r = e.run(forbidden, true)
	assert.NotEqual(t, 0, r.code)
	assert.Contains(t, r.out, "Cannot invoke method")
}
