// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

// rolloutEnv: a running config rollout for fleet edge-nl (2 targets).
func rolloutEnv(t *testing.T) (*apiEnv, uuid.UUID) {
	e := newAPIEnv(t)
	for _, s := range []string{"edge-nl-1001", "edge-nl-1003"} {
		id := e.addAgent(s)
		e.st.UpdateAgent(id, func(a *domain.Agent) { a.Assignment.AssignedConfigHash = domain.SHA256([]byte("new-" + s)) })
	}
	require.NoError(t, e.engine.Plan(context.Background()))
	list, _, err := e.st.Rollouts().List(context.Background(), storeFilter())
	require.NoError(t, err)
	require.Len(t, list, 2, "one version and one config rollout")
	for _, r := range list {
		if r.Kind == domain.RolloutKindConfig {
			return e, r.ID
		}
	}
	t.Fatal("no config rollout planned")
	return nil, uuid.Nil
}

func TestListRollouts(t *testing.T) {
	e, id := rolloutEnv(t)
	v := asMap(t, e.expect(e.get("/rollouts"), "listRollouts", http.StatusOK))
	assert.EqualValues(t, 2, v["total"])
	assert.EqualValues(t, 50, v["limit"])
	assert.EqualValues(t, 0, v["offset"])
	items := v["items"].([]any)
	require.Len(t, items, 2)
	r := byKey(t, items, "id", id.String())
	assert.Equal(t, id.String(), r["id"])
	assert.Equal(t, "config", r["kind"])
	assert.Equal(t, "edge-nl", r["fleetName"])
	assert.Equal(t, "edge-careful", r["policyName"])
	assert.Equal(t, "running", r["status"])
	assert.Equal(t, "abc123def456", r["toRef"])
	assert.EqualValues(t, 4, r["waveCount"])
	assert.EqualValues(t, 2, r["counts"].(map[string]any)["total"])
	assert.NotContains(t, r, "policy", "summaries carry no policy snapshot")

	// filters
	for _, q := range []string{"?kind=config", "?kind=version"} {
		assert.EqualValues(t, 1, asMap(t, e.expect(e.get("/rollouts"+q), "listRollouts", http.StatusOK))["total"], q)
	}
	for _, q := range []string{"?fleet=edge-nl", "?status=running", "?status=running&status=paused", "?status=running,paused", "?active=true"} {
		assert.EqualValues(t, 2, asMap(t, e.expect(e.get("/rollouts"+q), "listRollouts", http.StatusOK))["total"], q)
	}
	for _, q := range []string{"?fleet=office", "?status=completed", "?active=false&status=failed"} {
		assert.EqualValues(t, 0, asMap(t, e.expect(e.get("/rollouts"+q), "listRollouts", http.StatusOK))["total"], q)
	}
	// pagination
	v = asMap(t, e.expect(e.get("/rollouts?limit=1&offset=2"), "listRollouts", http.StatusOK))
	assert.Equal(t, []any{}, v["items"], "never null")
	assert.EqualValues(t, 2, v["total"])
	assert.EqualValues(t, 1, v["limit"])
	assert.EqualValues(t, 2, v["offset"])
	// invalid
	for _, q := range []string{"?kind=bogus", "?status=exploded", "?active=maybe", "?limit=0", "?limit=501", "?offset=-1"} {
		e.expect(e.get("/rollouts"+q), "listRollouts", http.StatusBadRequest)
	}
}

func TestGetRolloutAndTargets(t *testing.T) {
	e, id := rolloutEnv(t)
	v := asMap(t, e.expect(e.get("/rollouts/"+id.String()), "getRollout", http.StatusOK))
	assert.Equal(t, "running", v["status"])
	assert.Len(t, v["waves"], 4)
	assert.Equal(t, "pilot", v["waves"].([]any)[0].(map[string]any)["name"])
	assert.Equal(t, "10m0s", v["policy"].(map[string]any)["healthWait"], "policy snapshot")
	assert.NotNil(t, v["counts"])
	assert.Equal(t, "gitops", v["createdBy"])
	e.expect(e.get("/rollouts/"+uuid.NewString()), "getRollout", http.StatusNotFound)
	e.expect(e.get("/rollouts/not-a-uuid"), "getRollout", http.StatusBadRequest)

	tg := asMap(t, e.expect(e.get("/rollouts/"+id.String()+"/targets"), "listRolloutTargets", http.StatusOK))
	assert.EqualValues(t, 2, tg["total"])
	items := tg["items"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	assert.Equal(t, id.String(), first["rolloutId"])
	assert.NotEmpty(t, first["hostname"])
	assert.Equal(t, "pending", first["status"])
	assert.NotEmpty(t, first["toRef"])

	for q, total := range map[string]int{"?wave=0": 1, "?wave=3": 1, "?wave=1": 0, "?status=pending": 2, "?status=failed": 0, "?status=pending&wave=0": 1, "?status=pending,failed": 2, "?limit=1": 2} {
		assert.EqualValues(t, total, asMap(t, e.expect(e.get("/rollouts/"+id.String()+"/targets"+q), "listRolloutTargets", http.StatusOK))["total"], q)
	}
	one := asMap(t, e.expect(e.get("/rollouts/"+id.String()+"/targets?limit=1&offset=1"), "listRolloutTargets", http.StatusOK))
	assert.Len(t, one["items"], 1)
	e.expect(e.get("/rollouts/"+uuid.NewString()+"/targets"), "listRolloutTargets", http.StatusNotFound)
	for _, q := range []string{"?wave=x", "?wave=-1", "?status=nope"} {
		e.expect(e.get("/rollouts/"+id.String()+"/targets"+q), "listRolloutTargets", http.StatusBadRequest)
	}
}

func TestRolloutTargetsSort(t *testing.T) {
	e, id := rolloutEnv(t)
	base := "/rollouts/" + id.String() + "/targets"
	hosts := func(q string) []string {
		v := asMap(t, e.expect(e.get(base+q), "listRolloutTargets", http.StatusOK))
		var out []string
		for _, it := range v["items"].([]any) {
			out = append(out, it.(map[string]any)["hostname"].(string)+"/"+it.(map[string]any)["status"].(string))
		}
		return out
	}
	byWave := hosts("")
	require.Len(t, byWave, 2)
	// make the target that sorts last by wave the one that is running now
	list, _, err := e.st.Rollouts().ListTargets(context.Background(), id, store.TargetFilter{})
	require.NoError(t, err)
	last := list[len(list)-1]
	last.Status = domain.TargetVerifying
	conflicts, err := e.st.Rollouts().UpdateTargets(context.Background(), []domain.RolloutTarget{last})
	require.NoError(t, err)
	require.Empty(t, conflicts)

	wantWave := []string{list[0].Hostname + "/pending", last.Hostname + "/verifying"}
	assert.Equal(t, wantWave, hosts(""), "default order is wave, hostname, agent id")
	assert.Equal(t, wantWave, hosts("?sort=wave"))
	assert.Equal(t, wantWave, hosts("?sort="), "empty means the default")
	assert.Equal(t, []string{last.Hostname + "/verifying", list[0].Hostname + "/pending"}, hosts("?sort=activity"))
	assert.Equal(t, []string{list[0].Hostname + "/pending"}, hosts("?sort=activity&limit=1&offset=1"))
	assert.Equal(t, []string{last.Hostname + "/verifying"}, hosts("?sort=activity&status=verifying,pending&limit=1"))
	assert.Equal(t, []string{list[0].Hostname + "/pending"}, hosts("?sort=activity&wave=0"))
	v := asMap(t, e.expect(e.get(base+"?sort=activity&limit=1&offset=5"), "listRolloutTargets", http.StatusOK))
	assert.EqualValues(t, 2, v["total"])

	// the agent filter finds one target wherever it sits in the order
	assert.Equal(t, []string{last.Hostname + "/verifying"}, hosts("?sort=activity&agent="+last.AgentID.String()+"&limit=1"))
	assert.Equal(t, []string{list[0].Hostname + "/pending"}, hosts("?agent="+list[0].AgentID.String()))
	assert.Empty(t, hosts("?agent="+uuid.NewString()))
	e.expect(e.get(base+"?agent=nope"), "listRolloutTargets", http.StatusBadRequest)

	for _, q := range []string{"?sort=nope", "?sort=-activity", "?sort=Activity"} {
		e.expect(e.get(base+q), "listRolloutTargets", http.StatusBadRequest)
	}
	e.expect(e.get("/rollouts/"+uuid.NewString()+"/targets?sort=nope"), "listRolloutTargets", http.StatusBadRequest)
}

func TestRolloutActionsOverREST(t *testing.T) {
	e, id := rolloutEnv(t)
	base := "/rollouts/" + id.String() + "/actions/"

	v := asMap(t, e.expect(e.post(base+"pause", map[string]string{"reason": "change freeze"}), "pauseRollout", http.StatusOK))
	assert.Equal(t, "paused", v["status"])
	assert.Contains(t, v["statusReason"], "paused by user:u-operator")
	assert.Contains(t, v["statusReason"], "change freeze")

	p := asMap(t, e.expect(e.post(base+"pause", nil), "pauseRollout", http.StatusPreconditionFailed))
	assert.Equal(t, "precondition_failed", p["code"])
	assert.Contains(t, p["instance"], "/actions/pause")

	v = asMap(t, e.expect(e.post(base+"resume", nil), "resumeRollout", http.StatusOK))
	assert.Equal(t, "running", v["status"])

	// approve: validation and precondition errors
	e.expect(e.post(base+"approve", nil), "approveRolloutWave", http.StatusBadRequest)                               // body required
	e.expect(e.post(base+"approve", map[string]any{"wave": 0}), "approveRolloutWave", http.StatusPreconditionFailed) // pilot does not need approval
	e.expect(e.post(base+"approve", map[string]any{"wave": 99}), "approveRolloutWave", http.StatusPreconditionFailed)
	e.expect(e.post(base+"approve", map[string]any{"wave": 1, "bogus": true}), "approveRolloutWave", http.StatusBadRequest)

	// retry-failed, rollback on a running rollout
	e.expect(e.post(base+"retry-failed", nil), "retryFailedRolloutTargets", http.StatusOK)
	e.expect(e.post(base+"pause", `{"reason": 5}`), "pauseRollout", http.StatusBadRequest)

	v = asMap(t, e.expect(e.post(base+"abort", map[string]string{"reason": "wrong commit"}), "abortRollout", http.StatusOK))
	assert.Equal(t, "aborted", v["status"])
	assert.EqualValues(t, 2, v["counts"].(map[string]any)["cancelled"])
	e.expect(e.post(base+"rollback", nil), "rollbackRollout", http.StatusPreconditionFailed)
	e.expect(e.post(base+"resume", nil), "resumeRollout", http.StatusPreconditionFailed)

	e.expect(e.post("/rollouts/"+uuid.NewString()+"/actions/pause", nil), "pauseRollout", http.StatusNotFound)
	e.expect(e.post("/rollouts/xyz/actions/pause", nil), "pauseRollout", http.StatusBadRequest)

	// every successful action was audited with the acting user; rejected and
	// failed actions are audited as failures with request id and source address
	var actions []string
	failures := 0
	for _, ev := range e.st.AuditEvents() {
		if strings.HasPrefix(ev.Action, "rollout.") && ev.Result == "failure" {
			failures++
			assert.Equal(t, "u-operator", ev.ActorID)
			continue
		}
		if strings.HasPrefix(ev.Action, "rollout.") && ev.Action != "rollout.create" {
			actions = append(actions, ev.Action)
			assert.Equal(t, domain.ActorUser, ev.ActorType)
			assert.Equal(t, "u-operator", ev.ActorID)
		}
	}
	assert.Equal(t, []string{"rollout.pause", "rollout.resume", "rollout.retry_failed", "rollout.abort"}, actions[:4])
	assert.GreaterOrEqual(t, failures, 3, "rejected actions (precondition failed, not found) are audited as failures")
}

func TestRolloutRollbackOverREST(t *testing.T) {
	e, id := rolloutEnv(t)
	v := asMap(t, e.expect(e.post("/rollouts/"+id.String()+"/actions/rollback", map[string]string{"reason": "bad"}), "rollbackRollout", http.StatusOK))
	assert.Equal(t, "rolling_back", v["status"])
}

// ------------------------------------------------------------------ releases ----

func TestReleasesList(t *testing.T) {
	e := newAPIEnv(t)
	e.addAgent("edge-nl-1001") // edge-nl targets 0.162.0 = latest; edge-be targets 0.161.0, datacenter/office/k8s 0.162.0
	v := asMap(t, e.expect(e.get("/releases"), "listReleases", http.StatusOK))
	assert.EqualValues(t, 5, v["total"])
	items := v["items"].([]any)
	require.Len(t, items, 5)
	first := items[0].(map[string]any)
	assert.Equal(t, "0.162.0", first["version"], "highest first")
	assert.Equal(t, "otelcol-contrib", first["distribution"])
	assert.Equal(t, "v0.162.0", first["tag"])
	assert.Equal(t, true, first["isLatest"])
	assert.Equal(t, false, first["acknowledged"])
	assert.Greater(t, first["breakingCount"], float64(5))
	assert.EqualValues(t, 1, first["fleetsBehind"], "edge-be targets 0.161.0 < 0.162.0")
	second := items[1].(map[string]any)
	assert.Equal(t, false, second["isLatest"])
	assert.EqualValues(t, 0, second["fleetsBehind"], "edge-be (0.161.0) is not below 0.161.0")
	last := items[4].(map[string]any)
	assert.Equal(t, "0.158.0", last["version"])
	assert.EqualValues(t, 0, last["fleetsBehind"])

	sup := asMap(t, e.expect(e.get("/releases?distribution=opampsupervisor"), "listReleases", http.StatusOK))
	assert.EqualValues(t, 3, sup["total"])
	assert.EqualValues(t, 1, asMap(t, e.expect(e.get("/releases?limit=1&offset=4"), "listReleases", http.StatusOK))["limit"])
	e.expect(e.get("/releases?distribution=otelcol"), "listReleases", http.StatusBadRequest)
	e.expect(e.get("/releases?includePrerelease=x"), "listReleases", http.StatusBadRequest)
}

func TestReleaseDetailFleetsBehindAndSnippet(t *testing.T) {
	e := newAPIEnv(t)
	e.addAgent("edge-be-2001")  // 0.160.0, fleet edge-be (target 0.161.0)
	e.addAgent("edge-nl-1001")  // 0.161.0
	e.addAgent("office-app-01") // 0.162.0
	v := asMap(t, e.expect(e.get("/releases/otelcol-contrib/0.162.0"), "getRelease", http.StatusOK))
	assert.Equal(t, true, v["isLatest"])
	rel := v["release"].(map[string]any)
	assert.Equal(t, "0.162.0", rel["version"])
	assert.NotEmpty(t, rel["breakingChanges"])
	assert.NotNil(t, rel["assets"])
	assert.EqualValues(t, 2, v["agentsBehind"], "collectors below 0.162.0: 0.160.0 and 0.161.0")

	behind := v["fleetsBehind"].([]any)
	require.Len(t, behind, 1, "only edge-be is behind")
	fb := behind[0].(map[string]any)
	assert.Equal(t, "edge-be", fb["fleet"])
	assert.Equal(t, "0.161.0", fb["targetVersion"])
	assert.Equal(t, "fleets/edge-be.yaml", fb["sourcePath"])
	assert.EqualValues(t, 1, fb["agents"])
	assert.Greater(t, fb["breakingCount"], float64(0), "breaking changes between 0.161.0 and 0.162.0 (Compare)")
	assert.NotNil(t, fb["versions"])
	assert.Equal(t, "# fleets/edge-be.yaml\nspec:\n  collector:\n    version: 0.162.0\n", v["gitopsSnippet"])

	// older release: nothing is behind it
	old := asMap(t, e.expect(e.get("/releases/otelcol-contrib/0.158.0"), "getRelease", http.StatusOK))
	assert.Equal(t, false, old["isLatest"])
	assert.Equal(t, []any{}, old["fleetsBehind"])
	assert.Equal(t, "", old["gitopsSnippet"])

	// supervisor: no fleet linkage
	sup := asMap(t, e.expect(e.get("/releases/opampsupervisor/0.162.0"), "getRelease", http.StatusOK))
	assert.Equal(t, []any{}, sup["fleetsBehind"])
	assert.EqualValues(t, 0, sup["agentsBehind"])

	latest := asMap(t, e.expect(e.get("/releases/latest"), "getLatestRelease", http.StatusOK))
	assert.Equal(t, "0.162.0", latest["release"].(map[string]any)["version"])
	e.expect(e.get("/releases/latest?distribution=opampsupervisor"), "getLatestRelease", http.StatusOK)
	e.expect(e.get("/releases/otelcol-contrib/9.9.9"), "getRelease", http.StatusNotFound)
	e.expect(e.get("/releases/otelcol-contrib/v0.162.0"), "getRelease", http.StatusOK) // v prefix tolerated
}

func TestCompareVersionsOverREST(t *testing.T) {
	e := newAPIEnv(t)
	v := asMap(t, e.expect(e.get("/releases/compare?from=0.159.0&to=0.162.0"), "compareVersions", http.StatusOK))
	assert.Equal(t, []any{"0.160.0", "0.161.0", "0.162.0"}, v["versions"])
	assert.Equal(t, "otelcol-contrib", v["distribution"])
	assert.NotEmpty(t, v["breakingChanges"])
	assert.NotNil(t, v["deprecations"])
	e.expect(e.get("/releases/compare?from=0.159.0"), "compareVersions", http.StatusBadRequest)
	e.expect(e.get("/releases/compare?from=x&to=0.162.0"), "compareVersions", http.StatusBadRequest)
	e.expect(e.get("/releases/compare?from=0.159.0&to=0.162.0&distribution=nope"), "compareVersions", http.StatusBadRequest)
	e.expect(e.get("/releases/compare?from=0.159.0&to=0.162.0&distribution=opampsupervisor"), "compareVersions", http.StatusOK)
}

func TestRefreshAndAcknowledge(t *testing.T) {
	e := newAPIEnv(t)
	events, cancel := e.bus.Subscribe(eventbus.ReleaseRefreshRequested)
	defer cancel()
	v := asMap(t, e.expect(e.post("/releases/actions/refresh", nil), "refreshReleases", http.StatusAccepted))
	assert.Equal(t, true, v["accepted"])
	assert.Len(t, events, 1, "the leader is asked through the event bus")

	rel := asMap(t, e.expect(e.post("/releases/otelcol-contrib/0.162.0/actions/acknowledge", nil), "acknowledgeRelease", http.StatusOK))
	assert.Equal(t, "user:u-operator", rel["acknowledgedBy"])
	assert.NotEmpty(t, rel["acknowledgedAt"])
	sum := asMap(t, e.expect(e.get("/releases"), "listReleases", http.StatusOK))["items"].([]any)[0].(map[string]any)
	assert.Equal(t, true, sum["acknowledged"])
	e.expect(e.post("/releases/otelcol-contrib/9.9.9/actions/acknowledge", nil), "acknowledgeRelease", http.StatusNotFound)

	var actions []string
	for _, ev := range e.st.AuditEvents() {
		actions = append(actions, ev.Action)
	}
	assert.Contains(t, actions, "release.refresh")
	assert.Contains(t, actions, "release.acknowledge")
}

// -------------------------------------------------------------------- gitops ----

func TestGitOpsEndpoints(t *testing.T) {
	e := newAPIEnv(t)
	// initial state: nothing synced
	st := asMap(t, e.expect(e.get("/gitops/status"), "getGitOpsStatus", http.StatusOK))
	assert.Equal(t, "never", st["state"])
	assert.Equal(t, []any{}, st["issues"])
	assert.NotNil(t, st["objectCounts"])

	// sync once so that status, history and objects are populated
	require.NoError(t, e.syncer.SyncNow(context.Background(), "startup"))
	st = asMap(t, e.expect(e.get("/gitops/status"), "getGitOpsStatus", http.StatusOK))
	assert.Equal(t, "ok", st["state"])
	assert.NotNil(t, st["appliedCommit"])
	assert.EqualValues(t, 5, st["objectCounts"].(map[string]any)["Fleet"])

	syncs := asMap(t, e.expect(e.get("/gitops/syncs"), "listGitOpsSyncs", http.StatusOK))
	assert.GreaterOrEqual(t, syncs["total"], float64(1))
	first := syncs["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "applied", first["result"])
	assert.NotNil(t, first["issues"])
	e.expect(e.get("/gitops/syncs?limit=1&offset=0"), "listGitOpsSyncs", http.StatusOK)

	objs := asList(t, e.expect(e.get("/gitops/objects"), "listGitOpsObjects", http.StatusOK))
	assert.Len(t, objs, 16)
	kinds := []string{}
	for _, o := range objs {
		kinds = append(kinds, asMap(t, o)["kind"].(string))
	}
	assert.Equal(t, "Blueprint", kinds[0], "dependency order: blueprints first, fleets last")
	assert.Equal(t, "Fleet", kinds[len(kinds)-1])
	fleet := byKey(t, objs, "name", "edge-nl")
	assert.Equal(t, "Fleet", fleet["kind"])
	assert.Equal(t, "fleets/edge-nl.yaml", fleet["path"])
	assert.Equal(t, "https://git.example.com/otel/fleet-config/-/blob/abc123def456/fleets/edge-nl.yaml", fleet["webUrl"])

	// manual sync: published on the bus (the leader performs it) and audited
	events, cancel := e.bus.Subscribe(eventbus.GitOpsSyncRequested)
	defer cancel()
	v := asMap(t, e.expect(e.post("/gitops/actions/sync", nil), "triggerGitOpsSync", http.StatusAccepted))
	assert.Equal(t, true, v["accepted"])
	require.Len(t, events, 1)
	var pl eventbus.GitOpsSyncRequestedPayload
	require.NoError(t, (<-events).Decode(&pl))
	assert.Equal(t, "manual", pl.Trigger)
	assert.Equal(t, "user:u-operator", pl.RequestedBy)
	var actions []string
	for _, ev := range e.st.AuditEvents() {
		actions = append(actions, ev.Action)
	}
	assert.Contains(t, actions, "gitops.sync")
}

func TestGitOpsWebhookRouteIsMounted(t *testing.T) {
	e := newAPIEnv(t)
	// no principal needed: authenticated by the shared secret
	r := e.do(http.MethodPost, "/gitops/webhook", "", `{"ref":"refs/heads/main"}`, "X-Gitlab-Token", "s3cret")
	e.expect(r, "gitopsWebhook", http.StatusAccepted)
	r = e.do(http.MethodPost, "/gitops/webhook", "", `{}`, "X-Gitlab-Token", "wrong")
	e.expect(r, "gitopsWebhook", http.StatusUnauthorized)
}

// ------------------------------------------------------------------ artifacts ----

func TestArtifactsEndpoints(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()
	a, err := e.cache.Ensure(ctx, "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)

	v := asMap(t, e.expect(e.get("/artifacts"), "listArtifacts", http.StatusOK))
	assert.EqualValues(t, 1, v["total"])
	item := v["items"].([]any)[0].(map[string]any)
	assert.Equal(t, a.ID.String(), item["id"])
	assert.Equal(t, "ready", item["status"])
	assert.Equal(t, "otelcol-contrib_0.162.0_linux_amd64.tar.gz", item["filename"])
	assert.NotContains(t, item, "sigstoreBundle", "the raw bundle is not part of the artifact JSON")
	assert.EqualValues(t, 1, asMap(t, e.expect(e.get("/artifacts?distribution=otelcol-contrib&version=0.162.0&status=ready"), "listArtifacts", http.StatusOK))["total"])
	assert.EqualValues(t, 0, asMap(t, e.expect(e.get("/artifacts?status=failed"), "listArtifacts", http.StatusOK))["total"])
	assert.EqualValues(t, 0, asMap(t, e.expect(e.get("/artifacts?version=0.100.0"), "listArtifacts", http.StatusOK))["total"])
	e.expect(e.get("/artifacts?status=nope"), "listArtifacts", http.StatusBadRequest)
	e.expect(e.get("/artifacts?distribution=nope"), "listArtifacts", http.StatusBadRequest)

	g := asMap(t, e.expect(e.get("/artifacts/"+a.ID.String()), "getArtifact", http.StatusOK))
	assert.Equal(t, a.ID.String(), g["id"])
	e.expect(e.get("/artifacts/"+uuid.NewString()), "getArtifact", http.StatusNotFound)
	e.expect(e.get("/artifacts/xyz"), "getArtifact", http.StatusBadRequest)

	// prefetch: states are returned immediately (accepted)
	p := asList(t, e.expect(e.post("/artifacts/actions/prefetch", map[string]any{
		"distribution": "otelcol-contrib", "version": "0.162.0", "platforms": []string{"linux/amd64", "windows/amd64"},
	}), "prefetchArtifacts", http.StatusAccepted))
	require.Len(t, p, 2)
	for _, it := range p {
		assert.Contains(t, []any{"ready", "downloading"}, asMap(t, it)["status"])
	}
	// invalid requests are 422 with details
	r := e.post("/artifacts/actions/prefetch", map[string]any{"distribution": "otelcol-contrib", "version": "latest", "platforms": []string{"linux/amd64"}})
	pr := asMap(t, e.expect(r, "prefetchArtifacts", http.StatusUnprocessableEntity))
	assert.Equal(t, "validation_failed", pr["code"])
	assert.NotEmpty(t, pr["issues"])
	e.expect(e.post("/artifacts/actions/prefetch", map[string]any{"distribution": "otelcol-contrib", "version": "0.162.0", "platforms": []string{"linux"}}), "prefetchArtifacts", http.StatusUnprocessableEntity)
	// no platforms, none configured and no connected collector of the distribution: 422
	nr := asMap(t, e.expect(e.post("/artifacts/actions/prefetch", map[string]any{"distribution": "opampsupervisor", "version": "0.162.0"}), "prefetchArtifacts", http.StatusUnprocessableEntity))
	assert.Contains(t, fmt.Sprint(nr["issues"]), "no platforms given")
	e.expect(e.post("/artifacts/actions/prefetch", "{"), "prefetchArtifacts", http.StatusBadRequest)

	// delete
	e.waitNoDownloads(t)
	e.expect(e.do(http.MethodDelete, "/artifacts/"+a.ID.String(), "operator", nil), "deleteArtifact", http.StatusNoContent)
	e.expect(e.do(http.MethodDelete, "/artifacts/"+a.ID.String(), "operator", nil), "deleteArtifact", http.StatusNotFound)
	var actions []string
	for _, ev := range e.st.AuditEvents() {
		actions = append(actions, ev.Action)
	}
	assert.Contains(t, actions, "artifact.prefetch")
	assert.Contains(t, actions, "artifact.delete")
	// the audit entry records the platforms that were resolved and where they came from
	for _, ev := range e.st.AuditEvents() {
		if ev.Action == "artifact.prefetch" && ev.Result != "failure" {
			assert.Equal(t, "request", ev.Details["platformsSource"])
			assert.ElementsMatch(t, []string{"linux/amd64", "windows/amd64"}, ev.Details["platforms"])
		}
	}
}

func TestArtifactDownloadRouteUsesDownloadTokens(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()
	a, err := e.cache.Ensure(ctx, "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	agent := uuid.Must(uuid.NewV7())
	u, hdr, err := e.cache.DownloadURL(a, agent)
	require.NoError(t, err)
	require.NoError(t, e.st.Packages().SetOffers(ctx, agent, []domain.PackageOffer{{AgentID: agent, DownloadURL: u}}))
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	path := strings.TrimPrefix(parsed.Path, "/api/v1")

	// no principal (agents do not log in), only the download token
	r := e.do(http.MethodGet, path, "", nil, "Authorization", hdr["Authorization"])
	assert.Equal(t, http.StatusOK, r.Code)
	assert.Contains(t, r.Body.String(), "artifact-bytes-")
	assert.Equal(t, "application/gzip", r.Header().Get("Content-Type"))
	r = e.do(http.MethodGet, path, "", nil)
	e.expect(r, "downloadArtifact", http.StatusUnauthorized)
	r = e.do(http.MethodGet, path, "viewer", nil)
	assert.Equal(t, http.StatusUnauthorized, r.Code, "a user session is no download token")
}

// -------------------------------------------------------------- notifications ----

func TestNotificationChannels(t *testing.T) {
	e := newAPIEnv(t)
	items := asList(t, e.expect(e.get("/notifications/channels"), "listNotificationChannels", http.StatusOK))
	require.Len(t, items, 2)
	teams := byKey(t, items, "name", "teams-ops")
	assert.Equal(t, "teams", teams["type"])
	assert.Equal(t, []any{"rollout.*"}, teams["events"])
	assert.Contains(t, teams["target"], "/…")
	raw := e.get("/notifications/channels").Body.String()
	assert.NotContains(t, raw, "SECRETPATH")
	assert.NotContains(t, raw, "SECRETSIG")

	v := asMap(t, e.expect(e.post("/notifications/channels/teams-ops/actions/test", nil), "testNotificationChannel", http.StatusAccepted))
	assert.Equal(t, true, v["accepted"])
	e.hook.mu.Lock()
	hits := e.hook.hits
	e.hook.fail = true
	e.hook.mu.Unlock()
	assert.GreaterOrEqual(t, hits, 1)

	e.expect(e.post("/notifications/channels/nope/actions/test", nil), "testNotificationChannel", http.StatusNotFound)
	// a failing destination is reported as unavailable (and never leaks the URL)
	r := e.post("/notifications/channels/generic/actions/test", nil)
	pr := asMap(t, e.expect(r, "testNotificationChannel", http.StatusServiceUnavailable))
	assert.Equal(t, "unavailable", pr["code"])
	assert.NotContains(t, r.Body.String(), "SECRET")
	var actions []string
	for _, ev := range e.st.AuditEvents() {
		actions = append(actions, ev.Action)
	}
	assert.Equal(t, []string{"notification.test", "notification.test", "notification.test"}, actions)
	assert.Equal(t, "failure", e.st.AuditEvents()[2].Result)
}

// approvedBy / acknowledgedBy are stored as stable references ("user:<uuid>")
// but shown as the display name of the user.
func TestApproverAndAcknowledgerAreShownByName(t *testing.T) {
	e, id := rolloutEnv(t)
	ctx := context.Background()
	u, err := e.st.Identity().UpsertUser(ctx, domain.User{Subject: "local:erika", Provider: "local", Name: "Erika Muster", Email: "erika@example.com", Role: domain.RoleOperator})
	require.NoError(t, err)
	hdr := []string{"X-Test-User-ID", u.ID.String()}

	// the engine stores "user:<uuid>" when an operator approves a wave
	ref := "user:" + u.ID.String()
	before, err := e.st.Rollouts().Get(ctx, id)
	require.NoError(t, err)
	wi := -1
	for i, w := range before.Waves {
		if w.Status == domain.WavePending || w.Status == domain.WaveWaitingApproval {
			wi = i
			break
		}
	}
	require.GreaterOrEqual(t, wi, 0, "a wave that can still be approved")
	ok, err := e.st.Rollouts().SetWaveApproval(ctx, id, wi, ref, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	got := asMap(t, e.expect(e.get("/rollouts/"+id.String()), "getRollout", http.StatusOK))
	assert.Equal(t, "Erika Muster", got["waves"].([]any)[wi].(map[string]any)["approvedBy"])
	stored, err := e.st.Rollouts().Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, ref, stored.Waves[wi].ApprovedBy, "the stored reference stays the stable identity")

	rel := asMap(t, e.expect(e.do(http.MethodPost, "/releases/otelcol-contrib/0.162.0/actions/acknowledge", "operator", nil, hdr...), "acknowledgeRelease", http.StatusOK))
	assert.Equal(t, "Erika Muster", rel["acknowledgedBy"])
	detail := asMap(t, e.expect(e.get("/releases/otelcol-contrib/0.162.0"), "getRelease", http.StatusOK))
	assert.Equal(t, "Erika Muster", detail["release"].(map[string]any)["acknowledgedBy"])

	// without a name the email is shown; unknown references stay readable
	mail, err := e.st.Identity().UpsertUser(ctx, domain.User{Subject: "local:max", Provider: "local", Email: "max@example.com", Role: domain.RoleViewer})
	require.NoError(t, err)
	tokenID := uuid.New()
	require.NoError(t, e.st.Identity().CreateAPIToken(ctx, domain.APIToken{ID: tokenID, Name: "ci-deploy", Role: domain.RoleOperator, CreatedAt: time.Now()}))
	names := (&service{d: e.deps}).actors(ctx)
	assert.Equal(t, "max@example.com", names.Name("user:"+mail.ID.String()))
	assert.Equal(t, "token ci-deploy", names.Name("token:"+tokenID.String()))
	assert.Equal(t, "system", names.Name("system"))
	assert.Equal(t, "", names.Name(""))
	assert.Equal(t, "user:u-operator", names.Name("user:u-operator"), "not a uuid: shown as stored")
	missing := "user:" + uuid.NewString()
	assert.Equal(t, missing, names.Name(missing), "deleted user: shown as stored")
}
