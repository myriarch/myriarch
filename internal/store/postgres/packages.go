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

type packageStore struct{ db *DB }

var _ store.PackageOfferStore = (*packageStore)(nil)

// SetOffers implements store.PackageOfferStore (replaces the complete set).
func (s *packageStore) SetOffers(ctx context.Context, agentID domain.AgentID, offers []domain.PackageOffer) error {
	err := s.db.withTx(ctx, func(q querier) error {
		if _, err := q.Exec(ctx, `DELETE FROM package_offers WHERE agent_id = $1`, agentID); err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, o := range offers {
			headers, err := jsonArg(o.Headers, "")
			if err != nil {
				return err
			}
			created := now
			if !o.CreatedAt.IsZero() {
				created = o.CreatedAt.UTC()
			}
			var sig any
			if len(o.Signature) > 0 {
				sig = o.Signature
			}
			if _, err := q.Exec(ctx, `INSERT INTO package_offers (agent_id, name, version, download_url, content_hash, signature, headers, rollout_id, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				agentID, o.Name, o.Version, o.DownloadURL, []byte(o.ContentHash), sig, headers, uuidPtrArg(o.RolloutID), created); err != nil {
				return err
			}
		}
		return nil
	})
	return dbErr("packages.setOffers", err)
}

// GetOffers implements store.PackageOfferStore.
func (s *packageStore) GetOffers(ctx context.Context, agentID domain.AgentID) ([]domain.PackageOffer, error) {
	rows, err := s.db.q.Query(ctx, `SELECT agent_id, name, version, download_url, content_hash, signature, headers, rollout_id, created_at
FROM package_offers WHERE agent_id = $1 ORDER BY name`, agentID)
	if err != nil {
		return nil, dbErr("packages.getOffers", err)
	}
	defer rows.Close()
	out := []domain.PackageOffer{}
	for rows.Next() {
		var (
			o       domain.PackageOffer
			hash    []byte
			headers []byte
			rid     *uuid.UUID
		)
		if err := rows.Scan(&o.AgentID, &o.Name, &o.Version, &o.DownloadURL, &hash, &o.Signature, &headers, &rid, &o.CreatedAt); err != nil {
			return nil, dbErr("packages.getOffers scan", err)
		}
		o.ContentHash, o.RolloutID, o.CreatedAt = domain.Hash(hash), rid, o.CreatedAt.UTC()
		if len(headers) > 0 {
			if err := decodeJSON(headers, &o.Headers); err != nil {
				return nil, fmt.Errorf("decode offer headers: %w", err)
			}
		}
		out = append(out, o)
	}
	return out, dbErr("packages.getOffers", rows.Err())
}

// ClearOffers implements store.PackageOfferStore.
func (s *packageStore) ClearOffers(ctx context.Context, agentID domain.AgentID) error {
	_, err := s.db.q.Exec(ctx, `DELETE FROM package_offers WHERE agent_id = $1`, agentID)
	return dbErr("packages.clearOffers", err)
}
