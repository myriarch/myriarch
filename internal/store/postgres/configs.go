// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type configStore struct{ db *DB }

var _ store.ConfigStore = (*configStore)(nil)

func defaultContentType(ct string) string {
	if ct == "" {
		return "text/yaml"
	}
	return ct
}

// PutRendered implements store.ConfigStore.
func (s *configStore) PutRendered(ctx context.Context, c domain.RenderedConfig) error {
	hash := c.Hash
	if len(hash) == 0 {
		hash = domain.ConfigHash(c.Body)
	}
	created := c.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO rendered_configs (hash, body, content_type, fleet_name, blueprint_name, git_commit, size, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (hash) DO UPDATE SET touched_at = now()`,
		[]byte(hash), c.Body, defaultContentType(c.ContentType), c.FleetName, c.BlueprintName, c.GitCommit, len(c.Body), created.UTC())
	return dbErr("configs.putRendered", err)
}

// GetRendered implements store.ConfigStore.
func (s *configStore) GetRendered(ctx context.Context, hash domain.Hash) (*domain.RenderedConfig, error) {
	var (
		c    domain.RenderedConfig
		h    []byte
		size int
	)
	err := s.db.q.QueryRow(ctx, `SELECT hash, body, content_type, fleet_name, blueprint_name, git_commit, size, created_at
FROM rendered_configs WHERE hash = $1`, []byte(hash)).Scan(&h, &c.Body, &c.ContentType, &c.FleetName, &c.BlueprintName, &c.GitCommit, &size, &c.CreatedAt)
	if err != nil {
		return nil, dbErr("configs.getRendered", err)
	}
	c.Hash, c.Size, c.CreatedAt = domain.Hash(h), size, c.CreatedAt.UTC()
	return &c, nil
}

// PutEffective implements store.ConfigStore.
func (s *configStore) PutEffective(ctx context.Context, c domain.EffectiveConfig) error {
	hash := c.Hash
	if len(hash) == 0 {
		hash = domain.SHA256(c.Body)
	}
	created := c.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO effective_configs (hash, body, content_type, created_at) VALUES ($1,$2,$3,$4)
ON CONFLICT (hash) DO UPDATE SET touched_at = now()`, []byte(hash), c.Body, defaultContentType(c.ContentType), created.UTC())
	return dbErr("configs.putEffective", err)
}

// GetEffective implements store.ConfigStore.
func (s *configStore) GetEffective(ctx context.Context, hash domain.Hash) (*domain.EffectiveConfig, error) {
	var (
		c domain.EffectiveConfig
		h []byte
	)
	err := s.db.q.QueryRow(ctx, `SELECT hash, body, content_type, created_at FROM effective_configs WHERE hash = $1`, []byte(hash)).
		Scan(&h, &c.Body, &c.ContentType, &c.CreatedAt)
	if err != nil {
		return nil, dbErr("configs.getEffective", err)
	}
	c.Hash, c.CreatedAt = domain.Hash(h), c.CreatedAt.UTC()
	return &c, nil
}

// PutComponentSet implements store.ConfigStore.
func (s *configStore) PutComponentSet(ctx context.Context, cs domain.ComponentSet) error {
	if len(cs.Hash) == 0 {
		return fmt.Errorf("%w: component set hash is empty", domain.ErrInvalidArgument)
	}
	comps := cs.Components
	if comps == nil {
		comps = map[string]map[string]domain.ComponentDetails{}
	}
	b, err := json.Marshal(comps)
	if err != nil {
		return fmt.Errorf("marshal components: %w", err)
	}
	_, err = s.db.q.Exec(ctx, `INSERT INTO component_sets (hash, components) VALUES ($1, $2) ON CONFLICT (hash) DO NOTHING`, []byte(cs.Hash), b)
	return dbErr("configs.putComponentSet", err)
}

// GetComponentSet implements store.ConfigStore.
func (s *configStore) GetComponentSet(ctx context.Context, hash domain.Hash) (*domain.ComponentSet, error) {
	var (
		h []byte
		b []byte
	)
	if err := s.db.q.QueryRow(ctx, `SELECT hash, components FROM component_sets WHERE hash = $1`, []byte(hash)).Scan(&h, &b); err != nil {
		return nil, dbErr("configs.getComponentSet", err)
	}
	cs := domain.ComponentSet{Hash: domain.Hash(h), Components: map[string]map[string]domain.ComponentDetails{}}
	if err := decodeJSON(b, &cs.Components); err != nil {
		return nil, fmt.Errorf("decode components: %w", err)
	}
	if cs.Components == nil {
		cs.Components = map[string]map[string]domain.ComponentDetails{}
	}
	return &cs, nil
}

// HasComponentSet implements store.ConfigStore.
func (s *configStore) HasComponentSet(ctx context.Context, hash domain.Hash) (bool, error) {
	var ok bool
	err := s.db.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM component_sets WHERE hash = $1)`, []byte(hash)).Scan(&ok)
	return ok, dbErr("configs.hasComponentSet", err)
}

// GC implements store.ConfigStore.
func (s *configStore) GC(ctx context.Context, olderThan time.Time) (int64, error) {
	var total int64
	err := s.db.withTx(ctx, func(q querier) error {
		if _, err := q.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", maintenanceTimeout.Milliseconds())); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `
WITH refs AS (
  SELECT assigned_config_hash AS h FROM agents WHERE assigned_config_hash IS NOT NULL
  UNION SELECT desired_config_hash FROM agents WHERE desired_config_hash IS NOT NULL
  UNION SELECT failed_config_hash FROM agents WHERE failed_config_hash IS NOT NULL
  UNION SELECT remote_config_hash FROM agents WHERE remote_config_hash IS NOT NULL
  UNION SELECT decode(t.to_ref, 'hex') FROM rollout_targets t JOIN rollouts r ON r.id = t.rollout_id
        WHERE r.kind = 'config' AND r.status NOT IN `+terminalRolloutStatusesSQL+` AND t.to_ref ~ '^([0-9a-f]{2})+$'
  UNION SELECT decode(t.from_ref, 'hex') FROM rollout_targets t JOIN rollouts r ON r.id = t.rollout_id
        WHERE r.kind = 'config' AND r.status NOT IN `+terminalRolloutStatusesSQL+` AND t.from_ref ~ '^([0-9a-f]{2})+$'
)
DELETE FROM rendered_configs c WHERE c.touched_at < $1 AND NOT EXISTS (SELECT 1 FROM refs WHERE refs.h = c.hash)`, olderThan.UTC())
		if err != nil {
			return err
		}
		total += tag.RowsAffected()
		tag, err = q.Exec(ctx, `
DELETE FROM effective_configs c WHERE c.touched_at < $1
  AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.effective_config_hash = c.hash)`, olderThan.UTC())
		if err != nil {
			return err
		}
		total += tag.RowsAffected()
		return nil
	})
	return total, dbErr("configs.gc", err)
}
