// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// Tests of the stale APPLYING rule (docs/design/opamp-server.md, "Stale
// APPLYING after a reconnect"): a supervisor that restarts while it applies a
// config reports the stored APPLYING again after it reconnects, and the server
// offers the config once more when that has not changed for
// opamp.staleApplyingAfter.

const (
	staleAfter = 90 * time.Second

	rcsApplying = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING
	rcsApplied  = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED
	rcsUnset    = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_UNSET
)

// staleClock is the clock of the servers of a test (Deps.Now). The servers share
// it, so that replicas agree on the time.
type staleClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStaleClock() *staleClock {
	return &staleClock{t: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)}
}

func (c *staleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *staleClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// staleRig is a rig with the fake clock and the given threshold.
func staleRig(t *testing.T, clk *staleClock, after time.Duration, o rigOpts) *rig {
	t.Helper()
	o.now = clk.Now
	o.cfg = func(c *config.OpAMPConfig) { c.StaleApplyingAfter = after }
	return newRig(t, o)
}

// registerStaleApplying registers a collector, takes the offered config and reports
// APPLYING for it, as a supervisor does while it applies. The connection made
// the offer itself.
func registerStaleApplying(t *testing.T, r *rig, host string) (*rawAgent, domain.AgentID, []byte) {
	t.Helper()
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: host, version: "0.162.0"}))
	hash, _ := remoteBody(t, a.mustRecv())
	a.send(statusMsg(hash, rcsApplying, ""))
	if m := a.mustRecv(); m.RemoteConfig != nil {
		t.Fatal("no offer while the agent reports APPLYING for the desired hash")
	}
	if st := r.agent(id).RemoteConfig; st.Status != domain.RemoteConfigApplying || !st.LastHash.Equal(hash) {
		t.Fatalf("stored status: %+v", st)
	}
	return a, id, hash
}

// staleFullState is the message of a restarted supervisor after the server asked for
// the full state: the description and the stored remote config status.
func staleFullState(id domain.AgentID, host string, status protobufs.RemoteConfigStatuses, hash []byte) *protobufs.AgentToServer {
	return &protobufs.AgentToServer{
		InstanceUid: id[:], AgentDescription: description(id, descOpts{host: host, version: "0.162.0"}), Health: healthy(),
		RemoteConfigStatus: &protobufs.RemoteConfigStatus{LastRemoteConfigHash: hash, Status: status},
	}
}

// restart ends the connection of a, as a restart of the agent does, and connects
// again. The reply to the first (almost empty) message asks for the full state;
// the reply to the full state, which reports status for hash, is returned.
func staleRestart(t *testing.T, r *rig, a *rawAgent, id domain.AgentID, host string, status protobufs.RemoteConfigStatuses, hash []byte) (*rawAgent, *protobufs.ServerToAgent) {
	t.Helper()
	a.close()
	eventually(t, "offline after the restart", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })
	return staleReconnect(t, r, id, host, status, hash)
}

// reconnect opens a new connection of the agent: the almost empty first message
// (the server asks for the full state) and the full state with the status.
func staleReconnect(t *testing.T, r *rig, id domain.AgentID, host string, status protobufs.RemoteConfigStatuses, hash []byte) (*rawAgent, *protobufs.ServerToAgent) {
	t.Helper()
	b := r.connect("", id, capsCollector)
	b.seq = 7 // the client keeps counting across reconnects
	if m := staleHeartbeat(b); m.Flags&flagFullState == 0 || m.RemoteConfig != nil {
		t.Fatalf("the first message of a connection asks for the full state and offers nothing yet: %v", m)
	}
	b.send(staleFullState(id, host, status, hash))
	return b, b.mustRecv()
}

func staleHeartbeat(a *rawAgent) *protobufs.ServerToAgent {
	a.t.Helper()
	a.send(&protobufs.AgentToServer{})
	return a.mustRecv()
}

// staleOfferEvents returns the config_offered events of an agent, oldest first, once
// the event writer has stored n of them.
func staleOfferEvents(t *testing.T, r *rig, id domain.AgentID, n int) []domain.AgentEvent {
	t.Helper()
	var out []domain.AgentEvent
	eventually(t, fmt.Sprintf("%d config_offered events", n), 5*time.Second, func() bool {
		evs, err := r.db.Agents().ListEvents(context.Background(), id, 200, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = out[:0]
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Type == domain.AgentEventConfigOffered {
				out = append(out, evs[i])
			}
		}
		return len(out) >= n
	})
	if len(out) != n {
		t.Fatalf("%d config_offered events, want %d: %+v", len(out), n, out)
	}
	return out
}

func staleReoffers(t *testing.T, r *rig) float64 {
	return counter(t, r, "myriarch_opamp_config_reoffers_total", "reason", "stale_applying")
}

func staleExpectNoOffer(t *testing.T, a *rawAgent, what string) {
	t.Helper()
	if m := staleHeartbeat(a); m.RemoteConfig != nil {
		t.Fatalf("%s: unexpected offer %x", what, m.RemoteConfig.ConfigHash)
	}
}

// ---------------------------------------------------------------------------

func TestStaleApplyingIsOfferedAgainOnceAfterAReconnect(t *testing.T) {
	clk := newStaleClock()
	r := staleRig(t, clk, staleAfter, rigOpts{})
	a, id, hash := registerStaleApplying(t, r, "edge-1")

	// the agent restarts while it applies: the new connection reports the stored status
	clk.Advance(5 * time.Second)
	b, m := staleRestart(t, r, a, id, "edge-1", rcsApplying, hash)
	if m.RemoteConfig != nil {
		t.Fatalf("a status reported just now is not stale: %v", m)
	}

	// just below the threshold, measured from the report on this connection: nothing
	clk.Advance(staleAfter - time.Nanosecond)
	staleExpectNoOffer(t, b, "one nanosecond below the threshold")
	if staleReoffers(t, r) != 0 {
		t.Fatal("no re-offer counted yet")
	}

	// at the threshold: exactly one offer of the desired config
	clk.Advance(time.Nanosecond)
	m = staleHeartbeat(b)
	gotHash, body := remoteBody(t, m)
	if string(gotHash) != string(hash) || !domain.ConfigHash([]byte(body)).Equal(gotHash) {
		t.Fatalf("the desired config is offered again: hash %x body %q", gotHash, body)
	}
	if m.Flags != 0 || m.PackagesAvailable != nil || m.Command != nil {
		t.Fatalf("the offer carries nothing else: %v", m)
	}
	if got := staleReoffers(t, r); got != 1 {
		t.Fatalf("myriarch_opamp_config_reoffers_total{reason=stale_applying} = %v, want 1", got)
	}
	evs := staleOfferEvents(t, r, id, 2)
	first, second := evs[0], evs[1]
	if _, has := first.Details["reason"]; has || first.Severity != "info" {
		t.Fatalf("the first offer is an ordinary one: %+v", first)
	}
	if second.Severity != "warning" || second.Details["reason"] != "stale_applying" || second.Details["hash"] != domain.Hash(hash).String() ||
		second.Details["resync"] != false || !strings.Contains(second.Message, "APPLYING") {
		t.Fatalf("the second offer names the reason: %+v", second)
	}

	// once per connection: neither the next message nor an hour later offers it again
	staleExpectNoOffer(t, b, "next heartbeat")
	clk.Advance(time.Hour)
	staleExpectNoOffer(t, b, "an hour later")
	if got := staleReoffers(t, r); got != 1 {
		t.Fatalf("one re-offer per connection, counted %v", got)
	}

	// the supervisor answers the offer: APPLIED ends it
	b.send(statusMsg(hash, rcsApplied, ""))
	b.mustRecv()
	eventually(t, "in sync", 5*time.Second, func() bool { return r.agent(id).ConfigSyncState() == domain.ConfigSyncInSync })
	staleExpectNoOffer(t, b, "after APPLIED")
	if len(staleOfferEvents(t, r, id, 2)) != 2 {
		t.Fatal("two offers in total")
	}
}

// The window starts with the report on the connection, not with the time the
// status was stored: an agent that comes back after a long time is not offered
// the config before it has told its current status.
func TestStaleApplyingWindowStartsWithTheReportOnTheConnection(t *testing.T) {
	clk := newStaleClock()
	r := staleRig(t, clk, staleAfter, rigOpts{})
	a, id, hash := registerStaleApplying(t, r, "edge-1")

	a.close()
	eventually(t, "offline", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })
	clk.Advance(10 * time.Minute) // the stored status is ten minutes old
	b, m := staleReconnect(t, r, id, "edge-1", rcsApplying, hash)
	if m.RemoteConfig != nil {
		t.Fatalf("an old stored status alone offers nothing: %v", m)
	}
	clk.Advance(staleAfter - time.Nanosecond)
	staleExpectNoOffer(t, b, "below the threshold of the new connection")
	clk.Advance(time.Nanosecond)
	if m := staleHeartbeat(b); m.RemoteConfig == nil {
		t.Fatal("offered once the status stayed unchanged on this connection")
	}
	if staleReoffers(t, r) != 1 {
		t.Fatal("one re-offer")
	}
}

// An APPLYING that this connection caused by its own offer is bounded by the
// applyTimeout of the rollout, not by this rule.
func TestStaleApplyingIgnoresAnApplyingThatTheConnectionCaused(t *testing.T) {
	clk := newStaleClock()
	r := staleRig(t, clk, staleAfter, rigOpts{})
	a, id, _ := registerStaleApplying(t, r, "edge-1")

	for _, d := range []time.Duration{time.Second, staleAfter, 10 * time.Minute, time.Hour} {
		clk.Advance(d)
		staleExpectNoOffer(t, a, "own offer, "+d.String()+" later")
	}
	if staleReoffers(t, r) != 0 {
		t.Fatal("no re-offer counted")
	}
	staleOfferEvents(t, r, id, 1)
}

// A restart without a stale status offers nothing extra: an agent that finished
// is not offered the config, and one that lost its status is offered it at once
// as before (not as a re-offer).
func TestStaleApplyingRestartWithoutAStaleStatus(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		clk := newStaleClock()
		r := staleRig(t, clk, staleAfter, rigOpts{})
		a, id, hash := registerApplied(t, r, "edge-1")
		b, m := staleRestart(t, r, a, id, "edge-1", rcsApplied, hash)
		if m.RemoteConfig != nil {
			t.Fatalf("applied hash offered after a restart: %v", m)
		}
		for _, d := range []time.Duration{staleAfter, time.Hour} {
			clk.Advance(d)
			staleExpectNoOffer(t, b, "applied, "+d.String()+" later")
		}
		if staleReoffers(t, r) != 0 {
			t.Fatal("no re-offer counted")
		}
		staleOfferEvents(t, r, id, 1)
	})
	t.Run("lost status", func(t *testing.T) {
		clk := newStaleClock()
		r := staleRig(t, clk, staleAfter, rigOpts{})
		a, id, hash := registerStaleApplying(t, r, "edge-1")
		_, m := staleRestart(t, r, a, id, "edge-1", rcsUnset, nil)
		gotHash, _ := remoteBody(t, m)
		if string(gotHash) != string(hash) {
			t.Fatal("an agent without a status gets the desired config at once")
		}
		evs := staleOfferEvents(t, r, id, 2)
		if _, has := evs[1].Details["reason"]; has || evs[1].Severity != "info" {
			t.Fatalf("an ordinary offer: %+v", evs[1])
		}
		if staleReoffers(t, r) != 0 {
			t.Fatal("no re-offer counted")
		}
	})
	t.Run("a different hash", func(t *testing.T) {
		clk := newStaleClock()
		r := staleRig(t, clk, staleAfter, rigOpts{})
		a, id, hash := registerStaleApplying(t, r, "edge-1")
		other := domain.ConfigHash([]byte("another config"))
		_, m := staleRestart(t, r, a, id, "edge-1", rcsApplying, other)
		if gotHash, _ := remoteBody(t, m); string(gotHash) != string(hash) {
			t.Fatal("an agent that applies another hash is offered the desired one as before")
		}
		if staleReoffers(t, r) != 0 {
			t.Fatal("no re-offer counted")
		}
	})
}

func TestStaleApplyingNeverOffersAFailedHashAndFollowsADesiredChange(t *testing.T) {
	clk := newStaleClock()
	r := staleRig(t, clk, staleAfter, rigOpts{})
	a, id, hash := registerStaleApplying(t, r, "edge-1")

	// the hash is marked failed (an earlier FAILED report, kept in the database)
	if err := r.db.Agents().SetFailedConfig(context.Background(), id, hash); err != nil {
		t.Fatal(err)
	}
	b, m := staleRestart(t, r, a, id, "edge-1", rcsApplying, hash)
	if m.RemoteConfig != nil {
		t.Fatalf("failed hash offered: %v", m)
	}
	for _, d := range []time.Duration{staleAfter, time.Hour} {
		clk.Advance(d)
		staleExpectNoOffer(t, b, "failed hash, "+d.String()+" later")
	}
	if staleReoffers(t, r) != 0 {
		t.Fatal("no re-offer counted")
	}

	// a new desired config is offered as usual
	newHash := newDesired(t, r, id, "service:\n  pipelines: {}\n# v2\n")
	gotHash, _ := remoteBody(t, b.mustRecv())
	if string(gotHash) != string(newHash) {
		t.Fatal("a changed desired config is pushed as an ordinary offer")
	}
	evs := staleOfferEvents(t, r, id, 2)
	if _, has := evs[1].Details["reason"]; has {
		t.Fatalf("an ordinary offer: %+v", evs[1])
	}
}

func TestStaleApplyingCanBeTurnedOff(t *testing.T) {
	clk := newStaleClock()
	r := staleRig(t, clk, 0, rigOpts{})
	a, id, hash := registerStaleApplying(t, r, "edge-1")
	b, m := staleRestart(t, r, a, id, "edge-1", rcsApplying, hash)
	if m.RemoteConfig != nil {
		t.Fatalf("%v", m)
	}
	for _, d := range []time.Duration{staleAfter, time.Hour, 24 * time.Hour} {
		clk.Advance(d)
		staleExpectNoOffer(t, b, "opamp.staleApplyingAfter: 0, "+d.String()+" later")
	}
	if staleReoffers(t, r) != 0 {
		t.Fatal("no re-offer counted")
	}
	staleOfferEvents(t, r, id, 1)
}

// An agent that moves to another replica (the replica died, a rolling update)
// is handled by the session of the new replica, which has not offered anything.
func TestStaleApplyingAfterAMoveToAnotherReplica(t *testing.T) {
	clk := newStaleClock()
	pool := pgtest.NewPool(t)
	a := staleRig(t, clk, staleAfter, rigOpts{pool: pool, replica: "replica-a"})
	b := staleRig(t, clk, staleAfter, rigOpts{pool: pool, replica: "replica-b"})
	ca, id, hash := registerStaleApplying(t, a, "edge-1")

	cb, m := staleRestart(t, b, ca, id, "edge-1", rcsApplying, hash)
	if m.RemoteConfig != nil {
		t.Fatalf("%v", m)
	}
	if got := b.agent(id).Connection.ReplicaID; got != "replica-b" {
		t.Fatalf("connected to %q", got)
	}
	clk.Advance(staleAfter - time.Nanosecond)
	staleExpectNoOffer(t, cb, "below the threshold")
	clk.Advance(time.Nanosecond)
	gotHash, _ := remoteBody(t, staleHeartbeat(cb))
	if string(gotHash) != string(hash) {
		t.Fatal("the new replica offers the desired config again")
	}
	if staleReoffers(t, b) != 1 || staleReoffers(t, a) != 0 {
		t.Fatalf("re-offers: replica b %v, replica a %v", staleReoffers(t, b), staleReoffers(t, a))
	}
	staleExpectNoOffer(t, cb, "once per connection")
}

func TestStaleApplyingOverHTTPPolling(t *testing.T) {
	// httpApplying registers an agent that polls over HTTP and leaves it in
	// APPLYING; the pooled session of the server made the offer itself.
	httpApplying := func(t *testing.T, r *rig, host string) (id domain.AgentID, hash []byte) {
		t.Helper()
		id = newAgentID()
		first := firstMessage(id, descOpts{host: host, version: "0.162.0"})
		first.Capabilities = capsCollector
		hash, _ = remoteBody(t, r.httpPoll(t, first, ""))
		m := r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 1,
			RemoteConfigStatus: &protobufs.RemoteConfigStatus{LastRemoteConfigHash: hash, Status: rcsApplying}}, "")
		if m.RemoteConfig != nil {
			t.Fatalf("no offer while the agent reports APPLYING: %v", m)
		}
		return id, hash
	}
	poll := func(t *testing.T, r *rig, id domain.AgentID, seq uint64) *protobufs.ServerToAgent {
		t.Helper()
		return r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: seq}, "")
	}
	// restarted is the first poll of a new process of the agent: sequence number
	// 0 and the full state with the stored status.
	restarted := func(id domain.AgentID, host string, hash []byte) *protobufs.AgentToServer {
		m := staleFullState(id, host, rcsApplying, hash)
		m.Capabilities = capsCollector
		return m
	}

	t.Run("the agent restarts and the pooled session of the server survives", func(t *testing.T) {
		clk := newStaleClock()
		r := staleRig(t, clk, staleAfter, rigOpts{})
		id, hash := httpApplying(t, r, "http-agent")

		clk.Advance(5 * time.Second)
		if m := r.httpPoll(t, restarted(id, "http-agent", hash), ""); m.RemoteConfig != nil {
			t.Fatalf("a status reported just now is not stale: %v", m)
		}
		clk.Advance(staleAfter - time.Nanosecond)
		if m := poll(t, r, id, 1); m.RemoteConfig != nil {
			t.Fatalf("one nanosecond below the threshold: %v", m)
		}
		clk.Advance(time.Nanosecond)
		if got, _ := remoteBody(t, poll(t, r, id, 2)); string(got) != string(hash) {
			t.Fatal("offered again with the next poll")
		}
		if m := poll(t, r, id, 3); m.RemoteConfig != nil {
			t.Fatalf("once per session: %v", m)
		}
		if got := staleReoffers(t, r); got != 1 {
			t.Fatalf("re-offers counted: %v", got)
		}
		staleOfferEvents(t, r, id, 2)
	})

	t.Run("a new pooled session, here on another replica", func(t *testing.T) {
		clk := newStaleClock()
		pool := pgtest.NewPool(t)
		a := staleRig(t, clk, staleAfter, rigOpts{pool: pool, replica: "replica-a"})
		b := staleRig(t, clk, staleAfter, rigOpts{pool: pool, replica: "replica-b"})
		id, hash := httpApplying(t, a, "http-agent")

		clk.Advance(5 * time.Second)
		if m := b.httpPoll(t, restarted(id, "http-agent", hash), ""); m.RemoteConfig != nil {
			t.Fatalf("a status reported just now is not stale: %v", m)
		}
		clk.Advance(staleAfter - time.Nanosecond)
		if m := poll(t, b, id, 1); m.RemoteConfig != nil {
			t.Fatalf("one nanosecond below the threshold: %v", m)
		}
		clk.Advance(time.Nanosecond)
		if got, _ := remoteBody(t, poll(t, b, id, 2)); string(got) != string(hash) {
			t.Fatal("replica b offers the config again")
		}
		if staleReoffers(t, b) != 1 || staleReoffers(t, a) != 0 {
			t.Fatalf("re-offers: replica b %v, replica a %v", staleReoffers(t, b), staleReoffers(t, a))
		}
	})
}

// A flapping agent is not offered the same config on every connection: a
// connection has to see the unchanged status for the whole threshold. Agents
// that stall together each get exactly one offer.
func TestStaleApplyingReconnectStormCausesNoDuplicateOffers(t *testing.T) {
	t.Run("one agent reconnects again and again", func(t *testing.T) {
		clk := newStaleClock()
		r := staleRig(t, clk, staleAfter, rigOpts{})
		a, id, hash := registerStaleApplying(t, r, "edge-1")
		cur := a
		// twenty connections of 4.5 seconds: 90 seconds in total, none long enough
		for i := 0; i < 20; i++ {
			next, m := staleRestart(t, r, cur, id, "edge-1", rcsApplying, hash)
			if m.RemoteConfig != nil {
				t.Fatalf("connection %d: %v", i, m)
			}
			clk.Advance(4500 * time.Millisecond)
			staleExpectNoOffer(t, next, fmt.Sprintf("connection %d", i))
			cur = next
		}
		if staleReoffers(t, r) != 0 {
			t.Fatal("short connections never cause an offer")
		}
		// one connection that stays long enough gets exactly one offer
		last, _ := staleRestart(t, r, cur, id, "edge-1", rcsApplying, hash)
		clk.Advance(staleAfter)
		if got, _ := remoteBody(t, staleHeartbeat(last)); string(got) != string(hash) {
			t.Fatal("offered after the threshold")
		}
		staleExpectNoOffer(t, last, "second heartbeat")
		if staleReoffers(t, r) != 1 {
			t.Fatalf("re-offers counted: %v", staleReoffers(t, r))
		}
		staleOfferEvents(t, r, id, 2)
	})

	t.Run("many agents reconnect together", func(t *testing.T) {
		const n = 15
		clk := newStaleClock()
		r := staleRig(t, clk, staleAfter, rigOpts{})
		type member struct {
			conn *rawAgent
			id   domain.AgentID
			hash []byte
			host string
		}
		members := make([]*member, n)
		for i := range members {
			host := fmt.Sprintf("edge-%d", i)
			a, id, hash := registerStaleApplying(t, r, host)
			members[i] = &member{conn: a, id: id, hash: hash, host: host}
		}
		for _, m := range members {
			m.conn.close()
		}
		for _, m := range members {
			id := m.id
			eventually(t, "offline", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })
		}
		for _, m := range members {
			var reply *protobufs.ServerToAgent
			m.conn, reply = staleReconnect(t, r, m.id, m.host, rcsApplying, m.hash)
			if reply.RemoteConfig != nil {
				t.Fatalf("%s: %v", m.host, reply)
			}
		}
		clk.Advance(staleAfter)
		for _, m := range members {
			if got, _ := remoteBody(t, staleHeartbeat(m.conn)); string(got) != string(m.hash) {
				t.Fatalf("%s: offered after the threshold", m.host)
			}
		}
		clk.Advance(time.Minute)
		for _, m := range members {
			staleExpectNoOffer(t, m.conn, m.host+", second round")
		}
		if got := staleReoffers(t, r); got != n {
			t.Fatalf("re-offers counted: %v, want %d", got, n)
		}
		for _, m := range members {
			staleOfferEvents(t, r, m.id, 2)
		}
	})
}

// Heartbeats are answered from the cached state of the connection, also while a
// window is open and when the offer is due.
func TestStaleApplyingCheckDoesNotTouchTheDatabase(t *testing.T) {
	base := pgtest.NewPool(t)
	cfg := base.Config().Copy()
	rec := &queryRecorder{}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	clk := newStaleClock()
	r := staleRig(t, clk, staleAfter, rigOpts{pool: pool})
	a, id, hash := registerStaleApplying(t, r, "edge-1")
	b, _ := staleRestart(t, r, a, id, "edge-1", rcsApplying, hash)
	time.Sleep(1500 * time.Millisecond) // let the asynchronous writers settle
	rec.reset()

	for i := 0; i < 20; i++ {
		clk.Advance(4 * time.Second)
		staleExpectNoOffer(t, b, fmt.Sprintf("heartbeat %d", i))
	}
	clk.Advance(staleAfter)
	if got, _ := remoteBody(t, staleHeartbeat(b)); string(got) != string(hash) {
		t.Fatal("the offer is due")
	}
	if q := rec.relevant(); len(q) != 0 {
		t.Fatalf("the heartbeats and the offer executed %d agent related statements, want 0:\n%s", len(q), strings.Join(q, "\n---\n"))
	}
}
