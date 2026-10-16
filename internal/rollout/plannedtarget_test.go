// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// The planner records, for each fleet, the effective target version that it has handled (store.PlannedTargetKey). The
// installation of hosts from the server waits for the record before it gives a new host the target: the target reaches the
// replicas before the leader has created the rollout for it, and a host that took the new version in between would skip
// the waves and approvals.

// planned returns the record of fleet "f" and whether there is one.
func (e *env) planned() (string, bool) {
	e.t.Helper()
	b, err := e.st.KV().Get(e.ctx, store.PlannedTargetKey("f"))
	if errors.Is(err, domain.ErrNotFound) {
		return "", false
	}
	require.NoError(e.t, err)
	return string(b), true
}

func TestPlannerRecordsTheGitTargetItHandled(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.162.0", nil)
	e.addAgents(3, "f", inSync())
	e.publishSnapshot()
	_, ok := e.planned()
	require.False(t, ok, "nothing is recorded before the planner has run")

	e.plan()
	require.Len(t, e.versionRollouts(), 1, "the members run 0.161.0")
	got, ok := e.planned()
	require.True(t, ok)
	assert.Equal(t, "0.162.0", got)

	// a rollout that is not needed is handled as well: the members run the target
	e2 := newEnv(t)
	e2.setPolicy("p", spec(allWave()))
	selFleet(e2, "0.161.0", nil)
	e2.addAgents(2, "f", inSync())
	e2.publishSnapshot()
	e2.plan()
	assert.Empty(t, e2.versionRollouts())
	got, ok = e2.planned()
	require.True(t, ok, "planned although nothing had to be done")
	assert.Equal(t, "0.161.0", got)
}

// The record follows the effective target, whatever its source: a selection in the UI, the target in Git after the
// selection was cleared, and a fleet that has none.
func TestPlannerRecordsTheEffectiveTargetOfEverySource(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.163.0"))
	e.addAgents(2, "f", inSync())
	e.publishSnapshot()
	e.plan()
	got, _ := e.planned()
	assert.Equal(t, "0.161.0", got, "the target in Git")

	e.putSelection("0.162.0", "0.161.0")
	e.plan()
	got, _ = e.planned()
	assert.Equal(t, "0.162.0", got, "the version chosen in the UI")
	require.NotNil(t, e.selection().PlannedAt, "and the selection carries its own mark")

	// the selection is cleared: the target falls back to Git, which is a new target for the planner too
	ok, err := e.st.FleetVersions().Delete(e.ctx, "f", nil)
	require.NoError(t, err)
	require.True(t, ok)
	e.plan()
	got, _ = e.planned()
	assert.Equal(t, "0.161.0", got)

	// a selection that became invalid puts the fleet on hold: there is no target, and the record says so
	e.putSelection("0.162.0", "0.161.0")
	e.plan()
	sel := e.selection()
	require.NotNil(t, sel)
	changed, err := e.st.FleetVersions().Invalidate(e.ctx, "f", "outside the frame", sel.UpdatedAt)
	require.NoError(t, err)
	require.True(t, changed)
	e.plan()
	got, ok2 := e.planned()
	require.True(t, ok2)
	assert.Equal(t, "", got, "a fleet on hold has no target")
}

// failingStore fails the creation of a rollout while fail is set.
type failingStore struct {
	store.Store
	fail *atomic.Bool
}

func (s failingStore) Rollouts() store.RolloutStore {
	return failingRollouts{s.Store.Rollouts(), s.fail}
}

type failingRollouts struct {
	store.RolloutStore
	fail *atomic.Bool
}

func (r failingRollouts) Create(ctx context.Context, ro *domain.Rollout, targets []domain.RolloutTarget) error {
	if r.fail.Load() && ro.Kind == domain.RolloutKindVersion {
		return errors.New("the database is down")
	}
	return r.RolloutStore.Create(ctx, ro, targets)
}

// A target whose rollout could not be created is not recorded: a host that asks waits instead of getting a version that
// the waves have not been planned for. The next run records it.
func TestPlannerDoesNotRecordATargetItCouldNotPlan(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.162.0", nil)
	e.addAgents(3, "f", inSync())
	e.publishSnapshot()

	fail := &atomic.Bool{}
	fail.Store(true)
	cfg := e.cfg
	eng := NewEngine(cfg, Deps{Store: failingStore{e.st, fail}, Bus: e.bus, Control: e.ctl, Executors: e.eng.deps.Executors, Notifier: e.notif,
		Audit: auditAdapter{e.st}, Logger: e.eng.log, Now: e.clk.Now})
	require.Error(t, eng.Plan(e.ctx), "the rollout cannot be created")
	_, ok := e.planned()
	assert.False(t, ok, "nothing is recorded for a target that was not planned")
	assert.Empty(t, e.versionRollouts())

	fail.Store(false)
	require.NoError(t, eng.Plan(e.ctx))
	require.Len(t, e.versionRollouts(), 1)
	got, ok := e.planned()
	require.True(t, ok)
	assert.Equal(t, "0.162.0", got, "recorded once the rollout exists")
}

// countingKV counts the writes, and fails them while fail is set.
type countingKV struct {
	store.KVStore
	puts *atomic.Int64
	fail *atomic.Bool
}

func (k countingKV) Put(ctx context.Context, key string, value []byte) error {
	if k.fail.Load() {
		return errors.New("the database is down")
	}
	k.puts.Add(1)
	return k.KVStore.Put(ctx, key, value)
}

type countingKVStore struct {
	store.Store
	kv store.KVStore
}

func (s countingKVStore) KV() store.KVStore { return s.kv }

// The record is written when it changes, not on every run, and a write that failed is tried again by the next run.
func TestPlannerWritesTheRecordWhenItChanges(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.161.0", rangeFrame(">=0.161.0, <=0.163.0"))
	e.addAgents(2, "f", inSync())
	e.publishSnapshot()

	puts, fail := &atomic.Int64{}, &atomic.Bool{}
	eng := NewEngine(e.cfg, Deps{Store: countingKVStore{e.st, countingKV{e.st.KV(), puts, fail}}, Bus: e.bus, Control: e.ctl,
		Executors: e.eng.deps.Executors, Notifier: e.notif, Audit: auditAdapter{e.st}, Logger: e.eng.log, Now: e.clk.Now})

	fail.Store(true)
	require.NoError(t, eng.Plan(e.ctx), "a record that cannot be written does not fail the planning")
	_, ok := e.planned()
	assert.False(t, ok)
	fail.Store(false)

	require.NoError(t, eng.Plan(e.ctx))
	require.NoError(t, eng.Plan(e.ctx))
	require.NoError(t, eng.Plan(e.ctx))
	assert.EqualValues(t, 1, puts.Load(), "one write for three runs")
	got, _ := e.planned()
	assert.Equal(t, "0.161.0", got)

	e.putSelection("0.162.0", "0.161.0")
	require.NoError(t, eng.Plan(e.ctx))
	require.NoError(t, eng.Plan(e.ctx))
	assert.EqualValues(t, 2, puts.Load(), "and one more for the new target")
	got, _ = e.planned()
	assert.Equal(t, "0.162.0", got)
}

// Another leader may have written the record while this engine was not the leader. The cache of what this engine wrote
// is dropped when it becomes the leader again (Run), so that the first run writes the record of every fleet.
func TestRunWritesTheRecordAgainWhenTheEngineLeadsAgain(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	selFleet(e, "0.162.0", nil)
	e.addAgents(2, "f", inSync())
	e.publishSnapshot()
	e.plan()
	got, _ := e.planned()
	require.Equal(t, "0.162.0", got)

	// another leader recorded something else in the meantime
	require.NoError(t, e.st.KV().Put(e.ctx, store.PlannedTargetKey("f"), []byte("0.150.0")))
	e.plan()
	got, _ = e.planned()
	assert.Equal(t, "0.150.0", got, "within a term the engine trusts what it wrote and does not write it again")

	ctx, cancel := context.WithCancel(e.ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = e.eng.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		got, _ := e.planned()
		return got == "0.162.0"
	}, 20*time.Second, 10*time.Millisecond, "the first run of a term writes the record again")
	cancel()
	wg.Wait()
}
