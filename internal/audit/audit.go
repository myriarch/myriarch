// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package audit records audit events.
//
// The Recorder implementation only writes to store.AuditStore and logs; the
// REST endpoint GET /audit is implemented in internal/coreapi.
//
// What must be audited (every mutating REST call, plus):
//   - auth: login success/failure, logout
//   - tokens/enrollment tokens/users: create, revoke, role change
//   - agents: restart, resync, delete
//   - rollouts: pause, resume, abort, approve, rollback, retry_failed,
//     automatic transitions by the engine (actor system), rollout creation
//     by GitOps (actor gitops)
//   - gitops: sync triggered manually or by webhook, snapshot applied
//   - releases: acknowledge, refresh
//   - artifacts: prefetch, delete
//   - notifications: test
//
// Action names are "<area>.<verb>" (lower case, snake_case verbs), e.g.
// "rollout.pause", "token.create", "agent.restart", "gitops.sync".
package audit

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// Recorder writes audit events. Record never fails the caller: errors are
// logged (and counted by the store layer).
type Recorder interface {
	Record(ctx context.Context, e domain.AuditEvent)
}

// NewRecorder returns a Recorder writing to s.
func NewRecorder(s store.AuditStore, logger *slog.Logger) Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &recorder{s: s, log: logger}
}

type recorder struct {
	s   store.AuditStore
	log *slog.Logger
}

func (r *recorder) Record(ctx context.Context, e domain.AuditEvent) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.Result == "" {
		e.Result = "success"
	}
	// Use a detached context: auditing must not be skipped because the
	// request was cancelled after the action happened.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.s.Append(wctx, e); err != nil {
		r.log.Error("audit append failed", "error", err, "action", e.Action, "target", e.TargetType+"/"+e.TargetID)
	}
}

// FromRequest builds an AuditEvent for an HTTP request using the principal
// from the request context. err != nil sets Result "failure" and adds
// details["error"].
func FromRequest(r *http.Request, action, targetType, targetID string, details map[string]any, err error) domain.AuditEvent {
	p := authz.FromContext(r.Context())
	e := domain.AuditEvent{
		Time:       time.Now().UTC(),
		ActorType:  p.ActorType(),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Result:     "success",
		Details:    details,
		RequestID:  r.Header.Get("X-Request-Id"),
		SourceIP:   clientIP(r),
	}
	if p != nil {
		e.ActorID, e.ActorName = p.ID, p.Name
	} else {
		e.ActorID, e.ActorName = "anonymous", "anonymous"
	}
	if err != nil {
		e.Result = "failure"
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		e.Details["error"] = err.Error()
	}
	return e
}

// System builds an AuditEvent for an action performed by an internal job.
func System(actor domain.ActorType, action, targetType, targetID string, details map[string]any) domain.AuditEvent {
	name := string(actor)
	return domain.AuditEvent{
		Time: time.Now().UTC(), ActorType: actor, ActorID: name, ActorName: name,
		Action: action, TargetType: targetType, TargetID: targetID, Result: "success", Details: details,
	}
}

// clientIP returns the remote IP. The HTTP middleware (internal/httpserver) rewrites
// r.RemoteAddr from X-Forwarded-For for trusted proxies before handlers run.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type requestKey struct{}

type requestInfo struct{ requestID, sourceIP string }

// WithRequest remembers the request id and source IP of an HTTP request in
// ctx, so audit events written deeper in the call chain (rollout engine) carry
// them (see Enrich).
func WithRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, requestKey{}, requestInfo{requestID: r.Header.Get("X-Request-Id"), sourceIP: clientIP(r)})
}

// Enrich fills RequestID and SourceIP of e from the request stored in ctx.
func Enrich(ctx context.Context, e *domain.AuditEvent) {
	if ri, ok := ctx.Value(requestKey{}).(requestInfo); ok {
		if e.RequestID == "" {
			e.RequestID = ri.requestID
		}
		if e.SourceIP == "" {
			e.SourceIP = ri.sourceIP
		}
	}
}
