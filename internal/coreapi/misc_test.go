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
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

func TestSavedFilters(t *testing.T) {
	e := newEnv(t, nil)
	var f domain.SavedFilter
	r := e.do("user-operator", "POST", "/saved-filters", map[string]any{"name": "offline edge", "page": "agents", "query": map[string]any{"status": []string{"offline"}, "n": 3}, "shared": true})
	r.into(&f)
	if r.Code != 201 || f.ID.String() == "" || f.Name != "offline edge" || !f.Shared || f.UserID != e.auth.userID {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	var list api.List[domain.SavedFilter]
	e.do("user-viewer", "GET", "/saved-filters", nil).into(&list)
	if len(list.Items) != 1 || list.Items[0].Query["n"] != float64(3) {
		t.Fatalf("list: %+v", list)
	}
	// default page + page filter
	r = e.do("user-operator", "POST", "/saved-filters", map[string]any{"name": "x", "page": "", "query": nil})
	if r.Code != 201 {
		t.Fatalf("defaults: %d %s", r.Code, r.Body)
	}
	e.do("user-operator", "GET", "/saved-filters?page=rollouts", nil).into(&list)
	if list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("other page: %+v", list)
	}
	// conflict
	if r := e.do("user-operator", "POST", "/saved-filters", map[string]any{"name": "offline edge", "page": "agents", "query": map[string]any{}}); r.Code != 409 || e.problemCode(r) != api.ErrorCodeConflict {
		t.Fatalf("duplicate: %d %s", r.Code, r.Body)
	}
	// validation
	for name, body := range map[string]map[string]any{
		"empty name": {"name": " ", "page": "agents", "query": map[string]any{}},
		"long name":  {"name": strings.Repeat("x", 101), "page": "agents", "query": map[string]any{}},
		"bad page":   {"name": "n", "page": "Agents!", "query": map[string]any{}},
	} {
		if r := e.do("user-operator", "POST", "/saved-filters", body); r.Code != 422 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	if r := e.do("user-operator", "POST", "/saved-filters", map[string]any{"name": "n", "bogus": 1}); r.Code != 400 {
		t.Errorf("unknown field: %d", r.Code)
	}
	// API tokens cannot own filters
	if r := e.do("operator", "POST", "/saved-filters", map[string]any{"name": "n", "page": "agents", "query": map[string]any{}}); r.Code != 403 {
		t.Errorf("token create: %d", r.Code)
	}
	e.do("operator", "GET", "/saved-filters", nil).into(&list)
	if len(list.Items) != 0 {
		t.Error("tokens see no filters")
	}
	// delete: own only
	if r := e.do("user-operator", "DELETE", "/saved-filters/"+f.ID.String(), nil); r.Code != 204 {
		t.Fatalf("delete: %d", r.Code)
	}
	if r := e.do("user-operator", "DELETE", "/saved-filters/"+f.ID.String(), nil); r.Code != 404 {
		t.Fatalf("second delete: %d", r.Code)
	}
	if r := e.do("user-operator", "DELETE", "/saved-filters/xyz", nil); r.Code != 400 {
		t.Fatalf("bad id: %d", r.Code)
	}
}

func TestSystemInfo(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Server.PublicURL = "https://fleet.example.com/"
		c.OpAMP.Path = "/v1/opamp"
	})
	ctx := context.Background()
	now := time.Now().UTC()
	if err := e.db.Replicas().Heartbeat(ctx, domain.Replica{ID: "replica-1", Hostname: "h1", Version: "1.2.3", StartedAt: now, IsLeader: true, Connections: 4}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Replicas().Heartbeat(ctx, domain.Replica{ID: "replica-2", Hostname: "h2", Version: "1.2.3", StartedAt: now, Connections: 6}); err != nil {
		t.Fatal(err)
	}
	// a crashed former leader still has its last heartbeat row (leader flag set) but must not count as leader
	if err := e.db.Replicas().Heartbeat(ctx, domain.Replica{ID: "replica-dead", Hostname: "h3", Version: "1.2.3", StartedAt: now.Add(-time.Hour),
		LastHeartbeatAt: now.Add(-10 * time.Minute), IsLeader: true, Connections: 99}); err != nil {
		t.Fatal(err)
	}
	var info dto.SystemInfo
	r := e.get("/system/info")
	r.into(&info)
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	leaders := 0
	for _, rep := range info.Replicas {
		if rep.IsLeader {
			leaders++
		}
	}
	if leaders != 1 || len(info.Replicas) != 3 {
		t.Fatalf("exactly one displayed leader expected, replicas: %+v", info.Replicas)
	}
	if info.ReplicaID != "replica-1" || info.LeaderID != "replica-1" || info.Replicas[1].Connections != 6 {
		t.Fatalf("replicas/leader: %+v", info)
	}
	// the local replica's live values overlay its (older) heartbeat row: 7 connections, leader
	if info.Replicas[0].ID != "replica-1" || info.Replicas[0].Connections != 7 || !info.Replicas[0].IsLeader {
		t.Fatalf("local replica overlay: %+v", info.Replicas[0])
	}
	if info.OpAMPEndpoint != "wss://fleet.example.com/v1/opamp" || info.PublicURL != "https://fleet.example.com/" {
		t.Fatalf("endpoints: %q %q", info.OpAMPEndpoint, info.PublicURL)
	}
	if !info.Database.OK || info.Database.ServerVersion == "" || info.Database.MigrationVersion < 2 {
		t.Fatalf("database: %+v", info.Database)
	}
	if !info.Features.DevMode || info.Features.OIDC || !info.Features.GitOps || info.Version == "" {
		t.Fatalf("features/version: %+v", info)
	}
	s := info.Settings
	if strings.Contains(s.GitOpsSource, "secret-token") || strings.Contains(s.GitOpsSource, "oauth2") || strings.Contains(s.GitOpsSource, "token=") || s.GitOpsSource != "https://git.example.com/otel/config.git" {
		t.Fatalf("credentials must be stripped from the git source: %q", s.GitOpsSource)
	}
	if s.GitOpsBranch != "main" || s.GitOpsPollInterval != "1m0s" || s.OfflineAfter != "1m30s" || !s.SignatureVerification || s.DefaultTimezone != "UTC" ||
		len(s.TrackedDistributions) != 2 || s.RetentionAudit == "" {
		t.Fatalf("settings: %+v", s)
	}
	if strings.Contains(string(r.Body), "secret-token") {
		t.Fatal("no secret may appear in system info")
	}

	// http public url -> ws, scp-style git address, empty list never null
	e2 := newEnv(t, func(c *config.Config) {
		c.Server.PublicURL = "http://localhost:8080"
		c.GitOps.RepoURL = "git@git.example.com:otel/config.git"
		c.Releases.Distributions = nil
	})
	var info2 dto.SystemInfo
	r = e2.get("/system/info")
	r.into(&info2)
	if info2.OpAMPEndpoint != "ws://localhost:8080/v1/opamp" || info2.Settings.GitOpsSource != "git.example.com:otel/config.git" {
		t.Fatalf("%q %q", info2.OpAMPEndpoint, info2.Settings.GitOpsSource)
	}
	if !strings.Contains(string(r.Body), `"trackedDistributions":[]`) || !strings.Contains(string(r.Body), `"replicas":[]`) {
		t.Fatalf("lists must not be null: %s", r.Body)
	}
}

// features.bootstrapAdmin follows AuthConfig.BootstrapActive: enabled with a
// credential file and not replaced by single sign-on.
func TestSystemInfoBootstrapAdminFlag(t *testing.T) {
	flag := func(mutate func(a *config.AuthConfig)) bool {
		e := newEnv(t, func(c *config.Config) { mutate(&c.Auth) })
		var info dto.SystemInfo
		r := e.get("/system/info")
		r.into(&info)
		if r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		if !strings.Contains(string(r.Body), `"bootstrapAdmin":`) {
			t.Fatalf("the flag is always present: %s", r.Body)
		}
		return info.Features.BootstrapAdmin
	}
	active := func(a *config.AuthConfig) {
		a.BootstrapAdmin = config.BootstrapAdminConfig{Enabled: true, Username: "admin", TokenFile: "/etc/myriarchd/bootstrap/token"}
	}
	if flag(func(a *config.AuthConfig) {}) {
		t.Error("off by default")
	}
	if !flag(active) {
		t.Error("enabled with a token file")
	}
	if flag(func(a *config.AuthConfig) { active(a); a.BootstrapAdmin.Enabled = false }) {
		t.Error("disabled")
	}
	if flag(func(a *config.AuthConfig) { active(a); a.BootstrapAdmin.TokenFile = "" }) {
		t.Error("no credential file")
	}
	if flag(func(a *config.AuthConfig) { active(a); a.OIDC.Enabled = true }) {
		t.Error("replaced by single sign-on")
	}
	if !flag(func(a *config.AuthConfig) { active(a); a.OIDC.Enabled, a.BootstrapAdmin.KeepWithOIDC = true, true }) {
		t.Error("kept next to single sign-on")
	}
}

func TestAuditEndpoint(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i, a := range []string{"rollout.pause", "rollout.abort", "token.create", "agent.restart"} {
		if err := e.db.Audit().Append(ctx, domain.AuditEvent{Time: base.Add(time.Duration(i) * time.Minute), ActorType: domain.ActorUser, ActorID: fmt.Sprint("u", i%2), ActorName: "N", Action: a, TargetType: "x", TargetID: fmt.Sprint(i), Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	var page api.Page[domain.AuditEvent]
	r := e.do("operator", "GET", "/audit"+qs("action", "rollout.*"), nil)
	r.into(&page)
	if r.Code != 200 || page.Total != 2 || len(page.Items) != 2 || page.Items[0].Action != "rollout.abort" {
		t.Fatalf("prefix filter: %d %+v", r.Code, page)
	}
	e.do("operator", "GET", "/audit"+qs("actor", "u1"), nil).into(&page)
	if page.Total != 2 {
		t.Fatalf("actor: %+v", page)
	}
	e.do("operator", "GET", "/audit"+qs("targetType", "x", "targetId", "2"), nil).into(&page)
	if page.Total != 1 || page.Items[0].Action != "token.create" {
		t.Fatalf("target: %+v", page)
	}
	e.do("operator", "GET", "/audit"+qs("from", base.Add(90*time.Second).Format(time.RFC3339), "to", base.Add(150*time.Second).Format(time.RFC3339)), nil).into(&page)
	if page.Total != 1 {
		t.Fatalf("time range: %+v", page)
	}
	e.do("operator", "GET", "/audit"+qs("limit", "1", "offset", "3"), nil).into(&page)
	if page.Total != 4 || len(page.Items) != 1 || page.Limit != 1 || page.Offset != 3 {
		t.Fatalf("paging: %+v", page)
	}
	if r := e.do("operator", "GET", "/audit"+qs("from", "yesterday"), nil); r.Code != 400 {
		t.Fatalf("bad time: %d", r.Code)
	}
	if r := e.do("viewer", "GET", "/audit", nil); r.Code != 403 || e.problemCode(r) != api.ErrorCodeForbidden {
		t.Fatalf("viewer must not read the audit log: %d", r.Code)
	}
	if r := e.do("", "GET", "/audit", nil); r.Code != 401 {
		t.Fatalf("anonymous: %d", r.Code)
	}
}

func TestOverviewStats(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	e.addRelease("0.162.0")
	e.seed(seed{host: "a", version: "0.162.0", online: true, fleet: "edge-nl", healthy: bptr(true)})
	e.seed(seed{host: "b", version: "0.161.0", online: true, fleet: "edge-nl", healthy: bptr(false)})
	e.seed(seed{host: "c", version: "0.161.0", fleet: "edge-be"})
	e.seed(seed{host: "d", version: "0.160.0", online: true}) // unassigned
	e.seed(seed{host: "u1", version: "0.1.0", kind: domain.AgentKindUpdater, online: true, healthy: bptr(true)})
	pol := domain.DefaultRolloutPolicy()
	rid := mustUUID()
	if err := e.db.Rollouts().Create(ctx, &domain.Rollout{ID: rid, Kind: domain.RolloutKindConfig, FleetName: "edge-nl", PolicyName: "p", Policy: pol, Status: domain.RolloutRunning, ToRef: "c", CreatedBy: "t",
		Waves: []domain.RolloutWave{{Index: 0, Name: "all", Status: domain.WaveRunning}}}, []domain.RolloutTarget{{AgentID: mustUUID(), Wave: 0, Status: domain.TargetInProgress, ToRef: "x"}}); err != nil {
		t.Fatal(err)
	}
	// fleets for "fleets behind" and gitops status
	if _, err := e.db.GitOps().ApplySnapshot(ctx, domain.GitOpsSnapshot{Commit: "abc", Fleets: []domain.Fleet{
		{ObjectMeta: domain.ObjectMeta{Name: "edge-nl"}, FleetSpec: domain.FleetSpec{Blueprint: "b", Collector: domain.CollectorSpec{Version: "0.161.0"}}, Source: domain.SourceRef{Path: "f.yaml", Commit: "abc"}},
		{ObjectMeta: domain.ObjectMeta{Name: "edge-be"}, FleetSpec: domain.FleetSpec{Blueprint: "b", Collector: domain.CollectorSpec{Version: "0.162.0"}}, Source: domain.SourceRef{Path: "g.yaml", Commit: "abc"}},
	}}, domain.GitOpsSync{Commit: &domain.GitCommit{SHA: "abc"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.GitOps().SaveStatus(ctx, domain.GitOpsSyncStatus{State: "ok", AppliedCommit: &domain.GitCommit{SHA: "abc"}, Issues: domain.Issues{{Severity: domain.SeverityWarning, Code: "w", Message: "m"}}}); err != nil {
		t.Fatal(err)
	}

	var o dto.OverviewStats
	r := e.get("/stats/overview")
	r.into(&o)
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if o.Agents.Total != 5 || o.Agents.Collectors != 4 || o.Agents.Updaters != 1 || o.Agents.Online != 4 || o.Agents.Offline != 1 ||
		o.Agents.Healthy != 2 || o.Agents.Unhealthy != 1 || o.Agents.HealthUnknown != 2 {
		t.Fatalf("agent counts: %+v", o.Agents)
	}
	if o.Outdated.LatestVersion != "0.162.0" || o.Outdated.Count != 3 || o.Outdated.Percent != 75 {
		t.Fatalf("outdated: %+v", o.Outdated)
	}
	if len(o.Versions) != 3 || o.Versions[0].Version != "0.161.0" || o.Versions[0].Count != 2 {
		t.Fatalf("versions: %+v", o.Versions)
	}
	for _, v := range o.Versions {
		if v.Outdated != (v.Version != "0.162.0") {
			t.Fatalf("version flags: %+v", v)
		}
	}
	if o.Unassigned != 1 {
		t.Fatalf("unassigned = %d", o.Unassigned)
	}
	if len(o.Fleets) != 2 || o.Fleets[0].Fleet != "edge-be" || o.Fleets[1].Fleet != "edge-nl" {
		t.Fatalf("fleets: %+v", o.Fleets)
	}
	nl := o.Fleets[1]
	if nl.Total != 2 || nl.Online != 2 || nl.Healthy != 1 || nl.InSync != 2 || nl.TargetVersion != "0.162.0" || nl.OnTarget != 1 || nl.Pending != 0 || nl.Failed != 0 {
		t.Fatalf("fleet counts: %+v", nl)
	}
	if len(o.ConfigSync) == 0 {
		t.Fatalf("config sync facet: %+v", o.ConfigSync)
	}
	if len(o.ActiveRollouts) != 1 || o.ActiveRollouts[0].ID != rid || o.ActiveRollouts[0].Counts.InProgress != 1 || o.ActiveRollouts[0].WaveCount != 1 {
		t.Fatalf("active rollouts: %+v", o.ActiveRollouts)
	}
	if o.LatestRelease == nil || o.LatestRelease.Version != "0.162.0" || !o.LatestRelease.IsLatest || o.LatestRelease.FleetsBehind != 1 {
		t.Fatalf("latest release: %+v", o.LatestRelease)
	}
	if o.GitOps.State != "ok" || o.GitOps.AppliedCommit != "abc" || o.GitOps.IssueCount != 1 {
		t.Fatalf("gitops: %+v", o.GitOps)
	}

	// An acknowledgement is visible immediately although the overview is cached.
	if o.LatestRelease.Acknowledged {
		t.Fatal("release must not be acknowledged yet")
	}
	if err := e.db.Releases().Acknowledge(context.Background(), domain.DistributionContrib, "0.162.0", "tester", e.now); err != nil {
		t.Fatal(err)
	}
	e.get("/stats/overview").into(&o)
	if !o.LatestRelease.Acknowledged {
		t.Fatalf("acknowledgement must bypass the overview cache: %+v", o.LatestRelease)
	}

	// cached for 2s (the test clock is frozen), recomputed afterwards
	e.seed(seed{host: "e", version: "0.162.0", online: true})
	e.get("/stats/overview").into(&o)
	if o.Agents.Total != 5 {
		t.Fatalf("overview must be cached for 2s, got total %d", o.Agents.Total)
	}
	e.now = e.now.Add(3 * time.Second)
	e.get("/stats/overview").into(&o)
	if o.Agents.Total != 6 {
		t.Fatalf("overview must be recomputed after the TTL, got total %d", o.Agents.Total)
	}

	// empty system: no nulls
	e2 := newEnv(t, nil)
	r = e2.get("/stats/overview")
	for _, want := range []string{`"versions":[]`, `"fleets":[]`, `"activeRollouts":[]`, `"configSync":[]`} {
		if !strings.Contains(string(r.Body), want) {
			t.Errorf("empty overview lacks %s: %s", want, r.Body)
		}
	}
	var empty dto.OverviewStats
	r.into(&empty)
	if empty.GitOps.State != "never" || empty.LatestRelease != nil {
		t.Fatalf("empty overview: %+v", empty)
	}
}

func TestOverviewFleetCountsFallbackMatchesAggregate(t *testing.T) {
	e := newEnv(t, nil)
	e.seed(seed{host: "a", version: "0.162.0", online: true, fleet: "f1", healthy: bptr(true)})
	e.seed(seed{host: "b", version: "0.161.0", fleet: "f1"})
	e.seed(seed{host: "c", version: "0.162.0", online: true, fleet: "f2"})
	agg, err := e.db.FleetCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var o dto.OverviewStats
	e.get("/stats/overview").into(&o)
	if fmt.Sprintf("%+v", agg) != fmt.Sprintf("%+v", o.Fleets) {
		t.Fatalf("aggregate %+v vs overview %+v", agg, o.Fleets)
	}
	_ = store.AgentFilter{}
}
