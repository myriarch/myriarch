// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
