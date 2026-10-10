// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"

	"github.com/myriarch/myriarch/internal/api/dto"
)

// FleetCounts returns the per-fleet agent counts of the dashboard (collectors
// with a fleet only), computed in one statement. Definitions: see
// dto.FleetCounts. Ordered by fleet name.
func (d *DB) FleetCounts(ctx context.Context) ([]dto.FleetCounts, error) {
	rows, err := d.q.Query(ctx, `
SELECT fleet_name,
       count(*),
       count(*) FILTER (WHERE status = 'online'),
       count(*) FILTER (WHERE healthy IS TRUE),
       count(*) FILTER (WHERE config_sync = 'in_sync'),
       count(*) FILTER (WHERE config_sync IN ('pending', 'drift')),
       count(*) FILTER (WHERE config_sync = 'failed'),
       COALESCE(max(target_version), ''),
       count(*) FILTER (WHERE target_version <> '' AND service_version = target_version)
FROM agents
WHERE kind = 'collector' AND fleet_name IS NOT NULL
GROUP BY fleet_name
ORDER BY fleet_name`)
	if err != nil {
		return nil, dbErr("stats.fleetCounts", err)
	}
	defer rows.Close()
	out := []dto.FleetCounts{}
	for rows.Next() {
		var c dto.FleetCounts
		if err := rows.Scan(&c.Fleet, &c.Total, &c.Online, &c.Healthy, &c.InSync, &c.Pending, &c.Failed, &c.TargetVersion, &c.OnTarget); err != nil {
			return nil, dbErr("stats.fleetCounts scan", err)
		}
		out = append(out, c)
	}
	return out, dbErr("stats.fleetCounts", rows.Err())
}
