// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
)

// UpdateEnrollmentToken changes name, labels and expiry only.
func TestUpdateEnrollmentToken(t *testing.T) {
	ctx := ctxT(t)
	is := newDB(t).Identity()
	now := time.Now().UTC().Truncate(time.Microsecond)

	hash := domain.SHA256([]byte("declared-token-value"))
	tok := domain.EnrollmentToken{ID: uuid.New(), Name: "edge", DisplayPrefix: "myre_declare", Hash: hash,
		Labels: map[string]string{"site": "a"}, CreatedBy: "config", CreatedAt: now}
	mustNoErr(t, is.CreateEnrollmentToken(ctx, tok))
	mustNoErr(t, is.TouchEnrollmentToken(ctx, tok.ID, now, 1))
	mustNoErr(t, is.RevokeEnrollmentToken(ctx, tok.ID, now.Add(time.Minute)))

	expires := now.Add(48 * time.Hour)
	upd := tok
	upd.Name = "edge-renamed"
	upd.Labels = map[string]string{"site": "b", "tier": "gold"}
	upd.ExpiresAt = &expires
	// fields that must not change through an update
	upd.Hash = domain.SHA256([]byte("something else"))
	upd.CreatedBy = "someone else"
	upd.UseCount = 99
	mustNoErr(t, is.UpdateEnrollmentToken(ctx, upd))

	got, err := is.GetEnrollmentToken(ctx, tok.ID)
	mustNoErr(t, err)
	if got.Name != "edge-renamed" || got.Labels["site"] != "b" || got.Labels["tier"] != "gold" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("updated fields: %+v", got)
	}
	if string(got.Hash) != string(hash) || got.CreatedBy != "config" || got.UseCount != 1 || got.RevokedAt == nil || got.DisplayPrefix != "myre_declare" {
		t.Fatalf("fields that an update must keep: %+v", got)
	}

	// clearing the expiry and the labels
	upd.ExpiresAt, upd.Labels = nil, nil
	mustNoErr(t, is.UpdateEnrollmentToken(ctx, upd))
	got, err = is.GetEnrollmentToken(ctx, tok.ID)
	mustNoErr(t, err)
	if got.ExpiresAt != nil || len(got.Labels) != 0 {
		t.Fatalf("cleared fields: %+v", got)
	}

	if err := is.UpdateEnrollmentToken(ctx, domain.EnrollmentToken{ID: uuid.New(), Name: "missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a missing token is ErrNotFound: %v", err)
	}
}

// The provider "bootstrap" is accepted by the users table.
func TestUpsertBootstrapUser(t *testing.T) {
	ctx := ctxT(t)
	is := newDB(t).Identity()
	u, err := is.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: domain.BootstrapSubjectPrefix + "admin", Provider: domain.ProviderBootstrap,
		Name: "admin", Role: domain.RoleAdmin, RoleSource: "mapping"})
	mustNoErr(t, err)
	if u.Provider != "bootstrap" || u.Role != domain.RoleAdmin || u.Disabled {
		t.Fatalf("bootstrap user: %+v", u)
	}
}

// TouchEnrollmentToken adds the number of counted uses with one statement.
func TestTouchEnrollmentTokenAddsTheCountedUses(t *testing.T) {
	ctx := ctxT(t)
	is := newDB(t).Identity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	tok := domain.EnrollmentToken{ID: uuid.New(), Name: "edge", DisplayPrefix: "myre_counted", Hash: domain.SHA256([]byte("counted-token-value")), CreatedBy: "config", CreatedAt: now}
	mustNoErr(t, is.CreateEnrollmentToken(ctx, tok))
	mustNoErr(t, is.TouchEnrollmentToken(ctx, tok.ID, now, 7))
	mustNoErr(t, is.TouchEnrollmentToken(ctx, tok.ID, now.Add(time.Second), 5))
	got, err := is.GetEnrollmentToken(ctx, tok.ID)
	mustNoErr(t, err)
	if got.UseCount != 12 || got.LastUsedAt == nil || !got.LastUsedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("use count %d, last used %v", got.UseCount, got.LastUsedAt)
	}
}
