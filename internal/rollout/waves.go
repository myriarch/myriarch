// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"bytes"
	"crypto/sha256"
	"sort"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
)

// waveCandidate is one target to distribute over waves.
type waveCandidate struct {
	AgentID domain.AgentID
	// View is the agent's AttributeView (incl. labels.*) for selector waves.
	View domain.AttributeView
}

// assignWaves distributes the targets of a rollout over the (defaulted)
// waves (docs/concepts/rollouts.md, "Waves"):
//
//   - selector wave: all not yet assigned targets matching the selector;
//   - percent wave: not yet assigned targets in stable pseudo-random order
//     (ascending sha256(rolloutID || agentID)) until the CUMULATIVE share of
//     all targets (including earlier selector waves) reaches
//     ceil(percent * total / 100).
//
// It returns the wave index per agent and the number of targets per wave
// (waves with 0 targets are to be marked skipped). specs must be the result of
// RolloutPolicySpec.WithDefaults (so that the last wave is a 100 % wave); any
// target left over after the last wave goes into the last wave.
func assignWaves(rolloutID uuid.UUID, specs []domain.WaveSpec, targets []waveCandidate) (map[domain.AgentID]int, []int) {
	counts := make([]int, len(specs))
	assigned := make(map[domain.AgentID]int, len(targets))
	if len(specs) == 0 {
		return assigned, counts
	}
	total := len(targets)

	// Stable order for percent waves.
	ordered := make([]waveCandidate, len(targets))
	copy(ordered, targets)
	keys := make(map[domain.AgentID][32]byte, len(ordered))
	for _, c := range ordered {
		h := sha256.New()
		h.Write(rolloutID[:])
		h.Write(c.AgentID[:])
		var k [32]byte
		copy(k[:], h.Sum(nil))
		keys[c.AgentID] = k
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		ki, kj := keys[ordered[i].AgentID], keys[ordered[j].AgentID]
		if c := bytes.Compare(ki[:], kj[:]); c != 0 {
			return c < 0
		}
		return bytes.Compare(ordered[i].AgentID[:], ordered[j].AgentID[:]) < 0
	})

	done := 0
	for wi, w := range specs {
		switch {
		case w.Selector != nil:
			for _, c := range ordered {
				if _, ok := assigned[c.AgentID]; ok {
					continue
				}
				if w.Selector.Matches(c.View) {
					assigned[c.AgentID] = wi
					counts[wi]++
					done++
				}
			}
		default:
			cum := ceilDiv(w.Percent*total, 100)
			need := cum - done
			for _, c := range ordered {
				if need <= 0 {
					break
				}
				if _, ok := assigned[c.AgentID]; ok {
					continue
				}
				assigned[c.AgentID] = wi
				counts[wi]++
				done++
				need--
			}
		}
	}
	// Leftovers (only possible if the caller passed specs without a final
	// 100 % wave) belong to the last wave.
	last := len(specs) - 1
	for _, c := range ordered {
		if _, ok := assigned[c.AgentID]; !ok {
			assigned[c.AgentID] = last
			counts[last]++
		}
	}
	return assigned, counts
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// newWaves builds the runtime waves of a rollout from the defaulted specs and
// the per-wave target counts; empty waves are skipped.
func newWaves(specs []domain.WaveSpec, counts []int) []domain.RolloutWave {
	out := make([]domain.RolloutWave, len(specs))
	for i, s := range specs {
		w := domain.RolloutWave{
			Index: i, Name: s.Name, Percent: s.Percent, RequireApproval: s.RequireApproval,
			Status: domain.WavePending,
		}
		if s.Selector != nil {
			sel := *s.Selector
			w.Selector = &sel
		}
		if counts[i] == 0 {
			w.Status = domain.WaveSkipped
		}
		out[i] = w
	}
	return out
}
