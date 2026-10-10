// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
)

func TestRegistrationAndConfigOffer(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true})
	tokenID := uuid.New()
	r.auth.tokens["myre_sites"] = &authn.AgentCredential{EnrollmentTokenID: &tokenID, Labels: map[string]string{"channel": "edge-nl"}}
	id := newAgentID()
	ch, cancelSub := r.bus.Subscribe(eventbus.AgentStateChanged)
	defer cancelSub()

	a := r.connect("myre_sites", id, capsCollector)

	// 1. unknown agent, message without description: only ask for the full state, store nothing
	a.send(&protobufs.AgentToServer{})
	m := a.mustRecv()
	if m.Flags&flagFullState == 0 {
		t.Fatalf("unknown agent without description must get ReportFullState, got flags %d", m.Flags)
	}
	if m.Capabilities == 0 || m.RemoteConfig != nil {
		t.Fatalf("capabilities must always be set, no config for unknown agents: %v", m)
	}
	if string(m.InstanceUid) != string(id[:]) {
		t.Fatal("reply must carry the agent's instance uid")
	}
	if r.agentOrNil(id) != nil {
		t.Fatal("nothing must be stored for an unknown agent without description")
	}
	if r.assigner.calls.Load() != 0 {
		t.Fatal("no assignment without description")
	}

	// 2. the full state registers the agent, assigns synchronously and answers with the config
	a.send(firstMessage(id, descOpts{host: "edge-1001", version: "v0.162.0", os: "windows", siteType: "edge", siteID: "1001"}))
	m = a.mustRecv()
	hash, body := remoteBody(t, m)
	if m.Flags&flagFullState != 0 {
		t.Fatal("agent just sent its full state, no ReportFullState expected")
	}
	if m.Flags&flagComponents == 0 {
		t.Fatalf("unknown available components hash must trigger ReportAvailableComponents (flags %d)", m.Flags)
	}
	if m.Capabilities&uint64(protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig) == 0 ||
		m.Capabilities&uint64(protobufs.ServerCapabilities_ServerCapabilities_OffersPackages) == 0 ||
		m.Capabilities&uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus) == 0 ||
		m.Capabilities&uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsEffectiveConfig) == 0 ||
		m.Capabilities&uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsPackagesStatus) == 0 {
		t.Fatalf("server capabilities incomplete: %b", m.Capabilities)
	}
	if !domain.ConfigHash([]byte(body)).Equal(hash) {
		t.Fatal("config_hash must be the SHA-256 of the body")
	}
	if r.assigner.calls.Load() != 1 {
		t.Fatalf("assigner calls = %d, want 1", r.assigner.calls.Load())
	}

	ag := r.agent(id)
	if ag.Kind != domain.AgentKindCollector || ag.Version != "0.162.0" || ag.Hostname != "edge-1001" || ag.OSType != "windows" {
		t.Fatalf("registered agent: %+v", ag)
	}
	if ag.Labels["channel"] != "edge-nl" || ag.EnrollmentTokenID == nil || *ag.EnrollmentTokenID != tokenID {
		t.Fatalf("labels/token from the credential: %+v", ag)
	}
	if ag.NonIdentifyingAttributes["site.id"] != "1001" || ag.IdentifyingAttributes["service.name"] != "otelcol-contrib" {
		t.Fatalf("attributes: %+v %+v", ag.IdentifyingAttributes, ag.NonIdentifyingAttributes)
	}
	if !ag.IsOnline() || ag.Connection.ReplicaID != r.replica || ag.Connection.Transport != "websocket" || ag.Connection.ConnectedAt == nil {
		t.Fatalf("connection: %+v", ag.Connection)
	}
	if ag.Capabilities != capsCollector {
		t.Fatalf("capabilities %d", ag.Capabilities)
	}
	if !ag.Assignment.DesiredConfigHash.Equal(hash) || ag.Assignment.FleetName != "edge-nl" {
		t.Fatalf("assignment: %+v", ag.Assignment)
	}
	if ag.Health == nil || !ag.Health.Healthy || ag.Health.Components["pipeline:metrics"].Components["receiver:otlp"] == nil {
		t.Fatalf("health tree: %+v", ag.Health)
	}
	if ag.EffectiveConfigHash.IsZero() {
		t.Fatal("effective config must be stored")
	}
	eff, err := r.db.Configs().GetEffective(context.Background(), ag.EffectiveConfigHash)
	if err != nil || string(eff.Body) != "supervisor: config\n" || eff.ContentType != "text/yaml" {
		t.Fatalf("effective config (empty content type must be stored as text/yaml): %+v %v", eff, err)
	}

	// the events are visible on the bus (UI live updates) ...
	var sawCreated bool
	deadline := time.After(5 * time.Second)
	for !sawCreated {
		select {
		case ev := <-ch:
			var p eventbus.AgentStateChangedPayload
			_ = ev.Decode(&p)
			if p.AgentID == id {
				for _, c := range p.Changes {
					if c == "created" {
						sawCreated = true
					}
				}
			}
		case <-deadline:
			t.Fatal("no agent.state_changed with change \"created\"")
		}
	}
	// ... and in the agent history
	r.waitEvent(id, domain.AgentEventRegistered)
	r.waitEvent(id, domain.AgentEventConnected)
	r.waitEvent(id, domain.AgentEventConfigOffered)

	// 3. the agent applies the config: APPLYING -> APPLIED
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING, ""))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("no re-offer while the agent reports APPLYING for the desired hash")
	}
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""))
	a.mustRecv()
	ag = r.agent(id)
	if ag.RemoteConfig.Status != domain.RemoteConfigApplied || !ag.RemoteConfig.LastHash.Equal(hash) || ag.ConfigSyncState() != domain.ConfigSyncInSync {
		t.Fatalf("config sync after APPLIED: %s (%+v)", ag.ConfigSyncState(), ag.RemoteConfig)
	}
	r.waitEvent(id, domain.AgentEventConfigApplied)

	// 4. available components: send the details once, then it is known
	a.send(&protobufs.AgentToServer{AvailableComponents: &protobufs.AvailableComponents{Hash: []byte("components-hash-v1"), Components: map[string]*protobufs.ComponentDetails{
		"receivers": {SubComponentMap: map[string]*protobufs.ComponentDetails{
			"otlp":         {Metadata: []*protobufs.KeyValue{kv("code.namespace", "go.opentelemetry.io/collector/receiver/otlpreceiver v0.162.0")}},
			"host_metrics": {Metadata: []*protobufs.KeyValue{kv("code.namespace", "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/hostmetricsreceiver v0.162.0"), kv("extra", "x")}},
		}},
		"exporters": {SubComponentMap: map[string]*protobufs.ComponentDetails{"debug": {}}},
	}}})
	if m := a.mustRecv(); m.Flags&flagComponents != 0 {
		t.Fatal("no further component request once the set was delivered")
	}
	ag = r.agent(id)
	cs, err := r.db.Configs().GetComponentSet(context.Background(), ag.AvailableComponentsHash)
	if err != nil || string(ag.AvailableComponentsHash) != "components-hash-v1" {
		t.Fatalf("component set: %v %v", cs, err)
	}
	if cs.Components["receivers"]["otlp"].Module != "go.opentelemetry.io/collector/receiver/otlpreceiver v0.162.0" ||
		cs.Components["receivers"]["host_metrics"].Metadata["extra"] != "x" || len(cs.Components["exporters"]) != 1 {
		t.Fatalf("components: %+v", cs.Components)
	}
	// the registration validated against an unknown (empty) set: the arrival of
	// the real component set re-runs the assignment exactly once
	if r.assigner.calls.Load() != 2 {
		t.Fatalf("a changed component set must re-run the assigner once: %d calls", r.assigner.calls.Load())
	}

	// 5. heartbeats (empty messages with sequential numbers) cause no flags, no offers and no writes
	for i := 0; i < 3; i++ {
		a.send(&protobufs.AgentToServer{})
		m := a.mustRecv()
		if m.Flags != 0 || m.RemoteConfig != nil || m.PackagesAvailable != nil || m.Command != nil || m.Capabilities == 0 {
			t.Fatalf("heartbeat reply: %v", m)
		}
	}
	if r.assigner.calls.Load() != 2 {
		t.Fatal("the assigner must not run again for heartbeats")
	}
}

func TestSecondAgentWithKnownComponentSetIsNotAskedAgain(t *testing.T) {
	r := newRig(t, rigOpts{})
	a1, a2 := newAgentID(), newAgentID()
	c1 := r.connect("", a1, capsCollector)
	c1.send(firstMessage(a1, descOpts{host: "h1", version: "0.162.0"}))
	if m := c1.mustRecv(); m.Flags&flagComponents == 0 {
		t.Fatal("first agent: set unknown")
	}
	c1.send(&protobufs.AgentToServer{AvailableComponents: &protobufs.AvailableComponents{Hash: []byte("components-hash-v1"),
		Components: map[string]*protobufs.ComponentDetails{"receivers": {SubComponentMap: map[string]*protobufs.ComponentDetails{"otlp": {}}}}}})
	c1.mustRecv()

	c2 := r.connect("", a2, capsCollector)
	c2.send(firstMessage(a2, descOpts{host: "h2", version: "0.162.0"}))
	if m := c2.mustRecv(); m.Flags&flagComponents != 0 {
		t.Fatal("a component set that is already stored must not be requested again")
	}
	eventually(t, "components hash recorded for the second agent", 5*time.Second, func() bool {
		return string(r.agent(a2).AvailableComponentsHash) == "components-hash-v1"
	})
}

func TestReconnectWithoutDescriptionRequestsFullState(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()
	r.waitEvent(id, domain.AgentEventConnected)
	a.close()
	eventually(t, "offline after close", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })
	r.waitEvent(id, domain.AgentEventDisconnected)
	ag := r.agent(id)
	if ag.Connection.ReplicaID != "" || ag.Connection.DisconnectedAt == nil {
		t.Fatalf("offline state: %+v", ag.Connection)
	}

	// the websocket client sends an (almost) empty message after a reconnect
	b := r.connect("", id, capsCollector)
	b.seq = 7 // the client keeps counting across reconnects
	b.send(&protobufs.AgentToServer{})
	m := b.mustRecv()
	if m.Flags&flagFullState == 0 {
		t.Fatalf("first message of a new connection without description must trigger ReportFullState, flags=%d", m.Flags)
	}
	eventually(t, "online again", 5*time.Second, func() bool { return r.agent(id).IsOnline() })
	eventually(t, "second connected event", 5*time.Second, func() bool { return r.countEvents(id, domain.AgentEventConnected) == 2 })
	// the full state that follows does not re-run the assignment: nothing changed
	b.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	b.mustRecv()
	if r.assigner.calls.Load() != 1 {
		t.Fatalf("assigner calls = %d, want 1 (unchanged description)", r.assigner.calls.Load())
	}
}

func TestSequenceGapRequestsFullState(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()
	a.send(&protobufs.AgentToServer{})
	if m := a.mustRecv(); m.Flags&flagFullState != 0 {
		t.Fatal("sequential heartbeat must not request full state")
	}
	a.seq += 5 // lost messages
	a.send(&protobufs.AgentToServer{})
	if m := a.mustRecv(); m.Flags&flagFullState == 0 {
		t.Fatal("a sequence number jump must request the full state")
	}
	a.send(&protobufs.AgentToServer{})
	if m := a.mustRecv(); m.Flags&flagFullState != 0 {
		t.Fatal("the request is not repeated once the sequence is continuous again")
	}
}

func TestDescriptionChangesVersionHealthEvents(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.161.0"}))
	a.mustRecv()
	if r.assigner.calls.Load() != 1 {
		t.Fatal("assigner on registration")
	}

	// same attributes, new version (collector upgraded and restarted by the supervisor)
	a.send(&protobufs.AgentToServer{AgentDescription: description(id, descOpts{host: "h", version: "0.162.0"})})
	a.mustRecv()
	if r.agent(id).Version != "0.162.0" {
		t.Fatal("version not updated")
	}
	r.waitEvent(id, domain.AgentEventVersionChanged)
	if r.assigner.calls.Load() != 2 {
		// service.version is an identifying attribute (selectors and templates can use it): reassign
		t.Fatalf("a version change changes the attribute view and must re-run the assigner: %d calls", r.assigner.calls.Load())
	}

	// changed attributes: description event + reassignment
	a.send(&protobufs.AgentToServer{AgentDescription: description(id, descOpts{host: "h", version: "0.162.0", siteType: "edge", siteID: "1001"})})
	a.mustRecv()
	r.waitEvent(id, domain.AgentEventDescription)
	if r.assigner.calls.Load() != 3 {
		t.Fatalf("changed attributes must trigger the assigner: %d calls", r.assigner.calls.Load())
	}

	// health: healthy -> unhealthy -> healthy
	unhealthy := &protobufs.ComponentHealth{Healthy: false, Status: "StatusStopping", LastError: "collector stopping"}
	a.send(&protobufs.AgentToServer{Health: unhealthy})
	a.mustRecv()
	eventually(t, "unhealthy stored", 5*time.Second, func() bool { h := r.agent(id).Health; return h != nil && !h.Healthy })
	if r.agent(id).Health.LastError != "collector stopping" {
		t.Fatalf("health: %+v", r.agent(id).Health)
	}
	r.waitEvent(id, domain.AgentEventHealthChanged)
	n := r.countEvents(id, domain.AgentEventHealthChanged)
	a.send(&protobufs.AgentToServer{Health: unhealthy}) // unchanged: no write, no event
	a.mustRecv()
	a.send(&protobufs.AgentToServer{Health: healthy()})
	a.mustRecv()
	eventually(t, "healthy again", 5*time.Second, func() bool { return r.agent(id).Health.Healthy })
	eventually(t, "second health event", 5*time.Second, func() bool { return r.countEvents(id, domain.AgentEventHealthChanged) == n+1 })
}

func TestInvalidInstanceUID(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.connect("", newAgentID(), capsCollector)
	for name, uid := range map[string][]byte{"15 bytes": make([]byte, 15), "empty": nil, "17 bytes": make([]byte, 17), "nil uuid": make([]byte, 16)} {
		a.sendRaw(&protobufs.AgentToServer{InstanceUid: uid, SequenceNum: 0})
		m := a.mustRecv()
		if m.ErrorResponse == nil || m.ErrorResponse.Type != protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest || m.ErrorResponse.ErrorMessage == "" {
			t.Errorf("%s: expected BadRequest error response, got %v", name, m)
		}
	}
	// the connection stays usable
	id := newAgentID()
	a.id = id
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	if m := a.mustRecv(); m.ErrorResponse != nil {
		t.Fatalf("valid message after errors: %v", m)
	}
}

func TestAssignerErrorIsTolerated(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.assigner.err = errors.New("assignment.Service.AssignAgent: not implemented")
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	m := a.mustRecv()
	if m.ErrorResponse != nil || m.RemoteConfig != nil {
		t.Fatalf("agent stays unassigned, connection OK: %v", m)
	}
	if r.assigner.calls.Load() != 1 {
		t.Fatal("assigner must have been called")
	}
	ag := r.agent(id)
	if !ag.IsOnline() || ag.Assignment.FleetName != "" || !ag.Assignment.DesiredConfigHash.IsZero() {
		t.Fatalf("unassigned agent: %+v", ag)
	}
	// heartbeats keep working; the assignment is retried at the next description-bearing message
	a.send(&protobufs.AgentToServer{})
	a.mustRecv()
	r.assigner.err = nil
	a.send(&protobufs.AgentToServer{AgentDescription: description(id, descOpts{host: "h", version: "0.162.0"})})
	m = a.mustRecv()
	if _, _ = remoteBody(t, m); r.agent(id).Assignment.FleetName != "edge-nl" {
		t.Fatal("unassigned collectors are assigned when a description arrives")
	}
}

func TestAssignerInfrastructureErrorDoesNotDropConnection(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.assigner.err = context.DeadlineExceeded
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	if m := a.mustRecv(); m.ErrorResponse != nil {
		t.Fatalf("%v", m)
	}
	a.send(&protobufs.AgentToServer{})
	a.mustRecv()
	if !r.agent(id).IsOnline() {
		t.Fatal("agent must stay connected")
	}
}

func TestUnknownKindWithoutRemoteConfigCapabilityIsNotAssigned(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus))
	a.send(firstMessage(id, descOpts{host: "other", version: "1.0.0", service: "some-agent"}))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("agents without AcceptsRemoteConfig never get a config")
	}
	if ag := r.agent(id); ag.Kind != domain.AgentKindUnknown {
		t.Fatalf("kind = %s", ag.Kind)
	}
	if r.assigner.calls.Load() != 0 {
		t.Fatal("only collectors are assigned")
	}
}

func TestAttributeSanitizing(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	msg := firstMessage(id, descOpts{host: "bad\x00host", version: "0.162.0", extra: []*protobufs.KeyValue{
		kv("evil\x00key", "v\x00al"),
		{Key: "num", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_IntValue{IntValue: 42}}},
		{Key: "flag", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_BoolValue{BoolValue: true}}},
		{Key: "ratio", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_DoubleValue{DoubleValue: 0.1}}},
		{Key: "blob", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_BytesValue{BytesValue: []byte("hi")}}},
		{Key: "list", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_ArrayValue{ArrayValue: &protobufs.ArrayValue{Values: []*protobufs.AnyValue{
			{Value: &protobufs.AnyValue_StringValue{StringValue: "a"}}, {Value: &protobufs.AnyValue_IntValue{IntValue: 2}}}}}}},
		{Key: "map", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_KvlistValue{KvlistValue: &protobufs.KeyValueList{Values: []*protobufs.KeyValue{kv("k", "v")}}}}},
	}})
	a.send(msg)
	if m := a.mustRecv(); m.ErrorResponse != nil {
		t.Fatalf("hostile attributes must not break registration: %v", m)
	}
	ag := r.agent(id)
	if ag.Hostname != "badhost" {
		t.Fatalf("NUL stripped from hostname: %q", ag.Hostname)
	}
	want := map[string]string{"evilkey": "val", "num": "42", "flag": "true", "ratio": "0.1", "blob": "aGk=", "list": `["a",2]`, "map": `{"k":"v"}`}
	for k, v := range want {
		if got := ag.NonIdentifyingAttributes[k]; got != v {
			t.Errorf("attribute %s = %q, want %q (conversion rules of docs/concepts/agents.md, \"The attribute view\")", k, got, v)
		}
	}
}
