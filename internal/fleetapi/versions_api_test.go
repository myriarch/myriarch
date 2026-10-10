// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

// editFleet changes a fleet of the stored snapshot (a new Git state) and
// reloads the assignment cache.
func (e *apiEnv) editFleet(name string, mut func(f *domain.Fleet)) {
	e.t.Helper()
	ctx := context.Background()
	snap, err := e.st.GitOps().GetSnapshot(ctx)
	require.NoError(e.t, err)
	found := false
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == name {
			mut(&snap.Fleets[i])
			found = true
		}
	}
	require.True(e.t, found, name)
	_, err = e.st.GitOps().ApplySnapshot(ctx, *snap, domain.GitOpsSync{Trigger: "poll", Result: "applied", StartedAt: e.clock, Commit: &domain.GitCommit{SHA: snap.Commit}})
	require.NoError(e.t, err)
	_, err = e.assigner.Reload(ctx)
	require.NoError(e.t, err)
}

// frame enables the selection frame of a fleet.
func frame(spec domain.VersionSelectionSpec) func(f *domain.Fleet) {
	spec.Enabled = true
	return func(f *domain.Fleet) { f.Update.Selection = &spec }
}

// startLeader plans on every fleet.version_changed event like the engine of
// the leader replica does.
func (e *apiEnv) startLeader() {
	e.t.Helper()
	events, cancel := e.bus.Subscribe(eventbus.FleetVersionChanged)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-events:
				_ = e.engine.Plan(ctx)
			}
		}
	}()
	e.t.Cleanup(func() { stop(); cancel(); <-done })
}

func (e *apiEnv) selectVersion(fleet string, body any) resp {
	return e.do(http.MethodPost, "/fleets/"+fleet+"/actions/select-version", "operator", body, "X-Test-User-ID", "5f1c2d3e-0000-4000-8000-000000000001")
}

func (e *apiEnv) auditOf(action string) []domain.AuditEvent {
	var out []domain.AuditEvent
	for _, ev := range e.st.AuditEvents() {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

func optionsByVersion(t *testing.T, v map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, o := range v["options"].([]any) {
		m := asMap(t, o)
		out[m["version"].(string)] = m
	}
	return out
}

// edgeNL: edge-nl runs the updater executor, targets 0.162.0 in Git, two members.
func edgeNL(t *testing.T, mut ...func(f *domain.Fleet)) *apiEnv {
	e := newAPIEnv(t)
	a := e.addAgent("edge-nl-1001") // 0.161.0
	b := e.addAgent("edge-nl-1003") // 0.162.0
	_, _ = a, b
	for _, m := range mut {
		e.editFleet("edge-nl", m)
	}
	return e
}

func TestVersionOptionsInsideTheFrame(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.160.0, <=0.162.0", RolloutPolicy: "standard"}), func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	v := asMap(t, e.expect(e.get("/fleets/edge-nl/version-options"), "getFleetVersionOptions", http.StatusOK))
	assert.Equal(t, "edge-nl", v["fleet"])
	assert.Equal(t, "otelcol-contrib", v["distribution"])
	assert.Equal(t, true, v["allowed"])
	assert.NotContains(t, v, "notAllowedCode")
	assert.Equal(t, "updater", v["executor"])
	assert.Equal(t, "standard", v["versionPolicy"], "the policy of the frame wins over spec.rollout.versionPolicy")
	target := v["target"].(map[string]any)
	assert.Equal(t, "git", target["source"])
	assert.Equal(t, "0.161.0", target["version"])
	assert.Equal(t, true, target["selectionEnabled"])
	assert.Equal(t, ">=0.160.0, <=0.162.0", target["frame"].(map[string]any)["versionRange"])
	assert.Equal(t, []any{map[string]any{"os": "windows", "arch": "amd64", "agents": float64(2)}}, v["platforms"])

	opts := optionsByVersion(t, v)
	var order []string
	for _, o := range v["options"].([]any) {
		order = append(order, o.(map[string]any)["version"].(string))
	}
	assert.Equal(t, []string{"0.162.0", "0.161.0", "0.160.0"}, order, "newest first; known stable releases inside the frame")

	latest := opts["0.162.0"]
	assert.Equal(t, true, latest["selectable"])
	assert.Equal(t, true, latest["known"])
	assert.Equal(t, true, latest["isLatest"])
	assert.Equal(t, false, latest["isCurrentTarget"])
	assert.Equal(t, false, latest["isDowngrade"])
	assert.EqualValues(t, 1, latest["agentsToUpdate"], "one member runs 0.161.0")
	assert.Greater(t, latest["breakingCount"], float64(0), "changes between the current target 0.161.0 and 0.162.0")
	assert.NotEmpty(t, latest["publishedAt"])

	cur := opts["0.161.0"]
	assert.Equal(t, false, cur["selectable"])
	assert.Equal(t, "current_target", cur["reasonCode"])
	assert.Equal(t, true, cur["isCurrentTarget"])
	assert.Equal(t, true, cur["isGitVersion"])
	assert.EqualValues(t, 1, cur["agentsToUpdate"], "one member runs 0.162.0")

	down := opts["0.160.0"]
	assert.Equal(t, false, down["selectable"])
	assert.Equal(t, "downgrade_not_allowed", down["reasonCode"])
	assert.Equal(t, true, down["isDowngrade"])
	assert.NotEmpty(t, down["reason"])
	assert.EqualValues(t, 0, down["breakingCount"], "no comparison for a downgrade")
}

func TestVersionOptionsListFrameAndDowngradeAllowed(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{Versions: []string{"0.160.0", "0.162.0", "0.163.0"}, AllowDowngrade: true}))
	v := asMap(t, e.expect(e.get("/fleets/edge-nl/version-options"), "getFleetVersionOptions", http.StatusOK))
	opts := optionsByVersion(t, v)
	require.Contains(t, opts, "0.160.0")
	assert.Equal(t, true, opts["0.160.0"]["selectable"], "allowDowngrade")
	assert.Equal(t, true, opts["0.160.0"]["isDowngrade"])
	// listed but not a known release: selectable only because the list names it
	require.Contains(t, opts, "0.163.0")
	assert.Equal(t, false, opts["0.163.0"]["known"])
	assert.Equal(t, true, opts["0.163.0"]["selectable"])
	assert.NotContains(t, opts, "0.161.0", "known releases outside the list are no candidates")
}

func TestVersionOptionsWhyNotAllowed(t *testing.T) {
	// no frame in Git: only the Git target is listed
	e := newAPIEnv(t)
	e.addAgent("office-app-01")
	v := asMap(t, e.expect(e.get("/fleets/office/version-options"), "getFleetVersionOptions", http.StatusOK))
	assert.Equal(t, false, v["allowed"])
	assert.Equal(t, "selection_disabled", v["notAllowedCode"])
	assert.NotEmpty(t, v["notAllowedReason"])
	require.Len(t, v["options"], 1)
	only := v["options"].([]any)[0].(map[string]any)
	assert.Equal(t, "0.162.0", only["version"])
	assert.Equal(t, "current_target", only["reasonCode"])
	assert.Equal(t, "awx", v["executor"])
	e.expect(e.get("/fleets/nope/version-options"), "getFleetVersionOptions", http.StatusNotFound)

	// executor none
	e.editFleet("office", func(f *domain.Fleet) {
		f.Update = domain.UpdateSpec{Selection: &domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.160.0"}}
	})
	v = asMap(t, e.expect(e.get("/fleets/office/version-options"), "getFleetVersionOptions", http.StatusOK))
	assert.Equal(t, "no_executor", v["notAllowedCode"])
	assert.Equal(t, "none", v["executor"])

	// a version rollout that needs attention blocks
	e.editFleet("office", func(f *domain.Fleet) {
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorManual, Selection: &domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.160.0"}}
	})
	ro := &domain.Rollout{ID: uuid.New(), Kind: domain.RolloutKindVersion, FleetName: "office", PolicyName: "standard", Status: domain.RolloutFailed, ToRef: "0.162.0",
		CreatedBy: "gitops", Policy: domain.DefaultRolloutPolicy(), Waves: []domain.RolloutWave{{Index: 0, Name: "all"}}, CreatedAt: e.clock}
	require.NoError(t, e.st.Rollouts().Create(context.Background(), ro, nil))
	v = asMap(t, e.expect(e.get("/fleets/office/version-options"), "getFleetVersionOptions", http.StatusOK))
	assert.Equal(t, false, v["allowed"])
	assert.Equal(t, "blocking_rollout", v["notAllowedCode"])
	assert.Equal(t, ro.ID.String(), v["activeRollout"].(map[string]any)["id"])
	assert.Equal(t, "gitops", v["activeRollout"].(map[string]any)["origin"])
}

func TestSelectVersionStartsTheRolloutAndMarksItAsSetInTheUI(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.161.0, <=0.162.0", RolloutPolicy: "standard"}), func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	e.startLeader()
	// settle: the Git target 0.161.0 needs nothing for 0.161.0/0.162.0 members? one member runs 0.162.0 (downgrade rollout)
	e.st.UpdateAgent(e.agents["edge-nl-1003"], func(a *domain.Agent) { a.Version = "0.161.0" })

	r := e.selectVersion("edge-nl", map[string]any{"version": "0.162.0", "reason": "weekly maintenance window", "expectedTargetVersion": "0.161.0"})
	v := asMap(t, e.expect(r, "selectFleetVersion", http.StatusOK))
	assert.Equal(t, true, v["planned"])
	require.NotEmpty(t, v["rolloutId"])
	assert.EqualValues(t, 2, v["targets"])
	target := v["target"].(map[string]any)
	assert.Equal(t, "ui", target["source"])
	assert.Equal(t, "0.162.0", target["version"])
	sel := target["selection"].(map[string]any)
	assert.Equal(t, "weekly maintenance window", sel["reason"])
	assert.Equal(t, "user:5f1c2d3e-0000-4000-8000-000000000001", sel["selectedBy"], "unknown accounts are shown by their reference")
	assert.Equal(t, "0.161.0", sel["gitVersion"])
	assert.Equal(t, "abc123def456", sel["gitCommit"])
	assert.Equal(t, v["rolloutId"], sel["rolloutId"])
	assert.NotEmpty(t, sel["plannedAt"])

	// the rollout: origin ui, who and why, the policy of the frame; the operator is shown by name once the account is known
	e.st.PutUser(domain.User{ID: uuid.MustParse("5f1c2d3e-0000-4000-8000-000000000001"), Name: "Erika Mustermann"})
	ro := asMap(t, e.expect(e.get("/rollouts/"+v["rolloutId"].(string)), "getRollout", http.StatusOK))
	assert.Equal(t, "ui", ro["origin"])
	assert.Equal(t, "Erika Mustermann", ro["createdBy"])
	assert.Equal(t, "weekly maintenance window", ro["requestReason"])
	assert.Equal(t, "standard", ro["policyName"])
	assert.Equal(t, "0.162.0", ro["toRef"])
	ann := ro["annotations"].(map[string]any)["selection"].(map[string]any)
	assert.Equal(t, "0.162.0", ann["version"])
	assert.Equal(t, "Erika Mustermann", ann["selectedBy"], "the annotation shows the operator by name, not by reference")
	list := asMap(t, e.expect(e.get("/rollouts?kind=version"), "listRollouts", http.StatusOK))
	first := asList(t, list)[0].(map[string]any)
	assert.Equal(t, "ui", first["origin"])
	assert.Equal(t, "Erika Mustermann", first["createdBy"])

	// the fleet shows where its target comes from, and counts the members against it
	fl := asMap(t, e.expect(e.get("/fleets/edge-nl"), "getFleet", http.StatusOK))
	vt := fl["status"].(map[string]any)["versionTarget"].(map[string]any)
	assert.Equal(t, "ui", vt["source"])
	assert.Equal(t, "0.162.0", vt["version"])
	assert.Equal(t, "0.161.0", vt["gitVersion"])
	assert.Equal(t, "0.162.0", fl["status"].(map[string]any)["agents"].(map[string]any)["targetVersion"])

	// audit: the selection, and the rollout that was created for it (by the operator)
	sels := e.auditOf("fleet.version.select")
	require.Len(t, sels, 1)
	assert.Equal(t, "success", sels[0].Result)
	assert.Equal(t, domain.ActorUser, sels[0].ActorType)
	assert.Equal(t, "fleet", sels[0].TargetType)
	assert.Equal(t, "edge-nl", sels[0].TargetID)
	assert.Equal(t, "0.162.0", sels[0].Details["version"])
	assert.Equal(t, "weekly maintenance window", sels[0].Details["reason"])
	assert.Equal(t, "0.161.0", sels[0].Details["previousVersion"])
	assert.Equal(t, "git", sels[0].Details["previousSource"])
	assert.Equal(t, "0.161.0", sels[0].Details["gitVersion"])
	creates := e.auditOf("rollout.create")
	var uiCreate *domain.AuditEvent
	for i := range creates {
		if creates[i].TargetID == v["rolloutId"] {
			uiCreate = &creates[i]
		}
	}
	require.NotNil(t, uiCreate)
	assert.Equal(t, domain.ActorUser, uiCreate.ActorType, "the operator created it")
	assert.Equal(t, "5f1c2d3e-0000-4000-8000-000000000001", uiCreate.ActorID)
	assert.Equal(t, "ui", uiCreate.Details["origin"])
	assert.Equal(t, "weekly maintenance window", uiCreate.Details["reason"])

	// selecting the target again is a no-op
	again := asMap(t, e.expect(e.selectVersion("edge-nl", map[string]any{"version": "0.162.0"}), "selectFleetVersion", http.StatusOK))
	assert.Equal(t, false, again["planned"])
	assert.Contains(t, again["message"], "already")
	sels = e.auditOf("fleet.version.select")
	require.Len(t, sels, 2, "the request is part of the trail, flagged as no-op")
	var noop int
	for _, a := range sels {
		if a.Details["noop"] == true {
			noop++
			assert.Equal(t, "success", a.Result)
			assert.Equal(t, "0.162.0", a.Details["version"])
		}
	}
	assert.Equal(t, 1, noop)
}

func TestSelectVersionReplacesTheSelectionWithCompareAndSet(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.159.0, <=0.162.0", AllowDowngrade: true}), func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	e.startLeader()
	first := asMap(t, e.expect(e.selectVersion("edge-nl", map[string]any{"version": "0.162.0"}), "selectFleetVersion", http.StatusOK))
	// someone else sees the old target: the expectation of the UI is stale
	stale := e.selectVersion("edge-nl", map[string]any{"version": "0.160.0", "expectedTargetVersion": "0.161.0"})
	body := asMap(t, e.expect(stale, "selectFleetVersion", http.StatusConflict))
	assert.Equal(t, "conflict", body["code"])
	// with the right expectation the selection is replaced (a downgrade is allowed by this frame)
	second := asMap(t, e.expect(e.selectVersion("edge-nl", map[string]any{"version": "0.160.0", "expectedTargetVersion": "0.162.0"}), "selectFleetVersion", http.StatusOK))
	assert.Equal(t, "0.160.0", second["target"].(map[string]any)["version"])
	assert.NotEqual(t, first["rolloutId"], second["rolloutId"], "the first rollout was superseded by a new one")
	row, err := e.st.FleetVersions().Get(context.Background(), "edge-nl")
	require.NoError(t, err)
	assert.Equal(t, "0.160.0", row.Version)
	old := asMap(t, e.expect(e.get("/rollouts/"+first["rolloutId"].(string)), "getRollout", http.StatusOK))
	assert.Equal(t, "superseded", old["status"])
	assert.Contains(t, old["statusReason"], "selected in the UI (0.160.0)")
	// the refused attempt was audited with its reason
	var failed []domain.AuditEvent
	for _, ev := range e.auditOf("fleet.version.select") {
		if ev.Result == "failure" {
			failed = append(failed, ev)
		}
	}
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Details["error"], "reload and decide again")
}

func TestSelectVersionErrors(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.159.0, <=0.162.0", RequireReason: true}), func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	post := func(fleet string, body any) resp { return e.selectVersion(fleet, body) }
	problem := func(r resp, status int) map[string]any {
		return asMap(t, e.expect(r, "selectFleetVersion", status))
	}
	issueCodes := func(p map[string]any) []string {
		var out []string
		for _, is := range p["issues"].([]any) {
			out = append(out, is.(map[string]any)["code"].(string))
		}
		return out
	}

	// 400: syntax
	for name, body := range map[string]any{
		"empty":         map[string]any{},
		"v prefix":      map[string]any{"version": "v0.162.0"},
		"short":         map[string]any{"version": "0.162"},
		"unknown field": map[string]any{"version": "0.162.0", "nope": 1},
		"long reason":   map[string]any{"version": "0.162.0", "reason": string(make([]byte, 501))},
		"not json":      "{",
	} {
		assert.Equal(t, "bad_request", problem(post("edge-nl", body), http.StatusBadRequest)["code"], name)
	}
	assert.Empty(t, e.auditOf("fleet.version.select"), "syntax errors are no operator actions")
	// 404
	problem(post("nope", map[string]any{"version": "0.162.0", "reason": "x"}), http.StatusNotFound)

	// 422: every rule, with its issue code
	cases := []struct {
		name string
		body map[string]any
		want []string
	}{
		{"reason required", map[string]any{"version": "0.162.0"}, []string{"reason_required"}},
		{"outside the frame", map[string]any{"version": "0.163.0", "reason": "x"}, []string{"version_not_allowed", "unknown_version"}},
		{"downgrade", map[string]any{"version": "0.160.0", "reason": "x"}, []string{"downgrade_not_allowed"}},
		{"prerelease", map[string]any{"version": "0.162.0-rc.1", "reason": "x"}, []string{"version_not_allowed", "prerelease_not_allowed", "unknown_version"}},
		{"unknown release inside the range", map[string]any{"version": "0.161.7", "reason": "x"}, []string{"unknown_version"}},
	}
	for _, c := range cases {
		p := problem(post("edge-nl", c.body), http.StatusUnprocessableEntity)
		assert.Equal(t, "validation_failed", p["code"], c.name)
		assert.ElementsMatch(t, c.want, issueCodes(p), c.name)
	}
	assert.Equal(t, "version", problem(post("edge-nl", map[string]any{"version": "0.160.0", "reason": "x"}), http.StatusUnprocessableEntity)["issues"].([]any)[0].(map[string]any)["path"])
	assert.Nil(t, mustNotFind(e, "edge-nl"), "nothing was stored")

	// 412: no frame in Git / no executor / blocking rollout
	problem(post("office", map[string]any{"version": "0.161.0", "reason": "x"}), http.StatusPreconditionFailed)
	e.editFleet("edge-be", func(f *domain.Fleet) {
		f.Update = domain.UpdateSpec{Selection: &domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.160.0"}}
	})
	assert.Contains(t, problem(post("edge-be", map[string]any{"version": "0.162.0"}), http.StatusPreconditionFailed)["detail"], "no update executor")
	ro := &domain.Rollout{ID: uuid.New(), Kind: domain.RolloutKindVersion, FleetName: "edge-nl", PolicyName: "standard", Status: domain.RolloutRollingBack, ToRef: "0.162.0",
		CreatedBy: "gitops", Policy: domain.DefaultRolloutPolicy(), Waves: []domain.RolloutWave{{Index: 0, Name: "all"}}, CreatedAt: e.clock}
	require.NoError(t, e.st.Rollouts().Create(context.Background(), ro, nil))
	assert.Contains(t, problem(post("edge-nl", map[string]any{"version": "0.162.0", "reason": "x"}), http.StatusPreconditionFailed)["detail"], "abort it first")

	// every refusal was audited as a failure with its reason
	var failures int
	for _, ev := range e.auditOf("fleet.version.select") {
		assert.Equal(t, "failure", ev.Result)
		assert.NotEmpty(t, ev.Details["error"])
		failures++
	}
	assert.GreaterOrEqual(t, failures, 8)
}

func mustNotFind(e *apiEnv, fleet string) *domain.FleetVersionSelection {
	sel, err := e.st.FleetVersions().Get(context.Background(), fleet)
	if err == nil {
		return sel
	}
	return nil
}

func TestSelectVersionPermissions(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.161.0", AllowDowngrade: true}))
	body := map[string]any{"version": "0.161.0", "reason": "x"}
	assert.Equal(t, http.StatusForbidden, e.do(http.MethodPost, "/fleets/edge-nl/actions/select-version", "viewer", body).Code)
	assert.Equal(t, http.StatusForbidden, e.do(http.MethodPost, "/fleets/edge-nl/actions/clear-version-selection", "viewer", nil).Code)
	assert.Equal(t, http.StatusUnauthorized, e.do(http.MethodPost, "/fleets/edge-nl/actions/select-version", "", body).Code)
	assert.Equal(t, "fleets:set_version", string(e.routes["selectFleetVersion"].Permission))
	assert.Equal(t, "fleets:set_version", string(e.routes["clearFleetVersionSelection"].Permission))
	assert.Equal(t, "read", string(e.routes["getFleetVersionOptions"].Permission))
	// operators and admins may
	assert.Equal(t, http.StatusOK, e.do(http.MethodPost, "/fleets/edge-nl/actions/select-version", "admin", body).Code)
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/fleets/edge-nl/version-options", "viewer", nil).Code)
}

// Without a leader the selection is stored and announced; the answer says that
// the plan follows.
func TestSelectVersionWithoutLeaderIsNotPlannedYet(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.161.0, <=0.162.0"}), func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	events, cancel := e.bus.Subscribe(eventbus.FleetVersionChanged)
	defer cancel()
	start := time.Now()
	v := asMap(t, e.expect(e.selectVersion("edge-nl", map[string]any{"version": "0.162.0"}), "selectFleetVersion", http.StatusOK))
	assert.Equal(t, false, v["planned"])
	assert.NotContains(t, v, "rolloutId")
	assert.Contains(t, v["message"], "follows")
	assert.GreaterOrEqual(t, time.Since(start), e.svc.waitMax, "the handler waited for the leader")
	select {
	case ev := <-events:
		var pl eventbus.FleetVersionChangedPayload
		require.NoError(t, ev.Decode(&pl))
		assert.Equal(t, "edge-nl", pl.FleetName)
	default:
		t.Fatal("fleet.version_changed was not published")
	}
	row := mustNotFind(e, "edge-nl")
	require.NotNil(t, row)
	assert.Equal(t, "0.162.0", row.Version)
	assert.Nil(t, row.PlannedAt)
	assert.Equal(t, "ui", asMap(t, e.expect(e.get("/fleets/edge-nl"), "getFleet", http.StatusOK))["status"].(map[string]any)["versionTarget"].(map[string]any)["source"], "the next read shows the selection at once")
}

// A leader that plans a selection without an update to do answers planned
// without a rollout.
func TestSelectVersionWhenNoCollectorNeedsAnUpdate(t *testing.T) {
	e := newAPIEnv(t)
	e.addAgent("edge-be-2001", func(a *domain.Agent) { a.Version = "0.162.0" }) // already there
	e.editFleet("edge-be", frame(domain.VersionSelectionSpec{VersionRange: ">=0.161.0, <=0.162.0"}))
	e.st.UpdateAgent(e.agents["edge-be-2001"], func(a *domain.Agent) { a.Version = "0.162.0" })
	e.startLeader()
	v := asMap(t, e.expect(e.selectVersion("edge-be", map[string]any{"version": "0.162.0"}), "selectFleetVersion", http.StatusOK))
	assert.Equal(t, true, v["planned"])
	assert.NotContains(t, v, "rolloutId")
	assert.Equal(t, "no collector of the fleet needs an update", v["message"])
}

func TestClearVersionSelection(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.159.0, <=0.162.0", AllowDowngrade: true}), func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	clear := func(fleet string, body any) resp {
		return e.do(http.MethodPost, "/fleets/"+fleet+"/actions/clear-version-selection", "operator", body, "X-Test-User-ID", "5f1c2d3e-0000-4000-8000-000000000001")
	}
	// nothing selected: 200, unchanged, audited as no-op
	v := asMap(t, e.expect(clear("edge-nl", nil), "clearFleetVersionSelection", http.StatusOK))
	assert.Equal(t, "git", v["source"])
	noop := e.auditOf("fleet.version.clear")
	require.Len(t, noop, 1)
	assert.Equal(t, true, noop[0].Details["noop"])
	assert.Equal(t, "success", noop[0].Result)
	e.expect(clear("nope", nil), "clearFleetVersionSelection", http.StatusNotFound)
	e.st.ResetAudit()

	e.startLeader()
	e.expect(e.selectVersion("edge-nl", map[string]any{"version": "0.162.0", "reason": "try"}), "selectFleetVersion", http.StatusOK)
	got := asMap(t, e.expect(clear("edge-nl", map[string]any{"reason": "back to Git"}), "clearFleetVersionSelection", http.StatusOK))
	assert.Equal(t, "git", got["source"])
	assert.Equal(t, "0.161.0", got["version"])
	assert.NotContains(t, got, "selection")
	assert.Nil(t, mustNotFind(e, "edge-nl"))
	cl := e.auditOf("fleet.version.clear")
	require.Len(t, cl, 1)
	assert.Equal(t, "success", cl[0].Result)
	assert.Equal(t, "0.162.0", cl[0].Details["version"])
	assert.Equal(t, "back to Git", cl[0].Details["reason"])
	assert.Equal(t, "0.161.0", cl[0].Details["gitVersion"])

	// the frame may demand a reason; clearing works although the frame is off
	e.expect(e.selectVersion("edge-nl", map[string]any{"version": "0.162.0"}), "selectFleetVersion", http.StatusOK)
	e.editFleet("edge-nl", func(f *domain.Fleet) { f.Update.Selection.RequireReason = true })
	p := asMap(t, e.expect(clear("edge-nl", nil), "clearFleetVersionSelection", http.StatusUnprocessableEntity))
	assert.Equal(t, "reason_required", p["issues"].([]any)[0].(map[string]any)["code"])
	assert.NotNil(t, mustNotFind(e, "edge-nl"))
	e.editFleet("edge-nl", func(f *domain.Fleet) { f.Update.Selection = nil })
	e.expect(clear("edge-nl", nil), "clearFleetVersionSelection", http.StatusOK)
	assert.Nil(t, mustNotFind(e, "edge-nl"))
}

func TestInvalidatedSelectionShowsTheFleetOnHold(t *testing.T) {
	e := edgeNL(t, frame(domain.VersionSelectionSpec{VersionRange: ">=0.161.0, <=0.162.0", AllowDowngrade: true}), func(f *domain.Fleet) { f.Collector.Version = "0.161.0" })
	_, err := e.st.FleetVersions().Put(context.Background(), domain.FleetVersionSelection{
		Fleet: "edge-nl", Version: "0.162.0", State: domain.SelectionInvalidated, StateReason: "version 0.162.0 is outside the frame of commit abc (<0.162.0)",
		GitVersion: "0.161.0", SelectedBy: "user:x", SelectedAt: e.clock,
	}, nil)
	require.NoError(t, err)
	e.svc.invalidateStats()
	fl := asMap(t, e.expect(e.get("/fleets/edge-nl"), "getFleet", http.StatusOK))
	vt := fl["status"].(map[string]any)["versionTarget"].(map[string]any)
	assert.Equal(t, "hold", vt["source"])
	assert.NotContains(t, vt, "version")
	assert.Equal(t, "invalidated", vt["selection"].(map[string]any)["state"])
	assert.Contains(t, vt["selection"].(map[string]any)["stateReason"], "outside the frame")
	assert.NotContains(t, fl["status"].(map[string]any)["agents"].(map[string]any), "targetVersion")
	assert.Equal(t, false, fl["status"].(map[string]any)["behindLatest"])
	// the dialog still works: selecting an allowed version ends the hold
	v := asMap(t, e.expect(e.get("/fleets/edge-nl/version-options"), "getFleetVersionOptions", http.StatusOK))
	assert.Equal(t, true, v["allowed"])
	assert.Equal(t, "hold", v["target"].(map[string]any)["source"])
	e.startLeader()
	ok := asMap(t, e.expect(e.selectVersion("edge-nl", map[string]any{"version": "0.161.0", "expectedTargetVersion": ""}), "selectFleetVersion", http.StatusOK))
	assert.Equal(t, "ui", ok["target"].(map[string]any)["source"])
	assert.Equal(t, "active", ok["target"].(map[string]any)["selection"].(map[string]any)["state"])
}

func TestReleaseDetailListsUpdatableFleetsAndCountsTheEffectiveTarget(t *testing.T) {
	e := newAPIEnv(t)
	e.addAgent("edge-be-2001") // 0.160.0, edge-be targets 0.161.0 in Git
	e.addAgent("edge-nl-1001")
	e.editFleet("edge-be", frame(domain.VersionSelectionSpec{VersionRange: ">=0.161.0, <=0.162.0"}))
	e.editFleet("edge-nl", frame(domain.VersionSelectionSpec{Versions: []string{"0.161.0"}}))

	v := asMap(t, e.expect(e.get("/releases/otelcol-contrib/0.162.0"), "getRelease", http.StatusOK))
	ups := v["updatableFleets"].([]any)
	require.Len(t, ups, 5, "every fleet of the distribution")
	byFleet := func(name string) map[string]any { return byKey(t, ups, "fleet", name) }
	assert.Equal(t, "edge-be", ups[0].(map[string]any)["fleet"], "selectable fleets first")
	be := byFleet("edge-be")
	assert.Equal(t, true, be["selectable"])
	assert.Equal(t, "git", be["target"].(map[string]any)["source"])
	assert.EqualValues(t, 1, be["agentsToUpdate"])
	nl := byFleet("edge-nl")
	assert.Equal(t, false, nl["selectable"])
	assert.Equal(t, "current_target", nl["reasonCode"], "edge-nl already targets 0.162.0")
	assert.Equal(t, "selection_disabled", byFleet("office")["reasonCode"])
	assert.Equal(t, "selection_disabled", byFleet("datacenter")["reasonCode"])
	e.expect(e.get("/releases/opampsupervisor/0.162.0"), "getRelease", http.StatusOK)

	// an older release: inside no frame
	old := asMap(t, e.expect(e.get("/releases/otelcol-contrib/0.160.0"), "getRelease", http.StatusOK))
	be = byKey(t, old["updatableFleets"].([]any), "fleet", "edge-be")
	assert.Equal(t, "outside_frame", be["reasonCode"])

	// edge-be is behind 0.162.0 with its Git target ...
	assert.EqualValues(t, 1, asMap(t, e.expect(e.get("/releases"), "listReleases", http.StatusOK))["items"].([]any)[0].(map[string]any)["fleetsBehind"])
	assert.Len(t, v["fleetsBehind"], 1)
	// ... and not any more once an operator selected 0.162.0 for it
	e.startLeader()
	e.expect(e.selectVersion("edge-be", map[string]any{"version": "0.162.0"}), "selectFleetVersion", http.StatusOK)
	e.svc.invalidateStats()
	v = asMap(t, e.expect(e.get("/releases/otelcol-contrib/0.162.0"), "getRelease", http.StatusOK))
	assert.Empty(t, v["fleetsBehind"], "the effective target counts")
	be = byKey(t, v["updatableFleets"].([]any), "fleet", "edge-be")
	assert.Equal(t, "current_target", be["reasonCode"])
	assert.Equal(t, "ui", be["target"].(map[string]any)["source"])
	rel := asMap(t, e.expect(e.get("/releases"), "listReleases", http.StatusOK))
	assert.EqualValues(t, 0, rel["items"].([]any)[0].(map[string]any)["fleetsBehind"])
}

func TestTargetProgressIsNormalizedForTargetsEndedFromOutside(t *testing.T) {
	e := newAPIEnv(t)
	now := e.clock
	ro := &domain.Rollout{ID: uuid.New(), Kind: domain.RolloutKindVersion, FleetName: "edge-nl", PolicyName: "standard", Status: domain.RolloutAborted, ToRef: "0.162.0",
		CreatedBy: "gitops", Policy: domain.DefaultRolloutPolicy(), Waves: []domain.RolloutWave{{Index: 0, Name: "all"}}, CreatedAt: now}
	mk := func(status domain.TargetStatus, withProgress bool) domain.RolloutTarget {
		t := domain.RolloutTarget{AgentID: domain.AgentID(uuid.New()), Hostname: "h-" + string(status), Status: status, ToRef: "0.162.0", FromRef: "0.161.0", Attempts: 1, LastError: "stopped"}
		if withProgress {
			t.Progress = &domain.TargetProgress{Phase: domain.PhaseInstalling, Source: domain.ProgressUpdater, UpdatedAt: now, Steps: []domain.UpdateStep{
				{Name: domain.StepOffered, Status: domain.StepDone},
				{Name: domain.StepInstalling, Status: domain.StepRunning},
				{Name: domain.StepHealthWait, Status: domain.StepPending},
			}}
		}
		return t
	}
	targets := []domain.RolloutTarget{mk(domain.TargetCancelled, true), mk(domain.TargetFailed, true), mk(domain.TargetPending, false)}
	require.NoError(t, e.st.Rollouts().Create(context.Background(), ro, targets))

	page := asMap(t, e.expect(e.get("/rollouts/"+ro.ID.String()+"/targets"), "listRolloutTargets", http.StatusOK))
	items := page["items"].([]any)
	byHost := func(h string) map[string]any { return byKey(t, items, "hostname", h) }
	stepStatus := func(tg map[string]any) []string {
		var out []string
		for _, s := range tg["progress"].(map[string]any)["steps"].([]any) {
			out = append(out, s.(map[string]any)["status"].(string))
		}
		return out
	}
	c := byHost("h-cancelled")
	assert.Equal(t, "cancelled", c["progress"].(map[string]any)["phase"])
	assert.Equal(t, []string{"done", "skipped", "skipped"}, stepStatus(c))
	f := byHost("h-failed")
	assert.Equal(t, "failed", f["progress"].(map[string]any)["phase"])
	assert.Equal(t, []string{"done", "failed", "skipped"}, stepStatus(f))
	assert.Equal(t, "stopped", f["progress"].(map[string]any)["steps"].([]any)[1].(map[string]any)["detail"])
	assert.NotContains(t, byHost("h-pending"), "progress")
	// the stored value is not rewritten by reading
	stored, _, _ := e.st.Rollouts().ListTargets(context.Background(), ro.ID, store.TargetFilter{Status: []domain.TargetStatus{domain.TargetCancelled}})
	assert.Equal(t, domain.PhaseInstalling, stored[0].Progress.Phase)
}
