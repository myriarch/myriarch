// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreflightErrorDoesNotForwardValidateOutput(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.cfg.Update.PreflightValidate = true
	e.rebuild()
	require.NoError(t, os.WriteFile(e.cfg.EffectiveConfigPath(), []byte("receivers: {}\n"), 0o600))
	e.inst.Exec = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "validate" {
			return []byte("resolved secret: hunter2"), assert.AnError
		}
		return osExecutor(ctx, env, name, args...)
	}
	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.ErrorContains(t, err, "rejects the current effective config")
	assert.NotContains(t, err.Error(), "hunter2", "validate output may contain resolved secrets and must stay local")
	assert.Equal(t, "0.162.0", e.active())
}

func TestPreflightPrivilegedHostValidatesBoundedCopy(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.cfg.Update.PreflightValidate = true
	e.rebuild()
	e.inst.privilegedHost = func() bool { return true }
	orig := "receivers: {}\nexporters: { otlp: { headers: { a: \"${env:TOKEN}\" } } }\n"
	require.NoError(t, os.WriteFile(e.cfg.EffectiveConfigPath(), []byte(orig), 0o600))

	var validated string
	var content []byte
	inner := e.inst.Exec
	e.inst.Exec = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "validate" {
			validated = args[2]
			content, _ = os.ReadFile(args[2])
		}
		return inner(ctx, env, name, args...)
	}
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0"))))
	require.NotEmpty(t, validated)
	assert.NotEqual(t, e.cfg.EffectiveConfigPath(), validated, "the supervisor-owned file is not validated in place")
	assert.Equal(t, orig, string(content))
	assert.NoFileExists(t, validated, "private copy is removed")
}

func TestPreflightPrivilegedHostSkipsConfigsWithForeignProviders(t *testing.T) {
	skipOnWindows(t)
	for name, cfg := range map[string]string{
		"file":  "exporters: { otlp: { headers: { a: \"${file:/etc/shadow}\" } } }\n",
		"http":  "x: ${http://example.invalid/a}\n",
		"https": "x: ${ https:example.invalid/a }\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "symlink")
			e.installOld("0.162.0")
			e.cfg.Update.PreflightValidate = true
			e.rebuild()
			e.inst.privilegedHost = func() bool { return true }
			require.NoError(t, os.WriteFile(e.cfg.EffectiveConfigPath(), []byte(cfg), 0o600))
			validateCalled := false
			inner := e.inst.Exec
			e.inst.Exec = func(ctx context.Context, env []string, n string, args ...string) ([]byte, error) {
				if len(args) > 0 && args[0] == "validate" {
					validateCalled = true
				}
				return inner(ctx, env, n, args...)
			}
			// validate would fail (exit 1): the install must still go through because it is skipped
			archive := makeTarGz(t, tarEntry{name: testBinary, body: fakeCollectorScript("0.163.0", 1)})
			require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", archive)))
			assert.False(t, validateCalled, "configs with providers other than env are never resolved with the updater's privileges")
		})
	}
}

func TestPreflightPrivilegedHostSkipsNonRegularEffectiveConfig(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.cfg.Update.PreflightValidate = true
	e.rebuild()
	e.inst.privilegedHost = func() bool { return true }
	target := filepath.Join(t.TempDir(), "elsewhere.yaml")
	require.NoError(t, os.WriteFile(target, []byte("x: 1\n"), 0o600))
	require.NoError(t, os.Symlink(target, e.cfg.EffectiveConfigPath()))
	validateCalled := false
	inner := e.inst.Exec
	e.inst.Exec = func(ctx context.Context, env []string, n string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "validate" {
			validateCalled = true
		}
		return inner(ctx, env, n, args...)
	}
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0"))))
	assert.False(t, validateCalled, "a symlinked effective.yaml is not followed")
}

func TestSystemdControllerReportsServiceUser(t *testing.T) {
	var got []string
	s := &SystemdController{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		got = args
		return []byte("otelcol\n"), nil
	}}
	u, err := s.ServiceUser(context.Background(), "otelcol-supervisor")
	require.NoError(t, err)
	assert.Equal(t, "otelcol", u)
	assert.Equal(t, []string{"show", "--property=User", "--value", "otelcol-supervisor.service"}, got)
	var _ ServiceUserResolver = s
}

func TestPreflightSkippedWhenServiceAccountUnknownAsRoot(t *testing.T) {
	// Without a resolver (and not able to prove the account) a root updater
	// does not validate; here the check is exercised through prepareValidation
	// with a controller that cannot report the account.
	if os.Geteuid() != 0 {
		t.Skip("only meaningful when running as root")
	}
	e := newEnv(t, "symlink")
	e.inst.Exec = nil
	v, err := e.inst.prepareValidation(context.Background(), "/nonexistent")
	require.NoError(t, err)
	assert.NotEmpty(t, v.skip)
}

// --- anti-downgrade ---------------------------------------------------------

func TestInstallRefusesDowngradeByDefault(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.163.0")
	archive := collectorArchive(t, "0.150.0")
	err := e.inst.Install(context.Background(), e.request("0.150.0", archive))
	require.ErrorContains(t, err, "downgrades are disabled")
	assert.Equal(t, "0.163.0", e.active())
	assert.Empty(t, e.svc.Calls(), "nothing is stopped or changed")
	assert.Zero(t, e.execCalls.Load(), "the offered binary never ran")
	e.noLeftovers()
}

func TestInstallAllowsOneStepRollbackToPreviousVersion(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0"))))
	assert.Equal(t, "0.163.0", e.active())
	m, err := e.layout.readMarker("0.163.0")
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", m.PreviousVersion)

	// roll back to the version that was active before: allowed
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.162.0", collectorArchive(t, "0.162.0"))))
	assert.Equal(t, "0.162.0", e.active())

	// but not any other older version
	e2 := newEnv(t, "symlink")
	e2.installOld("0.162.0")
	require.NoError(t, e2.inst.Install(context.Background(), e2.request("0.163.0", collectorArchive(t, "0.163.0"))))
	err = e2.inst.Install(context.Background(), e2.request("0.150.0", collectorArchive(t, "0.150.0")))
	require.ErrorContains(t, err, "downgrades are disabled")
}

func TestInstallAllowDowngradeFlagAndMinimumVersion(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.163.0")
	e.cfg.Update.AllowDowngrade = true
	e.rebuild()
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.150.0", collectorArchive(t, "0.150.0"))))
	assert.Equal(t, "0.150.0", e.active())

	// the floor applies even with allowDowngrade, and to the one-step rollback
	e.cfg.Update.MinimumVersion = "v0.160.0"
	e.rebuild()
	err := e.inst.Install(context.Background(), e.request("0.155.0", collectorArchive(t, "0.155.0")))
	require.ErrorContains(t, err, "below the host policy minimum")
	assert.Equal(t, "0.150.0", e.active())
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.160.1", collectorArchive(t, "0.160.1"))))
}

func TestInstallMinimumVersionAppliesToFreshHost(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.cfg.Update.MinimumVersion = "0.162.0"
	e.rebuild()
	err := e.inst.Install(context.Background(), e.request("0.161.9", collectorArchive(t, "0.161.9")))
	require.ErrorContains(t, err, "below the host policy minimum")
}

func TestVersionPolicyPrereleaseOrdering(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.163.0")
	// a pre-release of the active version is older
	err := e.inst.Install(context.Background(), e.request("0.163.0-rc.1", collectorArchive(t, "0.163.0-rc.1")))
	require.ErrorContains(t, err, "downgrades are disabled")
}

func TestConfigRejectsInvalidMinimumVersionAndInsecureTransport(t *testing.T) {
	base := func() Config {
		c := defaultConfigFor("linux")
		c.Server.Endpoint = "wss://fleet.example.com/v1/opamp"
		return c
	}
	c := base()
	c.Update.MinimumVersion = "latest"
	require.ErrorContains(t, c.Validate(), "update.minimumVersion")

	c = base()
	c.Update.MinimumVersion = "v0.162.0"
	require.NoError(t, c.Validate())

	c = base()
	c.Server.Endpoint = "ws://fleet.example.com/v1/opamp"
	require.ErrorContains(t, c.Validate(), "plain ws/http")
	c.Server.AllowInsecureTransport = true
	require.NoError(t, c.Validate())

	for _, ep := range []string{"ws://127.0.0.1:8080/v1/opamp", "http://localhost:8080/v1/opamp", "ws://[::1]:8080/v1/opamp"} {
		c = base()
		c.Server.Endpoint = ep
		require.NoError(t, c.Validate(), ep)
	}
}

// --- Windows copy activation: crash between the renames ---------------------

func TestRecoverRestoresMissingActiveBinaryAfterCrashBetweenRenames(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "copy")
	e.installOld("0.162.0")
	// simulate the crash: active renamed to .previous, .new staged, nothing activated
	active := filepath.Join(e.cfg.Collector.InstallDir, "bin", testBinary)
	require.NoError(t, os.WriteFile(active+".new", fakeCollectorScript("0.163.0", 0), 0o755))
	require.NoError(t, os.Rename(active, active+".previous"))
	require.NoError(t, writeJournal(e.cfg.StateDir, journal{From: "0.162.0", To: "0.163.0", Phase: "swapping", StartedAt: time.Now()}))
	e.healthy = func(string) bool {
		_, err := os.Stat(active)
		return err == nil
	}
	e.svc.SetState(testSupervisorSvc, ServiceStopped)

	require.NoError(t, e.inst.Recover(context.Background()))

	out, err := osExecutor(context.Background(), nil, active, "--version")
	require.NoError(t, err, "the collector binary is back")
	assert.Contains(t, string(out), "version 0.162.0")
	assert.Equal(t, "0.162.0", e.active())
	j, _ := readJournal(e.cfg.StateDir)
	assert.Nil(t, j)
	st, _ := e.svc.State(context.Background(), testSupervisorSvc)
	assert.Equal(t, ServiceRunning, st)
}

func TestRecoverKeepsJournalWhileHostIsUnhealthy(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.healthy = func(string) bool { return false }
	e.cfg.Update.HealthTimeout = Duration(100 * time.Millisecond)
	e.rebuild()
	require.NoError(t, writeJournal(e.cfg.StateDir, journal{From: "0.162.0", To: "0.163.0", Phase: "swapping", StartedAt: time.Now()}))
	require.Error(t, e.inst.Recover(context.Background()))
	j, _ := readJournal(e.cfg.StateDir)
	require.NotNil(t, j, "the next start tries again")
}

func TestActiveBinaryMismatchOnlyForCopyActivation(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	m, err := e.layout.ActiveBinaryMismatch()
	require.NoError(t, err)
	assert.False(t, m)

	c := newEnv(t, "copy")
	c.installOld("0.162.0")
	m, err = c.layout.ActiveBinaryMismatch()
	require.NoError(t, err)
	assert.False(t, m)
	require.NoError(t, os.WriteFile(filepath.Join(c.cfg.Collector.InstallDir, "bin", testBinary), []byte("tampered"), 0o755))
	m, err = c.layout.ActiveBinaryMismatch()
	require.NoError(t, err)
	assert.True(t, m)
}
