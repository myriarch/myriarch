// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

func byKey(t *testing.T, items []any, key, val string) map[string]any {
	t.Helper()
	for _, it := range items {
		m := asMap(t, it)
		if m[key] == val {
			return m
		}
	}
	t.Fatalf("no item with %s=%s", key, val)
	return nil
}

func fleetOf(m map[string]any) map[string]any { return m["fleet"].(map[string]any) }

func TestListFleetsWithLiveStatus(t *testing.T) {
	e := newAPIEnv(t)
	e.addAgent("edge-nl-1001")
	e.addAgent("edge-nl-1003", func(a *domain.Agent) {
		a.Connection.Status = domain.ConnectionOffline
		a.Health = &domain.AgentHealth{Healthy: false}
	})
	edgeBE := e.addAgent("edge-be-2001") // runs 0.161.0 = target of edge-be
	e.addAgent("office-app-01")
	// a render error and a pending (drifting) agent
	e.st.UpdateAgent(e.agents["office-app-01"], func(a *domain.Agent) { a.Assignment.Error = "missing_parameter: x" })
	e.st.UpdateAgent(edgeBE, func(a *domain.Agent) { a.Version = "0.161.0" })
	e.st.UpdateAgent(e.agents["edge-nl-1001"], func(a *domain.Agent) { a.Assignment.AssignedConfigHash = domain.SHA256([]byte("other")) }) // drift
	// an unassigned collector and an updater are not counted anywhere
	e.st.PutAgent(domain.Agent{ID: uuid.Must(uuid.NewV7()), Kind: domain.AgentKindCollector, Connection: domain.AgentConnection{Status: domain.ConnectionOnline}})
	e.st.PutAgent(domain.Agent{ID: uuid.Must(uuid.NewV7()), Kind: domain.AgentKindUpdater, Assignment: domain.AgentAssignment{FleetName: "edge-nl"}})
	e.svc = nil

	v := e.expect(e.get("/fleets"), "listFleets", http.StatusOK)
	items := asList(t, v)
	require.Len(t, items, 5)
	var names []string
	for _, it := range items {
		names = append(names, fleetOf(asMap(t, it))["name"].(string))
	}
	assert.Equal(t, []string{"datacenter", "edge-be", "edge-nl", "k8s-gateway", "office"}, names, "sorted by name")

	var edgeNL, office, edgeBEFleet, datacenter map[string]any
	for _, it := range items {
		m := asMap(t, it)
		switch fleetOf(m)["name"] {
		case "edge-nl":
			edgeNL = m
		case "office":
			office = m
		case "edge-be":
			edgeBEFleet = m
		case "datacenter":
			datacenter = m
		}
	}
	st := edgeNL["status"].(map[string]any)
	counts := st["agents"].(map[string]any)
	assert.EqualValues(t, 2, counts["total"])
	assert.EqualValues(t, 1, counts["online"])
	assert.EqualValues(t, 1, counts["healthy"])
	assert.EqualValues(t, 1, counts["inSync"], "the second agent stays in sync")
	assert.EqualValues(t, 1, counts["pending"], "drift counts as pending")
	assert.EqualValues(t, 0, counts["failed"])
	assert.Equal(t, "0.162.0", counts["targetVersion"])
	assert.EqualValues(t, 1, counts["onTarget"], "edge-nl-1003 runs the target 0.162.0, edge-nl-1001 runs 0.161.0")
	assert.Equal(t, "edge-nl", counts["fleet"])
	assert.Equal(t, true, st["behindLatest"] == false, "target 0.162.0 is the latest release in the fixtures")
	assert.Equal(t, "0.162.0", st["latestVersion"])
	assert.NotNil(t, st["versions"])
	assert.Equal(t, []any{}, st["activeRollouts"])
	assert.EqualValues(t, 0, st["renderErrors"])

	assert.EqualValues(t, 1, office["status"].(map[string]any)["renderErrors"])
	edgeBECounts := edgeBEFleet["status"].(map[string]any)["agents"].(map[string]any)
	assert.EqualValues(t, 1, edgeBECounts["onTarget"])
	assert.Equal(t, true, edgeBEFleet["status"].(map[string]any)["behindLatest"], "edge-be targets 0.161.0 < 0.162.0")
	datacenterCounts := datacenter["status"].(map[string]any)["agents"].(map[string]any)
	assert.EqualValues(t, 0, datacenterCounts["total"], "fleets without members are listed with zero counts")

	// Fleet objects have non-null collections
	f := fleetOf(edgeNL)
	assert.Equal(t, "fleets/edge-nl.yaml", f["source"].(map[string]any)["path"])
	assert.NotNil(t, f["parameters"])
}

func TestGetFleetDetail(t *testing.T) {
	e := newAPIEnv(t)
	e.addAgent("edge-nl-1001")
	v := asMap(t, e.expect(e.get("/fleets/edge-nl"), "getFleet", http.StatusOK))
	assert.Equal(t, []any{"base", "edge-windows"}, v["blueprintChain"])
	cp := v["configPolicy"].(map[string]any)
	assert.Equal(t, "edge-careful", cp["name"])
	assert.Equal(t, "10m0s", cp["healthWait"])
	assert.Equal(t, "Europe/Amsterdam", cp["defaultTimezone"], "defaults applied")
	assert.Equal(t, "edge-careful", v["versionPolicy"].(map[string]any)["name"])
	assert.Equal(t, "https://git.example.com/otel/fleet-config/-/blob/abc123def456/fleets/edge-nl.yaml", v["webUrl"], "uiRepoWebUrl with {commit} and {path}")
	assert.Equal(t, []any{}, v["issues"], "the example repository is valid")
	assert.Nil(t, v["executor"], "updater needs no executor object")
	waves := cp["waves"].([]any)
	require.Len(t, waves, 4)

	// executor reference: AWX with redacted target
	office := asMap(t, e.expect(e.get("/fleets/office"), "getFleet", http.StatusOK))
	ex := office["executor"].(map[string]any)
	assert.Equal(t, "awx-linux", ex["name"])
	assert.Equal(t, "awx", ex["type"])
	assert.Equal(t, "https://awx.example.internal/api/v2/job_templates/42", ex["target"])
	assert.Equal(t, []any{"office"}, ex["usedBy"])

	// a fleet without policy references gets the built-in default, resolved
	e.deps.Store.GitOps()
	k := asMap(t, e.expect(e.get("/fleets/k8s-gateway"), "getFleet", http.StatusOK))
	assert.Equal(t, "gateway", k["configPolicy"].(map[string]any)["name"])

	e.expect(e.get("/fleets/nope"), "getFleet", http.StatusNotFound)
}

func TestFleetWithoutPolicyUsesBuiltInDefault(t *testing.T) {
	e := newAPIEnv(t)
	snap, _ := e.assigner.Snapshot(t.Context())
	snap.Fleets[0].Rollout = domain.FleetRolloutSpec{}
	name := snap.Fleets[0].Name
	v := asMap(t, e.expect(e.get("/fleets/"+name), "getFleet", http.StatusOK))
	cp := v["configPolicy"].(map[string]any)
	assert.Equal(t, "default", cp["name"])
	assert.EqualValues(t, 50, cp["maxConcurrent"])
	assert.Equal(t, "10m0s", cp["applyTimeout"], "config default apply timeout")
	assert.Equal(t, "30m0s", v["versionPolicy"].(map[string]any)["applyTimeout"], "version default apply timeout")
}

func TestFleetIssuesAreReportedPerObject(t *testing.T) {
	e := newAPIEnv(t)
	snap, _ := e.assigner.Snapshot(t.Context())
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == "datacenter" {
			snap.Fleets[i].Rollout.ConfigPolicy = "ghost"
			snap.Fleets[i].Source.Path = "fleets/datacenter.yaml"
		}
	}
	snap.Commit = "c2" // a new commit invalidates the issues cache
	v := asMap(t, e.expect(e.get("/fleets/datacenter"), "getFleet", http.StatusOK))
	issues := v["issues"].([]any)
	require.Len(t, issues, 1)
	is := asMap(t, issues[0])
	assert.Equal(t, "policy_not_found", is["code"])
	assert.Equal(t, "Fleet/datacenter", is["object"])
	// other fleets unaffected, blueprint list shows issue counts
	office := asMap(t, e.expect(e.get("/fleets/office"), "getFleet", http.StatusOK))
	assert.Equal(t, []any{}, office["issues"])
}

func TestMatchFleets(t *testing.T) {
	e := newAPIEnv(t)
	id := e.addAgent("edge-nl-1001")

	v := asMap(t, e.expect(e.post("/fleets/match", map[string]any{"attributes": map[string]string{
		"site.type": "edge", "site.country": "BE", "os.type": "windows", "site.id": "2001",
	}}), "matchFleets", http.StatusOK))
	assert.Equal(t, "edge-be", v["fleet"])
	assert.Equal(t, []any{"edge-be"}, v["candidates"])

	// existing agent: its AttributeView (with labels from inventory/tokens) is used
	v = asMap(t, e.expect(e.post("/fleets/match", map[string]any{"agentId": id.String()}), "matchFleets", http.StatusOK))
	assert.Equal(t, "edge-nl", v["fleet"])
	assert.Equal(t, map[string]any{"pilot": "true"}, v["inventoryLabels"])

	// nothing matches
	v = asMap(t, e.expect(e.post("/fleets/match", map[string]any{"attributes": map[string]string{"site.type": "warehouse"}}), "matchFleets", http.StatusOK))
	assert.Nil(t, v["fleet"])
	assert.Equal(t, []any{}, v["candidates"], "never null")

	// errors
	e.expect(e.post("/fleets/match", map[string]any{}), "matchFleets", http.StatusBadRequest)
	e.expect(e.post("/fleets/match", map[string]any{"agentId": uuid.NewString()}), "matchFleets", http.StatusNotFound)
	e.expect(e.post("/fleets/match", "{not json"), "matchFleets", http.StatusBadRequest)
	e.expect(e.post("/fleets/match", map[string]any{"attributes": map[string]string{}, "unknown": 1}), "matchFleets", http.StatusBadRequest)
}

func TestBlueprints(t *testing.T) {
	e := newAPIEnv(t)
	v := e.expect(e.get("/blueprints"), "listBlueprints", http.StatusOK)
	items := asList(t, v)
	require.Len(t, items, 5)
	base := byKey(t, items, "name", "base")
	assert.EqualValues(t, 10, base["parameterCount"])
	assert.EqualValues(t, 1, base["fragmentCount"])
	assert.Equal(t, []any{"datacenter", "edge-be", "edge-nl", "k8s-gateway", "office"}, base["usedBy"], "used by all fleets through extends")
	assert.EqualValues(t, 0, base["issueCount"])
	assert.Equal(t, "blueprints/base.yaml", base["source"].(map[string]any)["path"])
	edgeWin := byKey(t, items, "name", "edge-windows")
	assert.Equal(t, "base", edgeWin["extends"])
	assert.Equal(t, []any{"edge-be", "edge-nl"}, edgeWin["usedBy"])

	d := asMap(t, e.expect(e.get("/blueprints/edge-windows"), "getBlueprint", http.StatusOK))
	assert.Equal(t, []any{"base", "edge-windows"}, d["chain"])
	var pnames []string
	for _, p := range d["parameters"].([]any) {
		pnames = append(pnames, p.(map[string]any)["name"].(string))
	}
	assert.Contains(t, pnames, "gatewayEndpoint", "inherited from base")
	assert.Contains(t, pnames, "appLogPaths", "own")
	assert.Equal(t, "gatewayEndpoint", pnames[0], "order of first declaration, root first")
	assert.Equal(t, 1, strings.Count(strings.Join(pnames, ","), "storageDir"), "redeclared parameter appears once")
	assert.Equal(t, []any{"edge-be", "edge-nl"}, d["usedBy"])
	assert.Equal(t, []any{}, d["issues"])
	assert.Equal(t, "https://git.example.com/otel/fleet-config/-/blob/abc123def456/blueprints/edge-windows.yaml", d["webUrl"])
	bp := d["blueprint"].(map[string]any)
	assert.NotNil(t, bp["config"])
	assert.NotNil(t, bp["fragments"])

	e.expect(e.get("/blueprints/nope"), "getBlueprint", http.StatusNotFound)
}

func TestRenderBlueprintPreview(t *testing.T) {
	e := newAPIEnv(t)
	id := e.addAgent("edge-nl-1001")

	// hypothetical agent + fleet parameters
	v := asMap(t, e.expect(e.post("/blueprints/edge-windows/render", map[string]any{
		"fleet": "edge-nl", "attributes": map[string]string{"site.id": "1001", "site.type": "edge", "site.country": "NL", "os.type": "windows"},
	}), "renderBlueprint", http.StatusOK))
	assert.Equal(t, "edge-windows", v["blueprint"])
	assert.Equal(t, "edge-nl", v["fleet"])
	assert.Equal(t, "ec29aa439d6c7e7f5b16fe972e6e60dfc92cbc5ac849c19309253f52585ec6c5", v["hash"], "the golden hash of edge-nl-1001")
	assert.Equal(t, []any{"base", "base#gateway-auth", "edge-windows"}, v["layers"])
	assert.Contains(t, v["body"], "otlp_grpc/gateway")
	params := v["parameters"].(map[string]any)
	assert.EqualValues(t, 75, params["memoryLimitPercent"])
	assert.Equal(t, "1001", params["siteId"])
	assert.Equal(t, "attribute", v["parameterSources"].(map[string]any)["siteId"])
	assert.Equal(t, []any{}, v["issues"])

	// existing agent: fleet and labels come from the agent, overrides win (preview only)
	v = asMap(t, e.expect(e.post("/blueprints/edge-windows/render", map[string]any{
		"agentId": id.String(), "parameters": map[string]any{"logLevel": "debug", "memoryLimitPercent": 80},
	}), "renderBlueprint", http.StatusOK))
	assert.Equal(t, "edge-nl", v["fleet"], "default: the agent's fleet")
	assert.Equal(t, "override", v["parameterSources"].(map[string]any)["logLevel"])
	assert.EqualValues(t, 80, v["parameters"].(map[string]any)["memoryLimitPercent"], "JSON numbers become ints")
	assert.Contains(t, v["body"], "limit_percentage: 80")
	assert.Contains(t, v["body"], "level: debug")

	// rendering problems are issues with HTTP 200, the body stays empty
	v = asMap(t, e.expect(e.post("/blueprints/edge-windows/render", map[string]any{
		"attributes": map[string]string{"site.id": "1"}, "parameters": map[string]any{"nope": 1},
	}), "renderBlueprint", http.StatusOK))
	assert.Equal(t, "", v["body"])
	assert.Nil(t, v["hash"])
	issues := v["issues"].([]any)
	codes := []string{}
	for _, i := range issues {
		codes = append(codes, i.(map[string]any)["code"].(string))
	}
	assert.Contains(t, codes, "unknown_parameter")
	assert.Contains(t, codes, "missing_parameter", "gatewayEndpoint is required and the preview has no fleet")

	// unknown things
	e.expect(e.post("/blueprints/nope/render", map[string]any{}), "renderBlueprint", http.StatusNotFound)
	e.expect(e.post("/blueprints/edge-windows/render", map[string]any{"fleet": "nope"}), "renderBlueprint", http.StatusNotFound)
	e.expect(e.post("/blueprints/edge-windows/render", map[string]any{"agentId": uuid.NewString()}), "renderBlueprint", http.StatusNotFound)
	e.expect(e.post("/blueprints/edge-windows/render", "[1]"), "renderBlueprint", http.StatusBadRequest)
}

func TestRenderBlueprintChecksComponentsOfTheAgent(t *testing.T) {
	e := newAPIEnv(t)
	hash := domain.SHA256([]byte("set"))
	require.NoError(t, e.st.Configs().PutComponentSet(t.Context(), domain.ComponentSet{Hash: hash, Components: map[string]map[string]domain.ComponentDetails{
		"receivers": {"otlp": {}}, "processors": {"batch": {}}, "exporters": {"otlp_grpc": {}}, "extensions": {"health_check": {}},
	}}))
	id := e.addAgent("edge-nl-1001", func(a *domain.Agent) { a.AvailableComponentsHash = hash })
	v := asMap(t, e.expect(e.post("/blueprints/edge-windows/render", map[string]any{"agentId": id.String()}), "renderBlueprint", http.StatusOK))
	var codes []string
	for _, i := range v["issues"].([]any) {
		codes = append(codes, i.(map[string]any)["code"].(string))
	}
	assert.Contains(t, codes, "unknown_component")
	// validate=false skips the component check
	v = asMap(t, e.expect(e.post("/blueprints/edge-windows/render", map[string]any{"agentId": id.String(), "validate": false}), "renderBlueprint", http.StatusOK))
	assert.Equal(t, []any{}, v["issues"])
	assert.NotEmpty(t, v["body"])
}

func TestRolloutPoliciesAndExecutors(t *testing.T) {
	e := newAPIEnv(t)
	v := e.expect(e.get("/rollout-policies"), "listRolloutPolicies", http.StatusOK)
	items := asList(t, v)
	var names []string
	for _, it := range items {
		names = append(names, asMap(t, it)["name"].(string))
	}
	assert.Equal(t, []string{"default", "edge-careful", "gateway", "standard"}, names, "the built-in default is listed when no object overrides it")
	def := byKey(t, items, "name", "default")
	assert.EqualValues(t, 50, def["maxConcurrent"])
	assert.NotNil(t, def["waves"])
	assert.NotContains(t, def, "maintenanceWindows", "empty optional lists are omitted, never null")

	p := asMap(t, e.expect(e.get("/rollout-policies/edge-careful"), "getRolloutPolicy", http.StatusOK))
	assert.Equal(t, "policies/edge-careful.yaml", p["source"].(map[string]any)["path"])
	assert.Len(t, p["waves"], 4)
	assert.Len(t, p["maintenanceWindows"], 2)
	e.expect(e.get("/rollout-policies/default"), "getRolloutPolicy", http.StatusOK)
	e.expect(e.get("/rollout-policies/nope"), "getRolloutPolicy", http.StatusNotFound)

	x := e.expect(e.get("/executors"), "listExecutors", http.StatusOK)
	exs := asList(t, x)
	require.Len(t, exs, 2)
	hook := byKey(t, exs, "name", "k8s-image-bump")
	assert.Equal(t, "webhook", hook["type"])
	assert.Equal(t, "https://ci-hooks.example.internal/myriarch/k8s-image-bump", hook["target"])
	assert.Equal(t, []any{"k8s-gateway"}, hook["usedBy"])
	raw := e.get("/executors").Body.String()
	assert.NotContains(t, raw, "MYRIARCH_AWX_TOKEN", "secret references are not exposed")
	assert.NotContains(t, raw, "MYRIARCH_WEBHOOK_SECRET_K8S")
	assert.NotContains(t, raw, "secretEnv")
}
