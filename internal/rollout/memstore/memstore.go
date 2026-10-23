// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package memstore is an in-memory store.Store for tests of the fleet
// packages (gitops, assignment, rollout, executors, release, artifact,
// fleetapi). It implements the semantics documented in internal/store/store.go
// for the methods the fleet code uses; all other methods panic (embedded nil
// interface), so unexpected calls are noticed.
//
// Test support only: never import from production code.
package memstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// Store is the in-memory store.Store.
type Store struct {
	mu sync.Mutex
	// Now is the clock for store-assigned timestamps (default time.Now).
	Now func() time.Time

	agents    map[domain.AgentID]*domain.Agent
	events    map[domain.AgentID][]domain.AgentEvent
	eventSeq  int64
	rendered  map[string]domain.RenderedConfig
	compSets  map[string]domain.ComponentSet
	snapshot  domain.GitOpsSnapshot
	snapJSON  []byte
	syncs     []domain.GitOpsSync
	syncSeq   int64
	status    domain.GitOpsSyncStatus
	statusSet bool
	rollouts  map[uuid.UUID]*rolloutRec
	rolloutSq int
	fleetVers map[string]domain.FleetVersionSelection
	offers    map[domain.AgentID][]domain.PackageOffer
	releases  map[string]domain.Release
	artifacts map[uuid.UUID]domain.Artifact
	kv        map[string][]byte
	audit     []domain.AuditEvent
	tokens    []domain.EnrollmentToken
	users     map[uuid.UUID]domain.User
	apiTokens []domain.APIToken
	tick      int64

	// Hooks for fault injection in tests.
	FailGetSnapshot error
}

// New returns an empty store.
func New() *Store {
	return &Store{
		Now:       func() time.Time { return time.Now().UTC() },
		agents:    map[domain.AgentID]*domain.Agent{},
		events:    map[domain.AgentID][]domain.AgentEvent{},
		rendered:  map[string]domain.RenderedConfig{},
		compSets:  map[string]domain.ComponentSet{},
		rollouts:  map[uuid.UUID]*rolloutRec{},
		fleetVers: map[string]domain.FleetVersionSelection{},
		offers:    map[domain.AgentID][]domain.PackageOffer{},
		releases:  map[string]domain.Release{},
		artifacts: map[uuid.UUID]domain.Artifact{},
		kv:        map[string][]byte{},
		status:    domain.GitOpsSyncStatus{State: "never", Issues: domain.Issues{}, ObjectCounts: map[domain.ObjectKind]int{}},
	}
}

var _ store.Store = (*Store)(nil)

func (s *Store) now() time.Time { return s.Now().UTC() }

// uniqueNow returns a strictly increasing timestamp (optimistic concurrency
// compares updated_at, so consecutive writes must differ).
func (s *Store) uniqueNow(prev time.Time) time.Time {
	t := s.now()
	if !t.After(prev) {
		t = prev.Add(time.Microsecond)
	}
	return t
}

// Agents implements store.Store.
func (s *Store) Agents() store.AgentStore { return &agentStore{s: s} }

// Configs implements store.Store.
func (s *Store) Configs() store.ConfigStore { return &configStore{s: s} }

// GitOps implements store.Store.
func (s *Store) GitOps() store.GitOpsStore { return &gitopsStore{s: s} }

// Rollouts implements store.Store.
func (s *Store) Rollouts() store.RolloutStore { return &rolloutStore{s: s} }

// FleetVersions implements store.Store.
func (s *Store) FleetVersions() store.FleetVersionStore { return &fleetVersionStore{s: s} }

// Packages implements store.Store.
func (s *Store) Packages() store.PackageOfferStore { return &packageStore{s: s} }

// Releases implements store.Store.
func (s *Store) Releases() store.ReleaseStore { return &releaseStore{s: s} }

// Artifacts implements store.Store.
func (s *Store) Artifacts() store.ArtifactStore { return &artifactStore{s: s} }

// Identity implements store.Store.
func (s *Store) Identity() store.IdentityStore { return &identityStore{s: s} }

// Audit implements store.Store.
func (s *Store) Audit() store.AuditStore { return &auditStore{s: s} }

// Replicas implements store.Store (unused: panics).
func (s *Store) Replicas() store.ReplicaStore { return nil }

// KV implements store.Store.
func (s *Store) KV() store.KVStore { return &kvStore{s: s} }

// InTx implements store.Store: runs fn on the same store (no rollback).
func (s *Store) InTx(ctx context.Context, fn func(tx store.Store) error) error { return fn(s) }

// Ping implements store.Store.
func (s *Store) Ping(ctx context.Context) error { return nil }

// Close implements store.Store.
func (s *Store) Close() {}

// ------------------------------------------------------------ test helpers --

// PutAgent inserts or replaces an agent (test setup).
func (s *Store) PutAgent(a domain.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := cloneAgent(a)
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = s.now()
	}
	s.agents[a.ID] = &c
}

// UpdateAgent mutates an agent in place (test setup: simulate reports).
func (s *Store) UpdateAgent(id domain.AgentID, fn func(a *domain.Agent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		panic(fmt.Sprintf("memstore: agent %s does not exist", id))
	}
	fn(a)
}

// Agent returns a copy of an agent (test assertions).
func (s *Store) Agent(id domain.AgentID) *domain.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return nil
	}
	c := cloneAgent(*a)
	return &c
}

// PutEnrollmentToken registers a token for ListEnrollmentTokens.
func (s *Store) PutEnrollmentToken(t domain.EnrollmentToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, t)
}

// PutUser registers a user for GetUser (test setup).
func (s *Store) PutUser(u domain.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = map[uuid.UUID]domain.User{}
	}
	s.users[u.ID] = u
}

// SetSnapshot replaces the active snapshot without a sync record (test setup).
func (s *Store) SetSnapshot(snap domain.GitOpsSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(snap)
	if err != nil {
		panic(err)
	}
	s.snapJSON = raw
	s.snapshot.Commit = snap.Commit
}

// AuditEvents returns the recorded audit events.
func (s *Store) AuditEvents() []domain.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.AuditEvent(nil), s.audit...)
}

// ResetAudit forgets the recorded audit events (test setup).
func (s *Store) ResetAudit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = nil
}

// Syncs returns the sync history (oldest first).
func (s *Store) Syncs() []domain.GitOpsSync {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.GitOpsSync(nil), s.syncs...)
}

// Events returns the agent events of an agent (oldest first).
func (s *Store) Events(id domain.AgentID) []domain.AgentEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.AgentEvent(nil), s.events[id]...)
}

// Rendered returns the stored rendered config bodies by hash.
func (s *Store) Rendered() map[string]domain.RenderedConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]domain.RenderedConfig{}
	for k, v := range s.rendered {
		out[k] = v
	}
	return out
}

// ------------------------------------------------------------------ agents --

func cloneAgent(a domain.Agent) domain.Agent {
	c := a
	c.IdentifyingAttributes = cloneStrMap(a.IdentifyingAttributes)
	c.NonIdentifyingAttributes = cloneStrMap(a.NonIdentifyingAttributes)
	c.Labels = cloneStrMap(a.Labels)
	c.Assignment.AssignedConfigHash = append(domain.Hash(nil), a.Assignment.AssignedConfigHash...)
	c.Assignment.DesiredConfigHash = append(domain.Hash(nil), a.Assignment.DesiredConfigHash...)
	c.Assignment.FailedConfigHash = append(domain.Hash(nil), a.Assignment.FailedConfigHash...)
	c.RemoteConfig.LastHash = append(domain.Hash(nil), a.RemoteConfig.LastHash...)
	if a.Health != nil {
		h := *a.Health
		c.Health = &h
	}
	if a.PackageStatus != nil {
		ps := *a.PackageStatus
		ps.Packages = map[string]*domain.PackageStatus{}
		for k, v := range a.PackageStatus.Packages {
			pv := *v
			ps.Packages[k] = &pv
		}
		c.PackageStatus = &ps
	}
	if a.Updater != nil {
		u := *a.Updater
		c.Updater = &u
	}
	return c
}

func cloneStrMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

type agentStore struct {
	store.AgentStore
	s *Store
}

func (a *agentStore) Get(ctx context.Context, id domain.AgentID) (*domain.Agent, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	ag, ok := a.s.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent %s: %w", id, domain.ErrNotFound)
	}
	c := cloneAgent(*ag)
	return &c, nil
}

func (a *agentStore) GetMany(ctx context.Context, ids []domain.AgentID) ([]domain.Agent, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	out := []domain.Agent{}
	for _, id := range ids {
		if ag, ok := a.s.agents[id]; ok {
			out = append(out, cloneAgent(*ag))
		}
	}
	return out, nil
}

func matchFilter(ag *domain.Agent, f store.AgentFilter) bool {
	if len(f.IDs) > 0 {
		found := false
		for _, id := range f.IDs {
			if id == ag.ID {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(f.Kinds) > 0 {
		found := false
		for _, k := range f.Kinds {
			if k == ag.Kind {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(f.Status) > 0 {
		found := false
		for _, st := range f.Status {
			if st == ag.Connection.Status {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(f.Fleets) > 0 {
		found := false
		for _, fl := range f.Fleets {
			if fl == ag.Assignment.FleetName {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if f.Unassigned != nil && (*f.Unassigned != (ag.Assignment.FleetName == "")) {
		return false
	}
	if f.Healthy != nil && (ag.Health == nil || ag.Health.Healthy != *f.Healthy) {
		return false
	}
	if len(f.Versions) > 0 {
		found := false
		for _, v := range f.Versions {
			if v == ag.Version {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(f.OSTypes) > 0 {
		found := false
		for _, v := range f.OSTypes {
			if v == ag.OSType {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(f.ConfigSync) > 0 {
		found := false
		cs := ag.ConfigSyncState()
		for _, v := range f.ConfigSync {
			if v == cs {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (a *agentStore) ListAll(ctx context.Context, f store.AgentFilter) ([]domain.Agent, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	out := []domain.Agent{}
	for _, ag := range a.s.agents {
		if matchFilter(ag, f) {
			out = append(out, cloneAgent(*ag))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (a *agentStore) List(ctx context.Context, q store.AgentQuery) (store.AgentPage, error) {
	all, _ := a.ListAll(ctx, q.Filter)
	total := len(all)
	if q.Offset > len(all) {
		all = nil
	} else {
		all = all[q.Offset:]
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return store.AgentPage{Items: all, Total: total}, nil
}

func (a *agentStore) SetLabels(ctx context.Context, id domain.AgentID, labels map[string]string) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	ag, ok := a.s.agents[id]
	if !ok {
		return fmt.Errorf("agent %s: %w", id, domain.ErrNotFound)
	}
	ag.Labels = cloneStrMap(labels)
	ag.UpdatedAt = a.s.now()
	return nil
}

func (a *agentStore) SetAssignment(ctx context.Context, id domain.AgentID, u store.AgentAssignmentUpdate) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	ag, ok := a.s.agents[id]
	if !ok {
		return fmt.Errorf("agent %s: %w", id, domain.ErrNotFound)
	}
	at := u.AssignedAt
	ag.Assignment.FleetName = u.FleetName
	ag.Assignment.AssignedConfigHash = append(domain.Hash(nil), u.AssignedConfigHash...)
	ag.Assignment.TargetVersion = u.TargetVersion
	ag.Assignment.Error = u.Error
	ag.Assignment.AssignedAt = &at
	ag.UpdatedAt = a.s.now()
	return nil
}

func (a *agentStore) CompareAndSetAssignment(ctx context.Context, id domain.AgentID, prev domain.AgentAssignment, u store.AgentAssignmentUpdate) (bool, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	ag, ok := a.s.agents[id]
	if !ok {
		return false, fmt.Errorf("agent %s: %w", id, domain.ErrNotFound)
	}
	cur := ag.Assignment
	if cur.FleetName != prev.FleetName || !cur.AssignedConfigHash.Equal(prev.AssignedConfigHash) ||
		cur.TargetVersion != prev.TargetVersion || cur.Error != prev.Error {
		return false, nil
	}
	at := u.AssignedAt
	ag.Assignment.FleetName = u.FleetName
	ag.Assignment.AssignedConfigHash = append(domain.Hash(nil), u.AssignedConfigHash...)
	ag.Assignment.TargetVersion = u.TargetVersion
	ag.Assignment.Error = u.Error
	ag.Assignment.AssignedAt = &at
	ag.UpdatedAt = a.s.now()
	return true, nil
}

func (a *agentStore) SetFleetTargetVersion(ctx context.Context, fleet, version string) (int64, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	var n int64
	for _, ag := range a.s.agents {
		if ag.Kind == domain.AgentKindCollector && ag.Assignment.FleetName == fleet && ag.Assignment.TargetVersion != version {
			ag.Assignment.TargetVersion = version
			ag.UpdatedAt = a.s.now()
			n++
		}
	}
	return n, nil
}

func (a *agentStore) SetDesiredConfig(ctx context.Context, ids []domain.AgentID, hash domain.Hash) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	for _, id := range ids {
		ag, ok := a.s.agents[id]
		if !ok {
			continue
		}
		if !ag.Assignment.DesiredConfigHash.Equal(hash) {
			ag.Assignment.FailedConfigHash = nil
		}
		ag.Assignment.DesiredConfigHash = append(domain.Hash(nil), hash...)
		ag.UpdatedAt = a.s.now()
	}
	return nil
}

func (a *agentStore) SetDesiredConfigIfUnset(ctx context.Context, ids []domain.AgentID, hash domain.Hash) ([]domain.AgentID, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	changed := []domain.AgentID{}
	for _, id := range ids {
		ag, ok := a.s.agents[id]
		if !ok || !ag.Assignment.DesiredConfigHash.IsZero() {
			continue
		}
		ag.Assignment.DesiredConfigHash = append(domain.Hash(nil), hash...)
		ag.UpdatedAt = a.s.now()
		changed = append(changed, id)
	}
	return changed, nil
}

func (a *agentStore) SetFailedConfig(ctx context.Context, id domain.AgentID, hash domain.Hash) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if ag, ok := a.s.agents[id]; ok {
		ag.Assignment.FailedConfigHash = append(domain.Hash(nil), hash...)
	}
	return nil
}

func (a *agentStore) ClearFailedConfig(ctx context.Context, id domain.AgentID) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if ag, ok := a.s.agents[id]; ok {
		ag.Assignment.FailedConfigHash = nil
	}
	return nil
}

func (a *agentStore) AppendEvents(ctx context.Context, events []domain.AgentEvent) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	for _, e := range events {
		a.s.eventSeq++
		e.ID = a.s.eventSeq
		a.s.events[e.AgentID] = append(a.s.events[e.AgentID], e)
	}
	return nil
}

func (a *agentStore) ListEvents(ctx context.Context, id domain.AgentID, limit int, beforeID int64) ([]domain.AgentEvent, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	src := a.s.events[id]
	out := []domain.AgentEvent{}
	for i := len(src) - 1; i >= 0; i-- {
		if beforeID > 0 && src[i].ID >= beforeID {
			continue
		}
		out = append(out, src[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ----------------------------------------------------------------- configs --

type configStore struct {
	store.ConfigStore
	s *Store
}

func (c *configStore) PutRendered(ctx context.Context, rc domain.RenderedConfig) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	key := rc.Hash.String()
	if _, ok := c.s.rendered[key]; ok {
		return nil
	}
	rc.Body = append([]byte(nil), rc.Body...)
	c.s.rendered[key] = rc
	return nil
}

func (c *configStore) GetRendered(ctx context.Context, hash domain.Hash) (*domain.RenderedConfig, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	rc, ok := c.s.rendered[hash.String()]
	if !ok {
		return nil, fmt.Errorf("rendered config %s: %w", hash.Short(), domain.ErrNotFound)
	}
	return &rc, nil
}

func (c *configStore) PutComponentSet(ctx context.Context, set domain.ComponentSet) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.compSets[set.Hash.String()] = set
	return nil
}

func (c *configStore) GetComponentSet(ctx context.Context, hash domain.Hash) (*domain.ComponentSet, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	set, ok := c.s.compSets[hash.String()]
	if !ok {
		return nil, fmt.Errorf("component set: %w", domain.ErrNotFound)
	}
	return &set, nil
}

// ------------------------------------------------------------------ gitops --

type gitopsStore struct {
	store.GitOpsStore
	s *Store
}

func objectHashes(snap domain.GitOpsSnapshot) map[string]string {
	m := map[string]string{}
	for _, o := range snap.Fleets {
		m["Fleet/"+o.Name] = o.Source.ContentHash.String()
	}
	for _, o := range snap.Blueprints {
		m["Blueprint/"+o.Name] = o.Source.ContentHash.String()
	}
	for _, o := range snap.RolloutPolicies {
		m["RolloutPolicy/"+o.Name] = o.Source.ContentHash.String()
	}
	for _, o := range snap.Executors {
		m["Executor/"+o.Name] = o.Source.ContentHash.String()
	}
	for _, o := range snap.Inventories {
		m["Inventory/"+o.Name] = o.Source.ContentHash.String()
	}
	return m
}

func (g *gitopsStore) ApplySnapshot(ctx context.Context, snap domain.GitOpsSnapshot, sync domain.GitOpsSync) (store.ObjectChanges, error) {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	var old domain.GitOpsSnapshot
	if g.s.snapJSON != nil {
		_ = domain.UnmarshalJSONPreservingInts(g.s.snapJSON, &old)
	}
	oh, nh := objectHashes(old), objectHashes(snap)
	ch := store.ObjectChanges{Added: []string{}, Changed: []string{}, Removed: []string{}}
	for k, h := range nh {
		if prev, ok := oh[k]; !ok {
			ch.Added = append(ch.Added, k)
		} else if prev != h {
			ch.Changed = append(ch.Changed, k)
		}
	}
	for k := range oh {
		if _, ok := nh[k]; !ok {
			ch.Removed = append(ch.Removed, k)
		}
	}
	sort.Strings(ch.Added)
	sort.Strings(ch.Changed)
	sort.Strings(ch.Removed)
	raw, err := json.Marshal(snap)
	if err != nil {
		return ch, err
	}
	g.s.snapJSON = raw
	g.s.snapshot.Commit = snap.Commit
	g.s.syncSeq++
	sync.ID = g.s.syncSeq
	sync.Added, sync.Changed, sync.Removed = ch.Added, ch.Changed, ch.Removed
	sync.Result = "applied"
	g.s.syncs = append(g.s.syncs, sync)
	return ch, nil
}

func (g *gitopsStore) GetSnapshot(ctx context.Context) (*domain.GitOpsSnapshot, error) {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	if g.s.FailGetSnapshot != nil {
		return nil, g.s.FailGetSnapshot
	}
	out := domain.GitOpsSnapshot{
		Fleets: []domain.Fleet{}, Blueprints: []domain.Blueprint{}, RolloutPolicies: []domain.RolloutPolicy{},
		Executors: []domain.Executor{}, Inventories: []domain.Inventory{},
	}
	if g.s.snapJSON != nil {
		// Like the real store: objects are read back from JSON with
		// UnmarshalJSONPreservingInts.
		if err := domain.UnmarshalJSONPreservingInts(g.s.snapJSON, &out); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

func (g *gitopsStore) ListFleets(ctx context.Context) ([]domain.Fleet, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Fleets, nil
}

func (g *gitopsStore) GetFleet(ctx context.Context, name string) (*domain.Fleet, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == name {
			return &snap.Fleets[i], nil
		}
	}
	return nil, fmt.Errorf("fleet %q: %w", name, domain.ErrNotFound)
}

func (g *gitopsStore) ListBlueprints(ctx context.Context) ([]domain.Blueprint, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Blueprints, nil
}

func (g *gitopsStore) GetBlueprint(ctx context.Context, name string) (*domain.Blueprint, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for i := range snap.Blueprints {
		if snap.Blueprints[i].Name == name {
			return &snap.Blueprints[i], nil
		}
	}
	return nil, fmt.Errorf("blueprint %q: %w", name, domain.ErrNotFound)
}

func (g *gitopsStore) ListRolloutPolicies(ctx context.Context) ([]domain.RolloutPolicy, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.RolloutPolicies, nil
}

func (g *gitopsStore) GetRolloutPolicy(ctx context.Context, name string) (*domain.RolloutPolicy, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for i := range snap.RolloutPolicies {
		if snap.RolloutPolicies[i].Name == name {
			return &snap.RolloutPolicies[i], nil
		}
	}
	return nil, fmt.Errorf("rollout policy %q: %w", name, domain.ErrNotFound)
}

func (g *gitopsStore) ListExecutors(ctx context.Context) ([]domain.Executor, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Executors, nil
}

func (g *gitopsStore) GetExecutor(ctx context.Context, name string) (*domain.Executor, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for i := range snap.Executors {
		if snap.Executors[i].Name == name {
			return &snap.Executors[i], nil
		}
	}
	return nil, fmt.Errorf("executor %q: %w", name, domain.ErrNotFound)
}

func (g *gitopsStore) ListInventories(ctx context.Context) ([]domain.Inventory, error) {
	snap, err := g.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Inventories, nil
}

func (g *gitopsStore) RecordSync(ctx context.Context, sync domain.GitOpsSync) (int64, error) {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	g.s.syncSeq++
	sync.ID = g.s.syncSeq
	g.s.syncs = append(g.s.syncs, sync)
	return sync.ID, nil
}

func (g *gitopsStore) ListSyncs(ctx context.Context, limit, offset int) ([]domain.GitOpsSync, int, error) {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	all := make([]domain.GitOpsSync, len(g.s.syncs))
	for i, sy := range g.s.syncs {
		all[len(all)-1-i] = sy
	}
	total := len(all)
	if offset > len(all) {
		offset = len(all)
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, total, nil
}

func (g *gitopsStore) SaveStatus(ctx context.Context, st domain.GitOpsSyncStatus) error {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	raw, _ := json.Marshal(st)
	var c domain.GitOpsSyncStatus
	_ = json.Unmarshal(raw, &c)
	if c.Issues == nil {
		c.Issues = domain.Issues{}
	}
	if c.ObjectCounts == nil {
		c.ObjectCounts = map[domain.ObjectKind]int{}
	}
	g.s.status, g.s.statusSet = c, true
	return nil
}

func (g *gitopsStore) GetStatus(ctx context.Context) (*domain.GitOpsSyncStatus, error) {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	raw, _ := json.Marshal(g.s.status)
	var c domain.GitOpsSyncStatus
	_ = json.Unmarshal(raw, &c)
	if c.Issues == nil {
		c.Issues = domain.Issues{}
	}
	if c.ObjectCounts == nil {
		c.ObjectCounts = map[domain.ObjectKind]int{}
	}
	return &c, nil
}

// ---------------------------------------------------------------- packages --

type packageStore struct {
	store.PackageOfferStore
	s *Store
}

func (p *packageStore) SetOffers(ctx context.Context, id domain.AgentID, offers []domain.PackageOffer) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	p.s.offers[id] = append([]domain.PackageOffer(nil), offers...)
	return nil
}

func (p *packageStore) GetOffers(ctx context.Context, id domain.AgentID) ([]domain.PackageOffer, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	return append([]domain.PackageOffer{}, p.s.offers[id]...), nil
}

func (p *packageStore) ClearOffers(ctx context.Context, id domain.AgentID) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	delete(p.s.offers, id)
	return nil
}

// ---------------------------------------------------------------------- kv --

type kvStore struct {
	store.KVStore
	s *Store
}

func (k *kvStore) Get(ctx context.Context, key string) ([]byte, error) {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	v, ok := k.s.kv[key]
	if !ok {
		return nil, fmt.Errorf("kv %q: %w", key, domain.ErrNotFound)
	}
	return append([]byte(nil), v...), nil
}

func (k *kvStore) Put(ctx context.Context, key string, value []byte) error {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	k.s.kv[key] = append([]byte(nil), value...)
	return nil
}

func (k *kvStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	if _, ok := k.s.kv[key]; ok {
		return false, nil
	}
	k.s.kv[key] = append([]byte(nil), value...)
	return true, nil
}

func (k *kvStore) Delete(ctx context.Context, key string) error {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	delete(k.s.kv, key)
	return nil
}

// -------------------------------------------------------- identity / audit --

type identityStore struct {
	store.IdentityStore
	s *Store
}

// UpsertUser creates or updates a user by Subject (enough of the contract for tests).
func (i *identityStore) UpsertUser(ctx context.Context, u domain.User) (*domain.User, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	if i.s.users == nil {
		i.s.users = map[uuid.UUID]domain.User{}
	}
	for id, ex := range i.s.users {
		if ex.Subject == u.Subject {
			ex.Email, ex.Name = u.Email, u.Name
			i.s.users[id] = ex
			return &ex, nil
		}
	}
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = i.s.now()
	}
	i.s.users[u.ID] = u
	return &u, nil
}

// GetUser implements store.IdentityStore.
func (i *identityStore) GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	u, ok := i.s.users[id]
	if !ok {
		return nil, fmt.Errorf("user %s: %w", id, domain.ErrNotFound)
	}
	return &u, nil
}

// LockUser implements store.IdentityStore (no transactions here: GetUser).
func (i *identityStore) LockUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return i.GetUser(ctx, id)
}

// CreateAPIToken implements store.IdentityStore.
func (i *identityStore) CreateAPIToken(ctx context.Context, t domain.APIToken) error {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	i.s.apiTokens = append(i.s.apiTokens, t)
	return nil
}

// ListAPITokens implements store.IdentityStore.
func (i *identityStore) ListAPITokens(ctx context.Context) ([]domain.APIToken, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	return append([]domain.APIToken{}, i.s.apiTokens...), nil
}

func (i *identityStore) ListEnrollmentTokens(ctx context.Context) ([]domain.EnrollmentToken, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	return append([]domain.EnrollmentToken{}, i.s.tokens...), nil
}

type auditStore struct {
	store.AuditStore
	s *Store
}

func (a *auditStore) Append(ctx context.Context, e domain.AuditEvent) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	e.ID = int64(len(a.s.audit) + 1)
	a.s.audit = append(a.s.audit, e)
	return nil
}

// ---------------------------------------------------------------- releases --

type releaseStore struct {
	store.ReleaseStore
	s *Store
}

func relKey(dist, version string) string { return dist + "/" + version }

func (r *releaseStore) Upsert(ctx context.Context, rel domain.Release) (bool, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	k := relKey(rel.Distribution, rel.Version)
	prev, ok := r.s.releases[k]
	if ok {
		rel.DetectedAt, rel.AcknowledgedBy, rel.AcknowledgedAt = prev.DetectedAt, prev.AcknowledgedBy, prev.AcknowledgedAt
	}
	if rel.BreakingChanges == nil {
		rel.BreakingChanges = []domain.ChangelogEntry{}
	}
	if rel.Deprecations == nil {
		rel.Deprecations = []domain.ChangelogEntry{}
	}
	if rel.Assets == nil {
		rel.Assets = []domain.ReleaseAsset{}
	}
	r.s.releases[k] = rel
	return !ok, nil
}

func (r *releaseStore) Get(ctx context.Context, dist, version string) (*domain.Release, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	rel, ok := r.s.releases[relKey(dist, version)]
	if !ok {
		return nil, fmt.Errorf("release %s %s: %w", dist, version, domain.ErrNotFound)
	}
	return &rel, nil
}

func (r *releaseStore) sorted(f store.ReleaseFilter) []domain.Release {
	var out []domain.Release
	for _, rel := range r.s.releases {
		if f.Distribution != "" && rel.Distribution != f.Distribution {
			continue
		}
		if rel.Prerelease && !f.IncludePrerelease {
			continue
		}
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool { return semverLess(out[j].Version, out[i].Version) })
	return out
}

func (r *releaseStore) List(ctx context.Context, f store.ReleaseFilter) ([]domain.Release, int, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := r.sorted(f)
	if f.SinceVersion != "" {
		var keep []domain.Release
		for _, rel := range out {
			if semverLess(f.SinceVersion, rel.Version) {
				keep = append(keep, rel)
			}
		}
		out = keep
	}
	total := len(out)
	if f.Offset > len(out) {
		f.Offset = len(out)
	}
	out = out[f.Offset:]
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	if out == nil {
		out = []domain.Release{}
	}
	return out, total, nil
}

func (r *releaseStore) Latest(ctx context.Context, dist string, includePre bool) (*domain.Release, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := r.sorted(store.ReleaseFilter{Distribution: dist, IncludePrerelease: includePre})
	if len(out) == 0 {
		return nil, fmt.Errorf("release of %s: %w", dist, domain.ErrNotFound)
	}
	return &out[0], nil
}

func (r *releaseStore) Acknowledge(ctx context.Context, dist, version, by string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	k := relKey(dist, version)
	rel, ok := r.s.releases[k]
	if !ok {
		return fmt.Errorf("release %s %s: %w", dist, version, domain.ErrNotFound)
	}
	rel.AcknowledgedBy, rel.AcknowledgedAt = by, &at
	r.s.releases[k] = rel
	return nil
}

// SetReleaseDetectedAt is a test helper for Upsert-created releases.
func (s *Store) SetReleaseDetectedAt(dist, version string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := relKey(dist, version)
	rel := s.releases[k]
	rel.DetectedAt = at
	s.releases[k] = rel
}

// semverLess compares dotted numeric versions (test helper; the real store
// uses semantic versions).
func semverLess(a, b string) bool {
	pa, pb := splitVersion(a), splitVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func splitVersion(v string) [3]int {
	var out [3]int
	var cur, idx int
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			cur = cur*10 + int(c-'0')
		case c == '.':
			if idx < 3 {
				out[idx] = cur
			}
			idx++
			cur = 0
		default:
			if idx < 3 {
				out[idx] = cur
			}
			return out
		}
	}
	if idx < 3 {
		out[idx] = cur
	}
	return out
}

// --------------------------------------------------------------- artifacts --

type artifactStore struct {
	store.ArtifactStore
	s *Store
}

func (a *artifactStore) Upsert(ctx context.Context, art domain.Artifact) (*domain.Artifact, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	for id, ex := range a.s.artifacts {
		if ex.Distribution == art.Distribution && ex.Version == art.Version && ex.OS == art.OS && ex.Arch == art.Arch && ex.Format == art.Format {
			art.ID, art.CreatedAt = id, ex.CreatedAt
			art.UpdatedAt = a.s.now()
			a.s.artifacts[id] = art
			c := art
			return &c, nil
		}
	}
	if art.ID == uuid.Nil {
		art.ID = uuid.Must(uuid.NewV7())
	}
	if art.CreatedAt.IsZero() {
		art.CreatedAt = a.s.now()
	}
	art.UpdatedAt = a.s.now()
	a.s.artifacts[art.ID] = art
	c := art
	return &c, nil
}

func (a *artifactStore) Get(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	art, ok := a.s.artifacts[id]
	if !ok {
		return nil, fmt.Errorf("artifact %s: %w", id, domain.ErrNotFound)
	}
	return &art, nil
}

func (a *artifactStore) Find(ctx context.Context, dist, version, os, arch, format string) (*domain.Artifact, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	for _, ex := range a.s.artifacts {
		if ex.Distribution == dist && ex.Version == version && ex.OS == os && ex.Arch == arch && ex.Format == format {
			c := ex
			return &c, nil
		}
	}
	return nil, fmt.Errorf("artifact: %w", domain.ErrNotFound)
}

func (a *artifactStore) List(ctx context.Context, f store.ArtifactFilter) ([]domain.Artifact, int, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	out := []domain.Artifact{}
	for _, ex := range a.s.artifacts {
		if f.Distribution != "" && ex.Distribution != f.Distribution {
			continue
		}
		if f.Version != "" && ex.Version != f.Version {
			continue
		}
		if len(f.Status) > 0 {
			ok := false
			for _, st := range f.Status {
				if st == ex.Status {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, ex)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Filename < out[j].Filename })
	total := len(out)
	if f.Offset > len(out) {
		f.Offset = len(out)
	}
	out = out[f.Offset:]
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, total, nil
}

func (a *artifactStore) Delete(ctx context.Context, id uuid.UUID) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if _, ok := a.s.artifacts[id]; !ok {
		return fmt.Errorf("artifact %s: %w", id, domain.ErrNotFound)
	}
	delete(a.s.artifacts, id)
	return nil
}
