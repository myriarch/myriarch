// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// processRSSMiB returns the resident set size of this process (best effort).
func processRSSMiB() int {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return -1
	}
	kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return kb / 1024
}

func logMem(t *testing.T, when string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	t.Logf("memory %s: heapAlloc=%d MiB, heapSys=%d MiB, sys=%d MiB, rss=%d MiB (includes the in-process test server), goroutines=%d",
		when, m.HeapAlloc>>20, m.HeapSys>>20, m.Sys>>20, processRSSMiB(), runtime.NumGoroutine())
}

// TestScale1000CollectorsAndUpdaters runs the acceptance scenario: 1000
// simulated collectors plus the updaters of the mixed-fleet profile (900) in one
// process against an in-process OpAMP server with the profile's connectRate
// of 100/s.
func TestScale1000CollectorsAndUpdaters(t *testing.T) {
	if testing.Short() || os.Getenv("MYRIARCH_SIM_SKIP_SCALE") == "1" {
		t.Skip("scale test skipped (-short or MYRIARCH_SIM_SKIP_SCALE=1)")
	}
	p, err := LoadProfile(mixedFleetPath)
	require.NoError(t, err)
	// keep connectRate 100/s (acceptance), shorten the behavior timings
	p.Behavior.ConfigApplyDelay = DurRange{Min: 50 * time.Millisecond, Max: 500 * time.Millisecond}
	p.Behavior.RestartDuration = DurRange{Min: 100 * time.Millisecond, Max: 300 * time.Millisecond}
	p.Behavior.UpdaterInstallDuration = DurRange{Min: 100 * time.Millisecond, Max: 600 * time.Millisecond}

	f := newFakeMyriarchd(t)
	logMem(t, "before start")
	started := time.Now()
	r := startSim(t, f, 1000, p, nil)

	// 1. everything connects in < 60 s
	eventually(t, 90*time.Second, func() bool {
		st := r.Stats()
		return st.ConnectedCollectors == 1000 && st.ConnectedUpdaters == len(r.Updaters())
	}, "all connect: %+v", r.Stats())
	elapsed := time.Since(started)
	st := r.Stats()
	t.Logf("connected %d collectors + %d updaters in %s (connectRate %.0f/s)", st.ConnectedCollectors, st.ConnectedUpdaters, elapsed.Round(time.Millisecond), p.Behavior.ConnectRate)
	assert.Equal(t, 900, st.Updaters)
	assert.Less(t, elapsed, 60*time.Second, "all agents must be connected within 60 s")
	eventually(t, 60*time.Second, func() bool {
		return f.connected("otelcol-contrib") == 1000 && f.connected("myriarch-updater") == 900
	}, "server side sees all connections")
	logMem(t, "after connect")

	// 2. remote config: every collector answers APPLIED
	hash := []byte("scale-config-1")
	f.pushConfig("receivers:\n  otlp_grpc: {}\nexporters:\n  debug: {}\n", hash)
	eventually(t, 90*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool {
			return a.ident["service.name"] == "otelcol-contrib" && a.rcStatus != nil &&
				a.rcStatus.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED && bytes.Equal(a.rcStatus.LastRemoteConfigHash, hash)
		}) == 1000
	}, "all 1000 collectors report APPLIED: %+v", r.Stats())
	assert.Equal(t, int64(1000), r.Stats().ConfigsApplied)

	// 3. restart command to a sample of 200 collectors
	before := map[string]uint64{}
	var targets []*CollectorAgent
	for i, c := range r.Collectors() {
		if i%5 == 0 {
			targets = append(targets, c)
			f.with(c.uid, func(a *fakeAgent) { before[c.uid.String()] = a.health.StartTimeUnixNano })
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for _, c := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(c *CollectorAgent) {
			defer wg.Done()
			defer func() { <-sem }()
			f.sendTo(c.uid, &protobufs.ServerToAgent{Command: &protobufs.ServerToAgentCommand{Type: protobufs.CommandType_CommandType_Restart}})
		}(c)
	}
	wg.Wait()
	eventually(t, 60*time.Second, func() bool {
		n := 0
		for _, c := range targets {
			f.with(c.uid, func(a *fakeAgent) {
				if a.health != nil && a.health.Healthy && a.health.StartTimeUnixNano > before[c.uid.String()] {
					n++
				}
			})
		}
		return n == len(targets)
	}, "restarted collectors report healthy with a new start time")

	// 4. external upgrade (control API) of all k8s-gateway hosts changes service.version
	var gw []string
	for _, c := range r.Collectors() {
		if c.spec.Group == "k8s-gateway" {
			gw = append(gw, c.Hostname())
		}
	}
	require.Len(t, gw, 100)
	for _, h := range gw {
		for _, c := range r.FindByHostname(h) {
			c.upgrade("0.163.0")
		}
	}
	eventually(t, 60*time.Second, func() bool {
		n := 0
		for _, c := range r.Collectors() {
			if c.spec.Group != "k8s-gateway" {
				continue
			}
			f.with(c.uid, func(a *fakeAgent) {
				if a.ident["service.version"] == "0.163.0" {
					n++
				}
			})
		}
		return n == 100
	}, "gateway collectors report 0.163.0")

	// 5. package offers to all updaters -> INSTALLED and new collector versions
	f.pushOffer(f.offerPackage("0.163.0", sha256Bytes([]byte("scale-offer"))))
	eventually(t, 90*time.Second, func() bool { return r.Stats().UpdatesInstalled == 900 }, "all 900 updaters install: %+v", r.Stats())
	eventually(t, 30*time.Second, func() bool {
		return f.count(func(a *fakeAgent) bool {
			s := a.pkgStatus[""]
			return a.ident["service.name"] == "myriarch-updater" && s != nil && s.Status == protobufs.PackageStatusEnum_PackageStatusEnum_Installed
		}) == 900
	}, "server sees INSTALLED from all updaters")
	for _, c := range r.Collectors() {
		if c.updater != nil {
			require.Equal(t, "0.163.0", c.Version(), c.Hostname())
		}
	}
	logMem(t, "after config/restart/upgrade/update")

	// idle: heartbeats only. The CPU time includes the in-process test server.
	time.Sleep(2 * time.Second) // let timers settle
	cpu0, wall0 := cpuTime(), time.Now()
	time.Sleep(10 * time.Second)
	if cpu0 > 0 {
		cores := float64(cpuTime()-cpu0) / float64(time.Since(wall0))
		t.Logf("idle CPU over 10s: %.4f cores (1900 connections, simulator + in-process server)", cores)
		assert.Less(t, cores, 2.0, "idle CPU stays below 2 cores")
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	assert.Less(t, m.Sys>>20, uint64(1536), "Go runtime memory stays below 1.5 GiB")

	r.stop()
	assert.Equal(t, 0, r.Stats().ConnectedCollectors+r.Stats().ConnectedUpdaters)
	_ = context.Background
}
