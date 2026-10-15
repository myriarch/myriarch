// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/authz"
)

// fleetTags are the OpenAPI tags served by this package. Every module is named
// exactly after the tag whose operations it serves.
var fleetTags = []string{"artifacts", "blueprints", "executors", "fleets", "gitops", "install", "notifications", "releases", "rolloutPolicies", "rollouts"}

// Every module serves exactly the operations of the OpenAPI tag it is named
// after, with the documented method, path and permission, and nothing else is
// exposed.
func TestTaggedOperationsMatchTheOpenAPIContract(t *testing.T) {
	e := newAPIEnv(t)

	// Group the spec operations by their single tag.
	byTag := map[string][]operation{}
	for _, op := range e.spec.operations() {
		require.Len(t, op.tags, 1, "operation %s must have exactly one tag", op.id)
		byTag[op.tags[0]] = append(byTag[op.tags[0]], op)
	}

	var moduleNames, gotIDs, wantIDs []string
	seen := map[string]string{}
	for _, m := range e.svc.modules() {
		moduleNames = append(moduleNames, m.Name())
		var routeIDs, specIDs []string
		for _, rt := range m.Routes() {
			if other, dup := seen[rt.OperationID]; dup {
				t.Errorf("operationId %s is served by both %s and %s", rt.OperationID, other, m.Name())
			}
			seen[rt.OperationID] = m.Name()
			routeIDs = append(routeIDs, rt.OperationID)
		}
		for _, op := range byTag[m.Name()] {
			specIDs = append(specIDs, op.id)
			rt, ok := e.routes[op.id]
			if !assert.True(t, ok, "operation %s is not implemented", op.id) {
				continue
			}
			assert.Equal(t, op.method, rt.Method, op.id)
			assert.Equal(t, op.path, rt.Pattern, op.id)
			switch op.permission {
			case "custom":
				assert.Equal(t, api.AuthCustom, rt.Auth, op.id)
			case "public":
				assert.Equal(t, api.AuthPublic, rt.Auth, op.id)
			default:
				assert.Equal(t, api.AuthRequired, rt.Auth, op.id)
				assert.Equal(t, authz.Permission(op.permission), rt.Permission, op.id)
			}
			assert.NotNil(t, rt.Handler, op.id)
		}
		sort.Strings(routeIDs)
		sort.Strings(specIDs)
		assert.Equal(t, specIDs, routeIDs, "module %s serves exactly the operations tagged %s", m.Name(), m.Name())
		gotIDs = append(gotIDs, routeIDs...)
	}
	assert.ElementsMatch(t, fleetTags, moduleNames)

	for _, tag := range fleetTags {
		for _, op := range byTag[tag] {
			wantIDs = append(wantIDs, op.id)
		}
	}
	sort.Strings(wantIDs)
	sort.Strings(gotIDs)
	assert.Equal(t, wantIDs, gotIDs, "exactly the operationIds of the fleet tags")
	assert.Len(t, wantIDs, 40)
}

func TestRoutesAreMountableWithoutConflicts(t *testing.T) {
	// newAPIEnv mounts all routes on one ServeMux: conflicting patterns would panic there.
	e := newAPIEnv(t)
	require.NotEmpty(t, e.routes)
	// the download route is a long transfer (idle write timeout, no handler deadline), the webhook is custom
	assert.True(t, e.routes["downloadArtifact"].LongTransfer)
	assert.False(t, e.routes["downloadArtifact"].Streaming)
	assert.Equal(t, api.AuthCustom, e.routes["gitopsWebhook"].Auth)
	assert.Equal(t, http.MethodPost, e.routes["gitopsWebhook"].Method)
	// the plan is asked for by hosts with an enrollment token: no session, a small body
	plan := e.routes["getInstallPlan"]
	assert.Equal(t, api.AuthCustom, plan.Auth)
	assert.Equal(t, http.MethodPost, plan.Method)
	assert.Equal(t, "/install/plan", plan.Pattern)
	assert.Equal(t, int64(16<<10), plan.MaxBodyBytes)
	assert.False(t, plan.Streaming)
	assert.False(t, plan.LongTransfer)
}

func TestAuthenticationAndPermissions(t *testing.T) {
	e := newAPIEnv(t)
	// no principal
	r := e.do(http.MethodGet, "/fleets", "", nil)
	e.expect(r, "listFleets", http.StatusUnauthorized)
	// viewer may read but not operate
	e.expect(e.do(http.MethodGet, "/fleets", "viewer", nil), "listFleets", http.StatusOK)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/rollouts/" + "00000000-0000-0000-0000-000000000001" + "/actions/pause"},
		{http.MethodPost, "/rollouts/00000000-0000-0000-0000-000000000001/actions/abort"},
		{http.MethodPost, "/releases/actions/refresh"},
		{http.MethodPost, "/releases/otelcol-contrib/0.162.0/actions/acknowledge"},
		{http.MethodPost, "/gitops/actions/sync"},
		{http.MethodPost, "/artifacts/actions/prefetch"},
		{http.MethodDelete, "/artifacts/00000000-0000-0000-0000-000000000001"},
		{http.MethodPost, "/notifications/channels/generic/actions/test"},
	} {
		r := e.do(c.method, c.path, "viewer", "{}")
		assert.Equal(t, http.StatusForbidden, r.Code, "%s %s", c.method, c.path)
		e.expect(r, "listFleets", http.StatusForbidden)
	}
}
