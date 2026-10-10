// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeServerEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"ws://myriarch:8080/v1/opamp":      "ws://myriarch:8080",
		"wss://fleet.example.com/v1/opamp": "wss://fleet.example.com:443",
		"ws://fleet.example.com/v1/opamp":  "ws://fleet.example.com:80",
		"ws://user:pw@Host:9/v1/opamp?x=1": "ws://host:9",
		"":                                 "",
		"ftp://host":                       "",
		"not a url":                        "",
	} {
		assert.Equal(t, want, normalizeServerEndpoint(in), in)
	}
}

// componentOf returns the reported dev.myriarch.update component of a simulated updater.
func componentOf(f *fakeMyriarchd, u *UpdaterAgent) (*protobufs.ComponentHealth, []string) {
	var c *protobufs.ComponentHealth
	var log []string
	f.with(u.uid, func(a *fakeAgent) {
		if a.health != nil {
			c = a.health.ComponentHealthMap[updateComponent]
		}
		log = append(log, a.updateLog...)
	})
	return c, log
}

func stepStatuses(c *protobufs.ComponentHealth) map[string]string {
	m := map[string]string{}
	for k, v := range c.ComponentHealthMap {
		if name, ok := strings.CutPrefix(k, updateStepPrefix); ok {
			m[name] = v.Status
		}
	}
	return m
}

func TestSimUpdaterReportsAttributesForTheDownloadChoice(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 4, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	want := normalizeServerEndpoint(f.url())
	require.NotEmpty(t, want)
	n := 0
	for _, c := range r.Collectors() {
		if c.updater == nil {
			continue
		}
		f.with(c.updater.uid, func(a *fakeAgent) {
			assert.Equal(t, want, a.nonIdent["dev.myriarch.updater.server_endpoint"])
			assert.Equal(t, "binary", a.nonIdent["dev.myriarch.updater.install_method"])
		})
		n++
	}
	assert.Positive(t, n)
}

func TestSimUpdaterReportsStepsOfASuccessfulUpdate(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 4, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	before := map[string]string{}
	for _, c := range r.Collectors() {
		before[c.Hostname()] = c.Version()
	}
	f.pushOffer(f.offerPackage("0.163.0", sha256Bytes([]byte("offer"))))
	eventually(t, 20*time.Second, func() bool { return r.Stats().UpdatesInstalled == 3 }, "installs complete")

	for _, c := range r.Collectors() {
		if c.updater == nil {
			continue
		}
		var comp *protobufs.ComponentHealth
		eventually(t, 10*time.Second, func() bool {
			comp, _ = componentOf(f, c.updater)
			return comp != nil && comp.Status == outcomeSucceeded
		}, "final component of "+c.Hostname())
		assert.True(t, comp.Healthy)
		assert.Empty(t, comp.LastError)
		assert.Equal(t, "v0.163.0", comp.ComponentHealthMap["version"].Status)
		assert.Equal(t, "v"+before[c.Hostname()], comp.ComponentHealthMap["from"].Status)
		assert.Equal(t, map[string]string{"downloading": "done", "verifying": "done", "installing": "done", "restarting": "done", "health_check": "done"}, stepStatuses(comp))
		for k, ch := range comp.ComponentHealthMap {
			if strings.HasPrefix(k, updateStepPrefix) {
				assert.NotZero(t, ch.StartTimeUnixNano, k)
				assert.GreaterOrEqual(t, ch.StatusTimeUnixNano, ch.StartTimeUnixNano, k)
			}
		}
		_, phases := componentOf(f, c.updater)
		// the phases appear in order (other health reports in between are fine)
		idx := -1
		for _, want := range []string{"downloading", "verifying", "installing", "restarting", "health_check", "succeeded"} {
			next := -1
			for i := idx + 1; i < len(phases); i++ {
				if phases[i] == want {
					next = i
					break
				}
			}
			require.Greater(t, next, idx, "phase %s after index %d in %v", want, idx, phases)
			idx = next
		}
	}
}

// driveAttempt runs one simulated attempt with a forced outcome on the first updater.
func driveAttempt(t *testing.T, kind string) (*protobufs.ComponentHealth, error, *runningSim, *fakeMyriarchd) {
	t.Helper()
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 4, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	var u *UpdaterAgent
	for _, c := range r.Collectors() {
		if c.updater != nil {
			u = c.updater
			break
		}
	}
	require.NotNil(t, u)
	from := u.col.Version()
	a := newSimAttempt("0.163.0", "v"+from, time.Now())
	u.mu.Lock()
	u.attempt = a
	u.mu.Unlock()
	err := u.simulateInstall(context.Background(), a, "0.163.0", from, 100*time.Millisecond, kind, strings.NewReader("pkg"), nil)
	final := ""
	switch kind {
	case failVerification:
		final = outcomeFailed
	case failHealth:
		final = outcomeRolledBack
	default:
		final = outcomeSucceeded
	}
	var comp *protobufs.ComponentHealth
	eventually(t, 10*time.Second, func() bool {
		comp, _ = componentOf(f, u)
		return comp != nil && comp.Status == final
	}, "final component "+final)
	return comp, err, r, f
}

func TestSimUpdaterVerificationFailureLeavesTheHostUntouched(t *testing.T) {
	comp, err, _, _ := driveAttempt(t, failVerification)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature verification")
	assert.True(t, comp.Healthy, "a failure before the swap leaves the host healthy")
	assert.Contains(t, comp.LastError, "signature verification")
	assert.Equal(t, map[string]string{"downloading": "done", "verifying": "failed", "installing": "skipped", "restarting": "skipped", "health_check": "skipped"}, stepStatuses(comp))
	assert.Contains(t, comp.ComponentHealthMap["step:verifying"].LastError, "signature verification")
}

func TestSimUpdaterHealthFailureRollsBack(t *testing.T) {
	comp, err, _, _ := driveAttempt(t, failHealth)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rolled back to v")
	assert.True(t, comp.Healthy, "the host runs the previous version again")
	assert.Contains(t, comp.LastError, "rolled back")
	assert.Equal(t, map[string]string{"downloading": "done", "verifying": "done", "installing": "done", "restarting": "done", "health_check": "failed", "rolling_back": "done"}, stepStatuses(comp))
}

func TestSimUpdaterCancelledAttemptEndsFailed(t *testing.T) {
	f := newFakeMyriarchd(t)
	r := startSim(t, f, 2, fastProfile(t, nil), nil)
	waitAllConnected(t, r, 20*time.Second)
	var u *UpdaterAgent
	for _, c := range r.Collectors() {
		if c.updater != nil {
			u = c.updater
		}
	}
	require.NotNil(t, u)
	a := newSimAttempt("0.163.0", "v0.162.0", time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := u.simulateInstall(ctx, a, "0.163.0", "0.162.0", time.Second, failNone, strings.NewReader("pkg"), nil)
	require.Error(t, err)
	assert.Equal(t, outcomeFailed, a.phase)
	assert.Equal(t, "failed", a.snapshotSteps()["downloading"])
}
