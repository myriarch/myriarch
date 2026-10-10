// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type releaseStore struct{ db *DB }

var _ store.ReleaseStore = (*releaseStore)(nil)

const releaseColumns = `distribution, version, tag, published_at, prerelease, html_url, notes, breaking_changes, deprecations, assets,
  detected_at, acknowledged_by, acknowledged_at`

// releaseOrder sorts by semantic version descending.
const releaseOrder = ` ORDER BY version_major DESC, version_minor DESC, version_patch DESC, version DESC`

func scanRelease(r rowScanner, extra ...any) (*domain.Release, error) {
	var (
		rel                        domain.Release
		published, ackAt           *time.Time
		breaking, deprecations, as []byte
	)
	dest := append([]any{&rel.Distribution, &rel.Version, &rel.Tag, &published, &rel.Prerelease, &rel.HTMLURL, &rel.Notes,
		&breaking, &deprecations, &as, &rel.DetectedAt, &rel.AcknowledgedBy, &ackAt}, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	rel.PublishedAt, rel.AcknowledgedAt, rel.DetectedAt = utcPtr(published), utcPtr(ackAt), rel.DetectedAt.UTC()
	rel.BreakingChanges, rel.Deprecations, rel.Assets = []domain.ChangelogEntry{}, []domain.ChangelogEntry{}, []domain.ReleaseAsset{}
	if err := decodeJSON(breaking, &rel.BreakingChanges); err != nil {
		return nil, fmt.Errorf("decode breaking_changes: %w", err)
	}
	if err := decodeJSON(deprecations, &rel.Deprecations); err != nil {
		return nil, fmt.Errorf("decode deprecations: %w", err)
	}
	if err := decodeJSON(as, &rel.Assets); err != nil {
		return nil, fmt.Errorf("decode assets: %w", err)
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
	return &rel, nil
}

// Upsert implements store.ReleaseStore.
func (s *releaseStore) Upsert(ctx context.Context, r domain.Release) (bool, error) {
	v, err := semver.NewVersion(domain.NormalizeVersion(r.Version))
	if err != nil {
		return false, fmt.Errorf("%w: release version %q is not a semantic version", domain.ErrInvalidArgument, r.Version)
	}
	version := domain.NormalizeVersion(r.Version)
	breaking, err := jsonArg(nonNilEntries(r.BreakingChanges), "[]")
	if err != nil {
		return false, err
	}
	deprecations, err := jsonArg(nonNilEntries(r.Deprecations), "[]")
	if err != nil {
		return false, err
	}
	assets := r.Assets
	if assets == nil {
		assets = []domain.ReleaseAsset{}
	}
	assetsArg, err := jsonArg(assets, "[]")
	if err != nil {
		return false, err
	}
	detected := r.DetectedAt
	if detected.IsZero() {
		detected = time.Now()
	}
	var created bool
	err = s.db.q.QueryRow(ctx, `INSERT INTO releases (distribution, version, version_major, version_minor, version_patch, tag, published_at,
    prerelease, html_url, notes, breaking_changes, deprecations, assets, detected_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (distribution, version) DO UPDATE SET tag = EXCLUDED.tag, published_at = EXCLUDED.published_at,
    prerelease = EXCLUDED.prerelease, html_url = EXCLUDED.html_url, notes = EXCLUDED.notes,
    breaking_changes = EXCLUDED.breaking_changes, deprecations = EXCLUDED.deprecations, assets = EXCLUDED.assets
RETURNING (xmax = 0)`,
		r.Distribution, version, int32(v.Major()), int32(v.Minor()), int32(v.Patch()), r.Tag, timePtrArg(r.PublishedAt),
		r.Prerelease, r.HTMLURL, r.Notes, breaking, deprecations, assetsArg, detected.UTC()).Scan(&created)
	if err != nil {
		return false, dbErr("releases.upsert", err)
	}
	return created, nil
}

func nonNilEntries(e []domain.ChangelogEntry) []domain.ChangelogEntry {
	if e == nil {
		return []domain.ChangelogEntry{}
	}
	return e
}

// Get implements store.ReleaseStore.
func (s *releaseStore) Get(ctx context.Context, distribution, version string) (*domain.Release, error) {
	r, err := scanRelease(s.db.q.QueryRow(ctx, `SELECT `+releaseColumns+` FROM releases WHERE distribution = $1 AND version = $2`,
		distribution, domain.NormalizeVersion(version)))
	if err != nil {
		return nil, dbErr("releases.get", err)
	}
	return r, nil
}

// List implements store.ReleaseStore.
func (s *releaseStore) List(ctx context.Context, f store.ReleaseFilter) ([]domain.Release, int, error) {
	var args argList
	var clauses []string
	if f.Distribution != "" {
		clauses = append(clauses, "distribution = "+args.add(f.Distribution))
	}
	if !f.IncludePrerelease {
		clauses = append(clauses, "prerelease = false")
	}
	if f.SinceVersion != "" {
		v, err := semver.NewVersion(domain.NormalizeVersion(f.SinceVersion))
		if err != nil {
			return nil, 0, fmt.Errorf("%w: invalid sinceVersion %q", domain.ErrInvalidArgument, f.SinceVersion)
		}
		clauses = append(clauses, fmt.Sprintf("(version_major, version_minor, version_patch) > (%s::int, %s::int, %s::int)",
			args.add(int32(v.Major())), args.add(int32(v.Minor())), args.add(int32(v.Patch()))))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	nWhereArgs := len(args.args)
	sql := `SELECT ` + releaseColumns + `, count(*) OVER () FROM releases` + where + releaseOrder
	if f.Limit > 0 {
		sql += " LIMIT " + args.add(f.Limit)
	}
	if f.Offset > 0 {
		sql += " OFFSET " + args.add(f.Offset)
	}
	rows, err := s.db.q.Query(ctx, sql, args.args...)
	if err != nil {
		return nil, 0, dbErr("releases.list", err)
	}
	defer rows.Close()
	out := []domain.Release{}
	total := 0
	for rows.Next() {
		r, err := scanRelease(rows, &total)
		if err != nil {
			return nil, 0, dbErr("releases.list scan", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dbErr("releases.list", err)
	}
	if len(out) == 0 && f.Offset > 0 {
		if err := s.db.q.QueryRow(ctx, "SELECT count(*) FROM releases"+where, args.args[:nWhereArgs]...).Scan(&total); err != nil {
			return nil, 0, dbErr("releases.list count", err)
		}
	}
	return out, total, nil
}

// Latest implements store.ReleaseStore.
func (s *releaseStore) Latest(ctx context.Context, distribution string, includePrerelease bool) (*domain.Release, error) {
	sql := `SELECT ` + releaseColumns + ` FROM releases WHERE distribution = $1`
	if !includePrerelease {
		sql += ` AND prerelease = false`
	}
	r, err := scanRelease(s.db.q.QueryRow(ctx, sql+releaseOrder+` LIMIT 1`, distribution))
	if err != nil {
		return nil, dbErr("releases.latest", err)
	}
	return r, nil
}

// Acknowledge implements store.ReleaseStore.
func (s *releaseStore) Acknowledge(ctx context.Context, distribution, version, by string, at time.Time) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE releases SET acknowledged_by = $3, acknowledged_at = $4 WHERE distribution = $1 AND version = $2`,
		distribution, domain.NormalizeVersion(version), by, at.UTC())
	if err != nil {
		return dbErr("releases.acknowledge", err)
	}
	return rowsAffectedOrNotFound("releases.acknowledge", tag)
}
