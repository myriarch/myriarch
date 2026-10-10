// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/opamp"
	"github.com/myriarch/myriarch/internal/store"
)

// fakeToken registers a token of the fake authenticator and returns its id.
func (r *rig) fakeToken(secret string) uuid.UUID {
	id := uuid.New()
	r.auth.mu.Lock()
	r.auth.tokens[secret] = &authn.AgentCredential{EnrollmentTokenID: &id, Labels: map[string]string{}}
	r.auth.mu.Unlock()
	return id
}

func (r *rig) auditActions() map[string]int {
	r.t.Helper()
	evs, _, err := r.db.Audit().List(context.Background(), store.AuditFilter{Limit: 500})
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range evs {
		out[e.Action]++
	}
	return out
}

func isErrorResponse(m *protobufs.ServerToAgent) bool { return m != nil && m.ErrorResponse != nil }

// AC-05: the instance uid is bound to the credential of its first registration.
func TestInstanceUIDIsBoundToTheCredentialOfItsFirstRegistration(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true})
	idA := r.fakeToken("tokenA")
	r.fakeToken("tokenB")
	id := newAgentID()

	a := r.connect("tokenA", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	if m := a.mustRecv(); isErrorResponse(m) {
		t.Fatalf("legitimate registration: %v", m)
	}
	if ag := r.agent(id); ag.EnrollmentTokenID == nil || *ag.EnrollmentTokenID != idA {
		t.Fatalf("the uid must be bound to token A: %+v", ag.EnrollmentTokenID)
	}

	// another valid credential claims the same uid
	b := r.connect("tokenB", id, capsCollector)
	b.send(firstMessage(id, descOpts{host: "attacker", version: "9.9.9"}))
	m := b.mustRecv()
	if !isErrorResponse(m) || m.ErrorResponse.Type != protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest {
		t.Fatalf("the takeover must be answered with an error response: %v", m)
	}
	b.expectClosed(5 * time.Second)

	ag := r.agent(id)
	if ag.EnrollmentTokenID == nil || *ag.EnrollmentTokenID != idA || ag.Hostname != "edge-1" || ag.Version != "0.162.0" {
		t.Fatalf("binding and data of the agent must be unchanged: %+v", ag)
	}
	// the legitimate connection stays up and keeps working
	a.send(&protobufs.AgentToServer{})
	if m := a.mustRecv(); isErrorResponse(m) {
		t.Fatalf("legitimate connection broken: %v", m)
	}
	if !r.agent(id).IsOnline() {
		t.Fatal("the legitimate agent must stay online")
	}
	r.waitEvent(id, domain.AgentEventIdentityConflict)
	evs, _ := r.db.Agents().ListEvents(context.Background(), id, 50, 0)
	warned := false
	for _, e := range evs {
		if e.Type == domain.AgentEventIdentityConflict && e.Severity == "warning" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("the conflict must be recorded with severity warning")
	}
	eventually(t, "audit entry", 5*time.Second, func() bool { return r.auditActions()["agent.identity_conflict"] >= 1 })
}

func TestHTTPPollWithAnotherCredentialIsRejected(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true})
	r.fakeToken("tokenA")
	r.fakeToken("tokenB")
	id := newAgentID()
	first := firstMessage(id, descOpts{host: "http-1", version: "0.162.0"})
	first.Capabilities = capsCollector
	if m := r.httpPoll(t, first, "tokenA"); isErrorResponse(m) {
		t.Fatalf("registration: %v", m)
	}
	// every poll is checked, also those of an already established pooled session
	m := r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 1}, "tokenB")
	if !isErrorResponse(m) {
		t.Fatalf("a poll with another credential must be rejected: %v", m)
	}
	if m := r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 2}, "tokenA"); isErrorResponse(m) {
		t.Fatalf("the legitimate poll must still work: %v", m)
	}
}

func TestWebSocketConnectionIsPinnedToItsFirstInstanceUID(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true})
	r.fakeToken("tokenA")
	id1, id2 := newAgentID(), newAgentID()
	a := r.connect("tokenA", id1, capsCollector)
	a.send(firstMessage(id1, descOpts{host: "edge-1", version: "0.162.0"}))
	a.mustRecv()

	// the same connection registers a second uid: refused, nothing is stored
	a.send(&protobufs.AgentToServer{InstanceUid: id2[:], SequenceNum: 0, AgentDescription: description(id2, descOpts{host: "ghost", version: "1.0.0"}), Capabilities: capsCollector})
	if m := a.mustRecv(); !isErrorResponse(m) {
		t.Fatalf("a uid switch must be refused: %v", m)
	}
	a.expectClosed(5 * time.Second)
	if r.agentOrNil(id2) != nil {
		t.Fatal("the second uid must not be registered")
	}
}

// AC-05 / token rotation: with opamp.autoRebindOnTokenRotation, once the bound token is revoked an agent that
// presents a new valid token moves over.
func TestAgentMovesToANewTokenWhenTheBoundTokenWasRevoked(t *testing.T) {
	r := newRig(t, rigOpts{realAuth: true, requireTk: true, cfg: func(c *config.OpAMPConfig) { c.AutoRebindOnTokenRotation = true }})
	ctx := context.Background()
	mk := func(name string) (string, uuid.UUID) {
		secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
		id := uuid.New()
		if err := r.db.Identity().CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: id, Name: name, DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)), CreatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
		return secret, id
	}
	secA, idA := mk("old")
	secB, idB := mk("new")
	id := newAgentID()

	a := r.connect(secA, id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	a.mustRecv()
	// token B while A is valid: refused
	b := r.connect(secB, id, capsCollector)
	b.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	if m := b.mustRecv(); !isErrorResponse(m) {
		t.Fatalf("a second valid token must not take over a valid binding: %v", m)
	}
	if err := r.db.Identity().RevokeEnrollmentToken(ctx, idA, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.close()
	eventually(t, "old connection gone", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })

	b2 := r.connect(secB, id, capsCollector)
	b2.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	if m := b2.mustRecv(); isErrorResponse(m) {
		t.Fatalf("with the old token revoked the agent moves to the new one: %v", m)
	}
	if ag := r.agent(id); ag.EnrollmentTokenID == nil || *ag.EnrollmentTokenID != idB {
		t.Fatalf("binding after rotation: %+v", ag.EnrollmentTokenID)
	}
	eventually(t, "rebind audited", 5*time.Second, func() bool { return r.auditActions()["agent.rebind"] == 1 })
}

// AC-05: by default the binding of an instance uid is never moved automatically, not even when the bound token was
// revoked; an admin re-enrolls the agent.
func TestRevokedBindingIsNotMovedWithoutOptIn(t *testing.T) {
	if config.Default().OpAMP.AutoRebindOnTokenRotation {
		t.Fatal("automatic re-binding must be off by default")
	}
	r := newRig(t, rigOpts{realAuth: true, requireTk: true})
	ctx := context.Background()
	mk := func(name string) (string, uuid.UUID) {
		secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
		id := uuid.New()
		if err := r.db.Identity().CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: id, Name: name, DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)), CreatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
		return secret, id
	}
	secA, idA := mk("old")
	secB, _ := mk("new")
	id := newAgentID()
	a := r.connect(secA, id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	a.mustRecv()
	if err := r.db.Identity().RevokeEnrollmentToken(ctx, idA, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.close()
	eventually(t, "old connection gone", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })

	b := r.connect(secB, id, capsCollector)
	b.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	if m := b.mustRecv(); !isErrorResponse(m) {
		t.Fatalf("another token must not take over the binding without an explicit re-enrollment: %v", m)
	}
	if ag := r.agent(id); ag.EnrollmentTokenID == nil || *ag.EnrollmentTokenID != idA {
		t.Fatalf("binding must stay: %+v", ag.EnrollmentTokenID)
	}
	if n := r.auditActions()["agent.rebind"]; n != 0 {
		t.Fatalf("no rebind expected, got %d", n)
	}
}

// AC-03: revocation disconnects the live connections on the replica that holds them.
func TestRevokedEnrollmentTokenDisconnectsLiveAgents(t *testing.T) {
	r := newRig(t, rigOpts{realAuth: true, requireTk: true})
	ctx := context.Background()
	secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	tokID := uuid.New()
	if err := r.db.Identity().CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: tokID, Name: "t", DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	other, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	otherID := uuid.New()
	if err := r.db.Identity().CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: otherID, Name: "other", DisplayPrefix: other[:12], Hash: domain.SHA256([]byte(other)), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	id, bystander := newAgentID(), newAgentID()
	a := r.connect(secret, id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	a.mustRecv()
	c := r.connect(other, bystander, capsCollector)
	c.send(firstMessage(bystander, descOpts{host: "edge-2", version: "0.162.0"}))
	c.mustRecv()

	if err := r.db.Identity().RevokeEnrollmentToken(ctx, tokID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.bus.Publish(ctx, eventbus.EnrollmentTokenRevoked, eventbus.EnrollmentTokenPayload{TokenID: tokID, Reason: "revoked"}); err != nil {
		t.Fatal(err)
	}
	a.expectClosed(5 * time.Second)
	eventually(t, "forced disconnect audited", 5*time.Second, func() bool { return r.auditActions()["agent.disconnect"] >= 1 })
	// reconnecting with the revoked token fails at the handshake (the cache was purged by the event)
	eventually(t, "revoked token refused", 5*time.Second, func() bool {
		_, resp, err := r.dialRaw(secret, id, capsCollector)
		return err != nil && resp != nil && resp.StatusCode == http.StatusUnauthorized
	})
	// agents of other tokens are not affected
	c.send(&protobufs.AgentToServer{})
	if m := c.mustRecv(); isErrorResponse(m) {
		t.Fatalf("bystander: %v", m)
	}
}

// AC-03: without an event (lost notification) the periodic check catches a revoked or expired token.
func TestPeriodicRecheckDisconnectsAgentsOfRevokedTokens(t *testing.T) {
	defer opamp.SetTimeouts(0, 300*time.Millisecond)()
	r := newRig(t, rigOpts{realAuth: true, requireTk: true})
	ctx := context.Background()
	secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	tokID := uuid.New()
	if err := r.db.Identity().CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: tokID, Name: "t", DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	id := newAgentID()
	a := r.connect(secret, id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	a.mustRecv()
	if err := r.db.Identity().RevokeEnrollmentToken(ctx, tokID, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.expectClosed(10 * time.Second)
}

// AC-04: the labels. namespace is the server's; agents cannot set it.
func TestAgentCannotSetServerLabels(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0", extra: []*protobufs.KeyValue{kv("labels.channel", "stable"), kv("site.id", "1001")}}))
	a.mustRecv()
	ag := r.agent(id)
	for k := range ag.NonIdentifyingAttributes {
		if strings.HasPrefix(k, domain.LabelPrefix) {
			t.Fatalf("reserved attribute stored: %s", k)
		}
	}
	if ag.NonIdentifyingAttributes["site.id"] != "1001" {
		t.Fatalf("normal attributes must survive: %v", ag.NonIdentifyingAttributes)
	}
	if v, ok := domain.NewAttributeView(ag).Get("labels.channel"); ok {
		t.Fatalf("labels.channel visible in the attribute view: %q", v)
	}
	eventually(t, "warning event", 5*time.Second, func() bool {
		evs, _ := r.db.Agents().ListEvents(context.Background(), id, 50, 0)
		for _, e := range evs {
			if e.Type == domain.AgentEventDescription && e.Severity == "warning" {
				return true
			}
		}
		return false
	})
}

// AC-06: an updater can only claim collectors that registered with the same credential.
func TestUpdaterCannotAttachToACollectorOfAnotherCredential(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true})
	r.fakeToken("tokenA")
	r.fakeToken("tokenB")
	collector, victim, updater := newAgentID(), newAgentID(), newAgentID()

	c := r.connect("tokenA", collector, capsCollector)
	c.send(firstMessage(collector, descOpts{host: "host1", version: "0.162.0"}))
	c.mustRecv()
	v := r.connect("tokenB", victim, capsCollector)
	v.send(firstMessage(victim, descOpts{host: "host2", version: "0.162.0"}))
	v.mustRecv()

	// an updater with token A claims the collector of token B: refused
	u := r.connect("tokenA", updater, capsUpdater)
	u.send(updaterFirst(updater, victim, []byte{}))
	u.mustRecv()
	if ag := r.agent(victim); ag.Updater != nil {
		t.Fatalf("the victim must not get an updater link: %+v", ag.Updater)
	}
	// its own collector (same token, same host) is linked
	u.send(&protobufs.AgentToServer{AgentDescription: description(updater, descOpts{host: "host1", version: "0.1.0", service: domain.UpdaterServiceName,
		extra: []*protobufs.KeyValue{kv(domain.AttrUpdaterCollectorUID, collector.String())}})})
	u.mustRecv()
	if ag := r.agent(collector); ag.Updater == nil || ag.Updater.AgentID != updater {
		t.Fatalf("the matching collector must be linked: %+v", ag.Updater)
	}
	// even a stored link of another credential or host does not resolve
	if err := r.db.Agents().SetUpdaterLink(context.Background(), updater, victim); err != nil {
		t.Fatal(err)
	}
	if ag := r.agent(victim); ag.Updater != nil {
		t.Fatalf("a link between different credentials must not resolve: %+v", ag.Updater)
	}
}

// AC-08: oversized effective configs are not stored.
func TestOversizedEffectiveConfigIsNotStored(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.MaxEffectiveConfigBytes = 1000 }})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	a.mustRecv()
	before := r.agent(id).EffectiveConfigHash

	big := strings.Repeat("x: y\n", 2000)
	a.send(&protobufs.AgentToServer{EffectiveConfig: &protobufs.EffectiveConfig{ConfigMap: &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{"": {Body: []byte(big)}}}}})
	a.mustRecv()
	if !r.agent(id).EffectiveConfigHash.Equal(before) {
		t.Fatal("an oversized effective config must not be stored")
	}
	eventually(t, "warning event", 5*time.Second, func() bool {
		evs, _ := r.db.Agents().ListEvents(context.Background(), id, 50, 0)
		for _, e := range evs {
			if e.Severity == "warning" && strings.Contains(e.Message, "exceeds the limit") {
				return true
			}
		}
		return false
	})
	// hashes of unreasonable length are ignored
	a.send(&protobufs.AgentToServer{AvailableComponents: &protobufs.AvailableComponents{Hash: make([]byte, 5000)}})
	a.mustRecv()
	if h := r.agent(id).AvailableComponentsHash; len(h) > 64 {
		t.Fatalf("component hash of %d bytes stored", len(h))
	}
}

// AC-10: connection limits.
func TestPerClientIPRateLimit(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.ConnectRatePerIP = 1 }}) // burst 2
	var codes []int
	for i := 0; i < 6; i++ {
		_, resp, err := r.dialRaw("", newAgentID(), capsCollector)
		switch {
		case err == nil:
			codes = append(codes, 101)
		case resp != nil:
			codes = append(codes, resp.StatusCode)
		}
	}
	limited := 0
	for _, c := range codes {
		if c == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited < 3 {
		t.Fatalf("per-IP limit not applied: %v", codes)
	}
	if counter(t, r, "myriarch_opamp_connect_rejected_total", "reason", "rate_limited_ip") < 3 {
		t.Fatal("per-IP rejections must be counted")
	}
}

func TestUnauthenticatedRequestsDoNotExhaustTheReplicaBucket(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true, cfg: func(c *config.OpAMPConfig) { c.ConnectRateLimit = 2; c.ConnectRatePerIP = 10000 }}) // burst 4
	r.fakeToken("good")
	for i := 0; i < 50; i++ {
		if _, resp, err := r.dialRaw("bad", newAgentID(), capsCollector); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bad token %d: %v %v", i, err, resp)
		}
	}
	// the bucket for authenticated connections is untouched
	if _, resp, err := r.dialRaw("good", newAgentID(), capsCollector); err != nil {
		t.Fatalf("the valid agent must get through after a flood of invalid requests: %v %v", err, resp)
	}
}

func TestConnectionsPerTokenAreCapped(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true, cfg: func(c *config.OpAMPConfig) { c.MaxConnectionsPerToken = 2 }})
	r.fakeToken("tokenA")
	r.fakeToken("tokenB")
	for i := 0; i < 2; i++ {
		id := newAgentID()
		a := r.connect("tokenA", id, capsCollector)
		a.send(firstMessage(id, descOpts{host: fmt.Sprintf("h%d", i), version: "0.162.0"}))
		a.mustRecv()
	}
	eventually(t, "two connections counted", 5*time.Second, func() bool { return r.srv.Connections() == 2 })
	if _, resp, err := r.dialRaw("tokenA", newAgentID(), capsCollector); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the third connection of the token must be refused: %v %v", err, resp)
	}
	if _, _, err := r.dialRaw("tokenB", newAgentID(), capsCollector); err != nil {
		t.Fatalf("another token is not affected: %v", err)
	}
}

func TestSilentWebSocketConnectionsAreClosed(t *testing.T) {
	defer opamp.SetTimeouts(300*time.Millisecond, 0)()
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.OfflineAfter = 500 * time.Millisecond }})
	// never sends anything: closed after the first-message timeout
	mute, _, err := r.dialRaw("", newAgentID(), capsCollector)
	if err != nil {
		t.Fatal(err)
	}
	mute.expectClosed(5 * time.Second)

	// sends one message and then falls silent: closed after twice offlineAfter
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "edge-1", version: "0.162.0"}))
	a.mustRecv()
	a.expectClosed(10 * time.Second)

	// a heartbeating agent stays connected
	id2 := newAgentID()
	b := r.connect("", id2, capsCollector)
	b.send(firstMessage(id2, descOpts{host: "edge-2", version: "0.162.0"}))
	b.mustRecv()
	for i := 0; i < 6; i++ {
		time.Sleep(300 * time.Millisecond)
		b.send(&protobufs.AgentToServer{})
		b.mustRecv()
	}
}

func TestPooledHTTPSessionsAreBounded(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.MaxConnectionsPerReplica = 16 }})
	for i := 0; i < 60; i++ {
		id := newAgentID()
		r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 0}, "")
	}
	if n := r.srv.HTTPSessionCount(); n > 16 {
		t.Fatalf("pooled HTTP sessions: %d (limit 16)", n)
	}
}

// AC-07: the message size limit is not weakened by WebSocket compression.
func TestCompressedMessagesCannotExceedTheSizeLimit(t *testing.T) {
	if def := config.Default().OpAMP; def.EnableCompression || def.MaxMessageSize > 4<<20 {
		t.Fatalf("defaults must be safe: compression=%v maxMessageSize=%d", def.EnableCompression, def.MaxMessageSize)
	}
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.MaxMessageSize = 16 << 10; c.EnableCompression = false }})
	dialer := websocket.Dialer{EnableCompression: true} // the client offers permessage-deflate
	conn, _, err := dialer.Dial(r.wsURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.EnableWriteCompression(true)
	// 4 MiB of zeros compress to a few KiB, but must be refused: the server did not accept compression
	payload := make([]byte, 4<<20)
	if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte{0}, payload...)); err != nil {
		t.Logf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, rerr := conn.ReadMessage()
	var ne net.Error
	if rerr == nil || (errors.As(rerr, &ne) && ne.Timeout()) {
		t.Fatalf("the oversized message must close the connection (read: %v)", rerr)
	}
	if r.srv.Connections() != 0 {
		eventually(t, "connection gone", 5*time.Second, func() bool { return r.srv.Connections() == 0 })
	}
}

type mintingAuthorizer struct {
	n   atomic.Int64
	ttl time.Duration
}

func (m *mintingAuthorizer) DownloadHeaders(url string, agent domain.AgentID) (map[string]string, error) {
	return map[string]string{"Authorization": fmt.Sprintf("Bearer minted-%d", m.n.Add(1))}, nil
}
func (m *mintingAuthorizer) DownloadTokenTTL() time.Duration { return m.ttl }

func downloadHeader(m *protobufs.ServerToAgent) string {
	for _, p := range m.GetPackagesAvailable().GetPackages() {
		for _, h := range p.GetFile().GetHeaders().GetHeaders() {
			if h.Key == "Authorization" {
				return h.Value
			}
		}
	}
	return ""
}

// AC-15: download tokens are minted when an offer is sent (not stored in the offer) and renewed before they expire.
func TestDownloadTokensAreMintedWhenOffersAreSentAndRenewed(t *testing.T) {
	auth := &mintingAuthorizer{ttl: 800 * time.Millisecond}
	r := newRig(t, rigOpts{downloads: auth})
	ctx := context.Background()
	collector, updater := newAgentID(), newAgentID()
	u := r.connect("", updater, capsUpdater)
	u.send(updaterFirst(updater, collector, []byte{}))
	u.mustRecv()

	offer := domain.PackageOffer{Name: domain.CollectorPackageName, Version: "v0.163.0", DownloadURL: "https://fleet.example.com/api/v1/artifacts/abc/content/x.tar.gz",
		ContentHash: domain.SHA256([]byte("archive")), Signature: []byte(`{"bundle":true}`)}
	if err := r.db.Packages().SetOffers(ctx, updater, []domain.PackageOffer{offer}); err != nil {
		t.Fatal(err)
	}
	if err := r.ctl.DesiredStateChanged(ctx, []domain.AgentID{updater}); err != nil {
		t.Fatal(err)
	}
	m := u.mustRecv()
	if got := downloadHeader(m); got != "Bearer minted-1" {
		t.Fatalf("the token must be minted when the offer is sent: %q", got)
	}
	stored, _ := r.db.Packages().GetOffers(ctx, updater)
	if len(stored) != 1 || len(stored[0].Headers) != 0 {
		t.Fatalf("the stored offer must not contain a token: %+v", stored)
	}
	// the updater is downloading (reported the offered hash, nothing installed yet)
	report := func() *protobufs.AgentToServer {
		return &protobufs.AgentToServer{PackageStatuses: &protobufs.PackageStatuses{ServerProvidedAllPackagesHash: m.PackagesAvailable.AllPackagesHash, Packages: map[string]*protobufs.PackageStatus{
			"": {Name: "", AgentHasVersion: "v0.162.0", ServerOfferedVersion: "v0.163.0", Status: protobufs.PackageStatusEnum_PackageStatusEnum_Downloading}}}}
	}
	u.send(report())
	if m2 := u.mustRecv(); m2.PackagesAvailable != nil {
		t.Fatal("no re-offer while the token is fresh")
	}
	// half of the token lifetime later the unfinished offer is sent again with a new token (same package hash)
	time.Sleep(500 * time.Millisecond)
	u.send(&protobufs.AgentToServer{})
	m3 := u.mustRecv()
	if got := downloadHeader(m3); got != "Bearer minted-2" {
		t.Fatalf("expected a renewed token, got %q (%v)", got, m3)
	}
	if !domain.Hash(m3.PackagesAvailable.AllPackagesHash).Equal(m.PackagesAvailable.AllPackagesHash) {
		t.Fatal("the package hash must not change with the token (headers are not part of it)")
	}
	// an installed package is not offered again
	done := report()
	done.PackageStatuses.Packages[""].AgentHasVersion = "v0.163.0"
	done.PackageStatuses.Packages[""].Status = protobufs.PackageStatusEnum_PackageStatusEnum_Installed
	u.send(done)
	u.mustRecv()
	time.Sleep(500 * time.Millisecond)
	u.send(&protobufs.AgentToServer{})
	if m4 := u.mustRecv(); m4.PackagesAvailable != nil {
		t.Fatal("completed offers are not renewed")
	}
}
