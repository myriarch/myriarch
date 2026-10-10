// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// containerHarness runs RunContainer in-process against a test OpAMP server
// and artifact server. The "supervisor" is a shell script that records its
// pid and stops on SIGTERM; collector health comes from a test HTTP server
// that depends on the active version (like in the service-mode tests).
type containerHarness struct {
	*env
	srv    *opampTestServer
	art    *artifactServer
	starts string
	health string // probe server address
	cancel context.CancelFunc
	done   chan error
}

func newContainerHarness(t *testing.T, imageVersion string, mod func(*Config)) *containerHarness {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	h := &containerHarness{env: e, srv: newOpampTestServer(t), art: newArtifactServer(t)}
	h.starts = filepath.Join(e.root, "starts")
	t.Setenv("STARTS", h.starts)

	supervisor := writeScript(t, e.root, "supervisor.sh", sleepyChild)
	image := filepath.Join(e.root, "image-otelcol-contrib")
	require.NoError(t, os.WriteFile(image, fakeCollectorScript(imageVersion, 0), 0o755))

	cfg := e.cfg
	cfg.Server.Endpoint = h.srv.url()
	cfg.Server.Token = "myre_testtoken"
	cfg.Update.AllowedURLPrefixes = []string{h.art.URL + "/api/v1/artifacts/"}
	cfg.Update.HealthTimeout = Duration(2 * time.Second)
	cfg.Supervisor.ServiceName = "opampsupervisor"
	cfg.Container = ContainerConfig{
		SupervisorCommand: []string{supervisor}, SeedBinary: image, SeedPolicy: SeedPolicyNewest,
		StopTimeout: Duration(3 * time.Second), RestartDelay: Duration(50 * time.Millisecond),
		MaxRestarts: 5, RestartWindow: Duration(time.Minute), HealthListen: "127.0.0.1:0",
	}
	if mod != nil {
		mod(&cfg)
	}
	h.cfg = cfg
	return h
}

func (h *containerHarness) run() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	addr := make(chan string, 1)
	reapAll := false
	go func() {
		h.done <- RunContainer(ctx, ContainerOptions{
			Config: h.cfg, Log: testLogger(h.t), ReapAll: &reapAll,
			Stdout: os.Stderr, Stderr: os.Stderr,
			Deps:        Deps{Verifier: h.verifier, Version: "9.9.9", CollectorUIDRefresh: 50 * time.Millisecond, HTTPClient: &http.Client{Timeout: 2 * time.Second}},
			OnListening: func(a string) { addr <- a },
		})
	}()
	select {
	case h.health = <-addr:
	case err := <-h.done:
		h.t.Fatalf("container mode ended early: %v", err)
	case <-time.After(20 * time.Second):
		h.t.Fatal("container mode did not start")
	}
	h.t.Cleanup(func() { _ = h.shutdown() })
	// the probe server starts before seeding and recovery: wait until the
	// child runs
	h.srv.eventually(func() bool { code, _ := h.get("/readyz"); return code == 200 }, "container ready after seeding and recovery")
}

func (h *containerHarness) shutdown() error {
	if h.cancel == nil {
		return nil
	}
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		return err
	case <-time.After(30 * time.Second):
		h.t.Error("container mode did not stop")
		return nil
	}
}

func (h *containerHarness) get(path string) (int, string) {
	h.t.Helper()
	resp, err := http.Get("http://" + h.health + path)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func (h *containerHarness) childStarts() int { return len(readPids(h.t, h.starts)) }

func (h *containerHarness) offer(version string) {
	archive := collectorArchive(h.t, version)
	url := h.art.add(version, archive)
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v"+version, archive)}, sha([]byte("offer-"+version)))
}

func TestContainerModeSeedsStartsAndUpdates(t *testing.T) {
	h := newContainerHarness(t, "0.161.0", nil)
	h.run()
	h.srv.waitConnected()

	// seeded from the image, child running, link attributes reported
	assert.Equal(t, "0.161.0", h.active())
	m, err := h.layout.readMarker("0.161.0")
	require.NoError(t, err)
	assert.Equal(t, SourceImage, m.Source)
	h.srv.eventually(func() bool { return h.childStarts() == 1 }, "child started")
	non := attrMap(h.srv.first().AgentDescription.NonIdentifyingAttributes)
	assert.Equal(t, "container", non["dev.myriarch.updater.install_method"])
	assert.Equal(t, "0.161.0", non["dev.myriarch.updater.image_collector_version"])
	assert.Equal(t, strings.TrimSuffix(h.srv.url(), "/v1/opamp"), non["dev.myriarch.updater.server_endpoint"])

	code, body := h.get("/healthz")
	assert.Equal(t, 200, code, body)
	code, body = h.get("/readyz")
	assert.Equal(t, 200, code, body)
	code, _ = h.get("/nope")
	assert.Equal(t, 404, code)
	resp, err := http.Post("http://"+h.health+"/healthz", "text/plain", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 405, resp.StatusCode)

	// update to 0.162.0: child is restarted on the new binary
	h.offer("0.162.0")
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)
	h.srv.eventually(func() bool { c := h.srv.updateComponent(); return c != nil && c.Status == "succeeded" }, "update reported")
	assert.Equal(t, "0.162.0", h.active())
	// the restarted child announces itself a moment later (loaded machines): wait, then check it is exactly one restart
	h.srv.eventually(func() bool { return h.childStarts() >= 2 }, "child restarted for the swap")
	assert.Equal(t, 2, h.childStarts(), "the supervisor child was restarted exactly once for the swap")
	assert.Equal(t, "v0.162.0", h.srv.updateComponent().ComponentHealthMap["version"].Status)
	assert.Equal(t, "v0.161.0", h.srv.updateComponent().ComponentHealthMap["from"].Status)
	m, err = h.layout.readMarker("0.162.0")
	require.NoError(t, err)
	assert.Equal(t, SourcePackage, m.Source)
	assert.Equal(t, "0.161.0", m.PreviousVersion)
	h.noLeftovers()

	// shutdown (SIGTERM): child stopped, clean exit
	childPids := readPids(t, h.starts)
	require.NoError(t, h.shutdown())
	for _, p := range childPids {
		assert.False(t, pidAlive(p), "child %d is gone", p)
	}
}

func TestContainerModeRollsBackAFailingUpdateAndKeepsTheChildRunning(t *testing.T) {
	h := newContainerHarness(t, "0.161.0", func(c *Config) { c.Update.HealthTimeout = Duration(500 * time.Millisecond) })
	h.healthy = func(active string) bool { return active != "0.162.0" }
	h.run()
	h.srv.waitConnected()
	h.srv.eventually(func() bool { return h.childStarts() == 1 }, "child started")

	h.offer("0.162.0")
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	h.srv.eventually(func() bool { c := h.srv.updateComponent(); return c != nil && c.Status == "rolled_back" }, "rolled_back reported")

	assert.Equal(t, "0.161.0", h.active(), "previous version is active again")
	h.srv.eventually(func() bool { return h.childStarts() >= 3 }, "child restarted after the rollback")
	assert.Equal(t, 3, h.childStarts(), "start + swap restart + rollback restart")
	assert.Eventually(t, func() bool { code, _ := h.get("/readyz"); return code == 200 }, 5*time.Second, 20*time.Millisecond, "the child runs after the rollback")
	c := h.srv.updateComponent()
	assert.Equal(t, "failed", c.ComponentHealthMap["step:health_check"].Status)
	assert.Equal(t, "done", c.ComponentHealthMap["step:rolling_back"].Status)
	code, _ := h.get("/readyz")
	assert.Equal(t, 200, code)
}

func TestContainerModeKeepsTheUpdatedVersionAfterARestart(t *testing.T) {
	h := newContainerHarness(t, "0.161.0", nil)
	h.run()
	h.srv.waitConnected()
	h.offer("0.162.0")
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)
	require.NoError(t, h.shutdown())

	// "pod restart": same volume, same image (0.161.0) -> the volume wins
	h.srv.mu.Lock()
	h.srv.msgs = nil
	h.srv.mu.Unlock()
	h.run()
	h.srv.waitConnected()
	assert.Equal(t, "0.162.0", h.active())
	non := attrMap(h.srv.first().AgentDescription.NonIdentifyingAttributes)
	assert.Equal(t, "0.161.0", non["dev.myriarch.updater.image_collector_version"], "the image version is still reported")

	// an image bump above the volume wins
	require.NoError(t, h.shutdown())
	require.NoError(t, os.WriteFile(h.cfg.Container.SeedBinary, fakeCollectorScript("0.163.0", 0), 0o755))
	h.run()
	assert.Equal(t, "0.163.0", h.active())
	assert.Equal(t, "0.162.0", h.layout.previousOf("0.163.0"))
}

func TestContainerModeExitsWhenTheChildKeepsCrashing(t *testing.T) {
	h := newContainerHarness(t, "0.161.0", func(c *Config) {
		c.Container.MaxRestarts = 2
		c.Container.RestartDelay = Duration(10 * time.Millisecond)
	})
	crasher := writeScript(t, h.root, "crash.sh", "echo $$ >> \"$STARTS\"\nexit 1\n")
	h.cfg.Container.SupervisorCommand = []string{crasher}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reapAll := false
	done := make(chan error, 1)
	go func() {
		done <- RunContainer(ctx, ContainerOptions{Config: h.cfg, Log: testLogger(t), ReapAll: &reapAll, Stdout: os.Stderr, Stderr: os.Stderr,
			Deps: Deps{Verifier: h.verifier, Version: "9.9.9"}})
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ended unexpectedly")
	case <-time.After(30 * time.Second):
		t.Fatal("the container did not exit")
	}
}

func TestContainerModeReadyzReflectsAnUpdateInProgress(t *testing.T) {
	h := newContainerHarness(t, "0.161.0", func(c *Config) { c.Update.HealthTimeout = Duration(3 * time.Second) })
	h.healthy = func(active string) bool { return active != "0.162.0" } // the health check keeps waiting
	h.run()
	h.srv.waitConnected()
	h.offer("0.162.0")
	h.srv.eventually(func() bool {
		code, body := h.get("/readyz")
		return code == 503 && strings.Contains(body, "update in progress")
	}, "not ready while the new collector is being health-checked")
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	h.srv.eventually(func() bool { code, _ := h.get("/readyz"); return code == 200 }, "ready again after the rollback")
}

func TestContainerModeRejectsInvalidContainerConfig(t *testing.T) {
	cfg := defaultConfigFor("linux")
	cfg.Server.Endpoint = "ws://127.0.0.1:1/v1/opamp"
	err := RunContainer(context.Background(), ContainerOptions{Config: cfg})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "container.supervisorCommand is required")
	assert.Contains(t, err.Error(), "container.seedBinary is required")
}

func TestContainerStatusLiveness(t *testing.T) {
	now := time.Now()
	s := &containerStatus{childRunning: func() bool { return true }, updateBusy: func() bool { return false }, now: func() time.Time { return now }}
	assert.False(t, s.alive(), "no heartbeat yet")
	s.heartbeat()
	assert.True(t, s.alive())
	now = now.Add(31 * time.Second)
	assert.False(t, s.alive(), "heartbeat too old: hung main loop")
}

// An image bump must not override an interrupted update: recovery decides
// first, then the image version is evaluated against the recovered state.
func TestContainerModeRecoversBeforeSeedingTheImage(t *testing.T) {
	h := newContainerHarness(t, "0.163.0", nil)
	h.installOld("0.161.0")
	h.installOld("0.162.0") // activated before the pod was killed during its health check
	require.NoError(t, writeJournal(h.cfg.StateDir, journal{From: "0.161.0", To: "0.162.0", Phase: "verifying", StartedAt: time.Now()}))
	h.run()
	h.srv.waitConnected()
	// 0.162.0 is healthy and accepted by the recovery, the newer image wins afterwards
	assert.Equal(t, "0.163.0", h.active())
	assert.Equal(t, "0.162.0", h.layout.previousOf("0.163.0"))
	j, _ := readJournal(h.cfg.StateDir)
	assert.Nil(t, j)
	h.srv.eventually(func() bool { code, _ := h.get("/readyz"); return code == 200 }, "ready")
}

func TestContainerModeRollsBackAnUnhealthyInterruptedUpdate(t *testing.T) {
	h := newContainerHarness(t, "0.161.0", func(c *Config) { c.Update.HealthTimeout = Duration(500 * time.Millisecond) })
	h.healthy = func(active string) bool { return active != "0.162.0" }
	h.installOld("0.161.0")
	h.installOld("0.162.0")
	require.NoError(t, writeJournal(h.cfg.StateDir, journal{From: "0.161.0", To: "0.162.0", Phase: "verifying", StartedAt: time.Now()}))
	h.run()
	h.srv.waitConnected()
	assert.Equal(t, "0.161.0", h.active())
	j, _ := readJournal(h.cfg.StateDir)
	assert.Nil(t, j)
}

// Liveness comes from the main loop of the updater (Deps.Heartbeat).
func TestUpdaterMainLoopReportsHeartbeats(t *testing.T) {
	skipOnWindows(t)
	h := newHarness(t)
	var beats atomic.Int32
	u, err := New(h.cfg, testLogger(t), Deps{
		Services: h.svc, Verifier: h.verifier, Version: "9.9.9", CollectorUIDRefresh: 50 * time.Millisecond,
		HTTPClient: &http.Client{Timeout: 2 * time.Second}, Heartbeat: func() { beats.Add(1) },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = u.Run(ctx) }()
	h.srv.eventually(func() bool { return beats.Load() >= 1 }, "heartbeat from the main loop")
	cancel()
	<-done
}
