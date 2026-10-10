// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// These tests keep the deployment artifacts (deploy/) consistent with the
// code: supervisor config keys, updater.yaml templates and the install script.

const deployDir = "../../deploy"

func readDeploy(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(deployDir, rel))
	require.NoError(t, err, rel)
	return b
}

// Allowed keys: exactly the supervisor v0.162.0 schema
// (docs/design/upstream-notes.md, Supervisor limits > Configuration keys that matter).
var (
	supTop    = []string{"server", "capabilities", "agent", "storage", "healthcheck", "telemetry", "extensions"}
	supServer = []string{"endpoint", "headers", "tls", "auth"}
	supCaps   = []string{"accepts_remote_config", "accepts_restart_command", "accepts_opamp_connection_settings", "reports_effective_config",
		"reports_own_metrics", "reports_own_logs", "reports_own_traces", "reports_health", "reports_available_components",
		"reports_heartbeat", "accepts_packages", "reports_remote_config"}
	supAgent = []string{"executable", "instance_id", "orphan_detection_interval", "description", "config_apply_timeout", "bootstrap_timeout",
		"opamp_server_port", "passthrough_logs", "collector_crash_log_snippet_kib", "automatic_config_rollback", "use_hup_config_reload",
		"validate_config", "config_files", "args", "env", "startup_fallback_configs", "package"}
)

func keysOf(m map[string]any) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

func assertSubset(t *testing.T, where string, got []string, allowed []string) {
	t.Helper()
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	for _, g := range got {
		assert.True(t, set[g], "%s: unknown supervisor config key %q", where, g)
	}
}

func TestSupervisorConfigTemplatesUseExactSupervisorKeys(t *testing.T) {
	for _, platform := range []string{"linux", "windows", "k8s"} {
		t.Run(platform, func(t *testing.T) {
			var cfg map[string]any
			require.NoError(t, yaml.Unmarshal(readDeploy(t, "supervisor/"+platform+"/supervisor.yaml"), &cfg))
			assertSubset(t, "top level", keysOf(cfg), supTop)
			server := cfg["server"].(map[string]any)
			assertSubset(t, "server", keysOf(server), supServer)
			assert.NotEmpty(t, server["endpoint"])

			hdr := server["headers"].(map[string]any)
			assert.Equal(t, "Bearer ${env:MYRIARCH_ENROLLMENT_TOKEN}", hdr["Authorization"], "token only via environment")

			caps := cfg["capabilities"].(map[string]any)
			assertSubset(t, "capabilities", keysOf(caps), supCaps)
			for _, on := range []string{"accepts_remote_config", "accepts_restart_command", "reports_effective_config", "reports_health", "reports_available_components", "reports_heartbeat"} {
				assert.Equal(t, true, caps[on], on)
			}
			assert.Equal(t, false, caps["accepts_opamp_connection_settings"], "deliberately off (upstream-notes.md, Remote configuration handling)")
			assert.Equal(t, false, caps["reports_own_metrics"])

			agent := cfg["agent"].(map[string]any)
			assertSubset(t, "agent", keysOf(agent), supAgent)
			assert.Equal(t, "30s", agent["config_apply_timeout"])
			assert.Equal(t, "15s", agent["bootstrap_timeout"])
			assert.Equal(t, true, agent["validate_config"])
			assert.Equal(t, true, agent["automatic_config_rollback"])
			assert.NotEmpty(t, agent["startup_fallback_configs"])
			desc := agent["description"].(map[string]any)
			assertSubset(t, "agent.description", keysOf(desc), []string{"identifying_attributes", "non_identifying_attributes", "include_resource_attributes"})
			attrs := desc["non_identifying_attributes"].(map[string]any)
			for _, k := range []string{"site.type", "site.id", "site.name", "site.country", "site.region", "site.timezone", "deployment.environment.name"} {
				assert.Contains(t, attrs, k, "site contract attribute (docs/concepts/agents.md, \"The site attribute contract\")")
			}
			assertSubset(t, "storage", keysOf(cfg["storage"].(map[string]any)), []string{"directory"})
			assert.Contains(t, cfg["healthcheck"].(map[string]any)["endpoint"], ":13134")

			switch platform {
			case "linux":
				assert.Equal(t, "/opt/myriarch/collector/current/otelcol-contrib", agent["executable"])
				assert.Equal(t, "/var/lib/otelcol/supervisor", cfg["storage"].(map[string]any)["directory"])
			case "windows":
				assert.Equal(t, `C:\Program Files\Myriarch\collector\bin\otelcol-contrib.exe`, agent["executable"])
				assert.Equal(t, `C:\ProgramData\Myriarch\supervisor`, cfg["storage"].(map[string]any)["directory"])
			case "k8s":
				assert.Equal(t, true, agent["passthrough_logs"])
				for _, k := range []string{"k8s.cluster.name", "k8s.namespace.name", "k8s.pod.name", "k8s.node.name"} {
					assert.Contains(t, attrs, k)
				}
				assert.Contains(t, attrs["k8s.pod.name"], "${env:K8S_POD_NAME}")
			}
		})
	}
}

func TestFallbackConfigsAreValidCollectorConfigs(t *testing.T) {
	for _, platform := range []string{"linux", "windows", "k8s"} {
		var cfg map[string]any
		require.NoError(t, yaml.Unmarshal(readDeploy(t, "supervisor/"+platform+"/fallback.yaml"), &cfg), platform)
		assert.Contains(t, cfg, "service")
		ext := cfg["extensions"].(map[string]any)
		assert.Contains(t, ext, "health_check", "the myriarch-updater health check needs the collector health endpoint")
		svc := cfg["service"].(map[string]any)
		assert.Contains(t, svc["extensions"], "health_check")
	}
}

func TestShippedUnitMatchesGoTemplate(t *testing.T) {
	unit := string(readDeploy(t, "systemd/myriarch-updater.service"))
	// every fixed (placeholder free) directive of the kardianos template must be in the shipped unit
	for _, line := range strings.Split(systemdUnitTemplate, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "{{") {
			continue
		}
		assert.Contains(t, unit, line, "deploy/systemd/myriarch-updater.service is out of sync with updater.go")
	}
	assert.Contains(t, unit, "ExecStart=/opt/myriarch/updater/myriarch-updater run --config /etc/myriarch/updater.yaml")
	assert.Contains(t, unit, "User=root")

	sup := string(readDeploy(t, "systemd/otelcol-supervisor.service"))
	for _, want := range []string{
		"User=otelcol", "EnvironmentFile=/etc/myriarch/myriarch.env",
		"ExecStart=/opt/myriarch/supervisor/opampsupervisor --config /etc/myriarch/supervisor.yaml",
		"Restart=always", "LimitNOFILE=65536", "StateDirectory=otelcol",
	} {
		assert.Contains(t, sup, want)
	}
}

func TestUpdaterTemplatesParseAsUpdaterConfig(t *testing.T) {
	for platform, goos := range map[string]string{"linux": "linux", "windows": "windows"} {
		t.Run(platform, func(t *testing.T) {
			raw := string(readDeploy(t, "updater/"+platform+"/updater.yaml"))
			raw = strings.NewReplacer(
				"@@OPAMP_ENDPOINT@@", "wss://fleet.example.com/v1/opamp",
				"@@CA_FILE@@", "", "@@TRUSTED_ROOT_FILE@@", "").Replace(raw)
			cfg := defaultConfigFor(goos)
			dec := yaml.NewDecoder(strings.NewReader(raw))
			dec.KnownFields(true)
			require.NoError(t, dec.Decode(&cfg), "updater template must only use keys the updater knows")
			require.NoError(t, cfg.Validate())
			def := defaultConfigFor(goos)
			assert.Equal(t, def.Collector.InstallDir, cfg.Collector.InstallDir)
			assert.Equal(t, def.Collector.BinaryName, cfg.Collector.BinaryName)
			assert.Equal(t, def.Collector.Activation, cfg.Collector.Activation)
			assert.Equal(t, def.StateDir, cfg.StateDir)
			assert.Equal(t, def.Supervisor.StorageDir, cfg.Supervisor.StorageDir)
			assert.Equal(t, "otelcol-supervisor", cfg.Supervisor.ServiceName)
			assert.Equal(t, DefaultCertificateIdentityRegexp, cfg.Update.CertificateIdentityRegexp)
			assert.True(t, cfg.Update.RequireSignature)
		})
	}
}

// --- install-linux.sh in staging mode against a fake release mirror -----------

type fakeRelease struct {
	base     string // file:// URL
	dir      string
	archive  []byte
	updater  []byte
	superBin []byte
}

func newFakeRelease(t *testing.T, collectorVersion, supervisorVersion string, arch string) *fakeRelease {
	t.Helper()
	dir := t.TempDir()
	r := &fakeRelease{dir: dir, base: "file://" + dir}
	r.archive = makeTarGz(t,
		tarEntry{name: "README.md", body: []byte("readme"), mode: 0o644},
		tarEntry{name: testBinary, body: fakeCollectorScript(collectorVersion, 0)},
	)
	put := func(rel string, data []byte) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, data, 0o644))
		require.NoError(t, os.WriteFile(p+".sha256", []byte(hex.EncodeToString(sha(data))), 0o644)) // upstream: 64 hex chars, no newline
	}
	put("v"+collectorVersion+"/otelcol-contrib_"+collectorVersion+"_linux_"+arch+".tar.gz", r.archive)
	r.superBin = []byte("#!/bin/sh\necho supervisor\n")
	put("cmd/opampsupervisor/v"+supervisorVersion+"/opampsupervisor_"+supervisorVersion+"_linux_"+arch, r.superBin)
	r.updater = []byte("#!/bin/sh\necho updater\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "myriarch-updater_linux_"+arch), r.updater, 0o755))
	return r
}

// runInstall runs the installer in the lab mode (--insecure: file:// mirror, no signature bundles).
func runInstall(t *testing.T, root string, rel *fakeRelease, extra ...string) (string, error) {
	t.Helper()
	return runInstallRaw(t, root, append([]string{"--insecure", "--verify-signatures", "skip", "--release-base-url", rel.base}, extra...)...)
}

func runInstallRaw(t *testing.T, root string, extra ...string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash installer")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	args := append([]string{filepath.Join(deployDir, "scripts/install-linux.sh"),
		"--root", root, "--arch", "amd64", "--templates-dir", deployDir,
	}, extra...)
	cmd := exec.Command("bash", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func baseArgs() []string {
	return []string{"--opamp-endpoint", "wss://fleet.example.com/v1/opamp", "--token", "myre_abcDEF123",
		"--site-type", "edge", "--site-id", "1001", "--collector-version", "0.162.0"}
}

func TestInstallLinuxScriptStagingInstall(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	root := t.TempDir()
	args := append(baseArgs(), "--site-name", `Site "North" & Co \ South/1`, "--site-country", "NL", "--site-timezone", "Europe/Amsterdam", "--environment", "prod")
	out, err := runInstall(t, root, rel, args...)
	require.NoError(t, err, out)

	// collector: versioned dir, marker, current symlink
	l := &Layout{InstallDir: filepath.Join(root, "opt/myriarch/collector"), BinaryName: "otelcol-contrib", Activation: "symlink"}
	cur, err := l.CurrentVersion()
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", cur)
	assert.Equal(t, sha(rel.archive), l.ActiveContentHash(), "marker format is compatible with myriarch-updater")
	vout, err := exec.Command(filepath.Join(l.InstallDir, "current", "otelcol-contrib"), "--version").Output()
	require.NoError(t, err)
	assert.Contains(t, string(vout), "version 0.162.0")
	assert.NoFileExists(t, filepath.Join(l.VersionDir("0.162.0"), "README.md"), "only the binary is extracted")
	var marker map[string]string
	b, _ := os.ReadFile(filepath.Join(l.VersionDir("0.162.0"), ".myriarch-install.json"))
	require.NoError(t, json.Unmarshal(b, &marker))
	assert.Equal(t, "0.162.0", marker["version"])

	// supervisor
	assert.FileExists(t, filepath.Join(root, "opt/myriarch/supervisor/opampsupervisor"))
	v, _ := os.ReadFile(filepath.Join(root, "opt/myriarch/supervisor/VERSION"))
	assert.Equal(t, "0.162.0\n", string(v))

	// rendered supervisor.yaml: valid YAML, exact values, no leftovers
	raw, err := os.ReadFile(filepath.Join(root, "etc/myriarch/supervisor.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "@@")
	assert.NotRegexp(t, `(?m)^  tls:`, string(raw), "no CA given: tls block removed")
	assert.NotRegexp(t, `(?m)^\s+ca_file:`, string(raw))
	var cfg struct {
		Server struct {
			Endpoint string            `yaml:"endpoint"`
			Headers  map[string]string `yaml:"headers"`
		} `yaml:"server"`
		Agent struct {
			Executable string `yaml:"executable"`
			Desc       struct {
				NonIdentifying map[string]string `yaml:"non_identifying_attributes"`
			} `yaml:"description"`
		} `yaml:"agent"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	assert.Equal(t, "wss://fleet.example.com/v1/opamp", cfg.Server.Endpoint)
	assert.Equal(t, "Bearer ${env:MYRIARCH_ENROLLMENT_TOKEN}", cfg.Server.Headers["Authorization"])
	attrs := cfg.Agent.Desc.NonIdentifying
	assert.Equal(t, "edge", attrs["site.type"])
	assert.Equal(t, "1001", attrs["site.id"], "site.id stays a string")
	assert.Equal(t, `Site "North" & Co \ South/1`, attrs["site.name"], "special characters survive YAML escaping")
	assert.Equal(t, "NL", attrs["site.country"])
	assert.Equal(t, "Europe/Amsterdam", attrs["site.timezone"])
	assert.Equal(t, "prod", attrs["deployment.environment.name"])
	assert.NotContains(t, attrs, "site.region", "empty optional values are removed, not sent as empty strings")
	assert.NotContains(t, string(raw), "myre_abcDEF123", "the token never appears in supervisor.yaml")

	// secrets: 0600
	envInfo, err := os.Stat(filepath.Join(root, "etc/myriarch/myriarch.env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), envInfo.Mode().Perm())
	envData, _ := os.ReadFile(filepath.Join(root, "etc/myriarch/myriarch.env"))
	assert.Contains(t, string(envData), "MYRIARCH_ENROLLMENT_TOKEN=myre_abcDEF123\n")
	assert.Contains(t, string(envData), "OTEL_GATEWAY_TOKEN", "placeholder for collector secrets")
	tokInfo, err := os.Stat(filepath.Join(root, "etc/myriarch/enrollment-token"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), tokInfo.Mode().Perm())

	assert.FileExists(t, filepath.Join(root, "etc/myriarch/fallback.yaml"))
	assert.FileExists(t, filepath.Join(root, "etc/systemd/system/otelcol-supervisor.service"))
	// the supervisor storage is created empty, never pre-populated (golden image pitfall)
	entries, err := os.ReadDir(filepath.Join(root, "var/lib/otelcol/supervisor"))
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.NoFileExists(t, filepath.Join(root, "etc/systemd/system/myriarch-updater.service"), "no updater without --with-updater")
}

func TestInstallLinuxScriptKeepsLeadingZerosInSiteID(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	root := t.TempDir()
	args := append([]string(nil), baseArgs()...)
	args[7] = "0042" // --site-id: a string, leading zeros are kept
	out, err := runInstall(t, root, rel, args...)
	require.NoError(t, err, out)
	raw, err := os.ReadFile(filepath.Join(root, "etc/myriarch/supervisor.yaml"))
	require.NoError(t, err)
	var cfg struct {
		Agent struct {
			Desc struct {
				NonIdentifying map[string]string `yaml:"non_identifying_attributes"`
			} `yaml:"description"`
		} `yaml:"agent"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	assert.Equal(t, "0042", cfg.Agent.Desc.NonIdentifying["site.id"])
}

func TestInstallLinuxScriptIsIdempotentAndPreservesLocalEdits(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	root := t.TempDir()
	out, err := runInstall(t, root, rel, baseArgs()...)
	require.NoError(t, err, out)

	// local edits: fallback.yaml and extra secrets in myriarch.env must survive
	fb := filepath.Join(root, "etc/myriarch/fallback.yaml")
	require.NoError(t, os.WriteFile(fb, []byte("# local\n"), 0o644))
	envp := filepath.Join(root, "etc/myriarch/myriarch.env")
	data, _ := os.ReadFile(envp)
	require.NoError(t, os.WriteFile(envp, append(data, []byte("OTEL_GATEWAY_TOKEN=secret-gw\n")...), 0o600))
	before, _ := os.Stat(filepath.Join(root, "etc/myriarch/supervisor.yaml"))

	out, err = runInstall(t, root, rel, baseArgs()...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "already installed")
	got, _ := os.ReadFile(fb)
	assert.Equal(t, "# local\n", string(got))
	data, _ = os.ReadFile(envp)
	assert.Contains(t, string(data), "OTEL_GATEWAY_TOKEN=secret-gw")
	assert.Equal(t, 1, strings.Count(string(data), "MYRIARCH_ENROLLMENT_TOKEN="))
	after, _ := os.Stat(filepath.Join(root, "etc/myriarch/supervisor.yaml"))
	assert.Equal(t, before.ModTime(), after.ModTime(), "unchanged config is not rewritten")

	// token rotation replaces only the token line
	args := append([]string(nil), baseArgs()...)
	args[3] = "myre_ROTATED999"
	out, err = runInstall(t, root, rel, args...)
	require.NoError(t, err, out)
	data, _ = os.ReadFile(envp)
	assert.Contains(t, string(data), "MYRIARCH_ENROLLMENT_TOKEN=myre_ROTATED999")
	assert.NotContains(t, string(data), "myre_abcDEF123")
	assert.Contains(t, string(data), "OTEL_GATEWAY_TOKEN=secret-gw")
	assert.NoFileExists(t, envp+".bak", "no backup that would keep the old token")
}

func TestInstallLinuxScriptUpgradeSwitchesSymlinkAndKeepsOldVersion(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	root := t.TempDir()
	_, err := runInstall(t, root, rel, baseArgs()...)
	require.NoError(t, err)
	// second release in the same mirror
	rel2 := newFakeRelease(t, "0.163.0", "0.163.0", "amd64")
	args := append([]string(nil), baseArgs()...)
	args[len(args)-1] = "0.163.0"
	out, err := runInstall(t, root, rel2, args...)
	require.NoError(t, err, out)
	l := &Layout{InstallDir: filepath.Join(root, "opt/myriarch/collector"), BinaryName: "otelcol-contrib", Activation: "symlink"}
	cur, _ := l.CurrentVersion()
	assert.Equal(t, "0.163.0", cur)
	assert.FileExists(t, l.BinaryPath("0.162.0"), "previous version stays for rollback")
}

func TestInstallLinuxScriptRejectsBadChecksumAndInstallsNothing(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	// corrupt the archive after the .sha256 was written
	p := filepath.Join(rel.dir, "v0.162.0", "otelcol-contrib_0.162.0_linux_amd64.tar.gz")
	data, _ := os.ReadFile(p)
	data[len(data)-5] ^= 0xff
	require.NoError(t, os.WriteFile(p, data, 0o644))
	root := t.TempDir()
	out, err := runInstall(t, root, rel, baseArgs()...)
	require.Error(t, err)
	assert.Contains(t, out, "[checksum_mismatch]")
	assert.NoFileExists(t, filepath.Join(root, "opt/myriarch/collector/current"))
	assert.NoDirExists(t, filepath.Join(root, "opt/myriarch/collector/versions/0.162.0"))
	assert.NoFileExists(t, filepath.Join(root, "etc/myriarch/supervisor.yaml"))
}

func TestInstallLinuxScriptRejectsWrongBinaryVersion(t *testing.T) {
	rel := newFakeRelease(t, "0.150.0", "0.162.0", "amd64") // archive v0.162.0 name, but binary says 0.150.0
	require.NoError(t, os.Rename(
		filepath.Join(rel.dir, "v0.150.0"), filepath.Join(rel.dir, "v0.162.0")))
	for _, ext := range []string{"", ".sha256"} {
		require.NoError(t, os.Rename(
			filepath.Join(rel.dir, "v0.162.0", "otelcol-contrib_0.150.0_linux_amd64.tar.gz"+ext),
			filepath.Join(rel.dir, "v0.162.0", "otelcol-contrib_0.162.0_linux_amd64.tar.gz"+ext)))
	}
	root := t.TempDir()
	out, err := runInstall(t, root, rel, baseArgs()...)
	require.Error(t, err)
	assert.Contains(t, out, "[collector_version_mismatch]")
	assert.Contains(t, out, "expected version 0.162.0")
	assert.NoFileExists(t, filepath.Join(root, "opt/myriarch/collector/current"))
}

func TestInstallLinuxScriptValidatesArguments(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	cases := map[string][]string{
		"missing endpoint": {"--token", "myre_x", "--site-type", "edge", "--site-id", "1", "--collector-version", "0.162.0"},
		"bad endpoint":     {"--opamp-endpoint", "ftp://x", "--token", "myre_x", "--site-type", "edge", "--site-id", "1", "--collector-version", "0.162.0"},
		"missing token":    {"--opamp-endpoint", "wss://x/v1/opamp", "--site-type", "edge", "--site-id", "1", "--collector-version", "0.162.0"},
		"missing site id":  {"--opamp-endpoint", "wss://x/v1/opamp", "--token", "myre_x", "--site-type", "edge", "--collector-version", "0.162.0"},
		"bad version":      {"--opamp-endpoint", "wss://x/v1/opamp", "--token", "myre_x", "--site-type", "edge", "--site-id", "1", "--collector-version", "latest"},
		"token with space": {"--opamp-endpoint", "wss://x/v1/opamp", "--token", "ofe x", "--site-type", "edge", "--site-id", "1", "--collector-version", "0.162.0"},
		"updater w/o url":  append(baseArgs(), "--with-updater"),
		"unknown option":   append(baseArgs(), "--frobnicate"),
		"newline in name":  append(baseArgs(), "--site-name", "a\nb"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			out, err := runInstall(t, root, rel, args...)
			require.Error(t, err, out)
			assert.NoFileExists(t, filepath.Join(root, "etc/myriarch/supervisor.yaml"))
		})
	}
}

func TestInstallLinuxScriptWithCAAndUpdater(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	root := t.TempDir()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"), 0o644))
	tr := filepath.Join(t.TempDir(), "trusted_root.json")
	require.NoError(t, os.WriteFile(tr, readDeploy(t, "../internal/updater/testdata/sigstore/trusted_root.json"), 0o644))
	args := append(baseArgs(), "--ca-file", ca, "--with-updater",
		"--updater-url", rel.base+"/myriarch-updater_linux_amd64", "--updater-sha256", hex.EncodeToString(sha(rel.updater)),
		"--trusted-root", tr)
	out, err := runInstall(t, root, rel, args...)
	require.NoError(t, err, out)

	raw, _ := os.ReadFile(filepath.Join(root, "etc/myriarch/supervisor.yaml"))
	var sup struct {
		Server struct {
			TLS struct {
				CAFile string `yaml:"ca_file"`
			} `yaml:"tls"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &sup))
	// the bundle is copied into the protected configuration directory and referenced there
	assert.Equal(t, "/etc/myriarch/ca.pem", sup.Server.TLS.CAFile)
	copied, err := os.ReadFile(filepath.Join(root, "etc/myriarch/ca.pem"))
	require.NoError(t, err)
	assert.Contains(t, string(copied), "BEGIN CERTIFICATE")

	// the rendered updater.yaml is accepted by the updater's own config loader
	upPath := filepath.Join(root, "etc/myriarch/updater.yaml")
	cfg, err := LoadConfig(upPath)
	require.NoError(t, err)
	assert.Equal(t, "wss://fleet.example.com/v1/opamp", cfg.Server.Endpoint)
	assert.Equal(t, "/etc/myriarch/enrollment-token", cfg.Server.TokenFile)
	assert.Equal(t, "/etc/myriarch/ca.pem", cfg.Server.CAFile)
	assert.Equal(t, "/etc/myriarch/trusted_root.json", cfg.Update.TrustedRootFile)
	assert.True(t, cfg.Update.RequireSignature)
	assert.Equal(t, []string{"https://fleet.example.com/api/v1/artifacts/"}, cfg.Update.AllowedURLPrefixes)

	assert.FileExists(t, filepath.Join(root, "opt/myriarch/updater/myriarch-updater"))
	assert.FileExists(t, filepath.Join(root, "etc/myriarch/trusted_root.json"))
	assert.FileExists(t, filepath.Join(root, "etc/systemd/system/myriarch-updater.service"))

	// wrong updater hash is refused
	args2 := append(baseArgs(), "--with-updater", "--updater-url", rel.base+"/myriarch-updater_linux_amd64", "--updater-sha256", strings.Repeat("0", 64))
	out, err = runInstall(t, t.TempDir(), rel, args2...)
	require.Error(t, err)
	assert.Contains(t, out, "[checksum_mismatch]")
	assert.Contains(t, out, "myriarch-updater")
}

func TestInstallLinuxScriptTokenFileAndUninstall(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	root := t.TempDir()
	tf := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tf, []byte("myre_fromfile\n"), 0o600))
	args := []string{"--opamp-endpoint", "wss://fleet.example.com/v1/opamp", "--token-file", tf,
		"--site-type", "office", "--site-id", "OFFICE1", "--collector-version", "0.162.0"}
	out, err := runInstall(t, root, rel, args...)
	require.NoError(t, err, out)
	data, _ := os.ReadFile(filepath.Join(root, "etc/myriarch/myriarch.env"))
	assert.Contains(t, string(data), "MYRIARCH_ENROLLMENT_TOKEN=myre_fromfile\n")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib/otelcol/supervisor"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "var/lib/otelcol/supervisor/persistent_state.yaml"), []byte("instance_id: x\n"), 0o600))
	out, err = runInstall(t, root, rel, "--uninstall")
	require.NoError(t, err, out)
	assert.NoDirExists(t, filepath.Join(root, "opt/myriarch"))
	assert.NoDirExists(t, filepath.Join(root, "etc/myriarch"))
	assert.FileExists(t, filepath.Join(root, "var/lib/otelcol/supervisor/persistent_state.yaml"), "data (identity) survives a plain uninstall")
	out, err = runInstall(t, root, rel, "--uninstall", "--purge")
	require.NoError(t, err, out)
	assert.NoDirExists(t, filepath.Join(root, "var/lib/otelcol"))
}

func TestInstallLinuxScriptHelp(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	out, err := runInstall(t, t.TempDir(), rel, "--help")
	require.NoError(t, err)
	for _, opt := range []string{"--opamp-endpoint", "--token-file", "--site-type", "--site-id", "--site-name", "--site-country",
		"--site-region", "--site-timezone", "--environment", "--collector-version", "--supervisor-version", "--release-base-url",
		"--updater-url", "--with-updater", "--uninstall"} {
		assert.Contains(t, out, opt)
	}
}

// --- trust model of the bootstrap installer ---------------------------------

func addBundles(t *testing.T, rel *fakeRelease) {
	t.Helper()
	for _, p := range []string{
		"v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz",
		"cmd/opampsupervisor/v0.162.0/opampsupervisor_0.162.0_linux_amd64",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(rel.dir, p+".sigstore.json"), []byte(`{"bundle":true}`), 0o644))
	}
}

// fakeVerifier writes a script that records its arguments and exits with code.
func writeFakeVerifier(t *testing.T, code int) (path, logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "calls.log")
	path = filepath.Join(dir, "myriarch-updater")
	script := "#!/bin/sh\necho \"$@\" >> " + logFile + "\nexit " + strconv.Itoa(code) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path, logFile
}

func TestInstallLinuxScriptVerifiesSigstoreBundlesWithVerifier(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	addBundles(t, rel)
	verifier, calls := writeFakeVerifier(t, 0)
	root := t.TempDir()
	out, err := runInstallRaw(t, root, append([]string{"--insecure", "--release-base-url", rel.base, "--verifier", verifier, "--trusted-root", writeTrustedRoot(t)}, baseArgs()...)...)
	require.NoError(t, err, out)
	raw, _ := os.ReadFile(calls)
	log := string(raw)
	assert.Equal(t, 2, strings.Count(log, "verify-bundle"), "collector and supervisor are both verified: %s", log)
	assert.Contains(t, log, "--version v0.162.0")
	assert.Contains(t, log, "--identity-regexp")
	assert.Contains(t, log, "workflows/base-release")
	assert.Contains(t, log, "workflows/base-binary-release")
	assert.Contains(t, log, "--trusted-root")
	assert.FileExists(t, filepath.Join(root, "opt/myriarch/supervisor/opampsupervisor"))
}

func writeTrustedRoot(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "trusted_root.json")
	require.NoError(t, os.WriteFile(p, readDeploy(t, "../internal/updater/testdata/sigstore/trusted_root.json"), 0o644))
	return p
}

func TestInstallLinuxScriptFailsClosedOnBadSignature(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	addBundles(t, rel)
	verifier, _ := writeFakeVerifier(t, 1)
	root := t.TempDir()
	out, err := runInstallRaw(t, root, append([]string{"--insecure", "--release-base-url", rel.base, "--verifier", verifier}, baseArgs()...)...)
	require.Error(t, err)
	assert.Contains(t, out, "[sigstore_verification_failed]")
	assert.NoFileExists(t, filepath.Join(root, "opt/myriarch/collector/current"))
	assert.NoFileExists(t, filepath.Join(root, "etc/myriarch/supervisor.yaml"))
}

func TestInstallLinuxScriptFailsClosedWithoutBundleOrVerifier(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64") // no .sigstore.json published
	verifier, _ := writeFakeVerifier(t, 0)
	out, err := runInstallRaw(t, t.TempDir(), append([]string{"--insecure", "--release-base-url", rel.base, "--verifier", verifier}, baseArgs()...)...)
	require.Error(t, err, "a missing bundle is not skipped")
	assert.Contains(t, out, "[download_failed]")

	if _, lerr := exec.LookPath("cosign"); lerr == nil {
		t.Skip("cosign is installed: the no-verifier case cannot be provoked")
	}
	out, err = runInstallRaw(t, t.TempDir(), append([]string{"--insecure", "--release-base-url", rel.base}, baseArgs()...)...)
	require.Error(t, err)
	assert.Contains(t, out, "[sigstore_verifier_missing]")
}

func TestInstallLinuxScriptUsesDownloadedUpdaterAsVerifier(t *testing.T) {
	if _, lerr := exec.LookPath("cosign"); lerr == nil {
		t.Skip("cosign is installed and would be preferred")
	}
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	addBundles(t, rel)
	calls := filepath.Join(t.TempDir(), "calls.log")
	updater := []byte("#!/bin/sh\necho \"$@\" >> " + calls + "\nexit 0\n")
	require.NoError(t, os.WriteFile(filepath.Join(rel.dir, "myriarch-updater_linux_amd64"), updater, 0o755))
	root := t.TempDir()
	out, err := runInstallRaw(t, root, append([]string{"--insecure", "--release-base-url", rel.base,
		"--with-updater", "--updater-url", rel.base + "/myriarch-updater_linux_amd64", "--updater-sha256", hex.EncodeToString(sha(updater))}, baseArgs()...)...)
	require.NoError(t, err, out)
	raw, _ := os.ReadFile(calls)
	assert.Equal(t, 2, strings.Count(string(raw), "verify-bundle"), "the SHA-256-checked updater verifies the bundles before anything is installed")
}

func TestInstallLinuxScriptRefusesUntrustedTransportAndUnsignedWithoutInsecure(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	verifier, _ := writeFakeVerifier(t, 0)
	cases := map[string]struct {
		args []string
		want string
	}{
		"file mirror":    {append([]string{"--release-base-url", rel.base, "--verifier", verifier}, baseArgs()...), "[https_required]"},
		"http mirror":    {append([]string{"--release-base-url", "http://mirror.example.com/r", "--verifier", verifier}, baseArgs()...), "[https_required]"},
		"skip signature": {append([]string{"--release-base-url", "https://mirror.example.com/r", "--verify-signatures", "skip"}, baseArgs()...), "[insecure_required]"},
		"plain ws": {[]string{"--release-base-url", "https://mirror.example.com/r", "--verifier", verifier, "--opamp-endpoint", "ws://fleet.example.com/v1/opamp",
			"--token", "myre_x", "--site-type", "edge", "--site-id", "1", "--collector-version", "0.162.0"}, "[insecure_required]"},
		"updater w/o checksum": {append([]string{"--release-base-url", "https://mirror.example.com/r", "--verifier", verifier,
			"--with-updater", "--updater-url", "https://mirror.example.com/myriarch-updater"}, baseArgs()...), "[updater_hash_source_missing]"},
		"updater over http": {append([]string{"--release-base-url", "https://mirror.example.com/r", "--verifier", verifier,
			"--with-updater", "--updater-url", "http://mirror.example.com/myriarch-updater", "--updater-sha256", strings.Repeat("a", 64)}, baseArgs()...), "[https_required]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			out, err := runInstallRaw(t, root, c.args...)
			require.Error(t, err, out)
			assert.Contains(t, out, c.want)
			assert.NoFileExists(t, filepath.Join(root, "etc/myriarch/supervisor.yaml"))
		})
	}
}

func TestInstallLinuxScriptWarnsAboutTokenOnCommandLine(t *testing.T) {
	rel := newFakeRelease(t, "0.162.0", "0.162.0", "amd64")
	out, err := runInstall(t, t.TempDir(), rel, baseArgs()...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "[token_on_command_line]")
	assert.Contains(t, out, "--token-file")
}

// The supervisor and the collector run Git-declared configuration with the rights of the service account:
// the unit carries the hardening directives that do not restrict typical receivers.
func TestSupervisorUnitIsHardened(t *testing.T) {
	sup := string(readDeploy(t, "systemd/otelcol-supervisor.service"))
	lines := map[string]bool{}
	for _, line := range strings.Split(sup, "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	for _, want := range []string{
		"NoNewPrivileges=true", "PrivateTmp=true", "ProtectSystem=full", "ProtectHome=true",
		"CapabilityBoundingSet=", "AmbientCapabilities=", "RestrictSUIDSGID=true",
		"ProtectKernelModules=true", "ProtectKernelLogs=true", "ProtectClock=true", "ProtectControlGroups=true",
		"RestrictNamespaces=true", "LockPersonality=true", "SystemCallArchitectures=native", "UMask=0027",
	} {
		assert.True(t, lines[want], "otelcol-supervisor.service lacks %q", want)
	}
	assert.NotContains(t, sup, "ProtectSystem=strict", "file exporters need to write below /var and the data directories")
}
