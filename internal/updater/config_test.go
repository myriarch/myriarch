// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const specConfig = `
server:
  endpoint: wss://fleet.example.com/v1/opamp
  tokenFile: /etc/myriarch/enrollment-token
  caFile: ""
supervisor:
  storageDir: /var/lib/otelcol/supervisor
  serviceName: otelcol-supervisor
  healthUrl: http://localhost:13134/health
collector:
  installDir: /opt/myriarch/collector
  binaryName: otelcol-contrib
  healthUrl: http://localhost:13133/
  keepVersions: 2
update:
  allowedUrlPrefixes: ["https://fleet.example.com/api/v1/artifacts/"]
  requireSignature: true
  trustedRootFile: /etc/myriarch/trusted_root.json
  certificateIdentityRegexp: '^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
  certificateOidcIssuer: https://token.actions.githubusercontent.com
  healthTimeout: 120s
  preflightValidate: true
stateDir: /var/lib/myriarch-updater
log: {level: info}
`

func TestParseSpecConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(specConfig))
	require.NoError(t, err)
	assert.Equal(t, "wss://fleet.example.com/v1/opamp", cfg.Server.Endpoint)
	assert.Equal(t, 120*time.Second, cfg.Update.HealthTimeout.D())
	assert.True(t, cfg.Update.RequireSignature)
	assert.Equal(t, DefaultCertificateIdentityRegexp, cfg.Update.CertificateIdentityRegexp)
	assert.Equal(t, "/var/lib/otelcol/supervisor/persistent_state.yaml", cfg.SupervisorStatePath())
	assert.Equal(t, 3, cfg.Update.HealthSuccesses)
	assert.Equal(t, 5*time.Second, cfg.Update.HealthInterval.D())
}

func TestConfigDefaultsPerOS(t *testing.T) {
	l := defaultConfigFor("linux")
	assert.Equal(t, "symlink", l.Collector.Activation)
	assert.Equal(t, "otelcol-contrib", l.Collector.BinaryName)
	w := defaultConfigFor("windows")
	assert.Equal(t, "copy", w.Collector.Activation)
	assert.Equal(t, "otelcol-contrib.exe", w.Collector.BinaryName)
	assert.Equal(t, `C:\Program Files\Myriarch\collector`, w.Collector.InstallDir)
	assert.Equal(t, `C:\ProgramData\Myriarch\updater`, w.StateDir)
	assert.Equal(t, `C:\ProgramData\Myriarch\supervisor`, w.Supervisor.StorageDir)
	assert.True(t, w.Update.RequireSignature, "signature required by default")
}

func TestConfigEnvExpansion(t *testing.T) {
	t.Setenv("MYRIARCH_ENROLLMENT_TOKEN", "myre_abc")
	t.Setenv("FLEET_HOST", "fleet.example.com")
	cfg, err := ParseConfig([]byte(`
server:
  endpoint: wss://${env:FLEET_HOST}/v1/opamp
  token: "${env:MYRIARCH_ENROLLMENT_TOKEN}"
update:
  allowedUrlPrefixes: ["https://${env:FLEET_HOST}/api/v1/artifacts/", "${env:UNSET_X:-https://mirror.example.com/a/}"]
`))
	require.NoError(t, err)
	assert.Equal(t, "wss://fleet.example.com/v1/opamp", cfg.Server.Endpoint)
	assert.Equal(t, "myre_abc", cfg.Server.Token)
	assert.Equal(t, []string{"https://fleet.example.com/api/v1/artifacts/", "https://mirror.example.com/a/"}, cfg.Update.AllowedURLPrefixes)
}

func TestConfigUnsetEnvIsAnError(t *testing.T) {
	_, err := ParseConfig([]byte("server:\n  endpoint: wss://x/v1/opamp\n  token: \"${env:DEFINITELY_NOT_SET_12345}\"\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEFINITELY_NOT_SET_12345")
}

func TestConfigUnknownKeyRejected(t *testing.T) {
	_, err := ParseConfig([]byte("server:\n  endpoint: wss://x/v1/opamp\n  tokn: x\n"))
	require.Error(t, err)
}

func TestConfigDerivesAllowedPrefixFromEndpoint(t *testing.T) {
	cfg, err := ParseConfig([]byte("server:\n  endpoint: wss://fleet.example.com:8443/v1/opamp\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"https://fleet.example.com:8443/api/v1/artifacts/"}, cfg.Update.AllowedURLPrefixes)
	cfg, err = ParseConfig([]byte("server:\n  endpoint: ws://localhost:8080/v1/opamp\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:8080/api/v1/artifacts/"}, cfg.Update.AllowedURLPrefixes)
}

func TestConfigValidation(t *testing.T) {
	bad := map[string]string{
		"missing endpoint":     "stateDir: /x\n",
		"bad scheme":           "server: {endpoint: ftp://x/y}\n",
		"token and file":       "server: {endpoint: wss://x/v1/opamp, token: a, tokenFile: b}\n",
		"binary with path":     "server: {endpoint: wss://x/v1/opamp}\ncollector: {binaryName: ../x}\n",
		"bad activation":       "server: {endpoint: wss://x/v1/opamp}\ncollector: {activation: hardlink}\n",
		"bad regexp":           "server: {endpoint: wss://x/v1/opamp}\nupdate: {certificateIdentityRegexp: '('}\n",
		"signature w/o regexp": "server: {endpoint: wss://x/v1/opamp}\nupdate: {certificateIdentityRegexp: ''}\n",
		"unanchored regexp":    "server: {endpoint: wss://x/v1/opamp}\nupdate: {certificateIdentityRegexp: 'opentelemetry-collector-releases'}\n",
		"regexp without end":   "server: {endpoint: wss://x/v1/opamp}\nupdate: {certificateIdentityRegexp: '^https://example\\.test/wf@refs/tags/v1'}\n",
		"regexp alternation":   "server: {endpoint: wss://x/v1/opamp}\nupdate: {certificateIdentityRegexp: '^a|b$'}\n",
		"zero health timeout":  "server: {endpoint: wss://x/v1/opamp}\nupdate: {healthTimeout: 0s}\n",
		"prefix with creds":    "server: {endpoint: wss://x/v1/opamp}\nupdate: {allowedUrlPrefixes: ['https://u:p@x/']}\n",
		"prefix relative":      "server: {endpoint: wss://x/v1/opamp}\nupdate: {allowedUrlPrefixes: ['/api/']}\n",
		"bad duration":         "server: {endpoint: wss://x/v1/opamp}\nupdate: {healthTimeout: abc}\n",
	}
	for name, y := range bad {
		_, err := ParseConfig([]byte(y))
		assert.Error(t, err, name)
	}
}

func TestConfigKeepVersionsMinimumTwo(t *testing.T) {
	cfg, err := ParseConfig([]byte("server: {endpoint: wss://x/v1/opamp}\ncollector: {keepVersions: 0}\n"))
	require.NoError(t, err)
	assert.Equal(t, 2, cfg.Collector.KeepVersions)
}

func TestLoadConfigFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "updater.yaml")
	require.NoError(t, os.WriteFile(p, []byte(specConfig), 0o600))
	_, err := LoadConfig(p)
	require.NoError(t, err)
	_, err = LoadConfig(p + ".missing")
	require.Error(t, err)
}

func TestRotatingLogFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log", "u.log")
	rf, err := newRotatingFile(p, 100, 2)
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		_, err := rf.Write([]byte("0123456789 0123456789\n"))
		require.NoError(t, err)
	}
	require.NoError(t, rf.Close())
	assert.FileExists(t, p)
	assert.FileExists(t, p+".1")
	assert.FileExists(t, p+".2")
	assert.NoFileExists(t, p+".3")
}

func TestAnchoredRegexp(t *testing.T) {
	for pat, want := range map[string]bool{
		DefaultCertificateIdentityRegexp:          true,
		`^https://example\.test/wf@refs/tags/v1$`: true,
		`^(a|b)$`: true,
		`^a|b$`:   false,
		`a$`:      false,
		`^a`:      false,
		`^a\$`:    false,
		`(`:       false,
		``:        false,
	} {
		assert.Equal(t, want, anchoredRegexp(pat), pat)
	}
}
