// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fastProfileYAML = `
seed: 7
components: ../../examples/sim/components-otelcol-contrib-0.162.0.json
groups:
  - name: windows-edge
    weight: 3
    os: windows
    arch: amd64
    version: 0.161.0
    updater: true
    hostnamePattern: "WIN-EDGE-%02d"
    siteIdPattern: "edge-%03d"
    attributes: {site.type: edge, site.country: NL}
    fixedSiteIds: ["edge-007"]
  - name: k8s-gateway
    weight: 1
    os: linux
    arch: amd64
    version: 0.162.0
    updater: false
    hostnamePattern: "otel-gateway-%d"
    siteIdPattern: "cloud-01"
    attributes: {site.type: cloud}
behavior:
  connectRate: 500
  heartbeatInterval: 30s
  configApplyDelay: 10ms-40ms
  restartDuration: 60ms
  updaterInstallDuration: 50ms-100ms
  failConfigsContaining: simulate-config-failure
`

func fastProfile(t testing.TB, mutate func(*Profile)) *Profile {
	t.Helper()
	p, err := ParseProfile([]byte(fastProfileYAML))
	require.NoError(t, err)
	p.baseDir = "." // components path is relative to the test dir via ../../
	if mutate != nil {
		mutate(p)
	}
	return p
}

type runningSim struct {
	*Simulator
	cancel context.CancelFunc
	done   chan struct{}
}

func startSim(t testing.TB, f *fakeMyriarchd, agents int, p *Profile, mutate func(*Options)) *runningSim {
	t.Helper()
	opts := Options{
		Server: f.url(), Token: "myre_simtest", Agents: agents, Updaters: true, Profile: p, FakeDownloads: true,
		Logger: slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})), SummaryInterval: time.Hour,
	}
	if mutate != nil {
		mutate(&opts)
	}
	s, err := New(opts)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningSim{Simulator: s, cancel: cancel, done: make(chan struct{})}
	go func() { defer close(r.done); _ = s.Run(ctx) }()
	t.Cleanup(r.stop)
	return r
}

func (r *runningSim) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(60 * time.Second):
	}
}

func waitAllConnected(t testing.TB, r *runningSim, d time.Duration) {
	t.Helper()
	eventually(t, d, func() bool {
		st := r.Stats()
		return st.ConnectedCollectors == st.Collectors && st.ConnectedUpdaters == st.Updaters
	}, "all agents connect: %+v", r.Stats())
}

func TestSimConnectsAgentsWithRealisticDescriptions(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 8, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	st := r.Stats()
	assert.Equal(t, 8, st.Collectors)
	assert.Equal(t, 6, st.Updaters, "windows-edge group (6 of 8) has updaters")

	eventually(t, 10*time.Second, func() bool {
		return f.connected("otelcol-contrib") == 8 && f.connected("myriarch-updater") == 6
	}, "server sees all connections")

	for _, c := range r.Collectors() {
		a := f.get(c.uid)
		require.NotNil(t, a, c.Hostname())
		assert.Equal(t, uint64(collectorCaps), a.caps)
		assert.Equal(t, uint64(31751), a.caps, "same bitmask as the real supervisor (docs/design/upstream-notes.md, Identity and reported attributes)")
		assert.Equal(t, "Bearer myre_simtest", a.auth)
		assert.Equal(t, "otelcol-contrib", a.ident["service.name"])
		assert.Equal(t, c.Version(), a.ident["service.version"])
		assert.Equal(t, c.uid.String(), a.ident["service.instance.id"])
		assert.Equal(t, c.spec.Hostname, a.nonIdent["host.name"])
		assert.Equal(t, c.spec.OS, a.nonIdent["os.type"])
		assert.Equal(t, c.spec.Arch, a.nonIdent["host.arch"])
		assert.NotEmpty(t, a.nonIdent["os.description"])
		assert.Equal(t, c.spec.SiteID, a.nonIdent["site.id"])
		assert.Equal(t, c.spec.Attributes["site.type"], a.nonIdent["site.type"])
		require.NotNil(t, a.health)
		assert.True(t, a.health.Healthy)
		require.NotNil(t, a.components, "available components reported")
		assert.Len(t, a.components.Hash, 32)

		if c.updater != nil {
			u := f.get(c.updater.uid)
			require.NotNil(t, u, "updater of "+c.Hostname())
			assert.Equal(t, uint64(updaterCaps), u.caps)
			assert.Equal(t, "myriarch-updater", u.ident["service.name"])
			assert.Equal(t, c.uid.String(), u.nonIdent["dev.myriarch.collector.instance_uid"], "linking attribute")
			assert.Equal(t, c.spec.Hostname, u.nonIdent["host.name"])
		}
	}
	assert.Equal(t, "edge-007", f.get(r.Collectors()[0].uid).nonIdent["site.id"], "fixed site id")
	assert.Equal(t, 14.0, metricValue(r.metrics.agentsConnected))
}

func TestSimAppliesRemoteConfig(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 12, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)

	body := "receivers:\n  otlp_grpc: {}\nexporters:\n  debug: {}\n"
	hash := []byte("hash-config-1")
	f.pushConfig(body, hash)

	eventually(t, 20*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool {
			return a.ident["service.name"] == "otelcol-contrib" && a.rcStatus != nil &&
				a.rcStatus.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED && bytes.Equal(a.rcStatus.LastRemoteConfigHash, hash)
		}) == 12
	}, "all collectors report APPLIED for the hash")

	for _, c := range r.Collectors() {
		a := f.get(c.uid)
		// APPLYING comes first, then APPLIED (docs/design/upstream-notes.md, Remote configuration handling)
		var log []protobufs.RemoteConfigStatuses
		f.with(c.uid, func(a *fakeAgent) { log = append(log, a.rcLog...) })
		require.NotEmpty(t, log)
		// the first message carries the initial UNSET status
		assert.Equal(t, []protobufs.RemoteConfigStatuses{
			protobufs.RemoteConfigStatuses_RemoteConfigStatuses_UNSET,
			protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING,
			protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		}, log, c.Hostname())
		f.with(c.uid, func(a *fakeAgent) { assert.Equal(t, body, string(a.effective), "effective config = received body") })
		_ = a
	}
	assert.Equal(t, int64(12), r.Stats().ConfigsApplied)
	assert.Equal(t, 12.0, metricValue(r.metrics.configsApplied))

	// the same config again is a no-op (no extra statuses, no counter change)
	f.pushConfig(body, hash)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(12), r.Stats().ConfigsApplied)
}

func TestSimFailedConfigIsRolledBackAutomatically(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 4, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)

	good, goodHash := "receivers:\n  otlp_grpc: {}\n", []byte("good")
	f.pushConfig(good, goodHash)
	waitAllReportedApplied(t, f, r, goodHash)

	bad, badHash := "receivers:\n  simulate-config-failure: {}\n", []byte("bad")
	f.pushConfig(bad, badHash)
	eventually(t, 10*time.Second, func() bool { return r.Stats().ConfigsFailed == 4 }, "bad config fails everywhere")

	// FAILED for the new hash, then APPLIED for the OLD hash (automatic_config_rollback)
	eventually(t, 10*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool {
			return a.ident["service.name"] == "otelcol-contrib" && a.rcStatus != nil &&
				a.rcStatus.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED && bytes.Equal(a.rcStatus.LastRemoteConfigHash, goodHash)
		}) == 4
	}, "agents report APPLIED for the previous hash after the failure")
	for _, c := range r.Collectors() {
		f.with(c.uid, func(a *fakeAgent) {
			require.GreaterOrEqual(t, len(a.rcLog), 5)
			n := len(a.rcLog)
			assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, a.rcLog[n-2])
			assert.Equal(t, badHash, a.rcHashLog[n-2])
			assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, a.rcLog[n-1])
			assert.Equal(t, goodHash, a.rcHashLog[n-1])
			assert.Equal(t, good, string(a.effective), "the failed config never became effective")
		})
		st := c.State()
		assert.Contains(t, st.LastConfigError, "simulate-config-failure")
	}
}

func TestSimFirstConfigFailureHasNoRollbackStatus(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	f.pushConfig("x: simulate-config-failure\n", []byte("bad"))
	eventually(t, 10*time.Second, func() bool { return r.Stats().ConfigsFailed == 2 }, "failed")
	time.Sleep(400 * time.Millisecond)
	for _, c := range r.Collectors() {
		f.with(c.uid, func(a *fakeAgent) {
			assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, a.rcLog[len(a.rcLog)-1])
		})
	}
}

func TestSimRandomConfigFailureRate(t *testing.T) {
	f := newFakeMyriarchd(t)
	p := fastProfile(t, func(p *Profile) { p.Behavior.ConfigFailureRate = 1 })
	r := startSim(t, f, 3, p, nil)
	waitAllConnected(t, r, 20*time.Second)
	f.pushConfig("a: b\n", []byte("h1"))
	eventually(t, 10*time.Second, func() bool { return r.Stats().ConfigsFailed == 3 }, "all fail")
	assert.Equal(t, int64(0), r.Stats().ConfigsApplied)
}

func TestSimUnhealthyAfterApply(t *testing.T) {
	f := newFakeMyriarchd(t)
	p := fastProfile(t, func(p *Profile) { p.Behavior.UnhealthyAfterApplyRate = 1 })
	r := startSim(t, f, 2, p, nil)
	waitAllConnected(t, r, 20*time.Second)
	f.pushConfig("a: b\n", []byte("h1"))
	eventually(t, 10*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool {
			return a.ident["service.name"] == "otelcol-contrib" && a.health != nil && !a.health.Healthy && a.health.LastError != ""
		}) == 2
	}, "collectors report unhealthy after apply")
}

func TestSimRestartCommand(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	c := r.Collectors()[0]
	var before uint64
	f.with(c.uid, func(a *fakeAgent) { before = a.health.StartTimeUnixNano })

	f.sendTo(c.uid, &protobufs.ServerToAgent{Command: &protobufs.ServerToAgentCommand{Type: protobufs.CommandType_CommandType_Restart}})
	eventually(t, 10*time.Second, func() bool {
		ok := false
		f.with(c.uid, func(a *fakeAgent) {
			ok = a.health != nil && a.health.Healthy && a.health.Status == "StatusOK" && a.health.StartTimeUnixNano > before
			hasStopping := false
			for _, s := range a.healthLog {
				hasStopping = hasStopping || s == "StatusStopping"
			}
			ok = ok && hasStopping
		})
		return ok
	}, "unhealthy (stopping) during restartDuration, then healthy with a new start time")
	assert.Equal(t, 1, c.State().Restarts)
	assert.GreaterOrEqual(t, metricValue(r.metrics.restarts), 1.0)
}

func TestSimControlAPIUpgradeChangesReportedVersion(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 8, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	srv := httptest.NewServer(r.ControlHandler())
	defer srv.Close()

	gw := r.Collectors()[len(r.Collectors())-1]
	require.Equal(t, "k8s-gateway", gw.spec.Group)
	post := func(body string) (*http.Response, string) {
		resp, err := http.Post(srv.URL+"/control/upgrade", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, out := post(`{"hostnames": ["` + strings.ToUpper(gw.Hostname()) + `", "does-not-exist"], "version": "v0.163.0"}`)
	require.Equal(t, 200, resp.StatusCode)
	assert.JSONEq(t, `{"matched": 1}`, out)

	eventually(t, 10*time.Second, func() bool {
		ok := false
		f.with(gw.uid, func(a *fakeAgent) {
			ok = a.ident["service.version"] == "0.163.0" && a.health != nil && a.health.Healthy
		})
		return ok
	}, "the collector restarts and reports the new service.version")
	assert.Equal(t, "0.163.0", gw.Version())
	// available components were re-reported with the new module versions
	eventually(t, 5*time.Second, func() bool {
		var h []byte
		f.with(gw.uid, func(a *fakeAgent) { h = a.components.GetHash() })
		return bytes.Equal(h, r.comps.forVersion("0.163.0").Hash)
	}, "components hash follows the version")

	resp, out = post(`{"hostnames": [], "version": "0.163.0"}`)
	assert.JSONEq(t, `{"matched": 0}`, out)
	resp, _ = post(`{"hostnames": ["x"], "version": "banana"}`)
	assert.Equal(t, 400, resp.StatusCode)
	resp, _ = post(`not json`)
	assert.Equal(t, 400, resp.StatusCode)
	g, err := http.Get(srv.URL + "/control/upgrade")
	require.NoError(t, err)
	g.Body.Close()
	assert.Equal(t, 405, g.StatusCode)

	// GET /control/agents
	g, err = http.Get(srv.URL + "/control/agents?hostname=" + gw.Hostname())
	require.NoError(t, err)
	var st AgentState
	require.NoError(t, json.NewDecoder(g.Body).Decode(&st))
	g.Body.Close()
	assert.Equal(t, gw.Hostname(), st.Hostname)
	assert.Equal(t, "0.163.0", st.Version)
	assert.True(t, st.Connected)
	assert.Equal(t, gw.uid.String(), st.InstanceUID)
	g, _ = http.Get(srv.URL + "/control/agents?hostname=nope")
	assert.Equal(t, 404, g.StatusCode)
	g.Body.Close()
	g, _ = http.Get(srv.URL + "/control/agents")
	assert.Equal(t, 400, g.StatusCode)
	g.Body.Close()
}

func TestSimUpdaterInstallsOfferedVersion(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 8, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)

	po := f.offerPackage("0.163.0", sha256Bytes([]byte("offer")))
	f.pushOffer(po)

	eventually(t, 20*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool {
			s := a.pkgStatus[""]
			return a.ident["service.name"] == "myriarch-updater" && s != nil && s.Status == protobufs.PackageStatusEnum_PackageStatusEnum_Installed
		}) == 6
	}, "all updaters report INSTALLED")
	for _, c := range r.Collectors() {
		if c.updater == nil {
			continue
		}
		u := f.get(c.updater.uid)
		assert.Equal(t, "v0.163.0", u.pkgStatus[""].AgentHasVersion)
		// INSTALLED is only reported after the collector restarted into the new version
		f.with(c.uid, func(a *fakeAgent) { assert.Equal(t, "0.163.0", a.ident["service.version"], c.Hostname()) })
		assert.Equal(t, "0.163.0", c.Version())
	}
	assert.Equal(t, int64(6), r.Stats().UpdatesInstalled)
	assert.Equal(t, 6.0, metricValue(r.metrics.updates.WithLabelValues("installed")))
	// the gateway group has no updater and keeps its version
	gw := r.Collectors()[len(r.Collectors())-1]
	assert.Equal(t, "0.162.0", gw.Version())
}

func TestSimUpdateFailureRateReportsInstallFailedWithoutVersionChange(t *testing.T) {
	f := newFakeMyriarchd(t)
	p := fastProfile(t, func(p *Profile) { p.Behavior.UpdateFailureRate = 1 })
	r := startSim(t, f, 4, p, nil)
	waitAllConnected(t, r, 20*time.Second)
	f.pushOffer(f.offerPackage("0.163.0", sha256Bytes([]byte("offer"))))
	eventually(t, 20*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool {
			s := a.pkgStatus[""]
			return a.ident["service.name"] == "myriarch-updater" && s != nil && s.Status == protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed
		}) == 3
	}, "INSTALL_FAILED reported")
	for _, c := range r.Collectors() {
		assert.NotEqual(t, "0.163.0", c.Version())
		if c.updater != nil {
			s := f.get(c.updater.uid).pkgStatus[""]
			// half of the simulated failures end before the swap, half in a rollback
			assert.Regexp(t, "rolled back to v|signature verification", s.ErrorMessage)
		}
	}
	assert.Equal(t, int64(3), r.Stats().UpdatesFailed)
	assert.Equal(t, 3.0, metricValue(r.metrics.updates.WithLabelValues("failed")))
}

func TestSimVersionAfterUpdateUnchanged(t *testing.T) {
	f := newFakeMyriarchd(t)
	p := fastProfile(t, func(p *Profile) { p.Behavior.VersionAfterUpdate = "unchanged" })
	r := startSim(t, f, 4, p, nil)
	waitAllConnected(t, r, 20*time.Second)
	f.pushOffer(f.offerPackage("0.163.0", sha256Bytes([]byte("offer"))))
	eventually(t, 20*time.Second, func() bool { return r.Stats().UpdatesInstalled == 3 }, "installs complete")
	for _, c := range r.Collectors() {
		assert.NotEqual(t, "0.163.0", c.Version(), "update reported INSTALLED but the version did not change")
	}
}

func TestSimRealDownloadVerifiesSHA256(t *testing.T) {
	payload := []byte(strings.Repeat("artifact", 100))
	art := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer myrd_x" {
			http.Error(w, "no", 401)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer art.Close()

	f := newFakeMyriarchd(t)
	r := startSim(t, f, 4, fastProfile(t, nil), func(o *Options) { o.FakeDownloads = false })
	waitAllConnected(t, r, 20*time.Second)

	mk := func(hash []byte) *protobufs.PackagesAvailable {
		po := f.offerPackage("0.163.0", hash)
		po.Packages[""].File.DownloadUrl = art.URL + "/otelcol-contrib_0.163.0_windows_amd64.tar.gz"
		return po
	}
	// wrong hash -> INSTALL_FAILED, version unchanged
	f.pushOffer(mk(sha256Bytes([]byte("wrong"))))
	eventually(t, 20*time.Second, func() bool { return r.Stats().UpdatesFailed == 3 }, "hash mismatch detected")
	for _, c := range r.Collectors() {
		if c.updater != nil {
			assert.Contains(t, f.get(c.updater.uid).pkgStatus[""].ErrorMessage, "SHA-256 mismatch")
		}
		assert.NotEqual(t, "0.163.0", c.Version())
	}

	// correct hash -> installed
	f.pushOffer(mk(sha256Bytes(payload)))
	eventually(t, 20*time.Second, func() bool { return r.Stats().UpdatesInstalled == 3 }, "real download installed")
}

func TestSimDeterministicIdentitiesAcrossRuns(t *testing.T) {
	f := newFakeMyriarchd(t)
	p := fastProfile(t, nil)
	var first []string
	for run := 0; run < 2; run++ {
		r := startSim(t, f, 6, p, nil)
		waitAllConnected(t, r, 20*time.Second)
		var uids []string
		for _, c := range r.Collectors() {
			uids = append(uids, c.uid.String())
		}
		if run == 0 {
			first = uids
		} else {
			assert.Equal(t, first, uids, "restarting the simulator yields the same agents")
		}
		r.stop()
	}
	assert.Equal(t, 6, f.connected("otelcol-contrib")+f.count(func(a *fakeAgent) bool { return a.conn == nil && a.ident["service.name"] == "otelcol-contrib" }),
		"the second run reused the same instance uids (no extra agents on the server)")
}

func TestSimChurnReconnects(t *testing.T) {
	f := newFakeMyriarchd(t)
	p := fastProfile(t, func(p *Profile) { p.Behavior.OfflineChurnRate = 1 })
	r := startSim(t, f, 4, p, func(o *Options) { o.ChurnInterval = 400 * time.Millisecond })
	waitAllConnected(t, r, 20*time.Second)
	eventually(t, 20*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool { return a.disconnects > 0 }) >= 2
	}, "agents went offline")
	// they come back
	eventually(t, 20*time.Second, func() bool {
		st := r.Stats()
		return st.ConnectedCollectors == st.Collectors
	}, "agents reconnect: %+v", r.Stats())
}

// Stop must not stop the client of a Start that is still running: opamp-go does not allow Start and Stop of one
// client to overlap, and under -race the overlap is reported as a data race. A churn reconnect (goOffline) and the
// shutdown of the simulator are such a pair. The test overlaps Start and Stop of the same agents on purpose.
func TestSimStartAndStopOfOneAgentDoNotOverlap(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, c := range r.Collectors() {
		for i := 0; i < 20; i++ {
			wg.Add(2)
			go func() { defer wg.Done(); _ = c.Start(ctx) }()
			go func() { defer wg.Done(); c.Stop(ctx) }()
			if c.updater != nil {
				wg.Add(2)
				go func() { defer wg.Done(); _ = c.updater.Start(ctx) }()
				go func() { defer wg.Done(); c.updater.Stop(ctx) }()
			}
		}
	}
	wg.Wait()
	// Whatever the interleaving ended in, the agents can be stopped and brought back to a connected state. (The
	// connection counters of Stats are not checked: a late OnConnect of a stopped client may still count.)
	for _, c := range r.Collectors() {
		c.Stop(ctx)
		if c.updater != nil {
			c.updater.Stop(ctx)
		}
		require.NoError(t, c.Start(ctx))
		if c.updater != nil {
			require.NoError(t, c.updater.Start(ctx))
		}
	}
	eventually(t, 20*time.Second, func() bool {
		for _, c := range r.Collectors() {
			if !c.State().Connected || (c.updater != nil && !c.updater.State().Connected) {
				return false
			}
		}
		return true
	}, "every agent reconnects")
}

func TestSimRemoteConfigSurvivesReconnect(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	f.pushConfig("a: b\n", []byte("h1"))
	waitAllReportedApplied(t, f, r, []byte("h1"))

	c := r.Collectors()[0]
	c.goOffline(context.Background(), 100*time.Millisecond)
	eventually(t, 10*time.Second, func() bool { return c.State().Connected }, "reconnected")
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(2), r.Stats().ConfigsApplied, "the already applied config is not applied again after reconnect")
	f.with(c.uid, func(a *fakeAgent) {
		assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, a.rcStatus.Status)
		assert.Equal(t, []byte("h1"), a.rcStatus.LastRemoteConfigHash)
	})
}

// holdApplies lets the apply of a remote config start (APPLYING is reported) but not finish on its own: the timer
// that ends an apply is an hour away, and the test ends the apply it wants to end with finishApplyNow. That makes
// the state "an apply is in flight" last as long as the test needs, without a timing window.
func holdApplies(p *Profile) {
	p.Behavior.ConfigApplyDelay = DurRange{Min: time.Hour, Max: time.Hour}
}

// inflightApply returns the epoch and generation of the apply that is in flight, the arguments of its finishApply.
func (a *CollectorAgent) inflightApply() (epoch, gen int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.epoch, a.applyGen
}

// finishApplyNow runs what the apply timer of the apply in flight would run.
func finishApplyNow(c *CollectorAgent, hash, body []byte) {
	epoch, gen := c.inflightApply()
	c.finishApply(epoch, gen, hash, body, "", false)
}

// reportedStatus reports whether the server's last remote config status of the agent is st for hash.
func (f *fakeMyriarchd) reportedStatus(id uuid.UUID, st protobufs.RemoteConfigStatuses, hash []byte) bool {
	ok := false
	f.with(id, func(a *fakeAgent) {
		ok = a.rcStatus != nil && a.rcStatus.Status == st && bytes.Equal(a.rcStatus.LastRemoteConfigHash, hash)
	})
	return ok
}

// waitAllReportedApplied waits until the server holds APPLIED for hash from every collector. The counter of
// Stats().ConfigsApplied is not enough as a barrier: it moves before the APPLIED report is sent, so a test that
// acts on it races with the tail of the apply.
func waitAllReportedApplied(t testing.TB, f *fakeMyriarchd, r *runningSim, hash []byte) {
	t.Helper()
	eventually(t, 10*time.Second, func() bool {
		for _, c := range r.Collectors() {
			if !f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, hash) {
				return false
			}
		}
		return true
	}, "every collector reports APPLIED for %q", hash)
}

// An apply that is cut short by a restart (the upgrade of a version rollout drops the connection while the config
// is being applied) must not leave the agent reporting APPLYING for ever: the new process reports the config it
// runs, so the server offers the desired one again. Found by the HA step of scripts/e2e.sh, where such an agent held
// a config rollout until its applyTimeout.
// The test holds the apply, so there is no time window: the interrupted apply (h2) stays in flight until the
// connection drops, the reconnected agent must report the config it runs (h1), and the timer of the interrupted apply,
// fired by hand after the reconnect, must not take effect.
func TestSimInterruptedApplyIsNotReplayedAfterReconnect(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, holdApplies), nil)
	waitAllConnected(t, r, 20*time.Second)
	c := r.Collectors()[0]
	applying := func(hash []byte) {
		eventually(t, 10*time.Second, func() bool {
			return f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING, hash)
		}, "the config %q is being applied", hash)
	}

	f.pushConfig("a: b\n", []byte("h1"))
	applying([]byte("h1"))
	finishApplyNow(c, []byte("h1"), []byte("a: b\n"))
	eventually(t, 10*time.Second, func() bool {
		return f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, []byte("h1"))
	}, "the first config is applied")

	f.pushConfig("a: c\n", []byte("h2"))
	applying([]byte("h2")) // stays so: nothing ends this apply but the test
	staleEpoch, staleGen := c.inflightApply()
	appliedBefore := c.State().ConfigsApplied

	c.goOffline(context.Background(), 50*time.Millisecond)
	eventually(t, 10*time.Second, func() bool {
		st := r.Stats()
		return c.State().Connected && st.ConnectedUpdaters == st.Updaters // goOffline restarts the updater after the collector
	}, "reconnected")
	eventually(t, 10*time.Second, func() bool {
		return f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, []byte("h1"))
	}, "the reconnected agent reports the config it runs instead of the interrupted apply")

	// The timer of the interrupted apply would fire now; its epoch is gone, so it must not take effect.
	c.finishApply(staleEpoch, staleGen, []byte("h2"), []byte("a: c\n"), "", false)
	st := c.State()
	assert.Equal(t, "6831", st.RemoteConfig, "the interrupted apply did not become the running config (hex of h1)")
	assert.Equal(t, appliedBefore, st.ConfigsApplied, "the interrupted apply did not complete")
	assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED.String(), st.RemoteStatus)
	assert.Equal(t, "6831", st.StatusHash)
	assert.True(t, f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, []byte("h1")))
}

// The report that ends an apply runs after the apply updated the agent state, outside of its lock. When the next
// config arrives in that gap, the APPLIED report of the old config must not replace the APPLYING report of the new
// one: the server would hold APPLIED for the old hash while the new config is being applied (and the APPLYING
// report would be lost whenever both are pending in the client at once).
func TestSimStaleApplyReportDoesNotReplaceTheNewerConfigStatus(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 1, fastProfile(t, holdApplies), nil)
	waitAllConnected(t, r, 20*time.Second)
	c := r.Collectors()[0]

	f.pushConfig("a: b\n", []byte("h1"))
	eventually(t, 10*time.Second, func() bool {
		return f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING, []byte("h1"))
	}, "the first config is being applied")
	oldEpoch, oldGen := c.inflightApply()

	f.pushConfig("a: c\n", []byte("h2"))
	eventually(t, 10*time.Second, func() bool {
		return f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING, []byte("h2"))
	}, "the second config is being applied")

	c.reportRemoteStatus(oldEpoch, oldGen, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, []byte("h1"), "")
	st := c.State()
	assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING.String(), st.RemoteStatus)
	assert.Equal(t, "6832", st.StatusHash, "the status is still the one of the newer config (hex of h2)")

	// The current apply can still report.
	epoch, gen := c.inflightApply()
	c.reportRemoteStatus(epoch, gen, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, []byte("h2"), "")
	eventually(t, 10*time.Second, func() bool {
		return f.reportedStatus(c.uid, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, []byte("h2"))
	}, "the current apply reports")
}

func TestSimMetricsEndpoint(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, nil), func(o *Options) { o.MetricsListen = "127.0.0.1:0" })
	waitAllConnected(t, r, 20*time.Second)
	// scrape through the registry handler (listen address with :0 is not retrievable)
	rec := httptest.NewRecorder()
	handler := newMetricsHandler(r.metrics)
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, name := range []string{"myriarch_sim_agents_connected", "myriarch_sim_configs_applied_total", "myriarch_sim_configs_failed_total", `myriarch_sim_updates_total{result="installed"}`, `myriarch_sim_updates_total{result="failed"}`} {
		assert.Contains(t, body, name)
	}
	assert.Contains(t, body, "myriarch_sim_agents_connected 4")
}

func TestSimShutdownDisconnectsEverything(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 4, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	r.stop()
	assert.Equal(t, 0, r.Stats().ConnectedCollectors)
	assert.Equal(t, 0, r.Stats().ConnectedUpdaters)
	eventually(t, 10*time.Second, func() bool {
		return f.connected("otelcol-contrib") == 0 && f.connected("myriarch-updater") == 0
	}, "server sees the disconnects")
}

func TestSimDurationStopsRun(t *testing.T) {
	f := newFakeMyriarchd(t)
	s, err := New(Options{Server: f.url(), Agents: 2, Updaters: false, Profile: fastProfile(t, nil), Duration: 500 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, s.Run(context.Background()))
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestNewValidatesOptions(t *testing.T) {
	p := fastProfile(t, nil)
	_, err := New(Options{Server: "ws://x", Agents: 1})
	require.Error(t, err)
	_, err = New(Options{Agents: 1, Profile: p})
	require.Error(t, err)
	_, err = New(Options{Server: "ws://x", Agents: 0, Profile: p})
	require.Error(t, err)
	seed := int64(99)
	s, err := New(Options{Server: "ws://x", Agents: 2, Profile: p, Seed: &seed})
	require.NoError(t, err)
	assert.Equal(t, int64(99), s.seed)
	_, err = New(Options{Server: "ws://x", Agents: 2, Profile: p, CAFile: "/nonexistent"})
	require.Error(t, err)
}

func TestCommandsExist(t *testing.T) {
	root := NewRootCommand()
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	assert.True(t, names["run"])
	assert.True(t, names["version"])
	run, _, err := root.Find([]string{"run"})
	require.NoError(t, err)
	for _, fl := range []string{"server", "token", "agents", "updaters", "profile", "seed", "metrics-listen", "control-listen", "duration", "fake-downloads", "log-level"} {
		assert.NotNil(t, run.Flags().Lookup(fl), fl)
	}
	assert.Equal(t, "true", run.Flags().Lookup("fake-downloads").DefValue)
	root.SetArgs([]string{"run"})
	root.SetErr(io.Discard)
	assert.Error(t, root.Execute(), "--profile is required")
}

// metricValue reads a gauge or counter value.
func metricValue(m prometheus.Metric) float64 {
	var out dto.Metric
	if err := m.Write(&out); err != nil {
		return -1
	}
	if out.Gauge != nil {
		return out.Gauge.GetValue()
	}
	if out.Counter != nil {
		return out.Counter.GetValue()
	}
	return -1
}

type testLogWriter struct{ t testing.TB }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// A real supervisor reports the new version with {healthy:false} first, then
// StatusStarting, then StatusOK; an upgrade that restarts the supervisor drops
// the connection. validate_config keeps the old config running after FAILED
// without an APPLIED report.
func TestSimUpgradeReportsTheRealStartupSequenceAndReconnects(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, func(p *Profile) {
		p.Behavior.ReconnectOnUpgrade = true
		p.Behavior.StartupDuration = DurRange{Min: 400 * time.Millisecond, Max: 400 * time.Millisecond}
	}), nil)
	waitAllConnected(t, r, 20*time.Second)
	c := r.Collectors()[len(r.Collectors())-1]
	var discBefore int
	f.with(c.uid, func(a *fakeAgent) { discBefore = a.disconnects })

	<-c.upgrade("0.163.0")
	eventually(t, 10*time.Second, func() bool {
		ok := false
		f.with(c.uid, func(a *fakeAgent) {
			ok = a.ident["service.version"] == "0.163.0" && a.health != nil && a.health.Healthy && a.health.Status == "StatusOK"
		})
		return ok
	}, "healthy with the new version")
	f.with(c.uid, func(a *fakeAgent) {
		assert.Greater(t, a.disconnects, discBefore, "the upgrade drops the connection and reconnects")
		n := len(a.healthLog)
		require.GreaterOrEqual(t, n, 3)
		assert.Equal(t, []string{"", "StatusStarting", "StatusOK"}, a.healthLog[n-3:], "boot sequence of the new process")
	})
}

func TestSimValidateConfigKeepsFailedStatus(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, func(p *Profile) { p.Behavior.ValidateConfig = true }), nil)
	waitAllConnected(t, r, 20*time.Second)
	f.pushConfig("receivers:\n  otlp_grpc: {}\n", []byte("good"))
	waitAllReportedApplied(t, f, r, []byte("good"))
	f.pushConfig("receivers:\n  simulate-config-failure: {}\n", []byte("bad"))
	eventually(t, 10*time.Second, func() bool { return r.Stats().ConfigsFailed == 2 }, "bad config fails")
	time.Sleep(600 * time.Millisecond)
	for _, c := range r.Collectors() {
		f.with(c.uid, func(a *fakeAgent) {
			require.NotNil(t, a.rcStatus)
			assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, a.rcStatus.Status, "no APPLIED report for the old hash")
			assert.Equal(t, []byte("bad"), a.rcStatus.LastRemoteConfigHash)
		})
	}
}
