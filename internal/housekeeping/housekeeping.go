// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package housekeeping runs periodic retention and cleanup jobs (leader only).
//
// Jobs (hourly): delete agent events, audit events, finished rollouts and
// GitOps sync history older than the configured retention; purge agents
// offline longer than retention.offlineAgents; GC unreferenced rendered and
// effective configs; delete expired sessions.
//
// A retention of 0 disables the respective job (for retention.offlineAgents 0
// means "keep forever"). Every job runs independently: a failing job does not
// stop the others and is retried at the next run.
package housekeeping

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/store"
)

// Interval between runs and delay of the first run after leadership starts.
var (
	Interval     = time.Hour
	InitialDelay = 30 * time.Second
)

const jobTimeout = 10 * time.Minute

// Runner executes housekeeping jobs.
type Runner struct {
	cfg config.RetentionConfig
	st  store.Store
	log *slog.Logger
	now func() time.Time

	deleted  *prometheus.CounterVec
	failures *prometheus.CounterVec
	lastRun  prometheus.Gauge
}

// Result is the outcome of one run: deleted rows per job and errors per job.
type Result struct {
	Deleted map[string]int64
	Errors  map[string]error
}

// New creates the runner.
func New(cfg config.RetentionConfig, st store.Store, logger *slog.Logger, reg prometheus.Registerer) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		cfg: cfg, st: st, log: logger.With("component", "housekeeping"), now: func() time.Time { return time.Now().UTC() },
		deleted: metrics.Register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "myriarch_housekeeping_deleted_total", Help: "Rows deleted by housekeeping jobs.",
		}, []string{"job"})),
		failures: metrics.Register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "myriarch_housekeeping_failures_total", Help: "Failed housekeeping job runs.",
		}, []string{"job"})),
		lastRun: metrics.Register(reg, prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "myriarch_housekeeping_last_run_timestamp_seconds", Help: "Unix time of the last completed housekeeping run.",
		})),
	}
}

// SetClock overrides the clock (tests).
func (r *Runner) SetClock(now func() time.Time) { r.now = now }

type job struct {
	name string
	// retention is the age after which data is deleted; <= 0 disables the job
	// (jobs without retention use a negative value to stay enabled, see always).
	retention time.Duration
	always    bool
	run       func(ctx context.Context, cutoff time.Time) (int64, error)
}

func (r *Runner) jobs() []job {
	return []job{
		{name: "agent_events", retention: r.cfg.AgentEvents, run: r.st.Agents().DeleteEventsBefore},
		{name: "audit_events", retention: r.cfg.Audit, run: r.st.Audit().DeleteBefore},
		{name: "offline_agents", retention: r.cfg.OfflineAgents, run: r.st.Agents().DeleteOfflineBefore},
		{name: "finished_rollouts", retention: r.cfg.FinishedRollouts, run: r.st.Rollouts().DeleteFinishedBefore},
		{name: "unreferenced_configs", retention: r.cfg.UnreferencedConfig, run: r.st.Configs().GC},
		{name: "gitops_syncs", retention: r.cfg.GitOpsSyncHistory, run: r.st.GitOps().DeleteSyncsBefore},
		{name: "short_lived_markers", always: true, run: func(ctx context.Context, now time.Time) (int64, error) {
			var total int64
			for _, prefix := range []string{store.KVPrefixOIDCUsed, store.KVPrefixWebhookDelivery} {
				n, err := r.st.KV().DeleteOlderThan(ctx, prefix, now.Add(-store.KVMarkerRetention))
				if err != nil {
					return total, err
				}
				total += n
			}
			return total, nil
		}},
		{name: "expired_sessions", always: true, run: func(ctx context.Context, now time.Time) (int64, error) {
			return r.st.Identity().DeleteExpiredSessions(ctx, now)
		}},
	}
}

// RunOnce executes all enabled jobs once.
func (r *Runner) RunOnce(ctx context.Context) Result {
	res := Result{Deleted: map[string]int64{}, Errors: map[string]error{}}
	now := r.now()
	for _, j := range r.jobs() {
		if ctx.Err() != nil {
			break
		}
		if !j.always && j.retention <= 0 {
			continue
		}
		cutoff := now.Add(-j.retention)
		if j.always {
			cutoff = now
		}
		jctx, cancel := context.WithTimeout(ctx, jobTimeout)
		start := time.Now()
		n, err := j.run(jctx, cutoff)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				r.log.Error("housekeeping job failed", "job", j.name, "error", err)
				r.failures.WithLabelValues(j.name).Inc()
			}
			res.Errors[j.name] = err
			continue
		}
		res.Deleted[j.name] = n
		if n > 0 {
			r.deleted.WithLabelValues(j.name).Add(float64(n))
			r.log.Info("housekeeping deleted rows", "job", j.name, "rows", n, "duration", time.Since(start).Round(time.Millisecond))
		}
	}
	if ctx.Err() == nil {
		r.lastRun.Set(float64(now.Unix()))
	}
	return res
}

// Run blocks until ctx is done (leader only): first run after InitialDelay,
// then every Interval.
func (r *Runner) Run(ctx context.Context) error {
	timer := time.NewTimer(InitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			r.RunOnce(ctx)
			timer.Reset(Interval)
		}
	}
}
