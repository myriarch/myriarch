// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	n := 0
	for _, a := range e.auditActions() {
		if a == "agent.bridge_held_back" {
			n++
		}
	}
	assert.Equal(t, 1, n, "an audit entry as well, once")
	for _, id := range good {
		assert.Empty(t, e.st.Events(id), "nothing is noted for a collector")
	}
}

func bridgeOpt(mode string) agentOpt {
	return func(_ int, a *domain.Agent) {
		a.Kind = domain.AgentKindBridge
		a.NonIdentifyingAttributes[domain.AttrBridgeMode] = mode
	}
}

// A target that was created while the agent still counted as a collector is
// skipped once the agent reports as a Bridge: no executor is called for it.
func TestVersionRolloutSkipsTargetThatBecameABridge(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	versionFleet(e)
	ids := e.addAgents(2, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	require.Len(t, e.targets(r.ID), 2)

	a := e.st.Agent(ids[0])
	a.Kind = domain.AgentKindBridge
	a.NonIdentifyingAttributes[domain.AttrBridgeMode] = "standalone"
	e.st.PutAgent(*a)
	e.steps(3)

	for _, c := range e.exec.startCalls {
		for _, tg := range c.Targets {
			assert.NotEqual(t, ids[0], tg.AgentID, "no executor call for the Bridge")
		}
	}
	tg := e.targetOf(r.ID, ids[0])
	assert.Equal(t, domain.TargetSkipped, tg.Status)
	assert.Equal(t, domain.BridgeVersionRefused, tg.LastError)
}

// A Bridge that its fleet configures is a member of config rollouts, so a
// change in Git moves its desired config like that of a collector; a locked
// Bridge is not.
func TestConfigRolloutIncludesOnlyBridgesThatTheirFleetConfigures(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	e.addFleet("f", func(f *domain.Fleet) { f.Bridge.Manage = true })
	e.addFleet("g")
	managed := e.addAgents(1, "f", bridgeOpt("standalone"))
	operator := e.addAgents(1, "f", bridgeOpt("operator"))
	locked := e.addAgents(1, "g", bridgeOpt("standalone"))
	coll := e.addAgents(1, "f")
	e.publishSnapshot()
	e.plan()

	var got []domain.AgentID
	for _, r := range e.rollouts() {
		for _, tg := range e.targets(r.ID) {
			got = append(got, tg.AgentID)
		}
	}
	assert.ElementsMatch(t, append(managed, coll...), got)
	assert.NotContains(t, got, operator[0])
	assert.NotContains(t, got, locked[0])
}

// The note that a Bridge is left out goes to the event and audit log of all
// replicas, so a new term of leadership writes it again: another leader may
// have noted other versions in between, and a note must not go missing.
func TestRunNotesTheBridgeAgainWhenTheEngineLeadsAgain(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	versionFleet(e)
	e.addAgents(2, "f", inSync())
	bridge := e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Kind = domain.AgentKindBridge })
	e.publishSnapshot()
	count := func() int {
		n := 0
		for _, ev := range e.st.Events(bridge[0]) {
			if ev.Type == domain.AgentEventBridgeLocked {
				n++
			}
		}
		return n
	}
	e.plan()
	e.plan()
	require.Equal(t, 1, count(), "within a term the note is written once")

	ctx, cancel := context.WithCancel(e.ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = e.eng.Run(ctx)
	}()
	require.Eventually(t, func() bool { return count() == 2 }, 20*time.Second, 10*time.Millisecond,
		"the first run of a term notes the Bridge again")
	cancel()
	wg.Wait()
}
