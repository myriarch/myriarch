// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package updaterexec implements executor type "updater": version updates via
// OpAMP PackagesAvailable offers to the linked myriarch-updater agent.
//
// Start: for every target, resolve the linked updater agent (error if none
// or offline), ensure the artifact (distribution, version, os, arch, tar.gz)
// is cached and verified, build the domain.PackageOffer (name "",
// version "v"+ToVersion, download URL + download token header (valid while
// the offer exists, see config.ArtifactsConfig.DownloadTokenTTL), SHA-256,
// sigstore bundle as signature), store it with PackageOfferStore.SetOffers and
// call Control.DesiredStateChanged. Cancel: ClearOffers + DesiredStateChanged.
package updaterexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/store"
)

// artifactWait bounds how long Start waits for artifacts that are not cached
// yet (the download continues in the background and the targets are retried
// by the next tick). Start runs under the engine mutex, which also serializes
// the planner and the GitOps reconciliation, so a long wait would stall every
// other rollout.
const artifactWait = 5 * time.Second

// ArtifactResolver provides verified artifacts and download URLs
// (implemented by *artifact.Cache).
type ArtifactResolver interface {
	// Ensure returns a ready, verified artifact (downloading it if needed;
	// may block for minutes - call with a deadline).
	Ensure(ctx context.Context, distribution, version, os, arch string) (*domain.Artifact, error)
	// DownloadURLFor returns an absolute URL and HTTP headers (Authorization
	// with a signed, short-lived download token) for an updater agent. The
	// base URL depends on the updater (artifacts.agentBaseUrls matched with
	// its reported server endpoint, else server.publicUrl).
	DownloadURLFor(a *domain.Artifact, updater *domain.Agent) (url string, headers map[string]string, err error)
}

// signatureRequirer is implemented by artifact resolvers that know whether
// sigstore verification is mandatory (*artifact.Cache).
type signatureRequirer interface{ SignatureRequired() bool }

// Deps are the dependencies of the executor.
type Deps struct {
	Store     store.Store
	Control   control.AgentControl
	Artifacts ArtifactResolver
	Logger    *slog.Logger
	// Now is the clock (tests).
	Now func() time.Time
}

// New returns the updater executor.
func New(deps Deps) executor.Executor {
	e := &exec{deps: deps, now: deps.Now}
	if e.now == nil {
		e.now = func() time.Time { return time.Now().UTC() }
	}
	e.log = deps.Logger
	if e.log == nil {
		e.log = slog.Default()
	}
	e.log = e.log.With("component", "updaterexec")
	return e
}

type exec struct {
	deps Deps
	log  *slog.Logger
	now  func() time.Time
}

func (e *exec) Type() domain.ExecutorType { return domain.ExecutorUpdater }

// Validate: the updater executor has no environment dependent prerequisites
// beyond what Start reports per target.
func (e *exec) Validate(spec domain.UpdateSpec, def *domain.Executor) domain.Issues { return nil }

type platform struct{ os, arch string }

// Start offers the new collector package to the updater of each target.
func (e *exec) Start(ctx context.Context, req executor.UpdateRequest) ([]executor.TargetResult, error) {
	results := make([]executor.TargetResult, len(req.Targets))
	for i, t := range req.Targets {
		results[i].AgentID = t.AgentID
	}
	if len(req.Targets) == 0 {
		return results, nil
	}
	fail := func(i int, format string, args ...any) { results[i].Error = fmt.Sprintf(format, args...) }

	// 1. updater agents.
	updaterOf := map[int]domain.AgentID{}
	var updaterIDs []domain.AgentID
	for i, t := range req.Targets {
		if t.UpdaterAgentID == nil {
			fail(i, "no myriarch-updater linked to this collector")
			continue
		}
		updaterOf[i] = *t.UpdaterAgentID
		updaterIDs = append(updaterIDs, *t.UpdaterAgentID)
	}
	updaters := map[domain.AgentID]domain.Agent{}
	collectors := map[domain.AgentID]domain.Agent{}
	if len(updaterIDs) > 0 {
		list, err := e.deps.Store.Agents().GetMany(ctx, updaterIDs)
		if err != nil {
			return nil, fmt.Errorf("load updater agents: %w", err)
		}
		for _, a := range list {
			updaters[a.ID] = a
		}
		targetIDs := make([]domain.AgentID, len(req.Targets))
		for i, t := range req.Targets {
			targetIDs[i] = t.AgentID
		}
		cols, err := e.deps.Store.Agents().GetMany(ctx, targetIDs)
		if err != nil {
			return nil, fmt.Errorf("load collector agents: %w", err)
		}
		for _, a := range cols {
			collectors[a.ID] = a
		}
	}
	for i, id := range updaterOf {
		up, ok := updaters[id]
		col := collectors[req.Targets[i].AgentID]
		switch {
		case !ok:
			fail(i, "myriarch-updater %s is unknown", id)
			delete(updaterOf, i)
		case up.Kind != domain.AgentKindUpdater:
			fail(i, "linked agent %s is not a myriarch-updater", id)
			delete(updaterOf, i)
		case col.Updater != nil && col.Updater.Ambiguous:
			// several online updaters claim the collector: never guess which one is right
			fail(i, "ambiguous updater link: more than one online myriarch-updater claims this collector")
			delete(updaterOf, i)
		case domain.UpdaterMismatch(&up, &col) != "":
			// re-check of the link binding right before the offer is made
			fail(i, "updater link refused: %s", domain.UpdaterMismatch(&up, &col))
			delete(updaterOf, i)
		case !up.IsOnline():
			fail(i, "myriarch-updater offline")
			delete(updaterOf, i)
		}
	}

	// 2. artifacts per platform (in parallel, one bounded wait for all).
	plats := map[platform]struct{}{}
	for i := range updaterOf {
		t := req.Targets[i]
		plats[platform{t.OSType, t.Arch}] = struct{}{}
	}
	type artResult struct {
		art *domain.Artifact
		err error
	}
	arts := map[platform]artResult{}
	var mu sync.Mutex
	wctx, cancel := context.WithTimeout(ctx, artifactWait)
	g, gctx := errgroup.WithContext(wctx)
	for p := range plats {
		g.Go(func() error {
			a, err := e.deps.Artifacts.Ensure(gctx, req.Distribution, req.ToVersion, p.os, p.arch)
			mu.Lock()
			arts[p] = artResult{a, err}
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 3. offers.
	var changed []domain.AgentID
	now := e.now()
	for i, up := range updaterOf {
		t := req.Targets[i]
		ar := arts[platform{t.OSType, t.Arch}]
		switch {
		case ar.err != nil && (errors.Is(ar.err, context.DeadlineExceeded) || errors.Is(ar.err, context.Canceled)):
			results[i].RetryLater = true // still downloading: not a failure
			e.log.Info("artifact still downloading, will retry", "agent_id", t.AgentID, "version", req.ToVersion, "os", t.OSType, "arch", t.Arch)
			continue
		case ar.err != nil:
			fail(i, "artifact %s %s for %s/%s is not available: %v", req.Distribution, req.ToVersion, t.OSType, t.Arch, ar.err)
			continue
		}
		a := ar.art
		// only verified artifacts are offered: ready, with a SHA-256 and, when
		// signatures are required, a verified sigstore bundle the updater checks again
		if a.Status != domain.ArtifactReady || len(a.SHA256) != 32 {
			fail(i, "artifact %s %s for %s/%s is not ready or has no SHA-256", req.Distribution, req.ToVersion, t.OSType, t.Arch)
			continue
		}
		if sr, ok := e.deps.Artifacts.(signatureRequirer); ok && sr.SignatureRequired() && (!a.SignatureVerified || len(a.SigstoreBundle) == 0) {
			fail(i, "artifact %s %s for %s/%s has no verified sigstore bundle", req.Distribution, req.ToVersion, t.OSType, t.Arch)
			continue
		}
		// The offer stores only the URL: the download token is minted whenever
		// the offer is sent to the updater (never stored, never stale).
		upAgent := updaters[up]
		url, _, err := e.deps.Artifacts.DownloadURLFor(a, &upAgent)
		if err != nil {
			fail(i, "cannot build the download URL: %v", err)
			continue
		}
		rid := req.RolloutID
		offer := domain.PackageOffer{
			AgentID: up, Name: domain.CollectorPackageName, Version: "v" + req.ToVersion, DownloadURL: url,
			ContentHash: a.SHA256, Signature: a.SigstoreBundle, RolloutID: &rid, CreatedAt: now,
		}
		if err := e.deps.Store.Packages().SetOffers(ctx, up, []domain.PackageOffer{offer}); err != nil {
			fail(i, "cannot store the package offer: %v", err)
			continue
		}
		results[i].Started = true
		changed = append(changed, up)
	}
	if len(changed) > 0 && e.deps.Control != nil {
		if err := e.deps.Control.DesiredStateChanged(ctx, changed); err != nil {
			e.log.Warn("push package offers failed (agents get them on their next message)", "error", err)
		}
	}
	return results, nil
}

// Cancel withdraws the package offers of the rollout for the given collectors
// (their linked updaters).
func (e *exec) Cancel(ctx context.Context, req executor.CancelRequest) error {
	if len(req.AgentIDs) == 0 {
		return nil
	}
	agents, err := e.deps.Store.Agents().GetMany(ctx, req.AgentIDs)
	if err != nil {
		return fmt.Errorf("load agents: %w", err)
	}
	var cleared []domain.AgentID
	var errs []error
	seen := map[domain.AgentID]bool{}
	for _, a := range agents {
		if a.Updater == nil || seen[a.Updater.AgentID] {
			continue
		}
		up := a.Updater.AgentID
		seen[up] = true
		offers, err := e.deps.Store.Packages().GetOffers(ctx, up)
		if err != nil {
			errs = append(errs, fmt.Errorf("offers of %s: %w", up, err))
			continue
		}
		if len(offers) == 0 {
			continue
		}
		// Only withdraw offers of THIS rollout (a successor may already have replaced them).
		own := true
		for _, o := range offers {
			if o.RolloutID != nil && *o.RolloutID != req.RolloutID {
				own = false
			}
		}
		if !own {
			continue
		}
		if err := e.deps.Store.Packages().ClearOffers(ctx, up); err != nil {
			errs = append(errs, fmt.Errorf("clear offers of %s: %w", up, err))
			continue
		}
		cleared = append(cleared, up)
	}
	if len(cleared) > 0 && e.deps.Control != nil {
		if err := e.deps.Control.DesiredStateChanged(ctx, cleared); err != nil {
			errs = append(errs, fmt.Errorf("push cleared offers: %w", err))
		}
	}
	return errors.Join(errs...)
}
