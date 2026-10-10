// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package executor defines the pluggable version update executors.
//
// Implementations:
//
//	internal/executor/updaterexec  ExecutorUpdater: OpAMP package offers to myriarch-updater
//	internal/executor/webhookexec  ExecutorWebhook and ExecutorAWX (fail closed: targets must be listed in executors.allowedWebhookHosts)
//	internal/executor/manualexec   ExecutorManual: no external call, the version changes outside of myriarchd
//
// Completion of a version update is NOT reported by executors. The rollout
// engine observes the collector agent's reported service.version (and
// health); executors only start the update and report immediate errors.
// Executors must be idempotent: Start may be called again for the same
// target (retry), and must not create duplicate external jobs for targets
// that are already running when called with the same RolloutID/Attempt.
package executor

import (
	"context"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
)

// Target is one collector agent to update.
type Target struct {
	AgentID  domain.AgentID `json:"agentId"`
	Hostname string         `json:"hostname"`
	OSType   string         `json:"osType"`
	Arch     string         `json:"arch"`
	// Attributes is the agent AttributeView (attributes + labels.*).
	Attributes domain.AttributeView `json:"attributes"`
	// CurrentVersion reported by the collector.
	CurrentVersion string `json:"currentVersion"`
	// UpdaterAgentID is the linked myriarch-updater (nil if none).
	UpdaterAgentID *domain.AgentID `json:"updaterAgentId,omitempty"`
	// Attempt counter (1-based) of the rollout target.
	Attempt int `json:"attempt"`
}

// UpdateRequest asks an executor to update targets to a version.
type UpdateRequest struct {
	RolloutID    uuid.UUID `json:"rolloutId"`
	Wave         int       `json:"wave"`
	Fleet        string    `json:"fleet"`
	Distribution string    `json:"distribution"`
	// ToVersion without "v" ("0.163.0").
	ToVersion string   `json:"toVersion"`
	Targets   []Target `json:"targets"`
	// Executor is the fleet's update spec (type, executorRef, params).
	Spec domain.UpdateSpec `json:"spec"`
	// Definition is the referenced Executor object (webhook/awx), nil for
	// the updater executor.
	Definition *domain.Executor `json:"definition,omitempty"`
}

// TargetResult is the immediate outcome for one target.
type TargetResult struct {
	AgentID domain.AgentID `json:"agentId"`
	// Started: the update was handed over (package offered / job launched).
	Started bool `json:"started"`
	// Error: why the update could not be started (target fails immediately).
	Error string `json:"error,omitempty"`
	// ExternalRef: e.g. AWX job id or webhook delivery id.
	ExternalRef string `json:"externalRef,omitempty"`
	// RetryLater: the target cannot be started YET (e.g. its artifact is
	// still being downloaded and verified). Started=false and Error=="" must
	// accompany it. The rollout engine keeps the target pending, does not
	// count it as a failure and calls Start again on its next tick.
	RetryLater bool `json:"retryLater,omitempty"`
}

// CancelRequest withdraws updates (e.g. rollout aborted or superseded): the
// updater executor clears the package offers (+ DesiredStateChanged); the
// webhook executor POSTs a WebhookPayload with event
// "collector.update.cancel" to the same URL; the AWX executor cancels
// launched jobs (POST /api/v2/jobs/<id>/cancel/). All best effort.
type CancelRequest struct {
	RolloutID uuid.UUID        `json:"rolloutId"`
	AgentIDs  []domain.AgentID `json:"agentIds"`
	// Refs maps the cancelled agents to the TargetResult.ExternalRef returned
	// by Start (e.g. "awx-job:12"); agents without a reference are absent.
	Refs map[domain.AgentID]string `json:"refs,omitempty"`
	Spec domain.UpdateSpec
	// Definition for webhook/awx executors (nil for updater).
	Definition *domain.Executor
}

// Executor starts version updates.
type Executor interface {
	// Type returns the executor type handled.
	Type() domain.ExecutorType
	// Validate performs the ENVIRONMENT-DEPENDENT checks of a fleet/executor
	// configuration on the myriarchd host: secret env var / file present,
	// target host allowed by executors.allowedWebhookHosts, artifact source
	// reachable is NOT checked. Called by the rollout engine before it starts
	// targets of a version rollout; issues with severity error fail the
	// targets with the issue message (rollout status per policy). Static
	// checks (type, URL syntax, required fields, secret reference present)
	// are part of GitOps validation in internal/blueprint and must not depend
	// on the environment (myrctl runs in CI). spec.ExecutorRef has been
	// resolved into def (nil for the updater executor).
	Validate(spec domain.UpdateSpec, def *domain.Executor) domain.Issues
	// Start triggers updates. It returns one TargetResult per requested
	// target (same order). A non-nil error means the whole call failed
	// (all targets are retried later by the engine).
	Start(ctx context.Context, req UpdateRequest) ([]TargetResult, error)
	// Cancel best-effort withdraws pending updates.
	Cancel(ctx context.Context, req CancelRequest) error
}

// Registry resolves executors by type.
type Registry interface {
	Get(t domain.ExecutorType) (Executor, bool)
}

// MapRegistry is a simple Registry.
type MapRegistry map[domain.ExecutorType]Executor

// Get implements Registry.
func (m MapRegistry) Get(t domain.ExecutorType) (Executor, bool) {
	e, ok := m[t]
	return e, ok
}

// WebhookPayload is the JSON body POSTed by the webhook executor. The AWX
// executor adds the same object as extra_vars key "myriarch" (next to the
// configured extraVars). Webhook headers: X-Myriarch-Delivery (uuid),
// X-Myriarch-Timestamp (unix seconds) and, when a secret is configured,
// X-Myriarch-Signature: "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)).
// The same signature scheme is used by the generic webhook notifier.
type WebhookPayload struct {
	Event        string    `json:"event"` // "collector.update" | "collector.update.cancel"
	DeliveryID   uuid.UUID `json:"deliveryId"`
	RolloutID    uuid.UUID `json:"rolloutId"`
	Wave         int       `json:"wave"`
	Fleet        string    `json:"fleet"`
	Distribution string    `json:"distribution"`
	ToVersion    string    `json:"toVersion"`
	// Params from the fleet's update spec.
	Params  map[string]any  `json:"params,omitempty"`
	Targets []WebhookTarget `json:"targets"`
}

// WebhookTarget is one target in a WebhookPayload.
type WebhookTarget struct {
	AgentID        domain.AgentID    `json:"agentId"`
	Hostname       string            `json:"hostname"`
	OSType         string            `json:"osType"`
	Arch           string            `json:"arch"`
	CurrentVersion string            `json:"currentVersion"`
	Attributes     map[string]string `json:"attributes"`
}
