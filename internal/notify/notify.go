// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package notify defines outbound notifications (MS Teams, Slack, generic
// webhook).
//
// This package only defines the interfaces and types. The implementation is
// internal/notify/channels (channel implementations + a Dispatcher that routes
// by config.NotificationsConfig).
package notify

import (
	"context"
	"time"
)

// EventType identifies notification events. Routing in the myriarchd config
// matches these names, with "*" suffix wildcards ("rollout.*").
type EventType string

const (
	EventReleaseNew            EventType = "release.new"
	EventReleaseBreaking       EventType = "release.breaking" // new release with breaking changes
	EventRolloutStarted        EventType = "rollout.started"
	EventRolloutWaitingApprove EventType = "rollout.waiting_approval"
	EventRolloutPaused         EventType = "rollout.paused"
	EventRolloutFailed         EventType = "rollout.failed"
	EventRolloutCompleted      EventType = "rollout.completed"
	EventRolloutRolledBack     EventType = "rollout.rolled_back"
	EventGitOpsSyncFailed      EventType = "gitops.sync_failed"
	EventTest                  EventType = "test"
	// Roadmap (not produced in MVP): "agents.offline_bulk".
)

// Severity of a notification.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Notification is channel independent; channels render it (Teams Adaptive
// Card, Slack blocks, JSON for webhooks).
type Notification struct {
	Event    EventType `json:"event"`
	Severity Severity  `json:"severity"`
	// Title and Summary are English (myriarchd messages are English; the UI is
	// localized, notifications are not in MVP).
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Link is a UI path starting with "/" (e.g. "/rollouts/<id>"); the
	// Dispatcher turns it into an absolute URL with server.publicUrl.
	Link string `json:"link,omitempty"`
	// Fields are key facts rendered as a table ("Fleet": "edge-nl").
	Fields []Field   `json:"fields,omitempty"`
	Time   time.Time `json:"time"`
	// DedupKey suppresses duplicates (e.g. "release.new/otelcol-contrib/0.163.0").
	DedupKey string `json:"dedupKey,omitempty"`
}

// Field is a key/value fact.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Notifier sends notifications. The Dispatcher implementation fans out to
// all channels whose routing matches, de-duplicates by DedupKey (KV store,
// cluster-wide) and never blocks the caller for longer than the context
// allows. Errors are logged and counted; Notify returns an error only if all
// matching channels failed.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
	// Test sends a test notification to one channel by name.
	Test(ctx context.Context, channel string) error
	// Channels lists configured channels (for GET /notifications/channels).
	Channels() []ChannelInfo
}

// ChannelInfo describes a channel without secrets.
type ChannelInfo struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Events      []string `json:"events"`
	MinSeverity string   `json:"minSeverity"`
	// Target is a redacted destination ("https://example.webhook.office.com/…").
	Target string `json:"target"`
}

// Nop is a Notifier that does nothing (tests).
type Nop struct{}

// Notify implements Notifier.
func (Nop) Notify(context.Context, Notification) error { return nil }

// Test implements Notifier.
func (Nop) Test(context.Context, string) error { return nil }

// Channels implements Notifier.
func (Nop) Channels() []ChannelInfo { return []ChannelInfo{} }
