// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// Action applies an operator action (any replica): validates the
// transition, updates the store (CompareAndSetStatus / SetWaveApproval),
// audits, publishes eventbus.RolloutChanged and returns the updated rollout.
// wave is only used by ActionApprove. Invalid transitions return
// domain.ErrPrecondition.
//
// Actions only use compare-and-set status changes, SetWaveApproval and the
// bulk target statements, never UpdateProgress: the leader's engine owns the
// progress columns, so actions on any replica cannot lose its updates.
func (e *Engine) Action(ctx context.Context, id uuid.UUID, action domain.RolloutAction, wave int, reason string, p *authz.Principal) (*domain.Rollout, error) {
	if p == nil {
		p = authz.System
	}
	rs := e.deps.Store.Rollouts()
	r, err := rs.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("rollout %s: %w", id, err)
	}

	who := principalString(p)
	details := map[string]any{"fleet": r.FleetName, "kind": string(r.Kind), "from": string(r.Status)}
	if reason != "" {
		details["reason"] = reason
	}
	statusReason := func(verb string) string {
		return fmtReason(fmt.Sprintf("%s by %s", verb, who), reason)
	}
	precondition := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{domain.ErrPrecondition}, args...)...)
	}
	cas := func(from []domain.RolloutStatus, to domain.RolloutStatus, verb string) error {
		ok, err := e.casStatus(ctx, r, from, to, statusReason(verb))
		if err != nil {
			return err
		}
		if !ok {
			cur, gerr := rs.Get(ctx, id)
			if gerr == nil {
				r = cur
			}
			return precondition("rollout is %s, cannot %s it", r.Status, strings.ReplaceAll(string(action), "_", "-"))
		}
		return nil
	}

	switch action {
	case domain.ActionPause:
		if err := cas([]domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutPaused, "paused"); err != nil {
			return nil, err
		}

	case domain.ActionResume:
		if err := cas([]domain.RolloutStatus{domain.RolloutPaused, domain.RolloutPending}, domain.RolloutRunning, "resumed"); err != nil {
			return nil, err
		}

	case domain.ActionAbort:
		if err := cas([]domain.RolloutStatus{domain.RolloutPending, domain.RolloutRunning, domain.RolloutWaitingApproval, domain.RolloutPaused, domain.RolloutFailed},
			domain.RolloutAborted, "aborted"); err != nil {
			return nil, err
		}
		if err := e.cancelOpenTargets(ctx, r, statusReason("aborted"), true); err != nil {
			e.log.Warn("abort: cancelling targets failed", "rollout_id", id, "error", err)
		}

	case domain.ActionApprove:
		if wave < 0 || wave >= len(r.Waves) {
			return nil, precondition("rollout has no wave %d", wave)
		}
		w := r.Waves[wave]
		if !w.RequireApproval {
			return nil, precondition("wave %d (%q) does not require approval", wave, w.Name)
		}
		if r.Status.IsTerminal() {
			return nil, precondition("rollout is %s", r.Status)
		}
		ok, err := rs.SetWaveApproval(ctx, id, wave, who, e.now())
		if err != nil {
			return nil, fmt.Errorf("approve wave: %w", err)
		}
		if !ok {
			return nil, precondition("wave %d (%q) is %s and cannot be approved", wave, w.Name, w.Status)
		}
		details["wave"] = wave
		// If the rollout waits exactly for this wave, release it right away; the
		// engine repeats this on its next tick if the CAS loses a race.
		if r.Status == domain.RolloutWaitingApproval && r.CurrentWave == wave {
			if _, err := e.casStatus(ctx, r, []domain.RolloutStatus{domain.RolloutWaitingApproval}, domain.RolloutRunning,
				statusReason(fmt.Sprintf("wave %q approved", w.Name))); err != nil {
				return nil, err
			}
		}

	case domain.ActionRollback:
		if err := cas([]domain.RolloutStatus{domain.RolloutRunning, domain.RolloutPaused, domain.RolloutFailed}, domain.RolloutRollingBack, "rollback requested"); err != nil {
			return nil, err
		}

	case domain.ActionRetryFailed:
		switch r.Status {
		case domain.RolloutRunning, domain.RolloutPaused, domain.RolloutFailed:
		default:
			return nil, precondition("rollout is %s, retry-failed needs running, paused or failed", r.Status)
		}
		n, err := rs.ResetFailedTargets(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("reset failed targets: %w", err)
		}
		details["targets"] = n
		if r.Status == domain.RolloutPaused || r.Status == domain.RolloutFailed {
			if _, err := e.casStatus(ctx, r, []domain.RolloutStatus{domain.RolloutPaused, domain.RolloutFailed}, domain.RolloutRunning,
				statusReason(fmt.Sprintf("retrying %d failed or skipped target(s)", n))); err != nil {
				return nil, err
			}
		}

	default:
		return nil, fmt.Errorf("%w: unknown rollout action %q", domain.ErrInvalidArgument, action)
	}

	updated, err := rs.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reload rollout: %w", err)
	}
	details["to"] = string(updated.Status)
	ev := domain.AuditEvent{
		Time: e.now(), ActorType: p.ActorType(), ActorID: p.ID, ActorName: p.Name,
		Action: "rollout." + string(action), TargetType: "rollout", TargetID: id.String(), Result: "success", Details: details,
	}
	e.audit(ctx, ev)
	e.publishChanged(ctx, updated)
	return updated, nil
}

// ---------------------------------------------------------------- rollback ----

// rollback executes a rollout in status rolling_back (docs 7.4): every
// touched target is reset to its initial state; when all of them report it,
// the rollout becomes rolled_back, if that does not happen within
// applyTimeout it becomes failed.
func (rc *runCtx) rollback() error {
	r := rc.r
	started := rc.rollbackStart()

	// Never-started targets are cancelled.
	for i := range rc.targets {
		if st := rc.targets[i].Status; st == domain.TargetPending || st == domain.TargetWaitingWindow {
			rc.setTarget(i, domain.TargetCancelled, "rollout is being rolled back")
		}
	}
	// The result of an executor call that outlived its tick (start calls of a
	// rollout that is being rolled back withdraw their updates again).
	if _, _, err := rc.collectDispatch(); err != nil {
		return err
	}
	var touched []int
	var ids []domain.AgentID
	for i := range rc.targets {
		switch rc.targets[i].Status {
		case domain.TargetInProgress, domain.TargetVerifying, domain.TargetSucceeded, domain.TargetFailed:
			touched = append(touched, i)
			ids = append(ids, rc.targets[i].AgentID)
		}
	}
	rc.loadAgents(ids)

	var err error
	if r.Kind == domain.RolloutKindConfig {
		err = rc.rollbackConfig(touched)
	} else {
		err = rc.rollbackVersion(touched)
	}
	if err != nil {
		rc.e.log.Warn("rollback step failed (will be retried)", "rollout_id", r.ID, "error", err)
	}

	remaining := 0
	for i := range rc.targets {
		switch rc.targets[i].Status {
		case domain.TargetInProgress, domain.TargetVerifying, domain.TargetSucceeded, domain.TargetFailed:
			remaining++
		}
	}
	if remaining == 0 {
		if ferr := rc.flush(); ferr != nil {
			return ferr
		}
		c := rc.countsTotal()
		r.Counts = c
		ok, cerr := rc.e.casStatus(rc.ctx, r, []domain.RolloutStatus{domain.RolloutRollingBack}, domain.RolloutRolledBack,
			fmt.Sprintf("%d target(s) rolled back", c.RolledBack))
		if cerr != nil || !ok {
			return cerr
		}
		rc.touched = true
		rc.e.auditSystem(rc.ctx, "rollout.rolled_back", r, map[string]any{"rolledBack": c.RolledBack})
		rc.e.notifyRollout(rc.ctx, r, notify.EventRolloutRolledBack, notify.SeverityWarning,
			fmt.Sprintf("Rollout rolled back: %s (%s)", r.FleetName, r.Kind),
			fmt.Sprintf("%d target(s) were returned to their previous state. Git still describes the new state: the agents show drift until Git is reverted or a new commit/version is rolled out.", c.RolledBack), "",
			notify.Field{Name: "Rolled back", Value: fmt.Sprint(c.RolledBack)})
		return nil
	}
	if rc.now.Sub(started) > rc.spec.ApplyTimeout.D() {
		return rc.failRollout(fmt.Sprintf("rollback did not complete within %s: %d target(s) did not return to their previous state", rc.spec.ApplyTimeout, remaining))
	}
	return err
}

// rollbackStart returns (and persists on first use) when the rollback began.
func (rc *runCtx) rollbackStart() time.Time {
	if s, ok := rc.r.Annotations[annRollbackStarted].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	rc.r.Annotations[annRollbackStarted] = rc.now.Format(time.RFC3339Nano)
	rc.progress = true
	return rc.now
}

func (rc *runCtx) rollbackConfig(touched []int) error {
	var push []int
	for _, i := range touched {
		t := &rc.targets[i]
		ag := rc.agent(t.AgentID)
		switch {
		case ag == nil:
			rc.setTarget(i, domain.TargetSkipped, "agent no longer exists")
			continue
		case ag.Assignment.FleetName != rc.r.FleetName:
			// Moved to another fleet: its desired config belongs to that fleet now.
			rc.setTarget(i, domain.TargetSkipped, "agent left the fleet")
			continue
		case rc.bridgeHeldBack(ag) != "":
			rc.setTarget(i, domain.TargetSkipped, rc.bridgeHeldBack(ag))
			continue
		case t.FromRef == "":
			rc.setTarget(i, domain.TargetRolledBack, "no previous configuration known")
			continue
		}
		if ag.Assignment.DesiredConfigHash.String() != t.FromRef {
			push = append(push, i)
			continue
		}
		if ag.RemoteConfig.LastHash.String() == t.FromRef && ag.RemoteConfig.Status == domain.RemoteConfigApplied {
			rc.setTarget(i, domain.TargetRolledBack, "")
		}
	}
	if len(push) == 0 {
		return nil
	}
	as := rc.e.deps.Store.Agents()
	groups := map[string][]domain.AgentID{}
	var all []domain.AgentID
	for _, i := range push {
		t := &rc.targets[i]
		if ag := rc.agent(t.AgentID); ag != nil && ag.Assignment.FailedConfigHash.String() == t.FromRef {
			if err := as.ClearFailedConfig(rc.ctx, t.AgentID); err != nil {
				return fmt.Errorf("clear failed config: %w", err)
			}
		}
		groups[t.FromRef] = append(groups[t.FromRef], t.AgentID)
		all = append(all, t.AgentID)
	}
	for from, ids := range groups {
		h, err := domain.ParseHash(from)
		if err != nil {
			return err
		}
		if err := as.SetDesiredConfig(rc.ctx, ids, h); err != nil {
			return fmt.Errorf("set desired config: %w", err)
		}
	}
	if rc.e.deps.Control != nil {
		if err := rc.e.deps.Control.DesiredStateChanged(rc.ctx, all); err != nil {
			return fmt.Errorf("push desired state: %w", err)
		}
	}
	return nil
}

func (rc *runCtx) rollbackSent() map[string]bool {
	sent := map[string]bool{}
	if l, ok := rc.r.Annotations[annRollbackSent].([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				sent[s] = true
			}
		}
	}
	return sent
}

func (rc *runCtx) rollbackVersion(touched []int) error {
	sent := rc.rollbackSent()
	byFrom := map[string][]int{}
	for _, i := range touched {
		t := &rc.targets[i]
		ag := rc.agent(t.AgentID)
		switch {
		case ag == nil:
			rc.setTarget(i, domain.TargetSkipped, "agent no longer exists")
			continue
		case ag.Assignment.FleetName != rc.r.FleetName:
			rc.setTarget(i, domain.TargetSkipped, "agent left the fleet")
			continue
		case rc.bridgeHeldBack(ag) != "":
			rc.setTarget(i, domain.TargetSkipped, rc.bridgeHeldBack(ag))
			continue
		case t.FromRef == "":
			rc.setTarget(i, domain.TargetRolledBack, "no previous version known")
			continue
		case versionOf(ag) == t.FromRef:
			rc.setTarget(i, domain.TargetRolledBack, "")
			continue
		}
		if !sent[t.AgentID.String()] {
			byFrom[t.FromRef] = append(byFrom[t.FromRef], i)
		}
	}
	if len(byFrom) == 0 {
		return nil
	}
	if rc.e.outstanding(rc.r.ID) {
		return nil // the executor is still working on the previous call
	}
	ex, ok := rc.e.deps.Executors.Get(rc.r.Executor)
	if !ok {
		return fmt.Errorf("executor %q is not configured", rc.r.Executor)
	}
	spec, def := rc.e.updateSpec(rc.ctx, rc.r)
	dist, _ := rc.r.Annotations[annDistribution].(string)
	if dist == "" {
		dist = domain.DistributionContrib
	}
	froms := make([]string, 0, len(byFrom))
	for from := range byFrom {
		froms = append(froms, from)
	}
	sort.Strings(froms)
	var errs []string
	for _, from := range froms {
		idxs := byFrom[from]
		req := executor.UpdateRequest{
			RolloutID: rc.r.ID, Wave: rc.targets[idxs[0]].Wave, Fleet: rc.r.FleetName, Distribution: dist, ToVersion: from,
			Spec: spec, Definition: def,
		}
		d := &dispatch{kind: dispatchRollback, wave: req.Wave, to: from}
		for _, i := range idxs {
			t := &rc.targets[i]
			ag := rc.agent(t.AgentID)
			tg := executor.Target{AgentID: ag.ID, Hostname: ag.Hostname, OSType: ag.OSType, Arch: ag.Arch,
				Attributes: domain.NewAttributeView(ag), CurrentVersion: versionOf(ag), Attempt: t.Attempts}
			if ag.Updater != nil {
				id := ag.Updater.AgentID
				tg.UpdaterAgentID = &id
			}
			req.Targets = append(req.Targets, tg)
			d.agents = append(d.agents, ag.ID)
		}
		if !rc.e.launch(rc.ctx, rc.r.ID, d, func(ctx context.Context) ([]executor.TargetResult, error) { return ex.Start(ctx, req) }) {
			break // collected on a later tick; the remaining versions follow afterwards
		}
		if err := rc.applyRollback(d); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// applyRollback records which agents the executor accepted for the rollback
// (they are not asked again) and reports the failures.
func (rc *runCtx) applyRollback(d *dispatch) error {
	if d.err != nil {
		return fmt.Errorf("rollback to %s: %v", d.to, d.err)
	}
	sent := rc.rollbackSent()
	var errs []string
	for _, res := range d.results {
		if res.Started && res.Error == "" {
			sent[res.AgentID.String()] = true
		} else if res.Error != "" {
			errs = append(errs, fmt.Sprintf("agent %s: %s", res.AgentID, res.Error))
		}
	}
	list := make([]any, 0, len(sent))
	for id := range sent {
		list = append(list, id)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].(string) < list[j].(string) })
	rc.r.Annotations[annRollbackSent] = list
	rc.progress = true
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

var _ = store.TargetFilter{}
