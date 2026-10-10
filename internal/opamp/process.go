// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type processResult struct {
	changes          []string
	reportComponents bool
}

func (r *processResult) change(c string) {
	for _, x := range r.changes {
		if x == c {
			return
		}
	}
	r.changes = append(r.changes, c)
}

// processMessage implements steps 6 to 11 of the message handling (the caller
// holds sess.mu). Errors are infrastructure failures (database).
func (s *Server) processMessage(ctx context.Context, sess *session, ci *connInfo, msg *protobufs.AgentToServer, pr *processResult) error {
	// ---- 6. agent_description
	if msg.AgentDescription != nil {
		if err := s.processDescription(ctx, sess, ci, msg, pr); err != nil {
			return err
		}
	}
	if sess.agent == nil {
		// unknown agent without description: store nothing
		return errAgentUnknown
	}
	a := sess.agent

	// ---- 7. health
	if msg.Health != nil {
		if err := s.processHealth(ctx, sess, msg.Health, pr); err != nil {
			return err
		}
	}
	// ---- 8. effective config
	if msg.EffectiveConfig != nil {
		if err := s.processEffectiveConfig(ctx, sess, msg.EffectiveConfig, pr); err != nil {
			return err
		}
	}
	// ---- 9. remote config status
	if msg.RemoteConfigStatus != nil {
		if err := s.processRemoteConfigStatus(ctx, sess, msg.RemoteConfigStatus, pr); err != nil {
			return err
		}
	}
	// ---- 10. available components
	if msg.AvailableComponents != nil {
		if err := s.processComponents(ctx, sess, msg.AvailableComponents, pr); err != nil {
			return err
		}
	}
	// ---- 11. package statuses
	if msg.PackageStatuses != nil {
		if err := s.processPackageStatuses(ctx, sess, msg.PackageStatuses, pr); err != nil {
			return err
		}
	}
	_ = a
	return nil
}

func attrsEqual(a, b map[string]string) bool { return maps.Equal(a, b) }

// processDescription upserts the agent, records registration/version events
// and runs the assignment for collectors.
func (s *Server) processDescription(ctx context.Context, sess *session, ci *connInfo, msg *protobufs.AgentToServer, pr *processResult) error {
	caps := msg.Capabilities
	if caps == 0 && sess.agent != nil {
		caps = sess.agent.Capabilities
	}
	upd := descriptionUpdate(msg.AgentDescription, caps, ci.cred)
	old := sess.agent
	if reserved := reservedAttributeKeys(msg.AgentDescription); len(reserved) > 0 && !sess.reservedWarned {
		// the labels. namespace belongs to the server: such attributes are not stored
		sess.reservedWarned = true
		s.emit(sess.id, domain.AgentEventDescription, "warning", "attributes in the reserved labels. namespace were ignored",
			map[string]any{"keys": limitStrings(reserved, 10)})
	}

	attrsChanged := old == nil || !attrsEqual(old.IdentifyingAttributes, upd.IdentifyingAttributes) || !attrsEqual(old.NonIdentifyingAttributes, upd.NonIdentifyingAttributes)
	changed := old == nil || attrsChanged || old.Kind != upd.Kind || old.ServiceName != upd.ServiceName || old.Version != upd.Version ||
		old.Hostname != upd.Hostname || old.OSType != upd.OSType || old.OSDescription != upd.OSDescription || old.Arch != upd.Arch ||
		old.Capabilities != upd.Capabilities ||
		(upd.EnrollmentTokenID != nil && old.EnrollmentTokenID == nil) ||
		(upd.ClientCertSubject != "" && old.ClientCertSubject == "")

	if changed {
		a, created, err := s.deps.Store.Agents().UpsertDescription(ctx, sess.id, upd)
		if err != nil {
			return fmt.Errorf("upsert description: %w", err)
		}
		sess.agent = a
		switch {
		case created:
			pr.change("created")
			s.emit(sess.id, domain.AgentEventRegistered, "info", fmt.Sprintf("registered as %s %s on host %q", a.Kind, a.Version, a.Hostname),
				map[string]any{"kind": string(a.Kind), "version": a.Version, "hostname": a.Hostname, "remoteAddr": ci.remoteAddr})
		case old != nil && old.Version != a.Version:
			pr.change("version")
			s.emit(sess.id, domain.AgentEventVersionChanged, "info", fmt.Sprintf("version changed from %s to %s", orDash(old.Version), a.Version),
				map[string]any{"from": old.Version, "to": a.Version})
		default:
			pr.change("description")
			s.emit(sess.id, domain.AgentEventDescription, "info", "agent description changed", nil)
		}
		if old == nil {
			// the agent did not exist when establish() ran: connection state and offers now
			if err := s.markOnline(ctx, sess, ci); err != nil {
				s.log.Warn("cannot mark new agent online", "agent_id", sess.id, "error", err)
			}
		}
		if err := s.loadOffers(ctx, sess); err != nil {
			s.log.Warn("cannot load package offers", "agent_id", sess.id, "error", err)
		}
	}
	a := sess.agent

	switch a.Kind {
	case domain.AgentKindCollector:
		if s.deps.Assigner != nil && (old == nil || attrsChanged || a.Assignment.AssignedConfigHash.IsZero()) {
			s.assign(ctx, sess, pr)
		}
	case domain.AgentKindUpdater:
		s.linkUpdater(ctx, sess)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// assign calls the Assigner synchronously (errors are logged, the connection
// stays up) and reloads the agent so that the reply carries the new config.
func (s *Server) assign(ctx context.Context, sess *session, pr *processResult) {
	actx, cancel := context.WithTimeout(ctx, assignTimeout)
	defer cancel()
	before := sess.agent.Assignment.FleetName
	res, err := s.deps.Assigner.AssignAgent(actx, sess.id)
	if err != nil {
		// tolerated: the agent stays connected and unassigned (e.g. while the
		// assignment service is not available yet); it is retried with the next
		// message that carries a description
		s.log.Error("assigning agent failed, agent stays unassigned", "agent_id", sess.id, "error", err)
		return
	}
	a, err := s.deps.Store.Agents().Get(ctx, sess.id)
	if err != nil {
		s.log.Warn("cannot reload agent after assignment", "agent_id", sess.id, "error", err)
		return
	}
	sess.agent = a
	if a.Assignment.FleetName != before || res.DesiredChanged {
		pr.change("assignment")
	}
}

// linkUpdater stores the collector an updater manages.
func (s *Server) linkUpdater(ctx context.Context, sess *session) {
	a := sess.agent
	raw := a.NonIdentifyingAttributes[domain.AttrUpdaterCollectorUID]
	if raw == "" {
		raw = a.IdentifyingAttributes[domain.AttrUpdaterCollectorUID]
	}
	if raw == "" {
		return
	}
	cid, err := uuid.Parse(raw)
	if err != nil {
		s.log.Warn("updater reports an invalid collector instance uid", "agent_id", sess.id, "value", raw)
		return
	}
	if a.Updater != nil && a.Updater.AgentID == cid {
		return
	}
	// An updater may only claim a collector with the same credential (and host).
	// A collector that is not registered yet is verified when it registers: the
	// store only resolves links between agents that belong together.
	if col, err := s.deps.Store.Agents().Get(ctx, cid); err == nil {
		if why := domain.UpdaterMismatch(a, col); why != "" {
			s.log.Warn("updater link refused", "agent_id", sess.id, "collector", cid, "reason", why)
			s.emit(sess.id, domain.AgentEventIdentityConflict, "warning", "link to collector "+cid.String()+" refused: "+why,
				map[string]any{"collector": cid.String()})
			return
		}
	}
	if err := s.deps.Store.Agents().SetUpdaterLink(ctx, sess.id, cid); err != nil {
		s.log.Warn("cannot link updater to collector", "agent_id", sess.id, "collector", cid, "error", err)
		return
	}
	a.Updater = &domain.UpdaterLink{AgentID: cid}
}

func (s *Server) processHealth(ctx context.Context, sess *session, h *protobufs.ComponentHealth, pr *processResult) error {
	a := sess.agent
	nh := healthFromProto(h, s.now())
	if healthEqual(a.Health, nh) {
		return nil
	}
	if err := s.deps.Store.Agents().SetHealth(ctx, sess.id, nh); err != nil {
		return fmt.Errorf("set health: %w", err)
	}
	prev := a.Health
	a.Health = nh
	pr.change("health")
	switch {
	case prev != nil && prev.Healthy != nh.Healthy:
		sev, msg := "info", "agent became healthy"
		if !nh.Healthy {
			sev, msg = "warning", "agent became unhealthy"
		}
		if nh.Status != "" {
			msg += " (" + nh.Status + ")"
		}
		s.emit(sess.id, domain.AgentEventHealthChanged, sev, msg, map[string]any{"healthy": nh.Healthy, "status": nh.Status, "lastError": nh.LastError})
	}
	return nil
}

func (s *Server) processEffectiveConfig(ctx context.Context, sess *session, ec *protobufs.EffectiveConfig, pr *processResult) error {
	body, ct, ok := effectiveConfigBody(ec)
	if !ok || len(body) == 0 {
		return nil
	}
	hash := domain.SHA256(body)
	a := sess.agent
	if hash.Equal(a.EffectiveConfigHash) {
		return nil
	}
	if limit := s.cfg.MaxEffectiveConfigBytes; limit > 0 && int64(len(body)) > limit {
		// not stored; one warning event per distinct oversized body
		if !hash.Equal(sess.oversizedConfig) {
			sess.oversizedConfig = hash
			s.emit(sess.id, domain.AgentEventDescription, "warning",
				fmt.Sprintf("effective config of %d bytes exceeds the limit of %d bytes and was not stored", len(body), limit),
				map[string]any{"size": len(body), "limit": limit})
		}
		return nil
	}
	if !sess.storeBudget.Allow() {
		s.log.Debug("effective config not stored: agent changes it too often", "agent_id", sess.id)
		return nil
	}
	now := s.now()
	if err := s.deps.Store.Configs().PutEffective(ctx, domain.EffectiveConfig{Hash: hash, Body: body, ContentType: ct, CreatedAt: now}); err != nil {
		return fmt.Errorf("store effective config: %w", err)
	}
	if err := s.deps.Store.Agents().SetEffectiveConfig(ctx, sess.id, hash, now); err != nil {
		return fmt.Errorf("set effective config: %w", err)
	}
	a.EffectiveConfigHash, a.EffectiveConfigUpdatedAt = hash, &now
	pr.change("effective_config")
	return nil
}

func (s *Server) processRemoteConfigStatus(ctx context.Context, sess *session, rcs *protobufs.RemoteConfigStatus, pr *processResult) error {
	a := sess.agent
	st := remoteConfigStatusFromProto(rcs)
	prev := a.RemoteConfig
	if prev.Status == st.Status && prev.LastHash.Equal(st.LastHash) && prev.ErrorMessage == st.ErrorMessage {
		// An identical FAILED report after the marker was cleared (retry-failed,
		// resync) is a new failure of the retry: mark the hash again, or it would
		// be offered forever.
		if st.Status == domain.RemoteConfigFailed {
			d := a.Assignment.DesiredConfigHash
			if !d.IsZero() && st.LastHash.Equal(d) && !a.Assignment.FailedConfigHash.Equal(d) {
				if err := s.deps.Store.Agents().SetFailedConfig(ctx, sess.id, d); err != nil {
					return fmt.Errorf("set failed config: %w", err)
				}
				a.Assignment.FailedConfigHash = d
				pr.change("remote_config")
			}
		}
		return nil
	}
	now := s.now()
	st.UpdatedAt = &now
	if err := s.deps.Store.Agents().SetRemoteConfigState(ctx, sess.id, st); err != nil {
		return fmt.Errorf("set remote config state: %w", err)
	}
	a.RemoteConfig = st
	pr.change("remote_config")

	desired := a.Assignment.DesiredConfigHash
	switch st.Status {
	case domain.RemoteConfigFailed:
		// the offer is finished; a retry must not wait for the re-offer guard
		sess.offeredConfig = nil
		if !desired.IsZero() && st.LastHash.Equal(desired) {
			// never offer this hash again automatically (apply/rollback loops with
			// the supervisor's automatic_config_rollback)
			if err := s.deps.Store.Agents().SetFailedConfig(ctx, sess.id, desired); err != nil {
				return fmt.Errorf("set failed config: %w", err)
			}
			a.Assignment.FailedConfigHash = desired
		}
		s.emit(sess.id, domain.AgentEventConfigFailed, "error", fmt.Sprintf("config %s failed: %s", st.LastHash.Short(), st.ErrorMessage),
			map[string]any{"hash": st.LastHash.String(), "error": st.ErrorMessage, "wasDesired": !desired.IsZero() && st.LastHash.Equal(desired)})
	case domain.RemoteConfigApplied:
		if prev.Status != domain.RemoteConfigApplied || !prev.LastHash.Equal(st.LastHash) {
			s.emit(sess.id, domain.AgentEventConfigApplied, "info", fmt.Sprintf("config %s applied", st.LastHash.Short()),
				map[string]any{"hash": st.LastHash.String()})
		}
	}
	return nil
}

func (s *Server) processComponents(ctx context.Context, sess *session, ac *protobufs.AvailableComponents, pr *processResult) error {
	a := sess.agent
	hash := cleanHash(ac.GetHash())
	if len(hash) == 0 {
		return nil
	}
	key := hash.String()
	if len(ac.GetComponents()) > 0 {
		if !sess.storeBudget.Allow() {
			s.log.Debug("component set not stored: agent changes it too often", "agent_id", sess.id)
			return nil
		}
		cs := componentSetFromProto(ac)
		if err := s.deps.Store.Configs().PutComponentSet(ctx, cs); err != nil {
			return fmt.Errorf("store component set: %w", err)
		}
		s.knownCS.Add(key, struct{}{})
		if !a.AvailableComponentsHash.Equal(hash) {
			if err := s.deps.Store.Agents().SetAvailableComponentsHash(ctx, sess.id, hash); err != nil {
				return fmt.Errorf("set components hash: %w", err)
			}
			a.AvailableComponentsHash = domain.Hash(append([]byte(nil), hash...))
			pr.change("components")
			s.assignAfterComponents(ctx, sess, pr)
		}
		return nil
	}
	// hash only: ask for the details if the set is unknown
	if _, known := s.knownCS.Get(key); !known {
		has, err := s.deps.Store.Configs().HasComponentSet(ctx, hash)
		if err != nil {
			return fmt.Errorf("check component set: %w", err)
		}
		if !has {
			pr.reportComponents = true
			return nil
		}
		s.knownCS.Add(key, struct{}{})
	}
	if !a.AvailableComponentsHash.Equal(hash) {
		if err := s.deps.Store.Agents().SetAvailableComponentsHash(ctx, sess.id, hash); err != nil {
			return fmt.Errorf("set components hash: %w", err)
		}
		a.AvailableComponentsHash = domain.Hash(append([]byte(nil), hash...))
		pr.change("components")
		s.assignAfterComponents(ctx, sess, pr)
	}
	return nil
}

// assignAfterComponents re-runs the assignment when the component set of a
// collector changed. The description (step 6) is processed before the
// available components (step 10) and a new collector version changes the set,
// so the assignment that ran for the description validated the blueprint
// against the PREVIOUS version's components (e.g. a receiver that only exists
// since the new version, or one that was removed). Without this, a render
// error caused by the old set stays until an unrelated commit.
func (s *Server) assignAfterComponents(ctx context.Context, sess *session, pr *processResult) {
	if s.deps.Assigner == nil || sess.agent == nil || sess.agent.Kind != domain.AgentKindCollector {
		return
	}
	s.assign(ctx, sess, pr)
}

func (s *Server) processPackageStatuses(ctx context.Context, sess *session, ps *protobufs.PackageStatuses, pr *processResult) error {
	a := sess.agent
	np := packageStatusesFromProto(ps, s.now())
	sess.reportedPkgHash = np.ServerProvidedAllPackagesHash
	old := a.PackageStatus
	if old != nil && bytes.Equal(old.ServerProvidedAllPackagesHash, np.ServerProvidedAllPackagesHash) && old.ErrorMessage == np.ErrorMessage &&
		packagesEqual(old.Packages, np.Packages) {
		return nil
	}
	if err := s.deps.Store.Agents().SetPackageStatuses(ctx, sess.id, np); err != nil {
		return fmt.Errorf("set package statuses: %w", err)
	}
	a.PackageStatus = np
	pr.change("packages")
	for name, p := range np.Packages {
		var prev *domain.PackageStatus
		if old != nil {
			prev = old.Packages[name]
		}
		if !packageStatusChanged(prev, p) {
			continue
		}
		sev := "info"
		if p.Status == domain.PackageInstallFailed {
			sev = "error"
		}
		msg := fmt.Sprintf("package %q: %s", displayPackage(name), p.Status)
		if p.ErrorMessage != "" {
			msg += ": " + p.ErrorMessage
		}
		s.emit(sess.id, domain.AgentEventPackageStatus, sev, msg, map[string]any{
			"package": name, "status": string(p.Status), "agentHasVersion": p.AgentHasVersion, "offeredVersion": p.ServerOfferedVersion, "error": p.ErrorMessage})
	}
	return nil
}

func displayPackage(n string) string {
	if n == "" {
		return "collector"
	}
	return n
}

// packagesEqual compares package statuses including download progress
// rounded to whole percents (progress updates would otherwise write on every
// report).
func packagesEqual(a, b map[string]*domain.PackageStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || x == nil || y == nil {
			return false
		}
		if x.Status != y.Status || x.ErrorMessage != y.ErrorMessage || x.AgentHasVersion != y.AgentHasVersion || x.ServerOfferedVersion != y.ServerOfferedVersion ||
			!x.AgentHasHash.Equal(y.AgentHasHash) || !x.ServerOfferedHash.Equal(y.ServerOfferedHash) || int(x.DownloadPercent) != int(y.DownloadPercent) {
			return false
		}
	}
	return true
}

var (
	_ = time.Second
	_ store.AgentStore
)
