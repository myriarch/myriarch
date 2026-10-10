// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallSuccess(t *testing.T) {
	skipOnWindows(t)
	for _, activation := range []string{"symlink", "copy"} {
		t.Run(activation, func(t *testing.T) {
			e := newEnv(t, activation)
			e.installOld("0.162.0")
			archive := collectorArchive(t, "0.163.0")
			req := e.request("0.163.0", archive)
			req.Signature = []byte(`{"bundle":true}`)

			require.NoError(t, e.inst.Install(context.Background(), req))

			assert.Equal(t, "0.163.0", e.active())
			assert.Equal(t, []string{"stop:" + testSupervisorSvc, "start:" + testSupervisorSvc}, e.svc.Calls())
			assert.Equal(t, 1, e.verifier.calls)
			assert.Equal(t, "0.163.0", e.verifier.lastVer)
			assert.Equal(t, sha(archive), e.verifier.lastDig)
			assert.JSONEq(t, `{"bundle":true}`, string(e.verifier.lastSig))
			// old version stays for rollback; marker proves what was verified
			assert.FileExists(t, e.layout.BinaryPath("0.162.0"))
			assert.Equal(t, sha(archive), e.layout.ActiveContentHash())
			j, err := readJournal(e.cfg.StateDir)
			require.NoError(t, err)
			assert.Nil(t, j, "journal must be cleared")
			e.noLeftovers()
			if activation == "copy" {
				assert.FileExists(t, filepath.Join(e.cfg.Collector.InstallDir, "bin", testBinary+".previous"))
			}
		})
	}
}

func TestInstallKeepsOnlyKeepVersions(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.cfg.Collector.KeepVersions = 2
	e.rebuild()
	e.installOld("0.160.0")
	for i, v := range []string{"0.161.0", "0.162.0", "0.163.0"} {
		time.Sleep(15 * time.Millisecond) // distinct install timestamps
		a := collectorArchive(t, v)
		require.NoError(t, e.inst.Install(context.Background(), e.request(v, a)), i)
	}
	vs, err := e.layout.ListVersions()
	require.NoError(t, err)
	var names []string
	for _, v := range vs {
		names = append(names, v.Version)
	}
	assert.ElementsMatch(t, []string{"0.163.0", "0.162.0"}, names)
	assert.Equal(t, "0.163.0", e.active())
}

func TestInstallHealthFailureRollsBack(t *testing.T) {
	skipOnWindows(t)
	for _, activation := range []string{"symlink", "copy"} {
		t.Run(activation, func(t *testing.T) {
			e := newEnv(t, activation)
			e.installOld("0.162.0")
			e.healthy = func(active string) bool { return active != "0.163.0" }
			e.cfg.Update.HealthTimeout = Duration(300 * time.Millisecond)
			e.rebuild()

			err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "update to v0.163.0 failed")
			assert.Contains(t, err.Error(), "rolled back to v0.162.0")

			assert.Equal(t, "0.162.0", e.active(), "previous version must be active again")
			assert.Equal(t, []string{
				"stop:" + testSupervisorSvc, "start:" + testSupervisorSvc, // swap
				"stop:" + testSupervisorSvc, "start:" + testSupervisorSvc, // rollback
			}, e.svc.Calls())
			assert.NoDirExists(t, e.layout.VersionDir("0.163.0"), "failed version removed")
			j, _ := readJournal(e.cfg.StateDir)
			assert.Nil(t, j)
			e.noLeftovers()
		})
	}
}

func TestInstallRollbackAlsoFails(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.healthy = func(string) bool { return false } // nothing ever becomes healthy
	e.cfg.Update.HealthTimeout = Duration(150 * time.Millisecond)
	e.rebuild()

	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update to v0.163.0 failed")
	assert.Contains(t, err.Error(), "rollback to v0.162.0 also failed")
	assert.Equal(t, "0.162.0", e.active())
	// journal is kept so that the next start retries the recovery
	j, _ := readJournal(e.cfg.StateDir)
	require.NotNil(t, j)
	assert.Equal(t, "rollingBack", j.Phase)
}

func TestInstallNoPreviousVersionCannotRollBack(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.healthy = func(string) bool { return false }
	e.cfg.Update.HealthTimeout = Duration(100 * time.Millisecond)
	e.rebuild()
	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no previous version to roll back to")
}

func TestInstallWrongHashDoesNotTouchHost(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	req := e.request("0.163.0", collectorArchive(t, "0.163.0"))
	req.ContentHash = sha([]byte("something else"))

	err := e.inst.Install(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHA-256 mismatch")
	assert.Empty(t, e.svc.Calls(), "service must not be touched")
	assert.Equal(t, "0.162.0", e.active())
	assert.Zero(t, e.verifier.calls, "signature is not even looked at before the hash matches")
	assert.NoDirExists(t, e.layout.VersionDir("0.163.0"))
	e.noLeftovers()
}

func TestInstallMissingContentHash(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	req := e.request("0.163.0", collectorArchive(t, "0.163.0"))
	req.ContentHash = nil
	err := e.inst.Install(context.Background(), req)
	require.ErrorContains(t, err, "no valid SHA-256")
	assert.Empty(t, e.svc.Calls())
}

func TestInstallSignatureFailureDoesNotTouchHost(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.verifier.err = errors.New("bad identity")
	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.ErrorContains(t, err, "signature verification: bad identity")
	assert.Empty(t, e.svc.Calls())
	assert.Equal(t, "0.162.0", e.active())
	assert.Zero(t, e.execCalls.Load(), "the downloaded binary must not run before it is authenticated")
	e.noLeftovers()
}

func TestInstallPreflightWrongVersionDoesNotTouchHost(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	// archive claims to be 0.163.0 but the binary reports 0.150.0
	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.150.0")))
	require.ErrorContains(t, err, "preflight")
	require.ErrorContains(t, err, "expected version 0.163.0")
	assert.Empty(t, e.svc.Calls())
	assert.Equal(t, "0.162.0", e.active())
	e.noLeftovers()
}

func TestInstallPreflightPrefixVersionIsNotAccepted(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.15.0")
	// 0.16.0 is a prefix of 0.162.0 as a string
	err := e.inst.Install(context.Background(), e.request("0.16.0", collectorArchive(t, "0.162.0")))
	require.ErrorContains(t, err, "preflight")
}

func TestInstallPreflightValidateUsesServiceEnvAndEffectiveConfig(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.cfg.Update.PreflightValidate = true
	e.rebuild()
	require.NoError(t, os.WriteFile(e.cfg.EffectiveConfigPath(), []byte("receivers: {}\n"), 0o600))
	e.svc.SetEnvironment(testSupervisorSvc, []string{"OTEL_GATEWAY_TOKEN=x"})

	var gotEnv []string
	var gotArgs []string
	inner := e.inst.Exec
	e.inst.Exec = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "validate" {
			gotEnv, gotArgs = env, args
		}
		return inner(ctx, env, name, args...)
	}
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0"))))
	assert.Equal(t, []string{"validate", "--config", e.cfg.EffectiveConfigPath()}, gotArgs)
	assert.Equal(t, []string{"OTEL_GATEWAY_TOKEN=x"}, gotEnv)
}

func TestInstallPreflightValidateFailureAborts(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.cfg.Update.PreflightValidate = true
	e.rebuild()
	require.NoError(t, os.WriteFile(e.cfg.EffectiveConfigPath(), []byte("x: 1\n"), 0o600))
	archive := makeTarGz(t, tarEntry{name: testBinary, body: fakeCollectorScript("0.163.0", 1)})
	err := e.inst.Install(context.Background(), e.request("0.163.0", archive))
	require.ErrorContains(t, err, "rejects the current effective config")
	assert.Empty(t, e.svc.Calls())
	assert.Equal(t, "0.162.0", e.active())
}

func TestInstallPreflightValidateSkippedWithoutEffectiveConfig(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.cfg.Update.PreflightValidate = true
	e.rebuild()
	archive := makeTarGz(t, tarEntry{name: testBinary, body: fakeCollectorScript("0.163.0", 1)}) // validate would fail
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", archive)))
}

func TestInstallStopFailureChangesNothing(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	stopFail := true
	e.svc.OnStop = func(string) error {
		if stopFail {
			stopFail = false
			return errors.New("access denied")
		}
		return nil
	}
	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.ErrorContains(t, err, "nothing was changed")
	assert.Equal(t, "0.162.0", e.active())
	assert.Equal(t, []string{"stop:" + testSupervisorSvc, "start:" + testSupervisorSvc}, e.svc.Calls(), "service is started again")
}

func TestInstallAlreadyActiveIdenticalBinaryIsNoop(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	archive := collectorArchive(t, "0.162.0")
	// installed by the install script: no marker
	e.installOld("0.162.0")
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.162.0", archive)))
	assert.Empty(t, e.svc.Calls(), "no restart for an identical, already active binary")
	assert.Equal(t, sha(archive), e.layout.ActiveContentHash(), "marker written so later offers are skipped")
}

func TestInstallAlreadyActiveDifferentBinaryRefused(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	require.NoError(t, os.WriteFile(e.layout.BinaryPath("0.162.0"), append(fakeCollectorScript("0.162.0", 0), '#'), 0o755))
	err := e.inst.Install(context.Background(), e.request("0.162.0", collectorArchive(t, "0.162.0")))
	require.ErrorContains(t, err, "already active but its binary differs")
	assert.Empty(t, e.svc.Calls())
}

func TestInstallDownloadLimit(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.cfg.Update.MaxDownloadBytes = 100
	e.rebuild()
	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.ErrorContains(t, err, "size limit")
	assert.Empty(t, e.svc.Calls())
	e.noLeftovers()
}

func TestInstallInvalidVersionRejected(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	for _, v := range []string{"", "latest", "../../etc", "v0.163.0/../x", "0.163", "v0.163.0;rm"} {
		req := e.request("0.163.0", collectorArchive(t, "0.163.0"))
		req.Version = v
		require.Error(t, e.inst.Install(context.Background(), req), v)
	}
	assert.Empty(t, e.svc.Calls())
}

func TestInstallCanceledBeforeSwapLeavesHostUntouched(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.inst.Install(ctx, e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.Error(t, err)
	assert.Empty(t, e.svc.Calls())
	e.noLeftovers()
}

func TestInstallCancelDuringHealthWaitStillCompletes(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	ctx, cancel := context.WithCancel(context.Background())
	e.svc.OnStart = func(string) error { cancel(); return nil } // shutdown request mid-swap
	require.NoError(t, e.inst.Install(ctx, e.request("0.163.0", collectorArchive(t, "0.163.0"))))
	assert.Equal(t, "0.163.0", e.active())
}

func TestInstallUnknownSupervisorService(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.svc = NewFakeServiceController() // service does not exist
	e.rebuild()
	err := e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0")))
	require.ErrorContains(t, err, "does not exist")
}

func TestInstallEmptyDownload(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	req := e.request("0.163.0", nil)
	req.Data = bytes.NewReader(nil)
	require.ErrorContains(t, e.inst.Install(context.Background(), req), "empty")
}

// --- recovery -----------------------------------------------------------

func TestRecoverAcceptsHealthyNewVersion(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.installOld("0.163.0") // activated before the crash
	require.NoError(t, writeJournal(e.cfg.StateDir, journal{From: "0.162.0", To: "0.163.0", Phase: "verifying", StartedAt: time.Now()}))
	require.NoError(t, e.inst.Recover(context.Background()))
	assert.Equal(t, "0.163.0", e.active())
	j, _ := readJournal(e.cfg.StateDir)
	assert.Nil(t, j)
}

func TestRecoverRollsBackUnhealthyNewVersion(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.installOld("0.163.0")
	e.healthy = func(active string) bool { return active == "0.162.0" }
	e.cfg.Update.HealthTimeout = Duration(200 * time.Millisecond)
	e.rebuild()
	e.svc.SetState(testSupervisorSvc, ServiceStopped) // died while stopped
	require.NoError(t, writeJournal(e.cfg.StateDir, journal{From: "0.162.0", To: "0.163.0", Phase: "swapping", StartedAt: time.Now()}))
	require.NoError(t, e.inst.Recover(context.Background()))
	assert.Equal(t, "0.162.0", e.active())
	st, _ := e.svc.State(context.Background(), testSupervisorSvc)
	assert.Equal(t, ServiceRunning, st)
	j, _ := readJournal(e.cfg.StateDir)
	assert.Nil(t, j)
}

func TestRecoverSwapNeverHappenedRestartsService(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.svc.SetState(testSupervisorSvc, ServiceStopped) // crashed right after the stop
	require.NoError(t, writeJournal(e.cfg.StateDir, journal{From: "0.162.0", To: "0.163.0", Phase: "swapping", StartedAt: time.Now()}))
	require.NoError(t, e.inst.Recover(context.Background()))
	assert.Equal(t, "0.162.0", e.active())
	st, _ := e.svc.State(context.Background(), testSupervisorSvc)
	assert.Equal(t, ServiceRunning, st)
}

func TestRecoverCorruptJournalIsDiscarded(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	require.NoError(t, os.WriteFile(journalPath(e.cfg.StateDir), []byte("{not json"), 0o600))
	require.NoError(t, e.inst.Recover(context.Background()))
	assert.NoFileExists(t, journalPath(e.cfg.StateDir))
}

func TestHealthCheckerRequiresConsecutiveSuccesses(t *testing.T) {
	svc := NewFakeServiceController(testSupervisorSvc)
	h := &HealthChecker{Services: svc, ServiceName: testSupervisorSvc, Interval: 5 * time.Millisecond, Successes: 3, Timeout: time.Second}
	flap := 0
	svc.OnStart = nil
	go func() {
		// flap the service state: running, stopped, running ... only a stable
		// run of 3 probes may pass
		for i := 0; i < 4; i++ {
			time.Sleep(7 * time.Millisecond)
			flap++
			svc.SetState(testSupervisorSvc, ServiceStopped)
			time.Sleep(7 * time.Millisecond)
			svc.SetState(testSupervisorSvc, ServiceRunning)
		}
	}()
	require.NoError(t, h.WaitHealthy(context.Background()))
}

func TestHealthCheckerTimeout(t *testing.T) {
	svc := NewFakeServiceController(testSupervisorSvc)
	svc.SetState(testSupervisorSvc, ServiceStopped)
	h := &HealthChecker{Services: svc, ServiceName: testSupervisorSvc, Interval: 5 * time.Millisecond, Successes: 2, Timeout: 60 * time.Millisecond}
	err := h.WaitHealthy(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "is stopped"), err.Error())
}

// With the real Sigstore verifier a bundle for another artifact must stop the
// update before anything on the host is touched.
func TestInstallRealSigstoreVerifierRejectsForeignBundle(t *testing.T) {
	skipOnWindows(t)
	f := loadSigFixture(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.inst.Verifier = &SigstoreVerifier{
		TrustedRootFile: f.trustedRoot, CertificateIdentityRegexp: DefaultCertificateIdentityRegexp,
		CertificateOIDCIssuer: DefaultCertificateOIDCIssuer, RequireSignature: true,
	}
	req := e.request("0.162.1", collectorArchive(t, "0.162.1"))
	req.Signature = f.bundle // genuine upstream bundle, but for the v0.162.0 archive
	err := e.inst.Install(context.Background(), req)
	require.ErrorContains(t, err, "signature verification")
	assert.Empty(t, e.svc.Calls())
	assert.Equal(t, "0.162.0", e.active())
	assert.Zero(t, e.execCalls.Load(), "downloaded binary never ran")
	e.noLeftovers()

	req = e.request("0.162.1", collectorArchive(t, "0.162.1"))
	req.Signature = nil
	err = e.inst.Install(context.Background(), req)
	require.ErrorContains(t, err, "signature required")
	assert.Empty(t, e.svc.Calls())
}

// A cancelled recovery (SIGTERM during startup) must not be mistaken for an
// unhealthy new version: nothing is rolled back and the journal stays.
func TestRecoverWithCancelledContextDoesNotRollBackAHealthyVersion(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	e.installOld("0.163.0")
	require.NoError(t, writeJournal(e.cfg.StateDir, journal{From: "0.162.0", To: "0.163.0", Phase: "verifying", StartedAt: time.Now()}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, e.inst.Recover(ctx), context.Canceled)
	assert.Equal(t, "0.163.0", e.active())
	j, _ := readJournal(e.cfg.StateDir)
	require.NotNil(t, j, "the journal is kept for the next start")

	require.NoError(t, e.inst.Recover(context.Background()))
	assert.Equal(t, "0.163.0", e.active())
}
