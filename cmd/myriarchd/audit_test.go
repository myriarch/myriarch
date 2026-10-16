// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/coreapi"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/fleetapi"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/live"
)

// auditedOperations declares the audit action of every state-changing REST operation. A new mutating route fails
// TestEveryMutatingRouteIsAudited until its action is listed here (and written by its handler).
var auditedOperations = map[string][]string{
	"devLogin":                   {"auth.login"},
	"bootstrapLogin":             {"auth.bootstrap.login"},
	"logout":                     {"auth.logout"},
	"updateUser":                 {"user.update"},
	"createApiToken":             {"token.create"},
	"revokeApiToken":             {"token.revoke"},
	"createEnrollmentToken":      {"enrollment_token.create"},
	"revokeEnrollmentToken":      {"enrollment_token.revoke"},
	"deleteAgent":                {"agent.delete"},
	"restartAgent":               {"agent.restart"},
	"resyncAgent":                {"agent.resync"},
	"reenrollAgent":              {"agent.reenroll"},
	"createSavedFilter":          {"saved_filter.create"},
	"deleteSavedFilter":          {"saved_filter.delete"},
	"refreshReleases":            {"release.refresh"},
	"acknowledgeRelease":         {"release.acknowledge"},
	"testNotificationChannel":    {"notification.test"},
	"triggerGitOpsSync":          {"gitops.sync"},
	"gitopsWebhook":              {"gitops.webhook"},
	"prefetchArtifacts":          {"artifact.prefetch"},
	"deleteArtifact":             {"artifact.delete"},
	"pauseRollout":               {"rollout."},
	"resumeRollout":              {"rollout."},
	"abortRollout":               {"rollout."},
	"approveRolloutWave":         {"rollout."},
	"rollbackRollout":            {"rollout."},
	"retryFailedRolloutTargets":  {"rollout."},
	"selectFleetVersion":         {"fleet.version.select"},
	"clearFleetVersionSelection": {"fleet.version.clear"},
}

// readOnlyPosts are POST operations without a state change (computations, permission read).
var readOnlyPosts = map[string]bool{"matchFleets": true, "renderBlueprint": true, "renderInstallCommands": true}

// tokenPosts are POST operations that a host calls with its enrollment token and that compute an answer without
// changing configuration, rollouts or the inventory: the plan for installing a host from the server. They have no
// principal to audit; the token counts as used, and the log names the token by its ID. They must authenticate
// themselves (api.AuthCustom).
var tokenPosts = map[string]bool{"getInstallPlan": true}

// TestEveryMutatingRouteIsAudited enumerates all state-changing operations and checks that each one has a declared
// audit action that is really written somewhere in the code base. (The behavior of the individual handlers is
// tested next to them: agent, user, token, rollout, GitOps, artifact and notification audit tests.)
func TestEveryMutatingRouteIsAudited(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := routesStore{kv: &routesKV{m: map[string][]byte{}}}
	authSvc, err := authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{}, config.OpAMPConfig{}, authn.Deps{Store: st, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	hub := live.New(eventbus.NewMemory("x", 0), nil, quiet)
	modules := []api.Module{authSvc.Module(), hub.Module()}
	modules = append(modules, coreapi.New(coreapi.Deps{Logger: quiet})...)
	syncer, _ := gitops.NewSyncer(config.GitOpsConfig{}, gitops.Deps{Logger: quiet})
	artifacts, _ := artifact.NewCache(config.ArtifactsConfig{Dir: t.TempDir()}, artifact.Deps{Logger: quiet})
	modules = append(modules, fleetapi.New(fleetapi.Deps{Logger: quiet, Syncer: syncer, Artifacts: artifacts})...)

	var mutating []api.Route
	for _, m := range modules {
		for _, r := range m.Routes() {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				if tokenPosts[r.OperationID] {
					if r.Auth != api.AuthCustom {
						t.Errorf("%s is declared as a call with an enrollment token but does not authenticate itself", r.OperationID)
					}
					continue
				}
				if readOnlyPosts[r.OperationID] {
					if r.Permission != authz.PermRead {
						t.Errorf("%s is declared read only but requires %s", r.OperationID, r.Permission)
					}
					continue
				}
				mutating = append(mutating, r)
			}
		}
	}
	mutating = append(mutating, syncer.WebhookHandler())
	if len(mutating) < 25 {
		t.Fatalf("only %d mutating routes found", len(mutating))
	}

	// all Go sources outside tests
	var src strings.Builder
	err = filepath.WalkDir("../../internal", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(p)
		src.Write(b)
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	code := src.String()

	var ids []string
	for _, r := range mutating {
		actions, ok := auditedOperations[r.OperationID]
		if !ok {
			ids = append(ids, r.OperationID)
			continue
		}
		for _, a := range actions {
			if !strings.Contains(code, `"`+a+`"`) {
				t.Errorf("%s: the audit action %q is not written anywhere", r.OperationID, a)
			}
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		t.Errorf("state-changing operations without a declared audit action: %v", ids)
	}
	// the table has no stale entries
	known := map[string]bool{}
	for _, r := range mutating {
		known[r.OperationID] = true
	}
	for id := range auditedOperations {
		if !known[id] {
			t.Errorf("auditedOperations lists %q which is not a mutating route", id)
		}
	}
}
