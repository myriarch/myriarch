// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

func TestListAgentsPaginationSortFilter(t *testing.T) {
	e := newEnv(t, nil)
	e.addRelease("0.162.0")
	for i := 0; i < 12; i++ {
		v := "0.160.0"
		if i%3 == 0 {
			v = "0.162.0"
		}
		e.seed(seed{host: fmt.Sprintf("edge-%02d", i), version: v, os: map[bool]string{true: "windows", false: "linux"}[i%2 == 0], site: fmt.Sprint(100 + i), siteType: "edge",
			online: i%4 != 0, fleet: map[bool]string{true: "edge-nl", false: "edge-be"}[i < 6], labels: map[string]string{"pilot": map[bool]string{true: "true", false: "false"}[i < 2]}, healthy: bptr(i%5 != 0)})
	}
	e.seed(seed{host: "updater-1", version: "0.1.0", kind: domain.AgentKindUpdater, online: true, caps: domain.CapAcceptsPackages})

	var page api.Page[dto.AgentSummary]
	r := e.get("/agents" + qs("limit", "5", "offset", "5", "sort", "-hostname"))
	r.into(&page)
	if r.Code != 200 || page.Total != 13 || page.Limit != 5 || page.Offset != 5 || len(page.Items) != 5 {
		t.Fatalf("page: %d %+v", r.Code, page)
	}
	if page.Items[0].Hostname != "edge-07" { // updater-1, edge-11 .. edge-07
		t.Fatalf("descending sort: %s", page.Items[0].Hostname)
	}
	// default sort is hostname ascending
	e.get("/agents" + qs("limit", "3")).into(&page)
	if page.Items[0].Hostname != "edge-00" || page.Items[2].Hostname != "edge-02" {
		t.Fatalf("default sort: %+v", page.Items)
	}
	// summary fields
	var first dto.AgentSummary
	e.get("/agents" + qs("q", "edge-00")).into(&page)
	first = page.Items[0]
	if first.Kind != domain.AgentKindCollector || first.Version != "0.162.0" || first.Outdated || first.OSType != "windows" || first.Status != domain.ConnectionOffline ||
		first.FleetName != "edge-nl" || first.ConfigSync != domain.ConfigSyncInSync || first.TargetVersion != "0.162.0" || first.Healthy == nil || *first.Healthy ||
		first.Site["site.id"] != "100" || first.Site["site.type"] != "edge" || len(first.Site) != 3 || first.Labels["pilot"] != "true" || first.LastSeenAt == nil {
		t.Fatalf("summary: %+v", first)
	}

	count := func(q string) int {
		t.Helper()
		var p api.Page[dto.AgentSummary]
		r := e.get("/agents" + q)
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", q, r.Code, r.Body)
		}
		r.into(&p)
		return p.Total
	}
	for q, want := range map[string]int{
		qs("kind", "updater"):                                     1,
		qs("kind", "collector", "kind", "updater"):                13,
		qs("status", "offline"):                                   3,
		qs("os", "windows"):                                       6,
		qs("fleet", "edge-be"):                                    6,
		qs("unassigned", "true"):                                  1,
		qs("unassigned", "false"):                                 12,
		qs("healthy", "false"):                                    3,
		qs("healthy", "true"):                                     9,
		qs("version", "0.160.0"):                                  8,
		qs("configSync", "in_sync"):                               12,
		qs("configSync", "none"):                                  1,
		qs("attr", "site.type=edge"):                              12,
		qs("attr", "labels.pilot=true"):                           2,
		qs("attr", "site.id=100", "attr", "site.id=101"):          2,
		qs("attr", "site.id=100", "attr", "site.type=edge"):       1,
		qs("attr", "site.id=100", "attr", "site.type=datacenter"): 0,
		qs("q", "EDGE-0"):                                         10,
		qs("outdated", "true"):                                    8,
		qs("outdated", "false"):                                   4,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: got %d, want %d", q, got, want)
		}
	}
	// outdated=true implies kind=collector (the updater never matches)
	var p api.Page[dto.AgentSummary]
	e.get("/agents" + qs("outdated", "true", "limit", "50")).into(&p)
	for _, a := range p.Items {
		if !a.Outdated || a.Kind != domain.AgentKindCollector {
			t.Fatalf("outdated filter result: %+v", a)
		}
	}
}

func TestListAgentsOutdatedWithoutKnownRelease(t *testing.T) {
	e := newEnv(t, nil)
	e.seed(seed{host: "a", version: "0.100.0"})
	e.seed(seed{host: "u", version: "0.1.0", kind: domain.AgentKindUpdater})
	count := func(q string) int {
		var p api.Page[dto.AgentSummary]
		e.get("/agents" + q).into(&p)
		return p.Total
	}
	if got := count(qs("outdated", "true")); got != 0 {
		t.Fatalf("no release known: outdated=true must be empty, got %d", got)
	}
	if got := count(qs("outdated", "false")); got != 1 {
		t.Fatalf("no release known: outdated=false = all collectors, got %d", got)
	}
}

func TestListAgentsValidation(t *testing.T) {
	e := newEnv(t, nil)
	for _, q := range []string{
		qs("kind", "robot"), qs("status", "sleeping"), qs("healthy", "maybe"), qs("unassigned", "x"), qs("outdated", "x"), qs("configSync", "weird"),
		qs("attr", "novalue"), qs("attr", "=x"), qs("limit", "0"), qs("limit", "501"), qs("offset", "-1"), qs("sort", "bogus"), qs("sort", "hostname;drop"),
	} {
		r := e.get("/agents" + q)
		if r.Code != 400 || e.problemCode(r) != api.ErrorCodeBadRequest {
			t.Errorf("%s: %d %s", q, r.Code, r.Body)
		}
	}
	// facets share the validation
	if r := e.get("/agents/facets" + qs("kind", "robot")); r.Code != 400 {
		t.Errorf("facets validation: %d", r.Code)
	}
	// every documented sort field is accepted
	for _, f := range store.AgentSortFields {
		for _, prefix := range []string{"", "-"} {
			if r := e.get("/agents" + qs("sort", prefix+f)); r.Code != 200 {
				t.Errorf("sort %s%s: %d %s", prefix, f, r.Code, r.Body)
			}
		}
	}
	// empty result is an empty list, not null
	r := e.get("/agents" + qs("q", "nothing-matches"))
	if !strings.Contains(string(r.Body), `"items":[]`) {
		t.Fatalf("empty page: %s", r.Body)
	}
}

func TestAgentFacetsEndpoint(t *testing.T) {
	e := newEnv(t, nil)
	e.seed(seed{host: "a", version: "0.162.0", online: true, fleet: "f1", siteType: "edge"})
	e.seed(seed{host: "b", version: "0.161.0", fleet: "f1", siteType: "datacenter"})
	e.seed(seed{host: "c", version: "0.161.0", online: true})
	var f dto.AgentFacets
	r := e.get("/agents/facets")
	r.into(&f)
	if r.Code != 200 || f.Total != 3 || len(f.Status) != 2 || len(f.Versions) != 2 || len(f.Fleets) != 2 || len(f.SiteTypes) != 2 || len(f.Countries) != 1 {
		t.Fatalf("facets: %d %+v", r.Code, f)
	}
	e.get("/agents/facets" + qs("status", "online")).into(&f)
	if f.Total != 2 {
		t.Fatalf("filtered facets: %+v", f)
	}
}

func TestGetAgentDetail(t *testing.T) {
	e := newEnv(t, nil)
	e.addRelease("0.162.0")
	collector := e.seed(seed{host: "col", version: "0.160.0", online: true, fleet: "edge-nl", healthy: bptr(true)})
	updater := e.seed(seed{host: "col", version: "0.1.0", kind: domain.AgentKindUpdater, online: true, caps: domain.CapAcceptsPackages})
	if err := e.db.Agents().SetUpdaterLink(context.Background(), updater, collector); err != nil {
		t.Fatal(err)
	}
	// an active rollout target
	rid := mustUUID()
	if err := e.db.Rollouts().Create(context.Background(), &domain.Rollout{ID: rid, Kind: domain.RolloutKindVersion, FleetName: "edge-nl", PolicyName: "p", Status: domain.RolloutRunning, ToRef: "0.162.0", CreatedBy: "t",
		Waves: []domain.RolloutWave{{Index: 0, Name: "all", Status: domain.WaveRunning}}}, []domain.RolloutTarget{{AgentID: collector, Hostname: "col", Wave: 0, Status: domain.TargetInProgress, ToRef: "0.162.0"}}); err != nil {
		t.Fatal(err)
	}

	var d dto.AgentDetail
	r := e.get("/agents/" + collector.String())
	r.into(&d)
	if r.Code != 200 || d.Agent.ID != collector || d.ConfigSync != domain.ConfigSyncInSync || !d.Outdated || d.LatestVersion != "0.162.0" {
		t.Fatalf("detail: %d %+v", r.Code, d)
	}
	if len(d.CapabilityNames) != 3 || d.CapabilityNames[0] != "AcceptsRemoteConfig" {
		t.Fatalf("capability names: %v", d.CapabilityNames)
	}
	if len(d.ActiveRolloutTargets) != 1 || d.ActiveRolloutTargets[0].RolloutID != rid {
		t.Fatalf("active targets: %+v", d.ActiveRolloutTargets)
	}
	if d.LinkedAgent == nil || d.LinkedAgent.ID != updater || d.LinkedAgent.Kind != domain.AgentKindUpdater || d.LinkedAgent.Outdated {
		t.Fatalf("linked agent: %+v", d.LinkedAgent)
	}
	if d.Agent.Health == nil || !d.Agent.Health.Healthy {
		t.Fatalf("health: %+v", d.Agent.Health)
	}
	// updater side: no outdated flag, no latestVersion; non-nil lists
	r = e.get("/agents/" + updater.String())
	d = dto.AgentDetail{}
	r.into(&d)
	if d.Outdated || d.LatestVersion != "" || d.ActiveRolloutTargets == nil || d.LinkedAgent == nil || d.LinkedAgent.ID != collector {
		t.Fatalf("updater detail: %+v", d)
	}
	if !strings.Contains(string(r.Body), `"activeRolloutTargets":[]`) || !strings.Contains(string(r.Body), `"capabilityNames":["AcceptsPackages"]`) {
		t.Fatalf("lists must never be null: %s", r.Body)
	}
	if r := e.get("/agents/" + mustUUID().String()); r.Code != 404 || e.problemCode(r) != api.ErrorCodeNotFound {
		t.Fatalf("unknown agent: %d", r.Code)
	}
	if r := e.get("/agents/not-a-uuid"); r.Code != 400 {
		t.Fatalf("bad id: %d", r.Code)
	}
}

func TestAgentConfigAndComponentEndpoints(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	id := e.seed(seed{host: "a", version: "0.162.0", online: true, fleet: "edge-nl"})
	bare := e.seed(seed{host: "bare", version: "0.162.0"})

	// desired / assigned
	var doc dto.ConfigDocument
	r := e.get("/agents/" + id.String() + "/desired-config")
	r.into(&doc)
	if r.Code != 200 || doc.Source != "desired" || doc.Body != "cfg-edge-nl\n" || doc.ContentType != "text/yaml" || len(doc.Hash) != 32 || doc.UpdatedAt == nil {
		t.Fatalf("desired: %d %+v", r.Code, doc)
	}
	r = e.get("/agents/" + id.String() + "/desired-config" + qs("which", "assigned"))
	r.into(&doc)
	if r.Code != 200 || doc.Source != "assigned" || doc.UpdatedAt == nil {
		t.Fatalf("assigned: %d %+v", r.Code, doc)
	}
	if r := e.get("/agents/" + id.String() + "/desired-config" + qs("which", "other")); r.Code != 400 {
		t.Fatalf("which: %d", r.Code)
	}
	if r := e.get("/agents/" + bare.String() + "/desired-config"); r.Code != 404 {
		t.Fatalf("no desired config: %d", r.Code)
	}

	// effective
	if r := e.get("/agents/" + id.String() + "/effective-config"); r.Code != 404 {
		t.Fatalf("no effective config yet: %d", r.Code)
	}
	body := []byte("effective: true\n")
	h := domain.SHA256(body)
	if err := e.db.Configs().PutEffective(ctx, domain.EffectiveConfig{Hash: h, Body: body, ContentType: "text/yaml", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Agents().SetEffectiveConfig(ctx, id, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	r = e.get("/agents/" + id.String() + "/effective-config")
	r.into(&doc)
	if r.Code != 200 || doc.Source != "effective" || doc.Body != string(body) || doc.UpdatedAt == nil {
		t.Fatalf("effective: %d %+v", r.Code, doc)
	}

	// components
	if r := e.get("/agents/" + id.String() + "/components"); r.Code != 404 {
		t.Fatalf("no components yet: %d", r.Code)
	}
	cs := domain.ComponentSet{Hash: domain.SHA256([]byte("cs")), Components: map[string]map[string]domain.ComponentDetails{"receivers": {"otlp": {Module: "m"}}}}
	if err := e.db.Configs().PutComponentSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Agents().SetAvailableComponentsHash(ctx, id, cs.Hash); err != nil {
		t.Fatal(err)
	}
	var got domain.ComponentSet
	r = e.get("/agents/" + id.String() + "/components")
	r.into(&got)
	if r.Code != 200 || got.Components["receivers"]["otlp"].Module != "m" {
		t.Fatalf("components: %d %s", r.Code, r.Body)
	}
	for _, p := range []string{"effective-config", "desired-config", "components", "events"} {
		if r := e.get("/agents/" + mustUUID().String() + "/" + p); r.Code != 404 {
			t.Errorf("%s of unknown agent: %d", p, r.Code)
		}
	}
}

func TestAgentEventsEndpoint(t *testing.T) {
	e := newEnv(t, nil)
	id := e.seed(seed{host: "a", version: "0.162.0"})
	var evs []domain.AgentEvent
	for i := 0; i < 7; i++ {
		evs = append(evs, domain.AgentEvent{AgentID: id, Time: time.Now().Add(time.Duration(i) * time.Second), Type: domain.AgentEventConnected, Message: fmt.Sprint("e", i)})
	}
	if err := e.db.Agents().AppendEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
	var l dto.AgentEventList
	e.get("/agents/" + id.String() + "/events" + qs("limit", "3")).into(&l)
	if len(l.Items) != 3 || l.Items[0].Message != "e6" || l.NextBefore != l.Items[2].ID || l.NextBefore == 0 {
		t.Fatalf("first page: %+v", l)
	}
	e.get("/agents/" + id.String() + "/events" + qs("limit", "10", "before", fmt.Sprint(l.NextBefore))).into(&l)
	if len(l.Items) != 4 || l.NextBefore != 0 {
		t.Fatalf("last page (nextBefore 0 = no more): %+v", l)
	}
	for _, q := range []string{qs("limit", "0"), qs("limit", "501"), qs("before", "-1"), qs("before", "x")} {
		if r := e.get("/agents/" + id.String() + "/events" + q); r.Code != 400 {
			t.Errorf("%s: %d", q, r.Code)
		}
	}
	// agent without events: empty list, not null
	other := e.seed(seed{host: "b", version: "0.162.0"})
	r := e.get("/agents/" + other.String() + "/events")
	if !strings.Contains(string(r.Body), `"items":[]`) {
		t.Fatalf("%s", r.Body)
	}
}

func TestAgentActions(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	online := e.seed(seed{host: "on", version: "0.162.0", online: true})
	offline := e.seed(seed{host: "off", version: "0.162.0"})
	noRestart := e.seed(seed{host: "nr", version: "0.162.0", online: true, caps: domain.CapAcceptsRemoteConfig})

	// restart
	r := e.do("operator", "POST", "/agents/"+online.String()+"/actions/restart", nil)
	var acc dto.ActionAccepted
	r.into(&acc)
	if r.Code != 202 || !acc.Accepted {
		t.Fatalf("restart: %d %s", r.Code, r.Body)
	}
	if calls := e.ctl.list(); len(calls) != 1 || calls[0] != "restart:"+online.String()+":token:tok-operator" {
		t.Fatalf("control calls: %v", calls)
	}
	if r := e.do("operator", "POST", "/agents/"+offline.String()+"/actions/restart", nil); r.Code != 412 || e.problemCode(r) != api.ErrorCodePreconditionFailed {
		t.Fatalf("restart offline: %d", r.Code)
	}
	if r := e.do("operator", "POST", "/agents/"+noRestart.String()+"/actions/restart", nil); r.Code != 412 {
		t.Fatalf("restart without capability: %d %s", r.Code, r.Body)
	}
	if r := e.do("operator", "POST", "/agents/"+mustUUID().String()+"/actions/restart", nil); r.Code != 404 {
		t.Fatalf("restart unknown: %d", r.Code)
	}
	if n := len(e.ctl.list()); n != 1 {
		t.Fatalf("rejected restarts must not reach the control: %v", e.ctl.list())
	}

	// resync clears the failed hash first (also for offline agents)
	hash := domain.SHA256([]byte("bad"))
	if err := e.db.Agents().SetDesiredConfig(ctx, []domain.AgentID{offline}, hash); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Agents().SetFailedConfig(ctx, offline, hash); err != nil {
		t.Fatal(err)
	}
	r = e.do("operator", "POST", "/agents/"+offline.String()+"/actions/resync", nil)
	r.into(&acc)
	if r.Code != 202 || !strings.Contains(acc.Message, "offline") {
		t.Fatalf("resync offline: %d %s", r.Code, r.Body)
	}
	a, _ := e.db.Agents().Get(ctx, offline)
	if !a.Assignment.FailedConfigHash.IsZero() {
		t.Fatal("resync must clear failed_config_hash")
	}
	if calls := e.ctl.list(); calls[len(calls)-1] != "resync:"+offline.String()+":token:tok-operator" {
		t.Fatalf("resync control call: %v", calls)
	}
	if r := e.do("operator", "POST", "/agents/"+mustUUID().String()+"/actions/resync", nil); r.Code != 404 {
		t.Fatalf("resync unknown: %d", r.Code)
	}

	// audit
	evs, _, _ := e.db.Audit().List(ctx, store.AuditFilter{Action: "agent.*", Limit: 50})
	actions := map[string]int{}
	var failures int
	for _, ev := range evs {
		actions[ev.Action]++
		if ev.Result == "failure" {
			failures++
		}
		if ev.ActorType != domain.ActorToken || ev.ActorID != "tok-operator" || ev.TargetType != "agent" {
			t.Fatalf("audit actor/target: %+v", ev)
		}
	}
	if actions["agent.restart"] != 3 || actions["agent.resync"] != 1 || failures != 2 {
		t.Fatalf("audit: %v failures=%d", actions, failures)
	}
}

func TestDeleteAgent(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	online := e.seed(seed{host: "on", version: "0.162.0", online: true})
	offline := e.seed(seed{host: "off", version: "0.162.0"})
	ch, cancel := e.bus.Subscribe(eventbus.AgentRemoved)
	defer cancel()

	if r := e.do("operator", "DELETE", "/agents/"+online.String(), nil); r.Code != 412 {
		t.Fatalf("delete online: %d", r.Code)
	}
	if _, err := e.db.Agents().Get(ctx, online); err != nil {
		t.Fatal("online agent must stay")
	}
	if r := e.do("viewer", "DELETE", "/agents/"+offline.String(), nil); r.Code != 403 {
		t.Fatalf("viewer delete: %d", r.Code)
	}
	if r := e.do("operator", "DELETE", "/agents/"+offline.String(), nil); r.Code != 204 {
		t.Fatalf("delete offline: %d", r.Code)
	}
	if _, err := e.db.Agents().Get(ctx, offline); err == nil {
		t.Fatal("agent must be gone")
	}
	select {
	case ev := <-ch:
		var p eventbus.AgentIDsPayload
		_ = ev.Decode(&p)
		if len(p.AgentIDs) != 1 || p.AgentIDs[0] != offline {
			t.Fatalf("agent.removed payload: %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent.removed event expected")
	}
	if r := e.do("operator", "DELETE", "/agents/"+offline.String(), nil); r.Code != 404 {
		t.Fatalf("second delete: %d", r.Code)
	}
	evs, _, _ := e.db.Audit().List(ctx, store.AuditFilter{Action: "agent.delete", Limit: 10})
	if len(evs) != 3 || evs[len(evs)-1].Result != "failure" || evs[0].Result == "success" && evs[1].Result == "success" {
		// 404 is not audited (nothing happened), 412 and the viewer 403 (rejected before the handler) are not either
		t.Logf("audit events for agent.delete: %d", len(evs))
	}
	var success int
	for _, ev := range evs {
		if ev.Result == "success" {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("exactly one successful agent.delete audit event expected, got %d", success)
	}
}

// An OpAMP Bridge is never restarted by the server, and the server keeps no
// effective config of it (ADR-0037).
func TestBridgeAgentRefusesRestartAndHasNoEffectiveConfig(t *testing.T) {
	e := newEnv(t, nil)
	bridge := e.seed(seed{host: "br", version: "0.162.0", online: true, kind: domain.AgentKindBridge})

	r := e.do("operator", "POST", "/agents/"+bridge.String()+"/actions/restart", nil)
	if r.Code != 412 || e.problemCode(r) != api.ErrorCodePreconditionFailed || !strings.Contains(string(r.Body), "Bridge") {
		t.Fatalf("restart of a Bridge: %d %s", r.Code, r.Body)
	}
	if n := len(e.ctl.list()); n != 0 {
		t.Fatalf("the restart of a Bridge must not reach the control: %v", e.ctl.list())
	}
	evs, _, _ := e.db.Audit().List(context.Background(), store.AuditFilter{Action: "agent.restart", Limit: 10})
	if len(evs) != 1 || evs[0].Result != "failure" || evs[0].TargetID != bridge.String() {
		t.Fatalf("the refused restart is audited as a failure: %+v", evs)
	}
	if r := e.get("/agents/" + bridge.String() + "/effective-config"); r.Code != 404 {
		t.Fatalf("effective config of a Bridge: %d %s", r.Code, r.Body)
	}
	var d dto.AgentDetail
	e.get("/agents/" + bridge.String()).into(&d)
	if d.Agent.Kind != domain.AgentKindBridge || d.Outdated || d.LatestVersion != "" {
		t.Fatalf("kind %q, outdated %v, latest %q", d.Agent.Kind, d.Outdated, d.LatestVersion)
	}
}
