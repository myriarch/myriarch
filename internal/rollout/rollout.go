// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package rollout implements the config and version rollout engine: planner
// (creates rollouts from desired-state differences), wave state machine,
// gating (health wait, failure thresholds, approvals, maintenance windows),
// executor dispatch and operator actions.
//
// Semantics: docs/concepts/rollouts.md and docs/architecture.md ("Rollout engine").
//
// Persistence and crash safety: all state lives in the database (rollouts,
// waves, targets). The engine keeps no state between ticks except caches, so
// a new leader continues exactly where the old one stopped. Status changes
// use compare-and-set, targets optimistic concurrency, and every step is
// idempotent or reconciled on the next tick.
package rollout

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/executor"
	fleetmetrics "github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// Deps are the dependencies of the engine.
type Deps struct {
	Store      store.Store
	Bus        eventbus.Bus
	Control    control.AgentControl
	Executors  executor.Registry
	Notifier   notify.Notifier
	Audit      audit.Recorder
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// Now is the clock (tests); default time.Now.
	Now func() time.Time
}

const (
	defaultTickInterval = 5 * time.Second
	planInterval        = time.Minute
	minTickGap          = time.Second
)

// Annotation keys stored in Rollout.Annotations (persisted, so that every
// replica and a new leader can act on them).
const (
	annExecutorRef     = "executorRef"
	annUpdateParams    = "updateParams"
	annDistribution    = "distribution"
	annAckedFailures   = "ackedFailures"
	annRollbackStarted = "rollbackStartedAt"
	annRollbackSent    = "rollbackSent"
	// annHealthTrack: per verifying version target, what the engine observed of
	// the collector's health after the new version appeared (crash-loop detection).
	annHealthTrack = "healthTrack"
)

// Engine runs rollouts.
type Engine struct {
	cfg  config.RolloutsConfig
	deps Deps
	log  *slog.Logger
	now  func() time.Time

	// mu serializes Plan and tick (never two planning/ticking runs at once).
	mu sync.Mutex

	// Executor calls never run inside the critical section for longer than
	// the wait budget below (see dispatch.go).
	execWait     time.Duration // how long a tick waits for one call
	execTimeout  time.Duration // hard limit of one call
	tickWait     time.Duration // total waiting budget per tick
	tickWaitLeft time.Duration // remaining budget of the running tick (guarded by mu)
	dmu          sync.Mutex
	dispatches   map[uuid.UUID]*dispatch // one outstanding executor call per rollout

	fleetsMu sync.RWMutex
	fleets   map[string]struct{} // fleet names of the last planned snapshot (nil: unknown)
	// lastTarget is the effective target version this engine last wrote to the
	// members of each fleet (guarded by fleetsMu).
	lastTarget map[string]string
	// lastPlanned is the effective target version of each fleet that this engine last recorded as planned
	// (store.PlannedTargetKey), so that the record is written when it changes only (guarded by fleetsMu). Run forgets
	// it at the start of every term of leadership: another leader may have written the record in between.
	lastPlanned map[string]string
	// bridgeNoted is the target version for which each OpAMP Bridge was noted as
	// left out of version rollouts (guarded by fleetsMu).
	bridgeNoted map[domain.AgentID]string

	// tzWarned holds the policy names the engine already warned about because
	// their maintenance windows depend on rollouts.defaultTimezone.
	tzWarnMu sync.Mutex
	tzWarned map[string]struct{}

	met *metrics
	// cycle times one cycle of a leader job (histogram myriarch_job_cycle_duration_seconds).
	cycle func(job string) func()
	// baseCtx is the context of the running tick before its time budget was
	// applied (the leader context in production). Background executor calls
	// derive from it, so they outlive the budget of the tick that started them
	// and still end with the leadership. Set by Tick (holding mu).
	baseCtx context.Context
}

// NewEngine creates the engine.
func NewEngine(cfg config.RolloutsConfig, deps Deps) *Engine {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = defaultTickInterval
	}
	if cfg.DefaultTimezone == "" {
		cfg.DefaultTimezone = "UTC"
	}
	if cfg.ExecutorTimeout <= 0 {
		cfg.ExecutorTimeout = defaultExecTimeout
	}
	e := &Engine{cfg: cfg, deps: deps, now: deps.Now,
		execWait: defaultExecWait, execTimeout: cfg.ExecutorTimeout, tickWait: defaultTickWait,
		dispatches: map[uuid.UUID]*dispatch{}}
	if e.now == nil {
		e.now = func() time.Time { return time.Now().UTC() }
	}
	e.log = deps.Logger
	if e.log == nil {
		e.log = slog.Default()
	}
	e.log = e.log.With("component", "rollout")
	if deps.Notifier == nil {
		e.deps.Notifier = notify.Nop{}
	}
	e.met = newMetrics(deps.Registerer)
	e.cycle = fleetmetrics.CycleTimer(deps.Registerer)
	return e
}

// cycleBudget bounds one tick or planning run: a stuck database call or a very
// large fleet must not hold the engine mutex (and with it the planner and the
// GitOps reconciliation) forever.
const cycleBudget = 60 * time.Second

// Run (leader only) ticks every cfg.TickInterval (and early on
// AgentStateChanged/RolloutChanged events) until ctx is done.
func (e *Engine) Run(ctx context.Context) error {
	events, cancel := e.deps.Bus.Subscribe(eventbus.AgentStateChanged, eventbus.RolloutChanged, eventbus.FleetVersionChanged)
	defer cancel()

	e.forgetPlanned()
	e.safePlan(ctx)
	e.safeTick(ctx)
	lastTick := e.now()

	ticker := time.NewTicker(e.cfg.TickInterval)
	defer ticker.Stop()
	planTicker := time.NewTicker(planInterval)
	defer planTicker.Stop()
	var throttle <-chan time.Time
	var throttleTimer *time.Timer
	defer func() {
		if throttleTimer != nil {
			throttleTimer.Stop()
		}
	}()
	// A changed UI version selection is planned at once (a person waits for
	// the rollout), with the same minimum gap as the ticks.
	lastPlan := e.now()
	var planThrottle <-chan time.Time
	var planTimer *time.Timer
	defer func() {
		if planTimer != nil {
			planTimer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			e.safeTick(ctx)
			lastTick = e.now()
		case <-planTicker.C:
			e.safePlan(ctx)
			e.safeTick(ctx)
			lastTick, lastPlan = e.now(), e.now()
		case <-planThrottle:
			planThrottle = nil
			e.safePlan(ctx)
			e.safeTick(ctx)
			lastTick, lastPlan = e.now(), e.now()
		case <-throttle:
			throttle = nil
			e.safeTick(ctx)
			lastTick = e.now()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			planWanted := ev.Type == eventbus.FleetVersionChanged
			for drained := false; !drained; { // coalesce bursts
				select {
				case more := <-events:
					planWanted = planWanted || more.Type == eventbus.FleetVersionChanged
				default:
					drained = true
				}
			}
			if planWanted {
				if gap := e.now().Sub(lastPlan); gap >= minTickGap {
					e.safePlan(ctx)
					lastPlan = e.now()
					e.safeTick(ctx)
					lastTick = e.now()
					continue
				} else if planThrottle == nil {
					if planTimer == nil {
						planTimer = time.NewTimer(minTickGap - gap)
					} else {
						planTimer.Reset(minTickGap - gap)
					}
					planThrottle = planTimer.C
				}
			}
			if throttle != nil {
				continue
			}
			if gap := e.now().Sub(lastTick); gap >= minTickGap {
				e.safeTick(ctx)
				lastTick = e.now()
			} else {
				if throttleTimer == nil {
					throttleTimer = time.NewTimer(minTickGap - gap)
				} else {
					throttleTimer.Reset(minTickGap - gap)
				}
				throttle = throttleTimer.C
			}
		}
	}
}

func (e *Engine) safePlan(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("rollout planner panicked", "panic", r)
		}
	}()
	if err := e.Plan(ctx); err != nil && ctx.Err() == nil {
		e.log.Error("rollout planning failed", "error", err)
	}
}

func (e *Engine) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("rollout tick panicked", "panic", r)
		}
	}()
	if err := e.Tick(ctx); err != nil && ctx.Err() == nil {
		e.log.Error("rollout tick failed", "error", err)
	}
}

// ---------------------------------------------------------------- helpers ----

func (e *Engine) publishChanged(ctx context.Context, r *domain.Rollout) {
	if e.deps.Bus == nil {
		return
	}
	if err := e.deps.Bus.Publish(ctx, eventbus.RolloutChanged, eventbus.RolloutChangedPayload{
		RolloutID: r.ID, FleetName: r.FleetName, Status: r.Status,
	}); err != nil {
		e.log.Warn("publish rollout.changed failed", "rollout_id", r.ID, "error", err)
	}
}

func (e *Engine) audit(ctx context.Context, ev domain.AuditEvent) {
	if e.deps.Audit == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = e.now()
	}
	audit.Enrich(ctx, &ev) // request id and source IP of operator actions
	e.deps.Audit.Record(ctx, ev)
}

func (e *Engine) auditSystem(ctx context.Context, action string, r *domain.Rollout, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["fleet"], details["kind"] = r.FleetName, string(r.Kind)
	ev := audit.System(domain.ActorSystem, action, "rollout", r.ID.String(), details)
	ev.Time = e.now()
	e.audit(ctx, ev)
}

func (e *Engine) notify(ctx context.Context, n notify.Notification) {
	if n.Time.IsZero() {
		n.Time = e.now()
	}
	if err := e.deps.Notifier.Notify(ctx, n); err != nil {
		e.log.Warn("notification failed", "event", n.Event, "error", err)
	}
}

func rolloutFields(r *domain.Rollout, extra ...notify.Field) []notify.Field {
	f := []notify.Field{
		{Name: "Fleet", Value: r.FleetName},
		{Name: "Kind", Value: string(r.Kind)},
		{Name: "Target", Value: shortRef(r.Kind, r.ToRef)},
		{Name: "Targets", Value: fmt.Sprint(r.Counts.Total)},
	}
	return append(f, extra...)
}

func shortRef(kind domain.RolloutKind, ref string) string {
	if kind == domain.RolloutKindConfig && len(ref) > 12 {
		return ref[:12]
	}
	return ref
}

func rolloutLink(r *domain.Rollout) string { return "/rollouts/" + r.ID.String() }

func (e *Engine) notifyRollout(ctx context.Context, r *domain.Rollout, ev notify.EventType, sev notify.Severity, title, summary, dedupSuffix string, extra ...notify.Field) {
	key := string(ev) + "/" + r.ID.String()
	if dedupSuffix != "" {
		key += "/" + dedupSuffix
	}
	e.notify(ctx, notify.Notification{
		Event: ev, Severity: sev, Title: title, Summary: summary,
		Link: rolloutLink(r), Fields: rolloutFields(r, extra...), DedupKey: key,
	})
}

// casStatus changes the rollout status with compare-and-set. It returns
// false if the status was changed concurrently.
func (e *Engine) casStatus(ctx context.Context, r *domain.Rollout, from []domain.RolloutStatus, to domain.RolloutStatus, reason string) (bool, error) {
	ok, err := e.deps.Store.Rollouts().CompareAndSetStatus(ctx, r.ID, from, to, reason)
	if err != nil {
		return false, fmt.Errorf("set rollout %s status %s: %w", r.ID, to, err)
	}
	if ok {
		r.Status, r.StatusReason = to, reason
		e.met.transitions.WithLabelValues(string(to)).Inc()
		e.log.Info("rollout status changed", "rollout_id", r.ID, "fleet", r.FleetName, "kind", r.Kind, "to", to, "reason", reason)
	}
	return ok, nil
}

func fmtReason(prefix, reason string) string {
	if reason == "" {
		return prefix
	}
	return prefix + ": " + reason
}

// principalString renders the acting principal for reasons and details.
func principalString(p *authz.Principal) string { return p.String() }

func shortID(id uuid.UUID) string { return strings.SplitN(id.String(), "-", 2)[0] }
