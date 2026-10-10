// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
	"github.com/myriarch/myriarch/internal/store"
)

type recNotifier struct {
	mu   sync.Mutex
	list []notify.Notification
}

func (r *recNotifier) Notify(_ context.Context, n notify.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, n)
	return nil
}
func (r *recNotifier) Test(context.Context, string) error { return nil }
func (r *recNotifier) Channels() []notify.ChannelInfo     { return nil }
func (r *recNotifier) all() []notify.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Notification(nil), r.list...)
}

type fixture struct {
	dir      string
	st       *memstore.Store
	bus      eventbus.Bus
	notifier *recNotifier
	reg      *prometheus.Registry
	applied  atomic.Int32
	// onAppliedErr makes OnApplied fail (leader crash / reassignment error).
	onAppliedErr atomic.Bool
	lastSnap     atomic.Pointer[domain.GitOpsSnapshot]
	now          atomic.Pointer[time.Time]
	cfg          config.GitOpsConfig
	syncer       *Syncer
	// httpClient and policy are the production wiring of the HTTP(S) path
	// (nil: go-git's default client).
	httpClient *http.Client
	policy     *netguard.Policy
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.CopyFS(dst, os.DirFS(src)))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		dir: filepath.Join(t.TempDir(), "repo"), st: memstore.New(), bus: eventbus.NewMemory("test", 0),
		notifier: &recNotifier{}, reg: prometheus.NewRegistry(),
	}
	t.Cleanup(func() { f.bus.Close() })
	copyDir(t, "../testdata/gitops", f.dir)
	f.cfg = config.GitOpsConfig{Enabled: true, RepoURL: "dir:" + f.dir, Branch: "main", PollInterval: time.Hour, WebhookSecret: "s3cret"}
	return f
}

func (f *fixture) build(t *testing.T) *Syncer {
	t.Helper()
	s, err := NewSyncer(f.cfg, Deps{
		Store: f.st, Bus: f.bus, Renderer: blueprint.NewRenderer(blueprint.Options{}),
		Audit: auditAdapter{f.st}, Notifier: f.notifier, Registerer: f.reg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), HTTPClient: f.httpClient, Policy: f.policy,
		OnApplied: func(ctx context.Context, snap *domain.GitOpsSnapshot, changes store.ObjectChanges) error {
			f.applied.Add(1)
			f.lastSnap.Store(snap)
			if f.onAppliedErr.Load() {
				return errors.New("reassign failed")
			}
			return nil
		},
	})
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	f.now.Store(&t0)
	s.now = func() time.Time { return *f.now.Load() }
	f.syncer = s
	return s
}

func (f *fixture) advance(d time.Duration) {
	n := f.now.Load().Add(d)
	f.now.Store(&n)
}

type auditAdapter struct{ st *memstore.Store }

func (a auditAdapter) Record(ctx context.Context, e domain.AuditEvent) {
	_ = a.st.Audit().Append(ctx, e)
}

func (f *fixture) edit(t *testing.T, rel, from, to string) {
	t.Helper()
	p := filepath.Join(f.dir, rel)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Contains(t, string(b), from)
	require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), from, to, 1)), 0o644))
}

func (f *fixture) status(t *testing.T) *domain.GitOpsSyncStatus {
	t.Helper()
	st, err := f.st.GitOps().GetStatus(context.Background())
	require.NoError(t, err)
	return st
}

func resultsOf(syncs []domain.GitOpsSync) []string {
	var out []string
	for _, s := range syncs {
		out = append(out, s.Result)
	}
	return out
}

func TestSyncAppliesSnapshot(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	events, cancel := f.bus.Subscribe(eventbus.GitOpsSynced)
	defer cancel()

	require.NoError(t, s.SyncNow(context.Background(), "startup"))

	snap, err := f.st.GitOps().GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Len(t, snap.Fleets, 5)
	st := f.status(t)
	assert.Equal(t, "ok", st.State)
	require.NotNil(t, st.AppliedCommit)
	assert.True(t, strings.HasPrefix(st.AppliedCommit.SHA, "dir:"))
	assert.Equal(t, st.AppliedCommit.SHA, snap.Commit)
	assert.Equal(t, 5, st.ObjectCounts[domain.KindFleet])
	assert.Equal(t, "dir:"+f.dir, st.Source)
	assert.NotNil(t, st.LastSuccessAt)
	assert.Equal(t, int32(1), f.applied.Load())
	assert.Equal(t, snap.Commit, f.lastSnap.Load().Commit)
	assert.Equal(t, []string{"applied"}, resultsOf(f.st.Syncs()))
	assert.Len(t, f.st.Syncs()[0].Added, 16, "added objects are reported by the store")

	ev := <-events
	var pl eventbus.GitOpsSyncedPayload
	require.NoError(t, ev.Decode(&pl))
	assert.Equal(t, "applied", pl.Result)
	assert.Equal(t, snap.Commit, pl.Commit)

	audits := f.st.AuditEvents()
	require.Len(t, audits, 1)
	assert.Equal(t, "gitops.apply", audits[0].Action)
	assert.Equal(t, domain.ActorGitOps, audits[0].ActorType)
	assert.Equal(t, snap.Commit, audits[0].Details["commit"])

	assert.Equal(t, 1.0, metricValue(t, f.reg, "myriarch_gitops_sync_total", map[string]string{"result": "applied"}))
	assert.InDelta(t, float64(f.now.Load().Unix()), metricValue(t, f.reg, "myriarch_gitops_last_success_timestamp_seconds", nil), 1)
}

func TestSyncUnchangedIsRecordedAtMostHourly(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	ctx := context.Background()
	require.NoError(t, s.SyncNow(ctx, "startup"))
	f.advance(time.Minute)
	require.NoError(t, s.SyncNow(ctx, "poll"))
	f.advance(time.Minute)
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, []string{"applied", "unchanged"}, resultsOf(f.st.Syncs()), "second unchanged poll is not recorded")
	assert.Equal(t, int32(1), f.applied.Load())
	assert.Equal(t, f.now.Load().Unix(), f.status(t).LastSyncAt.Unix(), "status timestamp is updated on every poll")

	f.advance(2 * time.Hour)
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, []string{"applied", "unchanged", "unchanged"}, resultsOf(f.st.Syncs()))
}

func TestInvalidRevisionKeepsOldSnapshotAndNotifiesOnce(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	ctx := context.Background()
	require.NoError(t, s.SyncNow(ctx, "startup"))
	good := f.status(t).AppliedCommit.SHA

	f.edit(t, "fleets/edge-nl.yaml", "blueprint: edge-windows", "blueprint: edge-wndows")
	require.NoError(t, s.SyncNow(ctx, "poll"), "an invalid revision is not an error")

	st := f.status(t)
	assert.Equal(t, "error", st.State)
	assert.Equal(t, good, st.AppliedCommit.SHA, "old stand stays applied")
	require.NotNil(t, st.LatestCommit)
	assert.NotEqual(t, good, st.LatestCommit.SHA)
	var found *domain.Issue
	for i := range st.Issues {
		if st.Issues[i].Code == "blueprint_not_found" {
			found = &st.Issues[i]
		}
	}
	require.NotNil(t, found, "%+v", st.Issues)
	assert.Equal(t, "fleets/edge-nl.yaml", found.File)
	assert.Equal(t, 19, found.Line)
	assert.Equal(t, int32(1), f.applied.Load())
	snap, _ := f.st.GitOps().GetSnapshot(ctx)
	assert.Equal(t, good, snap.Commit)
	assert.Equal(t, []string{"applied", "invalid"}, resultsOf(f.st.Syncs()))

	ns := f.notifier.all()
	require.Len(t, ns, 1)
	assert.Equal(t, notify.EventGitOpsSyncFailed, ns[0].Event)
	assert.Equal(t, "gitops.sync_failed/"+st.LatestCommit.SHA, ns[0].DedupKey)
	assert.Contains(t, ns[0].Summary, "fleets/edge-nl.yaml:19")

	// Polling the same invalid revision again must not spam history or notifications.
	f.advance(time.Minute)
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, []string{"applied", "invalid"}, resultsOf(f.st.Syncs()))
	assert.Len(t, f.notifier.all(), 1)
	assert.Equal(t, "error", f.status(t).State)

	// Fix it: applied again, state ok.
	f.edit(t, "fleets/edge-nl.yaml", "blueprint: edge-wndows", "blueprint: edge-windows\n  # fixed")
	require.NoError(t, s.SyncNow(ctx, "poll"))
	st = f.status(t)
	assert.Equal(t, "ok", st.State)
	assert.NotEqual(t, good, st.AppliedCommit.SHA)
	assert.Equal(t, int32(2), f.applied.Load())
	assert.False(t, st.Issues.HasErrors())
}

func TestRevertToAppliedRevisionClearsErrorWithoutReapply(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	ctx := context.Background()
	require.NoError(t, s.SyncNow(ctx, "startup"))
	f.edit(t, "fleets/edge-nl.yaml", "blueprint: edge-windows", "blueprint: nope")
	require.NoError(t, s.SyncNow(ctx, "poll"))
	require.Equal(t, "error", f.status(t).State)

	f.edit(t, "fleets/edge-nl.yaml", "blueprint: nope", "blueprint: edge-windows")
	require.NoError(t, s.SyncNow(ctx, "poll"))
	st := f.status(t)
	assert.Equal(t, "ok", st.State)
	assert.Equal(t, st.AppliedCommit.SHA, st.LatestCommit.SHA)
	assert.False(t, st.Issues.HasErrors())
	assert.Equal(t, int32(1), f.applied.Load(), "the applied revision is not applied again")
}

func TestFetchErrorsAreReportedAndRecover(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	ctx := context.Background()
	require.NoError(t, s.SyncNow(ctx, "startup"))
	require.NoError(t, os.Rename(f.dir, f.dir+".gone"))

	err := s.SyncNow(ctx, "poll")
	require.Error(t, err)
	st := f.status(t)
	assert.Equal(t, "error", st.State)
	require.Len(t, st.Issues, 1)
	assert.Equal(t, "sync_failed", st.Issues[0].Code)
	assert.NotNil(t, st.AppliedCommit, "old stand stays applied")
	assert.Equal(t, 1.0, metricValue(t, f.reg, "myriarch_gitops_sync_total", map[string]string{"result": "error"}))
	assert.Equal(t, []string{"applied", "error"}, resultsOf(f.st.Syncs()))

	// the same error again is not recorded again within the hour
	require.Error(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, []string{"applied", "error"}, resultsOf(f.st.Syncs()))

	require.NoError(t, os.Rename(f.dir+".gone", f.dir))
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, "ok", f.status(t).State)
	assert.Equal(t, int32(1), f.applied.Load())
}

func TestPersistentFetchErrorNotifies(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	require.NoError(t, s.SyncNow(context.Background(), "startup"))
	require.NoError(t, os.Rename(f.dir, f.dir+".gone"))
	for i := 0; i < 6; i++ {
		_ = s.SyncNow(context.Background(), "poll")
	}
	ns := f.notifier.all()
	require.Len(t, ns, 1, "one notification after 5 consecutive failures")
	assert.Equal(t, notify.EventGitOpsSyncFailed, ns[0].Event)
}

// Leader failover: a fresh syncer on the same store sees the applied revision
// in the persisted status and does not re-apply or re-plan.
func TestFailoverDoesNotReapply(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.build(t).SyncNow(context.Background(), "startup"))
	require.Equal(t, int32(1), f.applied.Load())

	second := f.build(t)
	require.NoError(t, second.SyncNow(context.Background(), "startup"))
	assert.Equal(t, int32(1), f.applied.Load())
	assert.Equal(t, []string{"applied", "unchanged"}, resultsOf(f.st.Syncs()))
}

// An applied revision whose reconciliation (reassignment, planning) failed or
// was lost with the leader is reconciled again by the next cycle, also on
// another leader, and not again afterwards.
func TestAppliedRevisionIsReconciledAgainAfterFailure(t *testing.T) {
	f := newFixture(t)
	f.onAppliedErr.Store(true)
	err := f.build(t).SyncNow(context.Background(), "startup")
	require.Error(t, err)
	require.Equal(t, int32(1), f.applied.Load())
	st, _ := f.st.GitOps().GetStatus(context.Background())
	require.NotNil(t, st.AppliedCommit, "the snapshot itself is applied")

	// New leader, still failing: retried.
	require.Error(t, f.build(t).SyncNow(context.Background(), "startup"))
	assert.Equal(t, int32(2), f.applied.Load())

	// Recovered: reconciled once, with the applied snapshot.
	f.onAppliedErr.Store(false)
	second := f.build(t)
	require.NoError(t, second.SyncNow(context.Background(), "poll"))
	assert.Equal(t, int32(3), f.applied.Load())
	require.NotNil(t, f.lastSnap.Load())
	require.NoError(t, second.SyncNow(context.Background(), "poll"))
	assert.Equal(t, int32(3), f.applied.Load(), "reconciled revisions are not reconciled again")
}

func TestRunPollsAndHandlesRequests(t *testing.T) {
	f := newFixture(t)
	f.cfg.PollInterval = time.Hour // only startup + events
	s := f.build(t)
	s.now = func() time.Time { return time.Now().UTC() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	require.Eventually(t, func() bool { return f.applied.Load() == 1 }, 5*time.Second, 10*time.Millisecond, "startup sync")

	f.edit(t, "fleets/edge-nl.yaml", "priority: 100", "priority: 101")
	require.NoError(t, s.RequestSync(ctx, "manual", "user:1"))
	require.Eventually(t, func() bool { return f.applied.Load() == 2 }, 5*time.Second, 10*time.Millisecond, "manual sync")
	assert.Equal(t, "manual", f.st.Syncs()[1].Trigger)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunPollInterval(t *testing.T) {
	f := newFixture(t)
	f.cfg.PollInterval = 30 * time.Millisecond
	s := f.build(t)
	s.now = func() time.Time { return time.Now().UTC() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	require.Eventually(t, func() bool { return f.applied.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	f.edit(t, "fleets/datacenter.yaml", "priority: 50", "priority: 51")
	require.Eventually(t, func() bool { return f.applied.Load() == 2 }, 5*time.Second, 10*time.Millisecond, "poll picks up the change")
}

func TestDisabledSyncerBlocksUntilCancel(t *testing.T) {
	f := newFixture(t)
	f.cfg.Enabled = false
	s := f.build(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case <-done:
		t.Fatal("disabled syncer must wait for ctx")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	require.NoError(t, <-done)
	assert.Error(t, s.SyncNow(context.Background(), "manual"))
}

func TestNewSyncerValidation(t *testing.T) {
	_, err := NewSyncer(config.GitOpsConfig{Enabled: true}, Deps{})
	assert.Error(t, err, "repoUrl is required")
	_, err = NewSyncer(config.GitOpsConfig{Enabled: true, RepoURL: "ftp://x/y"}, Deps{})
	assert.Error(t, err)
	_, err = NewSyncer(config.GitOpsConfig{Enabled: true, RepoURL: "ssh://git@host/repo.git", Auth: config.GitAuthConfig{SSHKeyFile: "/nonexistent"}}, Deps{})
	assert.Error(t, err, "ssh requires known_hosts and key")
	_, err = NewSyncer(config.GitOpsConfig{Enabled: true, RepoURL: "git@host:org/repo.git", Auth: config.GitAuthConfig{SSHKeyFile: "/nonexistent"}}, Deps{})
	assert.Error(t, err)
	_, err = NewSyncer(config.GitOpsConfig{Enabled: false}, Deps{})
	assert.NoError(t, err, "disabled syncers need no source")
}

func TestRedactRepoURL(t *testing.T) {
	assert.Equal(t, "https://gitlab.example.com/o/r.git", redactRepoURL("https://user:tok@gitlab.example.com/o/r.git?x=1"))
	assert.Equal(t, "git@host:org/repo.git", redactRepoURL("git@host:org/repo.git"))
	assert.Equal(t, "dir:/x/y", redactRepoURL("dir:/x/y"))
	assert.Equal(t, "<invalid>", redactRepoURL("::bad::"))
}

func TestStatusNeverContainsCredentials(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	s.src = &dirSource{dir: f.dir, display: redactRepoURL("https://user:topsecret@example.com/r.git")}
	require.NoError(t, s.SyncNow(context.Background(), "startup"))
	raw, _ := json.Marshal(f.status(t))
	assert.NotContains(t, string(raw), "topsecret")
}

// --------------------------------------------------------------- webhook ----

func webhookRequest(t *testing.T, s *Syncer, mutate func(r *http.Request, body []byte)) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`{"ref":"refs/heads/main"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/gitops/webhook", bytes.NewReader(body))
	mutate(r, body)
	w := httptest.NewRecorder()
	route := s.WebhookHandler()
	route.Handler(w, r)
	return w
}

func TestWebhookRoute(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	route := s.WebhookHandler()
	assert.Equal(t, http.MethodPost, route.Method)
	assert.Equal(t, "/gitops/webhook", route.Pattern)
	assert.Equal(t, "gitopsWebhook", route.OperationID)
	assert.Equal(t, 2, int(route.Auth), "api.AuthCustom")
}

func TestWebhookAuthentication(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	events, cancel := f.bus.Subscribe(eventbus.GitOpsSyncRequested)
	defer cancel()
	sign := func(secret string, body []byte) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(body)
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	pending := func() int { return len(events) }

	// no credentials / wrong credentials
	w := webhookRequest(t, s, func(r *http.Request, b []byte) {})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Gitlab-Token", "wrong") })
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Hub-Signature-256", sign("wrong", b)) })
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Hub-Signature-256", "md5=abc") })
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Hub-Signature-256", "sha256=zz") })
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, 0, pending())

	// GitLab
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Gitlab-Token", "s3cret") })
	assert.Equal(t, http.StatusAccepted, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["accepted"])
	ev := <-events
	var pl eventbus.GitOpsSyncRequestedPayload
	require.NoError(t, ev.Decode(&pl))
	assert.Equal(t, "webhook", pl.Trigger)
	assert.Equal(t, "gitlab", pl.RequestedBy)

	// GitHub
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Hub-Signature-256", sign("s3cret", b)) })
	assert.Equal(t, http.StatusAccepted, w.Code)
	ev = <-events
	require.NoError(t, ev.Decode(&pl))
	assert.Equal(t, "github", pl.RequestedBy)

	// GitHub ping: accepted, no sync
	w = webhookRequest(t, s, func(r *http.Request, b []byte) {
		r.Header.Set("X-Hub-Signature-256", sign("s3cret", b))
		r.Header.Set("X-GitHub-Event", "ping")
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, 0, pending())

	// push to another branch: accepted but ignored
	body := []byte(`{"ref":"refs/heads/feature"}`)
	r := httptest.NewRequest(http.MethodPost, "/gitops/webhook", bytes.NewReader(body))
	r.Header.Set("X-Gitlab-Token", "s3cret")
	w = httptest.NewRecorder()
	s.WebhookHandler().Handler(w, r)
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "ignored")
	assert.Equal(t, 0, pending())
}

func TestWebhookWithoutSecretIs404(t *testing.T) {
	f := newFixture(t)
	f.cfg.WebhookSecret = ""
	s := f.build(t)
	w := webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Gitlab-Token", "") })
	assert.Equal(t, http.StatusNotFound, w.Code)
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Gitlab-Token", "anything") })
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestWebhookSecretFile(t *testing.T) {
	f := newFixture(t)
	f.cfg.WebhookSecret = ""
	p := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(p, []byte("filesecret\n"), 0o600))
	f.cfg.WebhookSecretFile = p
	s := f.build(t)
	w := webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Gitlab-Token", "filesecret") })
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// metricValue reads one counter/gauge sample from a registry (the
// prometheus testutil package would need new go.mod entries).
func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			ok := true
			for _, lp := range m.GetLabel() {
				if want, has := labels[lp.GetName()]; has && want != lp.GetValue() {
					ok = false
				}
			}
			if ok {
				if m.GetCounter() != nil {
					return m.GetCounter().GetValue()
				}
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("metric %s %v not found", name, labels)
	return 0
}
