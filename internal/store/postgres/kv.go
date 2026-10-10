// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"time"

	"github.com/myriarch/myriarch/internal/store"
)

type kvStore struct{ db *DB }

var _ store.KVStore = (*kvStore)(nil)

// Get implements store.KVStore.
func (s *kvStore) Get(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	if err := s.db.q.QueryRow(ctx, `SELECT value FROM kv WHERE key = $1`, key).Scan(&v); err != nil {
		return nil, dbErr("kv.get", err)
	}
	if v == nil {
		v = []byte{}
	}
	return v, nil
}

// Put implements store.KVStore.
func (s *kvStore) Put(ctx context.Context, key string, value []byte) error {
	if value == nil {
		value = []byte{}
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO kv (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return dbErr("kv.put", err)
}

// PutIfAbsent implements store.KVStore.
func (s *kvStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if value == nil {
		value = []byte{}
	}
	tag, err := s.db.q.Exec(ctx, `INSERT INTO kv (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, key, value)
	if err != nil {
		return false, dbErr("kv.putIfAbsent", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteOlderThan implements store.KVStore.
func (s *kvStore) DeleteOlderThan(ctx context.Context, prefix string, before time.Time) (int64, error) {
	tag, err := s.db.q.Exec(ctx, `DELETE FROM kv WHERE starts_with(key, $1) AND updated_at < $2`, prefix, before.UTC())
	if err != nil {
		return 0, dbErr("kv.deleteOlderThan", err)
	}
	return tag.RowsAffected(), nil
}

// Delete implements store.KVStore.
func (s *kvStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.q.Exec(ctx, `DELETE FROM kv WHERE key = $1`, key)
	return dbErr("kv.delete", err)
}
