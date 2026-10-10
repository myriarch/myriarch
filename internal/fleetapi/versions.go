// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

// Version selection in the UI (docs/concepts/fleets.md, "Target version from the UI within the Git frame"): Git sets the frame
// (spec.update.selection), the operator chooses the target version inside it.
// This file serves the dialog data (getFleetVersionOptions) and stores or
// removes the selection (selectFleetVersion, clearFleetVersionSelection). The
// rollout itself is planned by the engine on the leader.

const (
	// selectWaitMax is how long select-version waits for the leader to plan
	// the fleet before it answers with planned=false.
	selectWaitMax = 10 * time.Second
	// selectWaitPoll is the polling interval of that wait.
	selectWaitPoll = 250 * time.Millisecond
	// compareBudget bounds the changelog comparisons of one options request
	// (they may need the network when the tracker has no data).
	compareBudget = 5 * time.Second
	// maxReasonLength is the longest reason an operator may give.
	maxReasonLength = 500

	auditSelect = "fleet.version.select"
	auditClear  = "fleet.version.clear"
)

var selectableVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)

// ------------------------------------------------------------------ facts ----

// versionFacts is everything the selection rules need to know about one fleet.
type versionFacts struct {
	fleet *domain.Fleet
	sel   *domain.FleetVersionSelection
	vt    domain.VersionTarget
	dist  string
	// frame is the frame from Git (nil unless enabled).
	frame *domain.VersionSelectionSpec
	// memberVersions counts the member collectors by normalized version.
	memberVersions map[string]int
	// releases are the known releases of the distribution by version.
	releases map[string]domain.Release
	// active is the active version rollout of the fleet (newest), nil if none.
	active *domain.Rollout
}

func newFacts(f *domain.Fleet, sel *domain.FleetVersionSelection, memberVersions map[string]int, releases map[string]domain.Release, active *domain.Rollout) *versionFacts {
	dist := f.Collector.Distribution
	if dist == "" {
		dist = domain.DistributionContrib
	}
	v := &versionFacts{
		fleet: f, sel: sel, vt: domain.ResolveVersionTarget(f, sel), dist: dist,
		memberVersions: memberVersions, releases: releases, active: active,
	}
	if f.Update.Selection.Active() {
		v.frame = f.Update.Selection
	}
	return v
}

// notAllowed explains why no version can be selected for the fleet now ("" =
// allowed).
func (v *versionFacts) notAllowed() (code, reason string) {
	switch {
	case v.frame == nil:
		return dto.NotAllowedSelectionDisabled, "UI-triggered version updates are not enabled for this fleet (update.selection.enabled in Git)"
	case !domain.VersionRolloutsEnabled(v.fleet):
		return dto.NotAllowedNoExecutor, "the fleet has no update executor (update.executor is none)"
	case v.active != nil && (v.active.Status == domain.RolloutFailed || v.active.Status == domain.RolloutRollingBack):
		return dto.NotAllowedBlockingRollout, fmt.Sprintf("the version rollout %s is %s: abort it first", v.active.ID, v.active.Status)
	}
	return "", ""
}

// versionParts parses the member versions: lowest and highest parsable one.
func (v *versionFacts) memberRange() (lowest, highest string) {
	var lo, hi *semver.Version
	for ver := range v.memberVersions {
		sv, err := semver.NewVersion(ver)
		if ver == "" || err != nil {
			continue
		}
		if lo == nil || sv.LessThan(lo) {
			lo = sv
		}
		if hi == nil || sv.GreaterThan(hi) {
			hi = sv
		}
	}
	if lo != nil {
		lowest, highest = lo.Original(), hi.Original()
	}
	return lowest, highest
}

// downgradeReference is what a version is compared with for "downgrade": the
// effective target, else the highest member version.
func (v *versionFacts) downgradeReference() string {
	if v.vt.Version != "" {
		return v.vt.Version
	}
	_, hi := v.memberRange()
	return hi
}

// changelogReference is where a comparison of changes starts: the effective
// target, else the lowest member version.
func (v *versionFacts) changelogReference() string {
	if v.vt.Version != "" {
		return v.vt.Version
	}
	lo, _ := v.memberRange()
	return lo
}

func (v *versionFacts) isDowngrade(ver string) bool {
	return versionLess(ver, v.downgradeReference()) && v.downgradeReference() != ""
}

func (v *versionFacts) agentsToUpdate(ver string) int {
	n := 0
	for have, count := range v.memberVersions {
		if have != "" && have != ver {
			n += count
		}
	}
	return n
}

func (v *versionFacts) listed(ver string) bool {
	if v.frame == nil {
		return false
	}
	for _, x := range v.frame.Versions {
		if domain.NormalizeVersion(x) == ver {
			return true
		}
	}
	return false
}

// problem is one reason a version cannot be selected.
type problem struct {
	// reasonCode is the VersionOption.reasonCode; issueCode the code of the
	// 422 issue select-version answers with.
	reasonCode, issueCode, message, field string
}

// problems returns why the version cannot be selected, in the order the
// dialog reports them (current target, frame, prerelease, unknown, downgrade).
func (v *versionFacts) problems(ver string) []problem {
	if ver == v.vt.Version {
		return []problem{{reasonCode: dto.ReasonCurrentTarget, message: fmt.Sprintf("version %s is already the target of the fleet", ver), field: "version"}}
	}
	var ps []problem
	rel, known := v.releases[ver]
	if v.frame == nil || !v.frame.Allows(ver) {
		msg := fmt.Sprintf("version %s is outside the frame in Git (%s)", ver, v.frame.FrameText())
		if v.frame == nil {
			msg = "UI-triggered version updates are not enabled for this fleet"
		}
		ps = append(ps, problem{reasonCode: dto.ReasonOutsideFrame, issueCode: "version_not_allowed", message: msg, field: "version"})
	}
	if sv, err := semver.StrictNewVersion(ver); (err == nil && sv.Prerelease() != "") || (known && rel.Prerelease) {
		ps = append(ps, problem{reasonCode: dto.ReasonPrereleaseNotAllowed, issueCode: "prerelease_not_allowed",
			message: fmt.Sprintf("version %s is a prerelease", ver), field: "version"})
	}
	if !known && !v.listed(ver) {
		ps = append(ps, problem{reasonCode: dto.ReasonUnknownVersion, issueCode: "unknown_version",
			message: fmt.Sprintf("version %s is not a known release of %s and not listed in update.selection.versions", ver, v.dist), field: "version"})
	}
	if v.frame != nil && !v.frame.AllowDowngrade && v.isDowngrade(ver) {
		ps = append(ps, problem{reasonCode: dto.ReasonDowngradeNotAllowed, issueCode: "downgrade_not_allowed",
			message: fmt.Sprintf("version %s is lower than %s and update.selection.allowDowngrade is false", ver, v.downgradeReference()), field: "version"})
	}
	return ps
}

// versionPolicy names the rollout policy a selection uses.
func (v *versionFacts) versionPolicy() string {
	name := v.fleet.Rollout.VersionPolicy
	if v.frame != nil && v.frame.RolloutPolicy != "" {
		name = v.frame.RolloutPolicy
	}
	if name == "" {
		name = domain.DefaultRolloutPolicyName
	}
	return name
}

// ------------------------------------------------------------------ loading ----

// releaseMap loads the known releases of a distribution by version.
func (s *service) releaseMap(ctx context.Context, dist string) (map[string]domain.Release, error) {
	list, _, err := s.d.Store.Releases().List(ctx, store.ReleaseFilter{Distribution: dist, IncludePrerelease: true, Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	m := make(map[string]domain.Release, len(list))
	for _, r := range list {
		m[domain.NormalizeVersion(r.Version)] = r
	}
	return m, nil
}

// activeVersionRollout returns the newest active version rollout of a fleet.
func (s *service) activeVersionRollout(ctx context.Context, fleet string) (*domain.Rollout, error) {
	list, _, err := s.d.Store.Rollouts().List(ctx, store.RolloutFilter{
		Fleets: []string{fleet}, Kinds: []domain.RolloutKind{domain.RolloutKindVersion}, ActiveOnly: true, Limit: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("list rollouts: %w", err)
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// selectionOf returns the stored selection of a fleet (nil if there is none).
func (s *service) selectionOf(ctx context.Context, fleet string) (*domain.FleetVersionSelection, error) {
	sel, err := s.d.Store.FleetVersions().Get(ctx, fleet)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return sel, err
}

// loadFacts loads the facts of one fleet with fresh member versions.
func (s *service) loadFacts(ctx context.Context, f *domain.Fleet) (*versionFacts, []domain.Agent, error) {
	sel, err := s.selectionOf(ctx, f.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("load version selection: %w", err)
	}
	members, err := s.d.Store.Agents().ListAll(ctx, store.AgentFilter{Fleets: []string{f.Name}, Kinds: []domain.AgentKind{domain.AgentKindCollector}})
	if err != nil {
		return nil, nil, fmt.Errorf("list members: %w", err)
	}
	versions := map[string]int{}
	for i := range members {
		versions[domain.NormalizeVersion(members[i].Version)]++
	}
	dist := f.Collector.Distribution
	if dist == "" {
		dist = domain.DistributionContrib
	}
	releases, err := s.releaseMap(ctx, dist)
	if err != nil {
		return nil, nil, err
	}
	active, err := s.activeVersionRollout(ctx, f.Name)
	if err != nil {
		return nil, nil, err
	}
	return newFacts(f, sel, versions, releases, active), members, nil
}

// fleetOf returns a copy of the fleet of the active snapshot.
func (s *service) fleetOf(ctx context.Context, name string) (*domain.GitOpsSnapshot, *domain.Fleet, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	f := findFleet(snap, name)
	if f == nil {
		return nil, nil, fmt.Errorf("%w: fleet %q", domain.ErrNotFound, name)
	}
	return snap, f, nil
}

// ---------------------------------------------------------- version options ----

func (s *service) getFleetVersionOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, f, err := s.fleetOf(ctx, r.PathValue("name"))
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	facts, members, err := s.loadFacts(ctx, f)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	names := s.actors(ctx)
	latest := s.latestStable(ctx, facts.dist)

	// candidates: known stable releases inside the frame, the listed versions,
	// the Git target and the version of the selection
	cands := map[string]bool{}
	if facts.frame != nil {
		for ver, rel := range facts.releases {
			if !rel.Prerelease && facts.frame.Allows(ver) {
				cands[ver] = true
			}
		}
		for _, ver := range facts.frame.Versions {
			cands[domain.NormalizeVersion(ver)] = true
		}
	}
	if g := domain.NormalizeVersion(f.Collector.Version); g != "" {
		cands[g] = true
	}
	if facts.sel != nil && facts.sel.Version != "" {
		cands[domain.NormalizeVersion(facts.sel.Version)] = true
	}
	versions := make([]string, 0, len(cands))
	for v := range cands {
		versions = append(versions, v)
	}
	sortVersionsDesc(versions)

	cctx, cancel := context.WithTimeout(ctx, compareBudget)
	defer cancel()
	opts := make([]dto.VersionOption, 0, len(versions))
	for _, ver := range versions {
		opts = append(opts, s.versionOption(cctx, facts, ver, latest))
	}

	out := dto.FleetVersionOptions{
		Fleet: f.Name, Distribution: facts.dist, Target: s.versionTarget(f, facts.vt, names),
		Executor: executorOf(f), VersionPolicy: facts.versionPolicy(), Options: opts, Platforms: platformsOf(members),
	}
	out.NotAllowedCode, out.NotAllowedReason = facts.notAllowed()
	out.Allowed = out.NotAllowedCode == ""
	if facts.active != nil {
		sum := rolloutSummaryNamed(*facts.active, names)
		out.ActiveRollout = &sum
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func executorOf(f *domain.Fleet) domain.ExecutorType {
	if f.Update.Executor == "" {
		return domain.ExecutorNone
	}
	return f.Update.Executor
}

func platformsOf(members []domain.Agent) []dto.PlatformCount {
	type key struct{ os, arch string }
	counts := map[key]int{}
	for i := range members {
		counts[key{strings.ToLower(members[i].OSType), strings.ToLower(members[i].Arch)}]++
	}
	out := make([]dto.PlatformCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, dto.PlatformCount{OS: k.os, Arch: k.arch, Agents: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		return out[i].Arch < out[j].Arch
	})
	return out
}

// sortVersionsDesc sorts newest first; unparsable versions come last.
func sortVersionsDesc(v []string) {
	sort.SliceStable(v, func(i, j int) bool {
		a, ea := semver.NewVersion(v[i])
		b, eb := semver.NewVersion(v[j])
		switch {
		case ea != nil && eb != nil:
			return v[i] > v[j]
		case ea != nil:
			return false
		case eb != nil:
			return true
		}
		return a.GreaterThan(b)
	})
}

func (s *service) versionOption(ctx context.Context, facts *versionFacts, ver, latest string) dto.VersionOption {
	rel, known := facts.releases[ver]
	prerelease := known && rel.Prerelease
	if sv, err := semver.StrictNewVersion(ver); err == nil && sv.Prerelease() != "" {
		prerelease = true
	}
	o := dto.VersionOption{
		Version: ver, Known: known, Prerelease: prerelease, IsLatest: !prerelease && ver == latest,
		IsCurrentTarget: ver == facts.vt.Version, IsGitVersion: ver == domain.NormalizeVersion(facts.fleet.Collector.Version),
		IsDowngrade: facts.isDowngrade(ver), AgentsToUpdate: facts.agentsToUpdate(ver),
	}
	if known {
		o.PublishedAt = rel.PublishedAt
	}
	if ps := facts.problems(ver); len(ps) > 0 {
		o.ReasonCode, o.Reason = ps[0].reasonCode, ps[0].message
	} else {
		o.Selectable = true
	}
	if ref := facts.changelogReference(); ref != "" && s.d.Tracker != nil && versionLess(ref, ver) {
		cmp, err := s.d.Tracker.Compare(ctx, facts.dist, ref, ver)
		if err != nil {
			s.log.Debug("changelog comparison for the version dialog failed", "fleet", facts.fleet.Name, "from", ref, "to", ver, "error", err)
		} else {
			o.BreakingCount, o.DeprecationCount = len(cmp.BreakingChanges), len(cmp.Deprecations)
		}
	}
	return o
}

// releaseFleetOptions lists, for the release page, every fleet of the
// release's distribution and whether the release can be selected for it.
func (s *service) releaseFleetOptions(ctx context.Context, snap *domain.GitOpsSnapshot, rel *domain.Release, st *fleetStats) []dto.ReleaseFleetOption {
	names := s.actors(ctx)
	ver := domain.NormalizeVersion(rel.Version)
	releases := map[string]domain.Release{ver: *rel}
	out := []dto.ReleaseFleetOption{}
	for i := range snap.Fleets {
		f := &snap.Fleets[i]
		dist := f.Collector.Distribution
		if dist == "" {
			dist = domain.DistributionContrib
		}
		if dist != rel.Distribution {
			continue
		}
		var active *domain.Rollout
		if list := st.active[f.Name]; len(list) > 0 {
			for _, a := range list {
				if a.Kind == domain.RolloutKindVersion {
					ro := domain.Rollout{ID: a.ID, Status: a.Status, Kind: a.Kind}
					active = &ro
					break
				}
			}
		}
		facts := newFacts(f, st.selections[f.Name], st.versions[f.Name], releases, active)
		opt := dto.ReleaseFleetOption{
			Fleet: f.Name, DisplayName: f.DisplayName, Target: s.versionTarget(f, facts.vt, names), AgentsToUpdate: facts.agentsToUpdate(ver),
		}
		if code, reason := facts.notAllowed(); code != "" {
			opt.ReasonCode, opt.Reason = code, reason
		} else if ps := facts.problems(ver); len(ps) > 0 {
			opt.ReasonCode, opt.Reason = ps[0].reasonCode, ps[0].message
		} else {
			opt.Selectable = true
		}
		out = append(out, opt)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Selectable != out[j].Selectable {
			return out[i].Selectable
		}
		return out[i].Fleet < out[j].Fleet
	})
	return out
}

// ------------------------------------------------------------------ select ----

// principalRef is the stored actor reference of the caller.
func principalRef(r *http.Request) string {
	return authz.FromContext(r.Context()).String()
}

func (s *service) selectFleetVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")
	var req dto.SelectFleetVersionRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteError(w, r, err)
		return
	}
	req.Version = strings.TrimSpace(req.Version)
	req.Reason = strings.TrimSpace(req.Reason)
	if !selectableVersionRE.MatchString(req.Version) {
		api.WriteError(w, r, fmt.Errorf("%w: version %q is not a semantic version without \"v\" (e.g. 0.162.0)", domain.ErrInvalidArgument, req.Version))
		return
	}
	if len([]rune(req.Reason)) > maxReasonLength {
		api.WriteError(w, r, fmt.Errorf("%w: reason is longer than %d characters", domain.ErrInvalidArgument, maxReasonLength))
		return
	}
	details := map[string]any{"version": req.Version}
	if req.Reason != "" {
		details["reason"] = req.Reason
	}
	// refused attempts are audited as well
	fail := func(err error) {
		s.recordAudit(r, auditSelect, "fleet", name, details, err)
		api.WriteError(w, r, err)
	}

	snap, f, err := s.fleetOf(ctx, name)
	if err != nil {
		fail(err)
		return
	}
	facts, _, err := s.loadFacts(ctx, f)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	details["gitVersion"], details["gitCommit"] = domain.NormalizeVersion(f.Collector.Version), snap.Commit
	details["previousVersion"], details["previousSource"] = facts.vt.Version, string(facts.vt.Source)

	// preconditions: the frame allows it, an executor exists, no rollout needs attention
	if code, reason := facts.notAllowed(); code != "" {
		fail(fmt.Errorf("%w: %s", domain.ErrPrecondition, reason))
		return
	}
	if req.ExpectedTargetVersion != nil && domain.NormalizeVersion(*req.ExpectedTargetVersion) != facts.vt.Version {
		fail(fmt.Errorf("%w: the target version of fleet %s is %s, not %s: reload and decide again", domain.ErrConflict,
			name, orNoneText(facts.vt.Version), orNoneText(domain.NormalizeVersion(*req.ExpectedTargetVersion))))
		return
	}
	names := s.actors(ctx)
	if req.Version == facts.vt.Version {
		// nothing changes, but the operator's request is part of the trail
		details["noop"] = true
		s.recordAudit(r, auditSelect, "fleet", name, details, nil)
		api.WriteJSON(w, http.StatusOK, dto.SelectFleetVersionResponse{
			Target: s.versionTarget(f, facts.vt, names), Planned: false, Message: fmt.Sprintf("version %s already is the target of the fleet", req.Version),
		})
		return
	}
	var issues []domain.Issue
	if facts.frame.RequireReason && req.Reason == "" {
		issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "reason_required", Path: "reason", Message: "a reason is required (update.selection.requireReason)"})
	}
	for _, p := range facts.problems(req.Version) {
		issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: p.issueCode, Path: p.field, Message: p.message})
	}
	if len(issues) > 0 {
		fail(domain.NewValidationError(issues...))
		return
	}

	// store (compare-and-set against the row that was read) and announce
	var expect *time.Time
	if facts.sel != nil {
		expect = &facts.sel.UpdatedAt
	}
	stored, err := s.d.Store.FleetVersions().Put(ctx, domain.FleetVersionSelection{
		Fleet: name, Version: req.Version, State: domain.SelectionActive, GitVersion: domain.NormalizeVersion(f.Collector.Version),
		GitCommit: snap.Commit, SelectedBy: principalRef(r), SelectedAt: s.now(), Reason: req.Reason,
	}, expect)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			err = fmt.Errorf("%w: the version selection of fleet %s was changed by someone else: reload and decide again", domain.ErrConflict, name)
		}
		fail(err)
		return
	}
	s.recordAudit(r, auditSelect, "fleet", name, details, nil)
	s.invalidateStats()
	s.announce(ctx, name)

	resp := dto.SelectFleetVersionResponse{Target: s.versionTarget(f, domain.ResolveVersionTarget(f, stored), names)}
	row, planned := s.waitPlanned(ctx, name, stored)
	resp.Planned = planned
	switch {
	case row == nil:
		resp.Message = "the selection was replaced or removed before it was planned"
	case !planned:
		resp.Message = "the leader has not planned the fleet yet: the rollout follows"
	case row.RolloutID != nil:
		resp.RolloutID = row.RolloutID
		if ro, err := s.d.Store.Rollouts().Get(ctx, *row.RolloutID); err == nil {
			resp.Targets = ro.Counts.Total
		}
	default:
		resp.Message = "no collector of the fleet needs an update"
	}
	if row != nil {
		resp.Target = s.versionTarget(f, domain.ResolveVersionTarget(f, row), names)
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

func orNoneText(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

// waitPlanned waits until the leader has planned the fleet for the stored
// selection. It returns the current row (nil if the selection was removed) and
// whether it was planned; a replaced selection counts as not planned.
func (s *service) waitPlanned(ctx context.Context, fleet string, stored *domain.FleetVersionSelection) (*domain.FleetVersionSelection, bool) {
	deadline := time.NewTimer(s.waitMax)
	defer deadline.Stop()
	tick := time.NewTicker(s.waitPoll)
	defer tick.Stop()
	row := stored
	for {
		cur, err := s.d.Store.FleetVersions().Get(ctx, fleet)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return nil, false
		case err == nil:
			row = cur
			if !cur.SelectedAt.Equal(stored.SelectedAt) {
				return cur, false // replaced by a newer selection
			}
			if cur.PlannedAt != nil {
				return cur, true
			}
		}
		select {
		case <-ctx.Done():
			return row, false
		case <-deadline.C:
			return row, false
		case <-tick.C:
		}
	}
}

// announce tells the leader (and every replica's UI) that the selection of a
// fleet changed.
func (s *service) announce(ctx context.Context, fleet string) {
	if s.d.Bus == nil {
		return
	}
	if err := s.d.Bus.Publish(ctx, eventbus.FleetVersionChanged, eventbus.FleetVersionChangedPayload{FleetName: fleet}); err != nil {
		s.log.Warn("publish fleet.version_changed failed", "fleet", fleet, "error", err)
	}
}

// ------------------------------------------------------------------- clear ----

func (s *service) clearFleetVersionSelection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")
	var req dto.ClearFleetVersionSelectionRequest
	if err := api.DecodeJSONOptional(r, &req); err != nil {
		api.WriteError(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len([]rune(req.Reason)) > maxReasonLength {
		api.WriteError(w, r, fmt.Errorf("%w: reason is longer than %d characters", domain.ErrInvalidArgument, maxReasonLength))
		return
	}
	details := map[string]any{}
	if req.Reason != "" {
		details["reason"] = req.Reason
	}
	fail := func(err error) {
		s.recordAudit(r, auditClear, "fleet", name, details, err)
		api.WriteError(w, r, err)
	}
	_, f, err := s.fleetOf(ctx, name)
	if err != nil {
		fail(err)
		return
	}
	details["gitVersion"] = domain.NormalizeVersion(f.Collector.Version)
	names := s.actors(ctx)
	if f.Update.Selection != nil && f.Update.Selection.RequireReason && req.Reason == "" {
		fail(domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "reason_required", Path: "reason",
			Message: "a reason is required (update.selection.requireReason)"}))
		return
	}
	sel, err := s.selectionOf(ctx, name)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	if sel == nil { // nothing to clear
		details["noop"] = true
		s.recordAudit(r, auditClear, "fleet", name, details, nil)
		api.WriteJSON(w, http.StatusOK, s.versionTarget(f, domain.ResolveVersionTarget(f, nil), names))
		return
	}
	details["version"] = sel.Version
	if _, err := s.d.Store.FleetVersions().Delete(ctx, name, &sel.UpdatedAt); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			err = fmt.Errorf("%w: the version selection of fleet %s was changed by someone else: reload and decide again", domain.ErrConflict, name)
		}
		fail(err)
		return
	}
	s.recordAudit(r, auditClear, "fleet", name, details, nil)
	s.invalidateStats()
	s.announce(ctx, name)
	api.WriteJSON(w, http.StatusOK, s.versionTarget(f, domain.ResolveVersionTarget(f, nil), names))
}

// invalidateStats drops the cached fleet statistics of this replica so that the
// next read shows the new target.
func (s *service) invalidateStats() {
	s.statsMu.Lock()
	s.stats = nil
	s.statsMu.Unlock()
}
