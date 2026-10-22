// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/myriarch/myriarch/internal/domain"
)

// An OpAMP Bridge is never a target of a version rollout, and the planner says
// why, once, on the agent (ADR-0037).
func TestVersionRolloutSkipsOpAMPBridgeAgents(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	versionFleet(e)
	good := e.addAgents(2, "f", inSync())
	bridge := e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Kind = domain.AgentKindBridge })
	e.publishSnapshot()
	e.plan()

	r := e.only()
	var got []domain.AgentID
	for _, tg := range e.targets(r.ID) {
		got = append(got, tg.AgentID)
	}
	assert.ElementsMatch(t, good, got, "the Bridge is not a target")

	count := func() int {
		n := 0
		for _, ev := range e.st.Events(bridge[0]) {
			if ev.Type == domain.AgentEventBridgeLocked {
				assert.Equal(t, "version_rollout", ev.Details["action"])
				assert.Contains(t, ev.Message, "spec.image")
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, count())
	e.plan()
	e.plan()
	assert.Equal(t, 1, count(), "planning again does not repeat the event")
	for _, id := range good {
		assert.Empty(t, e.st.Events(id), "nothing is noted for a collector")
	}
}
