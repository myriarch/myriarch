// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// Tick runs one engine iteration over all active rollouts (leader only; Run
// calls it every cfg.TickInterval). Exported for tests and tooling.
func (e *Engine) Tick(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.baseCtx = ctx
	ctx, cancel := context.WithTimeout(ctx, cycleBudget)
	defer cancel()
	defer e.cycle("rollout_tick")()
	e.tickWaitLeft = e.tickWait

	active, err := store.ListActiveRollouts(ctx, e.deps.Store.Rollouts())
	if err != nil {
		return fmt.Errorf("list active rollouts: %w", err)
	}
	e.met.observe(active)
	ids := make(map[uuid.UUID]bool, len(active))
	for _, ar := range active {
		ids[ar.ID] = true
	}
	e.sweepDispatches(ctx, ids)
	var errs []error
	for _, ar := range active {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.processRollout(ctx, ar.ID); err != nil {
			e.log.Error("process rollout failed", "rollout_id", ar.ID, "fleet", ar.FleetName, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// runCtx is the working state of one rollout during one tick.
type runCtx struct {
	e    *Engine
	ctx  context.Context
	r    *domain.Rollout
	spec domain.RolloutPolicySpec
	now  time.Time

	targets []domain.RolloutTarget
	idx     map[domain.AgentID]int
	agents  map[domain.AgentID]*domain.Agent
	// loaded remembers which agents were requested (missing agents stay nil).
	loaded map[domain.AgentID]bool

	dirty    map[int]struct{}
	events   []domain.AgentEvent
	progress bool
	touched  bool
}

func (e *Engine) processRollout(ctx context.Context, id uuid.UUID) error {
	r, err := e.deps.Store.Rollouts().Get(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("load rollout %s: %w", id, err)
	}
	switch r.Status {
	case domain.RolloutRunning, domain.RolloutPaused, domain.RolloutWaitingApproval, domain.RolloutRollingBack:
	default:
		return nil
	}
	rc := &runCtx{
		e: e, ctx: ctx, r: r, spec: r.Policy.RolloutPolicySpec, now: e.now().Truncate(time.Microsecond),
		agents: map[domain.AgentID]*domain.Agent{}, loaded: map[domain.AgentID]bool{}, dirty: map[int]struct{}{},
	}
	if r.Annotations == nil {
		r.Annotations = map[string]any{}
	}
	if err := rc.reloadTargets(); err != nil {
		return err
	}

	var runErr error
	switch {
	case r.Status != domain.RolloutRollingBack && e.fleetMissing(ctx, r.FleetName):
		runErr = rc.failRollout(fmt.Sprintf("fleet %q was removed from the GitOps repository", r.FleetName))
	case r.Status == domain.RolloutRollingBack:
		runErr = rc.rollback()
	default:
		runErr = rc.run()
	}
	flushErr := rc.flush()
	return errors.Join(runErr, flushErr)
}

// fleetMissing confirms that a fleet vanished from the active snapshot.
func (e *Engine) fleetMissing(ctx context.Context, name string) bool {
	e.fleetsMu.RLock()
	known := e.fleets
	e.fleetsMu.RUnlock()
	if known == nil {
		return false
	}
	if _, ok := known[name]; ok {
		return false
	}
	_, err := e.deps.Store.GitOps().GetFleet(ctx, name)
	return errors.Is(err, domain.ErrNotFound)
}

// ------------------------------------------------------------ state access --

func (rc *runCtx) reloadTargets() error {
	ts, _, err := rc.e.deps.Store.Rollouts().ListTargets(rc.ctx, rc.r.ID, store.TargetFilter{})
	if err != nil {
		return fmt.Errorf("list targets of rollout %s: %w", rc.r.ID, err)
	}
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].Wave != ts[j].Wave {
			return ts[i].Wave < ts[j].Wave
		}
		return ts[i].AgentID.String() < ts[j].AgentID.String()
	})
	rc.targets = ts
	rc.idx = make(map[domain.AgentID]int, len(ts))
	for i := range ts {
		rc.idx[ts[i].AgentID] = i
	}
	rc.dirty = map[int]struct{}{}
	return nil
}

// loadAgents fetches the given agents (and, for version rollouts, their
// linked updaters) that are not loaded yet.
func (rc *runCtx) loadAgents(ids []domain.AgentID) {
	var need []domain.AgentID
	for _, id := range ids {
		if !rc.loaded[id] {
			rc.loaded[id] = true
			need = append(need, id)
		}
	}
	if len(need) == 0 {
		return
	}
	got, err := rc.e.deps.Store.Agents().GetMany(rc.ctx, need)
	if err != nil {
		rc.e.log.Warn("load agents failed", "rollout_id", rc.r.ID, "error", err)
		for _, id := range need {
			delete(rc.loaded, id) // retry on the next call
		}
		return
	}
	var updaters []domain.AgentID
	for i := range got {
		a := got[i]
		rc.agents[a.ID] = &a
		if rc.r.Kind == domain.RolloutKindVersion && a.Updater != nil {
			updaters = append(updaters, a.Updater.AgentID)
		}
	}
	if len(updaters) > 0 {
		rc.loadAgents(updaters)
	}
}

func (rc *runCtx) agent(id domain.AgentID) *domain.Agent { return rc.agents[id] }

// bridgeHeldBack returns why a target is skipped because its agent is an OpAMP
// Bridge now, or "". A target can predate that (a rollout from before the
// upgrade, or an agent that reports the Bridge attributes later); no executor
// is called for a Bridge (ADR-0037). A config target of a Bridge stays valid
// while its fleet configures it.
func (rc *runCtx) bridgeHeldBack(ag *domain.Agent) string {
	if ag.Kind != domain.AgentKindBridge {
		return ""
	}
	if rc.r.Kind == domain.RolloutKindVersion {
		return domain.BridgeVersionRefused
	}
	return ag.Assignment.Error
}

func (rc *runCtx) updaterOf(a *domain.Agent) *domain.Agent {
	if a == nil || a.Updater == nil {
		return nil
	}
	return rc.agents[a.Updater.AgentID]
}

func (rc *runCtx) loadWave(wave int) {
	var ids []domain.AgentID
	for i := range rc.targets {
		t := &rc.targets[i]
		if t.Wave == wave && !isTerminalTarget(t.Status) {
			ids = append(ids, t.AgentID)
		}
	}
	rc.loadAgents(ids)
}

func isTerminalTarget(s domain.TargetStatus) bool {
	switch s {
	case domain.TargetSucceeded, domain.TargetFailed, domain.TargetSkipped, domain.TargetCancelled, domain.TargetRolledBack:
		return true
	}
	return false
}

func isInFlight(s domain.TargetStatus) bool {
	return s == domain.TargetInProgress || s == domain.TargetVerifying
}

// setTarget changes a target's status and records the agent event.
func (rc *runCtx) setTarget(i int, st domain.TargetStatus, msg string) {
	t := &rc.targets[i]
	if t.Status == st && (msg == "" || t.LastError == msg) {
		return
	}
	old := t.Status
	t.Status = st
	switch st {
	case domain.TargetInProgress:
		t.StartedAt, t.VerifiedAt, t.FinishedAt, t.LastError = &rc.now, nil, nil, ""
	case domain.TargetVerifying:
		t.VerifiedAt = &rc.now
	case domain.TargetPending, domain.TargetWaitingWindow:
		t.FinishedAt = nil
	default:
		t.FinishedAt, t.LastError = &rc.now, msg
	}
	rc.dirty[i] = struct{}{}
	rc.touched = true
	if rc.r.Kind == domain.RolloutKindVersion && st != domain.TargetVerifying {
		// verifying is evaluated with the in-flight targets right after
		rc.refreshProgress(i)
	}

	quiet := (old == domain.TargetPending && st == domain.TargetWaitingWindow) || (old == domain.TargetWaitingWindow && st == domain.TargetPending)
	if !quiet {
		sev := "info"
		if st == domain.TargetFailed {
			sev = "error"
		} else if st == domain.TargetSkipped {
			sev = "warning"
		}
		text := fmt.Sprintf("rollout %s (%s %s): target %s", shortID(rc.r.ID), rc.r.Kind, shortRef(rc.r.Kind, t.ToRef), st)
		if msg != "" {
			text += ": " + msg
		}
		rc.events = append(rc.events, domain.AgentEvent{
			AgentID: t.AgentID, Time: rc.now, Type: domain.AgentEventRolloutTarget, Severity: sev, Message: text,
			Details: map[string]any{"rolloutId": rc.r.ID.String(), "fleet": rc.r.FleetName, "wave": t.Wave, "status": string(st), "error": msg},
		})
	}
}

// flush persists dirty targets (optimistic), the rollout progress, agent
// events and publishes the change. Afterwards the target list is re-read, so
// updated_at values are fresh and rejected (conflicting) writes show the
// stored state.
func (rc *runCtx) flush() error {
	rs := rc.e.deps.Store.Rollouts()
	var errs []error
	wrote := false
	if len(rc.dirty) > 0 {
		ts := make([]domain.RolloutTarget, 0, len(rc.dirty))
		for i := range rc.dirty {
			ts = append(ts, rc.targets[i])
		}
		conflicts, err := rs.UpdateTargets(rc.ctx, ts)
		if err != nil {
			errs = append(errs, fmt.Errorf("update targets: %w", err))
		} else {
			wrote = true
			if len(conflicts) > 0 {
				rc.e.log.Debug("rollout targets changed concurrently, re-evaluating next tick", "rollout_id", rc.r.ID, "targets", len(conflicts))
				drop := map[domain.AgentID]bool{}
				for _, c := range conflicts {
					drop[c] = true
				}
				kept := rc.events[:0]
				for _, ev := range rc.events {
					if !drop[ev.AgentID] {
						kept = append(kept, ev)
					}
				}
				rc.events = kept
			}
		}
	}
	if rc.progress {
		if err := rs.UpdateProgress(rc.ctx, rc.r); err != nil {
			errs = append(errs, fmt.Errorf("update progress: %w", err))
		} else {
			wrote = true
			rc.progress = false
		}
	}
	if len(rc.events) > 0 {
		if err := rc.e.deps.Store.Agents().AppendEvents(rc.ctx, rc.events); err != nil {
			rc.e.log.Warn("append rollout events failed", "rollout_id", rc.r.ID, "error", err)
		}
		rc.events = nil
	}
	if wrote || rc.touched {
		rc.touched = false
		rc.e.publishChanged(rc.ctx, rc.r)
	}
	if len(errs) == 0 && len(rc.dirty) > 0 {
		if err := rc.reloadTargets(); err != nil {
			errs = append(errs, err)
		}
	} else if len(errs) == 0 {
		rc.dirty = map[int]struct{}{}
	}
	return errors.Join(errs...)
}

// refreshProgress derives the live progress of a version target and stores it
// with the target if its content changed.
func (rc *runCtx) refreshProgress(i int) {
	t := &rc.targets[i]
	var p *domain.TargetProgress
	switch {
	case t.Progress != nil && (t.Status == domain.TargetCancelled || t.Status == domain.TargetSkipped || t.Status == domain.TargetRolledBack):
		// ended from outside (abort, supersede, rollback): keep what was known
		p = domain.ProgressOf(*t)
		p.UpdatedAt = rc.now
	default:
		col := rc.agent(t.AgentID)
		p = computeProgress(rc.r, t, col, rc.updaterOf(col), rc.now)
	}
	if p == nil || (t.Progress != nil && t.Progress.SameContent(p)) {
		return
	}
	t.Progress = p
	rc.dirty[i] = struct{}{}
	rc.touched = true
}

// ------------------------------------------------------------- annotations --

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// ackKey identifies one failed attempt of one agent: a retry increments the
// attempt, so a new failure of the same agent is not covered by an earlier
// acknowledgement.
func ackKey(t *domain.RolloutTarget) string {
	return fmt.Sprintf("%s#%d", t.AgentID, t.Attempts)
}

// acked returns the failed attempts of the wave an operator acknowledged with
// "resume" after the failure threshold was reached.
func (rc *runCtx) acked(wave int) map[string]bool {
	out := map[string]bool{}
	m, _ := rc.r.Annotations[annAckedFailures].(map[string]any)
	if l, ok := m[fmt.Sprint(wave)].([]any); ok {
		for _, v := range l {
			if k, ok := v.(string); ok {
				out[k] = true
			}
		}
	}
	return out
}

// ackFailures acknowledges every failed target of the wave as it is now.
func (rc *runCtx) ackFailures(wave int) {
	keys := rc.acked(wave)
	for i := range rc.targets {
		if t := &rc.targets[i]; t.Wave == wave && t.Status == domain.TargetFailed {
			keys[ackKey(t)] = true
		}
	}
	list := make([]any, 0, len(keys))
	for k := range keys {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].(string) < list[j].(string) })
	m, _ := rc.r.Annotations[annAckedFailures].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	m[fmt.Sprint(wave)] = list
	rc.r.Annotations[annAckedFailures] = m
	rc.progress = true
}

// unackedFailed counts the failed targets of the wave that were not
// acknowledged.
func (rc *runCtx) unackedFailed(wave int) int {
	keys := rc.acked(wave)
	n := 0
	for i := range rc.targets {
		if t := &rc.targets[i]; t.Wave == wave && t.Status == domain.TargetFailed && !keys[ackKey(t)] {
			n++
		}
	}
	return n
}

func (rc *runCtx) waveCounts(wave int) (total, failed, open int) {
	for i := range rc.targets {
		t := &rc.targets[i]
		if t.Wave != wave {
			continue
		}
		total++
		switch t.Status {
		case domain.TargetFailed:
			failed++
		case domain.TargetPending, domain.TargetWaitingWindow, domain.TargetInProgress, domain.TargetVerifying:
			open++
		}
	}
	return total, failed, open
}

// ------------------------------------------------------------------- run ----

func (rc *runCtx) run() error {
	r := rc.r
	if len(r.Waves) == 0 {
		return rc.failRollout("rollout has no waves")
	}

	// Agents of the current wave and all in-flight targets.
	var ids []domain.AgentID
	for i := range rc.targets {
		t := &rc.targets[i]
		if isInFlight(t.Status) || (t.Wave == r.CurrentWave && !isTerminalTarget(t.Status)) || rc.settling(t) {
			ids = append(ids, t.AgentID)
		}
	}
	rc.loadAgents(ids)
	if allFailed, wave, err := rc.collectDispatch(); err != nil {
		return err
	} else if allFailed {
		return rc.failRollout("no target of wave " + fmt.Sprintf("%q", r.Waves[min(wave, len(r.Waves)-1)].Name) + " could be started: " + rc.lastErrors(wave))
	}
	if err := rc.evalInFlight(); err != nil {
		return err
	}

	switch r.Status {
	case domain.RolloutPaused:
		return nil // in-flight targets are evaluated, nothing new starts
	case domain.RolloutWaitingApproval:
		w := &r.Waves[min(r.CurrentWave, len(r.Waves)-1)]
		if w.ApprovedAt == nil {
			if w.Status == domain.WavePending {
				w.Status = domain.WaveWaitingApproval
				rc.progress = true
			}
			return nil
		}
		ok, err := rc.e.casStatus(rc.ctx, r, []domain.RolloutStatus{domain.RolloutWaitingApproval}, domain.RolloutRunning,
			fmt.Sprintf("wave %q approved", w.Name))
		if err != nil || !ok {
			return err
		}
		rc.touched = true
	}
	return rc.advance()
}

// advance moves a running rollout through its waves.
func (rc *runCtx) advance() error {
	r := rc.r

	// A retry (or late joiner) may have re-opened an earlier wave.
	for wi := range r.Waves {
		if wi >= r.CurrentWave {
			break
		}
		if _, _, open := rc.waveCounts(wi); open > 0 {
			r.CurrentWave = wi
			if r.Waves[wi].Status != domain.WavePending {
				r.Waves[wi].Status, r.Waves[wi].FinishedAt = domain.WaveRunning, nil
			}
			rc.progress = true
			break
		}
	}

	for guard := 0; guard < len(r.Waves)+4; guard++ {
		cur := r.CurrentWave
		if cur >= len(r.Waves) {
			return rc.complete()
		}
		w := &r.Waves[cur]
		switch w.Status {
		case domain.WaveSkipped, domain.WaveCompleted:
			if total, _, open := rc.waveCounts(cur); total > 0 && open > 0 {
				w.Status, w.FinishedAt = domain.WaveRunning, nil // targets were added to a finished wave
				rc.progress = true
				continue
			}
			if cur == len(r.Waves)-1 {
				return rc.complete()
			}
			r.CurrentWave++
			rc.progress = true
			continue
		case domain.WaveFailed:
			// The operator resumed (or retried) after the failure threshold:
			// acknowledge the failures seen so far.
			rc.ackFailures(cur)
			w.Status, w.FinishedAt = domain.WaveRunning, nil
			rc.progress = true
			continue
		case domain.WavePending, domain.WaveWaitingApproval:
			if w.RequireApproval && w.ApprovedAt == nil {
				if w.Status != domain.WaveWaitingApproval {
					w.Status = domain.WaveWaitingApproval
					rc.progress = true
				}
				if err := rc.flush(); err != nil { // progress before the status change
					return err
				}
				ok, err := rc.e.casStatus(rc.ctx, r, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutWaitingApproval,
					fmt.Sprintf("wave %q waits for approval", w.Name))
				if err == nil && ok {
					rc.touched = true
					rc.e.auditSystem(rc.ctx, "rollout.wave_waiting_approval", r, map[string]any{"wave": cur, "name": w.Name})
					rc.e.notifyRollout(rc.ctx, r, notify.EventRolloutWaitingApprove, notify.SeverityWarning,
						fmt.Sprintf("Rollout waits for approval: %s", r.FleetName),
						fmt.Sprintf("Wave %q of the %s rollout for fleet %s needs an approval.", w.Name, r.Kind, r.FleetName), fmt.Sprint(cur),
						notify.Field{Name: "Wave", Value: w.Name})
				}
				return err
			}
			w.Status, w.StartedAt, w.FinishedAt = domain.WaveRunning, &rc.now, nil
			rc.progress = true
			rc.loadWave(cur)
			continue
		}

		// Wave is running.
		rc.loadWave(cur)
		done, err := rc.processWave(cur)
		if err != nil || !done {
			return err
		}
		w.Status, w.FinishedAt = domain.WaveCompleted, &rc.now
		rc.progress = true
		if cur == len(r.Waves)-1 {
			return rc.complete()
		}
		r.CurrentWave++
	}
	return nil
}

// processWave evaluates a running wave and starts targets. It returns true
// when the wave is finished (all targets terminal, threshold not reached).
func (rc *runCtx) processWave(cur int) (bool, error) {
	if reached, err := rc.checkThreshold(cur); reached || err != nil {
		return false, err
	}
	execFailedAll, err := rc.startTargets(cur)
	if err != nil {
		return false, err
	}
	if execFailedAll {
		return false, rc.failRollout("no target of wave " + fmt.Sprintf("%q", rc.r.Waves[cur].Name) + " could be started: " + rc.lastErrors(cur))
	}
	if reached, err := rc.checkThreshold(cur); reached || err != nil {
		return false, err
	}
	_, _, open := rc.waveCounts(cur)
	return open == 0, nil
}

// checkThreshold applies the failure threshold of the wave (docs 7.4).
func (rc *runCtx) checkThreshold(cur int) (bool, error) {
	total, failed, _ := rc.waveCounts(cur)
	eff := rc.unackedFailed(cur)
	if !rc.spec.FailureThreshold.Reached(eff, total) {
		return false, nil
	}
	r := rc.r
	w := &r.Waves[cur]
	w.Status, w.FinishedAt = domain.WaveFailed, &rc.now
	rc.progress = true
	reason := fmt.Sprintf("wave %q: %d of %d targets failed, failure threshold reached (%s)", w.Name, failed, total, thresholdText(rc.spec.FailureThreshold))
	failures := rc.lastErrors(cur)

	if rc.spec.OnFailure == domain.FailureRollback {
		ok, err := rc.e.casStatus(rc.ctx, r, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutRollingBack, reason+"; rolling back automatically")
		if err != nil {
			return true, err
		}
		if ok {
			rc.touched = true
			rc.e.auditSystem(rc.ctx, "rollout.rollback", r, map[string]any{"automatic": true, "reason": reason})
		}
		return true, nil
	}
	ok, err := rc.e.casStatus(rc.ctx, r, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutPaused, reason+"; paused")
	if err != nil {
		return true, err
	}
	if ok {
		rc.touched = true
		rc.e.auditSystem(rc.ctx, "rollout.pause", r, map[string]any{"automatic": true, "reason": reason})
		rc.e.notifyRollout(rc.ctx, r, notify.EventRolloutPaused, notify.SeverityWarning,
			fmt.Sprintf("Rollout paused: %s", r.FleetName), reason+". "+failures, fmt.Sprintf("%d", cur),
			notify.Field{Name: "Wave", Value: w.Name}, notify.Field{Name: "Failed", Value: fmt.Sprintf("%d of %d", failed, total)})
	}
	return true, nil
}

func thresholdText(t domain.FailureThreshold) string {
	var parts []string
	if t.MaxFailed > 0 {
		parts = append(parts, fmt.Sprintf("maxFailed %d", t.MaxFailed))
	}
	if t.MaxFailedPercent > 0 {
		parts = append(parts, fmt.Sprintf("maxFailedPercent %d", t.MaxFailedPercent))
	}
	return strings.Join(parts, ", ")
}

// lastErrors summarizes up to three target error messages of a wave.
func (rc *runCtx) lastErrors(wave int) string {
	var msgs []string
	seen := map[string]bool{}
	for i := range rc.targets {
		t := &rc.targets[i]
		if t.Wave != wave || t.Status != domain.TargetFailed || t.LastError == "" || seen[t.LastError] {
			continue
		}
		seen[t.LastError] = true
		msgs = append(msgs, fmt.Sprintf("%s: %s", t.Hostname, t.LastError))
		if len(msgs) == 3 {
			break
		}
	}
	if len(msgs) == 0 {
		return ""
	}
	return "Errors: " + strings.Join(msgs, "; ")
}

func (rc *runCtx) countsTotal() domain.RolloutCounts {
	var c domain.RolloutCounts
	for i := range rc.targets {
		c.Total++
		switch rc.targets[i].Status {
		case domain.TargetPending:
			c.Pending++
		case domain.TargetWaitingWindow:
			c.WaitingWindow++
		case domain.TargetInProgress, domain.TargetVerifying:
			c.InProgress++
		case domain.TargetSucceeded:
			c.Succeeded++
		case domain.TargetFailed:
			c.Failed++
		case domain.TargetSkipped:
			c.Skipped++
		case domain.TargetCancelled:
			c.Cancelled++
		case domain.TargetRolledBack:
			c.RolledBack++
		}
	}
	return c
}

func (rc *runCtx) complete() error {
	r := rc.r
	if err := rc.flush(); err != nil { // progress and targets before the status change
		return err
	}
	c := rc.countsTotal()
	r.Counts = c
	ok, err := rc.e.casStatus(rc.ctx, r, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutCompleted, "all waves completed")
	if err != nil || !ok {
		return err
	}
	rc.touched = true
	rc.e.auditSystem(rc.ctx, "rollout.complete", r, map[string]any{"succeeded": c.Succeeded, "failed": c.Failed, "skipped": c.Skipped})
	rc.e.notifyRollout(rc.ctx, r, notify.EventRolloutCompleted, notify.SeverityInfo,
		fmt.Sprintf("Rollout completed: %s (%s)", r.FleetName, r.Kind),
		fmt.Sprintf("%d succeeded, %d failed, %d skipped of %d targets.", c.Succeeded, c.Failed, c.Skipped, c.Total), "",
		notify.Field{Name: "Succeeded", Value: fmt.Sprint(c.Succeeded)}, notify.Field{Name: "Failed", Value: fmt.Sprint(c.Failed)},
		notify.Field{Name: "Skipped", Value: fmt.Sprint(c.Skipped)})
	return nil
}

// failRollout ends a rollout that cannot proceed (docs 7.4 (b)).
func (rc *runCtx) failRollout(reason string) error {
	r := rc.r
	if err := rc.flush(); err != nil {
		return err
	}
	ok, err := rc.e.casStatus(rc.ctx, r, []domain.RolloutStatus{domain.RolloutRunning, domain.RolloutPaused, domain.RolloutWaitingApproval, domain.RolloutRollingBack}, domain.RolloutFailed, reason)
	if err != nil || !ok {
		return err
	}
	rc.touched = true
	rc.e.auditSystem(rc.ctx, "rollout.fail", r, map[string]any{"reason": reason})
	rc.e.notifyRollout(rc.ctx, r, notify.EventRolloutFailed, notify.SeverityCritical,
		fmt.Sprintf("Rollout failed: %s (%s)", r.FleetName, r.Kind), reason, "")
	rc.e.publishChanged(rc.ctx, r)
	return nil
}

// ------------------------------------------------------- in-flight targets --

func healthy(a *domain.Agent) bool { return a.Health == nil || a.Health.Healthy }

// staleHealthGrace bounds how long an unhealthy report that predates the
// verification start is ignored.
const staleHealthGrace = 2 * time.Minute

// staleUnhealthy reports whether the agent's unhealthy state is expected while
// the target verifies, i.e. it does not (yet) describe a failure of the new
// process:
//
//   - the report predates the verification start: it still describes the
//     process that has just been replaced (the new version/config is reported
//     before the old process' "stopping" health is superseded). Waits at most
//     staleHealthGrace.
//   - the report is a start-up state of the new process: the real
//     opampsupervisor reports the new version with {healthy:false} before the
//     collector runs, then the collector's own StatusStarting, and only then
//     StatusOK. Waits at most applyTimeout after verification started; a
//     process that never becomes healthy fails with a timeout.
//
// The health wait itself only counts a healthy report (a failure report such as
// a permanent error always fails the target). Agents without a report
// timestamp are never considered stale.
func (rc *runCtx) staleUnhealthy(a *domain.Agent, t *domain.RolloutTarget) bool {
	if a.Health == nil || a.Health.Healthy || t.VerifiedAt == nil {
		return false
	}
	since := rc.now.Sub(*t.VerifiedAt)
	if !a.Health.UpdatedAt.IsZero() && !a.Health.UpdatedAt.After(*t.VerifiedAt) && since < staleHealthGrace {
		return true
	}
	return startingUp(a.Health) && since < rc.spec.ApplyTimeout.D()
}

// startingUp reports whether an unhealthy health report is the normal start-up
// or shut-down state of a collector rather than an error.
func startingUp(h *domain.AgentHealth) bool {
	switch h.Status {
	case "StatusStarting", "StatusStopping":
		return true
	case "":
		return h.LastError == ""
	}
	return false
}

// maxHealthRegressions is how often a verifying collector may fall back from
// healthy to its start-up state (one restart is normal, e.g. an applied
// config right after the update) before the target counts as crash-looping.
const maxHealthRegressions = 1

// healthTrack is what the engine remembers of one verifying target.
type healthTrack struct {
	seen    bool // healthy at least once since the new version was reported
	last    bool // the last observation was healthy
	regress int  // healthy -> starting transitions
}

func (rc *runCtx) tracks() map[string]any {
	m, _ := rc.r.Annotations[annHealthTrack].(map[string]any)
	return m
}

func (rc *runCtx) loadTrack(id domain.AgentID) healthTrack {
	var ht healthTrack
	if e, ok := rc.tracks()[id.String()].(map[string]any); ok {
		ht.seen, _ = e["seen"].(bool)
		ht.last, _ = e["last"].(bool)
		ht.regress = asInt(e["regress"])
	}
	return ht
}

func (rc *runCtx) storeTrack(id domain.AgentID, ht healthTrack) {
	m := rc.tracks()
	if m == nil {
		m = map[string]any{}
	}
	m[id.String()] = map[string]any{"seen": ht.seen, "last": ht.last, "regress": ht.regress}
	rc.r.Annotations[annHealthTrack] = m
	rc.progress = true
}

// observeHealth records the collector's health of a verifying version target
// and reports whether it is crash-looping. The supervisor's normal start-up
// (starting, then OK) is tolerated by the health gate for up to applyTimeout;
// a collector that keeps alternating between OK and starting (the process
// dies and is restarted) would otherwise pass as long as the health wait ends
// on an OK report. Per target the engine tracks whether it was healthy after
// the new version appeared and fails it once it regresses to starting more
// than maxHealthRegressions times. The track is persisted with the rollout
// (a new leader continues it) and dropped when the target leaves verifying.
func (rc *runCtx) observeHealth(t *domain.RolloutTarget, ag *domain.Agent) bool {
	if t.VerifiedAt == nil || ag.Health == nil {
		return false
	}
	h := ag.Health
	ht := rc.loadTrack(t.AgentID)
	orig := ht
	switch {
	case h.Healthy:
		ht.seen, ht.last = true, true
	case startingUp(h) && (h.UpdatedAt.IsZero() || h.UpdatedAt.After(*t.VerifiedAt)):
		// a fresh start-up report of a process (reports that predate the
		// verification still describe the replaced process)
		if ht.seen && ht.last {
			ht.regress++
		}
		ht.last = false
	}
	if ht != orig {
		rc.storeTrack(t.AgentID, ht)
	}
	return ht.regress > maxHealthRegressions
}

// pruneHealthTracks drops the tracks of targets that are not verifying any more.
func (rc *runCtx) pruneHealthTracks() {
	m := rc.tracks()
	if len(m) == 0 {
		return
	}
	live := map[string]bool{}
	for i := range rc.targets {
		if rc.targets[i].Status == domain.TargetVerifying {
			live[rc.targets[i].AgentID.String()] = true
		}
	}
	for id := range m {
		if !live[id] {
			delete(m, id)
			rc.progress = true
		}
	}
	if len(m) == 0 {
		delete(rc.r.Annotations, annHealthTrack)
	}
}

func healthText(a *domain.Agent) string {
	if a.Health != nil && a.Health.LastError != "" {
		return ": " + a.Health.LastError
	}
	return ""
}

// fresh reports whether a report time is not older than the attempt start.
func fresh(report, start *time.Time) bool {
	return report == nil || start == nil || !report.Before(*start)
}

// settleWindow bounds how long the progress of a failed updater target keeps
// following the host (the updater may still be rolling back when the engine
// gives up on the target, e.g. on a failed health wait).
const settleWindow = 15 * time.Minute

// settling reports whether a failed version target still follows the update
// attempt of its myriarch-updater: the engine failed the target (health wait,
// timeout) while the host continues its own health check and rollback, and
// the UI shows that until the updater ends the attempt (docs/design/version-selection.md,
// Target progress > Derivation).
func (rc *runCtx) settling(t *domain.RolloutTarget) bool {
	return rc.r.Kind == domain.RolloutKindVersion && rc.r.Executor == domain.ExecutorUpdater &&
		t.Status == domain.TargetFailed && t.Progress != nil && t.Progress.Source == domain.ProgressUpdater &&
		t.Progress.Phase != domain.PhaseRolledBack && t.FinishedAt != nil && rc.now.Sub(*t.FinishedAt) < settleWindow
}

func (rc *runCtx) evalInFlight() error {
	var reconcile []int
	for i := range rc.targets {
		t := &rc.targets[i]
		if rc.settling(t) {
			rc.refreshProgress(i)
			continue
		}
		if !isInFlight(t.Status) {
			continue
		}
		ag := rc.agent(t.AgentID)
		switch {
		case ag == nil:
			rc.setTarget(i, domain.TargetSkipped, "agent no longer exists")
			continue
		case ag.Assignment.FleetName != rc.r.FleetName:
			rc.setTarget(i, domain.TargetSkipped, "agent left the fleet")
			continue
		}
		if why := rc.bridgeHeldBack(ag); why != "" {
			rc.setTarget(i, domain.TargetSkipped, why)
			continue
		}
		if rc.r.Kind == domain.RolloutKindConfig {
			if rc.evalConfig(i, t, ag) {
				reconcile = append(reconcile, i)
			}
		} else {
			rc.evalVersion(i, t, ag)
			rc.refreshProgress(i) // also after a transition (the final status freezes it)
		}
	}
	if rc.r.Kind == domain.RolloutKindVersion {
		rc.pruneHealthTracks()
	}
	if len(reconcile) > 0 {
		return rc.pushConfig(reconcile)
	}
	return nil
}

// evalConfig evaluates one in-flight config target. It returns true if the
// desired config of the agent must be re-pushed (reconcile).
func (rc *runCtx) evalConfig(i int, t *domain.RolloutTarget, ag *domain.Agent) bool {
	toRef := t.ToRef
	if t.Status == domain.TargetInProgress {
		if ag.Assignment.DesiredConfigHash.String() != toRef {
			return true // claimed but never pushed (crash/leader change): push again
		}
		reported := ag.RemoteConfig.LastHash.String() == toRef
		failedByAgent := ag.Assignment.FailedConfigHash.String() == toRef ||
			(reported && ag.RemoteConfig.Status == domain.RemoteConfigFailed && fresh(ag.RemoteConfig.UpdatedAt, t.StartedAt))
		switch {
		case failedByAgent:
			msg := "agent rejected the configuration"
			if ag.RemoteConfig.ErrorMessage != "" {
				msg += ": " + ag.RemoteConfig.ErrorMessage
			}
			rc.setTarget(i, domain.TargetFailed, msg)
			return false
		case reported && ag.RemoteConfig.Status == domain.RemoteConfigApplied:
			rc.setTarget(i, domain.TargetVerifying, "")
			// healthWait 0 completes in the same pass
		default:
			if t.StartedAt != nil && rc.now.Sub(*t.StartedAt) > rc.spec.ApplyTimeout.D() {
				rc.setTarget(i, domain.TargetFailed, fmt.Sprintf("timed out: the configuration was not applied within %s", rc.spec.ApplyTimeout))
			}
			return false
		}
	}
	// verifying
	switch {
	case !ag.IsOnline():
		rc.setTarget(i, domain.TargetFailed, "agent went offline during the health wait")
	case ag.RemoteConfig.LastHash.String() != toRef:
		rc.setTarget(i, domain.TargetFailed, "agent no longer runs the target configuration (reverted)")
	case ag.RemoteConfig.Status == domain.RemoteConfigFailed:
		rc.setTarget(i, domain.TargetFailed, "agent reported the configuration as failed")
	case rc.staleUnhealthy(ag, t):
		// the old process' health: wait for the new process' report
	case !healthy(ag):
		rc.setTarget(i, domain.TargetFailed, "agent became unhealthy during the health wait"+healthText(ag))
	case t.VerifiedAt != nil && rc.now.Sub(*t.VerifiedAt) >= rc.spec.HealthWait.D():
		rc.setTarget(i, domain.TargetSucceeded, "")
	}
	return false
}

func (rc *runCtx) evalVersion(i int, t *domain.RolloutTarget, ag *domain.Agent) {
	toRef := t.ToRef
	if t.Status == domain.TargetInProgress {
		switch {
		case versionOf(ag) == toRef:
			rc.setTarget(i, domain.TargetVerifying, "")
		default:
			if up := rc.updaterOf(ag); up != nil && up.PackageStatus != nil {
				if ps := up.PackageStatus.Packages[domain.CollectorPackageName]; ps != nil &&
					ps.Status == domain.PackageInstallFailed && ps.ServerOfferedVersion == "v"+toRef &&
					fresh(&up.PackageStatus.UpdatedAt, t.StartedAt) {
					msg := "myriarch-updater reported INSTALL_FAILED"
					if ps.ErrorMessage != "" {
						msg += ": " + ps.ErrorMessage
					}
					rc.setTarget(i, domain.TargetFailed, msg)
					return
				}
			}
			if t.StartedAt != nil && rc.now.Sub(*t.StartedAt) > rc.spec.ApplyTimeout.D() {
				rc.setTarget(i, domain.TargetFailed, fmt.Sprintf("timed out: the version %s was not reported within %s", toRef, rc.spec.ApplyTimeout))
			}
			return
		}
	}
	crashLoop := rc.observeHealth(t, ag)
	switch {
	case !ag.IsOnline():
		rc.setTarget(i, domain.TargetFailed, "agent went offline during the health wait")
	case versionOf(ag) != toRef:
		rc.setTarget(i, domain.TargetFailed, fmt.Sprintf("agent reports version %s instead of %s", versionOf(ag), toRef))
	case crashLoop:
		rc.setTarget(i, domain.TargetFailed, fmt.Sprintf("collector restarts repeatedly: it fell back from healthy to starting more than %d time(s) within the health wait (crash loop)", maxHealthRegressions))
	case rc.staleUnhealthy(ag, t):
		// the old process' health: wait for the new process' report
	case !healthy(ag):
		rc.setTarget(i, domain.TargetFailed, "agent became unhealthy during the health wait"+healthText(ag))
	case t.VerifiedAt != nil && rc.now.Sub(*t.VerifiedAt) >= rc.spec.HealthWait.D():
		rc.setTarget(i, domain.TargetSucceeded, "")
	}
}

// ---------------------------------------------------------------- starting ----

// startTargets starts the startable targets of the current wave. It returns
// true if every target that could start failed at the executor (docs 7.4 b).
func (rc *runCtx) startTargets(cur int) (bool, error) {
	r := rc.r
	if r.Kind == domain.RolloutKindVersion && rc.e.outstanding(r.ID) {
		return false, nil // the executor is still working on the previous batch
	}
	w := &r.Waves[cur]
	inFlight := 0
	for i := range rc.targets {
		if isInFlight(rc.targets[i].Status) {
			inFlight++
		}
	}
	slots := -1 // unlimited
	if rc.spec.MaxConcurrent > 0 {
		slots = rc.spec.MaxConcurrent - inFlight
	}
	var starters []int
	for i := range rc.targets {
		t := &rc.targets[i]
		if t.Wave != cur || (t.Status != domain.TargetPending && t.Status != domain.TargetWaitingWindow) {
			continue
		}
		ag := rc.agent(t.AgentID)
		switch {
		case ag == nil:
			rc.setTarget(i, domain.TargetSkipped, "agent no longer exists")
			continue
		case ag.Assignment.FleetName != r.FleetName:
			rc.setTarget(i, domain.TargetSkipped, "agent left the fleet")
			continue
		}
		if why := rc.bridgeHeldBack(ag); why != "" {
			rc.setTarget(i, domain.TargetSkipped, why)
			continue
		}
		if !ag.IsOnline() {
			if rc.offlineTooLong(ag, w) {
				rc.setTarget(i, domain.TargetSkipped, fmt.Sprintf("agent was offline for more than %s", rc.spec.OfflineGracePeriod))
			}
			continue
		}
		if !windowOpen(rc.spec, domain.NewAttributeView(ag), rc.now, rc.e.cfg.DefaultTimezone) {
			if t.Status != domain.TargetWaitingWindow {
				rc.setTarget(i, domain.TargetWaitingWindow, "")
			}
			continue
		}
		if slots == 0 {
			if t.Status == domain.TargetWaitingWindow {
				rc.setTarget(i, domain.TargetPending, "")
			}
			continue
		}
		if slots > 0 {
			slots--
		}
		starters = append(starters, i)
	}
	if len(starters) == 0 {
		return false, nil
	}
	if r.Kind == domain.RolloutKindConfig {
		return false, rc.startConfig(starters)
	}
	return rc.startVersion(cur, starters)
}

func (rc *runCtx) offlineTooLong(ag *domain.Agent, w *domain.RolloutWave) bool {
	since := rc.now
	if w.StartedAt != nil {
		since = *w.StartedAt
	}
	if ag.Connection.DisconnectedAt != nil && ag.Connection.DisconnectedAt.After(since) {
		since = *ag.Connection.DisconnectedAt
	}
	return rc.now.Sub(since) > rc.spec.OfflineGracePeriod.D()
}

// startConfig claims the targets (persisted first) and pushes the desired
// config. A crash between both steps is repaired by the reconcile step of
// the next tick.
func (rc *runCtx) startConfig(starters []int) error {
	ids := make([]domain.AgentID, len(starters))
	for k, i := range starters {
		rc.setTarget(i, domain.TargetInProgress, "")
		ids[k] = rc.targets[i].AgentID
	}
	if err := rc.flush(); err != nil {
		return err
	}
	// Only push for targets that really are ours now (no concurrent change).
	var push []int
	for _, id := range ids {
		i, ok := rc.idx[id]
		if !ok {
			continue
		}
		t := &rc.targets[i]
		if t.Status == domain.TargetInProgress && t.StartedAt != nil && t.StartedAt.Equal(rc.now) {
			push = append(push, i)
		}
	}
	return rc.pushConfig(push)
}

// pushConfig sets desired = target hash (clearing a stale failed marker
// first) for the given in-progress targets and notifies the OpAMP layer.
func (rc *runCtx) pushConfig(idxs []int) error {
	if len(idxs) == 0 {
		return nil
	}
	as := rc.e.deps.Store.Agents()
	groups := map[string][]domain.AgentID{}
	var all []domain.AgentID
	for _, i := range idxs {
		t := &rc.targets[i]
		ag := rc.agent(t.AgentID)
		if ag != nil && ag.Assignment.FailedConfigHash.String() == t.ToRef {
			if err := as.ClearFailedConfig(rc.ctx, t.AgentID); err != nil {
				return fmt.Errorf("clear failed config of agent %s: %w", t.AgentID, err)
			}
			ag.Assignment.FailedConfigHash = nil
		}
		groups[t.ToRef] = append(groups[t.ToRef], t.AgentID)
		all = append(all, t.AgentID)
	}
	for toRef, ids := range groups {
		h, err := domain.ParseHash(toRef)
		if err != nil || h.IsZero() {
			return fmt.Errorf("rollout target has invalid config hash %q", toRef)
		}
		if err := as.SetDesiredConfig(rc.ctx, ids, h); err != nil {
			return fmt.Errorf("set desired config: %w", err)
		}
		for _, id := range ids {
			if ag := rc.agent(id); ag != nil {
				ag.Assignment.DesiredConfigHash = h
			}
		}
	}
	if rc.e.deps.Control != nil {
		if err := rc.e.deps.Control.DesiredStateChanged(rc.ctx, all); err != nil {
			return fmt.Errorf("push desired state: %w", err)
		}
	}
	return nil
}

// startVersion calls the executor once for all starters of the wave.
func (rc *runCtx) startVersion(cur int, starters []int) (bool, error) {
	r := rc.r
	failAll := func(msg string) (bool, error) {
		for _, i := range starters {
			rc.setTarget(i, domain.TargetFailed, msg)
		}
		return rc.noWorkingTarget(cur), nil
	}
	ex, ok := rc.e.deps.Executors.Get(r.Executor)
	if !ok {
		return failAll(fmt.Sprintf("executor %q is not configured", r.Executor))
	}
	spec, def := rc.e.updateSpec(rc.ctx, r)
	if spec.ExecutorRef != "" && def == nil {
		return failAll(fmt.Sprintf("executor %q does not exist", spec.ExecutorRef))
	}
	if issues := ex.Validate(spec, def); issues.HasErrors() {
		var msgs []string
		for _, is := range issues {
			if is.Severity == domain.SeverityError {
				msgs = append(msgs, is.Message)
			}
		}
		return failAll("executor validation failed: " + strings.Join(msgs, "; "))
	}
	dist, _ := r.Annotations[annDistribution].(string)
	if dist == "" {
		dist = domain.DistributionContrib
	}
	req := executor.UpdateRequest{
		RolloutID: r.ID, Wave: cur, Fleet: r.FleetName, Distribution: dist, ToVersion: r.ToRef,
		Spec: spec, Definition: def,
	}
	for _, i := range starters {
		t := &rc.targets[i]
		ag := rc.agent(t.AgentID)
		tg := executor.Target{
			AgentID: ag.ID, Hostname: ag.Hostname, OSType: ag.OSType, Arch: ag.Arch,
			Attributes: domain.NewAttributeView(ag), CurrentVersion: versionOf(ag), Attempt: t.Attempts,
		}
		if ag.Updater != nil {
			id := ag.Updater.AgentID
			tg.UpdaterAgentID = &id
		}
		req.Targets = append(req.Targets, tg)
	}
	d := &dispatch{kind: dispatchStart, wave: cur, to: r.ToRef, agents: make([]domain.AgentID, len(starters))}
	for k, i := range starters {
		d.agents[k] = rc.targets[i].AgentID
	}
	if !rc.e.launch(rc.ctx, r.ID, d, func(ctx context.Context) ([]executor.TargetResult, error) { return ex.Start(ctx, req) }) {
		return false, nil // collected by collectDispatch on a later tick
	}
	return rc.applyStart(d)
}

// applyStart records the outcome of a finished executor call for the targets
// it covered. Targets that left the pending state while the call ran (abort,
// cancel, rollback) get their update withdrawn again.
func (rc *runCtx) applyStart(d *dispatch) (bool, error) {
	r := rc.r
	cur := d.wave
	var starters []int
	var gone []domain.AgentID
	for _, id := range d.agents {
		if i, ok := rc.idx[id]; ok && (rc.targets[i].Status == domain.TargetPending || rc.targets[i].Status == domain.TargetWaitingWindow) {
			starters = append(starters, i)
		} else {
			gone = append(gone, id)
		}
	}
	if d.err != nil && rc.ctx.Err() != nil {
		return false, rc.ctx.Err()
	}
	// A failed call may still carry the outcome of the batches that were
	// accepted before it failed (timeout); everything else fails.
	failMsg := ""
	if d.err != nil {
		failMsg = "executor call failed: " + d.err.Error()
	}
	byAgent := make(map[domain.AgentID]executor.TargetResult, len(d.results))
	for _, res := range d.results {
		byAgent[res.AgentID] = res
	}
	var started []domain.AgentID
	retry := 0
	for _, i := range starters {
		t := &rc.targets[i]
		res, ok := byAgent[t.AgentID]
		switch {
		case ok && !res.Started && res.Error == "" && res.RetryLater:
			retry++ // e.g. the artifact is still downloading: the target stays pending
		case !ok && failMsg != "":
			rc.setTarget(i, domain.TargetFailed, failMsg)
		case !ok:
			rc.setTarget(i, domain.TargetFailed, "executor returned no result for the agent")
		case res.Error != "":
			rc.setTarget(i, domain.TargetFailed, res.Error)
		case !res.Started && failMsg != "":
			rc.setTarget(i, domain.TargetFailed, failMsg)
		case !res.Started:
			rc.setTarget(i, domain.TargetFailed, "executor did not start the update")
		default:
			rc.setTarget(i, domain.TargetInProgress, "")
			ref := res.ExternalRef
			if ref == "" {
				ref = "started"
			}
			t.ExecutorRef = ref
			started = append(started, t.AgentID)
		}
	}
	allFailed := len(starters) > 0 && len(started) == 0 && retry == 0 && rc.noWorkingTarget(cur)
	if err := rc.flush(); err != nil {
		return allFailed, err
	}
	// Targets whose write was rejected (concurrent abort/cancel) must not keep
	// an update running.
	var lost []domain.AgentID
	for _, id := range started {
		if i, ok := rc.idx[id]; ok && !isInFlight(rc.targets[i].Status) {
			lost = append(lost, id)
		}
	}
	refs := map[string]any{}
	for _, id := range gone {
		if res, ok := byAgent[id]; ok && res.Started && res.Error == "" {
			lost = append(lost, id)
			if res.ExternalRef != "" {
				refs[id.String()] = res.ExternalRef
			}
		}
	}
	if len(lost) > 0 {
		rc.e.cancelExecutor(rc.ctx, r, lost, refs)
	}
	return allFailed, nil
}

// collectDispatch applies the result of an executor call that outlived its
// tick. It reports whether a start call failed for every target of its wave.
func (rc *runCtx) collectDispatch() (allFailed bool, wave int, err error) {
	d := rc.e.takeFinished(rc.r.ID)
	if d == nil {
		return false, 0, nil
	}
	switch d.kind {
	case dispatchRollback:
		if rerr := rc.applyRollback(d); rerr != nil {
			rc.e.log.Warn("rollback step failed (will be retried)", "rollout_id", rc.r.ID, "error", rerr)
		}
		return false, 0, nil
	default:
		allFailed, err = rc.applyStart(d)
		return allFailed, d.wave, err
	}
}

// noWorkingTarget reports that no target of the wave is running or done.
func (rc *runCtx) noWorkingTarget(wave int) bool {
	for i := range rc.targets {
		t := &rc.targets[i]
		if t.Wave != wave {
			continue
		}
		switch t.Status {
		case domain.TargetInProgress, domain.TargetVerifying, domain.TargetSucceeded:
			return false
		}
	}
	return true
}
