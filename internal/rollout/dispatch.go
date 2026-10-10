// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
)

// Executor I/O (webhook/AWX calls, package offers) can be slow or hang. The
// engine therefore never waits for it inside a tick longer than a small
// budget:
//
//   - a call runs in a background goroutine bounded by execTimeout;
//   - the tick waits for it up to execWait (and at most tickWait per tick in
//     total), so fast executors behave exactly like synchronous ones;
//   - a call that is still running keeps its targets untouched (pending): the
//     rollout starts nothing new until the result is collected on a later
//     tick, other rollouts, the planner and the next ticks proceed.
//
// Crash safety needs no new persisted state: the targets stay pending in the
// database until the result is applied, a new leader simply calls the executor
// again and executors are idempotent per rollout/attempt (executor.Executor).
const (
	defaultExecWait    = 2 * time.Second
	defaultExecTimeout = 2 * time.Minute
	defaultTickWait    = 10 * time.Second
)

type dispatchKind int

const (
	dispatchStart dispatchKind = iota
	dispatchRollback
)

// dispatch is one outstanding executor call of a rollout.
type dispatch struct {
	kind   dispatchKind
	wave   int
	to     string // version the call asks for
	agents []domain.AgentID
	done   chan struct{}
	// lctx is the context the call was launched under (the leader/tick
	// context). Once it is done, the leadership term of the call has ended and
	// its outcome is no longer authoritative.
	lctx context.Context

	// set before done is closed
	results []executor.TargetResult
	err     error
}

func (d *dispatch) finished() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

// outstanding reports whether a call of the rollout is still running.
func (e *Engine) outstanding(id uuid.UUID) bool {
	e.dmu.Lock()
	defer e.dmu.Unlock()
	d := e.dispatches[id]
	return d != nil && !d.finished()
}

// ended reports that the context the call was launched under is done, e.g.
// because the leadership term ended while the call ran.
func (d *dispatch) ended() bool {
	return d.lctx != nil && d.lctx.Err() != nil
}

// takeFinished removes and returns the finished call of the rollout (nil if
// there is none or it is still running). The outcome of a call whose launch
// context has ended is discarded: its error (typically context.Canceled) says
// nothing about the targets, they stay pending and the current term calls the
// executor again (executors are idempotent per rollout/attempt).
func (e *Engine) takeFinished(id uuid.UUID) *dispatch {
	e.dmu.Lock()
	d := e.dispatches[id]
	if d == nil || !d.finished() {
		e.dmu.Unlock()
		return nil
	}
	delete(e.dispatches, id)
	e.dmu.Unlock()
	if d.ended() {
		e.log.Info("discarding the result of an executor call from an ended leadership term", "rollout_id", id, "targets", len(d.agents))
		return nil
	}
	return d
}

// launch starts call in the background and waits for it within the tick
// budget. It reports whether the call finished (its entry is then removed
// and the caller applies d); otherwise it stays registered and is collected
// on a later tick. Only the tick (holding e.mu) calls launch.
func (e *Engine) launch(ctx context.Context, id uuid.UUID, d *dispatch, call func(context.Context) ([]executor.TargetResult, error)) bool {
	d.done = make(chan struct{})
	// the call is not bound to the time budget of this tick (it may run longer
	// than one tick), only to the leader context
	parent := ctx
	if e.baseCtx != nil {
		parent = e.baseCtx
	}
	d.lctx = parent
	e.dmu.Lock()
	e.dispatches[id] = d
	e.dmu.Unlock()
	go func() {
		defer close(d.done)
		cctx, cancel := context.WithTimeout(parent, e.execTimeout)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				d.results, d.err = nil, fmt.Errorf("executor panicked: %v", r)
			}
		}()
		d.results, d.err = call(cctx)
	}()

	wait := min(e.execWait, e.tickWaitLeft)
	if wait > 0 {
		start := time.Now()
		timer := time.NewTimer(wait)
		select {
		case <-d.done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		e.tickWaitLeft = max(0, e.tickWaitLeft-time.Since(start))
	}
	if d.finished() {
		e.dmu.Lock()
		delete(e.dispatches, id)
		e.dmu.Unlock()
		return true
	}
	e.log.Info("executor call still running; its result is applied on a later tick", "rollout_id", id, "targets", len(d.agents))
	return false
}

// sweepDispatches handles finished calls of rollouts that are no longer
// active (aborted or superseded while the call ran): updates they started are
// withdrawn again (best effort).
func (e *Engine) sweepDispatches(ctx context.Context, active map[uuid.UUID]bool) {
	e.dmu.Lock()
	var stale map[uuid.UUID]*dispatch
	for id, d := range e.dispatches {
		if !active[id] && d.finished() {
			if stale == nil {
				stale = map[uuid.UUID]*dispatch{}
			}
			stale[id] = d
			delete(e.dispatches, id)
		}
	}
	e.dmu.Unlock()
	for id, d := range stale {
		if d.kind != dispatchStart || d.err != nil {
			continue
		}
		var started []domain.AgentID
		refs := map[string]any{}
		for _, res := range d.results {
			if res.Started && res.Error == "" {
				started = append(started, res.AgentID)
				if res.ExternalRef != "" {
					refs[res.AgentID.String()] = res.ExternalRef
				}
			}
		}
		if len(started) == 0 {
			continue
		}
		r, err := e.deps.Store.Rollouts().Get(ctx, id)
		if err != nil {
			e.log.Warn("withdraw updates of an inactive rollout failed", "rollout_id", id, "error", err)
			continue
		}
		e.cancelExecutor(ctx, r, started, refs)
	}
}

// bestEffort runs fn in the background (bounded by execTimeout, detached from
// the caller's cancellation) and waits at most execWait for it.
func (e *Engine) bestEffort(ctx context.Context, fn func(context.Context)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("executor call panicked", "panic", r)
			}
		}()
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.execTimeout)
		defer cancel()
		fn(cctx)
	}()
	timer := time.NewTimer(e.execWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		e.log.Warn("executor call is slow; continuing without waiting for it")
	}
}
