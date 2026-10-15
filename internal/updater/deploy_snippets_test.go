// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The install examples of the enrollment token dialog must name every input that the installers require. The example of
// v0.1.0 left out the collector version and the Sigstore verifier, so a host ended with option_missing and then with
// sigstore_verifier_missing. The required options are read from the installers (their option_missing messages), not listed
// here, so a new required option fails these tests until the dialog names it.

const enrollmentSnippetsFile = "../../web/src/pages/settings/enrollmentSnippets.ts"

// readLF returns a source file with LF line endings, so that the tests also pass in a checkout with core.autocrlf=true.
func readLF(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.FromSlash(path))
	require.NoError(t, err)
	return strings.ReplaceAll(string(src), "\r\n", "\n")
}

// snippetFunction returns the source of one exported function of enrollmentSnippets.ts, up to its closing brace.
func snippetFunction(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "export function "+name+"(")
	require.GreaterOrEqual(t, start, 0, "enrollmentSnippets.ts defines %s", name)
	end := strings.Index(src[start:], "\n}\n")
	require.Greater(t, end, 0, "%s ends with a closing brace at the start of a line", name)
	return src[start : start+end]
}

// namesOption reports whether a string literal of the function body starts with the option, as in
// '--site-id <id>' or `-OpampEndpoint '${endpoint}'`. A comment line of the generated command starts with "#", so a
// mention in a comment does not count.
func namesOption(body, option string) bool {
	return regexp.MustCompile("['\"`]" + regexp.QuoteMeta(option) + `[\s'"` + "`" + `]`).MatchString(body)
}

func TestEnrollmentDialogLinuxExampleNamesEveryRequiredInstallerOption(t *testing.T) {
	body := snippetFunction(t, readLF(t, enrollmentSnippetsFile), "linuxInstallCommand")
	script := readLF(t, deployDir+"/scripts/install-linux.sh")

	// die option_missing "--site-type is missing": the unconditional inputs of validate_inputs
	missing := regexp.MustCompile(`die option_missing "(--[a-z-]+) is missing"`).FindAllStringSubmatch(script, -1)
	require.GreaterOrEqual(t, len(missing), 3, "install-linux.sh reports the site type, the site ID and the collector version as missing")
	required := []string{"--opamp-endpoint", "--token-file", "--verifier"} // endpoint and token (option_invalid, option_missing) and the verifier
	for _, m := range missing {
		required = append(required, m[1])
	}
	assert.Contains(t, required, "--collector-version", "the collector version is required")
	for _, option := range required {
		assert.Regexp(t, `(?m)^\s+`+regexp.QuoteMeta(option)+`\)`, script, "install-linux.sh accepts %s", option)
		assert.True(t, namesOption(body, option), "the Linux example of the token dialog must name %s", option)
	}
	// the script runs from the unpacked archive, where its templates are, in a root shell in a directory that only root can write
	assert.Contains(t, body, "'./deploy/scripts/install-linux.sh',")
	assert.NotRegexp(t, `sudo +\./`, body, "the example runs as root already: sudo would invite a run from a directory that the user owns")
}

func TestEnrollmentDialogWindowsExampleNamesEveryRequiredInstallerParameter(t *testing.T) {
	body := snippetFunction(t, readLF(t, enrollmentSnippetsFile), "windowsInstallCommand")
	script := readLF(t, deployDir+"/scripts/install-windows.ps1")

	// Stop-Install 'option_missing' '-SiteType is missing': the unconditional inputs of Test-Inputs
	missing := regexp.MustCompile(`Stop-Install 'option_missing' '(-[A-Za-z]+) is missing'`).FindAllStringSubmatch(script, -1)
	require.GreaterOrEqual(t, len(missing), 3, "install-windows.ps1 reports the site type, the site ID and the collector version as missing")
	required := []string{"-OpampEndpoint", "-TokenFile", "-Verifier"}
	for _, m := range missing {
		required = append(required, m[1])
	}
	assert.Contains(t, required, "-CollectorVersion", "the collector version is required")
	for _, parameter := range required {
		assert.Regexp(t, `(?m)^\s*\[string\]\$`+regexp.QuoteMeta(strings.TrimPrefix(parameter, "-"))+`,?\s*$`, script, "install-windows.ps1 accepts %s", parameter)
		assert.True(t, namesOption(body, parameter), "the Windows example of the token dialog must name %s", parameter)
	}

	// the start: the execution policy is set for the one process, never for the machine, the user or with Bypass
	assert.Contains(t, body, "powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\\\\deploy\\\\scripts\\\\install-windows.ps1")
	assert.NotRegexp(t, `(?i)bypass|unrestricted|set-executionpolicy`, body)
}
