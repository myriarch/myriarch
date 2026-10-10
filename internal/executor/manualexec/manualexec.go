// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package manualexec implements executor type "manual": version rollouts that
// are created and tracked by myriarchd while the version itself is changed
// outside of it (for example by an image tag merge request or an operator on
// the host).
//
// Start reports every target as started without any external call; the
// rollout engine then waits, like for every executor, until the collector
// reports the target version, and the policy's applyTimeout bounds the wait.
// Cancel has nothing to withdraw. The executor needs no configuration and no
// network access.
package manualexec

import (
	"context"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
)

// ExternalRef marks the targets of this executor.
const ExternalRef = "manual"

// New returns the manual executor.
func New() executor.Executor { return exec{} }

type exec struct{}

// Type implements executor.Executor.
func (exec) Type() domain.ExecutorType { return domain.ExecutorManual }

// Validate implements executor.Executor: there is nothing environment
// dependent to check.
func (exec) Validate(domain.UpdateSpec, *domain.Executor) domain.Issues { return nil }

// Start implements executor.Executor: it only acknowledges the targets.
func (exec) Start(_ context.Context, req executor.UpdateRequest) ([]executor.TargetResult, error) {
	results := make([]executor.TargetResult, len(req.Targets))
	for i, t := range req.Targets {
		results[i] = executor.TargetResult{AgentID: t.AgentID, Started: true, ExternalRef: ExternalRef}
	}
	return results, nil
}

// Cancel implements executor.Executor: nothing was started elsewhere.
func (exec) Cancel(context.Context, executor.CancelRequest) error { return nil }
