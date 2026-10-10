// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package api contains the shared HTTP API plumbing: the route registration
// pattern, the error model (RFC 9457 problem details) and JSON/pagination
// helpers used by all REST handlers.
//
// Route registration pattern: every package with REST endpoints exposes its handlers as
// one or more Modules. cmd/myriarchd collects all modules and passes them to the
// HTTP server (internal/httpserver), which mounts every Route
// under /api/v1 on a Go 1.22+ net/http.ServeMux, wraps it with the standard
// middleware chain (request id, logging, metrics, recovery, authentication,
// CSRF check for cookie sessions, permission check, body size limit) and
// serves the embedded UI for all other paths. Nobody edits a shared router
// file: adding an endpoint = adding a Route to your own Module.
//
// Path patterns use net/http.ServeMux syntax relative to /api/v1, e.g.
// "/agents/{id}" or "/rollouts/{id}/actions/{action}"; read parameters with
// r.PathValue("id"). Conflicting patterns panic at startup (detected in
// tests via httpserver.New).
package api

import (
	"net/http"

	"github.com/myriarch/myriarch/internal/authz"
)

// BasePath is the prefix of all REST routes.
const BasePath = "/api/v1"

// AuthMode selects how a route is authenticated.
type AuthMode int

const (
	// AuthRequired (default): a Principal (session or API token) is
	// required and must have Route.Permission.
	AuthRequired AuthMode = iota
	// AuthPublic: no authentication (login endpoints, auth config).
	AuthPublic
	// AuthCustom: the handler authenticates itself (GitOps webhook with
	// shared secret, artifact downloads with signed download tokens).
	// No CSRF check, no principal in context.
	AuthCustom
)

// Route is one REST endpoint.
type Route struct {
	// Method is the HTTP method ("GET", "POST", "PUT", "PATCH", "DELETE").
	Method string
	// Pattern is the ServeMux path pattern relative to BasePath.
	Pattern string
	// OperationID must equal the operationId in api/openapi.yaml.
	OperationID string
	// Permission required when Auth == AuthRequired. Use authz.PermRead for
	// read-only endpoints.
	Permission authz.Permission
	// Auth mode (default AuthRequired).
	Auth AuthMode
	// Handler serves the request.
	Handler http.HandlerFunc
	// MaxBodyBytes overrides the default request body limit (1 MiB).
	MaxBodyBytes int64
	// Streaming marks long-lived responses (SSE): no write timeout, no
	// response compression.
	Streaming bool
	// LongTransfer marks large responses that a slow client may take a long time to read
	// (artifact downloads): no handler deadline, but the idle write timeout applies.
	LongTransfer bool
}

// Module groups the routes of one functional area.
type Module interface {
	// Name is used in logs ("agents", "rollouts", ...).
	Name() string
	// Routes returns all routes of the module. Called once at startup.
	Routes() []Route
}

// ModuleFunc adapts a function to Module.
type ModuleFunc struct {
	ModuleName string
	RoutesFunc func() []Route
}

// Name implements Module.
func (m ModuleFunc) Name() string { return m.ModuleName }

// Routes implements Module.
func (m ModuleFunc) Routes() []Route { return m.RoutesFunc() }
