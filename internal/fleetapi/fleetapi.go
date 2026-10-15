// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package fleetapi implements the REST endpoints of the tags fleets,
// blueprints, rolloutPolicies, executors, rollouts, releases, gitops,
// artifacts, notifications and install.
package fleetapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/assignment"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/release"
	"github.com/myriarch/myriarch/internal/rollout"
	"github.com/myriarch/myriarch/internal/store"
)

// Deps are the dependencies of the fleet REST modules.
type Deps struct {
	Store     store.Store
	Syncer    *gitops.Syncer
	Assigner  *assignment.Service
	Renderer  *blueprint.Renderer
	Engine    *rollout.Engine
	Tracker   *release.Tracker
	Artifacts *artifact.Cache
	Notifier  notify.Notifier
	Audit     audit.Recorder
	// Bus announces changed version selections (fleet.version_changed) so that
	// the leader plans them at once; nil in tests without a leader.
	Bus    eventbus.Bus
	Config config.Config
	Logger *slog.Logger
	// Install answers the plan requests of hosts that install from the server and InstallAuth authenticates them
	// by enrollment token (ADR-0046). Without either, or with install.enabled off, the routes of the tag install
	// answer 404.
	Install     *install.Planner
	InstallAuth InstallAuthenticator
	// Registerer receives the metrics of the install routes (nil: none are registered).
	Registerer prometheus.Registerer
}

// statsTTL is how long aggregated fleet statistics are reused (the UI
// refreshes pages often; the aggregation reads all collectors).
const statsTTL = 3 * time.Second

type service struct {
	d   Deps
	log *slog.Logger
	now func() time.Time

	// statsTTL is overridable in tests.
	statsTTL time.Duration
	// waitMax and waitPoll bound how long select-version waits for the leader
	// to plan the fleet (overridable in tests).
	waitMax, waitPoll time.Duration

	statsMu sync.Mutex
	stats   *fleetStats
	statsAt time.Time

	issuesMu     sync.Mutex
	issuesCommit string
	issuesAll    domain.Issues
	issuesValid  bool

	installPlans *prometheus.CounterVec
}

// New returns the fleet modules, one per OpenAPI tag (module name = tag); the
// operations of each tag are defined in api/openapi.yaml.
func New(deps Deps) []api.Module { return newService(deps).modules() }

func newService(deps Deps) *service {
	s := &service{d: deps, now: func() time.Time { return time.Now().UTC() }, statsTTL: statsTTL, waitMax: selectWaitMax, waitPoll: selectWaitPoll}
	s.log = deps.Logger
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.d.Renderer == nil {
		s.d.Renderer = blueprint.NewRenderer(blueprint.Options{})
	}
	if s.d.Notifier == nil {
		s.d.Notifier = notify.Nop{}
	}
	s.installPlans = newInstallMetrics(deps.Registerer)
	return s
}

func (s *service) modules() []api.Module {
	return []api.Module{
		module("fleets", s.fleetRoutes),
		module("blueprints", s.blueprintRoutes),
		module("rolloutPolicies", s.policyRoutes),
		module("executors", s.executorRoutes),
		module("rollouts", s.rolloutRoutes),
		module("releases", s.releaseRoutes),
		module("gitops", s.gitopsRoutes),
		module("artifacts", s.artifactRoutes),
		module("notifications", s.notificationRoutes),
		module("install", s.installRoutes),
	}
}

func module(name string, routes func() []api.Route) api.Module {
	return api.ModuleFunc{ModuleName: name, RoutesFunc: routes}
}

// route builds a Route; permission is the x-permission value of the OpenAPI
// operation ("read", "rollouts:operate", ...).
func route(method, pattern, opID, permission string, h http.HandlerFunc) api.Route {
	r := api.Route{Method: method, Pattern: pattern, OperationID: opID, Handler: h}
	switch permission {
	case "public":
		r.Auth = api.AuthPublic
	case "custom":
		r.Auth = api.AuthCustom
	default:
		r.Auth = api.AuthRequired
		r.Permission = authz.Permission(permission)
	}
	return r
}

// ----------------------------------------------------------------- helpers ----

func (s *service) snapshot(ctx context.Context) (*domain.GitOpsSnapshot, error) {
	if s.d.Assigner != nil {
		return s.d.Assigner.Snapshot(ctx)
	}
	return s.d.Store.GitOps().GetSnapshot(ctx)
}

func principalOf(r *http.Request) *authz.Principal {
	if p := authz.FromContext(r.Context()); p != nil {
		return p
	}
	return nil
}

func (s *service) recordAudit(r *http.Request, action, targetType, targetID string, details map[string]any, err error) {
	if s.d.Audit == nil {
		return
	}
	s.d.Audit.Record(r.Context(), audit.FromRequest(r, action, targetType, targetID, details, err))
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a UUID", domain.ErrInvalidArgument, name)
	}
	return id, nil
}

// queryValues returns repeated and comma separated values of a query parameter.
func queryValues(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func queryBool(r *http.Request, name string) (*bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be true or false", domain.ErrInvalidArgument, name)
	}
	return &b, nil
}

// webURL expands gitops.uiRepoWebUrl ({commit}, {path}).
func (s *service) webURL(src domain.SourceRef) string {
	tmpl := s.d.Config.GitOps.UIRepoWebURL
	if tmpl == "" || src.Path == "" {
		return ""
	}
	commit := src.Commit
	if strings.HasPrefix(commit, "dir:") {
		return ""
	}
	return strings.NewReplacer("{commit}", url.PathEscape(commit), "{path}", src.Path).Replace(tmpl)
}

func versionLess(a, b string) bool {
	va, ea := semver.NewVersion(domain.NormalizeVersion(a))
	vb, eb := semver.NewVersion(domain.NormalizeVersion(b))
	if ea != nil || eb != nil {
		return false
	}
	return va.LessThan(vb)
}

// latestStable returns the highest stable release version of a distribution
// ("" if unknown).
func (s *service) latestStable(ctx context.Context, dist string) string {
	rel, err := s.d.Store.Releases().Latest(ctx, dist, false)
	if err != nil {
		return ""
	}
	return rel.Version
}

// ------------------------------------------------------------ issues cache ----

// issues returns the validation issues of the active snapshot (cached per
// commit), filtered to one object ("Fleet/edge-nl").
func (s *service) issuesFor(snap *domain.GitOpsSnapshot, object string) domain.Issues {
	all := s.allIssues(snap)
	out := domain.Issues{}
	for _, is := range all {
		if is.Object == object {
			out = append(out, is)
		}
	}
	return out
}

func (s *service) allIssues(snap *domain.GitOpsSnapshot) domain.Issues {
	s.issuesMu.Lock()
	defer s.issuesMu.Unlock()
	if s.issuesValid && s.issuesCommit == snap.Commit {
		return s.issuesAll
	}
	s.issuesAll = s.d.Renderer.ValidateSnapshot(snap, blueprint.ValidateOptions{})
	s.issuesCommit, s.issuesValid = snap.Commit, true
	return s.issuesAll
}

// ------------------------------------------------------------ fleet stats ----

type fleetStats struct {
	// selections are the UI version selections by fleet; targets the effective
	// target of every fleet of the snapshot.
	selections map[string]*domain.FleetVersionSelection
	targets    map[string]domain.VersionTarget
	counts     map[string]*dto.FleetCounts
	versions   map[string]map[string]int
	renderErr  map[string]int
	active     map[string][]dto.RolloutSummary
	// byVersion over all collectors of any fleet (agentsBehind).
	allVersions map[string]int
	latest      map[string]string
}

// fleetStatistics aggregates collector state per fleet (cached briefly).
func (s *service) fleetStatistics(ctx context.Context) (*fleetStats, error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.stats != nil && s.statsTTL > 0 && s.now().Sub(s.statsAt) < s.statsTTL {
		return s.stats, nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	sels, err := s.d.Store.FleetVersions().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list version selections: %w", err)
	}
	selections := make(map[string]*domain.FleetVersionSelection, len(sels))
	for i := range sels {
		selections[sels[i].Fleet] = &sels[i]
	}
	targets := make(map[string]domain.VersionTarget, len(snap.Fleets))
	target := map[string]string{}
	for i := range snap.Fleets {
		f := &snap.Fleets[i]
		targets[f.Name] = domain.ResolveVersionTarget(f, selections[f.Name])
		target[f.Name] = targets[f.Name].Version
	}
	agents, err := s.d.Store.Agents().ListAll(ctx, store.AgentFilter{Kinds: []domain.AgentKind{domain.AgentKindCollector}})
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	st := &fleetStats{
		selections: selections, targets: targets,
		counts: map[string]*dto.FleetCounts{}, versions: map[string]map[string]int{}, renderErr: map[string]int{},
		active: map[string][]dto.RolloutSummary{}, allVersions: map[string]int{},
	}
	for i := range agents {
		a := &agents[i]
		st.allVersions[domain.NormalizeVersion(a.Version)]++
		f := a.Assignment.FleetName
		if f == "" {
			continue
		}
		c := st.counts[f]
		if c == nil {
			c = &dto.FleetCounts{Fleet: f, TargetVersion: target[f]}
			st.counts[f] = c
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
		if c.TargetVersion != "" && domain.NormalizeVersion(a.Version) == c.TargetVersion {
			c.OnTarget++
		}
		if a.Assignment.Error != "" {
			st.renderErr[f]++
		}
		vm := st.versions[f]
		if vm == nil {
			vm = map[string]int{}
			st.versions[f] = vm
		}
		vm[domain.NormalizeVersion(a.Version)]++
	}
	rollouts, err := store.ListActiveRollouts(ctx, s.d.Store.Rollouts())
	if err != nil {
		return nil, fmt.Errorf("list rollouts: %w", err)
	}
	names := s.actors(ctx)
	for _, r := range rollouts {
		st.active[r.FleetName] = append(st.active[r.FleetName], rolloutSummaryNamed(r, names))
	}
	st.latest = map[string]string{
		domain.DistributionContrib:    s.latestStable(ctx, domain.DistributionContrib),
		domain.DistributionSupervisor: s.latestStable(ctx, domain.DistributionSupervisor),
	}
	s.stats, s.statsAt = st, s.now()
	return st, nil
}

func (st *fleetStats) versionCounts(fleet string, latest string) []dto.VersionCount {
	m := st.versions[fleet]
	out := make([]dto.VersionCount, 0, len(m))
	for v, n := range m {
		out = append(out, dto.VersionCount{Version: v, Count: n, Outdated: latest != "" && versionLess(v, latest)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func (s *service) fleetStatus(st *fleetStats, f *domain.Fleet, names *actorNames) dto.FleetStatus {
	dist := f.Collector.Distribution
	if dist == "" {
		dist = domain.DistributionContrib
	}
	latest := st.latest[dist]
	vt, ok := st.targets[f.Name]
	if !ok {
		vt = domain.ResolveVersionTarget(f, st.selections[f.Name])
	}
	counts := dto.FleetCounts{Fleet: f.Name, TargetVersion: vt.Version}
	if c := st.counts[f.Name]; c != nil {
		counts = *c
		counts.TargetVersion = vt.Version
	}
	active := st.active[f.Name]
	if active == nil {
		active = []dto.RolloutSummary{}
	}
	return dto.FleetStatus{
		Agents: counts, Versions: st.versionCounts(f.Name, latest), ActiveRollouts: active,
		BehindLatest: vt.Version != "" && latest != "" && versionLess(vt.Version, latest), LatestVersion: latest,
		RenderErrors: st.renderErr[f.Name], VersionTarget: s.versionTarget(f, vt, names),
	}
}

// versionTarget presents the effective target of a fleet: where it comes from,
// the frame from Git and the UI selection (who, when, why).
func (s *service) versionTarget(f *domain.Fleet, vt domain.VersionTarget, names *actorNames) dto.FleetVersionTarget {
	out := dto.FleetVersionTarget{
		Version: vt.Version, Source: vt.Source, GitVersion: domain.NormalizeVersion(f.Collector.Version),
		SelectionEnabled: f.Update.Selection.Active(),
	}
	if f.Update.Selection != nil {
		frame := *f.Update.Selection
		frame.Versions = append([]string(nil), frame.Versions...)
		if len(frame.Versions) == 0 {
			frame.Versions = nil
		}
		out.Frame = &frame
	}
	if vt.Selection != nil {
		sel := *vt.Selection
		sel.SelectedBy = names.Name(sel.SelectedBy)
		out.Selection = &sel
	}
	return out
}

// rolloutSummaryNamed is the list representation of a rollout with the
// creator shown by its display name.
func rolloutSummaryNamed(r domain.Rollout, names *actorNames) dto.RolloutSummary {
	sum := dto.NewRolloutSummary(&r)
	sum.CreatedBy = names.Name(sum.CreatedBy)
	return sum
}

func isNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }
