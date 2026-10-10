// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// registerApplied connects a collector, registers it and applies the bootstrap config.
func registerApplied(t *testing.T, r *rig, host string) (*rawAgent, domain.AgentID, []byte) {
	t.Helper()
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: host, version: "0.162.0"}))
	hash, _ := remoteBody(t, a.mustRecv())
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""))
	a.mustRecv()
	return a, id, hash
}

// newDesired renders a new config, assigns it and makes it the desired one (what
// the rollout engine does) and notifies the OpAMP layer.
func newDesired(t *testing.T, r *rig, id domain.AgentID, body string) domain.Hash {
	t.Helper()
	ctx := context.Background()
	hash := domain.ConfigHash([]byte(body))
	if err := r.db.Configs().PutRendered(ctx, domain.RenderedConfig{Hash: hash, Body: []byte(body), FleetName: "edge-nl", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Agents().SetAssignment(ctx, id, store.AgentAssignmentUpdate{FleetName: "edge-nl", AssignedConfigHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Agents().SetDesiredConfig(ctx, []domain.AgentID{id}, hash); err != nil {
		t.Fatal(err)
	}
	if err := r.ctl.DesiredStateChanged(ctx, []domain.AgentID{id}); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestPushOfNewDesiredConfigToConnectedAgent(t *testing.T) {
	r := newRig(t, rigOpts{})
	a, id, oldHash := registerApplied(t, r, "edge-1")

	hash := newDesired(t, r, id, "service:\n  pipelines: {}\n# v2\n")
	m := a.mustRecv() // pushed without any incoming message
	gotHash, body := remoteBody(t, m)
	if string(gotHash) != string(hash) || body != "service:\n  pipelines: {}\n# v2\n" || string(gotHash) == string(oldHash) {
		t.Fatalf("pushed config: hash=%x body=%q", gotHash, body)
	}
	if m.Flags != 0 || m.PackagesAvailable != nil || m.Command != nil {
		t.Fatalf("a config push carries nothing else: %v", m)
	}

	// no duplicate push for the same event; the agent applies
	a.expectSilence(300 * time.Millisecond)
	a.send(statusMsg(gotHash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("applied: no offer")
	}
	eventually(t, "in sync", 5*time.Second, func() bool { return r.agent(id).ConfigSyncState() == domain.ConfigSyncInSync })

	// DesiredStateChanged for an agent whose state did not change pushes nothing
	_ = r.ctl.DesiredStateChanged(context.Background(), []domain.AgentID{id})
	a.expectSilence(500 * time.Millisecond)
}

func TestFailedConfigIsNotReofferedAndRollbackReportsOldHash(t *testing.T) {
	r := newRig(t, rigOpts{})
	a, id, oldHash := registerApplied(t, r, "edge-1")

	bad := newDesired(t, r, id, "this: is\n  not: valid collector config\n")
	m := a.mustRecv()
	hash, _ := remoteBody(t, m)
	if string(hash) != string(bad) {
		t.Fatal("bad config must be pushed once")
	}
	// the supervisor: APPLYING, then FAILED (invalid config), then (automatic_config_rollback) APPLIED for the OLD hash
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING, ""))
	a.mustRecv()
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "yaml: unmarshal errors"))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("a config the agent reported FAILED for must not be offered in the same reply")
	}
	ag := r.agent(id)
	if !ag.Assignment.FailedConfigHash.Equal(bad) || ag.ConfigSyncState() != domain.ConfigSyncFailed || ag.RemoteConfig.ErrorMessage != "yaml: unmarshal errors" {
		t.Fatalf("failed state: %+v / %s", ag.Assignment, ag.ConfigSyncState())
	}
	r.waitEvent(id, domain.AgentEventConfigFailed)

	a.send(statusMsg(oldHash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("rollback endless loop: the supervisor reports APPLIED for the old hash, the failed desired config must not be re-offered")
	}
	for i := 0; i < 3; i++ {
		a.send(&protobufs.AgentToServer{})
		if m := a.mustRecv(); m.RemoteConfig != nil {
			t.Fatal("heartbeats must not re-offer the failed config")
		}
	}
	if r.agent(id).ConfigSyncState() != domain.ConfigSyncFailed {
		t.Fatal("config sync stays failed until an operator acts")
	}
	// a bus event for the agent does not re-offer either
	_ = r.ctl.DesiredStateChanged(context.Background(), []domain.AgentID{id})
	a.expectSilence(500 * time.Millisecond)

	// a reconnect does not re-offer it (state comes from the database)
	a.close()
	eventually(t, "offline", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })
	b := r.connect("", id, capsCollector)
	b.send(&protobufs.AgentToServer{})
	if m := b.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("failed config re-offered after reconnect")
	}

	// operator "resync": clear the failed hash, then Resync -> re-offered plus ReportFullState
	if err := r.db.Agents().ClearFailedConfig(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := r.ctl.Resync(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	m = b.mustRecv()
	hash, _ = remoteBody(t, m)
	if string(hash) != string(bad) || m.Flags&flagFullState == 0 {
		t.Fatalf("resync must re-offer the desired config and request the full state: flags=%d", m.Flags)
	}
	r.waitEvent(id, domain.AgentEventCommand)
}

// With validate_config the supervisor keeps the old config after a failed
// validation: it reports FAILED for the new hash and nothing after that. When
// an operator retries (the failed marker is cleared, desired is unchanged) the
// config must be offered again, and a repeated failure must set the marker
// again instead of looping.
func TestRetryAfterValidationFailureOffersTheConfigAgain(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	a, id, _ := registerApplied(t, r, "edge-1")

	bad := newDesired(t, r, id, "service:\n  pipelines: {}\n# invalid\n")
	hash, _ := remoteBody(t, a.mustRecv())
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "validation failed"))
	a.mustRecv()
	if !r.agent(id).Assignment.FailedConfigHash.Equal(bad) {
		t.Fatal("failed marker must be set")
	}

	// retry-failed: marker cleared, desired unchanged, nothing was reported after FAILED
	if err := r.db.Agents().ClearFailedConfig(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := r.ctl.DesiredStateChanged(ctx, []domain.AgentID{id}); err != nil {
		t.Fatal(err)
	}
	got, _ := remoteBody(t, a.mustRecv())
	if string(got) != string(bad) {
		t.Fatal("the config must be offered again after the failed marker was cleared")
	}
	// the retry fails identically: the marker is set again, no loop
	a.send(statusMsg(got, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "validation failed"))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("a failed retry must not be offered in the same reply")
	}
	eventually(t, "failed marker set again", 5*time.Second, func() bool { return r.agent(id).Assignment.FailedConfigHash.Equal(bad) })
	_ = r.ctl.DesiredStateChanged(ctx, []domain.AgentID{id})
	a.expectSilence(500 * time.Millisecond)

	// resync of an agent that was offline when the operator clicked: the offer
	// comes with the next connection.
	if err := r.db.Agents().ClearFailedConfig(ctx, id); err != nil {
		t.Fatal(err)
	}
	a.close()
	eventually(t, "offline", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })
	b := r.connect("", id, capsCollector)
	b.send(&protobufs.AgentToServer{})
	if got, _ := remoteBody(t, b.mustRecv()); string(got) != string(bad) {
		t.Fatal("the config must be offered after reconnect once the marker was cleared")
	}
}

func TestResyncOfferedEvenWhenAgentReportsDesiredAsApplied(t *testing.T) {
	r := newRig(t, rigOpts{})
	a, id, hash := registerApplied(t, r, "edge-1")
	if err := r.ctl.Resync(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	m := a.mustRecv()
	got, _ := remoteBody(t, m)
	if string(got) != string(hash) || m.Flags&flagFullState == 0 {
		t.Fatalf("resync of an in-sync agent: %v", m)
	}
	// a plain request for the full state sends just the flag
	if err := r.ctl.RequestFullState(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	m = a.mustRecv()
	if m.Flags&flagFullState == 0 || m.RemoteConfig != nil {
		t.Fatalf("RequestFullState: %v", m)
	}
}

func TestOfferIsNotRepeatedWithinGuardWindowWhileAgentIsSilent(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	hash, _ := remoteBody(t, a.mustRecv())
	// the agent does not report anything; heartbeats within 60s must not re-offer the same hash
	for i := 0; i < 5; i++ {
		a.send(&protobufs.AgentToServer{})
		if m := a.mustRecv(); m.RemoteConfig != nil {
			t.Fatalf("offer repeated within the guard window (heartbeat %d)", i)
		}
	}
	// but a different desired hash is offered immediately
	newHash := newDesired(t, r, id, "service: {}\n")
	m := a.mustRecv()
	got, _ := remoteBody(t, m)
	if string(got) == string(hash) || string(got) != string(newHash) {
		t.Fatal("new desired hash must be pushed immediately")
	}
}

func TestEmptyConfigBodyIsNeverOffered(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.assigner.body = func(domain.AgentID) []byte { return nil } // an empty rendered config
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	m := a.mustRecv()
	if m.RemoteConfig != nil {
		t.Fatal("an empty config_map would start the supervisor's no-op pipeline: never send it")
	}
	// missing rendered config (garbage collected / not yet written): no offer either, no crash
	ctx := context.Background()
	missing := domain.SHA256([]byte("missing"))
	if err := r.db.Agents().SetDesiredConfig(ctx, []domain.AgentID{id}, missing); err != nil {
		t.Fatal(err)
	}
	_ = r.ctl.DesiredStateChanged(ctx, []domain.AgentID{id})
	a.expectSilence(500 * time.Millisecond)
	a.send(&protobufs.AgentToServer{})
	if m := a.mustRecv(); m.RemoteConfig != nil || m.ErrorResponse != nil {
		t.Fatalf("%v", m)
	}
}

func TestAgentWithoutRemoteConfigCapabilityGetsNoConfig(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector&^uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig))
	// the description message carries capabilities without AcceptsRemoteConfig -> kind unknown; set desired manually
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()
	newDesired(t, r, id, "service: {}\n")
	a.expectSilence(500 * time.Millisecond)
	a.send(&protobufs.AgentToServer{})
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("capability AcceptsRemoteConfig is required")
	}
}

func TestRestartIsSentAsOwnMessage(t *testing.T) {
	r := newRig(t, rigOpts{})
	a, id, _ := registerApplied(t, r, "edge-1")
	// a pending config change exists at the same time: it must not be mixed into the command message
	newDesired(t, r, id, "service: {}\n# pending\n")
	push := a.mustRecv()
	remoteBody(t, push)

	if err := r.ctl.Restart(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	m := a.mustRecv()
	if m.Command == nil || m.Command.Type != protobufs.CommandType_CommandType_Restart {
		t.Fatalf("expected a restart command, got %v", m)
	}
	if m.RemoteConfig != nil || m.PackagesAvailable != nil || m.Flags != 0 || m.Capabilities != 0 {
		t.Fatalf("a command displaces all other fields; it must be an own message: %v", m)
	}
	r.waitEvent(id, domain.AgentEventCommand)
	evs, _ := r.db.Agents().ListEvents(context.Background(), id, 50, 0)
	found := false
	for _, e := range evs {
		if e.Type == domain.AgentEventCommand && e.Details["command"] == "restart" && e.Details["requestedBy"] == "user:1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restart event with requester missing: %+v", evs)
	}
}

func TestRestartNotSupportedIsIgnoredAndRecorded(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	caps := capsCollector &^ uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand)
	a := r.connect("", id, caps)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()
	if err := r.ctl.Restart(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	a.expectSilence(500 * time.Millisecond)
	eventually(t, "warning event", 5*time.Second, func() bool {
		evs, _ := r.db.Agents().ListEvents(context.Background(), id, 50, 0)
		for _, e := range evs {
			if e.Type == domain.AgentEventCommand && e.Severity == "warning" {
				return true
			}
		}
		return false
	})
}

func TestCommandsForUnconnectedAgentsAreNoOps(t *testing.T) {
	r := newRig(t, rigOpts{})
	ghost := newAgentID()
	for _, f := range []func() error{
		func() error { return r.ctl.Restart(context.Background(), ghost, "u") },
		func() error { return r.ctl.Resync(context.Background(), ghost, "u") },
		func() error { return r.ctl.RequestFullState(context.Background(), ghost, "u") },
		func() error { return r.ctl.DesiredStateChanged(context.Background(), []domain.AgentID{ghost}) },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if r.srv.Connections() != 0 {
		t.Fatal("no connections expected")
	}
}

// TestResyncClearsFailedHashEvenIfCallerDidNot: control.Resync alone (without the REST
// handler clearing the failed hash first) must re-offer a failed config.
func TestResyncClearsFailedHashEvenIfCallerDidNot(t *testing.T) {
	r := newRig(t, rigOpts{})
	a, id, _ := registerApplied(t, r, "edge-1")
	bad := newDesired(t, r, id, "bad: config\n")
	hash, _ := remoteBody(t, a.mustRecv())
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "nope"))
	a.mustRecv()
	if r.agent(id).ConfigSyncState() != domain.ConfigSyncFailed {
		t.Fatal("precondition: failed")
	}
	if err := r.ctl.Resync(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	got, _ := remoteBody(t, a.mustRecv())
	if string(got) != string(bad) {
		t.Fatal("resync must re-offer the failed config")
	}
	if !r.agent(id).Assignment.FailedConfigHash.IsZero() {
		t.Fatal("failed hash must be cleared by the resync")
	}
}
