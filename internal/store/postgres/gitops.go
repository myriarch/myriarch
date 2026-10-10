// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type gitopsStore struct{ db *DB }

var _ store.GitOpsStore = (*gitopsStore)(nil)

type snapshotObject struct {
	kind   domain.ObjectKind
	name   string
	object []byte
	source domain.SourceRef
	hash   []byte
}

func (o snapshotObject) key() string { return string(o.kind) + "/" + o.name }

// snapshotObjects flattens a snapshot into rows.
func snapshotObjects(snap domain.GitOpsSnapshot) ([]snapshotObject, error) {
	var out []snapshotObject
	add := func(kind domain.ObjectKind, name string, src domain.SourceRef, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal %s/%s: %w", kind, name, err)
		}
		h := []byte(src.ContentHash)
		if len(h) == 0 {
			h = []byte(domain.SHA256(b))
		}
		if src.Commit == "" {
			src.Commit = snap.Commit
		}
		out = append(out, snapshotObject{kind: kind, name: name, object: b, source: src, hash: h})
		return nil
	}
	for _, o := range snap.Fleets {
		if err := add(domain.KindFleet, o.Name, o.Source, o); err != nil {
			return nil, err
		}
	}
	for _, o := range snap.Blueprints {
		if err := add(domain.KindBlueprint, o.Name, o.Source, o); err != nil {
			return nil, err
		}
	}
	for _, o := range snap.RolloutPolicies {
		if err := add(domain.KindRolloutPolicy, o.Name, o.Source, o); err != nil {
			return nil, err
		}
	}
	for _, o := range snap.Executors {
		if err := add(domain.KindExecutor, o.Name, o.Source, o); err != nil {
			return nil, err
		}
	}
	for _, o := range snap.Inventories {
		if err := add(domain.KindInventory, o.Name, o.Source, o); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	for _, o := range out {
		if o.name == "" {
			return nil, fmt.Errorf("%w: %s object without name", domain.ErrInvalidArgument, o.kind)
		}
		if seen[o.key()] {
			return nil, fmt.Errorf("%w: duplicate object %s", domain.ErrInvalidArgument, o.key())
		}
		seen[o.key()] = true
	}
	return out, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ApplySnapshot implements store.GitOpsStore.
func (s *gitopsStore) ApplySnapshot(ctx context.Context, snap domain.GitOpsSnapshot, sync domain.GitOpsSync) (store.ObjectChanges, error) {
	objs, err := snapshotObjects(snap)
	if err != nil {
		return store.ObjectChanges{}, err
	}
	changes := store.ObjectChanges{Added: []string{}, Changed: []string{}, Removed: []string{}}
	err = s.db.withTx(ctx, func(q querier) error {
		// serialize concurrent appliers (only the leader writes, but be safe)
		if _, err := q.Exec(ctx, `LOCK TABLE gitops_objects IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		rows, err := q.Query(ctx, `SELECT kind, name, content_hash FROM gitops_objects`)
		if err != nil {
			return err
		}
		existing := map[string][]byte{}
		for rows.Next() {
			var kind, name string
			var h []byte
			if err := rows.Scan(&kind, &name, &h); err != nil {
				rows.Close()
				return err
			}
			existing[kind+"/"+name] = h
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		b := &pgx.Batch{}
		keep := map[string]bool{}
		for _, o := range objs {
			keep[o.key()] = true
			old, ok := existing[o.key()]
			switch {
			case !ok:
				changes.Added = append(changes.Added, o.key())
			case string(old) != string(o.hash):
				changes.Changed = append(changes.Changed, o.key())
			}
			b.Queue(`INSERT INTO gitops_objects (kind, name, object, source_path, commit_sha, content_hash)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (kind, name) DO UPDATE SET object = EXCLUDED.object, source_path = EXCLUDED.source_path,
    commit_sha = EXCLUDED.commit_sha, content_hash = EXCLUDED.content_hash, updated_at = now()`,
				string(o.kind), o.name, o.object, o.source.Path, o.source.Commit, o.hash)
		}
		for k := range existing {
			if !keep[k] {
				changes.Removed = append(changes.Removed, k)
			}
		}
		sort.Strings(changes.Added)
		sort.Strings(changes.Changed)
		sort.Strings(changes.Removed)
		for _, k := range changes.Removed {
			var kind, name string
			for i := range k {
				if k[i] == '/' {
					kind, name = k[:i], k[i+1:]
					break
				}
			}
			b.Queue(`DELETE FROM gitops_objects WHERE kind = $1 AND name = $2`, kind, name)
		}
		br := q.SendBatch(ctx, b)
		for i := 0; i < b.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}

		rec := sync
		rec.Added, rec.Changed, rec.Removed = changes.Added, changes.Changed, changes.Removed
		if rec.Result == "" {
			rec.Result = "applied"
		}
		if rec.Commit == nil && snap.Commit != "" {
			rec.Commit = &domain.GitCommit{SHA: snap.Commit}
		}
		_, err = insertSync(ctx, q, rec)
		return err
	})
	if err != nil {
		return store.ObjectChanges{}, dbErr("gitops.applySnapshot", err)
	}
	return changes, nil
}

func insertSync(ctx context.Context, q querier, rec domain.GitOpsSync) (int64, error) {
	started := rec.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	finished := rec.FinishedAt
	if finished == nil {
		now := time.Now()
		finished = &now
	}
	commitSHA := ""
	var commit any
	if rec.Commit != nil {
		commitSHA = rec.Commit.SHA
		b, err := json.Marshal(rec.Commit)
		if err != nil {
			return 0, err
		}
		commit = b
	}
	issues := rec.Issues
	if issues == nil {
		issues = domain.Issues{}
	}
	ib, err := json.Marshal(issues)
	if err != nil {
		return 0, err
	}
	enc := func(l []string) []byte { b, _ := json.Marshal(nonNilStrings(l)); return b }
	trigger := rec.Trigger
	if trigger == "" {
		trigger = "poll"
	}
	var id int64
	err = q.QueryRow(ctx, `INSERT INTO gitops_syncs (started_at, finished_at, trigger, commit_sha, commit, result, issues, added, changed, removed)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		started.UTC(), finished.UTC(), trigger, commitSHA, commit, rec.Result, ib, enc(rec.Added), enc(rec.Changed), enc(rec.Removed)).Scan(&id)
	return id, err
}

// GetSnapshot implements store.GitOpsStore.
func (s *gitopsStore) GetSnapshot(ctx context.Context) (*domain.GitOpsSnapshot, error) {
	snap := &domain.GitOpsSnapshot{
		Fleets: []domain.Fleet{}, Blueprints: []domain.Blueprint{}, RolloutPolicies: []domain.RolloutPolicy{},
		Executors: []domain.Executor{}, Inventories: []domain.Inventory{},
	}
	rows, err := s.db.q.Query(ctx, `SELECT kind, object FROM gitops_objects ORDER BY kind, name`)
	if err != nil {
		return nil, dbErr("gitops.getSnapshot", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var b []byte
		if err := rows.Scan(&kind, &b); err != nil {
			return nil, dbErr("gitops.getSnapshot scan", err)
		}
		switch domain.ObjectKind(kind) {
		case domain.KindFleet:
			var o domain.Fleet
			if err := decodeJSON(b, &o); err != nil {
				return nil, fmt.Errorf("decode Fleet: %w", err)
			}
			snap.Fleets = append(snap.Fleets, o)
		case domain.KindBlueprint:
			var o domain.Blueprint
			if err := decodeJSON(b, &o); err != nil {
				return nil, fmt.Errorf("decode Blueprint: %w", err)
			}
			snap.Blueprints = append(snap.Blueprints, o)
		case domain.KindRolloutPolicy:
			var o domain.RolloutPolicy
			if err := decodeJSON(b, &o); err != nil {
				return nil, fmt.Errorf("decode RolloutPolicy: %w", err)
			}
			snap.RolloutPolicies = append(snap.RolloutPolicies, o)
		case domain.KindExecutor:
			var o domain.Executor
			if err := decodeJSON(b, &o); err != nil {
				return nil, fmt.Errorf("decode Executor: %w", err)
			}
			snap.Executors = append(snap.Executors, o)
		case domain.KindInventory:
			var o domain.Inventory
			if err := decodeJSON(b, &o); err != nil {
				return nil, fmt.Errorf("decode Inventory: %w", err)
			}
			snap.Inventories = append(snap.Inventories, o)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr("gitops.getSnapshot", err)
	}
	rows.Close()
	err = s.db.q.QueryRow(ctx, `SELECT commit_sha FROM gitops_syncs WHERE result = 'applied' ORDER BY id DESC LIMIT 1`).Scan(&snap.Commit)
	if err != nil && !isNotFound(err) {
		return nil, dbErr("gitops.getSnapshot commit", err)
	}
	return snap, nil
}

func isNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func (s *gitopsStore) getObject(ctx context.Context, kind domain.ObjectKind, name string, v any) error {
	var b []byte
	if err := s.db.q.QueryRow(ctx, `SELECT object FROM gitops_objects WHERE kind = $1 AND name = $2`, string(kind), name).Scan(&b); err != nil {
		return dbErr("gitops.get"+string(kind), err)
	}
	if err := decodeJSON(b, v); err != nil {
		return fmt.Errorf("decode %s/%s: %w", kind, name, err)
	}
	return nil
}

func (s *gitopsStore) listObjects(ctx context.Context, kind domain.ObjectKind, each func(b []byte) error) error {
	rows, err := s.db.q.Query(ctx, `SELECT object FROM gitops_objects WHERE kind = $1 ORDER BY name`, string(kind))
	if err != nil {
		return dbErr("gitops.list"+string(kind), err)
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return dbErr("gitops.list"+string(kind), err)
		}
		if err := each(b); err != nil {
			return err
		}
	}
	return dbErr("gitops.list"+string(kind), rows.Err())
}

// ListFleets implements store.GitOpsStore.
func (s *gitopsStore) ListFleets(ctx context.Context) ([]domain.Fleet, error) {
	out := []domain.Fleet{}
	err := s.listObjects(ctx, domain.KindFleet, func(b []byte) error {
		var o domain.Fleet
		if err := decodeJSON(b, &o); err != nil {
			return fmt.Errorf("decode Fleet: %w", err)
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

// GetFleet implements store.GitOpsStore.
func (s *gitopsStore) GetFleet(ctx context.Context, name string) (*domain.Fleet, error) {
	var o domain.Fleet
	if err := s.getObject(ctx, domain.KindFleet, name, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListBlueprints implements store.GitOpsStore.
func (s *gitopsStore) ListBlueprints(ctx context.Context) ([]domain.Blueprint, error) {
	out := []domain.Blueprint{}
	err := s.listObjects(ctx, domain.KindBlueprint, func(b []byte) error {
		var o domain.Blueprint
		if err := decodeJSON(b, &o); err != nil {
			return fmt.Errorf("decode Blueprint: %w", err)
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

// GetBlueprint implements store.GitOpsStore.
func (s *gitopsStore) GetBlueprint(ctx context.Context, name string) (*domain.Blueprint, error) {
	var o domain.Blueprint
	if err := s.getObject(ctx, domain.KindBlueprint, name, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListRolloutPolicies implements store.GitOpsStore.
func (s *gitopsStore) ListRolloutPolicies(ctx context.Context) ([]domain.RolloutPolicy, error) {
	out := []domain.RolloutPolicy{}
	err := s.listObjects(ctx, domain.KindRolloutPolicy, func(b []byte) error {
		var o domain.RolloutPolicy
		if err := decodeJSON(b, &o); err != nil {
			return fmt.Errorf("decode RolloutPolicy: %w", err)
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

// GetRolloutPolicy implements store.GitOpsStore.
func (s *gitopsStore) GetRolloutPolicy(ctx context.Context, name string) (*domain.RolloutPolicy, error) {
	var o domain.RolloutPolicy
	if err := s.getObject(ctx, domain.KindRolloutPolicy, name, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListExecutors implements store.GitOpsStore.
func (s *gitopsStore) ListExecutors(ctx context.Context) ([]domain.Executor, error) {
	out := []domain.Executor{}
	err := s.listObjects(ctx, domain.KindExecutor, func(b []byte) error {
		var o domain.Executor
		if err := decodeJSON(b, &o); err != nil {
			return fmt.Errorf("decode Executor: %w", err)
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

// GetExecutor implements store.GitOpsStore.
func (s *gitopsStore) GetExecutor(ctx context.Context, name string) (*domain.Executor, error) {
	var o domain.Executor
	if err := s.getObject(ctx, domain.KindExecutor, name, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListInventories implements store.GitOpsStore.
func (s *gitopsStore) ListInventories(ctx context.Context) ([]domain.Inventory, error) {
	out := []domain.Inventory{}
	err := s.listObjects(ctx, domain.KindInventory, func(b []byte) error {
		var o domain.Inventory
		if err := decodeJSON(b, &o); err != nil {
			return fmt.Errorf("decode Inventory: %w", err)
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

// RecordSync implements store.GitOpsStore.
func (s *gitopsStore) RecordSync(ctx context.Context, sync domain.GitOpsSync) (int64, error) {
	if sync.Result == "" {
		sync.Result = "unchanged"
	}
	id, err := insertSync(ctx, s.db.q, sync)
	if err != nil {
		return 0, dbErr("gitops.recordSync", err)
	}
	return id, nil
}

// ListSyncs implements store.GitOpsStore.
func (s *gitopsStore) ListSyncs(ctx context.Context, limit, offset int) ([]domain.GitOpsSync, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset = max(offset, 0)
	rows, err := s.db.q.Query(ctx, `SELECT id, started_at, finished_at, trigger, commit, result, issues, added, changed, removed, count(*) OVER ()
FROM gitops_syncs ORDER BY started_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, dbErr("gitops.listSyncs", err)
	}
	defer rows.Close()
	out := []domain.GitOpsSync{}
	total := 0
	for rows.Next() {
		var (
			g                               domain.GitOpsSync
			finished                        *time.Time
			commit, issues, added, chg, rem []byte
		)
		if err := rows.Scan(&g.ID, &g.StartedAt, &finished, &g.Trigger, &commit, &g.Result, &issues, &added, &chg, &rem, &total); err != nil {
			return nil, 0, dbErr("gitops.listSyncs scan", err)
		}
		g.StartedAt, g.FinishedAt = g.StartedAt.UTC(), utcPtr(finished)
		g.Issues = domain.Issues{}
		if len(commit) > 0 {
			var c domain.GitCommit
			if err := decodeJSON(commit, &c); err != nil {
				return nil, 0, fmt.Errorf("decode commit: %w", err)
			}
			c.Time = c.Time.UTC()
			g.Commit = &c
		}
		if err := decodeJSON(issues, &g.Issues); err != nil {
			return nil, 0, fmt.Errorf("decode issues: %w", err)
		}
		if g.Issues == nil {
			g.Issues = domain.Issues{}
		}
		for _, p := range []struct {
			raw []byte
			dst *[]string
		}{{added, &g.Added}, {chg, &g.Changed}, {rem, &g.Removed}} {
			if err := decodeJSON(p.raw, p.dst); err != nil {
				return nil, 0, fmt.Errorf("decode change list: %w", err)
			}
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dbErr("gitops.listSyncs", err)
	}
	if len(out) == 0 && offset > 0 {
		if err := s.db.q.QueryRow(ctx, `SELECT count(*) FROM gitops_syncs`).Scan(&total); err != nil {
			return nil, 0, dbErr("gitops.listSyncs count", err)
		}
	}
	return out, total, nil
}

// DeleteSyncsBefore implements store.GitOpsStore.
func (s *gitopsStore) DeleteSyncsBefore(ctx context.Context, before time.Time) (int64, error) {
	// never delete the newest applied sync: GetSnapshot derives the commit from it
	n, err := s.db.execMaintenance(ctx, `DELETE FROM gitops_syncs WHERE started_at < $1
AND id <> COALESCE((SELECT max(id) FROM gitops_syncs WHERE result = 'applied'), 0)`, before.UTC())
	if err != nil {
		return 0, dbErr("gitops.deleteSyncsBefore", err)
	}
	return n, nil
}

// SaveStatus implements store.GitOpsStore.
func (s *gitopsStore) SaveStatus(ctx context.Context, st domain.GitOpsSyncStatus) error {
	if st.Issues == nil {
		st.Issues = domain.Issues{}
	}
	if st.ObjectCounts == nil {
		st.ObjectCounts = map[domain.ObjectKind]int{}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	_, err = s.db.q.Exec(ctx, `INSERT INTO gitops_status (id, status) VALUES (1, $1)
ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, updated_at = now()`, b)
	return dbErr("gitops.saveStatus", err)
}

// GetStatus implements store.GitOpsStore.
func (s *gitopsStore) GetStatus(ctx context.Context) (*domain.GitOpsSyncStatus, error) {
	var b []byte
	err := s.db.q.QueryRow(ctx, `SELECT status FROM gitops_status WHERE id = 1`).Scan(&b)
	st := domain.GitOpsSyncStatus{State: "never", Issues: domain.Issues{}, ObjectCounts: map[domain.ObjectKind]int{}}
	if err != nil {
		if isNotFound(err) {
			return &st, nil
		}
		return nil, dbErr("gitops.getStatus", err)
	}
	if err := decodeJSON(b, &st); err != nil {
		return nil, fmt.Errorf("decode gitops status: %w", err)
	}
	if st.Issues == nil {
		st.Issues = domain.Issues{}
	}
	if st.ObjectCounts == nil {
		st.ObjectCounts = map[domain.ObjectKind]int{}
	}
	if st.LastSyncAt != nil {
		st.LastSyncAt = utcPtr(st.LastSyncAt)
	}
	if st.LastSuccessAt != nil {
		st.LastSuccessAt = utcPtr(st.LastSuccessAt)
	}
	return &st, nil
}
