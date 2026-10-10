// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateComponent returns the latest reported dev.myriarch.update component.
func (s *opampTestServer) updateComponent() *protobufs.ComponentHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if h := s.msgs[i].Health; h != nil {
			if c, ok := h.ComponentHealthMap[UpdateComponent]; ok {
				return c
			}
		}
	}
	return nil
}

// seenUpdatePhases returns the distinct phases of dev.myriarch.update in the
// order they were reported.
func (s *opampTestServer) seenUpdatePhases() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		if h := m.Health; h != nil {
			if c, ok := h.ComponentHealthMap[UpdateComponent]; ok && (len(out) == 0 || out[len(out)-1] != c.Status) {
				out = append(out, c.Status)
			}
		}
	}
	return out
}

func TestUpdaterReportsServerEndpointAttribute(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.srv.waitConnected()
	non := attrMap(h.srv.first().AgentDescription.NonIdentifyingAttributes)
	ep := h.srv.url() // ws://127.0.0.1:<port>/v1/opamp
	assert.Equal(t, ep[:len(ep)-len("/v1/opamp")], non["dev.myriarch.updater.server_endpoint"])
	assert.Equal(t, "binary", non["dev.myriarch.updater.install_method"])
	assert.NotContains(t, non, "dev.myriarch.updater.image_collector_version")
}

func TestUpdaterReportsUpdateStepsInHealth(t *testing.T) {
	h := newHarness(t)
	h.art.delay = 100 * time.Millisecond
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	assert.Nil(t, h.srv.updateComponent(), "no attempt yet, no component")

	before := time.Now().Add(-time.Second)
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("1")))
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_Installed)
	h.srv.eventually(func() bool {
		c := h.srv.updateComponent()
		return c != nil && c.Status == "succeeded"
	}, "final component reported")

	c := h.srv.updateComponent()
	assert.True(t, c.Healthy)
	assert.Empty(t, c.LastError)
	assert.GreaterOrEqual(t, c.StartTimeUnixNano, uint64(before.UnixNano()))
	m := c.ComponentHealthMap
	assert.Equal(t, "v0.163.0", m["version"].Status)
	assert.Equal(t, "v0.162.0", m["from"].Status)
	for _, s := range []string{"downloading", "verifying", "installing", "restarting", "health_check"} {
		require.Contains(t, m, "step:"+s)
		assert.Equal(t, "done", m["step:"+s].Status, s)
	}
	assert.NotContains(t, m, "step:rolling_back")

	phases := h.srv.seenUpdatePhases()
	assert.Contains(t, phases, "downloading", "intermediate phases are reported while they run")
	assert.Equal(t, "succeeded", phases[len(phases)-1])
	assert.Subset(t, phases, []string{"downloading", "verifying", "installing", "restarting", "health_check", "succeeded"})

	// the attempt survives an updater restart (last-attempt.json)
	h.stop()
	h.srv.mu.Lock()
	h.srv.msgs = nil
	h.srv.mu.Unlock()
	h.start()
	h.srv.waitConnected()
	c = h.srv.updateComponent()
	require.NotNil(t, c)
	assert.Equal(t, "succeeded", c.Status)
	assert.Equal(t, "v0.163.0", c.ComponentHealthMap["version"].Status)
}

func TestUpdaterReportsRollbackInHealth(t *testing.T) {
	h := newHarness(t)
	h.healthy = func(active string) bool { return active != "0.163.0" }
	h.cfg.Update.HealthTimeout = Duration(300 * time.Millisecond)
	archive := collectorArchive(t, "0.163.0")
	url := h.art.add("0.163.0", archive)
	h.start()
	h.srv.waitConnected()
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.163.0", archive)}, sha([]byte("1")))
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	h.srv.eventually(func() bool {
		c := h.srv.updateComponent()
		return c != nil && c.Status == "rolled_back"
	}, "rolled_back reported")
	c := h.srv.updateComponent()
	assert.True(t, c.Healthy, "the host runs the previous version again")
	assert.Contains(t, c.LastError, "rolled back to v0.162.0")
	m := c.ComponentHealthMap
	assert.Equal(t, "failed", m["step:health_check"].Status)
	assert.Contains(t, m["step:health_check"].LastError, "health check")
	assert.Equal(t, "done", m["step:rolling_back"].Status)
}

func TestUpdaterReportsPolicyRejectionInHealth(t *testing.T) {
	h := newHarness(t)
	archive := collectorArchive(t, "0.100.0") // older than the active 0.162.0
	url := h.art.add("0.100.0", archive)
	h.start()
	h.srv.waitConnected()
	h.srv.offerPackages(map[string]*protobufs.PackageAvailable{"": h.art.offer(url, "v0.100.0", archive)}, sha([]byte("1")))
	h.srv.waitStatus("", protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed)
	h.srv.eventually(func() bool {
		c := h.srv.updateComponent()
		return c != nil && c.Status == "failed"
	}, "failed reported")
	m := h.srv.updateComponent().ComponentHealthMap
	assert.Equal(t, "failed", m["step:verifying"].Status)
	assert.Contains(t, m["step:verifying"].LastError, "downgrades are disabled")
	assert.Equal(t, "skipped", m["step:downloading"].Status)
	assert.Empty(t, h.svc.Calls(), "supervisor untouched")
}
