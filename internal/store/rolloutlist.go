// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	"github.com/myriarch/myriarch/internal/domain"
)

// activeRolloutPage is the page size of ListActiveRollouts. PostgreSQL caps a
// page at 500 rows and uses 50 when no limit is given (the in-memory stores do
// not), so callers that need every row must page explicitly.
const activeRolloutPage = 500

// ListActiveRollouts returns ALL non-terminal rollouts (newest first), paging
// through the store. RolloutStore.List without a limit returns at most 50 rows
// in PostgreSQL, which silently hides the oldest active rollouts from
// consumers that need the complete set (rollout engine, statistics).
func ListActiveRollouts(ctx context.Context, rs RolloutStore) ([]domain.Rollout, error) {
	var all []domain.Rollout
	for offset := 0; ; offset += activeRolloutPage {
		page, total, err := rs.List(ctx, RolloutFilter{ActiveOnly: true, Limit: activeRolloutPage, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) == 0 || offset+len(page) >= total {
			return all, nil
		}
	}
}
