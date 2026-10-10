// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// REST-3: the application pool bounds every statement; maintenance statements raise the limit per transaction.
func TestStatementTimeout(t *testing.T) {
	ctx := ctxT(t)
	base := pgtest.NewPool(t).Config().ConnConfig.ConnString()
	pool, err := postgres.Connect(ctx, config.DatabaseConfig{DSN: base, MaxConns: 5, StatementTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var shown string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&shown); err != nil || shown != "300ms" {
		t.Fatalf("statement_timeout = %q (%v)", shown, err)
	}
	_, err = pool.Exec(ctx, `SELECT pg_sleep(5)`)
	if err == nil || !strings.Contains(err.Error(), "statement timeout") {
		t.Fatalf("a long statement must be cancelled by the server: %v", err)
	}
	// an explicit setting in the DSN wins
	pool2, err := postgres.Connect(ctx, config.DatabaseConfig{DSN: base + "&statement_timeout=45000", MaxConns: 5, StatementTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	if err := pool2.QueryRow(ctx, `SHOW statement_timeout`).Scan(&shown); err != nil || shown != "45s" {
		t.Fatalf("DSN setting: %q (%v)", shown, err)
	}

	// retention deletes run with a raised limit
	db := postgres.New(pool, nil)
	if _, err := db.Audit().DeleteBefore(ctx, time.Now()); err != nil {
		t.Fatalf("maintenance statement: %v", err)
	}
	// migrations are not subject to the application limit
	if _, err := postgres.Migrate(ctx, pool, nil); err != nil {
		t.Fatalf("migrate with a statement timeout configured: %v", err)
	}
}

func TestKVDeleteOlderThan(t *testing.T) {
	ctx := ctxT(t)
	db := newDB(t)
	kv := db.KV()
	mustNoErr(t, kv.Put(ctx, "authn/oidc-used/a", []byte("1")))
	mustNoErr(t, kv.Put(ctx, "authn/oidc-used/b", []byte("1")))
	mustNoErr(t, kv.Put(ctx, "other/key", []byte("1")))
	// nothing is older than an hour ago
	n, err := kv.DeleteOlderThan(ctx, store.KVPrefixOIDCUsed, time.Now().Add(-time.Hour))
	mustNoErr(t, err)
	if n != 0 {
		t.Fatalf("deleted %d fresh keys", n)
	}
	n, err = kv.DeleteOlderThan(ctx, store.KVPrefixOIDCUsed, time.Now().Add(time.Minute))
	mustNoErr(t, err)
	if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	if _, err := kv.Get(ctx, "other/key"); err != nil {
		t.Fatalf("keys with another prefix must survive: %v", err)
	}
	// a prefix is a prefix, not a pattern
	mustNoErr(t, kv.Put(ctx, "x%y", []byte("1")))
	mustNoErr(t, kv.Put(ctx, "xzzy", []byte("1")))
	if n, _ := kv.DeleteOlderThan(ctx, "x%", time.Now().Add(time.Minute)); n != 1 {
		t.Fatalf("literal prefix: %d", n)
	}
}

// AC-04 / AC-05: the credential binding is set once and only changes explicitly.
func TestCredentialBindingIsSetOnce(t *testing.T) {
	ctx := ctxT(t)
	db := newDB(t)
	id := newID()
	a, b := uuid.New(), uuid.New()
	up := func(tok *uuid.UUID, cert string) *domain.Agent {
		t.Helper()
		_, _, err := db.Agents().UpsertDescription(ctx, id, store.AgentDescriptionUpdate{Kind: domain.AgentKindCollector, Hostname: "h", Version: "1.0.0",
			EnrollmentTokenID: tok, ClientCertSubject: cert})
		mustNoErr(t, err)
		ag, err := db.Agents().Get(ctx, id)
		mustNoErr(t, err)
		return ag
	}
	if ag := up(nil, ""); ag.EnrollmentTokenID != nil || ag.ClientCertSubject != "" {
		t.Fatalf("no credential: %+v", ag)
	}
	if ag := up(&a, "edge-1"); ag.EnrollmentTokenID == nil || *ag.EnrollmentTokenID != a || ag.ClientCertSubject != "edge-1" {
		t.Fatalf("first credential binds a legacy agent: %+v", ag)
	}
	if ag := up(&b, "edge-2"); *ag.EnrollmentTokenID != a || ag.ClientCertSubject != "edge-1" {
		t.Fatalf("another credential must not overwrite the binding: %+v", ag)
	}
	mustNoErr(t, db.Agents().SetCredentialBinding(ctx, id, &b, ""))
	if ag, _ := db.Agents().Get(ctx, id); *ag.EnrollmentTokenID != b || ag.ClientCertSubject != "" {
		t.Fatalf("explicit rebind: %+v", ag)
	}
	mustNoErr(t, db.Agents().SetCredentialBinding(ctx, id, nil, ""))
	if ag, _ := db.Agents().Get(ctx, id); ag.EnrollmentTokenID != nil {
		t.Fatalf("cleared: %+v", ag)
	}
	if err := db.Agents().SetCredentialBinding(ctx, newID(), nil, ""); err == nil {
		t.Fatal("unknown agent")
	}
}

// AC-06: updater links only resolve between agents of the same credential and host.
func TestUpdaterLinkRequiresTheSameCredential(t *testing.T) {
	ctx := ctxT(t)
	db := newDB(t)
	ag := db.Agents()
	tokA, tokB := uuid.New(), uuid.New()
	mk := func(kind domain.AgentKind, host string, tok *uuid.UUID) domain.AgentID {
		id := newID()
		_, _, err := ag.UpsertDescription(ctx, id, store.AgentDescriptionUpdate{Kind: kind, Hostname: host, Version: "1.0.0", EnrollmentTokenID: tok})
		mustNoErr(t, err)
		return id
	}
	collector := mk(domain.AgentKindCollector, "host1", &tokA)
	good := mk(domain.AgentKindUpdater, "HOST1", &tokA) // host names compare case-insensitively
	foreignToken := mk(domain.AgentKindUpdater, "host1", &tokB)
	foreignHost := mk(domain.AgentKindUpdater, "host2", &tokA)
	for _, u := range []domain.AgentID{good, foreignToken, foreignHost} {
		mustNoErr(t, ag.SetUpdaterLink(ctx, u, collector))
	}
	c, err := ag.Get(ctx, collector)
	mustNoErr(t, err)
	if c.Updater == nil || c.Updater.AgentID != good {
		t.Fatalf("only the updater with the same token and host resolves: %+v", c.Updater)
	}
	// without any matching updater there is no link at all
	solo := mk(domain.AgentKindCollector, "host9", &tokA)
	mustNoErr(t, ag.SetUpdaterLink(ctx, foreignToken, solo))
	if s, _ := ag.Get(ctx, solo); s.Updater != nil {
		t.Fatalf("link of a foreign credential: %+v", s.Updater)
	}
	// certificate based setups (no token): the subjects must match
	cert1 := newID()
	_, _, err = ag.UpsertDescription(ctx, cert1, store.AgentDescriptionUpdate{Kind: domain.AgentKindCollector, Hostname: "h", Version: "1", ClientCertSubject: "cn-1"})
	mustNoErr(t, err)
	u1, u2 := newID(), newID()
	_, _, err = ag.UpsertDescription(ctx, u1, store.AgentDescriptionUpdate{Kind: domain.AgentKindUpdater, Hostname: "h", Version: "1", ClientCertSubject: "cn-1"})
	mustNoErr(t, err)
	_, _, err = ag.UpsertDescription(ctx, u2, store.AgentDescriptionUpdate{Kind: domain.AgentKindUpdater, Hostname: "h", Version: "1", ClientCertSubject: "cn-2"})
	mustNoErr(t, err)
	mustNoErr(t, ag.SetUpdaterLink(ctx, u1, cert1))
	mustNoErr(t, ag.SetUpdaterLink(ctx, u2, cert1))
	if c, _ := ag.Get(ctx, cert1); c.Updater == nil || c.Updater.AgentID != u1 {
		t.Fatalf("certificate subject match: %+v", c.Updater)
	}
}

// AUTH-SESS-2 / AUTH-SESS-4: the absolute cap and the removal of sessions by user.
func TestSessionMaxAgeAndDeleteByUser(t *testing.T) {
	ctx := ctxT(t)
	db := newDB(t)
	is := db.Identity()
	u, err := is.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "local:x", Provider: "local", Role: domain.RoleViewer})
	mustNoErr(t, err)
	other, err := is.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "local:y", Provider: "local", Role: domain.RoleViewer})
	mustNoErr(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"s1", "s2"} {
		mustNoErr(t, is.CreateSession(ctx, domain.Session{ID: id, UserID: u.ID, CSRFToken: "c", CreatedAt: now, ExpiresAt: now.Add(2 * time.Hour)}))
	}
	mustNoErr(t, is.CreateSession(ctx, domain.Session{ID: "s3", UserID: other.ID, CSRFToken: "c", CreatedAt: now, ExpiresAt: now.Add(2 * time.Hour)}))

	// activity after 90 minutes: the sliding expiry would move to +3h30, the cap is created_at + 3h
	mustNoErr(t, is.TouchSession(ctx, "s1", now.Add(90*time.Minute), 3*time.Hour))
	s1, _ := is.GetSession(ctx, "s1")
	if !s1.ExpiresAt.Equal(now.Add(3 * time.Hour)) {
		t.Fatalf("expires_at = %v, want created_at + 3h", s1.ExpiresAt)
	}
	// without a cap the old behavior stays
	mustNoErr(t, is.TouchSession(ctx, "s2", now.Add(90*time.Minute), 0))
	if s2, _ := is.GetSession(ctx, "s2"); !s2.ExpiresAt.Equal(now.Add(210 * time.Minute)) {
		t.Fatalf("uncapped: %v", s2.ExpiresAt)
	}
	n, err := is.DeleteSessionsByUser(ctx, u.ID)
	mustNoErr(t, err)
	if n != 2 {
		t.Fatalf("deleted %d sessions", n)
	}
	if _, err := is.GetSession(ctx, "s3"); err != nil {
		t.Fatal("the sessions of other users stay")
	}
}

// AUTH-TOK-1: all tokens of a creator are revoked together.
func TestRevokeAPITokensByCreator(t *testing.T) {
	ctx := ctxT(t)
	db := newDB(t)
	is := db.Identity()
	u, _ := is.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "local:x", Provider: "local", Role: domain.RoleAdmin})
	uid := u.ID
	mk := func(name string, creator *uuid.UUID) uuid.UUID {
		id := uuid.New()
		mustNoErr(t, is.CreateAPIToken(ctx, domain.APIToken{ID: id, Name: name, DisplayPrefix: name, Hash: domain.SHA256([]byte(name)), Role: domain.RoleViewer, CreatedBy: "x", CreatedByUserID: creator}))
		return id
	}
	mk("a", &uid)
	mk("b", &uid)
	mk("c", nil)
	already := mk("d", &uid)
	mustNoErr(t, is.RevokeAPIToken(ctx, already, time.Now()))
	ids, err := is.RevokeAPITokensByCreator(ctx, uid, time.Now())
	mustNoErr(t, err)
	if len(ids) != 2 {
		t.Fatalf("revoked %v", ids)
	}
	for _, h := range []string{"a", "b", "c"} {
		tok, _ := is.GetAPITokenByHash(ctx, domain.SHA256([]byte(h)))
		if (h != "c") != (tok.RevokedAt != nil) {
			t.Fatalf("token %s: %+v", h, tok)
		}
		if h != "c" && (tok.CreatedByUserID == nil || *tok.CreatedByUserID != uid) {
			t.Fatalf("creator not stored: %+v", tok)
		}
	}
}

// AUTH-OIDC-1 / AC-03: listing and lookup helpers.
func TestEnrollmentTokenLookupsForRevalidation(t *testing.T) {
	ctx := ctxT(t)
	db := newDB(t)
	is := db.Identity()
	now := time.Now().UTC()
	exp := now.Add(-time.Minute)
	mk := func(name string, expires *time.Time) uuid.UUID {
		id := uuid.New()
		mustNoErr(t, is.CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: id, Name: name, DisplayPrefix: name, Hash: domain.SHA256([]byte(name)), CreatedBy: "x", ExpiresAt: expires}))
		return id
	}
	ok, expired, revoked := mk("ok", nil), mk("expired", &exp), mk("revoked", nil)
	mustNoErr(t, is.RevokeEnrollmentToken(ctx, revoked, now))
	got, err := is.GetEnrollmentToken(ctx, ok)
	mustNoErr(t, err)
	if got.Name != "ok" {
		t.Fatalf("%+v", got)
	}
	if _, err := is.GetEnrollmentToken(ctx, uuid.New()); err == nil {
		t.Fatal("unknown token")
	}
	active, err := is.ListActiveEnrollmentTokenIDs(ctx, now)
	mustNoErr(t, err)
	if len(active) != 1 || active[0] != ok {
		t.Fatalf("active tokens: %v (expired %s, revoked %s)", active, expired, revoked)
	}
}
