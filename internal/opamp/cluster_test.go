// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// TestDesiredStateChangeReachesAgentOnOtherReplica is the HA acceptance test:
// two server instances share the database and the pgbus; the change is made
// on replica A (what the rollout engine on the leader does) and the agent is
// connected to replica B.
func TestDesiredStateChangeReachesAgentOnOtherReplica(t *testing.T) {
	pool := pgtest.NewPool(t)
	a := newRig(t, rigOpts{pool: pool, replica: "replica-a"})
	b := newRig(t, rigOpts{pool: pool, replica: "replica-b"})

	id := newAgentID()
	agent := b.connect("", id, capsCollector)
	agent.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	hash, _ := remoteBody(t, agent.mustRecv())
	agent.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""))
	agent.mustRecv()
	if got := b.agent(id).Connection.ReplicaID; got != "replica-b" {
		t.Fatalf("connected replica = %q", got)
	}

	newHash := newDesired(t, a, id, "service: {}\n# from replica A\n") // ctl of replica A
	m := agent.mustRecv()
	got, body := remoteBody(t, m)
	if string(got) != string(newHash) || body != "service: {}\n# from replica A\n" {
		t.Fatalf("push from the other replica: %x %q", got, body)
	}
	if a.srv.Connections() != 0 || b.srv.Connections() != 1 {
		t.Fatalf("connections a=%d b=%d", a.srv.Connections(), b.srv.Connections())
	}

	// commands travel the same way
	if err := a.ctl.Restart(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	if m := agent.mustRecv(); m.Command == nil {
		t.Fatalf("restart via the other replica: %v", m)
	}

	// replica heartbeats are visible and report the connections
	eventually(t, "replica heartbeats", 5*time.Second, func() bool {
		rs, _ := a.db.Replicas().List(context.Background())
		if len(rs) != 2 {
			return false
		}
		for _, r := range rs {
			if r.ID == "replica-b" && r.Connections == 1 && r.Hostname == "testhost" && r.IsLeader {
				return true
			}
		}
		return false
	})
}

func TestAgentMovesBetweenReplicasWithoutBeingMarkedOffline(t *testing.T) {
	pool := pgtest.NewPool(t)
	a := newRig(t, rigOpts{pool: pool, replica: "replica-a"})
	b := newRig(t, rigOpts{pool: pool, replica: "replica-b"})
	id := newAgentID()
	ca := a.connect("", id, capsCollector)
	ca.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	ca.mustRecv()
	eventually(t, "online at a", 5*time.Second, func() bool { return a.agent(id).Connection.ReplicaID == "replica-a" })

	// the agent reconnects to B while A still holds the old (half-open) connection
	cb := b.connect("", id, capsCollector)
	cb.send(&protobufs.AgentToServer{})
	cb.mustRecv()
	eventually(t, "online at b", 5*time.Second, func() bool { return b.agent(id).Connection.ReplicaID == "replica-b" })

	// now A's stale connection finally closes: the conditional offline write must not win
	ca.close()
	time.Sleep(700 * time.Millisecond)
	ag := b.agent(id)
	if !ag.IsOnline() || ag.Connection.ReplicaID != "replica-b" {
		t.Fatalf("closing the stale connection on replica A marked the agent offline: %+v", ag.Connection)
	}
}

func TestDeadReplicaIsReapedAndItsAgentsMarkedOffline(t *testing.T) {
	pool := pgtest.NewPool(t)
	leaderRig := newRig(t, rigOpts{pool: pool, replica: "replica-a"})
	ctx := context.Background()
	id := newAgentID()
	if _, _, err := leaderRig.db.Agents().UpsertDescription(ctx, id, firstUpdate()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := leaderRig.db.Agents().SetConnection(ctx, id, domain.AgentConnection{Status: domain.ConnectionOnline, ReplicaID: "replica-dead", Transport: "websocket", LastSeenAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := leaderRig.db.Replicas().Heartbeat(ctx, domain.Replica{ID: "replica-dead", StartedAt: now, LastHeartbeatAt: now.Add(-time.Minute), Connections: 1}); err != nil {
		t.Fatal(err)
	}
	ch, cancel := leaderRig.bus.Subscribe(eventbus.AgentStateChanged)
	defer cancel()

	leaderRig.srv.ReapReplicas(ctx)

	ag := leaderRig.agent(id)
	if ag.IsOnline() || ag.Connection.ReplicaID != "" {
		t.Fatalf("agent of the dead replica must be offline: %+v", ag.Connection)
	}
	rs, _ := leaderRig.db.Replicas().List(ctx)
	for _, r := range rs {
		if r.ID == "replica-dead" {
			t.Fatal("dead replica must be removed from the registry")
		}
	}
	leaderRig.waitEvent(id, domain.AgentEventDisconnected)
	select {
	case ev := <-ch:
		var p eventbus.AgentStateChangedPayload
		_ = ev.Decode(&p)
		if p.AgentID != id || p.Online {
			t.Fatalf("state change: %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent.state_changed expected for agents of a dead replica")
	}
	// a live replica is never reaped, not even itself
	if err := leaderRig.db.Replicas().Heartbeat(ctx, domain.Replica{ID: "replica-a", StartedAt: now, LastHeartbeatAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	other := newAgentID()
	_, _, _ = leaderRig.db.Agents().UpsertDescription(ctx, other, firstUpdate())
	_ = leaderRig.db.Agents().SetConnection(ctx, other, domain.AgentConnection{Status: domain.ConnectionOnline, ReplicaID: "replica-a", LastSeenAt: &now})
	leaderRig.srv.ReapReplicas(ctx)
	if !leaderRig.agent(other).IsOnline() {
		t.Fatal("the leader must never mark its own agents offline when it reaps")
	}
	_ = uuid.Nil
}

func TestOfflineSweeperMarksSilentAgentsOffline(t *testing.T) {
	r := newRig(t, rigOpts{}) // OfflineAfter = 2s
	ctx := context.Background()
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()

	r.srv.SweepOffline(ctx)
	if !r.agent(id).IsOnline() {
		t.Fatal("fresh agent must stay online")
	}
	// keep the TCP connection (half-open scenario) but send nothing for > OfflineAfter
	time.Sleep(2500 * time.Millisecond)
	// the last_seen flush for the earlier messages happened long ago
	r.srv.SweepOffline(ctx)
	ag := r.agent(id)
	if ag.IsOnline() || ag.Connection.ReplicaID != "" {
		t.Fatalf("silent agent must be marked offline: %+v", ag.Connection)
	}
	r.waitEvent(id, domain.AgentEventDisconnected)

	// a heartbeat brings it back (self healing of the cached connection state)
	a.send(&protobufs.AgentToServer{})
	a.mustRecv()
	// the cached session still says online; the 5 minute refresh heals it. Force it through a push.
	_ = r.ctl.DesiredStateChanged(ctx, []domain.AgentID{id})
	eventually(t, "online again after refresh", 5*time.Second, func() bool { return r.agent(id).IsOnline() })
}

func TestLastSeenIsBatchedAndKeepsAgentsOnline(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()
	before := r.agent(id).Connection.LastSeenAt
	if before == nil {
		t.Fatal("last_seen set at connect")
	}
	for round := 0; round < 4; round++ {
		time.Sleep(700 * time.Millisecond)
		a.send(&protobufs.AgentToServer{}) // heartbeat
		a.mustRecv()
		r.srv.SweepOffline(ctx)
		if !r.agent(id).IsOnline() {
			t.Fatalf("round %d: heartbeating agent marked offline", round)
		}
	}
	after := r.agent(id).Connection.LastSeenAt
	if !after.After(*before) {
		t.Fatalf("last_seen_at must advance with heartbeats: %v -> %v", before, after)
	}
}

func firstUpdate() (u storeUpdate) { return newUpdate("x") }

// toggleLeader is an elector whose leadership the test flips.
type toggleLeader struct{ leading atomic.Bool }

func (l *toggleLeader) Run(ctx context.Context, _ func(ctx context.Context)) error {
	<-ctx.Done()
	return nil
}
func (l *toggleLeader) IsLeader() bool { return l.leading.Load() }

// The replica row shows a change of leadership within about a second, not with the next regular heartbeat (here: 1 h),
// so that /system/info served by another replica does not lag behind an election or a takeover.
func TestReplicaRowFollowsLeadershipWithoutWaitingForTheHeartbeat(t *testing.T) {
	el := &toggleLeader{}
	r := newRig(t, rigOpts{elector: el, heartbeat: time.Hour})
	isLeader := func() bool {
		rs, err := r.db.Replicas().List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, rep := range rs {
			if rep.ID == r.replica {
				return rep.IsLeader
			}
		}
		return false
	}
	eventually(t, "the replica row exists without leadership", 5*time.Second, func() bool {
		rs, _ := r.db.Replicas().List(context.Background())
		for _, rep := range rs {
			if rep.ID == r.replica {
				return !rep.IsLeader
			}
		}
		return false
	})
	el.leading.Store(true)
	eventually(t, "the replica row shows is_leader after the acquisition", 5*time.Second, isLeader)
	el.leading.Store(false)
	eventually(t, "the replica row drops is_leader after the loss", 5*time.Second, func() bool { return !isLeader() })
}
