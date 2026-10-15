// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/domain"
)

// reconcile builds the reply for an agent from its cached state (no database
// access except for fetching a config body that is not cached yet): remote
// config when desired differs from what the agent reported and was not
// reported FAILED, package offers for updaters, and the given flags. The
// capabilities are always set (opamp-go does not set them). The caller holds
// sess.mu.
//
// Rules (docs/design/opamp-server.md, OpAMP server > Reconcile: what the reply
// contains, and > Pitfalls to remember):
//   - never send an empty config_map (the supervisor would start the no-op
//     pipeline) and never send an empty package set (the agent would delete
//     local packages);
//   - never offer the failed config hash again (resync clears it);
//   - do not offer the same hash twice within 60s on one connection;
//   - offer a hash that the agent keeps reporting as APPLYING once more after
//     opamp.staleApplyingAfter, once per connection (staleapplying.go); a
//     pooled HTTP session confirms it against the stored status first;
//   - commands are never part of this message (a command displaces all other
//     fields).
func (s *Server) reconcile(sess *session, flags uint64) *protobufs.ServerToAgent {
	uid := sess.id[:]
	resp := &protobufs.ServerToAgent{InstanceUid: append([]byte(nil), uid...), Capabilities: serverCapabilities, Flags: flags}
	a := sess.agent
	if a == nil {
		return resp
	}
	if sess.resync {
		resp.Flags |= uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState)
	}
	now := s.now()

	// A pooled HTTP session may cache a status that another replica has replaced
	// (staleapplying.go): the agent is read again when an offer is due.
	a, confirmed := s.confirmStaleApplying(sess, a, now)
	if a == nil {
		return resp // the agent does not exist any more
	}

	// ---- remote config
	if domain.HasCapability(a.Capabilities, domain.CapAcceptsRemoteConfig) && !a.Assignment.DesiredConfigHash.IsZero() {
		desired := a.Assignment.DesiredConfigHash
		needs := sess.resync || configNeedsOffer(a, desired)
		stale := confirmed && !needs && s.staleApplying(sess, a, desired, now)
		if (needs || stale) && (!sess.offeredRecently(desired, now) || sess.resync) {
			body, err := s.renderedBody(desired)
			switch {
			case err != nil:
				s.log.Warn("cannot offer config: rendered body unavailable", "agent_id", sess.id, "hash", desired.Short(), "error", err)
			case len(body) == 0:
				s.log.Error("refusing to offer an empty config", "agent_id", sess.id, "hash", desired.Short())
			default:
				resp.RemoteConfig = &protobufs.AgentRemoteConfig{
					Config: &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{
						domain.RemoteConfigKey: {Body: body, ContentType: domain.RemoteConfigContentType},
					}},
					ConfigHash: append([]byte(nil), desired...),
				}
				sess.offeredConfig, sess.offeredConfigAt = append(domain.Hash(nil), desired...), now
				if stale {
					s.offeredStale(sess, desired, now)
				} else {
					s.emit(sess.id, domain.AgentEventConfigOffered, "info", fmt.Sprintf("config %s offered", desired.Short()),
						map[string]any{"hash": desired.String(), "resync": sess.resync})
				}
			}
		}
	}

	// ---- packages (updaters)
	if domain.HasCapability(a.Capabilities, domain.CapAcceptsPackages) && len(sess.offers) > 0 {
		all := sess.offersHash
		newest := newestOffer(sess.offers)
		retry := sess.retryOffer(a, newest)
		// download tokens are minted when an offer is sent: an offer that is
		// still not completed when its token is half way through its lifetime is
		// sent again with a fresh one
		stale := false
		if s.deps.Downloads != nil && !sess.offeredPkgsAt.IsZero() {
			if ttl := s.deps.Downloads.DownloadTokenTTL(); ttl > 0 && now.Sub(sess.offeredPkgsAt) > ttl/2 {
				stale = offersIncomplete(a, sess.offers)
			}
		}
		needs := !all.Equal(sess.reportedPkgHash) || retry || stale
		recently := sess.offeredPkgs.Equal(all) && now.Sub(sess.offeredPkgsAt) < reofferGuard
		if needs && (!recently || sess.resync || retry || stale) {
			resp.PackagesAvailable = s.packagesFor(sess.id, sess.offers, all)
			sess.offeredPkgs, sess.offeredPkgsAt = append(domain.Hash(nil), all...), now
			sess.sentOfferCreated = newest
			s.emit(sess.id, domain.AgentEventPackageOffered, "info", fmt.Sprintf("package offer %s sent (%d package(s))", all.Short(), len(sess.offers)),
				map[string]any{"hash": all.String(), "packages": len(sess.offers), "version": sess.offers[0].Version})
		}
	}
	sess.resync = false
	return resp
}

// configNeedsOffer reports whether the desired config has to be offered: the
// agent does not run it and it is not the hash that failed. An agent that
// reports APPLYING for the desired hash is not offered it again here; only the
// stale APPLYING rule does that (staleapplying.go).
//
// An agent that reports FAILED for the desired hash did not apply it (the
// supervisor keeps the previous config; with validate_config there is not even
// an APPLIED report for the old hash afterwards), so LastHash == desired does
// not mean "reported". It is offered again only after an operator cleared the
// failed marker (retry-failed, resync); a repeated failure sets the marker
// again, so this cannot loop.
func configNeedsOffer(a *domain.Agent, desired domain.Hash) bool {
	if desired.Equal(a.Assignment.FailedConfigHash) {
		return false
	}
	if desired.Equal(a.RemoteConfig.LastHash) && a.RemoteConfig.Status != domain.RemoteConfigFailed {
		return false
	}
	return true
}

// packagesFor builds the PackagesAvailable message with fresh download tokens.
func (s *Server) packagesFor(agent domain.AgentID, offers []domain.PackageOffer, all domain.Hash) *protobufs.PackagesAvailable {
	if s.deps.Downloads == nil {
		return packagesAvailable(offers, all)
	}
	withHeaders := make([]domain.PackageOffer, len(offers))
	copy(withHeaders, offers)
	for i, o := range withHeaders {
		h, err := s.deps.Downloads.DownloadHeaders(o.DownloadURL, agent)
		if err != nil {
			s.log.Warn("cannot create a download token", "agent_id", agent, "package", o.Name, "error", err)
			continue
		}
		withHeaders[i].Headers = h
	}
	return packagesAvailable(withHeaders, all)
}

// offersIncomplete reports whether the agent has not installed every offered package.
func offersIncomplete(a *domain.Agent, offers []domain.PackageOffer) bool {
	for _, o := range offers {
		var ps *domain.PackageStatus
		if a.PackageStatus != nil {
			ps = a.PackageStatus.Packages[o.Name]
		}
		if ps == nil || ps.Status != domain.PackageInstalled || ps.AgentHasVersion != o.Version {
			return true
		}
	}
	return false
}

// newestOffer is the creation time of the newest package offer.
func newestOffer(offers []domain.PackageOffer) time.Time {
	var t time.Time
	for _, o := range offers {
		if o.CreatedAt.After(t) {
			t = o.CreatedAt
		}
	}
	return t
}

// retryOffer reports whether an offer whose hash the updater already reports
// (opamp-go reports the offered hash as soon as a sync starts, also when it
// ends in INSTALL_FAILED) has to be sent again: the install of an offered
// package failed and an offer that was issued after the last one sent on this
// connection exists (retry-failed stores an identical offer with a new
// download token and creation time). The opamp-go client then syncs again
// because it does not store the hash of a failed sync.
func (sess *session) retryOffer(a *domain.Agent, newest time.Time) bool {
	if a.PackageStatus == nil || !newest.After(sess.sentOfferCreated) {
		return false
	}
	for _, o := range sess.offers {
		if ps := a.PackageStatus.Packages[o.Name]; ps != nil && ps.Status == domain.PackageInstallFailed && ps.ServerOfferedVersion == o.Version {
			return true
		}
	}
	return false
}

// renderedBody returns the body of a rendered config (content addressed, so
// cached forever).
func (s *Server) renderedBody(hash domain.Hash) ([]byte, error) {
	key := hash.String()
	if b, ok := s.bodies.Get(key); ok {
		return b, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), messageTimeout)
	defer cancel()
	c, err := s.deps.Store.Configs().GetRendered(ctx, hash)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("rendered config %s not found", hash.Short())
		}
		return nil, err
	}
	s.bodies.Add(key, c.Body)
	return c.Body, nil
}
