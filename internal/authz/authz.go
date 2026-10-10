// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package authz defines principals, permissions and the role -> permission
// mapping (RBAC).
//
// Authentication (OIDC, sessions, API tokens, dev login, enrollment tokens)
// is implemented in internal/authn. The HTTP layer (internal/httpserver)
// resolves the Principal for every request, stores it in the request context
// (WithPrincipal) and enforces the Permission declared on each api.Route.
// Handlers read it with FromContext.
package authz

import (
	"context"

	"github.com/myriarch/myriarch/internal/domain"
)

// Permission is an action class. Read permissions are granted to every
// authenticated principal with at least the viewer role.
type Permission string

const (
	// PermRead: read everything except audit/users/tokens (viewer+).
	PermRead Permission = "read"
	// PermSavedFilters: manage own saved filters (viewer+).
	PermSavedFilters Permission = "saved_filters:write"
	// PermAgentsOperate: restart, resync, request full state (operator+).
	PermAgentsOperate Permission = "agents:operate"
	// PermAgentsDelete: forget (delete) an offline agent (operator+).
	PermAgentsDelete Permission = "agents:delete"
	// PermRolloutsOperate: pause, resume, abort, approve, rollback, retry (operator+).
	PermRolloutsOperate Permission = "rollouts:operate"
	// PermFleetVersionSet: select or clear the UI target version of a fleet
	// within the frame from Git (operator+).
	PermFleetVersionSet Permission = "fleets:set_version"
	// PermReleasesAcknowledge: acknowledge release notices, trigger refresh (operator+).
	PermReleasesAcknowledge Permission = "releases:acknowledge"
	// PermGitOpsSync: trigger a GitOps sync (operator+).
	PermGitOpsSync Permission = "gitops:sync"
	// PermArtifactsManage: prefetch/delete cached artifacts (operator+).
	PermArtifactsManage Permission = "artifacts:manage"
	// PermNotificationsTest: send test notifications (operator+).
	PermNotificationsTest Permission = "notifications:test"
	// PermAuditRead: read the audit log (operator+).
	PermAuditRead Permission = "audit:read"
	// PermTokensManage: create/revoke API tokens (admin).
	PermTokensManage Permission = "tokens:manage"
	// PermEnrollmentManage: create/revoke enrollment tokens (admin).
	PermEnrollmentManage Permission = "enrollment:manage"
	// PermUsersManage: list users, change roles, disable (admin).
	PermUsersManage Permission = "users:manage"
)

var viewerPerms = []Permission{PermRead, PermSavedFilters}

var operatorPerms = append(append([]Permission{}, viewerPerms...),
	PermAgentsOperate, PermAgentsDelete, PermRolloutsOperate, PermFleetVersionSet, PermReleasesAcknowledge,
	PermGitOpsSync, PermArtifactsManage, PermNotificationsTest, PermAuditRead)

var adminPerms = append(append([]Permission{}, operatorPerms...),
	PermTokensManage, PermEnrollmentManage, PermUsersManage)

// PermissionsFor returns the permissions granted to a role.
func PermissionsFor(r domain.Role) []Permission {
	switch r {
	case domain.RoleAdmin:
		return append([]Permission{}, adminPerms...)
	case domain.RoleOperator:
		return append([]Permission{}, operatorPerms...)
	case domain.RoleViewer:
		return append([]Permission{}, viewerPerms...)
	}
	return []Permission{}
}

// RoleHas reports whether role r grants permission p.
func RoleHas(r domain.Role, p Permission) bool {
	for _, x := range PermissionsFor(r) {
		if x == p {
			return true
		}
	}
	return false
}

// PrincipalType is the kind of authenticated caller.
type PrincipalType string

const (
	PrincipalUser   PrincipalType = "user"
	PrincipalToken  PrincipalType = "token"
	PrincipalSystem PrincipalType = "system"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	Type PrincipalType `json:"type"`
	// ID: user id or API token id (UUID string); "system" for internal.
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Email string      `json:"email,omitempty"`
	Role  domain.Role `json:"role"`
	// AuthMethod: "oidc", "local", "api_token", "system".
	AuthMethod string `json:"authMethod"`
}

// Can reports whether the principal has permission p.
func (p *Principal) Can(perm Permission) bool {
	if p == nil {
		return false
	}
	if p.Type == PrincipalSystem {
		return true
	}
	return RoleHas(p.Role, perm)
}

// ActorType maps the principal to an audit actor type.
func (p *Principal) ActorType() domain.ActorType {
	if p == nil {
		return domain.ActorSystem
	}
	switch p.Type {
	case PrincipalUser:
		return domain.ActorUser
	case PrincipalToken:
		return domain.ActorToken
	}
	return domain.ActorSystem
}

// String returns "user:<id>" / "token:<id>" / "system" (used in CreatedBy,
// ApprovedBy, RequestedBy fields).
func (p *Principal) String() string {
	if p == nil || p.Type == PrincipalSystem {
		return "system"
	}
	return string(p.Type) + ":" + p.ID
}

// System is the principal used by internal jobs.
var System = &Principal{Type: PrincipalSystem, ID: "system", Name: "system", Role: domain.RoleAdmin, AuthMethod: "system"}

type ctxKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal or nil.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
