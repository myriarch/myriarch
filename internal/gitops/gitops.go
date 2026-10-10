// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package gitops parses the GitOps repository format and runs the syncer
// (go-git clone/fetch, poll + webhook, validation, atomic snapshot apply).
//
// Format: docs/concepts/gitops.md and docs/reference/gitops-schema.md.
package gitops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// Deps are the dependencies of the syncer.
type Deps struct {
	Store    store.Store
	Bus      eventbus.Bus
	Renderer *blueprint.Renderer
	Audit    audit.Recorder
	// OnApplied is called (leader) after a new snapshot was applied: the
	// caller re-assigns agents and plans rollouts. It must be idempotent: the
	// syncer persists the revision up to which it succeeded and calls it again
	// (with empty changes) on the next sync cycle, also on another leader, as
	// long as it returned an error or was interrupted.
	OnApplied  func(ctx context.Context, snap *domain.GitOpsSnapshot, changes store.ObjectChanges) error
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// Notifier is optional (nil = no notifications). Used for
	// gitops.sync_failed. (Added after the initial interface; run.go passes
	// the dispatcher.)
	Notifier notify.Notifier
	// HTTPClient (optional) is the client of Git over HTTP(S). It is installed
	// as the transport of go-git, so the outbound destination policy, redirect
	// limits and timeouts apply to Git, too. Responses are limited to
	// gitops.maxRepoBytes.
	// Policy (optional) is the outbound destination policy. It is applied to
	// the dial of SSH remotes (the HTTP(S) path uses HTTPClient). Nil or a
	// permissive policy (dev mode) allows every destination.
	Policy     *netguard.Policy
	HTTPClient *http.Client
}

const (
	// unchangedRecordInterval limits "unchanged" entries in the sync history.
	unchangedRecordInterval = time.Hour
	fetchTimeout            = 3 * time.Minute
	// validateBudget bounds parsing and validation of one revision, syncBudget
	// the whole cycle including reassignment of the agents.
	validateBudget  = 2 * time.Minute
	syncBudget      = 10 * time.Minute
	maxStoredIssues = 200
	// webhook rate limit per replica: 1 request/s, bursts of 10
	webhookRate  = 1
	webhookBurst = 10
)

// Syncer synchronizes the GitOps repository.
type Syncer struct {
	cfg  config.GitOpsConfig
	deps Deps
	log  *slog.Logger
	// Now is the clock (tests).
	now func() time.Time

	src           source
	webhookSecret []byte

	hookLimiter *rate.Limiter
	cycle       func(job string) func()

	mu                sync.Mutex // serializes syncs
	lastInvalid       string     // revision that was found invalid (in-memory dedup)
	lastUnchangedAt   time.Time
	lastErrorRecorded time.Time
	lastErrorText     string
	consecutiveErrors int

	syncTotal   *prometheus.CounterVec
	lastSuccess prometheus.Gauge
}

// source fetches a revision of the repository.
type source interface {
	// describe returns the credential-free source URL for the status.
	describe() string
	// fetch updates the local copy and returns the current revision.
	fetch(ctx context.Context) (*fetched, error)
}

// NewSyncer creates the syncer.
func NewSyncer(cfg config.GitOpsConfig, deps Deps) (*Syncer, error) {
	s := &Syncer{cfg: cfg, deps: deps, now: func() time.Time { return time.Now().UTC() }}
	s.log = deps.Logger
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "gitops")
	if cfg.PollInterval <= 0 {
		s.cfg.PollInterval = 60 * time.Second
	}
	if s.cfg.Branch == "" {
		s.cfg.Branch = "main"
	}
	s.cfg.Path = strings.Trim(strings.TrimSpace(cfg.Path), "/")
	s.hookLimiter = rate.NewLimiter(webhookRate, webhookBurst)
	s.cycle = metrics.CycleTimer(deps.Registerer)
	def := config.Default().GitOps
	if s.cfg.MaxRepoBytes <= 0 {
		s.cfg.MaxRepoBytes = def.MaxRepoBytes
	}
	if s.cfg.MaxFiles <= 0 {
		s.cfg.MaxFiles = def.MaxFiles
	}
	if s.cfg.MaxYAMLBytes <= 0 {
		s.cfg.MaxYAMLBytes = def.MaxYAMLBytes
	}
	srcCfg := s.cfg
	if cfg.Enabled && deps.HTTPClient != nil {
		// gitops.auth.caFile extends the trust store of the shared client (it is
		// read here, so a broken file stops myriarchd at startup). A disabled
		// syncer has no source: it reads no file and installs nothing.
		var extraCA []byte
		if srcCfg.Auth.CAFile != "" {
			b, err := os.ReadFile(srcCfg.Auth.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read gitops.auth.caFile: %w", err)
			}
			extraCA = b
		}
		if err := installGitHTTP(deps.HTTPClient, s.cfg.MaxRepoBytes, extraCA); err != nil {
			return nil, err
		}
		if len(extraCA) > 0 {
			srcCfg.Auth.CAFile = "" // already part of the client
		}
	}

	// The webhook secret exists only while GitOps is enabled: a disabled
	// syncer reads no secret file, ignores an inline secret and the webhook
	// endpoint answers 404.
	if cfg.Enabled {
		secret := cfg.WebhookSecret
		if cfg.WebhookSecretFile != "" {
			b, err := os.ReadFile(cfg.WebhookSecretFile)
			if err != nil {
				return nil, fmt.Errorf("read gitops webhook secret file: %w", err)
			}
			secret = strings.TrimSpace(string(b))
		}
		s.webhookSecret = []byte(secret)
	}

	if cfg.Enabled {
		src, err := newSource(srcCfg, deps.Policy)
		if err != nil {
			return nil, fmt.Errorf("gitops source: %w", err)
		}
		s.src = src
	}

	reg := deps.Registerer
	s.syncTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_gitops_sync_total", Help: "GitOps sync attempts by result (applied, unchanged, invalid, error).",
	}, []string{"result"})
	s.lastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "myriarch_gitops_last_success_timestamp_seconds", Help: "Unix time of the last successfully applied or confirmed GitOps sync.",
	})
	if reg != nil {
		s.syncTotal = registerOrGet(reg, s.syncTotal)
		s.lastSuccess = registerOrGet(reg, s.lastSuccess)
	}
	return s, nil
}

// limits are the parse limits of one validation run.
func (s *Syncer) limits() Limits {
	return Limits{MaxFiles: s.cfg.MaxFiles, MaxFileBytes: maxFileSize, MaxTotalBytes: s.cfg.MaxYAMLBytes, Deadline: time.Now().Add(validateBudget)}
}

func registerOrGet[T prometheus.Collector](reg prometheus.Registerer, c T) T {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(T); ok {
				return existing
			}
		}
	}
	return c
}

// Run (leader only) performs an initial sync, then polls every
// cfg.PollInterval and on eventbus.GitOpsSyncRequested events.
func (s *Syncer) Run(ctx context.Context) error {
	if !s.cfg.Enabled || s.src == nil {
		s.log.Info("gitops syncer disabled")
		<-ctx.Done()
		return nil
	}
	events, cancel := s.deps.Bus.Subscribe(eventbus.GitOpsSyncRequested)
	defer cancel()

	s.syncSafe(ctx, "startup", "")
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.syncSafe(ctx, "poll", "")
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			var pl eventbus.GitOpsSyncRequestedPayload
			_ = ev.Decode(&pl)
			// Coalesce a burst of requests into one sync.
			for drained := false; !drained; {
				select {
				case <-events:
				default:
					drained = true
				}
			}
			trigger := pl.Trigger
			if trigger == "" {
				trigger = "manual"
			}
			s.syncSafe(ctx, trigger, pl.RequestedBy)
		}
	}
}

// syncSafe runs one sync and logs errors; panics must not kill the loop.
func (s *Syncer) syncSafe(ctx context.Context, trigger, by string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("gitops sync panicked", "panic", r)
		}
	}()
	if err := s.SyncNow(ctx, trigger); err != nil && ctx.Err() == nil {
		s.log.Warn("gitops sync failed", "trigger", trigger, "error", err)
	}
}

// RequestSync asks the leader to sync now (any replica; publishes
// eventbus.GitOpsSyncRequested).
func (s *Syncer) RequestSync(ctx context.Context, trigger, requestedBy string) error {
	if trigger == "" {
		trigger = "manual"
	}
	return s.deps.Bus.Publish(ctx, eventbus.GitOpsSyncRequested, eventbus.GitOpsSyncRequestedPayload{Trigger: trigger, RequestedBy: requestedBy})
}

// ----------------------------------------------------------------- webhook --

// WebhookHandler validates the webhook secret (GitLab X-Gitlab-Token or
// GitHub X-Hub-Signature-256) and requests a sync. Mounted by fleetapi as
// operation gitopsWebhook (api.AuthCustom).
func (s *Syncer) WebhookHandler() api.Route {
	return api.Route{
		Method:       http.MethodPost,
		Pattern:      "/gitops/webhook",
		OperationID:  "gitopsWebhook",
		Auth:         api.AuthCustom,
		MaxBodyBytes: 1 << 20,
		Handler:      s.handleWebhook,
	}
}

// auditWebhook records an accepted or rejected webhook call.
func (s *Syncer) auditWebhook(r *http.Request, provider, result, note string, err error) {
	if s.deps.Audit == nil {
		return
	}
	details := map[string]any{"provider": provider, "outcome": result}
	if note != "" {
		details["note"] = note
	}
	s.deps.Audit.Record(r.Context(), audit.FromRequest(r, "gitops.webhook", "gitops", "", details, err))
}

func (s *Syncer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if len(s.webhookSecret) == 0 {
		api.WriteError(w, r, fmt.Errorf("%w: gitops webhook is not configured", domain.ErrNotFound))
		return
	}
	// A sync request is idempotent and coalesced, but the endpoint is public:
	// bound the rate of requests per replica.
	if !s.hookLimiter.Allow() {
		w.Header().Set("Retry-After", "1")
		api.WriteError(w, r, fmt.Errorf("%w: too many webhook requests", domain.ErrTooManyRequests))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		api.WriteError(w, r, fmt.Errorf("%w: cannot read request body: %v", domain.ErrInvalidArgument, err))
		return
	}
	provider := ""
	switch {
	case r.Header.Get("X-Gitlab-Token") != "":
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Gitlab-Token")), s.webhookSecret) == 1 {
			provider = "gitlab"
		}
	case r.Header.Get("X-Hub-Signature-256") != "":
		if verifyGitHubSignature(s.webhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
			provider = "github"
		}
	}
	if provider == "" {
		err := fmt.Errorf("%w: invalid or missing webhook secret", domain.ErrUnauthorized)
		s.auditWebhook(r, "", "rejected", "", err)
		api.WriteError(w, r, err)
		return
	}
	// A delivery id is processed once (providers retry; an attacker could
	// replay a captured request): duplicates are acknowledged without a sync.
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		delivery = r.Header.Get("X-Gitlab-Event-UUID")
	}
	if delivery != "" && len(delivery) <= 128 {
		first, kerr := s.deps.Store.KV().PutIfAbsent(r.Context(), store.KVPrefixWebhookDelivery+provider+"/"+delivery, []byte("1"))
		if kerr == nil && !first {
			s.auditWebhook(r, provider, "duplicate", "", nil)
			api.WriteJSON(w, http.StatusAccepted, dtoAccepted("duplicate delivery ignored"))
			return
		}
	}
	if provider == "github" && r.Header.Get("X-GitHub-Event") == "ping" {
		api.WriteJSON(w, http.StatusAccepted, dtoAccepted("pong"))
		return
	}
	var payload struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Ref != "" && strings.HasPrefix(payload.Ref, "refs/heads/") && payload.Ref != "refs/heads/"+s.cfg.Branch {
		s.auditWebhook(r, provider, "ignored", "branch", nil)
		api.WriteJSON(w, http.StatusAccepted, dtoAccepted("ignored: push to branch "+strings.TrimPrefix(payload.Ref, "refs/heads/")))
		return
	}
	if err := s.RequestSync(r.Context(), "webhook", provider); err != nil {
		s.auditWebhook(r, provider, "error", "", err)
		api.WriteError(w, r, fmt.Errorf("%w: sync request failed: %v", domain.ErrUnavailable, err))
		return
	}
	s.auditWebhook(r, provider, "accepted", "", nil)
	api.WriteJSON(w, http.StatusAccepted, dtoAccepted("sync requested"))
}

func dtoAccepted(msg string) dto.ActionAccepted {
	return dto.ActionAccepted{Accepted: true, Message: msg}
}

func verifyGitHubSignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// redactRepoURL strips credentials and query parts from a repository URL.
func redactRepoURL(raw string) string {
	if strings.HasPrefix(raw, "dir:") || strings.HasPrefix(raw, "file://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// scp-like "git@host:org/repo.git" carries no secret.
		if i := strings.Index(raw, "@"); i >= 0 && !strings.Contains(raw[:i], "/") && !strings.Contains(raw, "://") {
			return raw
		}
		return "<invalid>"
	}
	u.User = nil
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}
