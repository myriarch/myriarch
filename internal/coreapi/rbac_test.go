// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi_test

import (
	"fmt"
	"net/http"
	"testing"
)

// TestRBACMatrix checks the status class of every core route for every role
// (and anonymous): 401 without credentials, 403 below the required role,
// anything else (200/202/204/404/412) at or above it.
func TestRBACMatrix(t *testing.T) {
	e := newEnv(t, nil)
	id := e.seed(seed{host: "a", version: "0.162.0", online: true})
	off := e.seed(seed{host: "b", version: "0.162.0"})
	fid := mustUUID().String()

	type route struct {
		method, path string
		min          string // lowest role allowed: viewer | operator | admin
	}
	routes := []route{
		{"GET", "/agents", "viewer"}, {"GET", "/agents/facets", "viewer"}, {"GET", "/agents/" + id.String(), "viewer"},
		{"GET", "/agents/" + id.String() + "/effective-config", "viewer"}, {"GET", "/agents/" + id.String() + "/desired-config", "viewer"},
		{"GET", "/agents/" + id.String() + "/components", "viewer"}, {"GET", "/agents/" + id.String() + "/events", "viewer"},
		{"POST", "/agents/" + id.String() + "/actions/reenroll", "admin"},
		{"POST", "/agents/" + id.String() + "/actions/restart", "operator"}, {"POST", "/agents/" + id.String() + "/actions/resync", "operator"},
		{"DELETE", "/agents/" + off.String(), "operator"},
		{"GET", "/saved-filters", "viewer"}, {"DELETE", "/saved-filters/" + fid, "viewer"}, // POST needs a user principal: TestSavedFilters
		{"GET", "/stats/overview", "viewer"}, {"GET", "/system/info", "viewer"}, {"GET", "/audit", "operator"},
	}
	rank := map[string]int{"": 0, "viewer": 1, "operator": 2, "admin": 3}
	for _, rt := range routes {
		for _, role := range []string{"", "viewer", "operator", "admin"} {
			name := fmt.Sprintf("%s %s as %q", rt.method, rt.path, role)
			r := e.do(role, rt.method, rt.path, nil)
			switch {
			case role == "":
				if r.Code != http.StatusUnauthorized {
					t.Errorf("%s: %d, want 401", name, r.Code)
				}
			case rank[role] < rank[rt.min]:
				if r.Code != http.StatusForbidden {
					t.Errorf("%s: %d, want 403", name, r.Code)
				}
			default:
				if r.Code == http.StatusUnauthorized || r.Code == http.StatusForbidden || r.Code >= 500 {
					t.Errorf("%s: %d must be allowed (%s)", name, r.Code, r.Body)
				}
			}
		}
	}
}
