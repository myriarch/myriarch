// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func frameFleet(version string, exec ExecutorType, sel *VersionSelectionSpec) *Fleet {
	f := &Fleet{ObjectMeta: ObjectMeta{Name: "gw"}}
	f.Collector.Version = version
	f.Update.Executor = exec
	f.Update.Selection = sel
	return f
}

func TestAllows(t *testing.T) {
	rng := &VersionSelectionSpec{Enabled: true, VersionRange: ">=0.161.0, <=0.162.0"}
	list := &VersionSelectionSpec{Enabled: true, Versions: []string{"0.161.0", "0.162.0"}}
	cases := []struct {
		name    string
		spec    *VersionSelectionSpec
		version string
		want    bool
	}{
		{"range lower bound", rng, "0.161.0", true},
		{"range upper bound", rng, "0.162.0", true},
		{"range below", rng, "0.160.9", false},
		{"range above", rng, "0.162.1", false},
		{"range with v prefix in the input", rng, "v0.161.5", true},
		{"range space separated is and", &VersionSelectionSpec{VersionRange: ">=0.161.0 <0.162.0"}, "0.161.9", true},
		{"prerelease never satisfies a plain range", rng, "0.162.0-rc.1", false},
		{"prerelease satisfies a prerelease range", &VersionSelectionSpec{VersionRange: ">=0.162.0-0, <0.163.0"}, "0.162.0-rc.1", true},
		{"list member", list, "0.162.0", true},
		{"list member with v prefix", list, "v0.161.0", true},
		{"list non member", list, "0.161.5", false},
		{"unparsable version", rng, "latest", false},
		{"short version", rng, "0.161", false},
		{"unparsable range", &VersionSelectionSpec{VersionRange: ">=nonsense"}, "0.161.0", false},
		{"empty frame", &VersionSelectionSpec{}, "0.161.0", false},
		{"nil frame", nil, "0.161.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.spec.Allows(tc.version))
		})
	}
}

func TestSelectionActive(t *testing.T) {
	var nilSpec *VersionSelectionSpec
	assert.False(t, nilSpec.Active())
	assert.False(t, (&VersionSelectionSpec{}).Active())
	assert.True(t, (&VersionSelectionSpec{Enabled: true}).Active())
}

func TestFrameText(t *testing.T) {
	assert.Equal(t, ">=1.0.0", (&VersionSelectionSpec{VersionRange: ">=1.0.0"}).FrameText())
	assert.Equal(t, "[0.1.0, 0.2.0]", (&VersionSelectionSpec{Versions: []string{"0.1.0", "0.2.0"}}).FrameText())
}

func TestResolveVersionTarget(t *testing.T) {
	git := frameFleet("v0.161.0", ExecutorUpdater, nil)
	none := frameFleet("", ExecutorUpdater, nil)
	active := &FleetVersionSelection{Fleet: "gw", Version: "0.162.0", State: SelectionActive}
	invalid := &FleetVersionSelection{Fleet: "gw", Version: "0.162.0", State: SelectionInvalidated}

	cases := []struct {
		name    string
		fleet   *Fleet
		sel     *FleetVersionSelection
		version string
		source  VersionTargetSource
	}{
		{"active selection wins over git", git, active, "0.162.0", TargetSourceUI},
		{"active selection without git target", none, active, "0.162.0", TargetSourceUI},
		{"invalidated selection holds the fleet", git, invalid, "", TargetSourceHold},
		{"git target", git, nil, "0.161.0", TargetSourceGit},
		{"nothing", none, nil, "", TargetSourceNone},
		{"missing fleet", nil, nil, "", TargetSourceNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveVersionTarget(tc.fleet, tc.sel)
			assert.Equal(t, tc.version, got.Version)
			assert.Equal(t, tc.source, got.Source)
			assert.Equal(t, tc.sel, got.Selection)
		})
	}
}

func TestVersionRolloutsEnabled(t *testing.T) {
	for exec, want := range map[ExecutorType]bool{
		"": false, ExecutorNone: false, ExecutorUpdater: true, ExecutorWebhook: true, ExecutorAWX: true, ExecutorManual: true,
	} {
		assert.Equal(t, want, VersionRolloutsEnabled(frameFleet("0.1.0", exec, nil)), "executor %q", exec)
	}
}

func TestReconcileSelection(t *testing.T) {
	frame := &VersionSelectionSpec{Enabled: true, VersionRange: ">=0.161.0, <=0.162.0"}
	sel := func(version, gitVersion string, state VersionSelectionState) FleetVersionSelection {
		return FleetVersionSelection{Fleet: "gw", Version: version, GitVersion: gitVersion, State: state, SelectedAt: time.Now()}
	}
	cases := []struct {
		name   string
		fleet  *Fleet
		sel    FleetVersionSelection
		want   SelectionDecision
		reason string // substring
	}{
		{"K4 fleet removed", nil, sel("0.162.0", "0.161.0", SelectionActive), SelectionRemove, "no longer exists"},
		{"K4 beats everything even for an invalidated selection", nil, sel("0.162.0", "", SelectionInvalidated), SelectionRemove, ""},
		{"K1 git target changed", frameFleet("0.161.5", ExecutorUpdater, frame), sel("0.162.0", "0.161.0", SelectionActive), SelectionSupersede, "from 0.161.0 to 0.161.5"},
		{"K1 git target set after the selection", frameFleet("0.161.0", ExecutorUpdater, frame), sel("0.162.0", "", SelectionActive), SelectionSupersede, "from none to 0.161.0"},
		{"K1 git target removed", frameFleet("", ExecutorUpdater, frame), sel("0.162.0", "0.161.0", SelectionActive), SelectionSupersede, "from 0.161.0 to none"},
		{"K1 v prefix is not a change", frameFleet("v0.161.0", ExecutorUpdater, frame), sel("0.162.0", "0.161.0", SelectionActive), SelectionKeep, ""},
		{"K1 also for an invalidated selection", frameFleet("0.162.0", ExecutorUpdater, frame), sel("0.170.0", "0.161.0", SelectionInvalidated), SelectionSupersede, ""},
		{"K1 beats K2a", frameFleet("0.162.0", ExecutorNone, nil), sel("0.162.0", "0.161.0", SelectionActive), SelectionSupersede, "Git changed"},
		{"K2a block removed", frameFleet("0.161.0", ExecutorUpdater, nil), sel("0.162.0", "0.161.0", SelectionActive), SelectionSupersede, "selection disabled"},
		{"K2a disabled", frameFleet("0.161.0", ExecutorUpdater, &VersionSelectionSpec{VersionRange: ">=0.161.0"}), sel("0.162.0", "0.161.0", SelectionActive), SelectionSupersede, "selection disabled"},
		{"K2a executor none", frameFleet("0.161.0", ExecutorNone, frame), sel("0.162.0", "0.161.0", SelectionActive), SelectionSupersede, "executor none"},
		{"K2a executor empty", frameFleet("0.161.0", "", frame), sel("0.162.0", "0.161.0", SelectionActive), SelectionSupersede, "executor none"},
		{"K2b outside the frame", frameFleet("0.161.0", ExecutorUpdater, &VersionSelectionSpec{Enabled: true, VersionRange: ">=0.161.0, <0.162.0"}), sel("0.162.0", "0.161.0", SelectionActive), SelectionInvalidate, "outside the frame"},
		{"K2b with a list frame", frameFleet("0.161.0", ExecutorManual, &VersionSelectionSpec{Enabled: true, Versions: []string{"0.161.0"}}), sel("0.162.0", "0.161.0", SelectionActive), SelectionInvalidate, "[0.161.0]"},
		{"K5 inside the frame", frameFleet("0.161.0", ExecutorUpdater, frame), sel("0.162.0", "0.161.0", SelectionActive), SelectionKeep, ""},
		{"K5 invalidated stays invalidated although the frame was widened", frameFleet("0.161.0", ExecutorUpdater, frame), sel("0.162.0", "0.161.0", SelectionInvalidated), SelectionKeep, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ReconcileSelection(tc.fleet, tc.sel, "0123456789abcdef")
			assert.Equal(t, tc.want, got, reason)
			if tc.reason != "" {
				assert.Contains(t, reason, tc.reason)
			}
		})
	}
	_, reason := ReconcileSelection(frameFleet("0.161.0", ExecutorUpdater, &VersionSelectionSpec{Enabled: true, VersionRange: "<0.162.0"}),
		sel("0.162.0", "0.161.0", SelectionActive), "0123456789abcdef")
	assert.Equal(t, "version 0.162.0 is outside the frame of commit 0123456789ab (<0.162.0)", reason)
}

func TestOriginOf(t *testing.T) {
	assert.Equal(t, OriginGitOps, OriginOf("gitops"))
	assert.Equal(t, OriginUI, OriginOf("user:5f1c"))
	assert.Equal(t, OriginUI, OriginOf("token:5f1c"))
	assert.Equal(t, OriginSystem, OriginOf("system"))
	assert.Equal(t, OriginSystem, OriginOf(""))
}

func TestProgressSameContent(t *testing.T) {
	now := time.Now()
	pct := 10.0
	a := &TargetProgress{Phase: PhaseDownloading, Source: ProgressUpdater, DownloadPercent: &pct, UpdatedAt: now,
		Steps: []UpdateStep{{Name: StepOffered, Status: StepDone, StartedAt: &now}}}
	b := *a
	b.UpdatedAt = now.Add(time.Hour)
	assert.True(t, a.SameContent(&b), "UpdatedAt is ignored")
	c := *a
	other := 11.0
	c.DownloadPercent = &other
	assert.False(t, a.SameContent(&c))
	d := *a
	d.Steps = []UpdateStep{{Name: StepOffered, Status: StepRunning, StartedAt: &now}}
	assert.False(t, a.SameContent(&d))
	assert.True(t, (*TargetProgress)(nil).SameContent(nil))
	assert.False(t, a.SameContent(nil))
}

func TestProgressOfSettlesTargetsEndedFromOutside(t *testing.T) {
	now := time.Now()
	steps := func() []UpdateStep {
		return []UpdateStep{
			{Name: StepOffered, Status: StepDone},
			{Name: StepDownloading, Status: StepRunning, Detail: "37%"},
			{Name: StepInstalling, Status: StepPending},
		}
	}
	pct := 37.0
	mk := func(status TargetStatus, phase UpdatePhase) RolloutTarget {
		return RolloutTarget{Status: status, LastError: "stopped by an operator", Progress: &TargetProgress{
			Phase: phase, Source: ProgressUpdater, Steps: steps(), DownloadPercent: &pct, UpdatedAt: now,
		}}
	}
	names := func(p *TargetProgress) []UpdateStepStatus {
		var out []UpdateStepStatus
		for _, s := range p.Steps {
			out = append(out, s.Status)
		}
		return out
	}

	cancelled := ProgressOf(mk(TargetCancelled, PhaseDownloading))
	assert.Equal(t, PhaseCancelled, cancelled.Phase)
	assert.Equal(t, []UpdateStepStatus{StepDone, StepSkipped, StepSkipped}, names(cancelled))
	assert.Nil(t, cancelled.DownloadPercent)
	assert.Equal(t, "stopped by an operator", cancelled.Error)

	failed := ProgressOf(mk(TargetFailed, PhaseDownloading))
	assert.Equal(t, PhaseFailed, failed.Phase)
	assert.Equal(t, []UpdateStepStatus{StepDone, StepFailed, StepSkipped}, names(failed))
	assert.Equal(t, "stopped by an operator", failed.Steps[1].Detail, "the running step carries the error")

	// the host rolled back although the engine failed the target: the precise phase stays
	hostRolledBack := ProgressOf(mk(TargetFailed, PhaseRolledBack))
	assert.Equal(t, PhaseRolledBack, hostRolledBack.Phase)

	rolledBack := ProgressOf(mk(TargetRolledBack, PhaseDownloading))
	assert.Equal(t, PhaseRolledBack, rolledBack.Phase)
	assert.Equal(t, []UpdateStepStatus{StepDone, StepSkipped, StepSkipped}, names(rolledBack))
	// a target that succeeded and was rolled back later changes its final phase
	was := mk(TargetRolledBack, PhaseSucceeded)
	assert.Equal(t, PhaseRolledBack, ProgressOf(was).Phase)

	skipped := ProgressOf(mk(TargetSkipped, PhaseOffered))
	assert.Equal(t, PhaseCancelled, skipped.Phase)

	done := ProgressOf(mk(TargetSucceeded, PhaseHealthWait))
	assert.Equal(t, PhaseSucceeded, done.Phase)
	assert.Equal(t, []UpdateStepStatus{StepDone, StepDone, StepDone}, names(done))

	// nothing to settle: running targets and finished progress are returned as they are
	running := mk(TargetInProgress, PhaseDownloading)
	assert.True(t, running.Progress.SameContent(ProgressOf(running)))
	final := mk(TargetFailed, PhaseFailed)
	final.Progress.Steps = []UpdateStep{{Name: StepOffered, Status: StepDone}, {Name: StepDownloading, Status: StepFailed}}
	assert.True(t, final.Progress.SameContent(ProgressOf(final)))
	assert.Nil(t, ProgressOf(RolloutTarget{Status: TargetPending}))

	// a copy: the stored value is not rewritten
	src := mk(TargetCancelled, PhaseDownloading)
	_ = ProgressOf(src)
	assert.Equal(t, PhaseDownloading, src.Progress.Phase)
	assert.Equal(t, StepRunning, src.Progress.Steps[1].Status)
	assert.NotNil(t, src.Progress.DownloadPercent)
}
