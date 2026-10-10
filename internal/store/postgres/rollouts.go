// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type rolloutStore struct{ db *DB }

var _ store.RolloutStore = (*rolloutStore)(nil)

// copyThreshold: target inserts above this size use COPY.
const copyThreshold = 1000

var targetColumns = []string{"rollout_id", "agent_id", "hostname", "wave", "status", "from_ref", "to_ref", "attempts",
	"started_at", "verified_at", "finished_at", "last_error", "executor_ref", "updated_at", "progress"}

func targetRow(rolloutID uuid.UUID, t domain.RolloutTarget, now time.Time) ([]any, error) {
	status := string(t.Status)
	if status == "" {
		status = string(domain.TargetPending)
	}
	progress, err := progressArg(t.Progress)
	if err != nil {
		return nil, err
	}
	return []any{rolloutID, t.AgentID, cleanText(t.Hostname), t.Wave, status, t.FromRef, t.ToRef, t.Attempts,
		timePtrArg(t.StartedAt), timePtrArg(t.VerifiedAt), timePtrArg(t.FinishedAt), cleanText(t.LastError), t.ExecutorRef, now, progress}, nil
}

// progressArg marshals the live progress of a target (nil -> NULL).
func progressArg(p *domain.TargetProgress) (any, error) {
	if p == nil {
		return nil, nil
	}
	return jsonArg(p, "")
}

// insertTargets inserts targets with COPY (large sets) or a batch.
func insertTargets(ctx context.Context, q querier, rolloutID uuid.UUID, targets []domain.RolloutTarget, onConflictNothing bool) error {
	if len(targets) == 0 {
		return nil
	}
	now := time.Now().UTC()
	if len(targets) > copyThreshold && !onConflictNothing {
		rows := make([][]any, len(targets))
		for i, t := range targets {
			row, err := targetRow(rolloutID, t, now)
			if err != nil {
				return err
			}
			rows[i] = row
		}
		_, err := q.CopyFrom(ctx, pgx.Identifier{"rollout_targets"}, targetColumns, pgx.CopyFromRows(rows))
		return err
	}
	const sql = `INSERT INTO rollout_targets (rollout_id, agent_id, hostname, wave, status, from_ref, to_ref, attempts,
    started_at, verified_at, finished_at, last_error, executor_ref, updated_at, progress)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`
	suffix := ""
	if onConflictNothing {
		suffix = " ON CONFLICT (rollout_id, agent_id) DO NOTHING"
	}
	const chunk = 500
	for start := 0; start < len(targets); start += chunk {
		end := min(start+chunk, len(targets))
		b := &pgx.Batch{}
		for _, t := range targets[start:end] {
			row, err := targetRow(rolloutID, t, now)
			if err != nil {
				return err
			}
			b.Queue(sql+suffix, row...)
		}
		br := q.SendBatch(ctx, b)
		for range targets[start:end] {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Create implements store.RolloutStore.
func (s *rolloutStore) Create(ctx context.Context, r *domain.Rollout, targets []domain.RolloutTarget) error {
	if r == nil {
		return fmt.Errorf("%w: rollout is nil", domain.ErrInvalidArgument)
	}
	policy, err := jsonArg(r.Policy, "{}")
	if err != nil {
		return err
	}
	annotations, err := jsonArg(r.Annotations, "")
	if err != nil {
		return err
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	status := string(r.Status)
	if status == "" {
		status = string(domain.RolloutPending)
	}
	err = s.db.withTx(ctx, func(q querier) error {
		if _, err := q.Exec(ctx, `INSERT INTO rollouts (id, kind, fleet_name, policy_name, policy, status, status_reason, from_ref, to_ref,
    git_commit, executor, current_wave, created_by, annotations, created_at, started_at, finished_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			r.ID, string(r.Kind), r.FleetName, r.PolicyName, policy, status, cleanText(r.StatusReason), r.FromRef, r.ToRef,
			r.GitCommit, string(r.Executor), r.CurrentWave, r.CreatedBy, annotations, created.UTC(),
			timePtrArg(r.StartedAt), timePtrArg(r.FinishedAt)); err != nil {
			return err
		}
		if len(r.Waves) > 0 {
			b := &pgx.Batch{}
			for i, w := range r.Waves {
				sel, err := jsonArg(w.Selector, "")
				if err != nil {
					return err
				}
				ws := string(w.Status)
				if ws == "" {
					ws = string(domain.WavePending)
				}
				idx := w.Index
				if idx == 0 && i != 0 {
					idx = i
				}
				b.Queue(`INSERT INTO rollout_waves (rollout_id, idx, name, selector, percent, require_approval, status, approved_by, approved_at, started_at, finished_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
					r.ID, idx, w.Name, sel, w.Percent, w.RequireApproval, ws, w.ApprovedBy,
					timePtrArg(w.ApprovedAt), timePtrArg(w.StartedAt), timePtrArg(w.FinishedAt))
			}
			br := q.SendBatch(ctx, b)
			for range r.Waves {
				if _, err := br.Exec(); err != nil {
					_ = br.Close()
					return err
				}
			}
			if err := br.Close(); err != nil {
				return err
			}
		}
		return insertTargets(ctx, q, r.ID, targets, false)
	})
	return dbErr("rollouts.create", err)
}

const rolloutColumns = `r.id, r.kind, r.fleet_name, r.policy_name, r.status, r.status_reason, r.from_ref, r.to_ref, r.git_commit,
  r.executor, r.current_wave, r.created_by, r.created_at, r.started_at, r.updated_at, r.finished_at,
  COALESCE(r.annotations->'selection'->>'reason', '')`

func scanRollout(r rowScanner, extra ...any) (*domain.Rollout, error) {
	var (
		ro                     domain.Rollout
		kind, status, executor string
		started, finished      *time.Time
	)
	dest := append([]any{&ro.ID, &kind, &ro.FleetName, &ro.PolicyName, &status, &ro.StatusReason, &ro.FromRef, &ro.ToRef, &ro.GitCommit,
		&executor, &ro.CurrentWave, &ro.CreatedBy, &ro.CreatedAt, &started, &ro.UpdatedAt, &finished, &ro.RequestReason}, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	ro.Kind, ro.Status, ro.Executor = domain.RolloutKind(kind), domain.RolloutStatus(status), domain.ExecutorType(executor)
	ro.Origin = domain.OriginOf(ro.CreatedBy)
	ro.CreatedAt, ro.UpdatedAt = ro.CreatedAt.UTC(), ro.UpdatedAt.UTC()
	ro.StartedAt, ro.FinishedAt = utcPtr(started), utcPtr(finished)
	ro.Waves = []domain.RolloutWave{}
	return &ro, nil
}

// loadWavesAndCounts fills Waves and Counts for the given rollouts.
func (s *rolloutStore) loadWavesAndCounts(ctx context.Context, rollouts []*domain.Rollout) error {
	if len(rollouts) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(rollouts))
	byID := make(map[uuid.UUID]*domain.Rollout, len(rollouts))
	for i, r := range rollouts {
		ids[i], byID[r.ID] = r.ID, r
	}
	rows, err := s.db.q.Query(ctx, `SELECT rollout_id, idx, name, selector, percent, require_approval, status, approved_by, approved_at, started_at, finished_at
FROM rollout_waves WHERE rollout_id = ANY($1) ORDER BY rollout_id, idx`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			id                          uuid.UUID
			w                           domain.RolloutWave
			sel                         []byte
			status                      string
			approved, started, finished *time.Time
		)
		if err := rows.Scan(&id, &w.Index, &w.Name, &sel, &w.Percent, &w.RequireApproval, &status, &w.ApprovedBy, &approved, &started, &finished); err != nil {
			rows.Close()
			return err
		}
		w.Status = domain.WaveStatus(status)
		w.ApprovedAt, w.StartedAt, w.FinishedAt = utcPtr(approved), utcPtr(started), utcPtr(finished)
		if len(sel) > 0 {
			var sl domain.Selector
			if err := decodeJSON(sel, &sl); err != nil {
				rows.Close()
				return fmt.Errorf("decode wave selector: %w", err)
			}
			w.Selector = &sl
		}
		if r := byID[id]; r != nil {
			r.Waves = append(r.Waves, w)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	crows, err := s.db.q.Query(ctx, `SELECT rollout_id, wave, status, count(*) FROM rollout_targets WHERE rollout_id = ANY($1) GROUP BY rollout_id, wave, status`, ids)
	if err != nil {
		return err
	}
	defer crows.Close()
	for crows.Next() {
		var (
			id     uuid.UUID
			wave   int
			status string
			n      int
		)
		if err := crows.Scan(&id, &wave, &status, &n); err != nil {
			return err
		}
		r := byID[id]
		if r == nil {
			continue
		}
		addCount(&r.Counts, status, n)
		for i := range r.Waves {
			if r.Waves[i].Index == wave {
				addCount(&r.Waves[i].Counts, status, n)
			}
		}
	}
	return crows.Err()
}

func addCount(c *domain.RolloutCounts, status string, n int) {
	c.Total += n
	switch domain.TargetStatus(status) {
	case domain.TargetPending:
		c.Pending += n
	case domain.TargetWaitingWindow:
		c.WaitingWindow += n
	case domain.TargetInProgress, domain.TargetVerifying:
		c.InProgress += n
	case domain.TargetSucceeded:
		c.Succeeded += n
	case domain.TargetFailed:
		c.Failed += n
	case domain.TargetSkipped:
		c.Skipped += n
	case domain.TargetCancelled:
		c.Cancelled += n
	case domain.TargetRolledBack:
		c.RolledBack += n
	}
}

// Get implements store.RolloutStore.
func (s *rolloutStore) Get(ctx context.Context, id uuid.UUID) (*domain.Rollout, error) {
	var policy, annotations []byte
	r, err := scanRollout(s.db.q.QueryRow(ctx, `SELECT `+rolloutColumns+`, r.policy, r.annotations FROM rollouts r WHERE r.id = $1`, id), &policy, &annotations)
	if err != nil {
		return nil, dbErr("rollouts.get", err)
	}
	if err := decodeJSON(policy, &r.Policy); err != nil {
		return nil, fmt.Errorf("decode rollout policy: %w", err)
	}
	if len(annotations) > 0 {
		if err := decodeJSON(annotations, &r.Annotations); err != nil {
			return nil, fmt.Errorf("decode rollout annotations: %w", err)
		}
	}
	if err := s.loadWavesAndCounts(ctx, []*domain.Rollout{r}); err != nil {
		return nil, dbErr("rollouts.get", err)
	}
	return r, nil
}

// List implements store.RolloutStore.
func (s *rolloutStore) List(ctx context.Context, f store.RolloutFilter) ([]domain.Rollout, int, error) {
	var (
		args    argList
		clauses []string
	)
	if len(f.Fleets) > 0 {
		clauses = append(clauses, "r.fleet_name = ANY("+args.add(f.Fleets)+")")
	}
	if len(f.Kinds) > 0 {
		ks := make([]string, len(f.Kinds))
		for i, k := range f.Kinds {
			ks[i] = string(k)
		}
		clauses = append(clauses, "r.kind = ANY("+args.add(ks)+")")
	}
	if len(f.Status) > 0 {
		ss := make([]string, len(f.Status))
		for i, st := range f.Status {
			ss[i] = string(st)
		}
		clauses = append(clauses, "r.status = ANY("+args.add(ss)+")")
	}
	if f.ActiveOnly {
		clauses = append(clauses, "r.status NOT IN "+terminalRolloutStatusesSQL)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := max(f.Offset, 0)
	sql := `SELECT ` + rolloutColumns + `, count(*) OVER () FROM rollouts r` + where +
		` ORDER BY r.created_at DESC, r.id LIMIT ` + args.add(limit) + ` OFFSET ` + args.add(offset)
	rows, err := s.db.q.Query(ctx, sql, args.args...)
	if err != nil {
		return nil, 0, dbErr("rollouts.list", err)
	}
	var (
		list  []*domain.Rollout
		total int
	)
	for rows.Next() {
		r, err := scanRollout(rows, &total)
		if err != nil {
			rows.Close()
			return nil, 0, dbErr("rollouts.list scan", err)
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, dbErr("rollouts.list", err)
	}
	if len(list) == 0 && offset > 0 {
		if err := s.db.q.QueryRow(ctx, "SELECT count(*) FROM rollouts r"+where, args.args[:len(args.args)-2]...).Scan(&total); err != nil {
			return nil, 0, dbErr("rollouts.list count", err)
		}
	}
	if err := s.loadWavesAndCounts(ctx, list); err != nil {
		return nil, 0, dbErr("rollouts.list", err)
	}
	out := make([]domain.Rollout, len(list))
	for i, r := range list {
		out[i] = *r
	}
	return out, total, nil
}

// UpdateProgress implements store.RolloutStore.
func (s *rolloutStore) UpdateProgress(ctx context.Context, r *domain.Rollout) error {
	annotations, err := jsonArg(r.Annotations, "")
	if err != nil {
		return err
	}
	err = s.db.withTx(ctx, func(q querier) error {
		tag, err := q.Exec(ctx, `UPDATE rollouts SET current_wave = $2, annotations = $3, updated_at = now() WHERE id = $1`, r.ID, r.CurrentWave, annotations)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: rollout %s", domain.ErrNotFound, r.ID)
		}
		if len(r.Waves) == 0 {
			return nil
		}
		b := &pgx.Batch{}
		for _, w := range r.Waves {
			b.Queue(`UPDATE rollout_waves SET status = $3, started_at = $4, finished_at = $5 WHERE rollout_id = $1 AND idx = $2`,
				r.ID, w.Index, string(w.Status), timePtrArg(w.StartedAt), timePtrArg(w.FinishedAt))
		}
		br := q.SendBatch(ctx, b)
		for range r.Waves {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		return br.Close()
	})
	return dbErr("rollouts.updateProgress", err)
}

// CompareAndSetStatus implements store.RolloutStore.
func (s *rolloutStore) CompareAndSetStatus(ctx context.Context, id uuid.UUID, from []domain.RolloutStatus, to domain.RolloutStatus, reason string) (bool, error) {
	if len(from) == 0 {
		return false, nil
	}
	fs := make([]string, len(from))
	for i, f := range from {
		fs[i] = string(f)
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE rollouts SET status = $3, status_reason = $4,
    started_at = CASE WHEN $3 = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    finished_at = CASE WHEN $5::boolean THEN now() ELSE finished_at END,
    updated_at = now()
WHERE id = $1 AND status = ANY($2)`, id, fs, string(to), cleanText(reason), to.IsTerminal())
	if err != nil {
		return false, dbErr("rollouts.compareAndSetStatus", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetWaveApproval implements store.RolloutStore.
func (s *rolloutStore) SetWaveApproval(ctx context.Context, id uuid.UUID, wave int, by string, at time.Time) (bool, error) {
	tag, err := s.db.q.Exec(ctx, `UPDATE rollout_waves SET approved_by = $3, approved_at = $4
WHERE rollout_id = $1 AND idx = $2 AND status IN ('pending', 'waiting_approval')`, id, wave, by, at.UTC())
	if err != nil {
		return false, dbErr("rollouts.setWaveApproval", err)
	}
	if tag.RowsAffected() > 0 {
		_, _ = s.db.q.Exec(ctx, `UPDATE rollouts SET updated_at = now() WHERE id = $1`, id)
	}
	return tag.RowsAffected() > 0, nil
}

const targetSelect = `t.rollout_id, t.agent_id, t.hostname, t.wave, t.status, t.from_ref, t.to_ref, t.attempts,
  t.started_at, t.verified_at, t.finished_at, t.last_error, t.executor_ref, t.updated_at, t.progress`

func scanTarget(r rowScanner, extra ...any) (domain.RolloutTarget, error) {
	var (
		t                           domain.RolloutTarget
		status                      string
		started, verified, finished *time.Time
		progress                    []byte
	)
	dest := append([]any{&t.RolloutID, &t.AgentID, &t.Hostname, &t.Wave, &status, &t.FromRef, &t.ToRef, &t.Attempts,
		&started, &verified, &finished, &t.LastError, &t.ExecutorRef, &t.UpdatedAt, &progress}, extra...)
	if err := r.Scan(dest...); err != nil {
		return t, err
	}
	t.Status, t.UpdatedAt = domain.TargetStatus(status), t.UpdatedAt.UTC()
	t.StartedAt, t.VerifiedAt, t.FinishedAt = utcPtr(started), utcPtr(verified), utcPtr(finished)
	if len(progress) > 0 {
		var p domain.TargetProgress
		if err := decodeJSON(progress, &p); err != nil {
			return t, fmt.Errorf("decode target progress: %w", err)
		}
		if p.Steps == nil {
			p.Steps = []domain.UpdateStep{}
		}
		t.Progress = &p
	}
	return t, nil
}

// targetOrderSQL returns the ORDER BY expression for a target order. The CASE
// literals are generated from the domain statuses and ranks, never from user
// input, and the expression adds no bind parameter (ListTargets relies on the
// argument positions for its count fallback).
func targetOrderSQL(o store.TargetOrder) string {
	const stable = "t.wave, t.hostname, t.agent_id"
	if o != store.TargetOrderActivity {
		return stable
	}
	var b strings.Builder
	b.WriteString("CASE t.status")
	for _, st := range domain.AllTargetStatuses() {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", st, st.ActivityRank())
	}
	fmt.Fprintf(&b, " ELSE %d END, %s", domain.UnknownActivityRank, stable)
	return b.String()
}

// ListTargets implements store.RolloutStore (Limit 0 = all targets).
func (s *rolloutStore) ListTargets(ctx context.Context, id uuid.UUID, f store.TargetFilter) ([]domain.RolloutTarget, int, error) {
	var args argList
	clauses := []string{"t.rollout_id = " + args.add(id)}
	if f.Wave != nil {
		clauses = append(clauses, "t.wave = "+args.add(*f.Wave))
	}
	if len(f.Status) > 0 {
		ss := make([]string, len(f.Status))
		for i, st := range f.Status {
			ss[i] = string(st)
		}
		clauses = append(clauses, "t.status = ANY("+args.add(ss)+")")
	}
	if f.Agent != nil {
		clauses = append(clauses, "t.agent_id = "+args.add(*f.Agent))
	}
	where := " WHERE " + strings.Join(clauses, " AND ")
	sql := `SELECT ` + targetSelect + `, count(*) OVER () FROM rollout_targets t` + where + ` ORDER BY ` + targetOrderSQL(f.Order)
	if f.Limit > 0 {
		sql += " LIMIT " + args.add(f.Limit)
	}
	if f.Offset > 0 {
		sql += " OFFSET " + args.add(f.Offset)
	}
	rows, err := s.db.q.Query(ctx, sql, args.args...)
	if err != nil {
		return nil, 0, dbErr("rollouts.listTargets", err)
	}
	defer rows.Close()
	out := []domain.RolloutTarget{}
	total := 0
	for rows.Next() {
		t, err := scanTarget(rows, &total)
		if err != nil {
			return nil, 0, dbErr("rollouts.listTargets scan", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dbErr("rollouts.listTargets", err)
	}
	if len(out) == 0 && f.Offset > 0 {
		cnt := args.args[:len(args.args)-1]
		if f.Limit > 0 {
			cnt = args.args[:len(args.args)-2]
		}
		if err := s.db.q.QueryRow(ctx, "SELECT count(*) FROM rollout_targets t"+where, cnt...).Scan(&total); err != nil {
			return nil, 0, dbErr("rollouts.listTargets count", err)
		}
	}
	return out, total, nil
}

// UpdateTargets implements store.RolloutStore (optimistic concurrency on updated_at).
func (s *rolloutStore) UpdateTargets(ctx context.Context, targets []domain.RolloutTarget) ([]domain.AgentID, error) {
	conflicts := []domain.AgentID{}
	if len(targets) == 0 {
		return conflicts, nil
	}
	err := s.db.withTx(ctx, func(q querier) error {
		b := &pgx.Batch{}
		for _, t := range targets {
			progress, err := progressArg(t.Progress)
			if err != nil {
				return err
			}
			b.Queue(`UPDATE rollout_targets SET status = $3, attempts = $4, started_at = $5, verified_at = $6, finished_at = $7,
    last_error = $8, executor_ref = $9, progress = $11, updated_at = clock_timestamp()
WHERE rollout_id = $1 AND agent_id = $2 AND updated_at = $10`,
				t.RolloutID, t.AgentID, string(t.Status), t.Attempts, timePtrArg(t.StartedAt), timePtrArg(t.VerifiedAt), timePtrArg(t.FinishedAt),
				cleanText(t.LastError), t.ExecutorRef, t.UpdatedAt.UTC(), progress)
		}
		br := q.SendBatch(ctx, b)
		defer br.Close()
		for _, t := range targets {
			tag, err := br.Exec()
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				conflicts = append(conflicts, t.AgentID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, dbErr("rollouts.updateTargets", err)
	}
	return conflicts, nil
}

// AddTargets implements store.RolloutStore.
func (s *rolloutStore) AddTargets(ctx context.Context, id uuid.UUID, targets []domain.RolloutTarget) error {
	err := s.db.withTx(ctx, func(q querier) error { return insertTargets(ctx, q, id, targets, true) })
	return dbErr("rollouts.addTargets", err)
}

// CancelOpenTargets implements store.RolloutStore.
func (s *rolloutStore) CancelOpenTargets(ctx context.Context, id uuid.UUID, reason string) (int64, error) {
	tag, err := s.db.q.Exec(ctx, `UPDATE rollout_targets SET status = 'cancelled', last_error = $2, finished_at = now(), updated_at = clock_timestamp()
WHERE rollout_id = $1 AND status IN ('pending', 'waiting_window')`, id, cleanText(reason))
	if err != nil {
		return 0, dbErr("rollouts.cancelOpenTargets", err)
	}
	return tag.RowsAffected(), nil
}

// ResetFailedTargets implements store.RolloutStore.
func (s *rolloutStore) ResetFailedTargets(ctx context.Context, id uuid.UUID) (int64, error) {
	tag, err := s.db.q.Exec(ctx, `UPDATE rollout_targets SET status = 'pending', attempts = attempts + 1, last_error = '',
    started_at = NULL, verified_at = NULL, finished_at = NULL, executor_ref = '', progress = NULL, updated_at = clock_timestamp()
WHERE rollout_id = $1 AND status IN ('failed', 'skipped')`, id)
	if err != nil {
		return 0, dbErr("rollouts.resetFailedTargets", err)
	}
	return tag.RowsAffected(), nil
}

// ActiveTargetsForAgent implements store.RolloutStore.
func (s *rolloutStore) ActiveTargetsForAgent(ctx context.Context, agentID domain.AgentID) ([]domain.RolloutTarget, error) {
	rows, err := s.db.q.Query(ctx, `SELECT `+targetSelect+` FROM rollout_targets t JOIN rollouts r ON r.id = t.rollout_id
WHERE t.agent_id = $1 AND r.status NOT IN `+terminalRolloutStatusesSQL+` ORDER BY t.updated_at DESC`, agentID)
	if err != nil {
		return nil, dbErr("rollouts.activeTargetsForAgent", err)
	}
	defer rows.Close()
	out := []domain.RolloutTarget{}
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, dbErr("rollouts.activeTargetsForAgent scan", err)
		}
		out = append(out, t)
	}
	return out, dbErr("rollouts.activeTargetsForAgent", rows.Err())
}

// LatestTargetForAgent implements store.RolloutStore.
func (s *rolloutStore) LatestTargetForAgent(ctx context.Context, agentID domain.AgentID, kind domain.RolloutKind, toRef string) (*store.TargetRef, error) {
	var rolloutStatus string
	t, err := scanTarget(s.db.q.QueryRow(ctx, `SELECT `+targetSelect+`, r.status FROM rollout_targets t JOIN rollouts r ON r.id = t.rollout_id
WHERE t.agent_id = $1 AND r.kind = $2 AND t.to_ref = $3 ORDER BY r.created_at DESC, t.updated_at DESC LIMIT 1`,
		agentID, string(kind), toRef), &rolloutStatus)
	if err != nil {
		return nil, dbErr("rollouts.latestTargetForAgent", err)
	}
	return &store.TargetRef{Target: t, RolloutStatus: domain.RolloutStatus(rolloutStatus)}, nil
}

// DeleteFinishedBefore implements store.RolloutStore.
func (s *rolloutStore) DeleteFinishedBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.db.execMaintenance(ctx, `DELETE FROM rollouts WHERE status IN `+terminalRolloutStatusesSQL+` AND finished_at < $1`, before.UTC())
	if err != nil {
		return 0, dbErr("rollouts.deleteFinishedBefore", err)
	}
	return n, nil
}
