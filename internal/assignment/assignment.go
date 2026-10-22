// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package assignment evaluates fleet membership (inventory labels,
// selectors, priorities), renders assigned configs and applies the bootstrap
// rule. Implements control.Assigner.
package assignment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	lru "github.com/myriarch/myriarch/internal/lru"
	"github.com/myriarch/myriarch/internal/store"
)

const (
	reassignWorkers  = 8
	snapshotMaxAge   = 5 * time.Minute
	tokenCacheTTL    = time.Minute
	tokenRefreshGap  = 10 * time.Second
	renderedPutTTL   = 5 * time.Minute
	lockStripes      = 64
	assignmentChunk  = 100
	maxErrorMessages = 3
)

// Deps are the dependencies of the service.
type Deps struct {
	Store      store.Store
	Bus        eventbus.Bus
	Control    control.AgentControl
	Renderer   *blueprint.Renderer
	Logger     *slog.Logger
	Registerer prometheus.Registerer
}

// Service implements control.Assigner.
type Service struct {
	deps Deps
	log  *slog.Logger
	// now is the clock (tests).
	now func() time.Time

	snap   atomic.Pointer[state]
	loadMu sync.Mutex

	tokMu      sync.Mutex
	tokLabels  map[uuid.UUID]map[string]string
	tokLoaded  time.Time
	tokRefresh time.Time

	comps *lru.Cache[string, *domain.ComponentSet]

	putMu     sync.Mutex
	recentPut map[string]time.Time

	locks [lockStripes]sync.Mutex

	// written remembers when this process last changed an agent's assignment
	// (ReassignAll re-reads agents whose listing may be stale).
	writtenMu sync.Mutex
	written   map[domain.AgentID]time.Time

	duration *prometheus.HistogramVec
}

type state struct {
	snap *domain.GitOpsSnapshot
	// selections are the target versions operators chose in the UI, by fleet
	// (the effective target of a fleet is domain.ResolveVersionTarget).
	selections map[string]*domain.FleetVersionSelection
	loadedAt   time.Time
}

// targetVersion is the effective target version of a fleet.
func (st *state) targetVersion(f *domain.Fleet) string {
	return domain.ResolveVersionTarget(f, st.selections[f.Name]).Version
}

var _ control.Assigner = (*Service)(nil)

// New creates the service.
func New(deps Deps) *Service {
	s := &Service{
		deps: deps, now: func() time.Time { return time.Now().UTC() },
		tokLabels: map[uuid.UUID]map[string]string{}, recentPut: map[string]time.Time{}, written: map[domain.AgentID]time.Time{},
	}
	s.log = deps.Logger
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "assignment")
	s.comps, _ = lru.New[string, *domain.ComponentSet](128)
	if s.deps.Renderer == nil {
		s.deps.Renderer = blueprint.NewRenderer(blueprint.Options{})
	}
	s.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "myriarch_assignment_duration_seconds", Help: "Duration of fleet matching and rendering for one agent.",
		Buckets: []float64{.001, .0025, .005, .01, .02, .05, .1, .25, 1},
	}, []string{"result"})
	if deps.Registerer != nil {
		if err := deps.Registerer.Register(s.duration); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				if h, ok := are.ExistingCollector.(*prometheus.HistogramVec); ok {
					s.duration = h
				}
			}
		}
	}
	return s
}

// ---------------------------------------------------------------- snapshot --

// Run (every replica) keeps the in-memory GitOps snapshot and the UI version
// selections current: loads them at start, reloads on eventbus.GitOpsSynced,
// eventbus.FleetVersionChanged and eventbus.BusReconnected events and at
// least every 5 minutes. Returns when ctx is done.
func (s *Service) Run(ctx context.Context) error {
	events, cancel := s.deps.Bus.Subscribe(eventbus.GitOpsSynced, eventbus.FleetVersionChanged, eventbus.BusReconnected)
	defer cancel()
	if _, err := s.Reload(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("initial gitops snapshot load failed (will retry)", "error", err)
	}
	ticker := time.NewTicker(snapshotMaxAge)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case _, ok := <-events:
			if !ok {
				return nil
			}
			// Coalesce bursts.
			for drained := false; !drained; {
				select {
				case <-events:
				default:
					drained = true
				}
			}
		}
		if _, err := s.Reload(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("reload gitops snapshot failed", "error", err)
		}
	}
}

// Reload loads the active snapshot and the version selections from the store
// and makes them current.
func (s *Service) Reload(ctx context.Context) (*domain.GitOpsSnapshot, error) {
	st, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return st.snap, nil
}

func (s *Service) load(ctx context.Context) (*state, error) {
	snap, err := s.deps.Store.GitOps().GetSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("load gitops snapshot: %w", err)
	}
	list, err := s.deps.Store.FleetVersions().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load version selections: %w", err)
	}
	sels := make(map[string]*domain.FleetVersionSelection, len(list))
	for i := range list {
		sels[list[i].Fleet] = &list[i]
	}
	st := &state{snap: snap, selections: sels, loadedAt: s.now()}
	s.snap.Store(st)
	return st, nil
}

// current returns the in-memory state, loading it on first use.
func (s *Service) current(ctx context.Context) (*state, error) {
	if st := s.snap.Load(); st != nil {
		return st, nil
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if st := s.snap.Load(); st != nil {
		return st, nil
	}
	return s.load(ctx)
}

// Snapshot returns the current snapshot (loading it if needed).
func (s *Service) Snapshot(ctx context.Context) (*domain.GitOpsSnapshot, error) {
	st, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return st.snap, nil
}

// VersionSelections returns the current UI version selections by fleet name
// (the map must not be modified).
func (s *Service) VersionSelections(ctx context.Context) (map[string]*domain.FleetVersionSelection, error) {
	st, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return st.selections, nil
}

// ------------------------------------------------------------------ assign --

// outcome of assigning one agent.
type outcome struct {
	assignment      domain.Assignment
	assignmentWrote bool // SetAssignment was written (fleet, hash, version or error changed)
	bridgeLocked    bool // the agent is a Bridge that its fleet does not configure
}

func (s *Service) lockFor(id domain.AgentID) *sync.Mutex { return &s.locks[int(id[15])%lockStripes] }

// AssignAgent implements control.Assigner.
func (s *Service) AssignAgent(ctx context.Context, id domain.AgentID) (domain.Assignment, error) {
	start := time.Now()
	st, err := s.current(ctx)
	if err != nil {
		return domain.Assignment{AgentID: id}, err
	}
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	ag, err := s.deps.Store.Agents().Get(ctx, id)
	if err != nil {
		return domain.Assignment{AgentID: id}, fmt.Errorf("assign agent %s: %w", id, err)
	}
	out, err := s.assignOne(ctx, st, ag, time.Time{})
	result := "ok"
	if err != nil {
		result = "error"
	} else if out.bridgeLocked {
		result = "bridge_locked"
	} else if out.assignment.Error != "" {
		result = "render_error"
	}
	s.duration.WithLabelValues(result).Observe(time.Since(start).Seconds())
	return out.assignment, err
}

// staleSince reports whether ag (listed at listedAt) may be stale because
// another call wrote it at or after that time. A write in the same clock tick
// as the listing counts as later: the clock can be coarse (Windows advances it
// in steps of up to 15.6 ms), and an equal time says nothing about the order.
// Both times must come from time.Now(), so that the comparison uses the
// monotonic reading (s.now() strips it).
func (s *Service) staleSince(id domain.AgentID, listedAt time.Time) bool {
	if listedAt.IsZero() {
		return false
	}
	s.writtenMu.Lock()
	defer s.writtenMu.Unlock()
	t, ok := s.written[id]
	return ok && !t.Before(listedAt)
}

func (s *Service) markWritten(id domain.AgentID) {
	s.writtenMu.Lock()
	defer s.writtenMu.Unlock()
	now := time.Now()
	s.written[id] = now
	if len(s.written) > 20000 {
		for k, t := range s.written {
			if now.Sub(t) > time.Minute {
				delete(s.written, k)
			}
		}
	}
}

// assignOne is the shared implementation of AssignAgent and ReassignAll. The
// caller holds the agent's stripe lock. listedAt is the time ag was read as
// part of a list (zero: ag is fresh).
func (s *Service) assignOne(ctx context.Context, st *state, ag *domain.Agent, listedAt time.Time) (outcome, error) {
	snap := st.snap
	res := outcome{assignment: domain.Assignment{AgentID: ag.ID}}
	if ag.Kind != domain.AgentKindCollector && ag.Kind != domain.AgentKindBridge {
		return res, nil
	}
	if s.staleSince(ag.ID, listedAt) {
		fresh, err := s.deps.Store.Agents().Get(ctx, ag.ID)
		if err != nil {
			return res, fmt.Errorf("re-read agent %s: %w", ag.ID, err)
		}
		ag = fresh
	}
	defer func() {
		if res.assignmentWrote || res.assignment.DesiredChanged {
			s.markWritten(ag.ID)
		}
	}()
	// Effective labels: enrollment token labels, inventory labels win.
	plain := *ag
	plain.Labels = nil
	attrs := map[string]string(domain.NewAttributeView(&plain))
	inv := blueprint.ApplyInventory(snap, attrs)
	labels := map[string]string{}
	for k, v := range s.tokenLabels(ctx, ag.EnrollmentTokenID) {
		labels[k] = v
	}
	for k, v := range inv.Labels {
		labels[k] = v
	}
	if !equalStringMaps(labels, ag.Labels) {
		if err := s.deps.Store.Agents().SetLabels(ctx, ag.ID, labels); err != nil {
			return res, fmt.Errorf("set labels of agent %s: %w", ag.ID, err)
		}
	}
	view := blueprint.ViewFrom(attrs, labels)
	match := blueprint.MatchFleet(snap, view)
	res.assignment.Candidates = match.Candidates

	prev := ag.Assignment
	now := s.now()
	write := func(u store.AgentAssignmentUpdate) error {
		changed := prev.FleetName != u.FleetName || !prev.AssignedConfigHash.Equal(u.AssignedConfigHash) ||
			prev.TargetVersion != u.TargetVersion || prev.Error != u.Error
		if !changed {
			return nil
		}
		u.AssignedAt = now
		if err := s.deps.Store.Agents().SetAssignment(ctx, ag.ID, u); err != nil {
			return fmt.Errorf("set assignment of agent %s: %w", ag.ID, err)
		}
		res.assignmentWrote = true
		return nil
	}

	// Unassigned (no match or ambiguous).
	if match.Fleet == nil {
		res.assignment.Error = match.Error
		if err := write(store.AgentAssignmentUpdate{Error: match.Error}); err != nil {
			return res, err
		}
		if prev.FleetName != "" {
			s.fleetEvent(ctx, ag.ID, "", prev.FleetName, snap.Commit, match.Error)
		}
		return res, nil
	}

	fleet := match.Fleet
	res.assignment.FleetName = fleet.Name
	if ag.Kind == domain.AgentKindBridge {
		// A Bridge is a member of the fleet that selects it, but is rendered a
		// configuration only when the fleet opted in (ADR-0037). The opamp
		// server holds the offer back as well.
		if reason := domain.BridgeConfigLock(ag, fleet); reason != "" {
			res.assignment.Error = reason
			res.bridgeLocked = true
			if err := write(store.AgentAssignmentUpdate{FleetName: fleet.Name, Error: reason}); err != nil {
				return res, err
			}
			if prev.FleetName != fleet.Name {
				s.fleetEvent(ctx, ag.ID, fleet.Name, prev.FleetName, snap.Commit, "")
			}
			return res, nil
		}
	}
	if ag.Kind != domain.AgentKindBridge {
		// A Bridge is no member for versions (ADR-0037): it gets no target.
		res.assignment.TargetVersion = st.targetVersion(fleet)
	}

	render, rerr := s.deps.Renderer.Render(blueprint.RenderInput{
		Snapshot: snap, Fleet: fleet, Agent: blueprint.AgentInput{ID: &ag.ID, Attributes: view},
		InventoryParameters: inv.Parameters, Components: s.componentSet(ctx, ag),
	})
	if rerr != nil {
		render.Issues = append(render.Issues, domain.Issue{Severity: domain.SeverityError, Code: "internal", Message: rerr.Error()})
	}
	if render.Issues.HasErrors() {
		// Keep the previous assigned config: the agent keeps running what it has.
		msg := issueSummary(render.Issues)
		res.assignment.Error = msg
		res.assignment.AssignedConfigHash = prev.AssignedConfigHash
		if err := write(store.AgentAssignmentUpdate{
			FleetName: fleet.Name, AssignedConfigHash: prev.AssignedConfigHash,
			TargetVersion: res.assignment.TargetVersion, Error: msg,
		}); err != nil {
			return res, err
		}
		if prev.FleetName != fleet.Name {
			s.fleetEvent(ctx, ag.ID, fleet.Name, prev.FleetName, snap.Commit, msg)
		}
		return res, nil
	}

	if err := s.putRendered(ctx, render, fleet, snap.Commit); err != nil {
		return res, err
	}
	res.assignment.AssignedConfigHash = render.Hash
	if err := write(store.AgentAssignmentUpdate{
		FleetName: fleet.Name, AssignedConfigHash: render.Hash, TargetVersion: res.assignment.TargetVersion,
	}); err != nil {
		return res, err
	}

	// Bootstrap rule (docs/concepts/fleets.md, "When does the offered configuration change?").
	fleetChanged := prev.FleetName != fleet.Name
	if fleetChanged {
		if err := s.deps.Store.Agents().SetDesiredConfig(ctx, []domain.AgentID{ag.ID}, render.Hash); err != nil {
			return res, fmt.Errorf("set desired config of agent %s: %w", ag.ID, err)
		}
		res.assignment.DesiredChanged = !prev.DesiredConfigHash.Equal(render.Hash)
		s.fleetEvent(ctx, ag.ID, fleet.Name, prev.FleetName, snap.Commit, "")
	} else if prev.DesiredConfigHash.IsZero() {
		changed, err := s.deps.Store.Agents().SetDesiredConfigIfUnset(ctx, []domain.AgentID{ag.ID}, render.Hash)
		if err != nil {
			return res, fmt.Errorf("bootstrap desired config of agent %s: %w", ag.ID, err)
		}
		res.assignment.DesiredChanged = len(changed) > 0
	}
	return res, nil
}

func issueSummary(is domain.Issues) string {
	var parts []string
	for _, i := range is {
		if i.Severity != domain.SeverityError {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", i.Code, i.Message))
		if len(parts) == maxErrorMessages {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func (s *Service) fleetEvent(ctx context.Context, id domain.AgentID, fleet, previous, commit, errMsg string) {
	msg := "assigned to fleet " + fleet
	sev := "info"
	switch {
	case fleet == "" && errMsg != "":
		msg, sev = "unassigned: "+errMsg, "warning"
	case fleet == "":
		msg = "no longer assigned to a fleet (was " + previous + ")"
	case errMsg != "":
		msg += " (config render failed: " + errMsg + ")"
		sev = "warning"
	case previous != "":
		msg += " (was " + previous + ")"
	}
	ev := domain.AgentEvent{
		AgentID: id, Time: s.now(), Type: domain.AgentEventAssigned, Severity: sev, Message: msg,
		Details: map[string]any{"fleet": fleet, "previousFleet": previous, "commit": commit},
	}
	if err := s.deps.Store.Agents().AppendEvents(ctx, []domain.AgentEvent{ev}); err != nil {
		s.log.Warn("append assignment event failed", "agent_id", id, "error", err)
	}
}

// putRendered stores the rendered config (idempotent; repeated puts of the
// same hash within a short time are skipped).
func (s *Service) putRendered(ctx context.Context, r domain.RenderResult, fleet *domain.Fleet, commit string) error {
	key := r.Hash.String()
	now := s.now()
	s.putMu.Lock()
	if t, ok := s.recentPut[key]; ok && now.Sub(t) < renderedPutTTL {
		s.putMu.Unlock()
		return nil
	}
	s.putMu.Unlock()
	err := s.deps.Store.Configs().PutRendered(ctx, domain.RenderedConfig{
		Hash: r.Hash, Body: r.Body, ContentType: domain.RemoteConfigContentType,
		FleetName: fleet.Name, BlueprintName: r.Blueprint, GitCommit: commit, Size: len(r.Body), CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("store rendered config %s: %w", r.Hash.Short(), err)
	}
	s.putMu.Lock()
	s.recentPut[key] = now
	if len(s.recentPut) > 4096 {
		for k, t := range s.recentPut {
			if now.Sub(t) >= renderedPutTTL {
				delete(s.recentPut, k)
			}
		}
	}
	s.putMu.Unlock()
	return nil
}

// tokenLabels returns the labels of an enrollment token (cached).
func (s *Service) tokenLabels(ctx context.Context, id *uuid.UUID) map[string]string {
	if id == nil {
		return nil
	}
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	now := s.now()
	l, ok := s.tokLabels[*id]
	stale := now.Sub(s.tokLoaded) > tokenCacheTTL
	if (!ok && now.Sub(s.tokRefresh) > tokenRefreshGap) || (stale && now.Sub(s.tokRefresh) > tokenRefreshGap) {
		s.tokRefresh = now
		tokens, err := s.deps.Store.Identity().ListEnrollmentTokens(ctx)
		if err != nil {
			s.log.Warn("load enrollment tokens failed", "error", err)
		} else {
			m := make(map[uuid.UUID]map[string]string, len(tokens))
			for _, t := range tokens {
				m[t.ID] = t.Labels
			}
			s.tokLabels, s.tokLoaded = m, now
			l, ok = m[*id]
		}
	}
	if !ok {
		return nil
	}
	return l
}

// componentSet returns the agent's reported component set (cached by hash).
func (s *Service) componentSet(ctx context.Context, ag *domain.Agent) *domain.ComponentSet {
	if ag.AvailableComponentsHash.IsZero() {
		return nil
	}
	key := ag.AvailableComponentsHash.String()
	if cs, ok := s.comps.Get(key); ok {
		return cs
	}
	cs, err := s.deps.Store.Configs().GetComponentSet(ctx, ag.AvailableComponentsHash)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.log.Warn("load component set failed", "agent_id", ag.ID, "error", err)
		}
		return nil
	}
	s.comps.Add(key, cs)
	return cs
}

// ------------------------------------------------------------- reassign all --

// ReassignAll re-evaluates all agents (after a GitOps snapshot change),
// pushes bootstrap configs via Control and publishes
// eventbus.AssignmentChanged.
func (s *Service) ReassignAll(ctx context.Context) error {
	st, err := s.load(ctx)
	if err != nil {
		return err
	}
	snap := st.snap
	listedAt := time.Now()
	agents, err := s.deps.Store.Agents().ListAll(ctx, store.AgentFilter{Kinds: []domain.AgentKind{domain.AgentKindCollector, domain.AgentKindBridge}})
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	var mu sync.Mutex
	var desiredChanged, assignmentChanged []domain.AgentID
	var failed int
	var firstErr error
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(reassignWorkers)
	for i := range agents {
		ag := &agents[i]
		g.Go(func() error {
			if gctx.Err() != nil {
				return nil
			}
			lk := s.lockFor(ag.ID)
			lk.Lock()
			out, err := s.assignOne(gctx, st, ag, listedAt)
			lk.Unlock()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
				s.log.Warn("assign agent failed", "agent_id", ag.ID, "error", err)
				return nil
			}
			if out.assignmentWrote {
				assignmentChanged = append(assignmentChanged, ag.ID)
			}
			if out.assignment.DesiredChanged {
				desiredChanged = append(desiredChanged, ag.ID)
			}
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(desiredChanged) > 0 && s.deps.Control != nil {
		if err := s.deps.Control.DesiredStateChanged(ctx, desiredChanged); err != nil {
			s.log.Warn("push bootstrap configs failed", "error", err)
		}
	}
	if s.deps.Bus != nil {
		for _, chunk := range eventbus.ChunkAgentIDs(assignmentChanged) {
			if err := s.deps.Bus.Publish(ctx, eventbus.AssignmentChanged, eventbus.AgentIDsPayload{AgentIDs: chunk}); err != nil {
				s.log.Warn("publish assignment.changed failed", "error", err)
			}
		}
	}
	s.log.Info("reassigned agents", "agents", len(agents), "assignments_changed", len(assignmentChanged),
		"bootstrapped", len(desiredChanged), "failed", failed, "commit", snap.Commit)
	if failed > 0 {
		return fmt.Errorf("%d of %d agents could not be assigned (first error: %w)", failed, len(agents), firstErr)
	}
	return nil
}

// MatchResult explains fleet matching for an attribute view.
type MatchResult struct {
	Fleet           string
	Candidates      []string
	InventoryLabels map[string]string
	Error           string
}

// Match evaluates fleet membership for an attribute view without persisting
// (POST /fleets/match).
func (s *Service) Match(ctx context.Context, view domain.AttributeView) (MatchResult, error) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return MatchResult{}, err
	}
	attrs, labels := blueprint.SplitView(view)
	inv := blueprint.ApplyInventory(snap, attrs)
	for k, v := range inv.Labels {
		labels[k] = v
	}
	m := blueprint.MatchFleet(snap, blueprint.ViewFrom(attrs, labels))
	res := MatchResult{Candidates: m.Candidates, InventoryLabels: inv.Labels, Error: m.Error}
	if m.Fleet != nil {
		res.Fleet = m.Fleet.Name
	}
	return res, nil
}
