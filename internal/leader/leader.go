// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package leader defines leader election for singleton jobs.
//
// The PostgreSQL advisory lock implementation lives in
// internal/leader/pglock.
// AlwaysLeader (this package) is provided for tests and single-replica dev.
//
// Singleton jobs (all leader-only): GitOps syncer polling, rollout engine,
// release tracker, artifact prefetch, offline sweeper / replica reaper,
// retention. They are started by cmd/myriarchd via Elector.Run and MUST stop
// promptly when their context is cancelled (leadership lost).
package leader

import (
	"context"
	"sync/atomic"
)

// Elector runs a function while this replica holds leadership.
type Elector interface {
	// Run blocks until ctx is cancelled. Whenever leadership is acquired it
	// calls onElected with a context that is cancelled when leadership is
	// lost (or ctx is done) and waits for onElected to return before trying
	// to re-acquire. Implementations retry acquisition every few seconds.
	Run(ctx context.Context, onElected func(ctx context.Context)) error
	// IsLeader reports whether this replica currently holds leadership.
	IsLeader() bool
}

// AlwaysLeader is an Elector that is always leader (tests, dev).
type AlwaysLeader struct{ leading atomic.Bool }

// Run calls onElected once with ctx and returns when ctx is done.
func (a *AlwaysLeader) Run(ctx context.Context, onElected func(ctx context.Context)) error {
	a.leading.Store(true)
	defer a.leading.Store(false)
	onElected(ctx)
	<-ctx.Done()
	return nil
}

// IsLeader reports true while Run is active.
func (a *AlwaysLeader) IsLeader() bool { return a.leading.Load() }
