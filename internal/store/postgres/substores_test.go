// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

func TestConfigsContentAddressedAndGC(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	cs := db.Configs()
	body := []byte("receivers:\n  otlp: {}\n")
	hash := domain.ConfigHash(body)
	rc := domain.RenderedConfig{Hash: hash, Body: body, FleetName: "edge-nl", BlueprintName: "edge", GitCommit: "abc", CreatedAt: time.Now().Add(-48 * time.Hour)}
	mustNoErr(t, cs.PutRendered(ctx, rc))
	got, err := cs.GetRendered(ctx, hash)
	mustNoErr(t, err)
	if string(got.Body) != string(body) || got.ContentType != "text/yaml" || got.Size != len(body) || got.FleetName != "edge-nl" || !got.Hash.Equal(hash) {
		t.Fatalf("rendered config: %+v", got)
	}
	if _, err := cs.GetRendered(ctx, domain.SHA256([]byte("x"))); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown rendered config: %v", err)
	}
	// idempotent put refreshes touched_at only (original provenance kept)
	rc.FleetName = "other"
	mustNoErr(t, cs.PutRendered(ctx, rc))
	got, _ = cs.GetRendered(ctx, hash)
	if got.FleetName != "edge-nl" {
		t.Fatal("conflicting put must not overwrite the first render")
	}

	eff := domain.EffectiveConfig{Body: []byte("effective: true\n")}
	mustNoErr(t, cs.PutEffective(ctx, eff)) // hash computed
	ge, err := cs.GetEffective(ctx, domain.SHA256(eff.Body))
	mustNoErr(t, err)
	if string(ge.Body) != string(eff.Body) || ge.ContentType != "text/yaml" {
		t.Fatalf("effective: %+v", ge)
	}

	set := domain.ComponentSet{Hash: domain.SHA256([]byte("set")), Components: map[string]map[string]domain.ComponentDetails{
		"receivers": {"otlp": {Module: "go.opentelemetry.io/collector/receiver/otlpreceiver v0.162.0"}, "host_metrics": {Module: "x", Metadata: map[string]string{"a": "b"}}},
	}}
	ok, err := cs.HasComponentSet(ctx, set.Hash)
	mustNoErr(t, err)
	if ok {
		t.Fatal("component set must not exist yet")
	}
	mustNoErr(t, cs.PutComponentSet(ctx, set))
	mustNoErr(t, cs.PutComponentSet(ctx, set))
	ok, _ = cs.HasComponentSet(ctx, set.Hash)
	gs, err := cs.GetComponentSet(ctx, set.Hash)
	mustNoErr(t, err)
	if !ok || gs.Components["receivers"]["otlp"].Module != set.Components["receivers"]["otlp"].Module || gs.Components["receivers"]["host_metrics"].Metadata["a"] != "b" {
		t.Fatalf("component set: %+v", gs)
	}

	// GC: unreferenced and old -> deleted; referenced by an agent or an active config rollout -> kept
	mk := func(s string) domain.Hash {
		b := []byte(s)
		h := domain.ConfigHash(b)
		mustNoErr(t, cs.PutRendered(ctx, domain.RenderedConfig{Hash: h, Body: b}))
		return h
	}
	unref, byAgent, byRollout := mk("unreferenced"), mk("agent"), mk("rollout")
	id := upsert(t, ctx, db, agentSpec{host: "a", version: "0.162.0"})
	mustNoErr(t, db.Agents().SetDesiredConfig(ctx, []domain.AgentID{id}, byAgent))
	rid := uuid.New()
	mustNoErr(t, db.Rollouts().Create(ctx, &domain.Rollout{ID: rid, Kind: domain.RolloutKindConfig, FleetName: "f", PolicyName: "p", Status: domain.RolloutRunning, ToRef: "c", CreatedBy: "t",
		Waves: []domain.RolloutWave{{Index: 0, Name: "all", Status: domain.WaveRunning}}},
		[]domain.RolloutTarget{{AgentID: newID(), Wave: 0, Status: domain.TargetPending, FromRef: "", ToRef: byRollout.String()}}))
	mustNoErr(t, db.Agents().SetEffectiveConfig(ctx, id, domain.SHA256(eff.Body), time.Now()))
	// touched_at is now() for everything: GC with a cutoff in the future deletes all unreferenced
	n, err := cs.GC(ctx, time.Now().Add(time.Hour))
	mustNoErr(t, err)
	for _, c := range []struct {
		h    domain.Hash
		want bool
	}{{unref, false}, {byAgent, true}, {byRollout, true}} {
		_, err := cs.GetRendered(ctx, c.h)
		if (err == nil) != c.want {
			t.Errorf("GC: config %s exists=%v want %v", c.h.Short(), err == nil, c.want)
		}
	}
	if n < 1 {
		t.Errorf("GC deleted %d rows", n)
	}
	// rollout becomes terminal -> its config can be collected
	ok2, err := db.Rollouts().CompareAndSetStatus(ctx, rid, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutCompleted, "done")
	mustNoErr(t, err)
	if !ok2 {
		t.Fatal("CAS")
	}
	_, err = cs.GC(ctx, time.Now().Add(time.Hour))
	mustNoErr(t, err)
	if _, err := cs.GetRendered(ctx, byRollout); !errors.Is(err, domain.ErrNotFound) {
		t.Error("config of a finished rollout must be collectable")
	}
	// recently touched configs survive
	recent := mk("recent")
	_, err = cs.GC(ctx, time.Now().Add(-time.Hour))
	mustNoErr(t, err)
	if _, err := cs.GetRendered(ctx, recent); err != nil {
		t.Error("GC must respect touched_at")
	}
	// effective configs referenced by an agent survive, unreferenced ones go
	if _, err := cs.GetEffective(ctx, domain.SHA256(eff.Body)); err != nil {
		t.Error("referenced effective config must survive GC")
	}
	mustNoErr(t, cs.PutEffective(ctx, domain.EffectiveConfig{Body: []byte("orphan")}))
	_, err = cs.GC(ctx, time.Now().Add(time.Hour))
	mustNoErr(t, err)
	if _, err := cs.GetEffective(ctx, domain.SHA256([]byte("orphan"))); !errors.Is(err, domain.ErrNotFound) {
		t.Error("unreferenced effective config must be collected")
	}
}

func testSnapshot(commit string, n int, tweak func(*domain.GitOpsSnapshot)) domain.GitOpsSnapshot {
	src := func(path string, hashSeed string) domain.SourceRef {
		return domain.SourceRef{Path: path, Commit: commit, ContentHash: domain.SHA256([]byte(hashSeed))}
	}
	snap := domain.GitOpsSnapshot{
		Commit: commit,
		Fleets: []domain.Fleet{{
			ObjectMeta: domain.ObjectMeta{Name: "edge-nl", Labels: map[string]string{"a": "b"}},
			FleetSpec: domain.FleetSpec{Priority: 10, Blueprint: "edge", Parameters: map[string]any{"batch": 8192, "ratio": 0.5, "name": "x", "list": []any{1, "a"}},
				Selector: domain.Selector{MatchAttributes: map[string]string{"site.type": "edge"}}, Collector: domain.CollectorSpec{Version: "0.162.0"}},
			Source: src("fleets/edge-nl.yaml", fmt.Sprint("fleet", n)),
		}},
		Blueprints: []domain.Blueprint{{
			ObjectMeta: domain.ObjectMeta{Name: "edge"},
			BlueprintSpec: domain.BlueprintSpec{Parameters: []domain.ParameterSpec{{Name: "batch", Type: domain.ParamInt, Default: 75}},
				Config: map[string]any{"processors": map[string]any{"batch": map[string]any{"send_batch_size": 10000000}}}},
			Source: src("blueprints/edge.yaml", "bp"),
		}},
		RolloutPolicies: []domain.RolloutPolicy{{ObjectMeta: domain.ObjectMeta{Name: "careful"}, RolloutPolicySpec: domain.RolloutPolicySpec{
			Waves: []domain.WaveSpec{{Name: "canary", Percent: 5}, {Name: "rest", Percent: 100}}, MaxConcurrent: 10, HealthWait: domain.Duration(time.Minute)},
			Source: src("policies/careful.yaml", "pol")}},
		Executors: []domain.Executor{{ObjectMeta: domain.ObjectMeta{Name: "awx"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorAWX,
			AWX: &domain.AWXExecutorSpec{URL: "https://awx", JobTemplateID: 7, ExtraVars: map[string]any{"n": 3}}}, Source: src("executors/awx.yaml", "ex")}},
		Inventories: []domain.Inventory{{ObjectMeta: domain.ObjectMeta{Name: "sites"}, InventorySpec: domain.InventorySpec{Entries: []domain.InventoryEntry{
			{Match: map[string]string{"site.id": "1001"}, Labels: map[string]string{"pilot": "true"}, Parameters: map[string]any{"k": 5}}}}, Source: src("inventory/sites.yaml", "inv")}},
	}
	if tweak != nil {
		tweak(&snap)
	}
	return snap
}

func TestGitOpsSnapshotRoundTripAndChanges(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	g := db.GitOps()

	empty, err := g.GetSnapshot(ctx)
	mustNoErr(t, err)
	if empty.Commit != "" || empty.Fleets == nil || empty.Blueprints == nil || empty.Inventories == nil || empty.Executors == nil || empty.RolloutPolicies == nil {
		t.Fatalf("empty snapshot must have non-nil slices: %+v", empty)
	}
	st, err := g.GetStatus(ctx)
	mustNoErr(t, err)
	if st.State != "never" || st.Issues == nil || st.ObjectCounts == nil {
		t.Fatalf("status without data: %+v", st)
	}

	snap := testSnapshot("c1", 1, nil)
	ch, err := g.ApplySnapshot(ctx, snap, domain.GitOpsSync{Trigger: "manual", StartedAt: time.Now(), Commit: &domain.GitCommit{SHA: "c1", Author: "me", Time: time.Now()}, Result: "applied"})
	mustNoErr(t, err)
	if len(ch.Added) != 5 || len(ch.Changed) != 0 || len(ch.Removed) != 0 || !sort.StringsAreSorted(ch.Added) {
		t.Fatalf("first apply: %+v", ch)
	}
	if ch.Added[0] != "Blueprint/edge" || ch.Added[1] != "Executor/awx" {
		t.Fatalf("keys must be <Kind>/<name> sorted: %v", ch.Added)
	}

	got, err := g.GetSnapshot(ctx)
	mustNoErr(t, err)
	if got.Commit != "c1" || len(got.Fleets) != 1 || len(got.Blueprints) != 1 || len(got.RolloutPolicies) != 1 || len(got.Executors) != 1 || len(got.Inventories) != 1 {
		t.Fatalf("snapshot: %+v", got)
	}
	f := got.Fleets[0]
	if v, ok := f.Parameters["batch"].(int); !ok || v != 8192 {
		t.Fatalf("integers in interface fields must stay int, got %T %v", f.Parameters["batch"], f.Parameters["batch"])
	}
	if _, ok := f.Parameters["ratio"].(float64); !ok {
		t.Fatalf("float stays float: %T", f.Parameters["ratio"])
	}
	if l, ok := f.Parameters["list"].([]any); !ok || l[0].(int) != 1 {
		t.Fatalf("list: %#v", f.Parameters["list"])
	}
	if f.Source.Path != "fleets/edge-nl.yaml" || f.Source.Commit != "c1" || len(f.Source.ContentHash) != 32 {
		t.Fatalf("source ref lost: %+v", f.Source)
	}
	bp := got.Blueprints[0]
	if v, ok := bp.Config["processors"].(map[string]any)["batch"].(map[string]any)["send_batch_size"].(int); !ok || v != 10000000 {
		t.Fatalf("blueprint config integer: %#v", bp.Config)
	}
	if d, ok := bp.Parameters[0].Default.(int); !ok || d != 75 {
		t.Fatalf("parameter default: %#v", bp.Parameters[0].Default)
	}
	if got.Executors[0].AWX.ExtraVars["n"].(int) != 3 || got.Inventories[0].Entries[0].Parameters["k"].(int) != 5 {
		t.Fatalf("executor/inventory ints: %+v", got.Executors[0])
	}
	if got.RolloutPolicies[0].HealthWait.D() != time.Minute {
		t.Fatalf("duration: %+v", got.RolloutPolicies[0])
	}

	fl, err := g.GetFleet(ctx, "edge-nl")
	mustNoErr(t, err)
	if fl.Priority != 10 || fl.Labels["a"] != "b" {
		t.Fatalf("GetFleet: %+v", fl)
	}
	if _, err := g.GetFleet(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := g.GetBlueprint(ctx, "edge"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GetRolloutPolicy(ctx, "careful"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GetExecutor(ctx, "awx"); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]func() (int, error){
		"fleets":     func() (int, error) { l, e := g.ListFleets(ctx); return len(l), e },
		"blueprints": func() (int, error) { l, e := g.ListBlueprints(ctx); return len(l), e },
		"policies":   func() (int, error) { l, e := g.ListRolloutPolicies(ctx); return len(l), e },
		"executors":  func() (int, error) { l, e := g.ListExecutors(ctx); return len(l), e },
		"inventory":  func() (int, error) { l, e := g.ListInventories(ctx); return len(l), e },
	} {
		if n, err := fn(); err != nil || n != 1 {
			t.Errorf("%s list: %d %v", name, n, err)
		}
	}

	// change one object, remove one, add one
	snap2 := testSnapshot("c2", 2, func(s *domain.GitOpsSnapshot) {
		s.Executors = nil
		s.Fleets = append(s.Fleets, domain.Fleet{ObjectMeta: domain.ObjectMeta{Name: "datacenter"}, FleetSpec: domain.FleetSpec{Blueprint: "edge"},
			Source: domain.SourceRef{Path: "fleets/datacenter.yaml", Commit: "c2", ContentHash: domain.SHA256([]byte("datacenter"))}})
	})
	ch, err = g.ApplySnapshot(ctx, snap2, domain.GitOpsSync{Trigger: "poll", Commit: &domain.GitCommit{SHA: "c2"}})
	mustNoErr(t, err)
	if fmt.Sprint(ch.Added) != "[Fleet/datacenter]" || fmt.Sprint(ch.Changed) != "[Fleet/edge-nl]" || fmt.Sprint(ch.Removed) != "[Executor/awx]" {
		t.Fatalf("changes: %+v", ch)
	}
	// unchanged re-apply: no changes, but the commit moves
	ch, err = g.ApplySnapshot(ctx, snap2, domain.GitOpsSync{Trigger: "poll", Commit: &domain.GitCommit{SHA: "c2"}})
	mustNoErr(t, err)
	if len(ch.Added)+len(ch.Changed)+len(ch.Removed) != 0 || ch.Added == nil {
		t.Fatalf("no-op apply: %+v", ch)
	}
	got, _ = g.GetSnapshot(ctx)
	if got.Commit != "c2" || len(got.Fleets) != 2 || len(got.Executors) != 0 {
		t.Fatalf("snapshot after second apply: commit %q fleets %d executors %d", got.Commit, len(got.Fleets), len(got.Executors))
	}

	// a failed (invalid) sync is only recorded, the snapshot commit stays
	id, err := g.RecordSync(ctx, domain.GitOpsSync{Trigger: "webhook", Result: "invalid", StartedAt: time.Now(), Commit: &domain.GitCommit{SHA: "c3"},
		Issues: domain.Issues{{Severity: domain.SeverityError, Code: "yaml_syntax", Message: "bad"}}})
	mustNoErr(t, err)
	if id == 0 {
		t.Fatal("RecordSync must return the id")
	}
	got, _ = g.GetSnapshot(ctx)
	if got.Commit != "c2" {
		t.Fatalf("snapshot commit must be the last APPLIED sync, got %q", got.Commit)
	}
	syncs, total, err := g.ListSyncs(ctx, 2, 0)
	mustNoErr(t, err)
	if total != 4 || len(syncs) != 2 || syncs[0].Result != "invalid" || len(syncs[0].Issues) != 1 || syncs[0].Commit == nil || syncs[0].Commit.SHA != "c3" {
		t.Fatalf("syncs: total=%d %+v", total, syncs)
	}
	all, _, _ := g.ListSyncs(ctx, 10, 0)
	var applied *domain.GitOpsSync
	for i := range all {
		if all[i].Result == "applied" && all[i].Commit != nil && all[i].Commit.SHA == "c1" {
			applied = &all[i]
		}
	}
	if applied == nil || len(applied.Added) != 5 || applied.Issues == nil {
		t.Fatalf("applied sync must carry the change lists: %+v", applied)
	}
	// retention never deletes the newest applied sync
	n, err := g.DeleteSyncsBefore(ctx, time.Now().Add(time.Hour))
	mustNoErr(t, err)
	if n != 3 {
		t.Fatalf("DeleteSyncsBefore deleted %d, want 3 (all but newest applied)", n)
	}
	got, _ = g.GetSnapshot(ctx)
	if got.Commit != "c2" {
		t.Fatal("commit must survive retention")
	}

	// status
	now := time.Now().UTC()
	mustNoErr(t, g.SaveStatus(ctx, domain.GitOpsSyncStatus{Source: "dir:x", State: "ok", LastSyncAt: &now, AppliedCommit: &domain.GitCommit{SHA: "c2", Time: now}}))
	mustNoErr(t, g.SaveStatus(ctx, domain.GitOpsSyncStatus{Source: "dir:x", State: "error", ObjectCounts: map[domain.ObjectKind]int{domain.KindFleet: 2}}))
	st, err = g.GetStatus(ctx)
	mustNoErr(t, err)
	if st.State != "error" || st.ObjectCounts[domain.KindFleet] != 2 || st.Issues == nil {
		t.Fatalf("status: %+v", st)
	}

	// duplicate object names are rejected
	dup := testSnapshot("c4", 4, func(s *domain.GitOpsSnapshot) { s.Fleets = append(s.Fleets, s.Fleets[0]) })
	if _, err := g.ApplySnapshot(ctx, dup, domain.GitOpsSync{}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("duplicate names: %v", err)
	}
	got, _ = g.GetSnapshot(ctx)
	if len(got.Fleets) != 2 {
		t.Fatal("a rejected snapshot must not change the stored one")
	}
}

func TestGitOpsApplyIsAtomic(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	g := db.GitOps()
	_, err := g.ApplySnapshot(ctx, testSnapshot("c1", 1, nil), domain.GitOpsSync{Commit: &domain.GitCommit{SHA: "c1"}})
	mustNoErr(t, err)
	// an invalid object (name too long is fine for text; use a value json cannot marshal)
	bad := testSnapshot("c2", 2, func(s *domain.GitOpsSnapshot) {
		s.Fleets[0].Parameters = map[string]any{"ch": make(chan int)}
	})
	if _, err := g.ApplySnapshot(ctx, bad, domain.GitOpsSync{}); err == nil {
		t.Fatal("unmarshalable snapshot must fail")
	}
	got, _ := g.GetSnapshot(ctx)
	if got.Commit != "c1" || len(got.Fleets) != 1 {
		t.Fatalf("failed apply must leave the previous snapshot: %+v", got.Commit)
	}
}

func newRollout(kind domain.RolloutKind, status domain.RolloutStatus, nWaves int) *domain.Rollout {
	r := &domain.Rollout{ID: uuid.New(), Kind: kind, FleetName: "edge-nl", PolicyName: "careful", Status: status, FromRef: "a", ToRef: "b", CreatedBy: "gitops",
		Policy:      domain.DefaultRolloutPolicy(),
		Annotations: map[string]any{"commit": "abc", "n": 3}}
	for i := 0; i < nWaves; i++ {
		w := domain.RolloutWave{Index: i, Name: fmt.Sprintf("w%d", i), Status: domain.WavePending, RequireApproval: i == 1, Percent: (i + 1) * 100 / nWaves}
		if i == 0 {
			w.Selector = &domain.Selector{MatchAttributes: map[string]string{"labels.pilot": "true"}}
			w.Percent = 0
		}
		r.Waves = append(r.Waves, w)
	}
	return r
}

func targetsFor(n, waves int, toRef string) []domain.RolloutTarget {
	out := make([]domain.RolloutTarget, n)
	for i := range out {
		out[i] = domain.RolloutTarget{AgentID: newID(), Hostname: fmt.Sprintf("h%04d", i), Wave: i % waves, Status: domain.TargetPending, FromRef: "f", ToRef: toRef}
	}
	return out
}

func TestRolloutsCreateGetListCounts(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	r := newRollout(domain.RolloutKindConfig, domain.RolloutPending, 2)
	targets := targetsFor(10, 2, "to")
	targets[0].Status, targets[1].Status, targets[2].Status = domain.TargetInProgress, domain.TargetVerifying, domain.TargetSucceeded
	targets[3].Status, targets[4].Status = domain.TargetFailed, domain.TargetWaitingWindow
	mustNoErr(t, rs.Create(ctx, r, targets))

	got, err := rs.Get(ctx, r.ID)
	mustNoErr(t, err)
	if got.Status != domain.RolloutPending || got.Kind != domain.RolloutKindConfig || got.CreatedBy != "gitops" || got.Policy.Name != domain.DefaultRolloutPolicyName {
		t.Fatalf("rollout: %+v", got)
	}
	if got.Annotations["commit"] != "abc" || got.Annotations["n"].(int) != 3 {
		t.Fatalf("annotations: %#v", got.Annotations)
	}
	if len(got.Waves) != 2 || got.Waves[0].Selector == nil || got.Waves[0].Selector.MatchAttributes["labels.pilot"] != "true" || !got.Waves[1].RequireApproval || got.Waves[1].Selector != nil {
		t.Fatalf("waves: %+v", got.Waves)
	}
	c := got.Counts
	if c.Total != 10 || c.InProgress != 2 || c.Succeeded != 1 || c.Failed != 1 || c.WaitingWindow != 1 || c.Pending != 5 {
		t.Fatalf("counts: %+v", c)
	}
	if got.Waves[0].Counts.Total != 5 || got.Waves[1].Counts.Total != 5 || got.Waves[0].Counts.InProgress != 1 || got.Waves[1].Counts.InProgress != 1 {
		t.Fatalf("wave counts: %+v %+v", got.Waves[0].Counts, got.Waves[1].Counts)
	}
	if _, err := rs.Get(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}

	other := newRollout(domain.RolloutKindVersion, domain.RolloutRunning, 1)
	other.FleetName = "datacenter"
	other.Executor = domain.ExecutorUpdater
	mustNoErr(t, rs.Create(ctx, other, targetsFor(3, 1, "0.163.0")))
	done := newRollout(domain.RolloutKindConfig, domain.RolloutCompleted, 1)
	mustNoErr(t, rs.Create(ctx, done, nil))

	list, total, err := rs.List(ctx, store.RolloutFilter{})
	mustNoErr(t, err)
	if total != 3 || len(list) != 3 {
		t.Fatalf("list: %d/%d", total, len(list))
	}
	for _, l := range list {
		if l.Waves == nil {
			t.Fatal("List must fill waves")
		}
		if l.ID == r.ID && (l.Counts.Total != 10 || len(l.Waves) != 2 || l.Waves[0].Counts.Total != 5) {
			t.Fatalf("List must fill counts: %+v", l)
		}
	}
	active, total, _ := rs.List(ctx, store.RolloutFilter{ActiveOnly: true})
	if total != 2 || len(active) != 2 {
		t.Fatalf("active only: %d", total)
	}
	byFleet, _, _ := rs.List(ctx, store.RolloutFilter{Fleets: []string{"datacenter"}, Kinds: []domain.RolloutKind{domain.RolloutKindVersion}})
	if len(byFleet) != 1 || byFleet[0].ID != other.ID || byFleet[0].Executor != domain.ExecutorUpdater {
		t.Fatalf("filter: %+v", byFleet)
	}
	page, total, _ := rs.List(ctx, store.RolloutFilter{Limit: 1, Offset: 2})
	if total != 3 || len(page) != 1 {
		t.Fatalf("pagination: %d %d", total, len(page))
	}

	// targets
	tl, n, err := rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	mustNoErr(t, err)
	if n != 10 || len(tl) != 10 {
		t.Fatalf("targets: %d %d", n, len(tl))
	}
	w1 := 1
	tl, n, _ = rs.ListTargets(ctx, r.ID, store.TargetFilter{Wave: &w1, Status: []domain.TargetStatus{domain.TargetPending}})
	if n != 3 || len(tl) != 3 {
		t.Fatalf("wave+status filter: %d %d", n, len(tl))
	}
	tl, n, _ = rs.ListTargets(ctx, r.ID, store.TargetFilter{Limit: 3, Offset: 6})
	if n != 10 || len(tl) != 3 {
		t.Fatalf("target pagination: %d %d", n, len(tl))
	}
}

func TestRolloutTargetsActivityOrder(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	r := newRollout(domain.RolloutKindConfig, domain.RolloutRunning, 2)
	type spec struct {
		host   string
		wave   int
		status domain.TargetStatus
	}
	specs := []spec{
		{"WIN-EDGE-01", 0, domain.TargetSucceeded},
		{"WIN-EDGE-02", 0, domain.TargetCancelled},
		{"WIN-EDGE-03", 0, domain.TargetPending},
		{"WIN-EDGE-04", 1, domain.TargetPending},
		{"WIN-EDGE-05", 1, domain.TargetInProgress},
		{"WIN-EDGE-06", 1, domain.TargetVerifying},
		{"WIN-EDGE-07", 0, domain.TargetInProgress},
		{"WIN-EDGE-08", 1, domain.TargetFailed},
		{"WIN-EDGE-09", 0, domain.TargetWaitingWindow},
		{"WIN-EDGE-10", 1, domain.TargetSkipped},
		{"WIN-EDGE-11", 1, domain.TargetRolledBack},
	}
	targets := make([]domain.RolloutTarget, len(specs))
	for i, s := range specs {
		targets[i] = domain.RolloutTarget{AgentID: newID(), Hostname: s.host, Wave: s.wave, Status: s.status, FromRef: "f", ToRef: "to"}
	}
	mustNoErr(t, rs.Create(ctx, r, targets))

	key := func(tl []domain.RolloutTarget) []string {
		out := make([]string, len(tl))
		for i, x := range tl {
			out[i] = fmt.Sprintf("%s/%d/%s", x.Status, x.Wave, x.Hostname)
		}
		return out
	}
	act := store.TargetFilter{Order: store.TargetOrderActivity}
	all, total, err := rs.ListTargets(ctx, r.ID, act)
	mustNoErr(t, err)
	want := []string{
		"in_progress/0/WIN-EDGE-07", "in_progress/1/WIN-EDGE-05", "verifying/1/WIN-EDGE-06",
		"waiting_window/0/WIN-EDGE-09",
		"failed/1/WIN-EDGE-08", "rolled_back/1/WIN-EDGE-11",
		"pending/0/WIN-EDGE-03", "pending/1/WIN-EDGE-04",
		"succeeded/0/WIN-EDGE-01", "skipped/1/WIN-EDGE-10", "cancelled/0/WIN-EDGE-02",
	}
	if total != 11 || !reflect.DeepEqual(key(all), want) {
		t.Fatalf("activity order (total %d):\n got %v\nwant %v", total, key(all), want)
	}

	// pages concatenate to the unpaged list and the total stays constant
	var paged []domain.RolloutTarget
	for off := 0; off < 12; off += 3 {
		f := act
		f.Limit, f.Offset = 3, off
		page, n, err := rs.ListTargets(ctx, r.ID, f)
		mustNoErr(t, err)
		if n != 11 {
			t.Fatalf("offset %d: total %d", off, n)
		}
		paged = append(paged, page...)
	}
	if !reflect.DeepEqual(key(paged), want) {
		t.Fatalf("paged order: %v", key(paged))
	}

	// a status filter keeps the relative order
	f := act
	f.Status = []domain.TargetStatus{domain.TargetPending, domain.TargetInProgress}
	sub, n, err := rs.ListTargets(ctx, r.ID, f)
	mustNoErr(t, err)
	if n != 4 || !reflect.DeepEqual(key(sub), []string{"in_progress/0/WIN-EDGE-07", "in_progress/1/WIN-EDGE-05", "pending/0/WIN-EDGE-03", "pending/1/WIN-EDGE-04"}) {
		t.Fatalf("filtered: %d %v", n, key(sub))
	}

	// the agent filter returns exactly the one target, whatever its position
	f = act
	f.Agent = &all[len(all)-1].AgentID
	one, n, err := rs.ListTargets(ctx, r.ID, f)
	mustNoErr(t, err)
	if n != 1 || len(one) != 1 || one[0].AgentID != all[len(all)-1].AgentID {
		t.Fatalf("agent filter: %d items, total %d", len(one), n)
	}
	other := newID()
	f.Agent = &other
	none, n, err := rs.ListTargets(ctx, r.ID, f)
	mustNoErr(t, err)
	if len(none) != 0 || n != 0 {
		t.Fatalf("unknown agent: %d items, total %d", len(none), n)
	}

	// an offset past the end still reports the total
	f = act
	f.Limit, f.Offset = 3, 40
	empty, n, err := rs.ListTargets(ctx, r.ID, f)
	mustNoErr(t, err)
	if len(empty) != 0 || n != 11 {
		t.Fatalf("past the end: %d items, total %d", len(empty), n)
	}

	// the default order is unchanged: wave, hostname, agent id
	def, _, err := rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	mustNoErr(t, err)
	for i := 1; i < len(def); i++ {
		a, b := def[i-1], def[i]
		if a.Wave > b.Wave || (a.Wave == b.Wave && a.Hostname > b.Hostname) {
			t.Fatalf("default order broken at %d: %v", i, key(def))
		}
	}
}

func TestRolloutLargeCreateUsesCopy(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	r := newRollout(domain.RolloutKindVersion, domain.RolloutPending, 3)
	mustNoErr(t, rs.Create(ctx, r, targetsFor(2500, 3, "0.163.0")))
	got, err := rs.Get(ctx, r.ID)
	mustNoErr(t, err)
	if got.Counts.Total != 2500 || got.Counts.Pending != 2500 {
		t.Fatalf("counts: %+v", got.Counts)
	}
	if got.Waves[0].Counts.Total != 834 || got.Waves[1].Counts.Total != 833 {
		t.Fatalf("wave counts: %+v", got.Waves)
	}
	all, n, err := rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	mustNoErr(t, err)
	if n != 2500 || len(all) != 2500 || all[0].UpdatedAt.IsZero() {
		t.Fatalf("targets %d/%d", n, len(all))
	}
	// duplicate create fails and rolls back everything
	if err := rs.Create(ctx, r, targetsFor(5, 3, "x")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate rollout id: %v", err)
	}
	// duplicate agent in targets (COPY path)
	r2 := newRollout(domain.RolloutKindVersion, domain.RolloutPending, 1)
	tg := targetsFor(1100, 1, "y")
	tg[1099].AgentID = tg[0].AgentID
	if err := rs.Create(ctx, r2, tg); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate target: %v", err)
	}
	if _, err := rs.Get(ctx, r2.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("failed create must be atomic")
	}
}

func TestRolloutCompareAndSetStatusConcurrency(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	r := newRollout(domain.RolloutKindConfig, domain.RolloutPending, 1)
	mustNoErr(t, rs.Create(ctx, r, targetsFor(2, 1, "t")))

	ok, err := rs.CompareAndSetStatus(ctx, r.ID, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutPaused, "no")
	mustNoErr(t, err)
	if ok {
		t.Fatal("CAS with wrong from-state must fail")
	}
	ok, _ = rs.CompareAndSetStatus(ctx, r.ID, []domain.RolloutStatus{domain.RolloutPending, domain.RolloutWaitingApproval}, domain.RolloutRunning, "started")
	got, _ := rs.Get(ctx, r.ID)
	if !ok || got.Status != domain.RolloutRunning || got.StatusReason != "started" || got.StartedAt == nil || got.FinishedAt != nil {
		t.Fatalf("running: ok=%v %+v", ok, got)
	}
	started := *got.StartedAt
	// running again later must not reset started_at
	_, _ = rs.CompareAndSetStatus(ctx, r.ID, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutPaused, "pause")
	_, _ = rs.CompareAndSetStatus(ctx, r.ID, []domain.RolloutStatus{domain.RolloutPaused}, domain.RolloutRunning, "resume")
	got, _ = rs.Get(ctx, r.ID)
	if !got.StartedAt.Equal(started) {
		t.Fatal("started_at must be kept")
	}

	// N concurrent writers race for the same transition: exactly one wins
	var wg sync.WaitGroup
	wins := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := rs.CompareAndSetStatus(ctx, r.ID, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutAborted, fmt.Sprintf("writer %d", i))
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins <- i
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("%d writers won the CAS, want exactly 1", len(wins))
	}
	got, _ = rs.Get(ctx, r.ID)
	if got.Status != domain.RolloutAborted || got.FinishedAt == nil {
		t.Fatalf("terminal state must set finished_at: %+v", got)
	}
	if ok, _ := rs.CompareAndSetStatus(ctx, r.ID, nil, domain.RolloutRunning, ""); ok {
		t.Fatal("empty from list never matches")
	}
}

func TestRolloutUpdateProgressDoesNotTouchStatusOrApproval(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	r := newRollout(domain.RolloutKindConfig, domain.RolloutRunning, 2)
	mustNoErr(t, rs.Create(ctx, r, targetsFor(4, 2, "t")))

	// operator approves wave 1 concurrently with an engine progress write that carries stale approval data
	ok, err := rs.SetWaveApproval(ctx, r.ID, 1, "user:1", time.Now())
	mustNoErr(t, err)
	if !ok {
		t.Fatal("approval of a pending wave must succeed")
	}
	now := time.Now().UTC()
	stale := *r
	stale.Status = domain.RolloutFailed // must be ignored
	stale.StatusReason = "stale"
	stale.CurrentWave = 1
	stale.Annotations = map[string]any{"tick": 7}
	stale.Waves = []domain.RolloutWave{
		{Index: 0, Status: domain.WaveCompleted, StartedAt: &now, FinishedAt: &now},
		{Index: 1, Status: domain.WaveRunning, StartedAt: &now, ApprovedBy: "nobody"}, // must not erase the approval
	}
	mustNoErr(t, rs.UpdateProgress(ctx, &stale))
	got, _ := rs.Get(ctx, r.ID)
	if got.Status != domain.RolloutRunning || got.StatusReason != "" {
		t.Fatalf("UpdateProgress must not write status: %+v", got)
	}
	if got.CurrentWave != 1 || got.Annotations["tick"].(int) != 7 {
		t.Fatalf("progress not written: %+v", got)
	}
	if got.Waves[0].Status != domain.WaveCompleted || got.Waves[1].Status != domain.WaveRunning || got.Waves[0].FinishedAt == nil {
		t.Fatalf("wave progress: %+v", got.Waves)
	}
	if got.Waves[1].ApprovedBy != "user:1" || got.Waves[1].ApprovedAt == nil {
		t.Fatalf("approval must survive UpdateProgress: %+v", got.Waves[1])
	}
	// approval of a running/completed wave is refused
	if ok, _ := rs.SetWaveApproval(ctx, r.ID, 0, "x", time.Now()); ok {
		t.Fatal("approval of a completed wave must fail")
	}
	if ok, _ := rs.SetWaveApproval(ctx, r.ID, 9, "x", time.Now()); ok {
		t.Fatal("approval of an unknown wave must fail")
	}
	if err := rs.UpdateProgress(ctx, &domain.Rollout{ID: uuid.New()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("progress of unknown rollout: %v", err)
	}
}

func TestRolloutTargetOptimisticConcurrencyAndBulkActions(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	r := newRollout(domain.RolloutKindConfig, domain.RolloutRunning, 1)
	tg := targetsFor(6, 1, "t")
	mustNoErr(t, rs.Create(ctx, r, tg))
	list, _, _ := rs.ListTargets(ctx, r.ID, store.TargetFilter{})

	// engine reads, then an operator action changes a row, then the engine writes
	mine := make([]domain.RolloutTarget, len(list))
	copy(mine, list)
	for i := range mine {
		mine[i].Status = domain.TargetInProgress
		started := time.Now().UTC()
		mine[i].StartedAt = &started
		mine[i].ExecutorRef = "job-1"
	}
	cancelled, err := rs.CancelOpenTargets(ctx, r.ID, "aborted by operator") // changes every pending row
	mustNoErr(t, err)
	if cancelled != 6 {
		t.Fatalf("CancelOpenTargets = %d", cancelled)
	}
	conflicts, err := rs.UpdateTargets(ctx, mine)
	mustNoErr(t, err)
	if len(conflicts) != 6 {
		t.Fatalf("all updates must conflict after the concurrent bulk change, got %d", len(conflicts))
	}
	after, _, _ := rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	for _, a := range after {
		if a.Status != domain.TargetCancelled || a.LastError != "aborted by operator" || a.FinishedAt == nil {
			t.Fatalf("conflicting update must not be applied: %+v", a)
		}
	}

	// fresh read -> update succeeds and moves updated_at
	fresh := after[:2]
	fresh[0].Status, fresh[1].Status = domain.TargetFailed, domain.TargetSkipped
	fresh[0].LastError = "boom"
	conflicts, err = rs.UpdateTargets(ctx, fresh)
	mustNoErr(t, err)
	if len(conflicts) != 0 || conflicts == nil {
		t.Fatalf("fresh update conflicted: %v", conflicts)
	}
	again, _, _ := rs.ListTargets(ctx, r.ID, store.TargetFilter{Status: []domain.TargetStatus{domain.TargetFailed}})
	if len(again) != 1 || again[0].LastError != "boom" || !again[0].UpdatedAt.After(fresh[0].UpdatedAt) {
		t.Fatalf("updated target: %+v", again)
	}
	// stale second write to the same row conflicts
	conflicts, _ = rs.UpdateTargets(ctx, fresh[:1])
	if len(conflicts) != 1 {
		t.Fatal("re-using the old updated_at must conflict")
	}

	n, err := rs.ResetFailedTargets(ctx, r.ID)
	mustNoErr(t, err)
	if n != 2 {
		t.Fatalf("ResetFailedTargets = %d (failed + skipped)", n)
	}
	reset, _, _ := rs.ListTargets(ctx, r.ID, store.TargetFilter{Status: []domain.TargetStatus{domain.TargetPending}})
	if len(reset) != 2 || reset[0].Attempts != 1 || reset[0].LastError != "" || reset[0].FinishedAt != nil {
		t.Fatalf("reset targets: %+v", reset)
	}

	// late joiners
	late := targetsFor(3, 1, "t")
	late = append(late, reset[0]) // already a target: ignored
	mustNoErr(t, rs.AddTargets(ctx, r.ID, late))
	_, total, _ := rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	if total != 9 {
		t.Fatalf("AddTargets: total %d", total)
	}
	if c, err := rs.UpdateTargets(ctx, nil); err != nil || c == nil {
		t.Fatalf("empty UpdateTargets: %v %v", c, err)
	}
}

func TestRolloutAgentQueries(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	agent := newID()
	mk := func(kind domain.RolloutKind, status domain.RolloutStatus, toRef string, age time.Duration) *domain.Rollout {
		r := newRollout(kind, status, 1)
		r.ToRef = toRef
		r.CreatedAt = time.Now().Add(-age)
		mustNoErr(t, rs.Create(ctx, r, []domain.RolloutTarget{{AgentID: agent, Hostname: "h", Wave: 0, Status: domain.TargetPending, FromRef: "o", ToRef: toRef}}))
		return r
	}
	oldDone := mk(domain.RolloutKindConfig, domain.RolloutCompleted, "hash-a", 3*time.Hour)
	running := mk(domain.RolloutKindConfig, domain.RolloutRunning, "hash-a", time.Hour)
	version := mk(domain.RolloutKindVersion, domain.RolloutPaused, "0.163.0", 30*time.Minute)

	active, err := rs.ActiveTargetsForAgent(ctx, agent)
	mustNoErr(t, err)
	if len(active) != 2 {
		t.Fatalf("active targets: %+v", active)
	}
	ids := map[uuid.UUID]bool{}
	for _, a := range active {
		ids[a.RolloutID] = true
	}
	if !ids[running.ID] || !ids[version.ID] || ids[oldDone.ID] {
		t.Fatalf("active targets must belong to non-terminal rollouts: %v", ids)
	}
	none, err := rs.ActiveTargetsForAgent(ctx, newID())
	mustNoErr(t, err)
	if none == nil || len(none) != 0 {
		t.Fatal("empty result must be non-nil")
	}

	ref, err := rs.LatestTargetForAgent(ctx, agent, domain.RolloutKindConfig, "hash-a")
	mustNoErr(t, err)
	if ref.Target.RolloutID != running.ID || ref.RolloutStatus != domain.RolloutRunning {
		t.Fatalf("latest config target must be the most recent rollout: %+v", ref)
	}
	ref, err = rs.LatestTargetForAgent(ctx, agent, domain.RolloutKindVersion, "0.163.0")
	mustNoErr(t, err)
	if ref.RolloutStatus != domain.RolloutPaused {
		t.Fatalf("version target: %+v", ref)
	}
	if _, err := rs.LatestTargetForAgent(ctx, agent, domain.RolloutKindConfig, "hash-zzz"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no target for ref: %v", err)
	}
	if _, err := rs.LatestTargetForAgent(ctx, agent, domain.RolloutKindVersion, "hash-a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("kind must be part of the match")
	}

	// retention: terminal rollouts finished long ago are deleted with their targets
	mustNoErr(t, func() error {
		_, err := db.Pool().Exec(ctx, `UPDATE rollouts SET finished_at = now() - interval '200 days' WHERE id = $1`, oldDone.ID)
		return err
	}())
	n, err := rs.DeleteFinishedBefore(ctx, time.Now().Add(-180*24*time.Hour))
	mustNoErr(t, err)
	if n != 1 {
		t.Fatalf("DeleteFinishedBefore = %d", n)
	}
	if _, err := rs.Get(ctx, oldDone.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("rollout must be deleted")
	}
	var left int
	_ = db.Pool().QueryRow(ctx, `SELECT count(*) FROM rollout_targets WHERE rollout_id = $1`, oldDone.ID).Scan(&left)
	if left != 0 {
		t.Fatal("targets must cascade")
	}
	if _, err := rs.Get(ctx, running.ID); err != nil {
		t.Fatal("running rollout must stay")
	}
}

func TestPackageOffers(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ps := db.Packages()
	agent := upsert(t, ctx, db, agentSpec{host: "u", version: "0.1.0", kind: domain.AgentKindUpdater})
	rid := uuid.New()
	offer := domain.PackageOffer{Name: domain.CollectorPackageName, Version: "v0.163.0", DownloadURL: "https://fleet/api/v1/artifacts/x/content/a.tar.gz",
		ContentHash: domain.SHA256([]byte("archive")), Signature: []byte(`{"mediaType":"x"}`), Headers: map[string]string{"Authorization": "Bearer myrd_x"}, RolloutID: &rid}
	got, err := ps.GetOffers(ctx, agent)
	mustNoErr(t, err)
	if got == nil || len(got) != 0 {
		t.Fatal("no offers: empty non-nil slice")
	}
	mustNoErr(t, ps.SetOffers(ctx, agent, []domain.PackageOffer{offer, {Name: "opampsupervisor", Version: "v0.162.0", DownloadURL: "u2", ContentHash: domain.SHA256([]byte("s"))}}))
	got, err = ps.GetOffers(ctx, agent)
	mustNoErr(t, err)
	if len(got) != 2 || got[0].Name != "" || got[1].Name != "opampsupervisor" {
		t.Fatalf("offers ordered by name: %+v", got)
	}
	o := got[0]
	if o.AgentID != agent || o.Version != "v0.163.0" || !o.ContentHash.Equal(offer.ContentHash) || string(o.Signature) != string(offer.Signature) ||
		o.Headers["Authorization"] != "Bearer myrd_x" || o.RolloutID == nil || *o.RolloutID != rid || o.CreatedAt.IsZero() {
		t.Fatalf("offer: %+v", o)
	}
	if got[1].Signature != nil && len(got[1].Signature) != 0 || got[1].Headers != nil && len(got[1].Headers) != 0 {
		t.Fatalf("optional fields: %+v", got[1])
	}
	// the hashes derived from what was stored equal those of what was offered
	if !domain.PackageHash(got[0]).Equal(domain.PackageHash(offer)) {
		t.Fatal("PackageHash must survive the round trip")
	}

	// SetOffers replaces the complete set
	mustNoErr(t, ps.SetOffers(ctx, agent, []domain.PackageOffer{got[1]}))
	got, _ = ps.GetOffers(ctx, agent)
	if len(got) != 1 || got[0].Name != "opampsupervisor" {
		t.Fatalf("replace: %+v", got)
	}
	mustNoErr(t, ps.ClearOffers(ctx, agent))
	got, _ = ps.GetOffers(ctx, agent)
	if len(got) != 0 {
		t.Fatal("ClearOffers")
	}
	if err := ps.SetOffers(ctx, newID(), []domain.PackageOffer{offer}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("offers for unknown agent: %v", err)
	}
	// a failed SetOffers keeps the previous set
	mustNoErr(t, ps.SetOffers(ctx, agent, []domain.PackageOffer{offer}))
	if err := ps.SetOffers(ctx, agent, []domain.PackageOffer{offer, offer}); err == nil {
		t.Fatal("duplicate names must fail")
	}
	got, _ = ps.GetOffers(ctx, agent)
	if len(got) != 1 {
		t.Fatal("failed SetOffers must roll back")
	}
	// offers disappear with the agent
	mustNoErr(t, db.Agents().Delete(ctx, agent))
	var n int
	_ = db.Pool().QueryRow(ctx, `SELECT count(*) FROM package_offers`).Scan(&n)
	if n != 0 {
		t.Fatal("offers must cascade with the agent")
	}
}

func TestReleases(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Releases()
	pub := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	mk := func(v string, pre bool) domain.Release {
		return domain.Release{Distribution: "otelcol-contrib", Version: v, Tag: "v" + v, PublishedAt: &pub, Prerelease: pre, HTMLURL: "https://x/" + v, Notes: "notes " + v,
			BreakingChanges: []domain.ChangelogEntry{{Source: "core", File: "CHANGELOG.md", Version: v, Component: "receiver/x", Text: "t", Issues: []int{1, 2}}},
			Assets:          []domain.ReleaseAsset{{Name: "a.tar.gz", URL: "u", Size: 5, OS: "linux", Arch: "amd64", Format: "tar.gz"}}}
	}
	for _, v := range []string{"0.99.0", "0.100.0", "0.162.0", "0.162.1", "0.9.0"} {
		created, err := rs.Upsert(ctx, mk(v, false))
		mustNoErr(t, err)
		if !created {
			t.Fatalf("%s must be created", v)
		}
	}
	_, err := rs.Upsert(ctx, mk("0.163.0", true))
	mustNoErr(t, err)
	_, err = rs.Upsert(ctx, domain.Release{Distribution: "opampsupervisor", Version: "0.162.0", Tag: "cmd/opampsupervisor/v0.162.0"})
	mustNoErr(t, err)

	list, total, err := rs.List(ctx, store.ReleaseFilter{Distribution: "otelcol-contrib"})
	mustNoErr(t, err)
	var vs []string
	for _, r := range list {
		vs = append(vs, r.Version)
	}
	if total != 5 || fmt.Sprint(vs) != "[0.162.1 0.162.0 0.100.0 0.99.0 0.9.0]" {
		t.Fatalf("semver ordering (not lexical!): %v total %d", vs, total)
	}
	withPre, _, _ := rs.List(ctx, store.ReleaseFilter{Distribution: "otelcol-contrib", IncludePrerelease: true, Limit: 1})
	if len(withPre) != 1 || withPre[0].Version != "0.163.0" {
		t.Fatalf("prerelease: %+v", withPre)
	}
	since, n, _ := rs.List(ctx, store.ReleaseFilter{Distribution: "otelcol-contrib", SinceVersion: "0.99.0"})
	if n != 3 || len(since) != 3 {
		t.Fatalf("SinceVersion: %d", n)
	}
	latest, err := rs.Latest(ctx, "otelcol-contrib", false)
	mustNoErr(t, err)
	if latest.Version != "0.162.1" {
		t.Fatalf("latest stable: %s", latest.Version)
	}
	latest, _ = rs.Latest(ctx, "otelcol-contrib", true)
	if latest.Version != "0.163.0" {
		t.Fatalf("latest incl. prerelease: %s", latest.Version)
	}
	if _, err := rs.Latest(ctx, "nothing", false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}

	r, err := rs.Get(ctx, "otelcol-contrib", "v0.162.0") // leading v tolerated
	mustNoErr(t, err)
	if len(r.BreakingChanges) != 1 || r.BreakingChanges[0].Issues[1] != 2 || len(r.Assets) != 1 || r.Deprecations == nil || r.PublishedAt == nil || !r.PublishedAt.Equal(pub) {
		t.Fatalf("release: %+v", r)
	}
	sup, _ := rs.Get(ctx, "opampsupervisor", "0.162.0")
	if sup.BreakingChanges == nil || sup.Assets == nil || sup.Deprecations == nil {
		t.Fatalf("lists must be non-nil: %+v", sup)
	}

	// upsert updates content but keeps acknowledgement and detection time
	at := time.Now().UTC()
	mustNoErr(t, rs.Acknowledge(ctx, "otelcol-contrib", "0.162.0", "user:1", at))
	upd := mk("0.162.0", false)
	upd.Notes = "edited"
	upd.BreakingChanges = nil
	created, err := rs.Upsert(ctx, upd)
	mustNoErr(t, err)
	if created {
		t.Fatal("second upsert must not report created")
	}
	r2, _ := rs.Get(ctx, "otelcol-contrib", "0.162.0")
	if r2.Notes != "edited" || len(r2.BreakingChanges) != 0 || r2.AcknowledgedBy != "user:1" || r2.AcknowledgedAt == nil || !r2.DetectedAt.Equal(r.DetectedAt) {
		t.Fatalf("upsert semantics: %+v", r2)
	}
	if err := rs.Acknowledge(ctx, "otelcol-contrib", "9.9.9", "u", at); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := rs.Upsert(ctx, domain.Release{Distribution: "x", Version: "not-semver", Tag: "t"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("non-semver version: %v", err)
	}
}

func TestArtifacts(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	as := db.Artifacts()
	a := domain.Artifact{ID: uuid.New(), Distribution: "otelcol-contrib", Version: "0.162.0", OS: "linux", Arch: "amd64", Format: "tar.gz",
		Filename: "otelcol-contrib_0.162.0_linux_amd64.tar.gz", SourceURL: "https://x", Status: domain.ArtifactDownloading}
	got, err := as.Upsert(ctx, a)
	mustNoErr(t, err)
	if got.ID != a.ID || got.Status != domain.ArtifactDownloading || got.SigstoreBundle != nil && len(got.SigstoreBundle) != 0 {
		t.Fatalf("insert: %+v", got)
	}
	// upsert by unique key keeps the id of the existing row
	b := a
	b.ID = uuid.New()
	b.Status, b.Size, b.SHA256, b.SigstoreBundle, b.SignatureVerified, b.SignatureIdentity = domain.ArtifactReady, 112, domain.SHA256([]byte("x")), []byte(`{"b":1}`), true, "id"
	got2, err := as.Upsert(ctx, b)
	mustNoErr(t, err)
	if got2.ID != a.ID || got2.Status != domain.ArtifactReady || got2.Size != 112 || !got2.SignatureVerified || string(got2.SigstoreBundle) != `{"b":1}` || !got2.SHA256.Equal(b.SHA256) {
		t.Fatalf("upsert: %+v", got2)
	}
	if !got2.CreatedAt.Equal(got.CreatedAt) {
		t.Fatal("created_at must be kept")
	}
	f, err := as.Find(ctx, "otelcol-contrib", "v0.162.0", "linux", "amd64", "tar.gz")
	mustNoErr(t, err)
	if f.ID != a.ID {
		t.Fatal("Find")
	}
	if _, err := as.Find(ctx, "otelcol-contrib", "0.162.0", "windows", "amd64", "tar.gz"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	w := a
	w.ID, w.OS, w.Status = uuid.Nil, "windows", domain.ArtifactFailed
	w.Error = "boom"
	w2, err := as.Upsert(ctx, w)
	mustNoErr(t, err)
	if w2.ID == uuid.Nil {
		t.Fatal("ID must be generated when missing")
	}
	l, n, err := as.List(ctx, store.ArtifactFilter{Distribution: "otelcol-contrib", Status: []domain.ArtifactStatus{domain.ArtifactFailed}})
	mustNoErr(t, err)
	if n != 1 || len(l) != 1 || l[0].Error != "boom" {
		t.Fatalf("list: %d %+v", n, l)
	}
	all, n, _ := as.List(ctx, store.ArtifactFilter{Version: "0.162.0", Limit: 1})
	if n != 2 || len(all) != 1 {
		t.Fatalf("list paging: %d %d", n, len(all))
	}
	mustNoErr(t, as.Delete(ctx, a.ID))
	if err := as.Delete(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := as.Get(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestIdentity(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	is := db.Identity()

	// users: mapping role is refreshed, manual role survives
	u, err := is.UpsertUser(ctx, domain.User{Subject: "oidc|1", Provider: "oidc", Email: "a@x", Name: "A", Role: domain.RoleViewer})
	mustNoErr(t, err)
	if u.ID == uuid.Nil || u.Role != domain.RoleViewer || u.RoleSource != "mapping" || u.Disabled {
		t.Fatalf("user: %+v", u)
	}
	u2, err := is.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "oidc|1", Provider: "oidc", Email: "new@x", Name: "A2", Role: domain.RoleOperator})
	mustNoErr(t, err)
	if u2.ID != u.ID || u2.Email != "new@x" || u2.Name != "A2" || u2.Role != domain.RoleOperator {
		t.Fatalf("mapping update must refresh role and keep id: %+v", u2)
	}
	mustNoErr(t, is.SetUserRole(ctx, u.ID, domain.RoleAdmin, "manual"))
	u3, _ := is.UpsertUser(ctx, domain.User{Subject: "oidc|1", Provider: "oidc", Email: "n3@x", Name: "A3", Role: domain.RoleViewer})
	if u3.Role != domain.RoleAdmin || u3.RoleSource != "manual" || u3.Email != "n3@x" {
		t.Fatalf("manual role must survive login: %+v", u3)
	}
	if err := is.SetUserRole(ctx, u.ID, "root", "manual"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid role: %v", err)
	}
	mustNoErr(t, is.SetUserDisabled(ctx, u.ID, true))
	mustNoErr(t, is.TouchUserLogin(ctx, u.ID, time.Now()))
	got, _ := is.GetUser(ctx, u.ID)
	if !got.Disabled || got.LastLoginAt == nil {
		t.Fatalf("user flags: %+v", got)
	}
	if _, err := is.GetUser(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if err := is.SetUserDisabled(ctx, uuid.New(), true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	users, err := is.ListUsers(ctx)
	mustNoErr(t, err)
	if len(users) != 1 {
		t.Fatalf("users: %d", len(users))
	}

	// sessions with sliding expiry
	now := time.Now().UTC().Truncate(time.Microsecond)
	sess := domain.Session{ID: "abc", UserID: u.ID, CSRFToken: "csrf", ExpiresAt: now.Add(12 * time.Hour), UserAgent: "ua", RemoteAddr: "1.2.3.4", CreatedAt: now}
	mustNoErr(t, is.CreateSession(ctx, sess))
	gs, err := is.GetSession(ctx, "abc")
	mustNoErr(t, err)
	if gs.CSRFToken != "csrf" || gs.UserID != u.ID || !gs.ExpiresAt.Equal(sess.ExpiresAt) || !gs.LastSeenAt.Equal(now) {
		t.Fatalf("session: %+v", gs)
	}
	mustNoErr(t, is.TouchSession(ctx, "abc", now.Add(3*time.Hour), 0))
	gs, _ = is.GetSession(ctx, "abc")
	if !gs.LastSeenAt.Equal(now.Add(3*time.Hour)) || !gs.ExpiresAt.Equal(now.Add(15*time.Hour)) {
		t.Fatalf("sliding expiry must keep the 12h lifetime: last_seen=%v expires=%v", gs.LastSeenAt, gs.ExpiresAt)
	}
	mustNoErr(t, is.TouchSession(ctx, "abc", now.Add(time.Hour), 0)) // older touch: ignored
	gs, _ = is.GetSession(ctx, "abc")
	if !gs.ExpiresAt.Equal(now.Add(15 * time.Hour)) {
		t.Fatal("touch must never shorten the session")
	}
	mustNoErr(t, is.CreateSession(ctx, domain.Session{ID: "old", UserID: u.ID, CSRFToken: "c", ExpiresAt: now.Add(-time.Hour)}))
	n, err := is.DeleteExpiredSessions(ctx, now)
	mustNoErr(t, err)
	if n != 1 {
		t.Fatalf("expired sessions: %d", n)
	}
	mustNoErr(t, is.DeleteSession(ctx, "abc"))
	if _, err := is.GetSession(ctx, "abc"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if err := is.CreateSession(ctx, domain.Session{ID: "x", UserID: uuid.New(), CSRFToken: "c", ExpiresAt: now}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("session for unknown user (FK): %v", err)
	}

	// api tokens
	hash := domain.SHA256([]byte("myra_secret"))
	tok := domain.APIToken{ID: uuid.New(), Name: "ci", DisplayPrefix: "myra_secret", Hash: hash, Role: domain.RoleOperator, CreatedBy: "user:1"}
	mustNoErr(t, is.CreateAPIToken(ctx, tok))
	if err := is.CreateAPIToken(ctx, domain.APIToken{ID: uuid.New(), Name: "dup", Hash: hash, Role: domain.RoleViewer, CreatedBy: "x"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate token hash: %v", err)
	}
	gt, err := is.GetAPITokenByHash(ctx, hash)
	mustNoErr(t, err)
	if gt.ID != tok.ID || gt.Role != domain.RoleOperator || gt.RevokedAt != nil || gt.ExpiresAt != nil {
		t.Fatalf("token: %+v", gt)
	}
	mustNoErr(t, is.TouchAPIToken(ctx, tok.ID, now))
	first := now.Add(time.Minute)
	mustNoErr(t, is.RevokeAPIToken(ctx, tok.ID, first))
	mustNoErr(t, is.RevokeAPIToken(ctx, tok.ID, first.Add(time.Hour)))
	gt, _ = is.GetAPITokenByHash(ctx, hash)
	if gt.RevokedAt == nil || !gt.RevokedAt.Equal(first) || gt.LastUsedAt == nil {
		t.Fatalf("revocation keeps the first timestamp: %+v", gt)
	}
	if err := is.RevokeAPIToken(ctx, uuid.New(), now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := is.GetAPITokenByHash(ctx, domain.SHA256([]byte("none"))); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	toks, _ := is.ListAPITokens(ctx)
	if len(toks) != 1 {
		t.Fatal("ListAPITokens")
	}

	// enrollment tokens
	eh := domain.SHA256([]byte("myre_x"))
	exp := now.Add(24 * time.Hour)
	et := domain.EnrollmentToken{ID: uuid.New(), Name: "sites", DisplayPrefix: "myre_x", Hash: eh, Labels: map[string]string{"channel": "edge-nl"}, CreatedBy: "user:1", ExpiresAt: &exp}
	mustNoErr(t, is.CreateEnrollmentToken(ctx, et))
	mustNoErr(t, is.TouchEnrollmentToken(ctx, et.ID, now, 1))
	mustNoErr(t, is.TouchEnrollmentToken(ctx, et.ID, now.Add(time.Second), 1))
	ge, err := is.GetEnrollmentTokenByHash(ctx, eh)
	mustNoErr(t, err)
	if ge.UseCount != 2 || ge.Labels["channel"] != "edge-nl" || ge.ExpiresAt == nil || !ge.ExpiresAt.Equal(exp) || ge.LastUsedAt == nil || !ge.LastUsedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("enrollment token: %+v", ge)
	}
	mustNoErr(t, is.CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: uuid.New(), Name: "nolabels", Hash: domain.SHA256([]byte("e2")), CreatedBy: "x"}))
	ets, _ := is.ListEnrollmentTokens(ctx)
	if len(ets) != 2 {
		t.Fatal("ListEnrollmentTokens")
	}
	for _, e := range ets {
		if e.Labels == nil {
			t.Fatal("labels must be a non-nil map")
		}
	}
	mustNoErr(t, is.RevokeEnrollmentToken(ctx, et.ID, now))
	ge, _ = is.GetEnrollmentTokenByHash(ctx, eh)
	if ge.RevokedAt == nil {
		t.Fatal("revoke")
	}

	// saved filters: own + shared
	other, _ := is.UpsertUser(ctx, domain.User{Subject: "oidc|2", Provider: "oidc", Role: domain.RoleViewer})
	mk := func(owner uuid.UUID, name string, shared bool) domain.SavedFilter {
		f := domain.SavedFilter{ID: uuid.New(), UserID: owner, Name: name, Page: "agents", Query: map[string]any{"status": []any{"offline"}, "n": 3}, Shared: shared}
		mustNoErr(t, is.CreateSavedFilter(ctx, f))
		return f
	}
	mine := mk(u.ID, "b-mine", false)
	mk(u.ID, "a-shared", true)
	mk(other.ID, "theirs-private", false)
	mk(other.ID, "c-theirs-shared", true)
	if err := is.CreateSavedFilter(ctx, domain.SavedFilter{ID: uuid.New(), UserID: u.ID, Name: "b-mine", Page: "agents", Query: map[string]any{}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate filter name: %v", err)
	}
	fl, err := is.ListSavedFilters(ctx, u.ID, "agents")
	mustNoErr(t, err)
	var names []string
	for _, f := range fl {
		names = append(names, f.Name)
	}
	if fmt.Sprint(names) != "[a-shared b-mine c-theirs-shared]" {
		t.Fatalf("own+shared sorted by name, no foreign private: %v", names)
	}
	if fl[1].Query["n"].(int) != 3 {
		t.Fatalf("query ints: %#v", fl[1].Query)
	}
	none, _ := is.ListSavedFilters(ctx, u.ID, "rollouts")
	if none == nil || len(none) != 0 {
		t.Fatal("other page: empty non-nil")
	}
	if err := is.DeleteSavedFilter(ctx, other.ID, mine.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleting a foreign filter: %v", err)
	}
	mustNoErr(t, is.DeleteSavedFilter(ctx, u.ID, mine.ID))
}

func TestAuditReplicasKV(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	as := db.Audit()
	base := time.Now().UTC().Add(-time.Hour)
	for i, a := range []string{"rollout.pause", "rollout.abort", "token.create", "agent.restart", "rollout_x.other"} {
		mustNoErr(t, as.Append(ctx, domain.AuditEvent{Time: base.Add(time.Duration(i) * time.Minute), ActorType: domain.ActorUser, ActorID: "u1", ActorName: "Una", Action: a,
			TargetType: "rollout", TargetID: fmt.Sprint(i), Details: map[string]any{"n": i, "bad": "x\x00y"}, SourceIP: "1.1.1.1", RequestID: "r"}))
	}
	mustNoErr(t, as.Append(ctx, domain.AuditEvent{ActorType: domain.ActorSystem, ActorID: "system", Action: "rollout.start", Result: "failure"}))
	list, total, err := as.List(ctx, store.AuditFilter{Action: "rollout.*"})
	mustNoErr(t, err)
	if total != 3 || len(list) != 3 {
		t.Fatalf("prefix filter must not match rollout_x (LIKE escaping): %d %+v", total, list)
	}
	if list[0].Action != "rollout.start" || list[0].Result != "failure" || list[1].Action != "rollout.abort" {
		t.Fatalf("newest first: %+v", list)
	}
	if d := list[1].Details; d["n"].(int) != 1 || list[1].SourceIP != "1.1.1.1" || list[1].ActorName != "Una" {
		t.Fatalf("details: %#v", list[1])
	}
	exact, _, _ := as.List(ctx, store.AuditFilter{Action: "token.create"})
	if len(exact) != 1 {
		t.Fatal("exact match")
	}
	from, to := base.Add(90*time.Second), base.Add(150*time.Second)
	rng, n, _ := as.List(ctx, store.AuditFilter{From: &from, To: &to})
	if n != 1 || rng[0].Action != "token.create" {
		t.Fatalf("time range: %d %+v", n, rng)
	}
	byTarget, _, _ := as.List(ctx, store.AuditFilter{TargetType: "rollout", TargetID: "3"})
	byActor, na, _ := as.List(ctx, store.AuditFilter{ActorID: "system"})
	if len(byTarget) != 1 || na != 1 || len(byActor) != 1 {
		t.Fatal("target/actor filters")
	}
	if _, n, _ := as.List(ctx, store.AuditFilter{ActorID: "u1"}); n != 5 {
		t.Fatalf("actor by id: %d", n)
	}
	if _, n, _ := as.List(ctx, store.AuditFilter{ActorID: "una"}); n != 5 {
		t.Fatalf("actor by display name (case-insensitive): %d", n)
	}
	pg, n, _ := as.List(ctx, store.AuditFilter{Limit: 2, Offset: 4})
	if n != 6 || len(pg) != 2 {
		t.Fatalf("paging: %d %d", n, len(pg))
	}
	pg, n, _ = as.List(ctx, store.AuditFilter{Limit: 2, Offset: 40})
	if n != 6 || len(pg) != 0 {
		t.Fatalf("offset beyond end must still report the total: %d %d", n, len(pg))
	}
	del, err := as.DeleteBefore(ctx, base.Add(150*time.Second))
	mustNoErr(t, err)
	if del != 3 {
		t.Fatalf("DeleteBefore: %d", del)
	}

	// replicas
	rs := db.Replicas()
	mustNoErr(t, rs.Heartbeat(ctx, domain.Replica{ID: "r-a", Hostname: "h", Version: "1", StartedAt: base, IsLeader: true, Connections: 5}))
	mustNoErr(t, rs.Heartbeat(ctx, domain.Replica{ID: "r-b", Hostname: "h2", Version: "1", StartedAt: base, LastHeartbeatAt: time.Now().Add(-5 * time.Minute)}))
	mustNoErr(t, rs.Heartbeat(ctx, domain.Replica{ID: "r-a", Hostname: "h", Version: "2", StartedAt: time.Now(), IsLeader: false, Connections: 7}))
	rl, err := rs.List(ctx)
	mustNoErr(t, err)
	if len(rl) != 2 || rl[0].ID != "r-a" || rl[0].Version != "2" || rl[0].Connections != 7 || rl[0].IsLeader {
		t.Fatalf("replicas: %+v", rl)
	}
	if !rl[0].StartedAt.Equal(base.Truncate(time.Microsecond)) {
		t.Fatalf("started_at must be kept on heartbeat: %v vs %v", rl[0].StartedAt, base)
	}
	if time.Since(rl[0].LastHeartbeatAt) > time.Minute {
		t.Fatal("zero LastHeartbeatAt must use the database clock")
	}
	stale, err := rs.DeleteStale(ctx, time.Now().Add(-30*time.Second))
	mustNoErr(t, err)
	if len(stale) != 1 || stale[0] != "r-b" {
		t.Fatalf("stale: %v", stale)
	}
	none, _ := rs.DeleteStale(ctx, time.Now().Add(-30*time.Second))
	if none == nil || len(none) != 0 {
		t.Fatal("non-nil empty")
	}

	// kv
	kv := db.KV()
	if _, err := kv.Get(ctx, "k"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	mustNoErr(t, kv.Put(ctx, "k", []byte("v1")))
	mustNoErr(t, kv.Put(ctx, "k", []byte("v2")))
	v, _ := kv.Get(ctx, "k")
	if string(v) != "v2" {
		t.Fatalf("kv put overwrites: %s", v)
	}
	ok, err := kv.PutIfAbsent(ctx, "k", []byte("v3"))
	mustNoErr(t, err)
	if ok {
		t.Fatal("PutIfAbsent on existing key")
	}
	ok, _ = kv.PutIfAbsent(ctx, "k2", []byte("x"))
	if !ok {
		t.Fatal("PutIfAbsent on new key")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := kv.PutIfAbsent(ctx, "race", []byte("x")); ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("PutIfAbsent race: %d winners", winners)
	}
	mustNoErr(t, kv.Delete(ctx, "k"))
	mustNoErr(t, kv.Delete(ctx, "k"))
	empty, err := func() ([]byte, error) { mustNoErr(t, kv.Put(ctx, "empty", nil)); return kv.Get(ctx, "empty") }()
	mustNoErr(t, err)
	if empty == nil || len(empty) != 0 {
		t.Fatal("empty value round trip")
	}
}

func TestTerminalStatusListMatchesDomain(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	// every status is inserted as a rollout; ActiveOnly must equal !IsTerminal
	all := []domain.RolloutStatus{domain.RolloutPending, domain.RolloutRunning, domain.RolloutWaitingApproval, domain.RolloutPaused, domain.RolloutCompleted,
		domain.RolloutFailed, domain.RolloutAborted, domain.RolloutRollingBack, domain.RolloutRolledBack, domain.RolloutSuperseded}
	for _, s := range all {
		mustNoErr(t, db.Rollouts().Create(ctx, newRollout(domain.RolloutKindConfig, s, 1), nil))
	}
	active, _, err := db.Rollouts().List(ctx, store.RolloutFilter{ActiveOnly: true, Limit: 100})
	mustNoErr(t, err)
	for _, r := range active {
		if r.Status.IsTerminal() {
			t.Errorf("status %s is terminal but listed as active", r.Status)
		}
	}
	want := 0
	for _, s := range all {
		if !s.IsTerminal() {
			want++
		}
	}
	if len(active) != want {
		t.Fatalf("active = %d, want %d: terminal status list in SQL out of sync with domain.RolloutStatus.IsTerminal", len(active), want)
	}
	_ = context.Background
}

// RolloutStore.List without a limit returns 50 rows in PostgreSQL; consumers
// that need every active rollout (the engine tick) must see all of them.
func TestListActiveRolloutsPagesThroughEverything(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	for i := 0; i < 120; i++ {
		mustNoErr(t, db.Rollouts().Create(ctx, newRollout(domain.RolloutKindConfig, domain.RolloutRunning, 1), nil))
	}
	mustNoErr(t, db.Rollouts().Create(ctx, newRollout(domain.RolloutKindConfig, domain.RolloutCompleted, 1), nil))
	page, total, err := db.Rollouts().List(ctx, store.RolloutFilter{ActiveOnly: true})
	mustNoErr(t, err)
	if len(page) != 50 || total != 120 {
		t.Fatalf("documented default page: %d of %d", len(page), total)
	}
	all, err := store.ListActiveRollouts(ctx, db.Rollouts())
	mustNoErr(t, err)
	seen := map[string]bool{}
	for _, r := range all {
		seen[r.ID.String()] = true
	}
	if len(all) != 120 || len(seen) != 120 {
		t.Fatalf("ListActiveRollouts returned %d rows (%d distinct), want 120", len(all), len(seen))
	}
}
