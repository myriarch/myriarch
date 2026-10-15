// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

var staleT0 = time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

func staleApplyingState(hash domain.Hash) domain.RemoteConfigState {
	return domain.RemoteConfigState{Status: domain.RemoteConfigApplying, LastHash: hash}
}

// TestTrackApplying covers which reports open, keep and close the window of the
// stale APPLYING rule.
func TestTrackApplying(t *testing.T) {
	h1, h2 := domain.Hash("hash-one"), domain.Hash("hash-two")
	applied := domain.RemoteConfigState{Status: domain.RemoteConfigApplied, LastHash: h1}
	failed := domain.RemoteConfigState{Status: domain.RemoteConfigFailed, LastHash: h1, ErrorMessage: "invalid"}

	t.Run("a status the connection did not cause opens the window", func(t *testing.T) {
		sess := &session{}
		sess.trackApplying(staleApplyingState(h1), false, staleT0)
		if !sess.applyingHash.Equal(h1) || !sess.applyingSince.Equal(staleT0) {
			t.Fatalf("window: %x since %v", sess.applyingHash, sess.applyingSince)
		}
	})
	t.Run("a status the connection caused does not", func(t *testing.T) {
		sess := &session{offeredConfig: h1}
		sess.trackApplying(staleApplyingState(h1), false, staleT0)
		if sess.applyingHash != nil || !sess.applyingSince.IsZero() {
			t.Fatalf("window: %x since %v", sess.applyingHash, sess.applyingSince)
		}
	})
	t.Run("a status reported again opens it even after an offer of the connection", func(t *testing.T) {
		sess := &session{offeredConfig: h1}
		sess.trackApplying(staleApplyingState(h1), true, staleT0)
		if !sess.applyingHash.Equal(h1) || !sess.applyingSince.Equal(staleT0) {
			t.Fatalf("window: %x since %v", sess.applyingHash, sess.applyingSince)
		}
	})
	t.Run("a report of an open window keeps its start", func(t *testing.T) {
		sess := &session{}
		sess.trackApplying(staleApplyingState(h1), true, staleT0)
		sess.trackApplying(staleApplyingState(h1), true, staleT0.Add(time.Minute))
		if !sess.applyingSince.Equal(staleT0) {
			t.Fatalf("the window start moved to %v", sess.applyingSince)
		}
	})
	t.Run("another hash replaces the window", func(t *testing.T) {
		sess := &session{}
		sess.trackApplying(staleApplyingState(h1), true, staleT0)
		sess.trackApplying(staleApplyingState(h2), true, staleT0.Add(time.Minute))
		if !sess.applyingHash.Equal(h2) || !sess.applyingSince.Equal(staleT0.Add(time.Minute)) {
			t.Fatalf("window: %x since %v", sess.applyingHash, sess.applyingSince)
		}
	})
	// A status that the server stores anew is a new episode. A window that is open
	// for the same hash belongs to an earlier episode whose end this session did not
	// see: HTTP polls of one agent are spread over the replicas, and the report that
	// ended the episode may have landed on another one.
	t.Run("a status stored anew replaces the window of the same hash", func(t *testing.T) {
		sess := &session{}
		sess.trackApplying(staleApplyingState(h1), true, staleT0)
		sess.trackApplying(staleApplyingState(h1), false, staleT0.Add(time.Hour))
		if !sess.applyingHash.Equal(h1) || !sess.applyingSince.Equal(staleT0.Add(time.Hour)) {
			t.Fatalf("the window of the earlier episode was reused: %x since %v", sess.applyingHash, sess.applyingSince)
		}
	})
	t.Run("a status stored anew after an offer of the connection closes the window of the same hash", func(t *testing.T) {
		sess := &session{offeredConfig: h1}
		sess.trackApplying(staleApplyingState(h1), true, staleT0)
		sess.trackApplying(staleApplyingState(h1), false, staleT0.Add(time.Hour))
		if sess.applyingHash != nil || !sess.applyingSince.IsZero() {
			t.Fatalf("window: %x since %v", sess.applyingHash, sess.applyingSince)
		}
	})
	t.Run("a status stored anew ends the record of the stale offer of the earlier episode", func(t *testing.T) {
		sess := &session{}
		sess.trackApplying(staleApplyingState(h1), true, staleT0)
		sess.staleOffered = h1
		sess.trackApplying(staleApplyingState(h1), false, staleT0.Add(time.Hour))
		if sess.staleOffered != nil {
			t.Fatalf("the record of the earlier stale offer stays: %x", sess.staleOffered)
		}
	})
	t.Run("a status reported again keeps the record of the stale offer", func(t *testing.T) {
		sess := &session{}
		sess.trackApplying(staleApplyingState(h1), true, staleT0)
		sess.staleOffered = h1
		sess.trackApplying(staleApplyingState(h1), true, staleT0.Add(time.Hour))
		if !sess.staleOffered.Equal(h1) || !sess.applyingSince.Equal(staleT0) {
			t.Fatalf("record %x, window since %v", sess.staleOffered, sess.applyingSince)
		}
	})
	for name, st := range map[string]domain.RemoteConfigState{
		"applied": applied, "failed": failed, "unset": {Status: domain.RemoteConfigUnset}, "applying without a hash": {Status: domain.RemoteConfigApplying},
	} {
		t.Run(name+" ends the episode", func(t *testing.T) {
			sess := &session{}
			sess.trackApplying(staleApplyingState(h1), true, staleT0)
			sess.staleOffered = h1
			sess.trackApplying(st, false, staleT0.Add(time.Second))
			if sess.applyingHash != nil || !sess.applyingSince.IsZero() || sess.staleOffered != nil {
				t.Fatalf("window %x since %v, offered %x", sess.applyingHash, sess.applyingSince, sess.staleOffered)
			}
		})
	}
}

// TestStaleApplyingDecision covers every condition of the decision and the
// timing edges around the threshold.
func TestStaleApplyingDecision(t *testing.T) {
	h1, h2 := domain.Hash("hash-one"), domain.Hash("hash-two")
	const after = 90 * time.Second
	srv := &Server{cfg: config.OpAMPConfig{StaleApplyingAfter: after}}

	// ready returns an agent that applies h1, a session with a window opened
	// at staleT0 and the desired hash h1.
	ready := func() (*session, *domain.Agent) {
		a := &domain.Agent{RemoteConfig: staleApplyingState(h1), Assignment: domain.AgentAssignment{DesiredConfigHash: h1}}
		return &session{applyingHash: h1, applyingSince: staleT0}, a
	}

	// storedAt sets the time the server stored the status, d after the start of the window.
	storedAt := func(d time.Duration) func(*session, *domain.Agent) {
		return func(_ *session, a *domain.Agent) {
			at := staleT0.Add(d)
			a.RemoteConfig.UpdatedAt = &at
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(sess *session, a *domain.Agent)
		wait   time.Duration
		want   bool
	}{
		{"one nanosecond below the threshold", nil, after - time.Nanosecond, false},
		{"at the threshold", nil, after, true},
		{"above the threshold", nil, after + time.Nanosecond, true},
		{"long after", nil, 24 * time.Hour, true},
		{"no time has passed", nil, 0, false},
		{"no window", func(sess *session, _ *domain.Agent) { sess.applyingHash, sess.applyingSince = nil, time.Time{} }, after, false},
		{"the window is for another hash", func(sess *session, _ *domain.Agent) { sess.applyingHash = h2 }, after, false},
		{"the desired hash changed", func(_ *session, a *domain.Agent) { a.Assignment.DesiredConfigHash = h2 }, after, false},
		{"the status is APPLIED", func(_ *session, a *domain.Agent) { a.RemoteConfig.Status = domain.RemoteConfigApplied }, after, false},
		{"the status is FAILED", func(_ *session, a *domain.Agent) { a.RemoteConfig.Status = domain.RemoteConfigFailed }, after, false},
		{"the status names another hash", func(_ *session, a *domain.Agent) { a.RemoteConfig.LastHash = h2 }, after, false},
		{"the hash is marked failed", func(_ *session, a *domain.Agent) { a.Assignment.FailedConfigHash = h1 }, after, false},
		{"another hash is marked failed", func(_ *session, a *domain.Agent) { a.Assignment.FailedConfigHash = h2 }, after, true},
		{"already offered again on this connection", func(sess *session, _ *domain.Agent) { sess.staleOffered = h1 }, after, false},
		{"an offer of another hash on this connection", func(sess *session, _ *domain.Agent) { sess.staleOffered = h2 }, after, true},
		{"no desired hash", func(_ *session, a *domain.Agent) { a.Assignment.DesiredConfigHash = nil }, after, false},
		// The status cannot have been unchanged for longer than it has been stored: another replica may have
		// stored a newer one after this session opened its window.
		{"stored after the window opened, one nanosecond below the threshold counted from the store time",
			storedAt(30 * time.Second), 30*time.Second + after - time.Nanosecond, false},
		{"stored after the window opened, at the threshold counted from the store time", storedAt(30 * time.Second), 30*time.Second + after, true},
		{"stored when the window opened", storedAt(0), after, true},
		{"stored before the window opened", storedAt(-time.Hour), after, true},
		{"stored just now", storedAt(after), after, false},
		{"stored in the future of this clock", storedAt(10 * time.Minute), after, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, a := ready()
			if tc.mutate != nil {
				tc.mutate(sess, a)
			}
			if got := srv.staleApplying(sess, a, a.Assignment.DesiredConfigHash, staleT0.Add(tc.wait)); got != tc.want {
				t.Fatalf("staleApplying = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("the setting 0 turns it off", func(t *testing.T) {
		sess, a := ready()
		off := &Server{cfg: config.OpAMPConfig{StaleApplyingAfter: 0}}
		if off.staleApplying(sess, a, h1, staleT0.Add(24*time.Hour)) {
			t.Fatal("0 turns the re-offer off")
		}
	})
}

// ---------------------------------------------------------------------------
// A pooled HTTP session shares the agent with the sessions of the other replicas.
// A status that one of them stores does not invalidate the cache of the others,
// so the rule rests on the stored status when an offer is due. The database is
// replaced by a store that counts the reads of the agent.

// staleStore is a store of which reconcile reads nothing but the agent (the
// rendered body is cached in the tests).
type staleStore struct {
	store.Store
	agents *staleAgents
}

func (s *staleStore) Agents() store.AgentStore { return s.agents }

type staleAgents struct {
	store.AgentStore
	reads  int
	stored *domain.Agent // nil: the agent does not exist (any more)
	err    error
}

func (s *staleAgents) Get(context.Context, domain.AgentID) (*domain.Agent, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	if s.stored == nil {
		return nil, domain.ErrNotFound
	}
	cp := *s.stored
	return &cp, nil
}

// pooledStale is a server and the pooled HTTP session of an agent that has
// reported APPLYING for the desired hash since staleT0.
type pooledStale struct {
	srv    *Server
	agents *staleAgents
	sess   *session
	hash   domain.Hash
	now    time.Time
}

func newPooledStale(t *testing.T) *pooledStale {
	t.Helper()
	p := &pooledStale{agents: &staleAgents{}, hash: domain.Hash("hash-one"), now: staleT0}
	srv, err := New(config.OpAMPConfig{StaleApplyingAfter: 90 * time.Second}, Deps{
		Store: &staleStore{agents: p.agents}, Bus: struct{ eventbus.Bus }{}, Auth: struct{ AgentAuthenticator }{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
		Now: func() time.Time { return p.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	p.srv = srv
	srv.bodies.Add(p.hash.String(), []byte("service:\n  pipelines: {}\n"))
	p.sess = srv.newSession(domain.AgentID{1}, transportHTTP)
	p.sess.established = true
	p.sess.agent = p.agent(domain.RemoteConfigApplying)
	p.sess.applyingHash, p.sess.applyingSince = p.hash, staleT0
	p.agents.stored = p.agent(domain.RemoteConfigApplying)
	return p
}

// agent is the agent with the given status for the desired hash.
func (p *pooledStale) agent(status domain.RemoteConfigStatus) *domain.Agent {
	return &domain.Agent{
		Capabilities: domain.CapAcceptsRemoteConfig,
		RemoteConfig: domain.RemoteConfigState{Status: status, LastHash: p.hash},
		Assignment:   domain.AgentAssignment{DesiredConfigHash: p.hash},
	}
}

// reply reconciles at staleT0+d and returns the hash of the offer, if any.
func (p *pooledStale) reply(d time.Duration) domain.Hash {
	p.now = staleT0.Add(d)
	if resp := p.srv.reconcile(p.sess, 0); resp.RemoteConfig != nil {
		return domain.Hash(resp.RemoteConfig.ConfigHash)
	}
	return nil
}

func (p *pooledStale) reoffers() float64 {
	var m dto.Metric
	if err := p.srv.m.reoffers.WithLabelValues(reasonStaleApplying).Write(&m); err != nil {
		panic(err)
	}
	return m.GetCounter().GetValue()
}

func TestStaleApplyingOfAPooledHTTPSessionRestsOnTheStoredStatus(t *testing.T) {
	const after = 90 * time.Second

	t.Run("the stored status moved on: no offer, one read, and the cache is current afterwards", func(t *testing.T) {
		p := newPooledStale(t)
		p.agents.stored = p.agent(domain.RemoteConfigApplied) // another replica stored APPLIED
		if got := p.reply(after); got != nil {
			t.Fatalf("offered %x although the stored status is APPLIED", got)
		}
		if p.agents.reads != 1 || p.sess.agent.RemoteConfig.Status != domain.RemoteConfigApplied {
			t.Fatalf("reads %d, cached status %s", p.agents.reads, p.sess.agent.RemoteConfig.Status)
		}
		for _, d := range []time.Duration{after + time.Second, 10 * time.Minute, time.Hour} {
			if got := p.reply(d); got != nil {
				t.Fatalf("offered %x %s after the window opened", got, d)
			}
		}
		if p.agents.reads != 1 || p.reoffers() != 0 {
			t.Fatalf("reads %d, re-offers %v: the refreshed cache decides without a read", p.agents.reads, p.reoffers())
		}
	})

	t.Run("still APPLYING in the store: offered once after one read", func(t *testing.T) {
		p := newPooledStale(t)
		if got := p.reply(after - time.Nanosecond); got != nil || p.agents.reads != 0 {
			t.Fatalf("below the threshold: offer %x, reads %d", got, p.agents.reads)
		}
		if got := p.reply(after); !got.Equal(p.hash) || p.agents.reads != 1 {
			t.Fatalf("at the threshold: offer %x, reads %d", got, p.agents.reads)
		}
		if got := p.reply(after + time.Minute); got != nil || p.agents.reads != 1 || p.reoffers() != 1 {
			t.Fatalf("once: offer %x, reads %d, re-offers %v", got, p.agents.reads, p.reoffers())
		}
	})

	t.Run("a status stored after the window opened restarts the window", func(t *testing.T) {
		p := newPooledStale(t)
		stored := p.agent(domain.RemoteConfigApplying)
		at := staleT0.Add(2 * time.Minute) // another replica stored a new APPLYING of the same hash
		stored.RemoteConfig.UpdatedAt = &at
		p.agents.stored = stored
		if got := p.reply(2*time.Minute + 10*time.Second); got != nil || p.agents.reads != 1 {
			t.Fatalf("10 seconds after the newer status: offer %x, reads %d", got, p.agents.reads)
		}
		if got := p.reply(2*time.Minute + after - time.Nanosecond); got != nil {
			t.Fatalf("one nanosecond below the threshold counted from the newer status: offer %x", got)
		}
		if got := p.reply(2*time.Minute + after); !got.Equal(p.hash) || p.reoffers() != 1 {
			t.Fatalf("at the threshold counted from the newer status: offer %x, re-offers %v", got, p.reoffers())
		}
	})

	t.Run("the read fails: nothing is offered and the next reply tries again", func(t *testing.T) {
		p := newPooledStale(t)
		p.agents.err = errors.New("database unavailable")
		if got := p.reply(after); got != nil || p.agents.reads != 1 || p.reoffers() != 0 {
			t.Fatalf("offer %x, reads %d, re-offers %v: no offer without the stored status", got, p.agents.reads, p.reoffers())
		}
		if p.sess.offeredConfig != nil || !p.sess.applyingHash.Equal(p.hash) {
			t.Fatalf("the failed read must not change the state of the rule: offered %x, window %x", p.sess.offeredConfig, p.sess.applyingHash)
		}
		p.agents.err = nil
		if got := p.reply(after + 30*time.Second); !got.Equal(p.hash) || p.agents.reads != 2 {
			t.Fatalf("after the database is back: offer %x, reads %d", got, p.agents.reads)
		}
	})

	t.Run("the agent does not exist any more: an empty reply", func(t *testing.T) {
		p := newPooledStale(t)
		p.agents.stored = nil
		p.now = staleT0.Add(after)
		resp := p.srv.reconcile(p.sess, 0)
		if resp == nil || resp.RemoteConfig != nil || p.sess.agent != nil || p.agents.reads != 1 {
			t.Fatalf("reply %v, cached agent %v, reads %d", resp, p.sess.agent, p.agents.reads)
		}
	})

	t.Run("a WebSocket session offers from its cache without a read", func(t *testing.T) {
		p := newPooledStale(t)
		p.sess.ci = &connInfo{} // the only writer of the status of its agent
		p.agents.stored = p.agent(domain.RemoteConfigApplied)
		if got := p.reply(after); !got.Equal(p.hash) || p.agents.reads != 0 {
			t.Fatalf("offer %x, reads %d", got, p.agents.reads)
		}
	})

	t.Run("no read where the offer would not be made anyway", func(t *testing.T) {
		p := newPooledStale(t)
		p.sess.agent.Capabilities = 0 // the agent does not accept remote config
		if got := p.reply(after); got != nil || p.agents.reads != 0 {
			t.Fatalf("an agent that does not accept remote config: offer %x, reads %d", got, p.agents.reads)
		}

		// an offer of this connection within the re-offer guard blocks the offer; the guard ends 60 seconds later
		p = newPooledStale(t)
		p.sess.offeredConfig, p.sess.offeredConfigAt = p.hash, staleT0.Add(after-10*time.Second)
		if got := p.reply(after); got != nil || p.agents.reads != 0 {
			t.Fatalf("within the re-offer guard: offer %x, reads %d", got, p.agents.reads)
		}
		if got := p.reply(after - 10*time.Second + reofferGuard); !got.Equal(p.hash) || p.agents.reads != 1 {
			t.Fatalf("after the re-offer guard: offer %x, reads %d", got, p.agents.reads)
		}
	})

	t.Run("a resync offers at once as for every session and reads nothing", func(t *testing.T) {
		p := newPooledStale(t)
		p.sess.resync = true
		if got := p.reply(after); !got.Equal(p.hash) || p.agents.reads != 0 || p.reoffers() != 0 {
			t.Fatalf("offer %x, reads %d, re-offers %v", got, p.agents.reads, p.reoffers())
		}
	})
}
