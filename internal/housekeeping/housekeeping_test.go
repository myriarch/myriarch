// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package housekeeping_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/housekeeping"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func counter(t *testing.T, reg *prometheus.Registry, job string) float64 {
	t.Helper()
	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "myriarch_housekeeping_deleted_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == job {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

var _ = dto.Metric{}

func TestRetentionJobs(t *testing.T) {
	db := postgres.New(pgtest.NewPool(t), quiet)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-400 * 24 * time.Hour)

	// agents: one offline for long, one online, one recently offline
	mkAgent := func(host string, status domain.ConnectionStatus, disc time.Time) domain.AgentID {
		id := domain.AgentID(uuid.Must(uuid.NewV7()))
		if _, _, err := db.Agents().UpsertDescription(ctx, id, store.AgentDescriptionUpdate{Kind: domain.AgentKindCollector, Hostname: host, Version: "0.162.0"}); err != nil {
			t.Fatal(err)
		}
		d := disc
		if err := db.Agents().SetConnection(ctx, id, domain.AgentConnection{Status: status, DisconnectedAt: &d}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	gone := mkAgent("gone", domain.ConnectionOffline, now.Add(-45*24*time.Hour))
	online := mkAgent("online", domain.ConnectionOnline, old)
	recent := mkAgent("recent", domain.ConnectionOffline, now.Add(-2*24*time.Hour))
	_ = recent

	// events: old and new
	for _, e := range []domain.AgentEvent{
		{AgentID: online, Time: now.Add(-40 * 24 * time.Hour), Type: domain.AgentEventConnected, Message: "old"},
		{AgentID: online, Time: now.Add(-time.Hour), Type: domain.AgentEventConnected, Message: "new"},
	} {
		if err := db.Agents().AppendEvents(ctx, []domain.AgentEvent{e}); err != nil {
			t.Fatal(err)
		}
	}
	// audit
	for _, tm := range []time.Time{old, now.Add(-time.Hour)} {
		if err := db.Audit().Append(ctx, domain.AuditEvent{Time: tm, ActorType: domain.ActorSystem, ActorID: "s", Action: "x.y", Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	// finished rollout (200 days ago) and running rollout
	mkRollout := func(status domain.RolloutStatus) uuid.UUID {
		id := uuid.New()
		if err := db.Rollouts().Create(ctx, &domain.Rollout{ID: id, Kind: domain.RolloutKindConfig, FleetName: "f", PolicyName: "p", Status: status, ToRef: "c", CreatedBy: "t",
			Waves: []domain.RolloutWave{{Index: 0, Name: "all", Status: domain.WaveCompleted}}}, nil); err != nil {
			t.Fatal(err)
		}
		return id
	}
	doneRollout, runningRollout := mkRollout(domain.RolloutRunning), mkRollout(domain.RolloutRunning)
	if ok, err := db.Rollouts().CompareAndSetStatus(ctx, doneRollout, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutCompleted, ""); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `UPDATE rollouts SET finished_at = $2 WHERE id = $1`, doneRollout, now.Add(-200*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// gitops sync history: old entries and the newest applied one (must survive)
	if _, err := db.GitOps().ApplySnapshot(ctx, domain.GitOpsSnapshot{Commit: "c1"}, domain.GitOpsSync{StartedAt: now.Add(-60 * 24 * time.Hour), Commit: &domain.GitCommit{SHA: "c1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GitOps().RecordSync(ctx, domain.GitOpsSync{StartedAt: now.Add(-50 * 24 * time.Hour), Result: "invalid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GitOps().RecordSync(ctx, domain.GitOpsSync{StartedAt: now.Add(-time.Hour), Result: "unchanged"}); err != nil {
		t.Fatal(err)
	}
	// configs: unreferenced old rendered config
	body := []byte("x: y\n")
	if err := db.Configs().PutRendered(ctx, domain.RenderedConfig{Hash: domain.ConfigHash(body), Body: body}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `UPDATE rendered_configs SET touched_at = $1`, now.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// sessions: expired one
	u, err := db.Identity().UpsertUser(ctx, domain.User{Subject: "s", Provider: "local", Role: domain.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []domain.Session{
		{ID: "expired", UserID: u.ID, CSRFToken: "c", ExpiresAt: now.Add(-time.Hour)},
		{ID: "valid", UserID: u.ID, CSRFToken: "c", ExpiresAt: now.Add(time.Hour)},
	} {
		if err := db.Identity().CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	reg := prometheus.NewRegistry()
	r := housekeeping.New(config.Default().Retention, db, quiet, reg)
	res := r.RunOnce(ctx)
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	for job, want := range map[string]int64{"agent_events": 1, "audit_events": 1, "offline_agents": 1, "finished_rollouts": 1, "unreferenced_configs": 1, "gitops_syncs": 1, "expired_sessions": 1} {
		if res.Deleted[job] != want {
			t.Errorf("job %s deleted %d, want %d (all: %v)", job, res.Deleted[job], want, res.Deleted)
		}
		if got := counter(t, reg, job); got != float64(want) {
			t.Errorf("metric for %s = %v, want %d", job, got, want)
		}
	}
	if _, err := db.Agents().Get(ctx, gone); !errors.Is(err, domain.ErrNotFound) {
		t.Error("long offline agent must be purged")
	}
	if _, err := db.Agents().Get(ctx, online); err != nil {
		t.Error("online agent must stay")
	}
	if _, err := db.Rollouts().Get(ctx, doneRollout); !errors.Is(err, domain.ErrNotFound) {
		t.Error("finished rollout must be deleted")
	}
	if _, err := db.Rollouts().Get(ctx, runningRollout); err != nil {
		t.Error("running rollout must stay")
	}
	snap, _ := db.GitOps().GetSnapshot(ctx)
	if snap.Commit != "c1" {
		t.Error("newest applied sync must survive")
	}
	if _, err := db.Identity().GetSession(ctx, "valid"); err != nil {
		t.Error("valid session must stay")
	}

	// second run: nothing left to delete
	res = r.RunOnce(ctx)
	for job, n := range res.Deleted {
		if n != 0 {
			t.Errorf("second run deleted %d rows for %s", n, job)
		}
	}
}

func TestZeroRetentionDisablesJobs(t *testing.T) {
	db := postgres.New(pgtest.NewPool(t), quiet)
	ctx := context.Background()
	id := domain.AgentID(uuid.Must(uuid.NewV7()))
	if _, _, err := db.Agents().UpsertDescription(ctx, id, store.AgentDescriptionUpdate{Kind: domain.AgentKindCollector, Hostname: "h", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().Add(-1000 * 24 * time.Hour)
	if err := db.Agents().SetConnection(ctx, id, domain.AgentConnection{Status: domain.ConnectionOffline, DisconnectedAt: &ancient}); err != nil {
		t.Fatal(err)
	}
	if err := db.Audit().Append(ctx, domain.AuditEvent{Time: ancient, ActorType: domain.ActorSystem, ActorID: "s", Action: "a", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.RetentionConfig{} // everything 0 = keep
	res := housekeeping.New(cfg, db, quiet, nil).RunOnce(ctx)
	if _, onlySessions := res.Deleted["expired_sessions"]; len(res.Deleted) != 2 || !onlySessions { // only the session and marker cleanup have no retention switch
		t.Fatalf("jobs executed with retention 0: %v", res.Deleted)
	}
	if _, err := db.Agents().Get(ctx, id); err != nil {
		t.Fatal("agent must be kept with offlineAgents=0")
	}
	if _, n, _ := db.Audit().List(ctx, store.AuditFilter{}); n != 1 {
		t.Fatal("audit must be kept")
	}
}

func TestRunStartsAfterDelayAndStopsOnCancel(t *testing.T) {
	db := postgres.New(pgtest.NewPool(t), quiet)
	oldI, oldD := housekeeping.Interval, housekeeping.InitialDelay
	housekeeping.Interval, housekeeping.InitialDelay = 150*time.Millisecond, 50*time.Millisecond
	defer func() { housekeeping.Interval, housekeeping.InitialDelay = oldI, oldD }()

	ctx := context.Background()
	if err := db.Audit().Append(ctx, domain.AuditEvent{Time: time.Now().Add(-500 * 24 * time.Hour), ActorType: domain.ActorSystem, ActorID: "s", Action: "a", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	r := housekeeping.New(config.Default().Retention, db, quiet, reg)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(rctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for counter(t, reg, "audit_events") < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first run must happen after the initial delay")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run must return when the context is cancelled")
	}
}

// failingStore makes one job fail to check job isolation.
type failingAudit struct{ store.AuditStore }

func (failingAudit) DeleteBefore(context.Context, time.Time) (int64, error) {
	return 0, errors.New("disk full")
}

type failingStore struct {
	store.Store
}

func (f failingStore) Audit() store.AuditStore { return failingAudit{f.Store.Audit()} }

func TestFailingJobDoesNotStopOthers(t *testing.T) {
	db := postgres.New(pgtest.NewPool(t), quiet)
	ctx := context.Background()
	if err := db.Agents().AppendEvents(ctx, nil); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	res := housekeeping.New(config.Default().Retention, failingStore{db}, quiet, reg).RunOnce(ctx)
	if res.Errors["audit_events"] == nil {
		t.Fatal("the failing job must be reported")
	}
	if _, ok := res.Deleted["expired_sessions"]; !ok || len(res.Deleted) < 5 {
		t.Fatalf("other jobs must still run: %v", res.Deleted)
	}
}

// OIDC login states and webhook delivery ids are short-lived markers: housekeeping removes old ones.
func TestShortLivedMarkersAreRemoved(t *testing.T) {
	db := postgres.New(pgtest.NewPool(t), quiet)
	ctx := context.Background()
	for _, k := range []string{store.KVPrefixOIDCUsed + "abc", store.KVPrefixWebhookDelivery + "github/d-1", "release/etag/x"} {
		if err := db.KV().Put(ctx, k, []byte("1")); err != nil {
			t.Fatal(err)
		}
	}
	r := housekeeping.New(config.RetentionConfig{}, db, quiet, nil)
	r.SetClock(func() time.Time { return time.Now().Add(2 * store.KVMarkerRetention) })
	res := r.RunOnce(ctx)
	if res.Deleted["short_lived_markers"] != 2 {
		t.Fatalf("markers deleted: %v", res.Deleted)
	}
	if _, err := db.KV().Get(ctx, "release/etag/x"); err != nil {
		t.Fatalf("component state must survive: %v", err)
	}
	if _, err := db.KV().Get(ctx, store.KVPrefixOIDCUsed+"abc"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old login state still there: %v", err)
	}
}
