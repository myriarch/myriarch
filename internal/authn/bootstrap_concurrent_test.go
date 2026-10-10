// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// Several replicas reconcile the bootstrap admin when they start together.
// The decision "the account is disabled / enabled" is taken inside the
// transaction, on the locked row, so of N starts one does the work and writes
// the audit entry, and none revokes what was issued after it.

// staleStore shows the replica a snapshot of the bootstrap users in which
// Disabled has the given value, whatever the database says: the state another
// replica has changed since this one listed the users. Transactions see the
// real state.
type staleStore struct {
	store.Store
	disabled bool
}

func (s staleStore) Identity() store.IdentityStore {
	return staleIdentity{s.Store.Identity(), s.disabled}
}

type staleIdentity struct {
	store.IdentityStore
	disabled bool
}

func (s staleIdentity) ListUsers(ctx context.Context) ([]domain.User, error) {
	users, err := s.IdentityStore.ListUsers(ctx)
	for i := range users {
		users[i].Disabled = s.disabled
	}
	return users, err
}

func (s staleIdentity) UpsertUser(ctx context.Context, u domain.User) (*domain.User, error) {
	out, err := s.IdentityStore.UpsertUser(ctx, u)
	if out != nil {
		out.Disabled = s.disabled
	}
	return out, err
}

func staleService(t *testing.T, e *env, auth config.AuthConfig, disabled bool) *authn.Service {
	t.Helper()
	svc, err := authn.New(context.Background(), auth, e.serverCfg, e.opampCfg, authn.Deps{
		Store: staleStore{e.db, disabled}, Audit: audit.NewRecorder(e.db.Audit(), quiet), Registerer: prometheus.NewRegistry(), Logger: quiet,
		HTTPClient: e.httpCli, Now: e.clock.Now, Bus: e.bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// A replica whose snapshot says "disabled" although another replica has
// enabled the account since: the credentials issued after that stay valid and
// the start writes no activation.
func TestBootstrapStaleDisabledSnapshotDoesNotRevokeNewCredentials(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	acc := e.bootstrapAccountWithToken(t, f) // active, with a session and a token
	before := len(e.findAudit("auth.bootstrap.activate"))

	if err := staleService(t, e, e.authCfg, true).EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.tokenRevoked(acc.secret) || !e.sessionExists(acc.session) || e.userDisabled(acc.user.ID) {
		t.Fatal("an account that is enabled already must keep the credentials issued after it was enabled")
	}
	if got := len(e.findAudit("auth.bootstrap.activate")); got != before {
		t.Fatalf("phantom activation entries: %d", got-before)
	}
}

// A replica whose snapshot says "enabled" although another replica has
// deactivated the account since: nothing is left to do and nothing is written.
func TestBootstrapStaleEnabledSnapshotWritesNoPhantomDeactivation(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	ctx := context.Background()
	acc := e.bootstrapAccountWithToken(t, f)
	off := bootstrapAuthWith(e.authCfg, func(c *config.AuthConfig) { c.BootstrapAdmin.Enabled = false })
	if err := newService(t, e, off, nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(e.findAudit("auth.bootstrap.deactivate")); got != 1 {
		t.Fatalf("precondition: %d deactivation entries", got)
	}

	if err := staleService(t, e, off, false).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(e.findAudit("auth.bootstrap.deactivate")); got != 1 {
		t.Fatalf("phantom deactivation entries: %d", got-1)
	}
	if !e.userDisabled(acc.user.ID) {
		t.Fatal("the account must stay disabled")
	}
}

// gateTimeout bounds the wait at a gate: a replica that never arrives (it failed
// before) fails the others with a message instead of hanging the test.
const gateTimeout = 30 * time.Second

// gate lets callers through together: arrive blocks until n callers have arrived.
type gate struct {
	n    int
	mu   sync.Mutex
	in   int
	open chan struct{}
}

func newGate(n int) *gate { return &gate{n: n, open: make(chan struct{})} }

func (g *gate) arrive(ctx context.Context) error {
	g.mu.Lock()
	g.in++
	if g.in == g.n {
		close(g.open)
	}
	g.mu.Unlock()
	timer := time.NewTimer(gateTimeout)
	defer timer.Stop()
	select {
	case <-g.open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		g.mu.Lock()
		defer g.mu.Unlock()
		return fmt.Errorf("overlap gate: %d of %d callers arrived within %s", g.in, g.n, gateTimeout)
	}
}

// overlapStore makes the replicas overlap on purpose. Left alone they overlap by
// chance: the first one is often done before the last one has connected, and a
// decision taken on a stale snapshot shows up only on a slow run (-race). InTx
// admits a replica in two steps:
//   - before it opens its transaction, once every replica has taken its snapshot
//     of the account (each stands at InTx after the reads of EnsureBootstrapAdmin),
//     so every decision rests on a snapshot that the first commit makes stale;
//   - before its first statement, once every transaction is open, so only the lock
//     on the user row keeps the replicas from doing the work together.
//
// Nothing is held at the first gate. The second one holds a connection per waiting
// replica, so it admits no more replicas than the pool has connections.
type overlapStore struct {
	store.Store
	snapshots, txs *gate
	once           sync.Once
}

func (s *overlapStore) InTx(ctx context.Context, fn func(tx store.Store) error) error {
	var err error
	s.once.Do(func() { err = s.snapshots.arrive(ctx) }) // a replica counts once, whatever it does afterwards
	if err != nil {
		return err
	}
	return s.Store.InTx(ctx, func(tx store.Store) error {
		if err := s.txs.arrive(ctx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// overlapService creates another authn service on the database of e whose
// transactions pass the gates.
func overlapService(t *testing.T, e *env, auth config.AuthConfig, snapshots, txs *gate) *authn.Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc, err := authn.New(ctx, auth, e.serverCfg, e.opampCfg, authn.Deps{
		Store: &overlapStore{Store: e.db, snapshots: snapshots, txs: txs}, Audit: audit.NewRecorder(e.db.Audit(), quiet),
		Registerer: prometheus.NewRegistry(), Logger: quiet, HTTPClient: e.httpCli, Now: e.clock.Now, Bus: e.bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// N services start at once on one database. They are lined up (overlapStore): all
// of them decide on a snapshot of the account that is stale by the time the first
// transaction commits, and all transactions are open before the first statement
// runs. Of N starts one does the work and writes the audit entry, whatever the
// timing; the others find the account changed when they lock its row.
func TestBootstrapConcurrentStartsAuditOnlyOnce(t *testing.T) {
	const replicas = 8
	run := func(t *testing.T, e *env, auth config.AuthConfig) {
		t.Helper()
		snapshots := newGate(replicas)
		txs := newGate(min(replicas, int(e.db.Pool().Config().MaxConns)))
		svcs := make([]*authn.Service, replicas)
		for i := range svcs {
			svcs[i] = overlapService(t, e, auth, snapshots, txs)
		}
		var wg sync.WaitGroup
		errs := make([]error, replicas)
		start := make(chan struct{})
		for i := range svcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = svcs[i].EnsureBootstrapAdmin(context.Background())
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("replica %d: %v", i, err) // all of them: the first one may be a gate timeout caused by another
			}
		}
		if t.Failed() {
			t.FailNow()
		}
	}

	t.Run("enable", func(t *testing.T) {
		f := newBootstrapFiles(t)
		e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
		acc := e.bootstrapAccountWithToken(t, f)
		if err := e.db.Identity().SetUserDisabled(context.Background(), acc.user.ID, true); err != nil { // an older build
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		run(t, e, e.authCfg)
		if e.userDisabled(acc.user.ID) || !e.tokenRevoked(acc.secret) || e.sessionExists(acc.session) {
			t.Fatal("the account must be enabled without the credentials of its earlier life")
		}
		if got := len(e.findAudit("auth.bootstrap.activate")); got != 2 { // creation + one re-enable
			t.Fatalf("activation entries: %d, want 2", got)
		}
	})
	t.Run("disable", func(t *testing.T) {
		f := newBootstrapFiles(t)
		e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
		acc := e.bootstrapAccountWithToken(t, f)
		off := bootstrapAuthWith(e.authCfg, func(c *config.AuthConfig) { c.BootstrapAdmin.Enabled = false })
		run(t, e, off)
		if !e.userDisabled(acc.user.ID) || !e.tokenRevoked(acc.secret) || e.sessionExists(acc.session) {
			t.Fatal("the account must be disabled without credentials")
		}
		if got := len(e.findAudit("auth.bootstrap.deactivate")); got != 1 {
			t.Fatalf("deactivation entries: %d, want 1", got)
		}
	})
}
