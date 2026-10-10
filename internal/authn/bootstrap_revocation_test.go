// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// The revocation of the credentials of the bootstrap admin converges across
// restarts: the steps of an account run in one transaction, an account that is
// disabled already is checked for leftovers, and the credentials of an
// account that comes back are revoked before it is enabled.

// bootstrapAccount is an active bootstrap admin that signed in with its
// password and created an API token.
type bootstrapAccount struct {
	user    domain.User
	client  *client // holds the session cookie
	session string  // id of the session in the database
	secret  string  // the API token
}

func (e *env) bootstrapAccountWithToken(t *testing.T, f *bootstrapFiles) bootstrapAccount {
	t.Helper()
	if err := e.svc.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := e.newClient()
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("sign-in: %d %s", r.Code, r.Body)
	}
	var created dto.CreatedAPIToken
	r := c.req("POST", "/api/v1/tokens", map[string]any{"name": "automation", "role": "admin"}, true)
	r.JSON(&created)
	if r.Code != 201 || created.Secret == "" {
		t.Fatalf("api token: %d %s", r.Code, r.Body)
	}
	users := e.bootstrapUsers()
	if len(users) != 1 {
		t.Fatalf("bootstrap users: %+v", users)
	}
	return bootstrapAccount{user: users[0], client: c, session: sha256Hex(c.cookie(authn.SessionCookie)), secret: created.Secret}
}

// tokenRevoked reports whether the API token with this secret is revoked.
func (e *env) tokenRevoked(secret string) bool {
	e.t.Helper()
	tok, err := e.db.Identity().GetAPITokenByHash(context.Background(), domain.SHA256([]byte(secret)))
	if err != nil {
		e.t.Fatal(err)
	}
	return tok.RevokedAt != nil
}

func (e *env) sessionExists(id string) bool {
	e.t.Helper()
	_, err := e.db.Identity().GetSession(context.Background(), id)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		e.t.Fatal(err)
	}
	return err == nil
}

func (e *env) userDisabled(id uuid.UUID) bool {
	e.t.Helper()
	u, err := e.db.Identity().GetUser(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.Disabled
}

func detailString(ev domain.AuditEvent, key string) string { return fmt.Sprint(ev.Details[key]) }

// An account that an older build disabled without revoking its credentials
// loses them at the next start, also while the configuration keeps the account
// inactive, and they stay rejected when the account comes back.
func TestBootstrapLeftoversOfADisabledAccountAreRemovedAtTheNextStart(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	ctx := context.Background()
	acc := e.bootstrapAccountWithToken(t, f)

	// the older build only set the flag
	if err := e.db.Identity().SetUserDisabled(ctx, acc.user.ID, true); err != nil {
		t.Fatal(err)
	}
	if e.tokenRevoked(acc.secret) || !e.sessionExists(acc.session) {
		t.Fatal("precondition: the credentials of the disabled account are still there")
	}

	// the configuration keeps the account inactive
	off := bootstrapAuthWith(e.authCfg, func(c *config.AuthConfig) { c.BootstrapAdmin.Enabled = false })
	if err := newService(t, e, off, nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.tokenRevoked(acc.secret) || e.sessionExists(acc.session) {
		t.Fatal("the leftovers of the disabled account must be removed")
	}
	deact := e.findAudit("auth.bootstrap.deactivate")
	if len(deact) != 1 {
		t.Fatalf("deactivate audit: %+v", deact)
	}
	d := deact[0]
	if d.Details["alreadyDisabled"] != true || d.Details["reason"] != "disabled_in_config" || detailString(d, "apiTokensRevoked") != "1" || detailString(d, "sessionsRemoved") != "1" ||
		d.ActorType != domain.ActorSystem || d.TargetID != acc.user.ID.String() {
		t.Fatalf("deactivate audit: %+v", d)
	}

	// converged: further starts write nothing
	entries := len(e.auditActions())
	for i := 0; i < 2; i++ {
		if err := newService(t, e, off, nil).EnsureBootstrapAdmin(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(e.auditActions()); got != entries {
		t.Fatalf("a start that finds nothing to remove wrote %d audit entries", got-entries)
	}

	// the account comes back: nothing of its earlier life does
	e.clock.Advance(time.Minute)
	back := newService(t, e, e.authCfg, nil)
	if err := back.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	act := e.findAudit("auth.bootstrap.activate")
	if len(act) != 2 || act[0].Details["reason"] != "enabled_in_config" || detailString(act[0], "apiTokensRevoked") != "0" || detailString(act[0], "sessionsRemoved") != "0" {
		t.Fatalf("activate audit: %+v", act)
	}
	if _, err := authenticate(back, nil, acc.secret); err == nil {
		t.Fatal("the API token of the earlier life must stay rejected")
	}
	if _, err := authenticate(back, acc.client, ""); err == nil {
		t.Fatal("the session of the earlier life must stay rejected")
	}
	if r := e.newClient().passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("the account itself works again: %d %s", r.Code, r.Body)
	}
}

// The credentials of an account that an older build disabled are revoked
// before the same start enables the account again, and the audit entry counts
// them.
func TestBootstrapCredentialsAreRevokedWhenADisabledAccountIsEnabled(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	ctx := context.Background()
	acc := e.bootstrapAccountWithToken(t, f)
	if err := e.db.Identity().SetUserDisabled(ctx, acc.user.ID, true); err != nil {
		t.Fatal(err)
	}

	e.clock.Advance(time.Minute)
	back := newService(t, e, e.authCfg, nil)
	if err := back.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if e.userDisabled(acc.user.ID) {
		t.Fatal("the account must be enabled")
	}
	if !e.tokenRevoked(acc.secret) || e.sessionExists(acc.session) {
		t.Fatal("the credentials of the earlier life must be gone")
	}
	act := e.findAudit("auth.bootstrap.activate")
	if len(act) != 2 || act[0].Details["reason"] != "enabled_in_config" || detailString(act[0], "apiTokensRevoked") != "1" || detailString(act[0], "sessionsRemoved") != "1" {
		t.Fatalf("activate audit: %+v", act)
	}
	if got := e.findAudit("auth.bootstrap.deactivate"); len(got) != 0 {
		t.Fatalf("an account that is enabled again is not deactivated: %+v", got)
	}
	if _, err := authenticate(back, nil, acc.secret); err == nil {
		t.Fatal("the API token must stay rejected")
	}
	if _, err := authenticate(back, acc.client, ""); err == nil {
		t.Fatal("the session must stay rejected")
	}
	if r := e.newClient().passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("sign-in after the account is enabled: %d %s", r.Code, r.Body)
	}
}

// faultyStore fails one step of the reconciliation, also inside transactions:
// "revoke" (RevokeAPITokensByCreator), "disable" (SetUserDisabled to true) or
// "enable" (SetUserDisabled to false).
type faultyStore struct {
	store.Store
	failOn string
}

var errInjected = errors.New("injected failure")

func (f faultyStore) Identity() store.IdentityStore {
	return faultyIdentity{f.Store.Identity(), f.failOn}
}

func (f faultyStore) InTx(ctx context.Context, fn func(tx store.Store) error) error {
	return f.Store.InTx(ctx, func(tx store.Store) error { return fn(faultyStore{tx, f.failOn}) })
}

type faultyIdentity struct {
	store.IdentityStore
	failOn string
}

func (f faultyIdentity) RevokeAPITokensByCreator(ctx context.Context, id uuid.UUID, at time.Time) ([]uuid.UUID, error) {
	if f.failOn == "revoke" {
		return nil, errInjected
	}
	return f.IdentityStore.RevokeAPITokensByCreator(ctx, id, at)
}

func (f faultyIdentity) SetUserDisabled(ctx context.Context, id uuid.UUID, disabled bool) error {
	if (disabled && f.failOn == "disable") || (!disabled && f.failOn == "enable") {
		return errInjected
	}
	return f.IdentityStore.SetUserDisabled(ctx, id, disabled)
}

// A step that fails leaves no half-finished account behind, whichever step it
// is: the account is never disabled with credentials left, and never enabled
// with the credentials of an earlier life. The next start finishes the job.
func TestBootstrapReconciliationIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	failing := func(t *testing.T, e *env, auth config.AuthConfig, failOn string) *authn.Service {
		t.Helper()
		svc, err := authn.New(ctx, auth, e.serverCfg, e.opampCfg, authn.Deps{
			Store: faultyStore{e.db, failOn}, Audit: audit.NewRecorder(e.db.Audit(), quiet), Registerer: prometheus.NewRegistry(), Logger: quiet,
			HTTPClient: e.httpCli, Now: e.clock.Now, Bus: e.bus,
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}

	// Deactivation: the tokens are revoked and the sessions removed first, the account is disabled last. A failure
	// at the last step must roll the first two back (a failure at the first step changes nothing anyway).
	for _, failOn := range []string{"revoke", "disable"} {
		t.Run("deactivation fails at "+failOn, func(t *testing.T) {
			f := newBootstrapFiles(t)
			e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
			acc := e.bootstrapAccountWithToken(t, f)
			off := bootstrapAuthWith(e.authCfg, func(c *config.AuthConfig) { c.BootstrapAdmin.Enabled = false })

			if err := failing(t, e, off, failOn).EnsureBootstrapAdmin(ctx); !errors.Is(err, errInjected) {
				t.Fatalf("EnsureBootstrapAdmin: %v", err)
			}
			if e.userDisabled(acc.user.ID) || e.tokenRevoked(acc.secret) || !e.sessionExists(acc.session) {
				t.Fatal("a failed deactivation must change nothing")
			}
			if len(e.findAudit("auth.bootstrap.deactivate")) != 0 {
				t.Fatal("a failed deactivation is not audited")
			}
			// the next start finishes it
			if err := newService(t, e, off, nil).EnsureBootstrapAdmin(ctx); err != nil {
				t.Fatal(err)
			}
			if !e.userDisabled(acc.user.ID) || !e.tokenRevoked(acc.secret) || e.sessionExists(acc.session) {
				t.Fatal("the next start must deactivate the account completely")
			}
		})
	}

	// Activation: the tokens are revoked and the sessions removed first, the account is enabled last. A failure
	// at either end leaves the account disabled with its credentials as they were.
	for _, failOn := range []string{"revoke", "enable"} {
		t.Run("activation fails at "+failOn, func(t *testing.T) {
			f := newBootstrapFiles(t)
			e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
			acc := e.bootstrapAccountWithToken(t, f)
			if err := e.db.Identity().SetUserDisabled(ctx, acc.user.ID, true); err != nil { // an older build
				t.Fatal(err)
			}

			if err := failing(t, e, e.authCfg, failOn).EnsureBootstrapAdmin(ctx); !errors.Is(err, errInjected) {
				t.Fatalf("EnsureBootstrapAdmin: %v", err)
			}
			if !e.userDisabled(acc.user.ID) {
				t.Fatal("the account must not be enabled while the credentials of its earlier life cannot be revoked")
			}
			if e.tokenRevoked(acc.secret) || !e.sessionExists(acc.session) {
				t.Fatal("a failed activation must change nothing")
			}
			if len(e.findAudit("auth.bootstrap.activate")) != 1 { // the creation only
				t.Fatal("a failed activation is not audited")
			}
			if err := newService(t, e, e.authCfg, nil).EnsureBootstrapAdmin(ctx); err != nil {
				t.Fatal(err)
			}
			if e.userDisabled(acc.user.ID) || !e.tokenRevoked(acc.secret) || e.sessionExists(acc.session) {
				t.Fatal("the next start must enable the account and remove what it held")
			}
		})
	}
}
