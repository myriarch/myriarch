// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type artifactStore struct{ db *DB }

var _ store.ArtifactStore = (*artifactStore)(nil)

const artifactColumns = `id, distribution, version, os, arch, format, filename, size, sha256, sigstore_bundle, signature_verified,
  signature_identity, verification_error, source_url, status, error, created_at, updated_at`

func scanArtifact(r rowScanner, extra ...any) (*domain.Artifact, error) {
	var (
		a      domain.Artifact
		sha    []byte
		status string
	)
	dest := append([]any{&a.ID, &a.Distribution, &a.Version, &a.OS, &a.Arch, &a.Format, &a.Filename, &a.Size, &sha, &a.SigstoreBundle,
		&a.SignatureVerified, &a.SignatureIdentity, &a.VerificationError, &a.SourceURL, &status, &a.Error, &a.CreatedAt, &a.UpdatedAt}, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	a.SHA256, a.Status = domain.Hash(sha), domain.ArtifactStatus(status)
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return &a, nil
}

// Upsert implements store.ArtifactStore.
func (s *artifactStore) Upsert(ctx context.Context, a domain.Artifact) (*domain.Artifact, error) {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	status := string(a.Status)
	if status == "" {
		status = string(domain.ArtifactPending)
	}
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	var bundle any
	if len(a.SigstoreBundle) > 0 {
		bundle = a.SigstoreBundle
	}
	out, err := scanArtifact(s.db.q.QueryRow(ctx, `INSERT INTO artifacts (id, distribution, version, os, arch, format, filename, size, sha256, sigstore_bundle,
    signature_verified, signature_identity, verification_error, source_url, status, error, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
ON CONFLICT (distribution, version, os, arch, format) DO UPDATE SET filename = EXCLUDED.filename, size = EXCLUDED.size,
    sha256 = EXCLUDED.sha256, sigstore_bundle = EXCLUDED.sigstore_bundle, signature_verified = EXCLUDED.signature_verified,
    signature_identity = EXCLUDED.signature_identity, verification_error = EXCLUDED.verification_error,
    source_url = EXCLUDED.source_url, status = EXCLUDED.status, error = EXCLUDED.error, updated_at = now()
RETURNING `+artifactColumns,
		a.ID, a.Distribution, a.Version, a.OS, a.Arch, a.Format, a.Filename, a.Size, hashArg(a.SHA256), bundle,
		a.SignatureVerified, a.SignatureIdentity, cleanText(a.VerificationError), a.SourceURL, status, cleanText(a.Error), created.UTC()))
	if err != nil {
		return nil, dbErr("artifacts.upsert", err)
	}
	return out, nil
}

// Get implements store.ArtifactStore.
func (s *artifactStore) Get(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	a, err := scanArtifact(s.db.q.QueryRow(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE id = $1`, id))
	if err != nil {
		return nil, dbErr("artifacts.get", err)
	}
	return a, nil
}

// Find implements store.ArtifactStore.
func (s *artifactStore) Find(ctx context.Context, distribution, version, os, arch, format string) (*domain.Artifact, error) {
	a, err := scanArtifact(s.db.q.QueryRow(ctx, `SELECT `+artifactColumns+` FROM artifacts
WHERE distribution = $1 AND version = $2 AND os = $3 AND arch = $4 AND format = $5`, distribution, domain.NormalizeVersion(version), os, arch, format))
	if err != nil {
		return nil, dbErr("artifacts.find", err)
	}
	return a, nil
}

// List implements store.ArtifactStore.
func (s *artifactStore) List(ctx context.Context, f store.ArtifactFilter) ([]domain.Artifact, int, error) {
	var args argList
	var clauses []string
	if f.Distribution != "" {
		clauses = append(clauses, "distribution = "+args.add(f.Distribution))
	}
	if f.Version != "" {
		clauses = append(clauses, "version = "+args.add(domain.NormalizeVersion(f.Version)))
	}
	if len(f.Status) > 0 {
		ss := make([]string, len(f.Status))
		for i, st := range f.Status {
			ss[i] = string(st)
		}
		clauses = append(clauses, "status = ANY("+args.add(ss)+")")
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	nWhereArgs := len(args.args)
	sql := `SELECT ` + artifactColumns + `, count(*) OVER () FROM artifacts` + where + ` ORDER BY updated_at DESC, id`
	if f.Limit > 0 {
		sql += " LIMIT " + args.add(f.Limit)
	}
	if f.Offset > 0 {
		sql += " OFFSET " + args.add(f.Offset)
	}
	rows, err := s.db.q.Query(ctx, sql, args.args...)
	if err != nil {
		return nil, 0, dbErr("artifacts.list", err)
	}
	defer rows.Close()
	out := []domain.Artifact{}
	total := 0
	for rows.Next() {
		a, err := scanArtifact(rows, &total)
		if err != nil {
			return nil, 0, dbErr("artifacts.list scan", err)
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dbErr("artifacts.list", err)
	}
	if len(out) == 0 && f.Offset > 0 {
		if err := s.db.q.QueryRow(ctx, "SELECT count(*) FROM artifacts"+where, args.args[:nWhereArgs]...).Scan(&total); err != nil {
			return nil, 0, dbErr("artifacts.list count", err)
		}
	}
	return out, total, nil
}

// Delete implements store.ArtifactStore.
func (s *artifactStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.q.Exec(ctx, `DELETE FROM artifacts WHERE id = $1`, id)
	if err != nil {
		return dbErr("artifacts.delete", err)
	}
	return rowsAffectedOrNotFound("artifacts.delete", tag)
}
