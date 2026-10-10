// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These checks pin the error code scheme of the two installers (see the catalog at the top of
// deploy/scripts/install-linux.sh and the "Error codes" section of deploy/windows/README.md).
// They run without PowerShell: they read the script sources.

var codeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// catalogCodes parses the lines "#   <code>   <description>" between the catalog markers of install-linux.sh.
func catalogCodes(t *testing.T, linux string) map[string]bool {
	t.Helper()
	i := strings.Index(linux, "# ---- Error code catalog")
	j := strings.Index(linux, "# ---- End of error code catalog")
	require.True(t, i >= 0 && j > i, "install-linux.sh has an error code catalog block")
	entry := regexp.MustCompile(`^#   ([a-z][a-z0-9_]*)\s{2,}\S`)
	codes := map[string]bool{}
	for _, line := range strings.Split(linux[i:j], "\n") {
		if m := entry.FindStringSubmatch(line); m != nil {
			codes[m[1]] = true
		}
	}
	require.NotEmpty(t, codes)
	return codes
}

// windowsOnlyCodes parses the "# Windows-only codes:" list in install-windows.ps1.
func windowsOnlyCodes(t *testing.T, win string) map[string]bool {
	t.Helper()
	i := strings.Index(win, "# Windows-only codes:")
	j := strings.Index(win, "# service_not_running is fatal here")
	require.True(t, i >= 0 && j > i, "install-windows.ps1 lists its Windows-only codes")
	entry := regexp.MustCompile(`^#   ([a-z][a-z0-9_]*)\s{2,}\S`)
	codes := map[string]bool{}
	for _, line := range strings.Split(win[i:j], "\n") {
		if m := entry.FindStringSubmatch(line); m != nil {
			codes[m[1]] = true
		}
	}
	require.NotEmpty(t, codes)
	return codes
}

func codeLines(script string) []string {
	var out []string
	for _, l := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

func TestInstallLinuxScriptEveryDieAndWarnCarriesACatalogCode(t *testing.T) {
	linux := string(readDeploy(t, "scripts/install-linux.sh"))
	catalog := catalogCodes(t, linux)
	for c := range catalog {
		assert.Regexp(t, codeRe, c)
	}
	call := regexp.MustCompile(`\b(die|warn)\b\s+(\S+)`)
	def := regexp.MustCompile(`^\s*(die|warn)\(\)`)
	used := map[string]bool{}
	n := 0
	for _, line := range codeLines(linux) {
		if def.MatchString(line) {
			continue
		}
		for _, m := range call.FindAllStringSubmatch(line, -1) {
			n++
			code := m[2]
			if !assert.Regexp(t, codeRe, code, "die/warn without a code: %s", strings.TrimSpace(line)) {
				continue
			}
			assert.Contains(t, catalog, code, "code %q is not in the catalog: %s", code, strings.TrimSpace(line))
			used[code] = true
		}
	}
	assert.Greater(t, n, 60, "the scan finds the die/warn calls")
	// the other direction: no stale catalog entry that the Linux script never raises and the Windows script does not either
	win := string(readDeploy(t, "scripts/install-windows.ps1"))
	for c := range catalog {
		assert.True(t, used[c] || strings.Contains(win, "'"+c+"'"), "catalog code %q is never raised by either installer", c)
	}
}

func TestInstallWindowsScriptEveryFailureCarriesACatalogCode(t *testing.T) {
	linux := string(readDeploy(t, "scripts/install-linux.sh"))
	win := string(readDeploy(t, "scripts/install-windows.ps1"))
	allowed := catalogCodes(t, linux)
	only := windowsOnlyCodes(t, win)
	for c := range only {
		allowed[c] = true
		assert.Regexp(t, codeRe, c)
	}

	// the Linux catalog names the Windows-only codes as well
	idx := strings.Index(linux, "# Windows-only codes")
	require.GreaterOrEqual(t, idx, 0)
	sentence := linux[idx:strings.Index(linux, "# ---- End of error code catalog")]
	for c := range only {
		assert.Contains(t, sentence, c, "install-linux.sh must list the Windows-only code %s", c)
	}

	stop := regexp.MustCompile(`Stop-Install\s+'([a-z][a-z0-9_]*)'\s`)
	warnRe := regexp.MustCompile(`Write-Warn\s+'([a-z][a-z0-9_]*)'\s`)
	throwRe := regexp.MustCompile(`\bthrow\b`)
	used := map[string]bool{}
	nStop := 0
	for _, line := range codeLines(win) {
		trim := strings.TrimSpace(line)
		isHelper := strings.HasPrefix(trim, "function Stop-Install") || strings.HasPrefix(trim, "function Write-Warn")
		if isHelper {
			continue
		}
		if strings.Contains(line, "Stop-Install") {
			m := stop.FindStringSubmatch(line)
			if assert.NotNil(t, m, "Stop-Install without a code: %s", trim) {
				nStop++
				assert.Contains(t, allowed, m[1], "code %q is not in a catalog: %s", m[1], trim)
				used[m[1]] = true
			}
		}
		if strings.Contains(line, "Write-Warn") {
			m := warnRe.FindStringSubmatch(line)
			if assert.NotNil(t, m, "Write-Warn without a code: %s", trim) {
				assert.Contains(t, allowed, m[1], "warning code %q is not in a catalog: %s", m[1], trim)
				used[m[1]] = true
			}
		}
		// no bare throw string outside the helper: only the bare re-throw inside the download retry loop remains
		if throwRe.MatchString(line) {
			assert.Regexp(t, `\bthrow\s*\}`, line, "bare throw with a message instead of Stop-Install: %s", trim)
		}
	}
	assert.Greater(t, nStop, 60, "the scan finds the Stop-Install calls")
	for c := range only {
		assert.True(t, used[c] || c == "unexpected_error", "Windows-only code %q is never raised", c)
	}
	// the top-level catch prints the one-line ERROR format
	assert.Contains(t, win, `$Host.UI.WriteErrorLine("[myriarch-install] ERROR $msg")`)
	// the retry loop tests for the code, not for message prose
	dl := psFunctionBody(t, win, "Invoke-Download")
	assert.Contains(t, dl, `Contains('[https_required]')`)
}

// sharedCondition names one condition that both installers must report with the same code. The two message
// fragments identify the die/warn/Stop-Install line in the respective script.
type sharedCondition struct {
	code  string
	linux string
	win   string
}

func TestInstallersUseTheSameCodeForSharedConditions(t *testing.T) {
	linux := string(readDeploy(t, "scripts/install-linux.sh"))
	win := string(readDeploy(t, "scripts/install-windows.ps1"))
	same := func(code, msg string) sharedCondition { return sharedCondition{code, msg, msg} }
	conditions := []sharedCondition{
		{"insecure_required", "--opamp-endpoint without TLS", "-OpampEndpoint without TLS"},
		{"insecure_required", "--verify-signatures skip is only allowed", "-VerifySignatures skip is only allowed"},
		{"updater_hash_source_missing", "requires --updater-sha256 or", "requires -UpdaterSha256 or"},
		{"option_conflict", "--token and --token-file", "-Token and -TokenFile"},
		{"option_conflict", "--signing-key requires", "-SigningKey requires"},
		{"option_conflict", "--updater-checksums-url requires --signing-key", "-UpdaterChecksumsUrl requires -SigningKey"},
		{"option_conflict", "--updater-checksums-sig-url requires", "-UpdaterChecksumsSigUrl requires"},
		{"option_conflict", "only apply together with --with-updater", "only apply together with -WithUpdater"},
		{"option_missing", "--site-type is missing", "-SiteType is missing"},
		{"option_missing", "--site-id is missing", "-SiteId is missing"},
		{"option_missing", "--collector-version is missing", "-CollectorVersion is missing"},
		{"option_missing", "--token or --token-file is missing", "-Token or -TokenFile is missing"},
		{"option_missing", "--with-updater requires --updater-url", "-WithUpdater requires -UpdaterUrl"},
		{"option_invalid", "invalid --collector-version", "invalid -CollectorVersion"},
		{"option_invalid", "invalid --supervisor-version", "invalid -SupervisorVersion"},
		{"option_invalid", "--opamp-endpoint must start with", "-OpampEndpoint must start with"},
		{"option_invalid", "--updater-sha256 is not a SHA-256", "-UpdaterSha256 is not a SHA-256"},
		{"option_invalid", "--arch must be amd64 or arm64", "-Arch must be amd64 or arm64"},
		{"option_invalid", "no file name in --updater-url", "no file name in -UpdaterUrl"},
		same("option_invalid", "the token contains forbidden characters"),
		same("option_invalid", "control character in a parameter value"),
		same("file_unreadable", "token file is not readable"),
		same("file_unreadable", "CA file is not readable"),
		same("file_unreadable", "trusted_root.json is not readable"),
		same("file_unreadable", "signing key file is not readable"),
		{"file_unreadable", "--verifier is not executable", "-Verifier not found"},
		{"https_required", "must be an https:// URL (--insecure only", "must be an https:// URL (-Insecure only"},
		same("sigstore_verifier_missing", "no Sigstore verifier found"),
		same("sigstore_verification_failed", "Sigstore verification of"),
		{"privilege_required", "run as root", "run as Administrator"},
		{"tool_missing", "tar not found", "tar.exe not found"},
		same("template_missing", "template not found"),
		same("template_missing", "templates not found under"),
		same("template_placeholder", "unreplaced placeholders"),
		same("checksum_invalid", "invalid .sha256 file"),
		same("checksum_mismatch", "SHA-256 mismatch"),
		same("checksum_mismatch", "SHA-256 of myriarch-updater does not match"),
		same("checksum_entry_count", "checksums.txt contains"),
		same("checksum_conflict", "contradicts the signed checksums.txt"),
		same("signature_malformed", "is not valid base64"),
		same("signature_invalid", "is invalid (wrong key or tampered file)"),
		same("archive_invalid", "the archive contains no regular file"),
		same("collector_start_failed", "the new collector binary cannot be started"),
		same("collector_version_mismatch", "collector reports"),
		same("unsupported_platform", "unsupported architecture"),
		same("download_failed", "download failed:"),
		{"token_on_command_line", "is visible in the process", "is visible in the process"},
		{"updater_unauthenticated", "neither --updater-sha256 nor", "neither -UpdaterSha256 nor"},
		same("service_not_running", "otelcol-supervisor is not running"),
	}
	linuxCall := func(code string) *regexp.Regexp { return regexp.MustCompile(`\b(die|warn)\s+` + code + `\s`) }
	winCall := func(code string) *regexp.Regexp {
		return regexp.MustCompile(`(Stop-Install|Write-Warn)\s+'` + code + `'\s`)
	}
	check := func(script, fragment string, re *regexp.Regexp, which string) {
		found := 0
		for _, line := range codeLines(script) {
			if strings.Contains(line, fragment) {
				found++
				assert.Regexp(t, re, line, "%s: message %q must use the shared code", which, fragment)
			}
		}
		assert.Greater(t, found, 0, "%s: no line contains the message fragment %q", which, fragment)
	}
	for _, c := range conditions {
		assert.Regexp(t, codeRe, c.code)
		check(linux, c.linux, linuxCall(c.code), "install-linux.sh ("+c.code+")")
		check(win, c.win, winCall(c.code), "install-windows.ps1 ("+c.code+")")
	}
}

func TestInstallLinuxScriptPrintsOneLineErrorWithCode(t *testing.T) {
	out, err := runInstallRaw(t, t.TempDir(), "--bogus")
	require.Error(t, err)
	var errLines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "[myriarch-install] ") {
			errLines = append(errLines, l)
		}
	}
	require.Len(t, errLines, 1, "exactly one prefixed line: %s", out)
	assert.Regexp(t, `^\[myriarch-install\] ERROR \[usage_error\] \S`, errLines[0])

	// a warning keeps the same shape
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	out, err = runInstall(t, t.TempDir(), rel, baseArgs()...)
	require.NoError(t, err, out)
	assert.Regexp(t, `(?m)^\[myriarch-install\] WARNING \[token_on_command_line\] \S`, out)
}

func TestInstallWindowsReadmeDocumentsEveryErrorCode(t *testing.T) {
	linux := string(readDeploy(t, "scripts/install-linux.sh"))
	win := string(readDeploy(t, "scripts/install-windows.ps1"))
	readme := string(readDeploy(t, "windows/README.md"))
	i := strings.Index(readme, "## Error codes")
	require.GreaterOrEqual(t, i, 0, "deploy/windows/README.md has an 'Error codes' section")
	section := readme[i:]
	if j := strings.Index(section[3:], "\n## "); j >= 0 {
		section = section[:j+3]
	}
	codes := catalogCodes(t, linux)
	for c := range windowsOnlyCodes(t, win) {
		codes[c] = true
	}
	var missing []string
	for _, c := range sortedKeys(codes) {
		if !strings.Contains(section, "`"+c+"`") {
			missing = append(missing, c)
		}
	}
	assert.Empty(t, missing, "codes missing from the README 'Error codes' section")
}
