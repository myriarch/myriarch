// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// Plan (leader only) creates rollouts for pending desired-state changes
// (called after GitOps snapshots and periodically). Runs are serialized.
func (e *Engine) Plan(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, cycleBudget)
	defer cancel()
	defer e.cycle("rollout_plan")()
	return e.plan(ctx)
}

func (e *Engine) plan(ctx context.Context) error {
	snap, err := e.deps.Store.GitOps().GetSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load gitops snapshot: %w", err)
	}
	e.setFleets(snap)
	// Rules K4, K1, K2a, K2b between the UI selections and Git come first: the
	// planner works on the effective targets they leave behind. A failure must
	// not stop planning (the next run repeats it).
	var errs []error
	if err := e.reconcileSelections(ctx, snap); err != nil {
		errs = append(errs, err)
	}
	if len(snap.Fleets) == 0 {
		return errors.Join(errs...)
	}
	selections, err := e.deps.Store.FleetVersions().List(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list version selections: %w", err))...)
	}
	selByFleet := make(map[string]*domain.FleetVersionSelection, len(selections))
	for i := range selections {
		selByFleet[selections[i].Fleet] = &selections[i]
	}
	agents, err := e.deps.Store.Agents().ListAll(ctx, store.AgentFilter{Kinds: []domain.AgentKind{domain.AgentKindCollector}})
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	byFleet := map[string][]*domain.Agent{}
	for i := range agents {
		a := &agents[i]
		if a.Assignment.FleetName != "" {
			byFleet[a.Assignment.FleetName] = append(byFleet[a.Assignment.FleetName], a)
		}
	}
	fleets := append([]domain.Fleet(nil), snap.Fleets...)
	sort.SliceStable(fleets, func(i, j int) bool { return fleets[i].Name < fleets[j].Name })

	for i := range fleets {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := &fleets[i]
		sel := selByFleet[f.Name]
		vt := domain.ResolveVersionTarget(f, sel)
		e.syncTargetVersion(ctx, f, vt)
		versionOK := true
		for _, kind := range []domain.RolloutKind{domain.RolloutKindConfig, domain.RolloutKindVersion} {
			if err := e.planFleetKind(ctx, snap, f, kind, vt, byFleet[f.Name]); err != nil {
				e.log.Error("plan rollout failed", "fleet", f.Name, "kind", kind, "error", err)
				errs = append(errs, fmt.Errorf("fleet %s %s: %w", f.Name, kind, err))
				if kind == domain.RolloutKindVersion {
					versionOK = false
				}
			}
		}
		if versionOK {
			e.markPlanned(ctx, f, sel, vt)
			e.markTargetPlanned(ctx, f, vt)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) setFleets(snap *domain.GitOpsSnapshot) {
	m := make(map[string]struct{}, len(snap.Fleets))
	for _, f := range snap.Fleets {
		m[f.Name] = struct{}{}
	}
	e.fleetsMu.Lock()
	e.fleets = m
	e.fleetsMu.Unlock()
}

// plannedTarget is an agent that needs a change.
type plannedTarget struct {
	agent *domain.Agent
	from  string
	to    string
}

func versionOf(a *domain.Agent) string { return domain.NormalizeVersion(a.Version) }

// relevant lists the member agents whose desired state differs (docs 7.5
// "Ziel je Agent"), before exclusion rules.
func relevantTargets(kind domain.RolloutKind, f *domain.Fleet, vt domain.VersionTarget, members []*domain.Agent) []plannedTarget {
	var out []plannedTarget
	switch kind {
	case domain.RolloutKindConfig:
		for _, a := range members {
			as := a.Assignment
			if as.Error != "" || as.AssignedConfigHash.IsZero() || as.DesiredConfigHash.IsZero() {
				continue
			}
			if as.DesiredConfigHash.Equal(as.AssignedConfigHash) {
				continue
			}
			out = append(out, plannedTarget{agent: a, from: as.DesiredConfigHash.String(), to: as.AssignedConfigHash.String()})
		}
	case domain.RolloutKindVersion:
		if !versionRolloutsEnabled(f, vt) {
			return nil
		}
		want := vt.Version
		for _, a := range members {
			if a.Assignment.Error != "" || versionOf(a) == "" || versionOf(a) == want {
				continue
			}
			out = append(out, plannedTarget{agent: a, from: versionOf(a), to: want})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].agent.ID.String() < out[j].agent.ID.String() })
	return out
}

// versionRolloutsEnabled: the fleet manages versions through an executor and
// has an effective target (a fleet on hold or without a target has none).
func versionRolloutsEnabled(f *domain.Fleet, vt domain.VersionTarget) bool {
	return domain.VersionRolloutsEnabled(f) && vt.Version != ""
}

// currentTarget is the goal of an agent as of now (what an open target must
// still match to stay valid).
func currentTarget(kind domain.RolloutKind, f *domain.Fleet, vt domain.VersionTarget, a *domain.Agent) string {
	if kind == domain.RolloutKindConfig {
		return a.Assignment.AssignedConfigHash.String()
	}
	if !versionRolloutsEnabled(f, vt) {
		return ""
	}
	return vt.Version
}

type decision int

const (
	decRelevant decision = iota
	decExcluded
	decCatchUp
)

// classify applies the exclusion rules (docs 7.5) using the latest target of
// the agent for the same goal.
func (e *Engine) classify(ctx context.Context, kind domain.RolloutKind, vt domain.VersionTarget, pt plannedTarget) (decision, error) {
	ref, err := e.deps.Store.Rollouts().LatestTargetForAgent(ctx, pt.agent.ID, kind, pt.to)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return decRelevant, nil
		}
		return decRelevant, fmt.Errorf("latest target of agent %s: %w", pt.agent.ID, err)
	}
	if kind == domain.RolloutKindVersion && vt.Source == domain.TargetSourceUI && vt.Selection != nil {
		// An operator who selects the version again (clear + select, or another
		// version and back) asks anew: what ended before that selection does not
		// exclude the agent. A target that ended after it (the rollout of this
		// very selection failed, was rolled back or aborted) still does.
		ended := ref.Target.UpdatedAt
		if ref.Target.FinishedAt != nil {
			ended = *ref.Target.FinishedAt
		}
		if ended.Before(vt.Selection.SelectedAt) {
			return decRelevant, nil
		}
	}
	switch ref.Target.Status {
	case domain.TargetFailed, domain.TargetRolledBack:
		return decExcluded, nil
	case domain.TargetCancelled:
		// Aborted by an operator, or never started because the rollout was
		// rolled back (manually or by the failure policy): the agent must not
		// be pushed the rejected goal again until Git produces a new one
		// (docs 7.4: agents stay on drift until Git changes).
		switch ref.RolloutStatus {
		case domain.RolloutAborted, domain.RolloutRollingBack, domain.RolloutRolledBack:
			return decExcluded, nil
		}
	case domain.TargetSkipped:
		if kind == domain.RolloutKindConfig && ref.RolloutStatus == domain.RolloutCompleted {
			return decCatchUp, nil
		}
		// A version target that was skipped because the agent stayed offline
		// (closed site, edge node switched off at night) would create a new rollout
		// after every completed one, with started/completed notifications each
		// time, for as long as the agent is away. Wait until the agent has
		// connected since the target was skipped.
		if kind == domain.RolloutKindVersion && ref.RolloutStatus == domain.RolloutCompleted {
			if seen := pt.agent.Connection.LastSeenAt; seen == nil || !seen.After(ref.Target.UpdatedAt) {
				return decExcluded, nil
			}
		}
	}
	return decRelevant, nil
}

// classifyAll classifies in parallel (bounded) and returns the relevant
// targets and the catch-up candidates.
func (e *Engine) classifyAll(ctx context.Context, kind domain.RolloutKind, vt domain.VersionTarget, cands []plannedTarget) (relevant, catchUp []plannedTarget, err error) {
	decs := make([]decision, len(cands))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i := range cands {
		g.Go(func() error {
			d, err := e.classify(gctx, kind, vt, cands[i])
			decs[i] = d
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	for i, d := range decs {
		switch d {
		case decRelevant:
			relevant = append(relevant, cands[i])
		case decCatchUp:
			catchUp = append(catchUp, cands[i])
		}
	}
	return relevant, catchUp, nil
}

func (e *Engine) planFleetKind(ctx context.Context, snap *domain.GitOpsSnapshot, f *domain.Fleet, kind domain.RolloutKind, vt domain.VersionTarget, members []*domain.Agent) error {
	rel := relevantTargets(kind, f, vt, members)

	active, _, err := e.deps.Store.Rollouts().List(ctx, store.RolloutFilter{Fleets: []string{f.Name}, Kinds: []domain.RolloutKind{kind}, ActiveOnly: true})
	if err != nil {
		return fmt.Errorf("list active rollouts: %w", err)
	}
	// Anomaly: more than one active rollout. Keep the newest, supersede the rest.
	for i := 1; i < len(active); i++ {
		if _, err := e.supersede(ctx, snap, f, &active[i], "more than one active rollout for the fleet"); err != nil {
			return err
		}
	}
	var cur *domain.Rollout
	if len(active) > 0 {
		cur = &active[0]
	}

	byID := make(map[domain.AgentID]*domain.Agent, len(members))
	for _, a := range members {
		byID[a.ID] = a
	}

	if cur != nil {
		switch cur.Status {
		case domain.RolloutFailed, domain.RolloutRollingBack:
			e.log.Debug("planning blocked by rollout that needs operator attention", "rollout_id", cur.ID, "status", cur.Status)
			return nil
		}
		// Step 1: open targets with an outdated goal -> supersede.
		statuses := []domain.TargetStatus{domain.TargetPending, domain.TargetWaitingWindow, domain.TargetInProgress, domain.TargetVerifying}
		if cur.Status == domain.RolloutPaused {
			// A rollout paused by the failure threshold may have no open
			// targets left (e.g. a single failed agent): the fix pushed to
			// the repository must still replace it.
			statuses = append(statuses, domain.TargetFailed)
		}
		open, _, err := e.deps.Store.Rollouts().ListTargets(ctx, cur.ID, store.TargetFilter{Status: statuses})
		if err != nil {
			return fmt.Errorf("list open targets: %w", err)
		}
		reason := ""
		for _, t := range open {
			a, ok := byID[t.AgentID]
			if !ok {
				continue // left the fleet: handled by the tick (skipped)
			}
			if want := currentTarget(kind, f, vt, a); want != t.ToRef {
				reason = fmt.Sprintf("goal of agent %s changed from %s to %s", a.Hostname, shortRef(kind, t.ToRef), shortRef(kind, want))
				switch {
				case kind == domain.RolloutKindVersion && want == "":
					reason = "the fleet no longer manages the collector version"
				case kind == domain.RolloutKindVersion && vt.Source == domain.TargetSourceUI:
					reason = fmt.Sprintf("the target version was selected in the UI (%s)", want)
				}
				break
			}
		}
		if reason != "" {
			ok, err := e.supersede(ctx, snap, f, cur, reason)
			if err != nil {
				return err
			}
			if !ok {
				return nil // changed concurrently: re-evaluate in the next run
			}
			cur = nil
		} else {
			// Step 2: late joiners.
			return e.addLateJoiners(ctx, f, kind, vt, cur, rel)
		}
	}

	// Step 3: new rollout (and catch-up).
	relevant, catchUp, err := e.classifyAll(ctx, kind, vt, rel)
	if err != nil {
		return err
	}
	if kind == domain.RolloutKindConfig && len(catchUp) > 0 {
		if err := e.catchUp(ctx, catchUp); err != nil {
			return err
		}
	}
	if len(relevant) == 0 {
		return nil
	}
	return e.createRollout(ctx, snap, f, kind, vt, relevant)
}

// catchUp releases agents that were skipped by a completed config rollout:
// desired = assigned (docs 5.3).
func (e *Engine) catchUp(ctx context.Context, cands []plannedTarget) error {
	groups := map[string][]domain.AgentID{}
	hashes := map[string]domain.Hash{}
	var all []domain.AgentID
	for _, c := range cands {
		groups[c.to] = append(groups[c.to], c.agent.ID)
		hashes[c.to] = c.agent.Assignment.AssignedConfigHash
		all = append(all, c.agent.ID)
	}
	for to, ids := range groups {
		if err := e.deps.Store.Agents().SetDesiredConfig(ctx, ids, hashes[to]); err != nil {
			return fmt.Errorf("catch-up desired config: %w", err)
		}
	}
	if e.deps.Control != nil {
		if err := e.deps.Control.DesiredStateChanged(ctx, all); err != nil {
			return fmt.Errorf("catch-up push: %w", err)
		}
	}
	e.log.Info("catch-up for agents skipped by a completed config rollout", "agents", len(all))
	return nil
}

// addLateJoiners adds relevant agents without target to the last wave.
func (e *Engine) addLateJoiners(ctx context.Context, f *domain.Fleet, kind domain.RolloutKind, vt domain.VersionTarget, cur *domain.Rollout, rel []plannedTarget) error {
	if len(rel) == 0 {
		return nil
	}
	existing, _, err := e.deps.Store.Rollouts().ListTargets(ctx, cur.ID, store.TargetFilter{})
	if err != nil {
		return fmt.Errorf("list targets: %w", err)
	}
	have := make(map[domain.AgentID]struct{}, len(existing))
	for _, t := range existing {
		have[t.AgentID] = struct{}{}
	}
	var cands []plannedTarget
	for _, pt := range rel {
		if _, ok := have[pt.agent.ID]; !ok {
			cands = append(cands, pt)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	relevant, catchUp, err := e.classifyAll(ctx, kind, vt, cands)
	if err != nil {
		return err
	}
	if kind == domain.RolloutKindConfig && len(catchUp) > 0 {
		if err := e.catchUp(ctx, catchUp); err != nil {
			return err
		}
	}
	if len(relevant) == 0 {
		return nil
	}
	full, err := e.deps.Store.Rollouts().Get(ctx, cur.ID)
	if err != nil {
		return fmt.Errorf("load rollout: %w", err)
	}
	if full.Status.IsTerminal() || full.Status == domain.RolloutFailed || full.Status == domain.RolloutRollingBack || len(full.Waves) == 0 {
		return nil
	}
	last := len(full.Waves) - 1
	now := e.now()
	targets := make([]domain.RolloutTarget, 0, len(relevant))
	for _, pt := range relevant {
		targets = append(targets, domain.RolloutTarget{
			RolloutID: full.ID, AgentID: pt.agent.ID, Hostname: pt.agent.Hostname, Wave: last,
			Status: domain.TargetPending, FromRef: pt.from, ToRef: pt.to, Attempts: 1, UpdatedAt: now,
		})
	}
	if err := e.deps.Store.Rollouts().AddTargets(ctx, full.ID, targets); err != nil {
		return fmt.Errorf("add late joiners: %w", err)
	}
	// Re-open the last wave if it was already finished or empty.
	lw := &full.Waves[last]
	if lw.Status == domain.WaveCompleted || lw.Status == domain.WaveSkipped || lw.Status == domain.WaveFailed {
		if lw.Status == domain.WaveSkipped {
			lw.Status = domain.WavePending
		} else {
			lw.Status, lw.FinishedAt = domain.WaveRunning, nil
			if lw.StartedAt == nil {
				lw.StartedAt = &now
			}
		}
		if full.CurrentWave > last || lw.Status == domain.WaveRunning {
			full.CurrentWave = last
		}
		if err := e.deps.Store.Rollouts().UpdateProgress(ctx, full); err != nil {
			return fmt.Errorf("reopen last wave: %w", err)
		}
	}
	e.log.Info("added late joiners to rollout", "rollout_id", full.ID, "fleet", f.Name, "count", len(targets), "wave", last)
	e.publishChanged(ctx, full)
	return nil
}

// supersede ends a rollout because a newer goal exists. Returns false if the
// rollout changed concurrently.
func (e *Engine) supersede(ctx context.Context, snap *domain.GitOpsSnapshot, f *domain.Fleet, r *domain.Rollout, reason string) (bool, error) {
	// List results omit policy and annotations: load the full rollout.
	full, err := e.deps.Store.Rollouts().Get(ctx, r.ID)
	if err != nil {
		return false, fmt.Errorf("load rollout %s: %w", r.ID, err)
	}
	r = full
	ok, err := e.casStatus(ctx, r, []domain.RolloutStatus{domain.RolloutPending, domain.RolloutRunning, domain.RolloutWaitingApproval, domain.RolloutPaused},
		domain.RolloutSuperseded, "superseded: "+reason)
	if err != nil || !ok {
		return false, err
	}
	e.auditSystem(ctx, "rollout.supersede", r, map[string]any{"reason": reason})
	if err := e.cancelOpenTargets(ctx, r, "superseded: "+reason, true); err != nil {
		e.log.Warn("cancel targets of superseded rollout failed", "rollout_id", r.ID, "error", err)
	}
	e.publishChanged(ctx, r)
	return true, nil
}

// cancelOpenTargets cancels pending/waiting targets and (inFlight) also
// in-progress/verifying ones; version rollouts additionally withdraw the
// executor's updates for the affected agents.
func (e *Engine) cancelOpenTargets(ctx context.Context, r *domain.Rollout, reason string, inFlight bool) error {
	rs := e.deps.Store.Rollouts()
	var cancelIDs []domain.AgentID
	refs := map[string]any{}
	if r.Kind == domain.RolloutKindVersion {
		open, _, err := rs.ListTargets(ctx, r.ID, store.TargetFilter{
			Status: []domain.TargetStatus{domain.TargetPending, domain.TargetWaitingWindow, domain.TargetInProgress, domain.TargetVerifying},
		})
		if err != nil {
			return fmt.Errorf("list open targets: %w", err)
		}
		for _, t := range open {
			cancelIDs = append(cancelIDs, t.AgentID)
			if t.ExecutorRef != "" && t.ExecutorRef != "started" {
				refs[t.AgentID.String()] = t.ExecutorRef
			}
		}
	}
	if _, err := rs.CancelOpenTargets(ctx, r.ID, reason); err != nil {
		return fmt.Errorf("cancel open targets: %w", err)
	}
	if inFlight {
		ts, _, err := rs.ListTargets(ctx, r.ID, store.TargetFilter{Status: []domain.TargetStatus{domain.TargetInProgress, domain.TargetVerifying}})
		if err != nil {
			return fmt.Errorf("list in-flight targets: %w", err)
		}
		now := e.now()
		for i := range ts {
			ts[i].Status, ts[i].LastError, ts[i].FinishedAt = domain.TargetCancelled, reason, &now
			if p := domain.ProgressOf(ts[i]); p != nil { // freeze what was known
				p.UpdatedAt = now
				ts[i].Progress = p
			}
		}
		if len(ts) > 0 {
			if _, err := rs.UpdateTargets(ctx, ts); err != nil {
				return fmt.Errorf("cancel in-flight targets: %w", err)
			}
		}
	}
	if r.Kind == domain.RolloutKindVersion && len(cancelIDs) > 0 {
		e.cancelExecutor(ctx, r, cancelIDs, refs)
	}
	return nil
}

// cancelExecutor withdraws updates at the executor (best effort). refs maps
// the cancelled agents to the executor references (AWX job ids) of their
// targets (agent id string -> ref).
func (e *Engine) cancelExecutor(ctx context.Context, r *domain.Rollout, ids []domain.AgentID, refs map[string]any) {
	ex, ok := e.deps.Executors.Get(r.Executor)
	if !ok {
		return
	}
	spec, def := e.updateSpec(ctx, r)
	var typed map[domain.AgentID]string
	if len(refs) > 0 {
		typed = make(map[domain.AgentID]string, len(refs))
		for _, id := range ids {
			if ref, ok := refs[id.String()].(string); ok {
				typed[id] = ref
			}
		}
	}
	req := executor.CancelRequest{RolloutID: r.ID, AgentIDs: ids, Refs: typed, Spec: spec, Definition: def}
	// Best effort and bounded: a hanging receiver must not stall the planner
	// (which holds the engine lock) or an operator's request.
	e.bestEffort(ctx, func(cctx context.Context) {
		if err := ex.Cancel(cctx, req); err != nil {
			e.log.Warn("executor cancel failed", "rollout_id", r.ID, "executor", r.Executor, "error", err)
		}
	})
}

// updateSpec rebuilds the fleet's update spec (and the executor object) from
// the annotations persisted with the rollout.
func (e *Engine) updateSpec(ctx context.Context, r *domain.Rollout) (domain.UpdateSpec, *domain.Executor) {
	spec := domain.UpdateSpec{Executor: r.Executor}
	if v, ok := r.Annotations[annExecutorRef].(string); ok {
		spec.ExecutorRef = v
	}
	if m, ok := r.Annotations[annUpdateParams].(map[string]any); ok {
		spec.Params = m
	}
	var def *domain.Executor
	if spec.ExecutorRef != "" {
		d, err := e.deps.Store.GitOps().GetExecutor(ctx, spec.ExecutorRef)
		if err == nil {
			def = d
		}
	}
	return spec, def
}

// ----------------------------------------------------------------- create ----

func (e *Engine) policyFor(snap *domain.GitOpsSnapshot, f *domain.Fleet, kind domain.RolloutKind, vt domain.VersionTarget) (string, domain.RolloutPolicy) {
	name := f.Rollout.ConfigPolicy
	if kind == domain.RolloutKindVersion {
		name = f.Rollout.VersionPolicy
		// a rollout that a UI selection caused uses the policy of the frame
		if vt.Source == domain.TargetSourceUI && f.Update.Selection != nil && f.Update.Selection.RolloutPolicy != "" {
			name = f.Update.Selection.RolloutPolicy
		}
	}
	if name == "" {
		name = domain.DefaultRolloutPolicyName
	}
	for _, p := range snap.RolloutPolicies {
		if p.Name == name {
			return name, p
		}
	}
	if name != domain.DefaultRolloutPolicyName {
		e.log.Warn("rollout policy not found, using the built-in default", "fleet", f.Name, "policy", name)
	}
	return name, domain.DefaultRolloutPolicy()
}

func (e *Engine) createRollout(ctx context.Context, snap *domain.GitOpsSnapshot, f *domain.Fleet, kind domain.RolloutKind, vt domain.VersionTarget, relevant []plannedTarget) error {
	policyName, policy := e.policyFor(snap, f, kind, vt)
	e.warnServerTimezoneOnce(policyName, policy.RolloutPolicySpec)
	spec := policy.RolloutPolicySpec.WithDefaults(kind, e.cfg.DefaultTimezone)

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new rollout id: %w", err)
	}
	cands := make([]waveCandidate, len(relevant))
	for i, pt := range relevant {
		cands[i] = waveCandidate{AgentID: pt.agent.ID, View: domain.NewAttributeView(pt.agent)}
	}
	waveOf, counts := assignWaves(id, spec.Waves, cands)
	waves := newWaves(spec.Waves, counts)
	first := 0
	for first < len(waves) && waves[first].Status == domain.WaveSkipped {
		first++
	}
	if first >= len(waves) {
		return nil
	}

	now := e.now()
	targets := make([]domain.RolloutTarget, 0, len(relevant))
	versions := map[string]struct{}{}
	for _, pt := range relevant {
		targets = append(targets, domain.RolloutTarget{
			RolloutID: id, AgentID: pt.agent.ID, Hostname: pt.agent.Hostname, Wave: waveOf[pt.agent.ID],
			Status: domain.TargetPending, FromRef: pt.from, ToRef: pt.to, Attempts: 1, UpdatedAt: now,
		})
		versions[pt.from] = struct{}{}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Wave != targets[j].Wave {
			return targets[i].Wave < targets[j].Wave
		}
		return targets[i].AgentID.String() < targets[j].AgentID.String()
	})

	r := &domain.Rollout{
		ID: id, Kind: kind, FleetName: f.Name, PolicyName: policyName,
		Policy:    domain.RolloutPolicy{ObjectMeta: policy.ObjectMeta, RolloutPolicySpec: spec, Source: policy.Source},
		Status:    domain.RolloutPending,
		GitCommit: snap.Commit, CurrentWave: first, Waves: waves,
		CreatedBy: "gitops", Origin: domain.OriginGitOps, CreatedAt: now, UpdatedAt: now, Annotations: map[string]any{},
	}
	switch kind {
	case domain.RolloutKindConfig:
		r.ToRef = snap.Commit
		r.FromRef = e.previousCommit(ctx, f.Name)
	case domain.RolloutKindVersion:
		r.ToRef = relevant[0].to
		if len(versions) == 1 {
			r.FromRef = relevant[0].from
		}
		r.Executor = f.Update.Executor
		r.Annotations[annExecutorRef] = f.Update.ExecutorRef
		if len(f.Update.Params) > 0 {
			r.Annotations[annUpdateParams] = f.Update.Params
		}
		dist := f.Collector.Distribution
		if dist == "" {
			dist = domain.DistributionContrib
		}
		r.Annotations[annDistribution] = dist
		if vt.Source == domain.TargetSourceUI && vt.Selection != nil {
			// an operator chose the target version: who, when, why
			r.CreatedBy, r.Origin = vt.Selection.SelectedBy, domain.OriginUI
			r.RequestReason = vt.Selection.Reason
			r.Annotations[domain.SelectionAnnotation] = selectionAnnotation(vt.Selection)
		}
	}
	needsApproval := waves[first].RequireApproval
	if needsApproval {
		r.Waves[first].Status = domain.WaveWaitingApproval
	}

	if err := e.deps.Store.Rollouts().Create(ctx, r, targets); err != nil {
		return fmt.Errorf("create rollout: %w", err)
	}
	r.Counts = domain.RolloutCounts{Total: len(targets), Pending: len(targets)}
	e.log.Info("rollout created", "rollout_id", id, "fleet", f.Name, "kind", kind, "to", shortRef(kind, r.ToRef), "targets", len(targets), "waves", len(waves), "policy", policyName)
	ev := e.createAudit(ctx, r, map[string]any{
		"fleet": f.Name, "kind": string(kind), "toRef": r.ToRef, "commit": snap.Commit, "targets": len(targets), "policy": policyName,
	})
	ev.Time = now
	e.audit(ctx, ev)
	e.met.transitions.WithLabelValues(string(domain.RolloutPending)).Inc()

	// Start it.
	startNote := ""
	if r.Origin == domain.OriginUI {
		startNote = e.selectionNote(ctx, r)
	}
	switch {
	case needsApproval:
		if ok, err := e.casStatus(ctx, r, []domain.RolloutStatus{domain.RolloutPending}, domain.RolloutWaitingApproval,
			fmt.Sprintf("wave %q waits for approval", waves[first].Name)); err != nil {
			return err
		} else if ok {
			e.notifyRollout(ctx, r, notify.EventRolloutStarted, notify.SeverityInfo, rolloutStartedTitle(r), rolloutStartedSummary(r, len(targets), startNote), "")
			e.notifyRollout(ctx, r, notify.EventRolloutWaitingApprove, notify.SeverityWarning,
				fmt.Sprintf("Rollout waits for approval: %s", f.Name),
				fmt.Sprintf("Wave %q of the %s rollout for fleet %s needs an approval.", waves[first].Name, kind, f.Name), fmt.Sprint(first),
				notify.Field{Name: "Wave", Value: waves[first].Name})
		}
	case e.cfg.AutoStart:
		if ok, err := e.casStatus(ctx, r, []domain.RolloutStatus{domain.RolloutPending}, domain.RolloutRunning, "started automatically"); err != nil {
			return err
		} else if ok {
			e.notifyRollout(ctx, r, notify.EventRolloutStarted, notify.SeverityInfo, rolloutStartedTitle(r), rolloutStartedSummary(r, len(targets), startNote), "")
		}
	}
	e.publishChanged(ctx, r)
	return nil
}

func rolloutStartedTitle(r *domain.Rollout) string {
	return fmt.Sprintf("Rollout started: %s (%s)", r.FleetName, r.Kind)
}

func rolloutStartedSummary(r *domain.Rollout, targets int, note string) string {
	what := "configuration " + shortRef(r.Kind, r.ToRef)
	if r.Kind == domain.RolloutKindVersion {
		what = "collector version " + r.ToRef
	}
	s := fmt.Sprintf("Rolling out %s to %d agent(s) of fleet %s in %d wave(s) (%s).", what, targets, r.FleetName, len(r.Waves), strings.TrimSpace(r.PolicyName))
	if note != "" {
		s += " Version " + note + "."
	}
	return s
}

// previousCommit returns the commit of the fleet's last completed config
// rollout ("" if none).
func (e *Engine) previousCommit(ctx context.Context, fleet string) string {
	list, _, err := e.deps.Store.Rollouts().List(ctx, store.RolloutFilter{
		Fleets: []string{fleet}, Kinds: []domain.RolloutKind{domain.RolloutKindConfig},
		Status: []domain.RolloutStatus{domain.RolloutCompleted}, Limit: 1,
	})
	if err != nil || len(list) == 0 {
		return ""
	}
	return list[0].ToRef
}
