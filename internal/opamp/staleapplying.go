// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// reasonStaleApplying is the reason of a config offer that repeats a hash the
// agent already reported as APPLYING: the label of the metric
// myriarch_opamp_config_reoffers_total and the value of details.reason of the
// agent event config_offered.
const reasonStaleApplying = "stale_applying"

// Stale APPLYING (docs/design/opamp-server.md, "Stale APPLYING after a
// reconnect").
//
// A supervisor that restarts while it applies a config stores APPLYING, reports
// it again after it reconnects, and nothing ends it (docs/design/upstream-notes.md,
// "Restart while a remote config is applying"). The server would wait for
// APPLIED until the applyTimeout of the rollout policy, because configNeedsOffer
// never offers a hash again that the agent reported as APPLYING.
//
// The rule is evaluated per connection from cached state, so heartbeats stay
// free of database access:
//
//  1. When the agent reports APPLYING for a hash and this connection did not
//     offer that hash itself (a new WebSocket connection, another replica, a
//     new pooled HTTP session) or the agent reports the status it had already
//     reported (a reconnect or restart on a long-lived pooled HTTP session),
//     trackApplying opens a window at the time of that report. A status that
//     the server stores anew is a new episode and replaces an open window, also
//     for the same hash.
//  2. Once the status has been unchanged for opamp.staleApplyingAfter, the next
//     reply (a heartbeat, a poll or a push) offers the desired config once more.
//     staleApplying checks the conditions, reconcile sends the offer.
//
// The window starts with the report on this connection, not with the time the
// status was stored: a connection that ends sooner never causes an offer, so
// an agent that flaps cannot be offered the same config on every connection.
// A status that this connection caused by its own offer is not touched; the
// applyTimeout of the rollout bounds it. FAILED hashes, empty bodies and the
// re-offer guard of reconcile apply as for every offer.
//
// A pooled HTTP session shares its agent with the sessions of the other
// replicas, which store statuses that this session does not hear of.
// confirmStaleApplying reads the agent once when an offer is due, so that the
// offer rests on the stored status, and the window never starts before the time
// the status was stored (unchangedSince).

// trackApplying maintains the window of the stale APPLYING rule. It runs for
// every remote config status the agent reports on this connection, after the
// status was stored; repeated is true when the stored status was identical
// already. The caller holds sess.mu.
func (sess *session) trackApplying(st domain.RemoteConfigState, repeated bool, now time.Time) {
	if st.Status != domain.RemoteConfigApplying || st.LastHash.IsZero() {
		// progress or the end of the status: the episode is over
		sess.applyingHash, sess.applyingSince, sess.staleOffered = nil, time.Time{}, nil
		return
	}
	if repeated {
		if sess.applyingHash.Equal(st.LastHash) {
			return // the window of this status is open already
		}
	} else {
		// A status that is stored anew starts a new episode. An open window for
		// the same hash belongs to an earlier one, the end of which this
		// session may not have seen: polls of one agent are spread over the
		// replicas, and the report that ended the episode may have reached
		// another one. It must not be reused, nor the record of its offer.
		sess.staleOffered = nil
		if sess.offeredConfig.Equal(st.LastHash) {
			// the agent reports the progress of the offer that this connection made
			sess.applyingHash, sess.applyingSince = nil, time.Time{}
			return
		}
	}
	sess.applyingHash, sess.applyingSince = append(domain.Hash(nil), st.LastHash...), now
}

// staleApplying reports whether the config desired has to be offered again
// because the agent kept reporting APPLYING for it on this connection for at
// least opamp.staleApplyingAfter. It uses the cached state of the session
// only. The caller holds sess.mu.
func (s *Server) staleApplying(sess *session, a *domain.Agent, desired domain.Hash, now time.Time) bool {
	after := s.cfg.StaleApplyingAfter
	rc := a.RemoteConfig
	switch {
	case after <= 0, desired.IsZero(),
		rc.Status != domain.RemoteConfigApplying, !rc.LastHash.Equal(desired),
		desired.Equal(a.Assignment.FailedConfigHash),
		sess.applyingSince.IsZero(), !sess.applyingHash.Equal(desired),
		sess.staleOffered.Equal(desired):
		return false
	}
	return now.Sub(sess.unchangedSince(rc, now)) >= after
}

// unchangedSince is the start of the time that the status has been unchanged:
// the start of the window of this session, or the time the server stored the
// status if that is later. The status cannot have been unchanged for longer
// than it has been stored, and another replica may have stored a newer one
// after this session opened its window. A time in the future of this clock
// (clocks that differ) is not trusted.
func (sess *session) unchangedSince(rc domain.RemoteConfigState, now time.Time) time.Time {
	since := sess.applyingSince
	if at := rc.UpdatedAt; at != nil && at.After(since) && !at.After(now) {
		since = *at
	}
	return since
}

// confirmStaleApplying prepares reconcile for the stale APPLYING rule. The
// cached status of a pooled HTTP session can be older than the stored one: the
// session is refreshed on own writes, on desired-state, assignment and command
// events, after a bus reconnect and every five minutes, and a status that
// another replica stores is none of these. When the cache says that an offer is
// due, the agent is read again, and the offer rests on what is stored. That is
// one read for an offer that is due; heartbeats and WebSocket sessions, whose
// connection is the only writer of the status of its agent, stay free of
// database access. It returns the agent to build the reply from (nil: the agent
// does not exist any more) and whether the offer may be made; it may not when
// the read failed. The caller holds sess.mu.
func (s *Server) confirmStaleApplying(sess *session, a *domain.Agent, now time.Time) (*domain.Agent, bool) {
	// the conditions under which reconcile makes a stale offer
	desired := a.Assignment.DesiredConfigHash
	if sess.ci != nil || sess.resync || !domain.HasCapability(a.Capabilities, domain.CapAcceptsRemoteConfig) ||
		sess.offeredRecently(desired, now) || !s.staleApplying(sess, a, desired, now) {
		return a, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), messageTimeout)
	defer cancel()
	if err := s.refresh(ctx, sess); err != nil {
		s.log.Warn("cannot read the agent to confirm its stale APPLYING status; the config is not offered again yet", "agent_id", sess.id, "error", err)
		return sess.agent, false
	}
	if fresh := sess.agent; fresh != nil && !s.staleApplying(sess, fresh, fresh.Assignment.DesiredConfigHash, now) {
		s.log.Debug("not a stale APPLYING: the stored status of the agent has moved on", "agent_id", sess.id)
	}
	return sess.agent, true
}

// offeredRecently reports whether this connection offered hash within the
// re-offer guard. The caller holds sess.mu.
func (sess *session) offeredRecently(hash domain.Hash, now time.Time) bool {
	return sess.offeredConfig.Equal(hash) && now.Sub(sess.offeredConfigAt) < reofferGuard
}

// offeredStale records and reports the offer of a config that the agent had
// reported as APPLYING: once per connection and hash, visible as the agent
// event config_offered with details.reason stale_applying and as a metric.
// The caller holds sess.mu.
func (s *Server) offeredStale(sess *session, desired domain.Hash, now time.Time) {
	applying := now.Sub(sess.unchangedSince(sess.agent.RemoteConfig, now)).Round(time.Second)
	sess.staleOffered = append(domain.Hash(nil), desired...)
	s.m.reoffers.WithLabelValues(reasonStaleApplying).Inc()
	s.log.Info("offering the config again: the agent keeps reporting APPLYING", "agent_id", sess.id, "hash", desired.Short(), "applying_for", applying.String())
	s.emit(sess.id, domain.AgentEventConfigOffered, "warning",
		"config "+desired.Short()+" offered again: the agent has reported APPLYING for "+applying.String(),
		map[string]any{"hash": desired.String(), "resync": false, "reason": reasonStaleApplying, "applyingSeconds": int64(applying / time.Second)})
}
