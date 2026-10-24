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

// catalogDescription returns the description of one catalog entry of install-linux.sh, continuation lines included.
func catalogDescription(t *testing.T, linux, code string) string {
	t.Helper()
	i := strings.Index(linux, "# ---- Error code catalog")
	j := strings.Index(linux, "# ---- End of error code catalog")
	require.True(t, i >= 0 && j > i, "install-linux.sh has an error code catalog block")
	first := regexp.MustCompile(`^#   ` + regexp.QuoteMeta(code) + `\s{2,}(\S.*)$`)
	more := regexp.MustCompile(`^#\s{4,}(\S.*)$`)
	var parts []string
	for _, line := range strings.Split(linux[i:j], "\n") {
		line = strings.TrimRight(line, "\r")
		if m := first.FindStringSubmatch(line); m != nil {
			parts = []string{m[1]}
		} else if m := more.FindStringSubmatch(line); m != nil && len(parts) > 0 {
			parts = append(parts, m[1])
		} else if len(parts) > 0 {
			break
		}
	}
	require.NotEmpty(t, parts, "the catalog has an entry for %s", code)
	return strings.Join(parts, " ")
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

// The catalog promises that a tool_missing message names the tool, and docs/tasks/connect-a-host.md tells the operator so.
// The tools are those in the parentheses of the catalog entry: every message names at least one of them and every one of
// them is named by a message, so neither the script nor the catalog can drift from the promise.
func TestInstallLinuxScriptToolMissingMessagesNameTheTool(t *testing.T) {
	linux := string(readDeploy(t, "scripts/install-linux.sh"))
	desc := catalogDescription(t, linux, "tool_missing")
	assert.Contains(t, desc, "the message names the tool")
	list := regexp.MustCompile(`\(([^)]+)\)`).FindStringSubmatch(desc)
	require.NotNil(t, list, "the tool_missing entry lists the tools in parentheses: %q", desc)
	var tools []string
	toolRe := map[string]*regexp.Regexp{}
	for _, tool := range regexp.MustCompile(`[\s,/]+`).Split(list[1], -1) {
		if tool == "" {
			continue
		}
		tools = append(tools, tool)
		toolRe[tool] = regexp.MustCompile(`\b` + regexp.QuoteMeta(tool) + `\b`)
	}
	require.NotEmpty(t, tools, "the tool_missing entry lists the tools: %q", list[1])

	call := regexp.MustCompile(`\bdie tool_missing "([^"]*)"`)
	named := map[string]bool{}
	for _, line := range codeLines(linux) {
		for _, m := range call.FindAllStringSubmatch(line, -1) {
			found := false
			for _, tool := range tools {
				if toolRe[tool].MatchString(m[1]) {
					named[tool] = true
					found = true
				}
			}
			assert.True(t, found, "the tool_missing message %q names none of the tools of the catalog (%s)", m[1], list[1])
		}
	}
	// this also fails when the scan above finds no tool_missing call at all
	for _, tool := range tools {
		assert.True(t, named[tool], "the catalog lists %s, but no tool_missing message names it", tool)
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
		{"updater_hash_source_missing", "requires --updater-sha256 or --updater-checksums-url", "requires -UpdaterSha256 or -UpdaterChecksumsUrl"},
		{"option_conflict", "--token and --token-file", "-Token and -TokenFile"},
		{"option_removed", "--signing-key was removed", "-SigningKey was removed"},
		{"option_removed", "--updater-checksums-sig-url was removed", "-UpdaterChecksumsSigUrl was removed"},
		{"option_conflict", "--updater-bundle-url requires --updater-checksums-url", "-UpdaterBundleUrl requires -UpdaterChecksumsUrl"},
		{"option_conflict", "cannot be combined with --verify-signatures skip", "cannot be combined with -VerifySignatures skip"},
		{"option_conflict", "and the version in --updater-url", "and the version in -UpdaterUrl"},
		{"option_conflict", "only apply together with --with-updater", "only apply together with -WithUpdater"},
		{"option_missing", "--updater-version is missing", "-UpdaterVersion is missing"},
		{"option_invalid", "invalid --updater-version", "invalid -UpdaterVersion"},
		{"option_missing", "--site-type is missing", "-SiteType is missing"},
		{"option_missing", "--site-id is missing", "-SiteId is missing"},
		// "--collector-version is missing" is a Windows condition only: install-linux.sh has the tested version as its default
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
		{"file_unreadable", "--verifier is not executable", "-Verifier not found"},
		{"https_required", "must be an https:// URL (--insecure only", "must be an https:// URL (-Insecure only"},
		same("sigstore_verifier_missing", "no Sigstore verifier found"),
		same("sigstore_verifier_outdated", "or newer is required (older versions have known verification flaws)"),
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
		same("archive_invalid", "the archive contains no regular file"),
		same("collector_start_failed", "the new collector binary cannot be started"),
		same("collector_version_mismatch", "collector reports"),
		same("unsupported_platform", "unsupported architecture"),
		same("download_failed", "download failed:"),
		{"token_on_command_line", "is visible in the process", "is visible in the process"},
		{"updater_unauthenticated", "neither --updater-sha256 nor", "neither -UpdaterSha256 nor"},
		same("service_not_running", "otelcol-supervisor is not running"),
		same("conflicting_install", "an installation of the upstream project is present"),
		same("supervisor_missing", "the supervisor binary"),
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

// tableRow returns the line of a Markdown table that starts with the given first cell, within one section.
func tableRow(section, firstCell string) string {
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| "+firstCell) {
			return line
		}
	}
	return ""
}

// markdownSection returns the text from a heading to the next heading of the same level.
func markdownSection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+heading+"\n")
	require.GreaterOrEqual(t, i, 0, "the document has the section %q", heading)
	section := doc[i+1:]
	if j := strings.Index(section[len(heading):], "\n## "); j >= 0 {
		section = section[:len(heading)+j]
	}
	return section
}

// The codes that v0.2.0 adds are in the table "When the installer stops" of the guide and in the table of the Windows README,
// both marked as new: the guide and the README also describe the installers of v0.1.0, which do not raise them.
func TestPreflightCodesAreDocumentedAsNewInV020(t *testing.T) {
	guide := markdownSection(t, readLF(t, "../../docs/tasks/connect-a-host.md"), "## When the installer stops")
	readme := markdownSection(t, readLF(t, deployDir+"/windows/README.md"), "## Error codes")
	for _, code := range []string{"conflicting_install", "supervisor_missing", "language_mode_unsupported"} {
		row := tableRow(guide, "`"+code+"`")
		if assert.NotEmpty(t, row, "the guide has a row for %s", code) {
			assert.Contains(t, row, "from v0.2.0", "the row of %s in the guide says that the code is new", code)
		}
		row = tableRow(readme, "`"+code+"`")
		if assert.NotEmpty(t, row, "the Windows README has a row for %s", code) {
			assert.Contains(t, row, "from v0.2.0", "the row of %s in the Windows README says that the code is new", code)
		}
	}
	// the next step depends on the case: a new run fetches a file that is gone or changed again, but it does not replace a directory
	// or a link and would stop at the same item, so both documents tell to remove it
	for name, doc := range map[string]string{"guide": guide, "Windows README": readme} {
		row := tableRow(doc, "`supervisor_missing`")
		assert.Contains(t, row, "verifies the file again", "the %s says that a new run fetches a file that is gone or changed", name)
		assert.Contains(t, row, "remove it yourself", "the %s tells to remove a directory or a link", name)
	}
	// the rows that describe changed behaviour say so as well
	assert.Contains(t, tableRow(readme, "`unsupported_platform`"), "From v0.2.0")
	assert.Contains(t, tableRow(readme, "`service_not_running`"), "From v0.2.0")
	assert.Contains(t, tableRow(guide, "`unsupported_platform`"), "From v0.2.0")
	assert.Contains(t, tableRow(guide, "`service_not_running`"), "From v0.2.0")
}
