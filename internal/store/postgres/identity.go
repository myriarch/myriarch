// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type identityStore struct{ db *DB }

var _ store.IdentityStore = (*identityStore)(nil)

// ------------------------------------------------------------------ users ---

const userColumns = `id, subject, provider, issuer, email, name, role, role_source, disabled, created_at, last_login_at`

func scanUser(r rowScanner) (*domain.User, error) {
	var (
		u        domain.User
		role     string
		lastSeen *time.Time
	)
	if err := r.Scan(&u.ID, &u.Subject, &u.Provider, &u.Issuer, &u.Email, &u.Name, &role, &u.RoleSource, &u.Disabled, &u.CreatedAt, &lastSeen); err != nil {
		return nil, err
	}
	u.Role, u.CreatedAt, u.LastLoginAt = domain.Role(role), u.CreatedAt.UTC(), utcPtr(lastSeen)
	return &u, nil
}

// UpsertUser implements store.IdentityStore. On update email and name are
// refreshed; role and roleSource only change while the stored role source is
// "mapping" (manual roles set by an admin survive logins).
func (s *identityStore) UpsertUser(ctx context.Context, u domain.User) (*domain.User, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if u.RoleSource == "" {
		u.RoleSource = "mapping"
	}
	created := u.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if u.Issuer != "" {
		// users created before the issuer was recorded adopt it at their next login
		if _, err := s.db.q.Exec(ctx, `UPDATE users SET issuer = $3 WHERE provider = $1 AND subject = $2 AND issuer = ''`, u.Provider, u.Subject, u.Issuer); err != nil {
			return nil, dbErr("identity.upsertUser adopt issuer", err)
		}
	}
	row := s.db.q.QueryRow(ctx, `INSERT INTO users (id, subject, provider, issuer, email, name, role, role_source, disabled, created_at)
VALUES ($1,$2,$3,$10,$4,$5,$6,$7,$8,$9)
ON CONFLICT (provider, issuer, subject) DO UPDATE SET
    email = EXCLUDED.email, name = EXCLUDED.name,
    role = CASE WHEN users.role_source = 'mapping' THEN EXCLUDED.role ELSE users.role END,
    role_source = CASE WHEN users.role_source = 'mapping' THEN EXCLUDED.role_source ELSE users.role_source END
RETURNING `+userColumns,
		u.ID, u.Subject, u.Provider, cleanText(u.Email), cleanText(u.Name), string(u.Role), u.RoleSource, u.Disabled, created.UTC(), u.Issuer)
	out, err := scanUser(row)
	if err != nil {
		return nil, dbErr("identity.upsertUser", err)
	}
	return out, nil
}

// GetUser implements store.IdentityStore.
func (s *identityStore) GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	u, err := scanUser(s.db.q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if err != nil {
		return nil, dbErr("identity.getUser", err)
	}
	return u, nil
}

// LockUser implements store.IdentityStore (SELECT ... FOR UPDATE).
func (s *identityStore) LockUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	u, err := scanUser(s.db.q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return nil, dbErr("identity.lockUser", err)
	}
	return u, nil
}

// ListUsers implements store.IdentityStore.
func (s *identityStore) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.q.Query(ctx, `SELECT `+userColumns+` FROM users ORDER BY lower(name), subject`)
	if err != nil {
		return nil, dbErr("identity.listUsers", err)
	}
	defer rows.Close()
	out := []domain.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, dbErr("identity.listUsers scan", err)
		}
		out = append(out, *u)
	}
	return out, dbErr("identity.listUsers", rows.Err())
}

// SetUserRole implements store.IdentityStore.
func (s *identityStore) SetUserRole(ctx context.Context, id uuid.UUID, role domain.Role, source string) error {
	if !role.Valid() {
		return fmt.Errorf("%w: unknown role %q", domain.ErrInvalidArgument, role)
	}
	if source == "" {
		source = "manual"
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE users SET role = $2, role_source = $3 WHERE id = $1`, id, string(role), source)
	if err != nil {
		return dbErr("identity.setUserRole", err)
	}
	return rowsAffectedOrNotFound("identity.setUserRole", tag)
}

// SetUserDisabled implements store.IdentityStore.
func (s *identityStore) SetUserDisabled(ctx context.Context, id uuid.UUID, disabled bool) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE users SET disabled = $2 WHERE id = $1`, id, disabled)
	if err != nil {
		return dbErr("identity.setUserDisabled", err)
	}
	return rowsAffectedOrNotFound("identity.setUserDisabled", tag)
}

// TouchUserLogin implements store.IdentityStore.
func (s *identityStore) TouchUserLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, id, at.UTC())
	if err != nil {
		return dbErr("identity.touchUserLogin", err)
	}
	return rowsAffectedOrNotFound("identity.touchUserLogin", tag)
}

// --------------------------------------------------------------- sessions ---

// CreateSession implements store.IdentityStore. last_seen_at defaults to the
// creation time; the sliding expiry of TouchSession preserves the original
// lifetime (expires_at - last_seen_at).
func (s *identityStore) CreateSession(ctx context.Context, sess domain.Session) error {
	created := sess.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	lastSeen := sess.LastSeenAt
	if lastSeen.IsZero() {
		lastSeen = created
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO sessions (id, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, remote_addr)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		sess.ID, sess.UserID, sess.CSRFToken, created.UTC(), sess.ExpiresAt.UTC(), lastSeen.UTC(), cleanText(sess.UserAgent), sess.RemoteAddr)
	return dbErr("identity.createSession", err)
}

// GetSession implements store.IdentityStore (expiry is checked by the caller).
func (s *identityStore) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	var sess domain.Session
	err := s.db.q.QueryRow(ctx, `SELECT id, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, remote_addr
FROM sessions WHERE id = $1`, id).Scan(&sess.ID, &sess.UserID, &sess.CSRFToken, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt, &sess.UserAgent, &sess.RemoteAddr)
	if err != nil {
		return nil, dbErr("identity.getSession", err)
	}
	sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt = sess.CreatedAt.UTC(), sess.ExpiresAt.UTC(), sess.LastSeenAt.UTC()
	return &sess, nil
}

// TouchSession implements store.IdentityStore: sliding expiry. The session
// keeps its lifetime (expires_at - last_seen_at), moved to the new activity time.
func (s *identityStore) TouchSession(ctx context.Context, id string, at time.Time, maxAge time.Duration) error {
	if maxAge <= 0 {
		_, err := s.db.q.Exec(ctx, `UPDATE sessions SET expires_at = $2::timestamptz + (expires_at - last_seen_at), last_seen_at = $2
WHERE id = $1 AND last_seen_at < $2`, id, at.UTC())
		return dbErr("identity.touchSession", err)
	}
	// absolute timeout: the sliding expiry never moves beyond created_at + maxAge
	_, err := s.db.q.Exec(ctx, `UPDATE sessions SET
    expires_at = LEAST($2::timestamptz + (expires_at - last_seen_at), created_at + $3::bigint * interval '1 microsecond'),
    last_seen_at = $2
WHERE id = $1 AND last_seen_at < $2`, id, at.UTC(), maxAge.Microseconds())
	return dbErr("identity.touchSession", err)
}

// DeleteSessionsByUser implements store.IdentityStore.
func (s *identityStore) DeleteSessionsByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	tag, err := s.db.q.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return 0, dbErr("identity.deleteSessionsByUser", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteSession implements store.IdentityStore.
func (s *identityStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.q.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return dbErr("identity.deleteSession", err)
}

// DeleteExpiredSessions implements store.IdentityStore.
func (s *identityStore) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.db.q.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1`, now.UTC())
	if err != nil {
		return 0, dbErr("identity.deleteExpiredSessions", err)
	}
	return tag.RowsAffected(), nil
}

// ------------------------------------------------------------- API tokens ---

const apiTokenColumns = `id, name, display_prefix, hash, role, created_by, created_by_user_id, created_at, expires_at, last_used_at, revoked_at`

func scanAPIToken(r rowScanner) (*domain.APIToken, error) {
	var (
		t                          domain.APIToken
		hash                       []byte
		role                       string
		expires, lastUsed, revoked *time.Time
	)
	if err := r.Scan(&t.ID, &t.Name, &t.DisplayPrefix, &hash, &role, &t.CreatedBy, &t.CreatedByUserID, &t.CreatedAt, &expires, &lastUsed, &revoked); err != nil {
		return nil, err
	}
	t.Hash, t.Role, t.CreatedAt = domain.Hash(hash), domain.Role(role), t.CreatedAt.UTC()
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = utcPtr(expires), utcPtr(lastUsed), utcPtr(revoked)
	return &t, nil
}

// CreateAPIToken implements store.IdentityStore.
func (s *identityStore) CreateAPIToken(ctx context.Context, t domain.APIToken) error {
	created := t.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO api_tokens (id, name, display_prefix, hash, role, created_by, created_by_user_id, created_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		t.ID, cleanText(t.Name), t.DisplayPrefix, []byte(t.Hash), string(t.Role), cleanText(t.CreatedBy), uuidPtrArg(t.CreatedByUserID), created.UTC(), timePtrArg(t.ExpiresAt))
	return dbErr("identity.createAPIToken", err)
}

// GetAPITokenByHash implements store.IdentityStore.
func (s *identityStore) GetAPITokenByHash(ctx context.Context, hash domain.Hash) (*domain.APIToken, error) {
	t, err := scanAPIToken(s.db.q.QueryRow(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE hash = $1`, []byte(hash)))
	if err != nil {
		return nil, dbErr("identity.getAPITokenByHash", err)
	}
	return t, nil
}

// ListAPITokens implements store.IdentityStore.
func (s *identityStore) ListAPITokens(ctx context.Context) ([]domain.APIToken, error) {
	rows, err := s.db.q.Query(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, dbErr("identity.listAPITokens", err)
	}
	defer rows.Close()
	out := []domain.APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, dbErr("identity.listAPITokens scan", err)
		}
		out = append(out, *t)
	}
	return out, dbErr("identity.listAPITokens", rows.Err())
}

// RevokeAPIToken implements store.IdentityStore (idempotent: the first
// revocation time is kept).
func (s *identityStore) RevokeAPIToken(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, at.UTC())
	if err != nil {
		return dbErr("identity.revokeAPIToken", err)
	}
	return rowsAffectedOrNotFound("identity.revokeAPIToken", tag)
}

// RevokeAPITokensByCreator implements store.IdentityStore.
func (s *identityStore) RevokeAPITokensByCreator(ctx context.Context, userID uuid.UUID, at time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.q.Query(ctx, `UPDATE api_tokens SET revoked_at = $2 WHERE created_by_user_id = $1 AND revoked_at IS NULL RETURNING id`, userID, at.UTC())
	if err != nil {
		return nil, dbErr("identity.revokeAPITokensByCreator", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, dbErr("identity.revokeAPITokensByCreator scan", err)
		}
		ids = append(ids, id)
	}
	return ids, dbErr("identity.revokeAPITokensByCreator", rows.Err())
}

// TouchAPIToken implements store.IdentityStore.
func (s *identityStore) TouchAPIToken(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.db.q.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2 WHERE id = $1`, id, at.UTC())
	return dbErr("identity.touchAPIToken", err)
}

// ------------------------------------------------------ enrollment tokens ---

const enrollmentColumns = `id, name, display_prefix, hash, labels, created_by, created_at, expires_at, last_used_at, revoked_at, use_count`

func scanEnrollmentToken(r rowScanner) (*domain.EnrollmentToken, error) {
	var (
		t                          domain.EnrollmentToken
		hash, labels               []byte
		expires, lastUsed, revoked *time.Time
	)
	if err := r.Scan(&t.ID, &t.Name, &t.DisplayPrefix, &hash, &labels, &t.CreatedBy, &t.CreatedAt, &expires, &lastUsed, &revoked, &t.UseCount); err != nil {
		return nil, err
	}
	t.Hash, t.CreatedAt = domain.Hash(hash), t.CreatedAt.UTC()
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = utcPtr(expires), utcPtr(lastUsed), utcPtr(revoked)
	t.Labels = map[string]string{}
	if err := decodeJSON(labels, &t.Labels); err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	t.Labels = stringMap(t.Labels)
	return &t, nil
}

// CreateEnrollmentToken implements store.IdentityStore.
func (s *identityStore) CreateEnrollmentToken(ctx context.Context, t domain.EnrollmentToken) error {
	created := t.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	labels, err := jsonArg(stringMap(t.Labels), "{}")
	if err != nil {
		return err
	}
	_, err = s.db.q.Exec(ctx, `INSERT INTO enrollment_tokens (id, name, display_prefix, hash, labels, created_by, created_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, cleanText(t.Name), t.DisplayPrefix, []byte(t.Hash), labels, cleanText(t.CreatedBy), created.UTC(), timePtrArg(t.ExpiresAt))
	return dbErr("identity.createEnrollmentToken", err)
}

// GetEnrollmentTokenByHash implements store.IdentityStore.
func (s *identityStore) GetEnrollmentTokenByHash(ctx context.Context, hash domain.Hash) (*domain.EnrollmentToken, error) {
	t, err := scanEnrollmentToken(s.db.q.QueryRow(ctx, `SELECT `+enrollmentColumns+` FROM enrollment_tokens WHERE hash = $1`, []byte(hash)))
	if err != nil {
		return nil, dbErr("identity.getEnrollmentTokenByHash", err)
	}
	return t, nil
}

// GetEnrollmentToken implements store.IdentityStore.
func (s *identityStore) GetEnrollmentToken(ctx context.Context, id uuid.UUID) (*domain.EnrollmentToken, error) {
	t, err := scanEnrollmentToken(s.db.q.QueryRow(ctx, `SELECT `+enrollmentColumns+` FROM enrollment_tokens WHERE id = $1`, id))
	if err != nil {
		return nil, dbErr("identity.getEnrollmentToken", err)
	}
	return t, nil
}

// ListActiveEnrollmentTokenIDs implements store.IdentityStore.
func (s *identityStore) ListActiveEnrollmentTokenIDs(ctx context.Context, at time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.q.Query(ctx, `SELECT id FROM enrollment_tokens WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $1)`, at.UTC())
	if err != nil {
		return nil, dbErr("identity.listActiveEnrollmentTokenIDs", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, dbErr("identity.listActiveEnrollmentTokenIDs scan", err)
		}
		ids = append(ids, id)
	}
	return ids, dbErr("identity.listActiveEnrollmentTokenIDs", rows.Err())
}

// ListEnrollmentTokens implements store.IdentityStore.
func (s *identityStore) ListEnrollmentTokens(ctx context.Context) ([]domain.EnrollmentToken, error) {
	rows, err := s.db.q.Query(ctx, `SELECT `+enrollmentColumns+` FROM enrollment_tokens ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, dbErr("identity.listEnrollmentTokens", err)
	}
	defer rows.Close()
	out := []domain.EnrollmentToken{}
	for rows.Next() {
		t, err := scanEnrollmentToken(rows)
		if err != nil {
			return nil, dbErr("identity.listEnrollmentTokens scan", err)
		}
		out = append(out, *t)
	}
	return out, dbErr("identity.listEnrollmentTokens", rows.Err())
}

// RevokeEnrollmentToken implements store.IdentityStore.
func (s *identityStore) RevokeEnrollmentToken(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE enrollment_tokens SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, at.UTC())
	if err != nil {
		return dbErr("identity.revokeEnrollmentToken", err)
	}
	return rowsAffectedOrNotFound("identity.revokeEnrollmentToken", tag)
}

// UpdateEnrollmentToken implements store.IdentityStore.
func (s *identityStore) UpdateEnrollmentToken(ctx context.Context, t domain.EnrollmentToken) error {
	labels, err := jsonArg(stringMap(t.Labels), "{}")
	if err != nil {
		return err
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE enrollment_tokens SET name = $2, labels = $3, expires_at = $4 WHERE id = $1`,
		t.ID, cleanText(t.Name), labels, timePtrArg(t.ExpiresAt))
	if err != nil {
		return dbErr("identity.updateEnrollmentToken", err)
	}
	return rowsAffectedOrNotFound("identity.updateEnrollmentToken", tag)
}

// TouchEnrollmentToken implements store.IdentityStore.
func (s *identityStore) TouchEnrollmentToken(ctx context.Context, id uuid.UUID, at time.Time, uses int) error {
	if uses < 1 {
		uses = 1
	}
	_, err := s.db.q.Exec(ctx, `UPDATE enrollment_tokens SET last_used_at = GREATEST(COALESCE(last_used_at, $2), $2), use_count = use_count + $3 WHERE id = $1`, id, at.UTC(), uses)
	return dbErr("identity.touchEnrollmentToken", err)
}

// ---------------------------------------------------------- saved filters ---

// ListSavedFilters implements store.IdentityStore.
func (s *identityStore) ListSavedFilters(ctx context.Context, userID uuid.UUID, page string) ([]domain.SavedFilter, error) {
	rows, err := s.db.q.Query(ctx, `SELECT id, user_id, name, page, query, shared, created_at FROM saved_filters
WHERE page = $2 AND (user_id = $1 OR shared) ORDER BY lower(name), id`, userID, page)
	if err != nil {
		return nil, dbErr("identity.listSavedFilters", err)
	}
	defer rows.Close()
	out := []domain.SavedFilter{}
	for rows.Next() {
		var (
			f domain.SavedFilter
			q []byte
		)
		if err := rows.Scan(&f.ID, &f.UserID, &f.Name, &f.Page, &q, &f.Shared, &f.CreatedAt); err != nil {
			return nil, dbErr("identity.listSavedFilters scan", err)
		}
		f.CreatedAt = f.CreatedAt.UTC()
		f.Query = map[string]any{}
		if err := decodeJSON(q, &f.Query); err != nil {
			return nil, fmt.Errorf("decode saved filter query: %w", err)
		}
		if f.Query == nil {
			f.Query = map[string]any{}
		}
		out = append(out, f)
	}
	return out, dbErr("identity.listSavedFilters", rows.Err())
}

// CreateSavedFilter implements store.IdentityStore.
func (s *identityStore) CreateSavedFilter(ctx context.Context, f domain.SavedFilter) error {
	created := f.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	page := f.Page
	if page == "" {
		page = "agents"
	}
	q := f.Query
	if q == nil {
		q = map[string]any{}
	}
	qb, err := jsonArg(q, "{}")
	if err != nil {
		return err
	}
	_, err = s.db.q.Exec(ctx, `INSERT INTO saved_filters (id, user_id, name, page, query, shared, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		f.ID, f.UserID, cleanText(f.Name), page, qb, f.Shared, created.UTC())
	return dbErr("identity.createSavedFilter", err)
}

// DeleteSavedFilter implements store.IdentityStore.
func (s *identityStore) DeleteSavedFilter(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.q.Exec(ctx, `DELETE FROM saved_filters WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return dbErr("identity.deleteSavedFilter", err)
	}
	return rowsAffectedOrNotFound("identity.deleteSavedFilter", tag)
}
