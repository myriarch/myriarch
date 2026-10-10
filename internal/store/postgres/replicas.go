// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type replicaStore struct{ db *DB }

var _ store.ReplicaStore = (*replicaStore)(nil)

// Heartbeat implements store.ReplicaStore. A zero LastHeartbeatAt uses the
// database clock (preferred: avoids clock skew between replicas).
func (s *replicaStore) Heartbeat(ctx context.Context, r domain.Replica) error {
	started := r.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO replicas (id, hostname, version, started_at, last_heartbeat_at, is_leader, connections)
VALUES ($1, $2, $3, $4, COALESCE($5, now()), $6, $7)
ON CONFLICT (id) DO UPDATE SET hostname = EXCLUDED.hostname, version = EXCLUDED.version,
    last_heartbeat_at = EXCLUDED.last_heartbeat_at, is_leader = EXCLUDED.is_leader, connections = EXCLUDED.connections`,
		r.ID, r.Hostname, r.Version, started.UTC(), timeArg(r.LastHeartbeatAt), r.IsLeader, r.Connections)
	return dbErr("replicas.heartbeat", err)
}

// List implements store.ReplicaStore.
func (s *replicaStore) List(ctx context.Context) ([]domain.Replica, error) {
	rows, err := s.db.q.Query(ctx, `SELECT id, hostname, version, started_at, last_heartbeat_at, is_leader, connections FROM replicas ORDER BY id`)
	if err != nil {
		return nil, dbErr("replicas.list", err)
	}
	defer rows.Close()
	out := []domain.Replica{}
	for rows.Next() {
		var r domain.Replica
		if err := rows.Scan(&r.ID, &r.Hostname, &r.Version, &r.StartedAt, &r.LastHeartbeatAt, &r.IsLeader, &r.Connections); err != nil {
			return nil, dbErr("replicas.list scan", err)
		}
		r.StartedAt, r.LastHeartbeatAt = r.StartedAt.UTC(), r.LastHeartbeatAt.UTC()
		out = append(out, r)
	}
	return out, dbErr("replicas.list", rows.Err())
}

// DeleteStale implements store.ReplicaStore.
func (s *replicaStore) DeleteStale(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.db.q.Query(ctx, `DELETE FROM replicas WHERE last_heartbeat_at < $1 RETURNING id`, before.UTC())
	if err != nil {
		return nil, dbErr("replicas.deleteStale", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbErr("replicas.deleteStale scan", err)
		}
		out = append(out, id)
	}
	return out, dbErr("replicas.deleteStale", rows.Err())
}
