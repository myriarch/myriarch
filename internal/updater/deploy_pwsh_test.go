// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// pwshPath finds PowerShell 7 (the installer targets Windows PowerShell 5.1+, but
// its logic is plain PowerShell and is exercised here in staging mode with -Root).
func pwshPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MYRIARCH_PWSH"); p != "" {
		return p
	}
	p, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not installed: install-windows.ps1 staging tests skipped")
	}
	return p
}

func newFakeWindowsRelease(t *testing.T, version string) (dir string, archive []byte, updater []byte) {
	t.Helper()
	dir = t.TempDir()
	archive = makeTarGz(t,
		tarEntry{name: "README.md", body: []byte("readme"), mode: 0o644},
		tarEntry{name: "otelcol-contrib.exe", body: fakeCollectorScript(version, 0)},
	)
	put := func(rel string, data []byte) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, data, 0o755))
		require.NoError(t, os.WriteFile(p+".sha256", []byte(hex.EncodeToString(sha(data))), 0o644))
	}
	put("v"+version+"/otelcol-contrib_"+version+"_windows_amd64.tar.gz", archive)
	put("cmd/opampsupervisor/v"+version+"/opampsupervisor_"+version+"_windows_amd64.exe", []byte("MZ-fake-supervisor"))
	updater = []byte("MZ-fake-updater")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "myriarch-updater_windows_amd64.exe"), updater, 0o755))
	return dir, archive, updater
}

func runPwshInstall(t *testing.T, root, relDir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"-NoProfile", "-NonInteractive", "-File", filepath.Join(deployDir, "scripts", "install-windows.ps1"),
		"-Root", root, "-Arch", "amd64", "-ReleaseBaseUrl", "file://" + relDir, "-TemplatesDir", deployDir,
		"-Insecure", "-VerifySignatures", "skip"}, args...)
	cmd := exec.Command(pwshPath(t), full...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func TestInstallWindowsScriptStaging(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, archive, updater := newFakeWindowsRelease(t, "0.162.0")
	root := t.TempDir()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, []byte("x"), 0o644))

	out, err := runPwshInstall(t, root, relDir,
		"-OpampEndpoint", "wss://fleet.example.com/v1/opamp", "-Token", "myre_abc123",
		"-SiteType", "edge", "-SiteId", "0042", "-SiteName", `Site "North" & Co \ South`, "-SiteCountry", "NL",
		"-SiteTimezone", "Europe/Amsterdam", "-CollectorVersion", "0.162.0",
		"-CaFile", caFile, "-WithUpdater", "-UpdaterUrl", "file://"+relDir+"/myriarch-updater_windows_amd64.exe",
		"-UpdaterSha256", hex.EncodeToString(sha(updater)))
	require.NoError(t, err, out)

	pf := filepath.Join(root, "ProgramFiles", "Myriarch")
	pd := filepath.Join(root, "ProgramData", "Myriarch")

	// layout compatible with the updater's "copy" activation (Windows)
	l := &Layout{InstallDir: filepath.Join(pf, "collector"), BinaryName: "otelcol-contrib.exe", Activation: "copy"}
	cur, err := l.CurrentVersion()
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", cur)
	assert.Equal(t, sha(archive), l.ActiveContentHash(), "install marker matches myriarch-updater's format")
	assert.FileExists(t, filepath.Join(pf, "collector", "bin", "otelcol-contrib.exe"))
	assert.FileExists(t, filepath.Join(pf, "supervisor", "opampsupervisor.exe"))
	assert.FileExists(t, filepath.Join(pf, "updater", "myriarch-updater.exe"))
	assert.NoFileExists(t, filepath.Join(pf, "collector", "versions", "0.162.0", "README.md"))

	// the supervisor storage stays empty
	entries, err := os.ReadDir(filepath.Join(pd, "supervisor"))
	require.NoError(t, err)
	assert.Empty(t, entries)

	raw, err := os.ReadFile(filepath.Join(pd, "config", "supervisor.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "@@")
	assert.NotContains(t, string(raw), "myre_abc123", "no token in supervisor.yaml")
	var sup struct {
		Server struct {
			Endpoint string            `yaml:"endpoint"`
			Headers  map[string]string `yaml:"headers"`
			TLS      struct {
				CAFile string `yaml:"ca_file"`
			} `yaml:"tls"`
		} `yaml:"server"`
		Agent struct {
			Executable string `yaml:"executable"`
			Desc       struct {
				NonIdentifying map[string]string `yaml:"non_identifying_attributes"`
			} `yaml:"description"`
		} `yaml:"agent"`
		Storage struct {
			Directory string `yaml:"directory"`
		} `yaml:"storage"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &sup))
	assert.Equal(t, "wss://fleet.example.com/v1/opamp", sup.Server.Endpoint)
	assert.Equal(t, `C:\ProgramData\Myriarch\config\ca.pem`, sup.Server.TLS.CAFile)
	assert.Equal(t, `C:\Program Files\Myriarch\collector\bin\otelcol-contrib.exe`, sup.Agent.Executable)
	attrs := sup.Agent.Desc.NonIdentifying
	assert.Equal(t, "0042", attrs["site.id"], "a site ID with leading zeros stays a string")
	assert.Equal(t, `Site "North" & Co \ South`, attrs["site.name"])
	assert.NotContains(t, attrs, "site.region")

	tok, _ := os.ReadFile(filepath.Join(pd, "config", "enrollment-token"))
	assert.Equal(t, "myre_abc123\n", string(tok))

	// updater.yaml is accepted by the updater's config loader and consistent with the layout
	cfg, err := LoadConfig(filepath.Join(pd, "config", "updater.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "otelcol-contrib.exe", cfg.Collector.BinaryName)
	assert.Equal(t, "copy", cfg.Collector.Activation)
	assert.Equal(t, `C:\Program Files\Myriarch\collector`, cfg.Collector.InstallDir)
	assert.Equal(t, `C:\ProgramData\Myriarch\supervisor`, cfg.Supervisor.StorageDir)
	assert.Equal(t, `C:\ProgramData\Myriarch\config\ca.pem`, cfg.Server.CAFile, "the CA bundle is copied into the protected configuration directory")
	assert.FileExists(t, filepath.Join(pd, "config", "ca.pem"))

	// idempotent
	out, err = runPwshInstall(t, root, relDir,
		"-OpampEndpoint", "wss://fleet.example.com/v1/opamp", "-Token", "myre_abc123",
		"-SiteType", "edge", "-SiteId", "0042", "-CollectorVersion", "0.162.0")
	require.NoError(t, err, out)
	assert.Contains(t, out, "already installed")
}

func TestInstallWindowsScriptRejectsBadHashAndBadArguments(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, _, _ := newFakeWindowsRelease(t, "0.162.0")
	p := filepath.Join(relDir, "v0.162.0", "otelcol-contrib_0.162.0_windows_amd64.tar.gz")
	data, _ := os.ReadFile(p)
	data[len(data)-4] ^= 0xff
	require.NoError(t, os.WriteFile(p, data, 0o644))
	root := t.TempDir()
	good := []string{"-OpampEndpoint", "wss://x/v1/opamp", "-Token", "myre_x", "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", "0.162.0"}
	out, err := runPwshInstall(t, root, relDir, good...)
	require.Error(t, err)
	assert.Contains(t, out, "[checksum_mismatch]")
	assert.NoFileExists(t, filepath.Join(root, "ProgramData", "Myriarch", "config", "supervisor.yaml"))

	for name, args := range map[string][]string{
		"no endpoint": {"-Token", "myre_x", "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", "0.162.0"},
		"bad version": {"-OpampEndpoint", "wss://x/v1/opamp", "-Token", "myre_x", "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", "latest"},
		"no token":    {"-OpampEndpoint", "wss://x/v1/opamp", "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", "0.162.0"},
		"no site id":  {"-OpampEndpoint", "wss://x/v1/opamp", "-Token", "myre_x", "-SiteType", "edge", "-CollectorVersion", "0.162.0"},
	} {
		out, err := runPwshInstall(t, t.TempDir(), relDir, args...)
		assert.Error(t, err, name+": "+out)
	}
}

// runPwshInstallRaw runs the installer in staging mode WITHOUT the lab switches.
func runPwshInstallRaw(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"-NoProfile", "-NonInteractive", "-File", filepath.Join(deployDir, "scripts", "install-windows.ps1"),
		"-Root", root, "-Arch", "amd64", "-TemplatesDir", deployDir}, args...)
	cmd := exec.Command(pwshPath(t), full...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func TestInstallWindowsScriptRefusesUntrustedInputs(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, _, updater := newFakeWindowsRelease(t, "0.162.0")
	good := []string{"-OpampEndpoint", "wss://x/v1/opamp", "-Token", "myre_x", "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", "0.162.0"}
	withEndpoint := func(ep string) []string { return append([]string{"-OpampEndpoint", ep}, good[2:]...) }
	cases := map[string]struct {
		args []string
		want string
	}{
		"file mirror":    {append(append([]string{}, good...), "-ReleaseBaseUrl", "file://"+relDir), "[https_required]"},
		"http mirror":    {append(append([]string{}, good...), "-ReleaseBaseUrl", "http://mirror.example.com/r"), "[https_required]"},
		"skip signature": {append(append([]string{}, good...), "-ReleaseBaseUrl", "https://mirror.example.com/r", "-VerifySignatures", "skip"), "[insecure_required]"},
		"plain ws":       {append(withEndpoint("ws://x/v1/opamp"), "-ReleaseBaseUrl", "https://mirror.example.com/r"), "[insecure_required]"},
		"updater w/o sha": {append(append([]string{}, good...), "-ReleaseBaseUrl", "https://mirror.example.com/r",
			"-WithUpdater", "-UpdaterUrl", "https://mirror.example.com/myriarch-updater.exe"), "[updater_hash_source_missing]"},
		"updater over http": {append(append([]string{}, good...), "-ReleaseBaseUrl", "https://mirror.example.com/r",
			"-WithUpdater", "-UpdaterUrl", "http://mirror.example.com/myriarch-updater.exe", "-UpdaterSha256", hex.EncodeToString(sha(updater))), "[https_required]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			out, err := runPwshInstallRaw(t, root, c.args...)
			require.Error(t, err, out)
			assert.Contains(t, out, c.want)
			assert.NoFileExists(t, filepath.Join(root, "ProgramData", "Myriarch", "config", "supervisor.yaml"))
		})
	}
}

func TestInstallWindowsScriptVerifiesSigstoreBundles(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, _, _ := newFakeWindowsRelease(t, "0.162.0")
	for _, p := range []string{
		"v0.162.0/otelcol-contrib_0.162.0_windows_amd64.tar.gz",
		"cmd/opampsupervisor/v0.162.0/opampsupervisor_0.162.0_windows_amd64.exe",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(relDir, p+".sigstore.json"), []byte(`{"bundle":true}`), 0o644))
	}
	args := []string{"-OpampEndpoint", "wss://x/v1/opamp", "-Token", "myre_x", "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", "0.162.0"}
	run := func(root string, code int) (string, string, error) {
		verifier, calls := writeFakeVerifier(t, code)
		out, err := runPwshInstallRaw(t, root, append(append([]string{}, args...), "-Insecure", "-ReleaseBaseUrl", "file://"+relDir, "-Verifier", verifier)...)
		raw, _ := os.ReadFile(calls)
		return out, string(raw), err
	}

	root := t.TempDir()
	out, calls, err := run(root, 0)
	require.NoError(t, err, out)
	assert.Equal(t, 2, strings.Count(calls, "verify-bundle"), calls)
	assert.Contains(t, calls, "--version v0.162.0")
	assert.Contains(t, calls, "base-binary-release")
	// the identity patterns are bound to the requested version (no generic tag pattern)
	assert.Equal(t, 2, strings.Count(calls, `@refs/tags/v0\.162\.0$`), calls)
	assert.NotContains(t, calls, `v[0-9]+`)

	root = t.TempDir()
	out, _, err = run(root, 1)
	require.Error(t, err)
	assert.Contains(t, out, "[sigstore_verification_failed]")
	assert.NoDirExists(t, filepath.Join(root, "ProgramFiles", "Myriarch", "collector", "versions", "0.162.0"))
}

// addWindowsUpdaterRelease adds the Windows updater, checksums.txt and a placeholder bundle for version to a fake release
// and returns the updater file name.
func addWindowsUpdaterRelease(t *testing.T, relDir, version string) (name string, content []byte) {
	t.Helper()
	name = "myriarch-updater_" + version + "_windows_amd64.exe"
	content = []byte("MZ-fake-updater-" + version)
	require.NoError(t, os.WriteFile(filepath.Join(relDir, name), content, 0o755))
	other := []byte("fake zip")
	require.NoError(t, os.WriteFile(filepath.Join(relDir, "myriarch-updater_"+version+"_windows_amd64.zip"), other, 0o644))
	sums := hex.EncodeToString(sha(content)) + "  " + name + "\n" + hex.EncodeToString(sha(other)) + "  myriarch-updater_" + version + "_windows_amd64.zip\n"
	require.NoError(t, os.WriteFile(filepath.Join(relDir, "checksums.txt"), []byte(sums), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(relDir, "checksums.txt.sigstore.json"), []byte(`{"bundle":true}`), 0o644))
	return name, content
}

func windowsGoodArgs() []string {
	return []string{"-OpampEndpoint", "wss://x/v1/opamp", "-Token", "myre_x", "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", "0.162.0"}
}

func TestInstallWindowsScriptVerifiesTheKeylessBundleOfChecksums(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, _, _ := newFakeWindowsRelease(t, "0.162.0")
	for _, p := range []string{
		"v0.162.0/otelcol-contrib_0.162.0_windows_amd64.tar.gz",
		"cmd/opampsupervisor/v0.162.0/opampsupervisor_0.162.0_windows_amd64.exe",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(relDir, p+".sigstore.json"), []byte(`{"bundle":true}`), 0o644))
	}
	name, content := addWindowsUpdaterRelease(t, relDir, "0.1.0-rc.1")
	args := func(verifier string, extra ...string) []string {
		return append(append(append([]string{}, windowsGoodArgs()...), "-Insecure", "-ReleaseBaseUrl", "file://"+relDir, "-Verifier", verifier,
			"-WithUpdater", "-UpdaterUrl", "file://"+relDir+"/"+name, "-UpdaterChecksumsUrl", "file://"+relDir+"/checksums.txt"), extra...)
	}

	verifier, calls := writeFakeVerifier(t, 0)
	root := t.TempDir()
	out, err := runPwshInstallRaw(t, root, args(verifier)...)
	require.NoError(t, err, out)
	lines := readCalls(t, calls)
	require.Len(t, lines, 3, "checksums.txt, collector and supervisor: %v", lines)
	assert.Contains(t, lines[0], "verify-bundle")
	assert.Contains(t, lines[0], "checksums.txt.sigstore.json")
	assert.Contains(t, lines[0], "--version v0.1.0-rc.1")
	assert.Contains(t, lines[0], `--identity-regexp ^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.1\.0-rc\.1$`)
	assert.Contains(t, lines[0], "--issuer https://token.actions.githubusercontent.com")
	got, err := os.ReadFile(filepath.Join(root, "ProgramFiles", "Myriarch", "updater", "myriarch-updater.exe"))
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// a bundle that does not authenticate checksums.txt: nothing is installed
	failing, _ := writeFakeVerifier(t, 1)
	root = t.TempDir()
	out, err = runPwshInstallRaw(t, root, args(failing)...)
	require.Error(t, err)
	assert.Contains(t, out, "[sigstore_verification_failed]")
	assert.Contains(t, out, "checksums.txt")
	assert.NoFileExists(t, filepath.Join(root, "ProgramFiles", "Myriarch", "updater", "myriarch-updater.exe"))
	assert.NoFileExists(t, filepath.Join(root, "ProgramData", "Myriarch", "config", "supervisor.yaml"))

	// the version binding: -UpdaterVersion must agree with the file name; a file name without a version needs it
	out, err = runPwshInstallRaw(t, t.TempDir(), args(verifier, "-UpdaterVersion", "0.1.0")...)
	require.Error(t, err)
	assert.Contains(t, out, "[option_conflict]")
	plain := "myriarch-updater-plain.exe"
	require.NoError(t, os.WriteFile(filepath.Join(relDir, plain), content, 0o755))
	sums, err := os.ReadFile(filepath.Join(relDir, "checksums.txt"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(relDir, "checksums.txt"), append(sums, []byte(hex.EncodeToString(sha(content))+"  "+plain+"\n")...), 0o644))
	plainArgs := append(append([]string{}, windowsGoodArgs()...), "-Insecure", "-ReleaseBaseUrl", "file://"+relDir, "-Verifier", verifier,
		"-WithUpdater", "-UpdaterUrl", "file://"+relDir+"/"+plain, "-UpdaterChecksumsUrl", "file://"+relDir+"/checksums.txt")
	out, err = runPwshInstallRaw(t, t.TempDir(), plainArgs...)
	require.Error(t, err)
	assert.Contains(t, out, "[option_missing]")
	assert.Contains(t, out, "-UpdaterVersion")
	out, err = runPwshInstallRaw(t, t.TempDir(), append(plainArgs, "-UpdaterVersion", "0.1.0-rc.1")...)
	require.NoError(t, err, out)
}

func TestInstallWindowsScriptRequiresCosign304OrNewer(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, _, _ := newFakeWindowsRelease(t, "0.162.0")
	name, _ := addWindowsUpdaterRelease(t, relDir, "0.1.0")
	for version, ok := range map[string]bool{"v3.0.3": false, "v2.4.1": false, "v3.0.4": true, "v3.1.3": true, "devel": false, "v3.0.4-rc.1": false, "v3.0.5-rc.1": true} {
		t.Run(version, func(t *testing.T) {
			cosign, _ := writeFakeCosign(t, version, 0)
			root := t.TempDir()
			out, err := runPwshInstallRaw(t, root, append(append([]string{}, windowsGoodArgs()...), "-Insecure", "-ReleaseBaseUrl", "file://"+relDir, "-Verifier", cosign,
				"-WithUpdater", "-UpdaterUrl", "file://"+relDir+"/"+name, "-UpdaterChecksumsUrl", "file://"+relDir+"/checksums.txt")...)
			if ok {
				// the collector bundles are missing in this fake release: the run gets as far as the first download of a bundle
				assert.NotContains(t, out, "[sigstore_verifier_outdated]")
				return
			}
			require.Error(t, err)
			assert.Contains(t, out, "[sigstore_verifier_outdated]")
			assert.Contains(t, out, "3.0.4")
			assert.NoFileExists(t, filepath.Join(root, "ProgramFiles", "Myriarch", "updater", "myriarch-updater.exe"))
		})
	}
}

func TestInstallWindowsScriptRemovedKeyOptionsNameTheirReplacement(t *testing.T) {
	skipOnWindowsHost(t)
	cases := map[string]struct {
		args        []string
		replacement string
	}{
		"-SigningKey":             {[]string{"-SigningKey", "cosign.pub"}, "-UpdaterChecksumsUrl"},
		"-UpdaterChecksumsSigUrl": {[]string{"-UpdaterChecksumsSigUrl", "https://mirror.example.com/checksums.txt.sig"}, "-UpdaterBundleUrl"},
	}
	// an empty value is still a use of the removed option
	cases["-SigningKey empty"] = struct {
		args        []string
		replacement string
	}{[]string{"-SigningKey", ""}, "-UpdaterChecksumsUrl"}
	cases["-UpdaterChecksumsSigUrl empty"] = struct {
		args        []string
		replacement string
	}{[]string{"-UpdaterChecksumsSigUrl", ""}, "-UpdaterBundleUrl"}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			out, err := runPwshInstallRaw(t, root, append(windowsGoodArgs(), c.args...)...)
			require.Error(t, err, out)
			assert.Contains(t, out, "[option_removed]")
			assert.Contains(t, out, c.replacement)
			assert.NoFileExists(t, filepath.Join(root, "ProgramData", "Myriarch", "config", "supervisor.yaml"))
		})
	}
}

func TestInstallWindowsScriptRejectsInconsistentUpdaterOptions(t *testing.T) {
	skipOnWindowsHost(t)
	relDir, _, updater := newFakeWindowsRelease(t, "0.162.0")
	name, _ := addWindowsUpdaterRelease(t, relDir, "0.1.0")
	verifier, _ := writeFakeVerifier(t, 0)
	url := "file://" + relDir + "/" + name
	base := append(append([]string{}, windowsGoodArgs()...), "-Insecure", "-ReleaseBaseUrl", "file://"+relDir)
	cases := map[string][]string{
		"bundle URL without checksums URL":          append(append([]string{}, base...), "-WithUpdater", "-UpdaterUrl", url, "-UpdaterSha256", hex.EncodeToString(sha(updater)), "-UpdaterBundleUrl", url+".sigstore.json"),
		"checksums URL with -VerifySignatures skip": append(append([]string{}, base...), "-VerifySignatures", "skip", "-WithUpdater", "-UpdaterUrl", url, "-UpdaterChecksumsUrl", "file://"+relDir+"/checksums.txt"),
		"checksums URL without -WithUpdater":        append(append([]string{}, base...), "-Verifier", verifier, "-UpdaterChecksumsUrl", "file://"+relDir+"/checksums.txt"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			out, err := runPwshInstallRaw(t, root, args...)
			require.Error(t, err, out)
			assert.Contains(t, out, "[option_conflict]")
			assert.NoFileExists(t, filepath.Join(root, "ProgramData", "Myriarch", "config", "supervisor.yaml"))
		})
	}
}

func skipOnWindowsHost(t *testing.T) {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("staging test uses POSIX-style fake binaries")
	}
}

// Get-Help must find the comment-based help of the scripts. A "#Requires" line that sits directly above the "<#" of the help
// block merges into it, and PowerShell then lists only the parameters.
func TestWindowsScriptsHaveCommentBasedHelp(t *testing.T) {
	pwsh := pwshPath(t)
	for _, rel := range []string{"scripts/install-windows.ps1", "windows/Set-ServiceEnvironment.ps1"} {
		script, err := filepath.Abs(filepath.Join(deployDir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", "(Get-Help -Name $env:MYRIARCH_TEST_SCRIPT).Synopsis")
		cmd.Env = append(os.Environ(), "MYRIARCH_TEST_SCRIPT="+script)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "%s: %s", rel, out)
		synopsis := strings.TrimSpace(string(out))
		assert.NotEmptyf(t, synopsis, "%s: Get-Help finds a synopsis", rel)
		assert.NotContainsf(t, synopsis, "[-", "%s: the synopsis is the .SYNOPSIS text, not the syntax line of the script", rel)
	}
}
