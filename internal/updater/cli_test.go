// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestCLIHasAllCommands(t *testing.T) {
	root := NewRootCommand()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"run", "container", "install", "uninstall", "start", "stop", "status", "version", "verify-bundle", "fetch-trusted-root", "licenses"} {
		assert.True(t, have[want], want)
	}
	out, err := runCLI(t, "version")
	require.NoError(t, err)
	assert.Contains(t, out, "0.0.0-dev")
}

func TestCLILicensesPrintsTheEmbeddedTexts(t *testing.T) {
	out, err := runCLI(t, "licenses")
	require.NoError(t, err)
	assert.Equal(t, myriarch.License(), out)
	assert.Contains(t, out, "Apache License")
	out, err = runCLI(t, "licenses", "--third-party")
	require.NoError(t, err)
	assert.Equal(t, myriarch.ThirdPartyNotices(), out)
	assert.Contains(t, out, "Third-party software notices")
	_, err = runCLI(t, "licenses", "extra")
	require.Error(t, err)
}

func TestCLIRunRequiresValidConfig(t *testing.T) {
	_, err := runCLI(t, "run")
	require.ErrorContains(t, err, "--config is required")
	_, err = runCLI(t, "run", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("server: {endpoint: ftp://x}\n"), 0o600))
	_, err = runCLI(t, "run", "--config", bad)
	require.ErrorContains(t, err, "server.endpoint")
	_, err = runCLI(t, "install", "--config", bad)
	require.ErrorContains(t, err, "refusing to install with an invalid config")
}

func TestCLIVerifyBundleOffline(t *testing.T) {
	f := loadSigFixture(t)
	args := func(version string, extra ...string) []string {
		return append([]string{"verify-bundle",
			"--bundle", filepath.Join(sigDir, "otelcol-contrib_0.162.0_linux_amd64.tar.gz.sigstore.json"),
			"--sha256", hex.EncodeToString(f.digest),
			"--version", version,
			"--trusted-root", f.trustedRoot}, extra...)
	}
	out, err := runCLI(t, args("v0.162.0")...)
	require.NoError(t, err)
	assert.Contains(t, out, "OK:")
	_, err = runCLI(t, args("v0.161.0")...)
	require.Error(t, err)
	_, err = runCLI(t, "verify-bundle")
	require.Error(t, err)
}

func TestCLIStatusWithConfigPrintsHostState(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	require.NoError(t, os.WriteFile(e.cfg.SupervisorStatePath(), []byte("instance_id: 0198c9a4-1b6f-7c2e-9a41-3d5e6f708192\n"), 0o600))
	cfgPath := filepath.Join(e.root, "updater.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(
		"server: {endpoint: 'wss://x/v1/opamp'}\n"+
			"stateDir: "+e.cfg.StateDir+"\n"+
			"supervisor: {storageDir: "+e.cfg.Supervisor.StorageDir+"}\n"+
			"collector: {installDir: "+e.cfg.Collector.InstallDir+"}\n"), 0o600))
	out, err := runCLI(t, "status", "--config", cfgPath)
	require.NoError(t, err)
	assert.Contains(t, out, "active collector version: v0.162.0")
	assert.Contains(t, out, "supervisor instance uid: 0198c9a4-1b6f-7c2e-9a41-3d5e6f708192")
}

func TestCLIContainerRequiresValidConfig(t *testing.T) {
	// Windows has no container mode: the command refuses before it looks at the configuration.
	const containerUnsupported = "only supported on Linux and macOS"
	_, err := runCLI(t, "container")
	if runtime.GOOS == "windows" {
		require.ErrorContains(t, err, containerUnsupported)
	} else {
		require.ErrorContains(t, err, "--config is required")
	}
	// a valid service-mode config without the container section is refused by the container command
	cfgPath := filepath.Join(t.TempDir(), "updater.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("server: {endpoint: 'wss://x/v1/opamp', token: t}\n"), 0o600))
	_, err = runCLI(t, "container", "--config", cfgPath)
	require.Error(t, err)
	if runtime.GOOS == "windows" {
		assert.Contains(t, err.Error(), containerUnsupported)
		return
	}
	assert.Contains(t, err.Error(), "container.supervisorCommand is required")
}

func TestCLIFetchTrustedRootRequiresOut(t *testing.T) {
	_, err := runCLI(t, "fetch-trusted-root")
	require.ErrorContains(t, err, "--out is required")
}

// The documented container template must parse and validate (strict keys, container section).
func TestContainerTemplateIsValid(t *testing.T) {
	t.Setenv("MYRIARCH_ENROLLMENT_TOKEN", "myre_test")
	cfg, err := LoadConfig(filepath.Join("..", "..", "deploy", "updater", "container", "updater.yaml"))
	require.NoError(t, err)
	require.NoError(t, cfg.ValidateContainer())
	assert.Equal(t, "myre_test", cfg.Server.Token)
	assert.Equal(t, "newest", cfg.Container.SeedPolicy)
	assert.Equal(t, "symlink", cfg.Collector.Activation)
	assert.Equal(t, []string{"http://myriarch:8080/api/v1/artifacts/"}, cfg.Update.AllowedURLPrefixes)
}

func TestVerifyBundleRefusesAnUnanchoredIdentityPattern(t *testing.T) {
	_, err := runCLI(t, "verify-bundle", "--bundle", "x.sigstore.json", "--version", "v0.162.0", "--sha256", "00", "--identity-regexp", "opentelemetry-collector-releases")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anchored")
}
