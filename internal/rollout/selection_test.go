// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/notify"
)

const operatorRef = "user:5f1c2d3e-0000-4000-8000-000000000001"

// selFleet is fleet "f": Git target gitVersion, updater executor, UI frame.
func selFleet(e *env, gitVersion string, frame *domain.VersionSelectionSpec, mut ...func(f *domain.Fleet)) {
	e.addFleet("f", func(f *domain.Fleet) {
		f.Collector = domain.CollectorSpec{Version: gitVersion}
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorUpdater, Selection: frame}
		for _, m := range mut {
			m(f)
		}
	})
}

func rangeFrame(r string) *domain.VersionSelectionSpec {
	return &domain.VersionSelectionSpec{Enabled: true, VersionRange: r}
}

func (e *env) putSelection(version, gitVersion string) domain.FleetVersionSelection {
	e.t.Helper()
	sel, err := e.st.FleetVersions().Put(e.ctx, domain.FleetVersionSelection{
		Fleet: "f", Version: version, State: domain.SelectionActive, GitVersion: gitVersion, GitCommit: "commit-0",
		SelectedBy: operatorRef, SelectedAt: e.clk.Now(), Reason: "maintenance window",
	}, nil)
	require.NoError(e.t, err)
	return *sel
}

func (e *env) selection() *domain.FleetVersionSelection {
	e.t.Helper()
	sel, err := e.st.FleetVersions().Get(e.ctx, "f")
	if err != nil {
		return nil
	}
	return sel
}

func (e *env) actionsOf(action string) []domain.AuditEvent {
	var out []domain.AuditEvent
	for _, ev := range e.st.AuditEvents() {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

func (e *env) versionRollouts() []domain.Rollout {
	var out []domain.Rollout
	for _, r := range e.rollouts() {
		if r.Kind == domain.RolloutKindVersion {
			out = append(out, r)
		}
	}
	return out
}

// An operator's selection creates the version rollout: origin ui, who/when/why
// on the rollout, the policy of the frame, the operator as audit actor.
func TestSelectionCreatesTheRolloutForTheOperator(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	ui := spec(domain.WaveSpec{Name: "first", Percent: 50}, allWave())
	ui.ApplyTimeout = domain.Duration(15 * time.Minute)
	e.setPolicy("ui-policy", ui)
	selFleet(e, "0.161.0", &domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.161.0, <=0.162.0", RolloutPolicy: "ui-policy"})
	ids := e.addAgents(4, "f", inSync())
	e.st.PutUser(domain.User{ID: uuid.MustParse("5f1c2d3e-0000-4000-8000-000000000001"), Name: "Erika Mustermann"})
	e.publishSnapshot()
	events, cancel := e.bus.Subscribe(eventbus.FleetVersionChanged)
	defer cancel()

	e.plan()
	assert.Empty(t, e.versionRollouts(), "members run the Git target: nothing to do")

	sel := e.putSelection("0.162.0", "0.161.0")
	e.clk.Advance(time.Second)
	e.plan()

	rs := e.versionRollouts()
	require.Len(t, rs, 1)
	r := rs[0]
	assert.Equal(t, "0.162.0", r.ToRef)
	assert.Equal(t, domain.OriginUI, r.Origin)
	assert.Equal(t, operatorRef, r.CreatedBy)
	assert.Equal(t, "maintenance window", r.RequestReason)
	assert.Equal(t, "ui-policy", r.PolicyName, "the frame names the policy")
	assert.Equal(t, 15*time.Minute, r.Policy.ApplyTimeout.D())
	assert.Len(t, r.Waves, 2)
	ann := r.Annotations[domain.SelectionAnnotation].(map[string]any)
	assert.Equal(t, "0.162.0", ann["version"])
	assert.Equal(t, operatorRef, ann["selectedBy"])
	assert.Equal(t, sel.SelectedAt.UTC().Format(time.RFC3339Nano), ann["selectedAt"])
	assert.Equal(t, "maintenance window", ann["reason"])
	assert.Equal(t, "commit-1", r.GitCommit)
	assert.Len(t, e.targets(r.ID), 4)
	assert.Equal(t, "0.162.0", e.targetOf(r.ID, ids[0]).ToRef)

	// audit: the operator created it
	creates := e.actionsOf("rollout.create")
	require.Len(t, creates, 1)
	assert.Equal(t, domain.ActorUser, creates[0].ActorType)
	assert.Equal(t, "5f1c2d3e-0000-4000-8000-000000000001", creates[0].ActorID)
	assert.Equal(t, "Erika Mustermann", creates[0].ActorName)
	assert.Equal(t, "ui", creates[0].Details["origin"])
	assert.Equal(t, operatorRef, creates[0].Details["selectedBy"])
	assert.Equal(t, "maintenance window", creates[0].Details["reason"])
	// the notification names the operator and the reason
	started := e.notif.all()[0]
	assert.Equal(t, notify.EventRolloutStarted, started.Event)
	assert.Contains(t, started.Summary, "selected in the UI by Erika Mustermann: maintenance window")

	// the planner marks the selection as planned (with the rollout) and tells the others
	got := e.selection()
	require.NotNil(t, got.PlannedAt)
	require.NotNil(t, got.RolloutID)
	assert.Equal(t, r.ID, *got.RolloutID)
	assert.True(t, got.UpdatedAt.Equal(sel.UpdatedAt), "planning does not change the compare-and-set version")
	select {
	case ev := <-events:
		var pl eventbus.FleetVersionChangedPayload
		require.NoError(t, ev.Decode(&pl))
		assert.Equal(t, "f", pl.FleetName)
	default:
		t.Fatal("fleet.version_changed expected")
	}
	// the members' target version follows
	assert.Equal(t, "0.162.0", e.st.Agent(ids[0]).Assignment.TargetVersion)

	// planning again changes nothing (idempotent, no new event)
	e.plan()
	assert.Len(t, e.versionRollouts(), 1)
	assert.Len(t, e.actionsOf("rollout.create"), 1)
	assert.Equal(t, r.ID, *e.selection().RolloutID)

	// the rollout runs to the end
	require.True(t, e.runUntil(300, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	for _, id := range ids {
		assert.Equal(t, "0.162.0", e.st.Agent(id).Version)
	}
	assert.Equal(t, 4, e.rollout(r.ID).Counts.Succeeded)

	// planning after the rollout has finished keeps the link to it
	plannedAt := *got.PlannedAt
	e.clk.Advance(time.Minute)
	e.plan()
	after := e.selection()
	require.NotNil(t, after.RolloutID, "the finished rollout stays linked")
	assert.Equal(t, r.ID, *after.RolloutID)
	require.NotNil(t, after.PlannedAt)
	assert.True(t, after.PlannedAt.Equal(plannedAt), "plannedAt is not rewritten")
	assert.Len(t, e.versionRollouts(), 1)
}

// A selection that needs no update is planned without a rollout.
func TestSelectionWithoutWorkIsPlannedWithoutARollout(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0"))
	e.addAgents(2, "f", inSync(), func(i int, a *domain.Agent) { a.Version = "0.162.0" })
	e.publishSnapshot()
	e.putSelection("0.162.0", "0.161.0")
	e.plan()
	assert.Empty(t, e.versionRollouts())
	got := e.selection()
	require.NotNil(t, got.PlannedAt, "planned although nothing had to be done")
	assert.Nil(t, got.RolloutID)
}

// The UI target replaces a running Git rollout (and the other way round).
func TestSelectionSupersedesARunningGitRollout(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.162.0", rangeFrame(">=0.161.0, <=0.163.0"))
	e.addAgents(3, "f", inSync())
	e.publishSnapshot()
	e.plan()
	git := e.versionRollouts()
	require.Len(t, git, 1)
	assert.Equal(t, domain.OriginGitOps, git[0].Origin)
	assert.Equal(t, "gitops", git[0].CreatedBy)
	assert.Empty(t, git[0].RequestReason)
	assert.NotContains(t, git[0].Annotations, domain.SelectionAnnotation)
	e.step() // started

	e.putSelection("0.163.0", "0.162.0")
	e.plan()
	rs := e.versionRollouts()
	require.Len(t, rs, 2)
	assert.Equal(t, domain.RolloutSuperseded, e.rollout(git[0].ID).Status)
	assert.Contains(t, e.rollout(git[0].ID).StatusReason, "the target version was selected in the UI (0.163.0)")
	ui := rs[1]
	assert.Equal(t, "0.163.0", ui.ToRef)
	assert.Equal(t, domain.OriginUI, ui.Origin)
	assert.Equal(t, ui.ID, *e.selection().RolloutID)
}

// Rule K1: Git changes collector.version after the selection - Git wins.
func TestRuleK1GitChangesTheTargetAfterTheSelection(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.163.0"))
	ids := e.addAgents(2, "f", inSync())
	e.publishSnapshot()
	e.putSelection("0.162.0", "0.161.0")
	e.plan()
	ui := e.versionRollouts()
	require.Len(t, ui, 1)
	require.Equal(t, "0.162.0", ui[0].ToRef)

	// a new commit sets the Git target
	e.fleets = nil
	selFleet(e, "0.163.0", rangeFrame(">=0.161.0, <=0.163.0"))
	e.commit = "commit-2"
	e.publishSnapshot()
	e.plan()

	assert.Nil(t, e.selection(), "the selection is gone: the newest intent wins")
	sup := e.actionsOf("fleet.version.superseded")
	require.Len(t, sup, 1)
	assert.Equal(t, domain.ActorGitOps, sup[0].ActorType)
	assert.Equal(t, "fleet", sup[0].TargetType)
	assert.Equal(t, "f", sup[0].TargetID)
	assert.Equal(t, "0.162.0", sup[0].Details["version"])
	assert.Equal(t, "0.161.0", sup[0].Details["gitVersionBefore"])
	assert.Equal(t, "0.163.0", sup[0].Details["gitVersionNow"])
	assert.Equal(t, "commit-2", sup[0].Details["commit"])
	assert.Equal(t, domain.RolloutSuperseded, e.rollout(ui[0].ID).Status)
	rs := e.versionRollouts()
	require.Len(t, rs, 2)
	assert.Equal(t, "0.163.0", rs[1].ToRef)
	assert.Equal(t, domain.OriginGitOps, rs[1].Origin)
	assert.Equal(t, "0.163.0", e.st.Agent(ids[0]).Assignment.TargetVersion)
}

// Rule K2a: Git withdraws the UI control (block removed, disabled, executor none).
func TestRuleK2aGitWithdrawsTheUIControl(t *testing.T) {
	cases := map[string]func(f *domain.Fleet){
		"block removed":  func(f *domain.Fleet) { f.Update.Selection = nil },
		"disabled":       func(f *domain.Fleet) { f.Update.Selection.Enabled = false },
		"executor none":  func(f *domain.Fleet) { f.Update.Executor = domain.ExecutorNone },
		"executor empty": func(f *domain.Fleet) { f.Update.Executor = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.setPolicy("p", spec(allWave()))
			selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.162.0"))
			e.addAgents(2, "f", inSync())
			e.publishSnapshot()
			e.putSelection("0.162.0", "0.161.0")
			e.plan()
			require.Len(t, e.versionRollouts(), 1)

			e.fleets = nil
			selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.162.0"), mut)
			e.commit = "commit-2"
			e.publishSnapshot()
			e.plan()

			assert.Nil(t, e.selection())
			sup := e.actionsOf("fleet.version.superseded")
			require.Len(t, sup, 1)
			assert.Contains(t, []any{"selection disabled", "executor none"}, sup[0].Details["reason"])
			assert.Equal(t, domain.RolloutSuperseded, e.versionRollouts()[0].Status, "the UI rollout is replaced")
		})
	}
}

// Rule K2b: the frame no longer contains the selection - the fleet is on hold,
// nothing is rolled back, nothing is rolled out.
func TestRuleK2bSelectionOutsideTheNewFrameHoldsTheFleet(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.162.0"))
	ids := e.addAgents(3, "f", inSync())
	e.publishSnapshot()
	e.putSelection("0.162.0", "0.161.0")
	e.plan()
	ui := e.versionRollouts()
	require.Len(t, ui, 1)
	e.steps(3) // in flight; some already updated
	require.NotEmpty(t, e.exec.started())

	e.fleets = nil
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <0.162.0"))
	e.commit = "commit-2"
	e.publishSnapshot()
	e.plan()

	sel := e.selection()
	require.NotNil(t, sel, "the selection stays (invalidated), it is not deleted")
	assert.Equal(t, domain.SelectionInvalidated, sel.State)
	assert.Contains(t, sel.StateReason, "version 0.162.0 is outside the frame of commit commit-2")
	assert.Contains(t, sel.StateReason, "<0.162.0")
	inv := e.actionsOf("fleet.version.invalidated")
	require.Len(t, inv, 1)
	assert.Equal(t, domain.ActorGitOps, inv[0].ActorType)
	assert.Equal(t, "0.162.0", inv[0].Details["version"])
	assert.Equal(t, ">=0.161.0, <0.162.0", inv[0].Details["frame"])
	assert.Equal(t, "commit-2", inv[0].Details["commit"])

	got := e.rollout(ui[0].ID)
	assert.Equal(t, domain.RolloutSuperseded, got.Status)
	assert.Contains(t, got.StatusReason, "the fleet no longer manages the collector version")
	assert.Len(t, e.versionRollouts(), 1, "no new rollout, no automatic downgrade")
	for _, id := range ids {
		assert.Empty(t, e.st.Agent(id).Assignment.TargetVersion, "no target while on hold")
	}
	// agents that were updated stay where they are
	updated := 0
	for _, id := range ids {
		if e.st.Agent(id).Version == "0.162.0" {
			updated++
		}
	}
	assert.Greater(t, updated, 0)

	// further plans keep the hold and stay quiet
	e.plan()
	assert.Len(t, e.versionRollouts(), 1)
	assert.Len(t, e.actionsOf("fleet.version.invalidated"), 1)

	// widening the frame again does not revive the selection
	e.fleets = nil
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.162.0"))
	e.commit = "commit-3"
	e.publishSnapshot()
	e.plan()
	assert.Equal(t, domain.SelectionInvalidated, e.selection().State)
	assert.Len(t, e.versionRollouts(), 1)

	// clearing it hands the decision back to Git
	_, err := e.st.FleetVersions().Delete(e.ctx, "f", nil)
	require.NoError(t, err)
	e.plan()
	assert.Equal(t, "0.161.0", e.st.Agent(ids[0]).Assignment.TargetVersion)
}

// Rule K4: a fleet that left Git takes its selection with it.
func TestRuleK4FleetRemovedFromGit(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0"))
	e.addFleet("other", func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	e.addAgents(1, "f", inSync())
	e.publishSnapshot()
	e.putSelection("0.162.0", "0.161.0")
	e.plan()
	require.NotNil(t, e.selection())

	e.fleets = e.fleets[1:] // "f" is gone
	e.commit = "commit-2"
	e.publishSnapshot()
	e.plan()
	assert.Nil(t, e.selection())
	rm := e.actionsOf("fleet.version.removed")
	require.Len(t, rm, 1)
	assert.Equal(t, domain.ActorGitOps, rm[0].ActorType)
	assert.Equal(t, "f", rm[0].TargetID)
	assert.Equal(t, "0.162.0", rm[0].Details["version"])
	assert.Equal(t, "commit-2", rm[0].Details["commit"])
}

// A snapshot that was never synced does not delete selections (nothing is known yet).
func TestReconcileDoesNothingBeforeTheFirstSync(t *testing.T) {
	e := newEnv(t)
	e.putSelection("0.162.0", "0.161.0")
	require.NoError(t, e.eng.ReconcileSelections(e.ctx, &domain.GitOpsSnapshot{}))
	assert.NotNil(t, e.selection())
	require.NoError(t, e.eng.ReconcileSelections(e.ctx, nil))
	assert.NotNil(t, e.selection())
}

// ReconcileSelections is what the syncer calls before the agents are reassigned.
func TestReconcileSelectionsIsIdempotentAndAudited(t *testing.T) {
	e := newEnv(t)
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <0.162.0"))
	e.addAgents(1, "f", inSync())
	e.publishSnapshot()
	e.putSelection("0.162.0", "0.161.0")
	snap, err := e.st.GitOps().GetSnapshot(e.ctx)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.NoError(t, e.eng.ReconcileSelections(e.ctx, snap))
	}
	assert.Equal(t, domain.SelectionInvalidated, e.selection().State)
	assert.Len(t, e.actionsOf("fleet.version.invalidated"), 1, "audited once")
}

// A selection that was replaced while the leader reconciled is left alone.
func TestReconcileDoesNotTouchAReplacedSelection(t *testing.T) {
	e := newEnv(t)
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <0.162.0"))
	e.addAgents(1, "f", inSync())
	e.publishSnapshot()
	old := e.putSelection("0.162.0", "0.161.0") // outside the frame
	snap, _ := e.st.GitOps().GetSnapshot(e.ctx)
	// the operator replaces it with an allowed version before the leader writes
	_, err := e.st.FleetVersions().Put(e.ctx, domain.FleetVersionSelection{Fleet: "f", Version: "0.161.5", State: domain.SelectionActive,
		GitVersion: "0.161.0", SelectedBy: operatorRef, SelectedAt: e.clk.Now().Add(time.Second)}, &old.UpdatedAt)
	require.NoError(t, err)
	ok, err := e.st.FleetVersions().Invalidate(e.ctx, "f", "stale view", old.UpdatedAt)
	require.NoError(t, err)
	assert.False(t, ok, "compare-and-set refuses the stale row")
	require.NoError(t, e.eng.ReconcileSelections(e.ctx, snap))
	assert.Equal(t, domain.SelectionActive, e.selection().State)
	assert.Equal(t, "0.161.5", e.selection().Version)
}

// The executor "manual" creates version rollouts and waits for the reported version.
func TestManualExecutorRolloutWaitsForTheReportedVersion(t *testing.T) {
	e := newEnv(t)
	e.exec.typ = domain.ExecutorManual
	s := spec(allWave())
	s.ApplyTimeout = domain.Duration(10 * time.Minute)
	e.setPolicy("p", s)
	e.addFleet("f", func(f *domain.Fleet) {
		f.Collector = domain.CollectorSpec{Version: "0.162.0"}
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorManual}
	})
	ids := e.addAgents(2, "f", inSync())
	e.noInstall[ids[0]], e.noInstall[ids[1]] = true, true // the version changes outside of myriarchd
	e.publishSnapshot()
	e.plan()
	r := e.only()
	assert.Equal(t, domain.ExecutorManual, r.Executor)
	e.step()
	for _, id := range ids {
		tg := e.targetOf(r.ID, id)
		assert.Equal(t, domain.TargetInProgress, tg.Status)
		require.NotNil(t, tg.Progress)
		assert.Equal(t, domain.ProgressExecutor, tg.Progress.Source)
	}
	e.steps(10)
	assert.Equal(t, domain.RolloutRunning, e.rollout(r.ID).Status, "waits as long as the version is not reported")

	// somebody updates one collector (the image tag was merged)
	e.st.UpdateAgent(ids[0], func(a *domain.Agent) { a.Version = "0.162.0" })
	delete(e.noInstall, ids[0])
	e.exec.installs[ids[0]] = "0.162.0"
	require.True(t, e.runUntil(30, func() bool { return e.targetOf(r.ID, ids[0]).Status == domain.TargetSucceeded }))
	assert.Equal(t, domain.TargetInProgress, e.targetOf(r.ID, ids[1]).Status)

	// the other one never changes: the policy's applyTimeout ends the wait
	e.clk.Advance(11 * time.Minute)
	e.steps(2)
	tg := e.targetOf(r.ID, ids[1])
	assert.Equal(t, domain.TargetFailed, tg.Status)
	assert.Contains(t, tg.LastError, "timed out: the version 0.162.0 was not reported within 10m0s")
	assert.Equal(t, domain.PhaseFailed, tg.Progress.Phase)
	assert.Equal(t, domain.StepFailed, tg.Progress.Steps[1].Status, "the external update step failed")
}

// The manual executor is a normal choice for the UI frame, too.
func TestManualExecutorWorksWithTheUIFrame(t *testing.T) {
	e := newEnv(t)
	e.exec.typ = domain.ExecutorManual
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0"), func(f *domain.Fleet) { f.Update.Executor = domain.ExecutorManual })
	e.addAgents(2, "f", inSync())
	e.publishSnapshot()
	e.putSelection("0.162.0", "0.161.0")
	e.plan()
	rs := e.versionRollouts()
	require.Len(t, rs, 1)
	assert.Equal(t, domain.OriginUI, rs[0].Origin)
	assert.Equal(t, domain.ExecutorManual, rs[0].Executor)
	require.NoError(t, e.eng.Plan(context.Background()))
}

// What an operator ended (abort, rollback, failure) is not started again by
// the same selection, but an operator who selects again asks anew.
func TestANewSelectionRestartsWhatEndedBeforeIt(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.163.0"))
	ids := e.addAgents(2, "f", inSync())
	e.noInstall[ids[0]], e.noInstall[ids[1]] = true, true
	e.publishSnapshot()
	first := e.putSelection("0.162.0", "0.161.0")
	e.plan()
	rs := e.versionRollouts()
	require.Len(t, rs, 1)
	e.step()
	_, err := e.eng.Action(e.ctx, rs[0].ID, domain.ActionAbort, 0, "wrong time", nil)
	require.NoError(t, err)
	assert.Equal(t, domain.RolloutAborted, e.rollout(rs[0].ID).Status)

	// the same selection does not start it again
	e.clk.Advance(time.Minute)
	e.plan()
	assert.Len(t, e.versionRollouts(), 1, "aborted on purpose: no restart by the same selection")

	// the operator selects again (clear + select, or replace): a new decision
	e.clk.Advance(time.Minute)
	_, err = e.st.FleetVersions().Put(e.ctx, domain.FleetVersionSelection{
		Fleet: "f", Version: "0.162.0", State: domain.SelectionActive, GitVersion: "0.161.0", SelectedBy: operatorRef, SelectedAt: e.clk.Now(), Reason: "again",
	}, &first.UpdatedAt)
	require.NoError(t, err)
	e.plan()
	rs = e.versionRollouts()
	require.Len(t, rs, 2)
	assert.Equal(t, domain.OriginUI, rs[1].Origin)
	assert.Equal(t, "again", rs[1].RequestReason)
	assert.Len(t, e.targets(rs[1].ID), 2)
	assert.Equal(t, rs[1].ID, *e.selection().RolloutID)

	// and a failure of THAT rollout excludes the agent again (until the next selection)
	e.step()
	_, err = e.eng.Action(e.ctx, rs[1].ID, domain.ActionAbort, 0, "", nil)
	require.NoError(t, err)
	e.clk.Advance(time.Minute)
	e.plan()
	assert.Len(t, e.versionRollouts(), 2)
}
