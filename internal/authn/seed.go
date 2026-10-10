// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

func hashPassword(p string) [32]byte { return sha256.Sum256([]byte(p)) }

// EnsureDevSeed creates the dev enrollment token (dev mode only) and the
// local dev users. Idempotent.
func (s *Service) EnsureDevSeed(ctx context.Context, dev config.DevConfig) error {
	ids := s.deps.Store.Identity()
	if tok := dev.SeedEnrollmentToken; tok != "" {
		if msg := domain.EnrollmentTokenProblem(tok); msg != "" {
			return fmt.Errorf("authn: dev.seedEnrollmentToken %s", msg)
		}
		h := hashToken(tok)
		_, err := ids.GetEnrollmentTokenByHash(ctx, h)
		switch {
		case err == nil:
		case errors.Is(err, domain.ErrNotFound):
			err = ids.CreateEnrollmentToken(ctx, domain.EnrollmentToken{
				ID: newUUID(), Name: "dev-seed", DisplayPrefix: displayPrefix(tok), Hash: h, Labels: map[string]string{"channel": "dev"},
				CreatedBy: "system", CreatedAt: time.Now().UTC(),
			})
			if err != nil && !errors.Is(err, domain.ErrConflict) { // concurrent replica
				return fmt.Errorf("authn: create dev enrollment token: %w", err)
			}
		default:
			return fmt.Errorf("authn: look up dev enrollment token: %w", err)
		}
	}
	for _, u := range s.cfg.DevLogin.Users {
		if _, err := ids.UpsertUser(ctx, domain.User{
			ID: newUUID(), Subject: "local:" + u.Username, Provider: "local", Name: u.Username, Role: u.Role, RoleSource: "mapping",
		}); err != nil {
			return fmt.Errorf("authn: seed dev user %s: %w", u.Username, err)
		}
	}
	return nil
}

// RevokeDevSeedTokens revokes the given (well-known) enrollment tokens if they
// exist and are still valid, writes one audit entry per revoked token and
// returns how many were revoked. `myriarchd serve` calls it at startup: a
// database that was used with `myriarchd dev` must not keep accepting the
// well-known dev token.
func (s *Service) RevokeDevSeedTokens(ctx context.Context, tokens ...string) (int, error) {
	ids := s.deps.Store.Identity()
	n := 0
	seen := map[string]bool{}
	for _, tok := range tokens {
		if tok == "" || seen[tok] {
			continue
		}
		seen[tok] = true
		t, err := ids.GetEnrollmentTokenByHash(ctx, hashToken(tok))
		switch {
		case errors.Is(err, domain.ErrNotFound):
			continue
		case err != nil:
			return n, err
		case t.RevokedAt != nil:
			continue
		}
		if err := ids.RevokeEnrollmentToken(ctx, t.ID, s.now()); err != nil {
			return n, err
		}
		n++
		s.publishEnrollmentRevoked(ctx, t.ID, "revoked")
		if s.deps.Audit != nil {
			s.deps.Audit.Record(ctx, domain.AuditEvent{
				Time: s.now(), ActorType: domain.ActorSystem, ActorID: "system", ActorName: "system", Action: "enrollment_token.revoke",
				TargetType: "enrollment_token", TargetID: t.ID.String(), Result: "success",
				Details: map[string]any{"name": t.Name, "reason": "well-known dev seed token found outside dev mode"},
			})
		}
	}
	return n, nil
}
