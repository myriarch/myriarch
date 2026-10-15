// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
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
// The rule is evaluated per connection and from cached state only, so
// heartbeats stay free of database access:
//
//  1. When the agent reports APPLYING for a hash and this connection did not
//     offer that hash itself (a new WebSocket connection, another replica, a
//     new pooled HTTP session) or the agent reports the status it had already
//     reported (a reconnect or restart on a long-lived pooled HTTP session),
//     trackApplying opens a window at the time of that report.
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
	if sess.applyingHash.Equal(st.LastHash) {
		return // the window of this status is open already
	}
	if !repeated && sess.offeredConfig.Equal(st.LastHash) {
		// the agent reports the progress of the offer that this connection made
		sess.applyingHash, sess.applyingSince = nil, time.Time{}
		return
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
	return now.Sub(sess.applyingSince) >= after
}

// offeredStale records and reports the offer of a config that the agent had
// reported as APPLYING: once per connection and hash, visible as the agent
// event config_offered with details.reason stale_applying and as a metric.
// The caller holds sess.mu.
func (s *Server) offeredStale(sess *session, desired domain.Hash, now time.Time) {
	applying := now.Sub(sess.applyingSince).Round(time.Second)
	sess.staleOffered = append(domain.Hash(nil), desired...)
	s.m.reoffers.WithLabelValues(reasonStaleApplying).Inc()
	s.log.Info("offering the config again: the agent keeps reporting APPLYING", "agent_id", sess.id, "hash", desired.Short(), "applying_for", applying.String())
	s.emit(sess.id, domain.AgentEventConfigOffered, "warning",
		"config "+desired.Short()+" offered again: the agent has reported APPLYING for "+applying.String(),
		map[string]any{"hash": desired.String(), "resync": false, "reason": reasonStaleApplying, "applyingSeconds": int64(applying / time.Second)})
}
