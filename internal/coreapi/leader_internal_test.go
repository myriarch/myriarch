// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi

import (
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

func TestMarkLeaderShowsAtMostOneLeader(t *testing.T) {
	now := time.Now().UTC()
	reps := []domain.Replica{
		{ID: "old", IsLeader: true, LastHeartbeatAt: now.Add(-12 * time.Second)}, // crashed 12 s ago: row still says leader
		{ID: "new", IsLeader: true, LastHeartbeatAt: now.Add(-2 * time.Second)},  // took over
		{ID: "follower", LastHeartbeatAt: now},
		{ID: "ancient", IsLeader: true, LastHeartbeatAt: now.Add(-time.Hour)},
	}
	got := markLeader(reps, now)
	var leaders []string
	for _, r := range got {
		if r.IsLeader {
			leaders = append(leaders, r.ID)
		}
	}
	if len(leaders) != 1 || leaders[0] != "new" {
		t.Fatalf("leaders = %v", leaders)
	}
	if l := markLeader([]domain.Replica{{ID: "x", IsLeader: true, LastHeartbeatAt: now.Add(-time.Hour)}}, now); l[0].IsLeader {
		t.Fatal("a replica without a recent heartbeat is not a leader")
	}
}
