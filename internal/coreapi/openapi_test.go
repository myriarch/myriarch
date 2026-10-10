// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/coreapi"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/live"
	"github.com/myriarch/myriarch/internal/store"
)

type memKV struct {
	store.KVStore
	m map[string][]byte
}

func (k *memKV) Get(_ context.Context, key string) ([]byte, error) {
	if v, ok := k.m[key]; ok {
		return v, nil
	}
	return nil, domain.ErrNotFound
}
func (k *memKV) PutIfAbsent(_ context.Context, key string, v []byte) (bool, error) {
	if _, ok := k.m[key]; ok {
		return false, nil
	}
	k.m[key] = v
	return true, nil
}

type kvStore struct {
	store.Store
	kv *memKV
}

func (s kvStore) KV() store.KVStore { return s.kv }

type specOp struct {
	method, path, perm string
}

// TestRoutesMatchOpenAPI verifies that the core modules (auth, agents, saved
// filters, audit, system, stats, live) implement exactly the operations that
// whose OpenAPI tag is one of coreTags: same operationId, method, path and
// the permission of x-permission (public -> AuthPublic, custom -> AuthCustom).
func TestRoutesMatchOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	// The OpenAPI tags served by authn, live and coreapi. authn's single module
	// "auth" serves the tags auth, users and tokens; every other module is named
	// after its tag.
	coreTags := map[string]bool{"auth": true, "users": true, "tokens": true, "agents": true, "savedFilters": true, "stats": true, "system": true, "audit": true, "live": true}
	want := map[string]specOp{}
	for path, item := range spec.Paths {
		for method, node := range item {
			switch method {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			var op struct {
				OperationID string   `yaml:"operationId"`
				Tags        []string `yaml:"tags"`
				Permission  string   `yaml:"x-permission"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if len(op.Tags) != 1 {
				t.Fatalf("%s %s: expected exactly one tag, got %v", method, path, op.Tags)
			}
			if coreTags[op.Tags[0]] {
				want[op.OperationID] = specOp{strings.ToUpper(method), path, op.Permission}
			}
		}
	}
	if len(want) != 33 {
		t.Fatalf("expected 33 operations with the core tags in api/openapi.yaml, found %d", len(want))
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{}, config.OpAMPConfig{},
		authn.Deps{Store: kvStore{kv: &memKV{m: map[string][]byte{}}}, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	hub := live.New(eventbus.NewMemory("x", 0), nil, quiet)
	mods := append([]api.Module{svc.Module(), hub.Module()}, coreapi.New(coreapi.Deps{Logger: quiet})...)

	got := map[string]api.Route{}
	for _, m := range mods {
		for _, r := range m.Routes() {
			if _, dup := got[r.OperationID]; dup {
				t.Errorf("operationId %s registered twice", r.OperationID)
			}
			got[r.OperationID] = r
		}
	}
	var ids []string
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w := want[id]
		r, ok := got[id]
		if !ok {
			t.Errorf("%s (%s %s) is not implemented", id, w.method, w.path)
			continue
		}
		if r.Method != w.method || r.Pattern != w.path {
			t.Errorf("%s: route is %s %s, spec says %s %s", id, r.Method, r.Pattern, w.method, w.path)
		}
		switch w.perm {
		case "public":
			if r.Auth != api.AuthPublic {
				t.Errorf("%s: x-permission public requires api.AuthPublic", id)
			}
		case "custom":
			if r.Auth != api.AuthCustom {
				t.Errorf("%s: x-permission custom requires api.AuthCustom", id)
			}
		default:
			if r.Auth != api.AuthRequired || string(r.Permission) != w.perm {
				t.Errorf("%s: auth=%v permission=%q, spec says %q", id, r.Auth, r.Permission, w.perm)
			}
		}
		if (id == "streamEvents") != r.Streaming {
			t.Errorf("%s: Streaming = %v", id, r.Streaming)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("route %s is not a core operation of the spec", id)
		}
	}
	// every permission string used by the spec is a known authz permission
	known := map[string]bool{"public": true, "custom": true}
	for _, p := range []authz.Permission{authz.PermRead, authz.PermSavedFilters, authz.PermAgentsOperate, authz.PermAgentsDelete, authz.PermAuditRead, authz.PermTokensManage,
		authz.PermEnrollmentManage, authz.PermUsersManage} {
		known[string(p)] = true
	}
	for id, w := range want {
		if !known[w.perm] {
			t.Errorf("%s: unknown permission %q in the spec", id, w.perm)
		}
	}
}
