// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import "testing"

func TestTargetActivityRank(t *testing.T) {
	all := []TargetStatus{
		TargetPending, TargetWaitingWindow, TargetInProgress, TargetVerifying, TargetSucceeded,
		TargetFailed, TargetSkipped, TargetCancelled, TargetRolledBack,
	}
	unknown := TargetStatus("nope").ActivityRank()
	if unknown != 7 {
		t.Fatalf("unknown status ranks %d, want 7", unknown)
	}
	for _, s := range all {
		if r := s.ActivityRank(); r < 0 || r >= unknown {
			t.Errorf("%s ranks %d, want below %d", s, r, unknown)
		}
	}
	r := func(s TargetStatus) int { return s.ActivityRank() }
	if r(TargetInProgress) != r(TargetVerifying) || r(TargetFailed) != r(TargetRolledBack) {
		t.Errorf("in_progress/verifying and failed/rolled_back must share a rank")
	}
	order := []TargetStatus{TargetInProgress, TargetWaitingWindow, TargetFailed, TargetPending, TargetSucceeded, TargetSkipped, TargetCancelled}
	for i := 1; i < len(order); i++ {
		if r(order[i-1]) >= r(order[i]) {
			t.Errorf("%s (%d) must rank before %s (%d)", order[i-1], r(order[i-1]), order[i], r(order[i]))
		}
	}
}
