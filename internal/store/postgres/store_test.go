// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

func newDB(t testing.TB) *postgres.DB {
	t.Helper()
	return postgres.New(pgtest.NewPool(t), nil)
}

func ctxT(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustNoErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func newID() domain.AgentID { return domain.AgentID(uuid.Must(uuid.NewV7())) }

// agentSpec describes a test agent.
type agentSpec struct {
	id       domain.AgentID
	host     string
	version  string
	os       string
	kind     domain.AgentKind
	site     string
	siteType string
	country  string
	labels   map[string]string
}

func (a agentSpec) update() store.AgentDescriptionUpdate {
	kind := a.kind
	if kind == "" {
		kind = domain.AgentKindCollector
	}
	os := a.os
	if os == "" {
		os = "linux"
	}
	non := map[string]string{"os.type": os, "host.name": a.host, "host.arch": "amd64"}
	if a.site != "" {
		non["site.id"] = a.site
	}
	if a.siteType != "" {
		non["site.type"] = a.siteType
	}
	if a.country != "" {
		non["site.country"] = a.country
	}
	return store.AgentDescriptionUpdate{
		Kind: kind, ServiceName: "otelcol-contrib", Version: a.version, Hostname: a.host, OSType: os, Arch: "amd64",
		IdentifyingAttributes:    map[string]string{"service.name": "otelcol-contrib", "service.version": a.version},
		NonIdentifyingAttributes: non, Capabilities: domain.CapAcceptsRemoteConfig | domain.CapReportsHealth,
		InitialLabels: a.labels,
	}
}

func upsert(t testing.TB, ctx context.Context, db *postgres.DB, a agentSpec) domain.AgentID {
	t.Helper()
	if a.id == (domain.AgentID{}) {
		a.id = newID()
	}
	_, _, err := db.Agents().UpsertDescription(ctx, a.id, a.update())
	mustNoErr(t, err)
	return a.id
}

func TestAgentUpsertAndHydration(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	id := newID()
	tokenID := uuid.New()
	u := agentSpec{id: id, host: "edge-1001", version: "0.162.0", os: "windows", site: "1001", siteType: "edge", labels: map[string]string{"channel": "edge-nl"}}.update()
	u.EnrollmentTokenID = &tokenID

	a, created, err := ag.UpsertDescription(ctx, id, u)
	mustNoErr(t, err)
	if !created {
		t.Fatal("first upsert must report created")
	}
	if a.Hostname != "edge-1001" || a.Version != "0.162.0" || a.Kind != domain.AgentKindCollector || a.OSType != "windows" {
		t.Fatalf("unexpected agent %+v", a)
	}
	if a.Labels["channel"] != "edge-nl" || a.EnrollmentTokenID == nil || *a.EnrollmentTokenID != tokenID {
		t.Fatalf("labels/token not stored: %+v", a)
	}
	if a.Connection.Status != domain.ConnectionOffline || a.Health != nil || a.Updater != nil || a.PackageStatus != nil {
		t.Fatalf("fresh agent must be offline without health/updater: %+v", a)
	}
	if a.Capabilities != domain.CapAcceptsRemoteConfig|domain.CapReportsHealth {
		t.Fatalf("capabilities %d", a.Capabilities)
	}
	if a.IdentifyingAttributes == nil || a.NonIdentifyingAttributes["site.id"] != "1001" {
		t.Fatalf("attributes: %+v", a)
	}
	if a.FirstSeenAt.Location() != time.UTC {
		t.Fatalf("timestamps must be UTC, got %v", a.FirstSeenAt.Location())
	}

	// second upsert: updates description, keeps labels (InitialLabels only on insert)
	u2 := u
	u2.Version = "0.163.0"
	u2.InitialLabels = map[string]string{"channel": "other"}
	u2.EnrollmentTokenID = nil
	a2, created, err := ag.UpsertDescription(ctx, id, u2)
	mustNoErr(t, err)
	if created {
		t.Fatal("second upsert must not report created")
	}
	if a2.Version != "0.163.0" || a2.Labels["channel"] != "edge-nl" {
		t.Fatalf("update semantics broken: %+v", a2)
	}
	if a2.EnrollmentTokenID == nil || *a2.EnrollmentTokenID != tokenID {
		t.Fatal("enrollment token id must be kept when the update carries none")
	}
	if !a2.FirstSeenAt.Equal(a.FirstSeenAt) {
		t.Fatal("first_seen_at must not change")
	}

	if _, err := ag.Get(ctx, newID()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get of unknown id: %v", err)
	}
	many, err := ag.GetMany(ctx, []domain.AgentID{id, newID()})
	mustNoErr(t, err)
	if len(many) != 1 || many[0].ID != id {
		t.Fatalf("GetMany must ignore missing ids: %+v", many)
	}
	empty, err := ag.GetMany(ctx, nil)
	mustNoErr(t, err)
	if empty == nil || len(empty) != 0 {
		t.Fatal("GetMany(nil) must return an empty non-nil slice")
	}
}

func TestAgentVersionParsingForOutdated(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	for i, v := range []string{"0.160.0", "0.162.0", "0.162.1", "garbage", "v0.161.5", "0.163.0-rc1"} {
		upsert(t, ctx, db, agentSpec{host: fmt.Sprintf("h%d", i), version: v})
	}
	upsert(t, ctx, db, agentSpec{host: "updater", version: "0.1.0", kind: domain.AgentKindUpdater})

	below, err := db.Agents().List(ctx, store.AgentQuery{Filter: store.AgentFilter{OutdatedBelow: "0.162.0"}})
	mustNoErr(t, err)
	hosts := map[string]bool{}
	for _, a := range below.Items {
		hosts[a.Hostname] = true
	}
	if !hosts["h0"] || !hosts["h4"] || len(hosts) != 2 {
		t.Fatalf("OutdatedBelow 0.162.0 must match 0.160.0 and 0.161.5 only (no unparsable, no updater): %v", hosts)
	}
	from, err := db.Agents().List(ctx, store.AgentQuery{Filter: store.AgentFilter{UpToDateFrom: "0.162.0"}})
	mustNoErr(t, err)
	if from.Total != 4 { // 0.162.0, 0.162.1, 0.163.0-rc1 ... and nothing else
		// 0.162.0, 0.162.1, 0.163.0-rc1 => 3
		if from.Total != 3 {
			t.Fatalf("UpToDateFrom total = %d", from.Total)
		}
	}
	if _, err := db.Agents().List(ctx, store.AgentQuery{Filter: store.AgentFilter{OutdatedBelow: "bogus"}}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid version must be ErrInvalidArgument, got %v", err)
	}
}

func TestAgentConnectionAndLastSeen(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	id := upsert(t, ctx, db, agentSpec{host: "a", version: "0.162.0"})

	if err := ag.SetConnection(ctx, newID(), domain.AgentConnection{Status: domain.ConnectionOnline}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetConnection for unknown agent: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustNoErr(t, ag.SetConnection(ctx, id, domain.AgentConnection{
		Status: domain.ConnectionOnline, ReplicaID: "r1", Transport: "websocket", RemoteAddr: "10.0.0.1", ConnectedAt: &now, LastSeenAt: &now,
	}))
	a, _ := ag.Get(ctx, id)
	if !a.IsOnline() || a.Connection.ReplicaID != "r1" || a.Connection.Transport != "websocket" || a.Connection.ConnectedAt == nil || !a.Connection.ConnectedAt.Equal(now) {
		t.Fatalf("connection: %+v", a.Connection)
	}

	// nil timestamp pointers leave the columns unchanged
	later := now.Add(time.Minute)
	mustNoErr(t, ag.SetConnection(ctx, id, domain.AgentConnection{Status: domain.ConnectionOnline, ReplicaID: "r1", Transport: "websocket", DisconnectedAt: &later}))
	a, _ = ag.Get(ctx, id)
	if a.Connection.ConnectedAt == nil || !a.Connection.ConnectedAt.Equal(now) || a.Connection.LastSeenAt == nil || !a.Connection.LastSeenAt.Equal(now) {
		t.Fatalf("nil pointers must keep columns: %+v", a.Connection)
	}

	// batched last seen only moves forward and ignores unknown ids
	mustNoErr(t, ag.TouchLastSeen(ctx, []domain.AgentID{id, newID()}, later))
	a, _ = ag.Get(ctx, id)
	if !a.Connection.LastSeenAt.Equal(later) {
		t.Fatalf("last_seen = %v, want %v", a.Connection.LastSeenAt, later)
	}
	mustNoErr(t, ag.TouchLastSeen(ctx, []domain.AgentID{id}, now))
	a, _ = ag.Get(ctx, id)
	if !a.Connection.LastSeenAt.Equal(later) {
		t.Fatal("last_seen_at must never move backwards")
	}
	mustNoErr(t, ag.TouchLastSeen(ctx, nil, now))

	// conditional offline (stale connection of another replica must not win)
	if err := ag.SetConnection(ctx, id, domain.AgentConnection{Status: domain.ConnectionOffline, ReplicaID: "r2", DisconnectedAt: &later}); err != nil {
		t.Fatal(err)
	}
	a, _ = ag.Get(ctx, id)
	if !a.IsOnline() || a.Connection.ReplicaID != "r1" {
		t.Fatalf("offline write from replica r2 must not override r1's connection: %+v", a.Connection)
	}
	mustNoErr(t, ag.SetConnection(ctx, id, domain.AgentConnection{Status: domain.ConnectionOffline, ReplicaID: "r1", DisconnectedAt: &later}))
	a, _ = ag.Get(ctx, id)
	if a.IsOnline() || a.Connection.ReplicaID != "" || a.Connection.DisconnectedAt == nil {
		t.Fatalf("offline write from owning replica: %+v", a.Connection)
	}
	if err := ag.SetConnection(ctx, newID(), domain.AgentConnection{Status: domain.ConnectionOffline, ReplicaID: "r1"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("conditional offline for unknown agent: %v", err)
	}
}

func TestAgentOfflineSweeps(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	now := time.Now().UTC()
	mk := func(host, replica string, seen time.Time) domain.AgentID {
		id := upsert(t, ctx, db, agentSpec{host: host, version: "0.162.0"})
		mustNoErr(t, ag.SetConnection(ctx, id, domain.AgentConnection{Status: domain.ConnectionOnline, ReplicaID: replica, Transport: "websocket", LastSeenAt: &seen}))
		return id
	}
	fresh := mk("fresh", "r1", now)
	stale := mk("stale", "r1", now.Add(-5*time.Minute))
	other := mk("other", "r2", now)

	ids, err := ag.MarkStaleOffline(ctx, now.Add(-90*time.Second))
	mustNoErr(t, err)
	if len(ids) != 1 || ids[0] != stale {
		t.Fatalf("MarkStaleOffline = %v, want [%s]", ids, stale)
	}
	a, _ := ag.Get(ctx, stale)
	if a.IsOnline() || a.Connection.ReplicaID != "" || a.Connection.DisconnectedAt == nil {
		t.Fatalf("stale agent not marked offline: %+v", a.Connection)
	}

	dead, err := ag.MarkReplicaOffline(ctx, "r2", now)
	mustNoErr(t, err)
	if len(dead) != 1 || dead[0] != other {
		t.Fatalf("MarkReplicaOffline = %v", dead)
	}
	if a, _ := ag.Get(ctx, fresh); !a.IsOnline() {
		t.Fatal("fresh agent on r1 must stay online")
	}
	none, err := ag.MarkReplicaOffline(ctx, "nobody", now)
	mustNoErr(t, err)
	if none == nil || len(none) != 0 {
		t.Fatal("empty result must be a non-nil empty slice")
	}
}

func TestAgentHealthRemoteConfigAndConfigSync(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	id := upsert(t, ctx, db, agentSpec{host: "a", version: "0.162.0"})

	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	h := &domain.AgentHealth{Healthy: true, Status: "StatusOK", StartTime: &start, UpdatedAt: time.Now().UTC(),
		Components: map[string]*domain.ComponentHealth{"pipeline:metrics": {Healthy: true, Status: "StatusOK", Components: map[string]*domain.ComponentHealth{
			"receiver:otlp": {Healthy: false, LastError: "boom\x00x"}}}}}
	mustNoErr(t, ag.SetHealth(ctx, id, h))
	a, _ := ag.Get(ctx, id)
	if a.Health == nil || !a.Health.Healthy || a.Health.Status != "StatusOK" || a.Health.StartTime == nil || !a.Health.StartTime.Equal(start) {
		t.Fatalf("health: %+v", a.Health)
	}
	if c := a.Health.Components["pipeline:metrics"].Components["receiver:otlp"]; c == nil || c.Healthy || c.LastError == "" {
		t.Fatalf("recursive component health lost: %+v", a.Health.Components)
	}
	mustNoErr(t, ag.SetHealth(ctx, id, nil))
	a, _ = ag.Get(ctx, id)
	if a.Health != nil {
		t.Fatalf("SetHealth(nil) must clear health: %+v", a.Health)
	}
	if err := ag.SetHealth(ctx, newID(), h); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetHealth unknown: %v", err)
	}

	// config sync lifecycle: none -> pending -> in_sync -> drift -> failed
	assigned := domain.SHA256([]byte("assigned"))
	check := func(want domain.ConfigSyncState) {
		t.Helper()
		a, err := ag.Get(ctx, id)
		mustNoErr(t, err)
		if got := a.ConfigSyncState(); got != want {
			t.Fatalf("domain config sync = %s, want %s", got, want)
		}
		page, err := ag.List(ctx, store.AgentQuery{Filter: store.AgentFilter{ConfigSync: []domain.ConfigSyncState{want}}})
		mustNoErr(t, err)
		if page.Total != 1 {
			t.Fatalf("SQL generated config_sync column disagrees with domain for %s (matches %d)", want, page.Total)
		}
	}
	check(domain.ConfigSyncNone)
	mustNoErr(t, ag.SetAssignment(ctx, id, store.AgentAssignmentUpdate{FleetName: "edge-nl", AssignedConfigHash: assigned, TargetVersion: "0.162.0"}))
	changed, err := ag.SetDesiredConfigIfUnset(ctx, []domain.AgentID{id}, assigned)
	mustNoErr(t, err)
	if len(changed) != 1 {
		t.Fatalf("SetDesiredConfigIfUnset must set unset desired: %v", changed)
	}
	again, err := ag.SetDesiredConfigIfUnset(ctx, []domain.AgentID{id}, domain.SHA256([]byte("other")))
	mustNoErr(t, err)
	if len(again) != 0 {
		t.Fatalf("SetDesiredConfigIfUnset must not overwrite: %v", again)
	}
	check(domain.ConfigSyncPending)
	mustNoErr(t, ag.SetRemoteConfigState(ctx, id, domain.RemoteConfigState{Status: domain.RemoteConfigApplied, LastHash: assigned}))
	check(domain.ConfigSyncInSync)

	newer := domain.SHA256([]byte("newer"))
	mustNoErr(t, ag.SetAssignment(ctx, id, store.AgentAssignmentUpdate{FleetName: "edge-nl", AssignedConfigHash: newer}))
	check(domain.ConfigSyncDrift)

	// failed hash is cleared when desired changes, kept when it stays
	mustNoErr(t, ag.SetDesiredConfig(ctx, []domain.AgentID{id}, newer))
	mustNoErr(t, ag.SetFailedConfig(ctx, id, newer))
	check(domain.ConfigSyncFailed)
	mustNoErr(t, ag.SetDesiredConfig(ctx, []domain.AgentID{id}, newer)) // unchanged desired
	check(domain.ConfigSyncFailed)
	mustNoErr(t, ag.SetDesiredConfig(ctx, []domain.AgentID{id}, assigned)) // changed -> failed cleared
	a, _ = ag.Get(ctx, id)
	if !a.Assignment.FailedConfigHash.IsZero() {
		t.Fatalf("failed hash must be cleared when desired changes: %s", a.Assignment.FailedConfigHash)
	}
	mustNoErr(t, ag.SetDesiredConfig(ctx, []domain.AgentID{id}, newer))
	mustNoErr(t, ag.SetFailedConfig(ctx, id, newer))
	mustNoErr(t, ag.ClearFailedConfig(ctx, id))
	a, _ = ag.Get(ctx, id)
	if !a.Assignment.FailedConfigHash.IsZero() {
		t.Fatal("ClearFailedConfig")
	}
	mustNoErr(t, ag.SetDesiredConfig(ctx, nil, newer))
}

func TestAgentEffectiveComponentsPackagesLabels(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	id := upsert(t, ctx, db, agentSpec{host: "a", version: "0.162.0"})
	h := domain.SHA256([]byte("eff"))
	at := time.Now().UTC().Truncate(time.Microsecond)
	mustNoErr(t, ag.SetEffectiveConfig(ctx, id, h, at))
	mustNoErr(t, ag.SetAvailableComponentsHash(ctx, id, domain.SHA256([]byte("c"))))
	mustNoErr(t, ag.SetLabels(ctx, id, map[string]string{"pilot": "true"}))
	ps := &domain.PackageStatuses{ServerProvidedAllPackagesHash: domain.SHA256([]byte("p")), Packages: map[string]*domain.PackageStatus{
		"": {Name: "", Status: domain.PackageInstalling, DownloadPercent: 42.5, ServerOfferedVersion: "v0.163.0", AgentHasHash: domain.SHA256([]byte("x"))}}}
	mustNoErr(t, ag.SetPackageStatuses(ctx, id, ps))
	a, _ := ag.Get(ctx, id)
	if !a.EffectiveConfigHash.Equal(h) || a.EffectiveConfigUpdatedAt == nil || !a.EffectiveConfigUpdatedAt.Equal(at) {
		t.Fatalf("effective: %+v", a)
	}
	if a.Labels["pilot"] != "true" || len(a.AvailableComponentsHash) != 32 {
		t.Fatalf("labels/components: %+v", a)
	}
	p := a.PackageStatus
	if p == nil || p.Packages[""] == nil || p.Packages[""].Status != domain.PackageInstalling || p.Packages[""].DownloadPercent != 42.5 ||
		!p.Packages[""].AgentHasHash.Equal(domain.SHA256([]byte("x"))) || !p.ServerProvidedAllPackagesHash.Equal(ps.ServerProvidedAllPackagesHash) {
		t.Fatalf("package statuses: %+v", p)
	}
	mustNoErr(t, ag.SetPackageStatuses(ctx, id, nil))
	a, _ = ag.Get(ctx, id)
	if a.PackageStatus != nil {
		t.Fatal("SetPackageStatuses(nil) must clear")
	}
	mustNoErr(t, ag.SetLabels(ctx, id, nil))
	a, _ = ag.Get(ctx, id)
	if a.Labels == nil || len(a.Labels) != 0 {
		t.Fatalf("labels must be an empty map: %#v", a.Labels)
	}
}

func TestUpdaterLinkBothDirections(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	collector := newID()
	updater := upsert(t, ctx, db, agentSpec{host: "host1", version: "0.1.0", kind: domain.AgentKindUpdater})

	// the collector does not exist yet: the link is stored on the updater row only
	mustNoErr(t, ag.SetUpdaterLink(ctx, updater, collector))
	u, _ := ag.Get(ctx, updater)
	if u.Updater == nil || u.Updater.AgentID != collector || u.Updater.Online {
		t.Fatalf("updater->collector link before the collector exists: %+v", u.Updater)
	}

	upsert(t, ctx, db, agentSpec{id: collector, host: "host1", version: "0.162.0"})
	c, _ := ag.Get(ctx, collector)
	if c.Updater == nil || c.Updater.AgentID != updater || c.Updater.Online {
		t.Fatalf("collector->updater link (offline): %+v", c.Updater)
	}
	now := time.Now().UTC()
	mustNoErr(t, ag.SetConnection(ctx, updater, domain.AgentConnection{Status: domain.ConnectionOnline, LastSeenAt: &now}))
	mustNoErr(t, ag.SetConnection(ctx, collector, domain.AgentConnection{Status: domain.ConnectionOnline}))
	c, _ = ag.Get(ctx, collector)
	u, _ = ag.Get(ctx, updater)
	if c.Updater == nil || !c.Updater.Online || u.Updater == nil || !u.Updater.Online {
		t.Fatalf("online flags: %+v %+v", c.Updater, u.Updater)
	}

	// two updaters pointing at the same collector: most recently seen wins
	updater2 := upsert(t, ctx, db, agentSpec{host: "host1", version: "0.1.0", kind: domain.AgentKindUpdater})
	mustNoErr(t, ag.SetUpdaterLink(ctx, updater2, collector))
	later := now.Add(time.Hour)
	mustNoErr(t, ag.TouchLastSeen(ctx, []domain.AgentID{updater2}, later))
	c, _ = ag.Get(ctx, collector)
	if c.Updater.AgentID != updater || c.Updater.Ambiguous {
		t.Fatalf("an online updater wins over an offline one: %+v", c.Updater)
	}
	mustNoErr(t, ag.SetConnection(ctx, updater2, domain.AgentConnection{Status: domain.ConnectionOnline}))
	c, _ = ag.Get(ctx, collector)
	if c.Updater.AgentID != updater2 || !c.Updater.Ambiguous {
		t.Fatalf("most recently seen online updater wins, the link is ambiguous: %+v", c.Updater)
	}

	// hydration also applies to list results
	page, err := ag.List(ctx, store.AgentQuery{Filter: store.AgentFilter{Kinds: []domain.AgentKind{domain.AgentKindCollector}}})
	mustNoErr(t, err)
	if len(page.Items) != 1 || page.Items[0].Updater == nil {
		t.Fatalf("List must hydrate the updater link: %+v", page.Items)
	}
	mustNoErr(t, ag.SetUpdaterLink(ctx, updater, domain.AgentID{}))
	u, _ = ag.Get(ctx, updater)
	if u.Updater != nil {
		t.Fatal("zero collector id removes the link")
	}
}

func TestAgentEventsAndDelete(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	id := upsert(t, ctx, db, agentSpec{host: "a", version: "0.162.0"})
	base := time.Now().UTC().Add(-time.Hour)
	var evs []domain.AgentEvent
	for i := 0; i < 5; i++ {
		evs = append(evs, domain.AgentEvent{AgentID: id, Time: base.Add(time.Duration(i) * time.Minute), Type: domain.AgentEventConnected,
			Severity: "info", Message: fmt.Sprintf("e%d", i), Details: map[string]any{"n": i, "nested": map[string]any{"k": "v"}}})
	}
	evs = append(evs, domain.AgentEvent{AgentID: newID(), Type: domain.AgentEventConnected, Message: "orphan"}) // unknown agent: skipped
	evs = append(evs, domain.AgentEvent{AgentID: id, Type: domain.AgentEventHealthChanged, Severity: "bogus", Message: "bad severity"})
	mustNoErr(t, ag.AppendEvents(ctx, evs))
	mustNoErr(t, ag.AppendEvents(ctx, nil))

	got, err := ag.ListEvents(ctx, id, 3, 0)
	mustNoErr(t, err)
	if len(got) != 3 || got[0].Message != "bad severity" || got[0].Severity != "info" {
		t.Fatalf("newest first: %+v", got)
	}
	next, err := ag.ListEvents(ctx, id, 10, got[2].ID)
	mustNoErr(t, err)
	if len(next) != 3 || next[0].ID >= got[2].ID {
		t.Fatalf("cursor pagination: %+v", next)
	}
	if n, ok := next[0].Details["n"].(int); !ok || n != 2 {
		t.Fatalf("details numbers must stay int, got %#v", next[0].Details["n"])
	}
	n, err := ag.DeleteEventsBefore(ctx, base.Add(150*time.Second))
	mustNoErr(t, err)
	if n != 3 {
		t.Fatalf("DeleteEventsBefore deleted %d", n)
	}

	mustNoErr(t, ag.Delete(ctx, id))
	if err := ag.Delete(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	left, _ := ag.ListEvents(ctx, id, 10, 0)
	if len(left) != 0 {
		t.Fatal("events must be deleted with the agent")
	}
}

func TestDeleteOfflineBefore(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	ag := db.Agents()
	old := upsert(t, ctx, db, agentSpec{host: "old", version: "0.162.0"})
	online := upsert(t, ctx, db, agentSpec{host: "online", version: "0.162.0"})
	longAgo := time.Now().UTC().Add(-48 * time.Hour)
	mustNoErr(t, ag.SetConnection(ctx, old, domain.AgentConnection{Status: domain.ConnectionOffline, DisconnectedAt: &longAgo}))
	mustNoErr(t, ag.SetConnection(ctx, online, domain.AgentConnection{Status: domain.ConnectionOnline}))
	n, err := ag.DeleteOfflineBefore(ctx, time.Now().Add(-24*time.Hour))
	mustNoErr(t, err)
	if n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if _, err := ag.Get(ctx, old); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("old offline agent must be gone")
	}
	if _, err := ag.Get(ctx, online); err != nil {
		t.Fatal("online agent must stay")
	}
}

func TestInTxRollbackAndCommit(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	id := newID()
	boom := errors.New("boom")
	err := db.InTx(ctx, func(tx store.Store) error {
		if _, _, err := tx.Agents().UpsertDescription(ctx, id, agentSpec{host: "tx", version: "0.1.0"}.update()); err != nil {
			return err
		}
		if _, err := tx.Agents().Get(ctx, id); err != nil {
			t.Errorf("row must be visible inside the transaction: %v", err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx must return the callback error, got %v", err)
	}
	if _, err := db.Agents().Get(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rolled back row must not exist: %v", err)
	}

	err = db.InTx(ctx, func(tx store.Store) error {
		_, _, err := tx.Agents().UpsertDescription(ctx, id, agentSpec{host: "tx", version: "0.1.0"}.update())
		return err
	})
	mustNoErr(t, err)
	if _, err := db.Agents().Get(ctx, id); err != nil {
		t.Fatalf("committed row: %v", err)
	}

	// panic inside the callback rolls back and re-panics
	id2 := newID()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("InTx must re-panic")
			}
		}()
		_ = db.InTx(ctx, func(tx store.Store) error {
			_, _, _ = tx.Agents().UpsertDescription(ctx, id2, agentSpec{host: "p", version: "0.1.0"}.update())
			panic("kaboom")
		})
	}()
	if _, err := db.Agents().Get(ctx, id2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("panic must roll back")
	}

	// nested InTx joins the running transaction
	id3 := newID()
	err = db.InTx(ctx, func(tx store.Store) error {
		return tx.InTx(ctx, func(inner store.Store) error {
			_, _, err := inner.Agents().UpsertDescription(ctx, id3, agentSpec{host: "n", version: "0.1.0"}.update())
			if err != nil {
				return err
			}
			return boom
		})
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := db.Agents().Get(ctx, id3); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("nested InTx error must roll back the outer transaction")
	}
}

func TestPingAndSchemaInfo(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	mustNoErr(t, db.Ping(ctx))
	ver, mig, err := db.SchemaInfo(ctx)
	mustNoErr(t, err)
	if ver == "" || mig < 2 {
		t.Fatalf("schema info %q %d", ver, mig)
	}
}
