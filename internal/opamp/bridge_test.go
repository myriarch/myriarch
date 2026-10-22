// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/domain"
)

// The fake assigner of the rig assigns every agent to the fleet "edge-nl" and
// sets its desired config, as the real one does for a collector. What a Bridge
// may be sent is decided by the server (bridge.go, ADR-0037).

func bridgeDesc(mode string) descOpts {
	o := descOpts{host: "bridge-1", version: "0.162.0", service: domain.BridgeServiceName}
	if mode != "" {
		o.extra = []*protobufs.KeyValue{kv(domain.AttrBridgeMode, mode)}
	}
	if mode == "standalone" {
		o.service = "payments-collector"
	}
	return o
}

// optIn stores the fleet "edge-nl" with or without spec.bridge.manage.
func optIn(t *testing.T, r *rig, manage bool) {
	t.Helper()
	snap := domain.GitOpsSnapshot{Commit: "c1", Fleets: []domain.Fleet{{
		ObjectMeta: domain.ObjectMeta{Name: "edge-nl"},
		FleetSpec:  domain.FleetSpec{Bridge: domain.BridgeSpec{Manage: manage}},
		Source:     domain.SourceRef{Path: "fleets/edge-nl.yaml", Commit: "c1"},
	}}}
	if _, err := r.db.GitOps().ApplySnapshot(context.Background(), snap, domain.GitOpsSync{Commit: &domain.GitCommit{SHA: "c1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeWithoutOptInGetsNoRemoteConfig(t *testing.T) {
	r := newRig(t, rigOpts{})
	optIn(t, r, false)
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, bridgeDesc("standalone")))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("a Bridge of a fleet without spec.bridge.manage must not be offered a config")
	}
	ag := r.agent(id)
	if ag.Kind != domain.AgentKindBridge || ag.Assignment.DesiredConfigHash.IsZero() {
		t.Fatalf("kind %q, desired %v: the Bridge is recognised and the guard, not the assignment, holds the offer back", ag.Kind, ag.Assignment.DesiredConfigHash)
	}
	if !ag.EffectiveConfigHash.IsZero() {
		t.Fatal("no effective config of a Bridge is stored")
	}

	// heartbeats, a push and a resync neither offer the config nor write an event each time
	for i := 0; i < 3; i++ {
		a.send(&protobufs.AgentToServer{})
		if m := a.mustRecv(); m.RemoteConfig != nil {
			t.Fatal("a heartbeat offered the config")
		}
	}
	newDesired(t, r, id, "service:\n  pipelines: {}\n# v2\n")
	a.expectSilence(300 * time.Millisecond)
	if err := r.ctl.Resync(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	if m, ok := a.recv(500 * time.Millisecond); ok && m.RemoteConfig != nil {
		t.Fatal("a resync offered the config")
	}
	r.waitEvent(id, domain.AgentEventBridgeLocked)
	// one event for the first hash and one for the second, not one per message
	if n := r.countEvents(id, domain.AgentEventBridgeLocked); n != 2 {
		t.Fatalf("%d bridge_locked events, want 2", n)
	}
	if r.hasEvent(id, domain.AgentEventConfigOffered) {
		t.Fatal("no config_offered event for a Bridge")
	}
}

func TestBridgeWithoutAnyFleetGetsNoRemoteConfig(t *testing.T) {
	r := newRig(t, rigOpts{}) // no fleet in the store: the fleet of the assignment is unknown
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, bridgeDesc("")))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("a Bridge of an unknown fleet must not be offered a config")
	}
	r.waitEvent(id, domain.AgentEventBridgeLocked)
}

func TestBridgeWithOptInInStandaloneModeGetsRemoteConfig(t *testing.T) {
	r := newRig(t, rigOpts{})
	optIn(t, r, true)
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, bridgeDesc("standalone")))
	remoteBody(t, a.mustRecv())
	if r.agent(id).Kind != domain.AgentKindBridge || r.hasEvent(id, domain.AgentEventBridgeLocked) {
		t.Fatal("an opted-in standalone Bridge is a Bridge and is not held back")
	}
	hash := newDesired(t, r, id, "service:\n  pipelines: {}\n# v2\n")
	got, _ := remoteBody(t, a.mustRecv())
	if string(got) != string(hash) {
		t.Fatal("the push reaches the opted-in Bridge")
	}
}

func TestBridgeInOperatorOrUnknownModeStaysLockedWithOptIn(t *testing.T) {
	for _, mode := range []string{"operator", "", "something-new"} {
		t.Run("mode="+mode, func(t *testing.T) {
			r := newRig(t, rigOpts{})
			optIn(t, r, true)
			id := newAgentID()
			a := r.connect("", id, capsCollector)
			a.send(firstMessage(id, bridgeDesc(mode)))
			if m := a.mustRecv(); m.RemoteConfig != nil {
				t.Fatal("this Bridge is configured by no fleet, with the opt-in as well")
			}
			r.waitEvent(id, domain.AgentEventBridgeLocked)
		})
	}
}

func TestBridgeRestartIsRefusedWithAndWithoutOptIn(t *testing.T) {
	for _, manage := range []bool{false, true} {
		r := newRig(t, rigOpts{})
		optIn(t, r, manage)
		id := newAgentID()
		a := r.connect("", id, capsCollector)
		a.send(firstMessage(id, bridgeDesc("standalone")))
		a.mustRecv()
		if err := r.ctl.Restart(context.Background(), id, "user:1"); err != nil {
			t.Fatal(err)
		}
		if m, ok := a.recv(500 * time.Millisecond); ok && m.Command != nil {
			t.Fatalf("manage=%v: a restart command reached the Bridge", manage)
		}
		r.waitEvent(id, domain.AgentEventBridgeLocked)
	}
}

func TestBridgeBecomesBridgeWhenItReportsTheAttributeLater(t *testing.T) {
	r := newRig(t, rigOpts{})
	optIn(t, r, false)
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	// a collector first (an earlier release stored it with its effective config) ...
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	remoteBody(t, a.mustRecv())
	if ag := r.agent(id); ag.Kind != domain.AgentKindCollector || ag.EffectiveConfigHash.IsZero() {
		t.Fatalf("kind %q", ag.Kind)
	}
	// ... then the same uid reports the Bridge attribute
	a.send(&protobufs.AgentToServer{AgentDescription: description(id, bridgeDesc("standalone"))})
	a.mustRecv()
	eventually(t, "kind bridge", 5*time.Second, func() bool { return r.agent(id).Kind == domain.AgentKindBridge })
	if !r.agent(id).EffectiveConfigHash.IsZero() {
		t.Fatal("the effective config stored before is forgotten")
	}
}

func TestCollectorIsUnchanged(t *testing.T) {
	r := newRig(t, rigOpts{})
	optIn(t, r, false) // the opt-in is about Bridges only
	a, id, _ := registerApplied(t, r, "edge-1")
	if r.agent(id).Kind != domain.AgentKindCollector {
		t.Fatal("a collector stays a collector")
	}
	newDesired(t, r, id, "service:\n  pipelines: {}\n# v2\n")
	remoteBody(t, a.mustRecv())
	if err := r.ctl.Restart(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	if m := a.mustRecv(); m.Command == nil || m.Command.Type != protobufs.CommandType_CommandType_Restart {
		t.Fatal("a collector is restarted")
	}
	if r.hasEvent(id, domain.AgentEventBridgeLocked) {
		t.Fatal("nothing is held back from a collector")
	}
}
