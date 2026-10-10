// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/actorname"
	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/version"
)

// ------------------------------------------------------- saved filters ---

var filterPage = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// userID returns the user id of a user principal (API tokens cannot own filters).
func userID(r *http.Request) (uuid.UUID, bool) {
	p := authz.FromContext(r.Context())
	if p == nil || p.Type != authz.PrincipalUser {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(p.ID)
	return id, err == nil
}

func (h *handler) listSavedFilters(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("page")
	if page == "" {
		page = "agents"
	}
	uid, ok := userID(r)
	if !ok {
		api.WriteJSON(w, http.StatusOK, api.NewList([]domain.SavedFilter{}))
		return
	}
	filters, err := h.d.Store.Identity().ListSavedFilters(r.Context(), uid, page)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewList(filters))
}

func (h *handler) createSavedFilter(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		writeErr(w, r, fmt.Errorf("%w: saved filters belong to a user; API tokens cannot create them", domain.ErrForbidden))
		return
	}
	var req dto.CreateSavedFilterRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		writeErr(w, r, domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "invalid_name", Path: "name", Message: "name must be 1 to 100 characters"}))
		return
	}
	page := req.Page
	if page == "" {
		page = "agents"
	}
	if !filterPage.MatchString(page) {
		writeErr(w, r, domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "invalid_page", Path: "page", Message: "page must match " + filterPage.String()}))
		return
	}
	q := req.Query
	if q == nil {
		q = map[string]any{}
	}
	f := domain.SavedFilter{ID: uuid.Must(uuid.NewV7()), UserID: uid, Name: name, Page: page, Query: q, Shared: req.Shared, CreatedAt: h.now()}
	if err := h.d.Store.Identity().CreateSavedFilter(r.Context(), f); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			err = fmt.Errorf("%w: a filter named %q already exists", domain.ErrConflict, name)
		}
		h.auditFilter(r, "saved_filter.create", f.ID.String(), map[string]any{"name": name, "page": page, "shared": req.Shared}, err)
		writeErr(w, r, err)
		return
	}
	h.auditFilter(r, "saved_filter.create", f.ID.String(), map[string]any{"name": name, "page": page, "shared": req.Shared}, nil)
	api.WriteJSON(w, http.StatusCreated, f)
}

func (h *handler) deleteSavedFilter(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		writeErr(w, r, fmt.Errorf("%w: saved filters belong to a user", domain.ErrNotFound))
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, badRequest("invalid filter id"))
		return
	}
	if err := h.d.Store.Identity().DeleteSavedFilter(r.Context(), uid, id); err != nil {
		h.auditFilter(r, "saved_filter.delete", id.String(), nil, err)
		writeErr(w, r, err)
		return
	}
	h.auditFilter(r, "saved_filter.delete", id.String(), nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) auditFilter(r *http.Request, action, id string, details map[string]any, err error) {
	if h.d.Audit == nil {
		return
	}
	h.d.Audit.Record(r.Context(), audit.FromRequest(r, action, "saved_filter", id, details, err))
}

// ---------------------------------------------------------------- audit ---

func parseTime(r *http.Request, name string) (*time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, badRequest("%s must be an RFC 3339 timestamp", name)
	}
	u := t.UTC()
	return &u, nil
}

func (h *handler) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	pg, err := api.ParsePagination(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.AuditFilter{ActorID: q.Get("actor"), Action: q.Get("action"), TargetType: q.Get("targetType"), TargetID: q.Get("targetId"), Limit: pg.Limit, Offset: pg.Offset}
	if f.From, err = parseTime(r, "from"); err != nil {
		writeErr(w, r, err)
		return
	}
	if f.To, err = parseTime(r, "to"); err != nil {
		writeErr(w, r, err)
		return
	}
	events, total, err := h.d.Store.Audit().List(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewPage(events, total, pg))
}

// --------------------------------------------------------------- system ---

// stripCredentials removes user info and query strings from a URL ("dir:" and
// scp-like Git addresses are returned without userinfo as well).
func stripCredentials(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return u.String()
	}
	if i := strings.Index(raw, "@"); i >= 0 && !strings.Contains(raw[:i], "/") {
		return raw[i+1:] // git@host:org/repo.git -> host:org/repo.git
	}
	return raw
}

func wsEndpoint(publicURL, path string) string {
	u := strings.TrimRight(publicURL, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + path
}

// markLeader makes sure that at most one replica is displayed as leader. The
// leader flag lives in the heartbeat rows, so a crashed former leader keeps
// its flag until it is reaped, while the new leader already reports it too.
// Only replicas with a recent heartbeat count; among them the one with the
// newest heartbeat wins (a leadership change always happens after the former
// leader's last heartbeat).
func markLeader(replicas []domain.Replica, now time.Time) []domain.Replica {
	best := -1
	for i := range replicas {
		if !replicas[i].IsLeader || now.Sub(replicas[i].LastHeartbeatAt) >= 30*time.Second {
			continue
		}
		if best < 0 || replicas[i].LastHeartbeatAt.After(replicas[best].LastHeartbeatAt) {
			best = i
		}
	}
	for i := range replicas {
		replicas[i].IsLeader = i == best
	}
	return replicas
}

func (h *handler) getSystemInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg := h.d.Config
	bi := version.Get()
	info := dto.SystemInfo{
		Version: bi.Version, Commit: bi.Commit, BuildDate: bi.BuildDate, ReplicaID: h.d.ReplicaID, Replicas: []domain.Replica{},
		PublicURL: cfg.Server.PublicURL, OpAMPEndpoint: wsEndpoint(cfg.Server.PublicURL, cfg.OpAMP.Path),
		Features: dto.FeatureFlags{OIDC: cfg.Auth.OIDC.Enabled, DevLogin: cfg.Auth.DevLogin.Enabled, BootstrapAdmin: cfg.Auth.BootstrapActive(), GitOps: cfg.GitOps.Enabled, Releases: cfg.Releases.Enabled, DevMode: h.d.DevMode},
		Settings: dto.SettingsSummary{
			GitOpsSource: stripCredentials(cfg.GitOps.RepoURL), GitOpsBranch: cfg.GitOps.Branch, GitOpsPollInterval: cfg.GitOps.PollInterval.String(),
			ReleasesRepo: cfg.Releases.ReleasesRepo, ReleasesPollInterval: cfg.Releases.PollInterval.String(), TrackedDistributions: append([]string{}, cfg.Releases.Distributions...),
			ArtifactSource: stripCredentials(cfg.Artifacts.SourceBaseURL), SignatureVerification: cfg.Artifacts.Verify.RequireSignature,
			DefaultTimezone: cfg.Rollouts.DefaultTimezone, OfflineAfter: cfg.OpAMP.OfflineAfter.String(),
			RetentionAgentEvents: cfg.Retention.AgentEvents.String(), RetentionAudit: cfg.Retention.Audit.String(),
		},
	}
	if replicas, err := h.d.Store.Replicas().List(ctx); err == nil {
		for i := range replicas {
			if replicas[i].ID != h.d.ReplicaID {
				continue
			}
			// the local replica knows better than its (up to one interval old) heartbeat row
			if h.d.LocalConnections != nil {
				replicas[i].Connections = h.d.LocalConnections()
			}
			if h.d.Elector != nil {
				replicas[i].IsLeader = h.d.Elector.IsLeader()
				replicas[i].LastHeartbeatAt = h.now()
			}
		}
		info.Replicas = markLeader(replicas, h.now())
		for _, rep := range info.Replicas {
			if rep.IsLeader {
				info.LeaderID = rep.ID
			}
		}
	} else {
		h.log.Warn("cannot list replicas", "error", err)
	}
	if info.LeaderID == "" && h.d.Elector != nil && h.d.Elector.IsLeader() {
		info.LeaderID = h.d.ReplicaID
	}
	if err := h.d.Store.Ping(ctx); err == nil {
		info.Database.OK = true
		if h.d.SchemaInfo != nil {
			if sv, mv, err := h.d.SchemaInfo(ctx); err == nil {
				info.Database.ServerVersion, info.Database.MigrationVersion = sv, mv
			} else {
				h.log.Warn("cannot read schema info", "error", err)
			}
		}
	}
	api.WriteJSON(w, http.StatusOK, info)
}

// ---------------------------------------------------------------- stats ---

const overviewTTL = 2 * time.Second

func facetCount(vs []store.FacetValue, value string) int {
	for _, v := range vs {
		if v.Value == value {
			return v.Count
		}
	}
	return 0
}

func (h *handler) getOverviewStats(w http.ResponseWriter, r *http.Request) {
	o, err := h.cachedOverview(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, h.withFreshAcknowledgement(r, o))
}

func (h *handler) cachedOverview(r *http.Request) (*dto.OverviewStats, error) {
	h.overviewMu.Lock()
	defer h.overviewMu.Unlock()
	if h.overview != nil && h.now().Sub(h.overviewAt) < overviewTTL {
		return h.overview, nil
	}
	o, err := h.computeOverview(r)
	if err != nil {
		return nil, err
	}
	h.overview, h.overviewAt = o, h.now()
	return o, nil
}

// withFreshAcknowledgement returns o with the acknowledgement state of the
// latest release read live: it changes through an operator action that the UI
// refetches right after, which must not see the up to overviewTTL old cached
// value (the banner would stay).
func (h *handler) withFreshAcknowledgement(r *http.Request, o *dto.OverviewStats) *dto.OverviewStats {
	if o.LatestRelease == nil {
		return o
	}
	rel, err := h.d.Store.Releases().Get(r.Context(), o.LatestRelease.Distribution, o.LatestRelease.Version)
	if err != nil {
		return o
	}
	acked := rel.AcknowledgedAt != nil
	if acked == o.LatestRelease.Acknowledged {
		return o
	}
	cp := *o
	lr := *o.LatestRelease
	lr.Acknowledged = acked
	cp.LatestRelease = &lr
	return &cp
}

func (h *handler) computeOverview(r *http.Request) (*dto.OverviewStats, error) {
	ctx := r.Context()
	st := h.d.Store
	latest := h.latestVersion(ctx)
	o := &dto.OverviewStats{GeneratedAt: h.now(), Versions: []dto.VersionCount{}, ConfigSync: []store.FacetValue{}, Fleets: []dto.FleetCounts{}, ActiveRollouts: []dto.RolloutSummary{}}

	all, err := st.Agents().Facets(ctx, store.AgentFilter{})
	if err != nil {
		return nil, err
	}
	o.Agents = dto.AgentCounts{
		Total: all.Total, Collectors: facetCount(all.Kinds, string(domain.AgentKindCollector)), Updaters: facetCount(all.Kinds, string(domain.AgentKindUpdater)),
		Online: facetCount(all.Status, string(domain.ConnectionOnline)), Offline: facetCount(all.Status, string(domain.ConnectionOffline)),
		Healthy: facetCount(all.Health, "healthy"), Unhealthy: facetCount(all.Health, "unhealthy"), HealthUnknown: facetCount(all.Health, "unknown"),
	}
	collectors, err := st.Agents().Facets(ctx, store.AgentFilter{Kinds: []domain.AgentKind{domain.AgentKindCollector}})
	if err != nil {
		return nil, err
	}
	outdated := 0
	for _, v := range collectors.Versions {
		out := IsOutdated(v.Value, latest)
		if out {
			outdated += v.Count
		}
		o.Versions = append(o.Versions, dto.VersionCount{Version: v.Value, Count: v.Count, Outdated: out})
	}
	sort.SliceStable(o.Versions, func(i, j int) bool { return o.Versions[i].Count > o.Versions[j].Count })
	o.Outdated = dto.OutdatedSummary{LatestVersion: latest, Count: outdated}
	if collectors.Total > 0 {
		o.Outdated.Percent = float64(int(float64(outdated)/float64(collectors.Total)*1000+0.5)) / 10
	}
	if collectors.ConfigSync != nil {
		o.ConfigSync = collectors.ConfigSync
	}
	o.Unassigned = facetCount(collectors.Fleets, "")

	if h.d.FleetCounts != nil {
		fc, err := h.d.FleetCounts(ctx)
		if err != nil {
			return nil, err
		}
		if fc != nil {
			o.Fleets = fc
		}
	} else if fc, err := h.fleetCountsFromList(r); err != nil {
		return nil, err
	} else {
		o.Fleets = fc
	}

	active, _, err := st.Rollouts().List(ctx, store.RolloutFilter{ActiveOnly: true, Limit: 50})
	if err != nil {
		return nil, err
	}
	names := actorname.New(ctx, st.Identity())
	for i := range active {
		o.ActiveRollouts = append(o.ActiveRollouts, RolloutSummaryNamed(&active[i], names))
	}

	if rel, err := st.Releases().Latest(ctx, domain.DistributionContrib, false); err == nil {
		sum, err := h.releaseSummary(r, rel)
		if err != nil {
			return nil, err
		}
		o.LatestRelease = &sum
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	gs, err := st.GitOps().GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	o.GitOps = dto.GitOpsBrief{State: gs.State, IssueCount: len(gs.Issues)}
	if gs.AppliedCommit != nil {
		o.GitOps.AppliedCommit = gs.AppliedCommit.SHA
	}
	return o, nil
}

// fleetCountsFromList computes FleetCounts over the agent list (fallback when
// no aggregate query is wired).
func (h *handler) fleetCountsFromList(r *http.Request) ([]dto.FleetCounts, error) {
	ctx := r.Context()
	agents, err := h.d.Store.Agents().ListAll(ctx, store.AgentFilter{Kinds: []domain.AgentKind{domain.AgentKindCollector}})
	if err != nil {
		return nil, err
	}
	byFleet := map[string]*dto.FleetCounts{}
	for i := range agents {
		a := &agents[i]
		if a.Assignment.FleetName == "" {
			continue
		}
		c := byFleet[a.Assignment.FleetName]
		if c == nil {
			c = &dto.FleetCounts{Fleet: a.Assignment.FleetName}
			byFleet[c.Fleet] = c
		}
		c.Total++
		if a.IsOnline() {
			c.Online++
		}
		if a.Health != nil && a.Health.Healthy {
			c.Healthy++
		}
		switch a.ConfigSyncState() {
		case domain.ConfigSyncInSync:
			c.InSync++
		case domain.ConfigSyncPending, domain.ConfigSyncDrift:
			c.Pending++
		case domain.ConfigSyncFailed:
			c.Failed++
		}
		if a.Assignment.TargetVersion != "" {
			c.TargetVersion = a.Assignment.TargetVersion
			if a.Version == a.Assignment.TargetVersion {
				c.OnTarget++
			}
		}
	}
	out := make([]dto.FleetCounts, 0, len(byFleet))
	for _, c := range byFleet {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fleet < out[j].Fleet })
	return out, nil
}

func (h *handler) releaseSummary(r *http.Request, rel *domain.Release) (dto.ReleaseSummary, error) {
	return ReleaseSummaryOf(r.Context(), h.d.Store, rel)
}

// ReleaseSummaryOf builds the list representation of a release. FleetsBehind
// counts fleets of the release's distribution (default otelcol-contrib) whose
// target version is below the release.
func ReleaseSummaryOf(ctx context.Context, st store.Store, rel *domain.Release) (dto.ReleaseSummary, error) {
	s := dto.ReleaseSummary{
		Distribution: rel.Distribution, Version: rel.Version, Tag: rel.Tag, PublishedAt: rel.PublishedAt, Prerelease: rel.Prerelease, HTMLURL: rel.HTMLURL,
		BreakingCount: len(rel.BreakingChanges), DeprecationCount: len(rel.Deprecations), Acknowledged: rel.AcknowledgedAt != nil,
	}
	if latest, err := st.Releases().Latest(ctx, rel.Distribution, false); err == nil {
		s.IsLatest = latest.Version == rel.Version
	}
	fleets, err := st.GitOps().ListFleets(ctx)
	if err != nil {
		return s, err
	}
	sels, err := st.FleetVersions().List(ctx)
	if err != nil {
		return s, err
	}
	selByFleet := make(map[string]*domain.FleetVersionSelection, len(sels))
	for i := range sels {
		selByFleet[sels[i].Fleet] = &sels[i]
	}
	for i := range fleets {
		f := &fleets[i]
		dist := f.Collector.Distribution
		if dist == "" {
			dist = domain.DistributionContrib
		}
		// the effective target: a UI selection counts like the Git target
		if target := domain.ResolveVersionTarget(f, selByFleet[f.Name]).Version; dist == rel.Distribution && target != "" && IsOutdated(target, rel.Version) {
			s.FleetsBehind++
		}
	}
	return s, nil
}
