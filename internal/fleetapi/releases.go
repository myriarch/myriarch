// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

func (s *service) releaseRoutes() []api.Route {
	return []api.Route{
		route(http.MethodGet, "/releases", "listReleases", "read", s.listReleases),
		route(http.MethodGet, "/releases/latest", "getLatestRelease", "read", s.getLatestRelease),
		route(http.MethodGet, "/releases/compare", "compareVersions", "read", s.compareVersions),
		route(http.MethodPost, "/releases/actions/refresh", "refreshReleases", "releases:acknowledge", s.refreshReleases),
		route(http.MethodGet, "/releases/{distribution}/{version}", "getRelease", "read", s.getRelease),
		route(http.MethodPost, "/releases/{distribution}/{version}/actions/acknowledge", "acknowledgeRelease", "releases:acknowledge", s.acknowledgeRelease),
	}
}

func distributionParam(r *http.Request) (string, error) {
	d := r.URL.Query().Get("distribution")
	if d == "" {
		return domain.DistributionContrib, nil
	}
	if d != domain.DistributionContrib && d != domain.DistributionSupervisor {
		return "", fmt.Errorf("%w: distribution must be %s or %s", domain.ErrInvalidArgument, domain.DistributionContrib, domain.DistributionSupervisor)
	}
	return d, nil
}

// trackedFleet is a fleet of a distribution together with its effective
// target version (a UI selection counts like the Git target).
type trackedFleet struct {
	fleet  domain.Fleet
	target string
}

// fleetsOf lists the fleets that track a distribution with an effective
// target version.
func fleetsOf(snap *domain.GitOpsSnapshot, dist string, st *fleetStats) []trackedFleet {
	var out []trackedFleet
	for _, f := range snap.Fleets {
		d := f.Collector.Distribution
		if d == "" {
			d = domain.DistributionContrib
		}
		if target := st.targets[f.Name].Version; d == dist && target != "" {
			out = append(out, trackedFleet{fleet: f, target: target})
		}
	}
	return out
}

func (s *service) listReleases(w http.ResponseWriter, r *http.Request) {
	dist, err := distributionParam(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	pg, err := api.ParsePagination(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	incl, err := queryBool(r, "includePrerelease")
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	f := store.ReleaseFilter{Distribution: dist, Limit: pg.Limit, Offset: pg.Offset}
	if incl != nil {
		f.IncludePrerelease = *incl
	}
	list, total, err := s.d.Store.Releases().List(r.Context(), f)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	st, err := s.fleetStatistics(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	latest := s.latestStable(r.Context(), dist)
	fleets := fleetsOf(snap, dist, st)
	items := make([]dto.ReleaseSummary, 0, len(list))
	for _, rel := range list {
		behind := 0
		for _, fl := range fleets {
			if versionLess(fl.target, rel.Version) {
				behind++
			}
		}
		items = append(items, dto.ReleaseSummary{
			Distribution: rel.Distribution, Version: rel.Version, Tag: rel.Tag, PublishedAt: rel.PublishedAt,
			Prerelease: rel.Prerelease, HTMLURL: rel.HTMLURL,
			BreakingCount: len(rel.BreakingChanges), DeprecationCount: len(rel.Deprecations),
			Acknowledged: rel.AcknowledgedAt != nil, FleetsBehind: behind, IsLatest: !rel.Prerelease && rel.Version == latest,
		})
	}
	api.WriteJSON(w, http.StatusOK, api.NewPage(items, total, pg))
}

func normalizeRelease(rel domain.Release) domain.Release {
	if rel.BreakingChanges == nil {
		rel.BreakingChanges = []domain.ChangelogEntry{}
	}
	if rel.Deprecations == nil {
		rel.Deprecations = []domain.ChangelogEntry{}
	}
	if rel.Assets == nil {
		rel.Assets = []domain.ReleaseAsset{}
	}
	for i := range rel.BreakingChanges {
		if rel.BreakingChanges[i].Issues == nil {
			rel.BreakingChanges[i].Issues = nil
		}
	}
	return rel
}

func (s *service) releaseDetail(r *http.Request, rel *domain.Release) (dto.ReleaseDetail, error) {
	ctx := r.Context()
	snap, err := s.snapshot(ctx)
	if err != nil {
		return dto.ReleaseDetail{}, err
	}
	latest := s.latestStable(ctx, rel.Distribution)
	detail := dto.ReleaseDetail{
		Release: s.presentRelease(ctx, *rel), IsLatest: !rel.Prerelease && rel.Version == latest,
		FleetsBehind: []dto.FleetBehind{}, GitOpsSnippet: "", UpdatableFleets: []dto.ReleaseFleetOption{},
	}
	if rel.Distribution != domain.DistributionContrib {
		return detail, nil
	}
	st, err := s.fleetStatistics(ctx)
	if err != nil {
		return detail, err
	}
	detail.UpdatableFleets = s.releaseFleetOptions(ctx, snap, rel, st)
	for ver, n := range st.allVersions {
		if ver != "" && versionLess(ver, rel.Version) {
			detail.AgentsBehind += n
		}
	}
	var snippets []string
	fleets := fleetsOf(snap, rel.Distribution, st)
	sort.Slice(fleets, func(i, j int) bool { return fleets[i].fleet.Name < fleets[j].fleet.Name })
	for _, tf := range fleets {
		f := tf.fleet
		if !versionLess(tf.target, rel.Version) {
			continue
		}
		fb := dto.FleetBehind{
			Fleet: f.Name, TargetVersion: tf.target, SourcePath: f.Source.Path,
			Versions: st.versionCounts(f.Name, latest),
		}
		if c := st.counts[f.Name]; c != nil {
			fb.Agents = c.Total
		}
		if s.d.Tracker != nil {
			if cmp, err := s.d.Tracker.Compare(ctx, rel.Distribution, tf.target, rel.Version); err == nil {
				fb.BreakingCount = len(cmp.BreakingChanges)
			} else {
				s.log.Warn("version comparison for release detail failed", "fleet", f.Name, "error", err)
			}
		}
		detail.FleetsBehind = append(detail.FleetsBehind, fb)
		snippets = append(snippets, fmt.Sprintf("# %s\nspec:\n  collector:\n    version: %s\n", f.Source.Path, rel.Version))
	}
	detail.GitOpsSnippet = strings.Join(snippets, "\n")
	return detail, nil
}

func (s *service) getLatestRelease(w http.ResponseWriter, r *http.Request) {
	dist, err := distributionParam(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	rel, err := s.d.Store.Releases().Latest(r.Context(), dist, false)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	detail, err := s.releaseDetail(r, rel)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, detail)
}

func (s *service) getRelease(w http.ResponseWriter, r *http.Request) {
	rel, err := s.d.Store.Releases().Get(r.Context(), r.PathValue("distribution"), domain.NormalizeVersion(r.PathValue("version")))
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	detail, err := s.releaseDetail(r, rel)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, detail)
}

func (s *service) compareVersions(w http.ResponseWriter, r *http.Request) {
	dist, err := distributionParam(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" || to == "" {
		api.WriteError(w, r, fmt.Errorf("%w: from and to are required", domain.ErrInvalidArgument))
		return
	}
	if s.d.Tracker == nil {
		api.WriteError(w, r, fmt.Errorf("%w: release tracker is not available", domain.ErrUnavailable))
		return
	}
	cmp, err := s.d.Tracker.Compare(r.Context(), dist, from, to)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, cmp)
}

func (s *service) refreshReleases(w http.ResponseWriter, r *http.Request) {
	if s.d.Tracker == nil {
		api.WriteError(w, r, fmt.Errorf("%w: release tracker is not available", domain.ErrUnavailable))
		return
	}
	err := s.d.Tracker.Refresh(r.Context())
	s.recordAudit(r, "release.refresh", "release", "", nil, err)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, dto.ActionAccepted{Accepted: true, Message: "release poll requested"})
}

func (s *service) acknowledgeRelease(w http.ResponseWriter, r *http.Request) {
	dist, ver := r.PathValue("distribution"), domain.NormalizeVersion(r.PathValue("version"))
	by := "system"
	if p := principalOf(r); p != nil {
		by = p.String()
	}
	err := s.d.Store.Releases().Acknowledge(r.Context(), dist, ver, by, time.Now().UTC())
	s.recordAudit(r, "release.acknowledge", "release", dist+"/"+ver, map[string]any{"distribution": dist, "version": ver}, err)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	rel, err := s.d.Store.Releases().Get(r.Context(), dist, ver)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s.presentRelease(r.Context(), *rel))
}
