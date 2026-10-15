// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
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
