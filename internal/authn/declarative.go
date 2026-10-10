// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// Declarative enrollment tokens (opamp.enrollmentTokens) are reconciled into
// the database at startup. Only the SHA-256 hash and the 12 character display
// prefix are stored; the plaintext exists in the mounted file and, while the
// reconciliation runs, in memory.
//
// Rules: a token that is missing is created (created_by "config"); a token of
// this origin whose name, labels or expiry differ is updated; a revoked token
// stays revoked and a token created in the UI stays as it is (both are
// reported); nothing is ever revoked automatically. Running it again with the
// same configuration changes nothing and writes no audit entry.

// configTokenOrigin is the created_by value of declarative enrollment tokens.
const configTokenOrigin = "config"

// declaredToken is an entry with its file read and checked.
type declaredToken struct {
	cfg     config.EnrollmentTokenConfig
	hash    domain.Hash
	prefix  string
	expires *time.Time
}

// readDeclaredTokens reads and checks the files of all entries. Every problem
// is reported; nothing has touched the database at that point.
func readDeclaredTokens(entries []config.EnrollmentTokenConfig, devSeedToken string) ([]declaredToken, error) {
	var errs []error
	out := make([]declaredToken, 0, len(entries))
	values := map[string]int{}
	devTokens := []string{config.Default().Dev.SeedEnrollmentToken}
	if devSeedToken != "" {
		devTokens = append(devTokens, devSeedToken)
	}
	for i, e := range entries {
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("opamp.enrollmentTokens[%d].%s", i, fmt.Sprintf(format, args...)))
		}
		before := len(errs)
		if !domain.ValidDNSLabel(e.Name) {
			bad(`name: must be a DNS label: lower case letters, digits and "-", 1 to 63 characters`)
		}
		if issue := domain.CheckLabels(e.Labels); issue != nil {
			bad("labels: %s", domain.LabelsRule)
		}
		var expires *time.Time
		if e.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, e.ExpiresAt)
			if err != nil {
				bad("expiresAt: must be an RFC 3339 timestamp such as 2027-01-01T00:00:00Z")
			} else {
				t = t.UTC().Truncate(time.Microsecond) // the database keeps microseconds
				expires = &t
			}
		}
		if e.TokenFile == "" {
			bad("tokenFile: is required")
		}
		if len(errs) > before {
			continue
		}
		tok, err := readCredentialFile(e.TokenFile)
		if err != nil {
			bad("tokenFile: %v", err)
			continue
		}
		if msg := domain.EnrollmentTokenProblem(tok); msg != "" {
			bad("tokenFile: file %s %s", e.TokenFile, msg)
			continue
		}
		if slices.Contains(devTokens, tok) {
			bad("tokenFile: file %s contains the well-known development enrollment token", e.TokenFile)
			continue
		}
		if j, dup := values[tok]; dup {
			bad("tokenFile: file %s contains the same token as opamp.enrollmentTokens[%d]", e.TokenFile, j)
			continue
		}
		values[tok] = i
		out = append(out, declaredToken{cfg: e, hash: hashToken(tok), prefix: displayPrefix(tok), expires: expires})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// EnsureEnrollmentTokens reconciles opamp.enrollmentTokens into the database.
// Any problem with the entries (unreadable or malformed token file, duplicate
// value) is returned before the database is changed. Called at startup in
// serve and dev mode, after EnsureBootstrapAdmin. devSeedToken is the
// configured development seed token (dev.seedEnrollmentToken, empty if none);
// like the built-in default it is refused as a value, the same rule config
// validate applies. Callers pass cfg.Dev.SeedEnrollmentToken, so a token that
// the dev mode would create can never be declared by accident.
func (s *Service) EnsureEnrollmentTokens(ctx context.Context, entries []config.EnrollmentTokenConfig, devSeedToken string) error {
	declared, err := readDeclaredTokens(entries, devSeedToken)
	if err != nil {
		return err
	}
	for i, d := range declared {
		s.warnLooseMode(fmt.Sprintf("opamp.enrollmentTokens[%d].tokenFile", i), d.cfg.TokenFile)
	}
	ids := s.deps.Store.Identity()
	configured := make(map[string]bool, len(declared))
	for _, d := range declared {
		configured[d.hash.String()] = true
		if err := s.reconcileToken(ctx, d); err != nil {
			return fmt.Errorf("opamp.enrollmentTokens[%s]: %w", d.cfg.Name, err)
		}
	}

	// Tokens of this origin that are no longer configured stay valid until
	// someone revokes them: report them once per start.
	all, err := ids.ListEnrollmentTokens(ctx)
	if err != nil {
		return fmt.Errorf("authn: list enrollment tokens: %w", err)
	}
	now := s.now()
	for _, t := range all {
		active := t.RevokedAt == nil && (t.ExpiresAt == nil || now.Before(*t.ExpiresAt))
		if t.CreatedBy == configTokenOrigin && active && !configured[t.Hash.String()] {
			s.log.Warn("declarative enrollment token is no longer configured but still valid; revoke it in the UI or with DELETE /api/v1/enrollment-tokens/{id}",
				"name", t.Name, "id", t.ID.String())
		}
	}
	return nil
}

// reconcileToken creates or updates the token of one entry.
func (s *Service) reconcileToken(ctx context.Context, d declaredToken) error {
	ids := s.deps.Store.Identity()
	labels := d.cfg.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	existing, err := ids.GetEnrollmentTokenByHash(ctx, d.hash)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrNotFound):
		t := domain.EnrollmentToken{
			ID: newUUID(), Name: d.cfg.Name, DisplayPrefix: d.prefix, Hash: d.hash, Labels: labels,
			CreatedBy: configTokenOrigin, CreatedAt: s.now(), ExpiresAt: d.expires,
		}
		err = ids.CreateEnrollmentToken(ctx, t)
		if err == nil {
			details := map[string]any{"source": configTokenOrigin, "name": t.Name, "labels": labels, "prefix": t.DisplayPrefix}
			if d.expires != nil {
				details["expiresAt"] = d.expires.Format(time.RFC3339)
			}
			s.recordSystem(ctx, "enrollment_token.create", "enrollment_token", t.ID.String(), details)
			s.log.Info("declarative enrollment token created", "name", t.Name, "id", t.ID.String())
			return nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			return fmt.Errorf("create: %w", err)
		}
		// another replica created it first: continue with its row
		if existing, err = ids.GetEnrollmentTokenByHash(ctx, d.hash); err != nil {
			return fmt.Errorf("look up after a concurrent create: %w", err)
		}
	default:
		return fmt.Errorf("look up: %w", err)
	}

	switch {
	case existing.RevokedAt != nil:
		s.log.Warn("declarative enrollment token is revoked; rotate its value to issue a new one", "name", d.cfg.Name, "id", existing.ID.String())
		return nil
	case existing.CreatedBy != configTokenOrigin:
		s.log.Warn("the value of a declarative enrollment token belongs to a token that was created elsewhere (for example in the UI); it is left unchanged",
			"name", d.cfg.Name, "id", existing.ID.String(), "createdBy", existing.CreatedBy)
		return nil
	}

	var changed []string
	if existing.Name != d.cfg.Name {
		changed = append(changed, "name")
	}
	if !maps.Equal(nonNilLabels(existing.Labels), labels) {
		changed = append(changed, "labels")
	}
	if !sameTime(existing.ExpiresAt, d.expires) {
		changed = append(changed, "expiresAt")
	}
	if len(changed) == 0 {
		return nil
	}
	upd := *existing
	upd.Name, upd.Labels, upd.ExpiresAt = d.cfg.Name, labels, d.expires
	if err := ids.UpdateEnrollmentToken(ctx, upd); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	// every replica drops its cached row: a shortened expiry or changed labels
	// must not wait for the cache TTL (no agent session is disconnected)
	s.publishInvalidate(ctx, "enrollment_token", upd.ID.String())
	details := map[string]any{"source": configTokenOrigin, "name": upd.Name, "labels": labels, "changed": changed}
	if d.expires != nil {
		details["expiresAt"] = d.expires.Format(time.RFC3339)
	}
	s.recordSystem(ctx, "enrollment_token.update", "enrollment_token", upd.ID.String(), details)
	s.log.Info("declarative enrollment token updated", "name", upd.Name, "id", upd.ID.String(), "changed", strings.Join(changed, ","))
	return nil
}

func nonNilLabels(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
