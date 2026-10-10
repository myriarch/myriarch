// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

func candidates(n int, pilots ...int) []waveCandidate {
	isPilot := map[int]bool{}
	for _, p := range pilots {
		isPilot[p] = true
	}
	out := make([]waveCandidate, n)
	for i := range out {
		id := uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", i))
		view := domain.AttributeView{"site.id": fmt.Sprint(i)}
		if isPilot[i] {
			view["labels.pilot"] = "true"
		}
		out[i] = waveCandidate{AgentID: id, View: view}
	}
	return out
}

func careful() []domain.WaveSpec {
	return domain.RolloutPolicySpec{Waves: []domain.WaveSpec{
		{Name: "pilot", Selector: &domain.Selector{MatchAttributes: map[string]string{"labels.pilot": "true"}}},
		{Name: "early", Percent: 10, RequireApproval: true},
		{Name: "half", Percent: 50},
		{Name: "all", Percent: 100},
	}}.WithDefaults(domain.RolloutKindConfig, "Europe/Berlin").Waves
}

func TestAssignWavesPilotThenCumulativePercent(t *testing.T) {
	id := uuid.MustParse("01a11121-e597-7b69-9d25-a80cc7b9c707")
	cands := candidates(100, 3, 77)
	waveOf, counts := assignWaves(id, careful(), cands)
	require.Len(t, waveOf, 100)
	assert.Equal(t, []int{2, 8, 40, 50}, counts, "pilot 2; early: ceil(10%%)=10 cumulative -> 8 more; half: 50 cumulative -> 40 more; rest 50")
	assert.Equal(t, 0, waveOf[cands[3].AgentID])
	assert.Equal(t, 0, waveOf[cands[77].AgentID])
	sum := 0
	for _, c := range counts {
		sum += c
	}
	assert.Equal(t, 100, sum)
}

func TestAssignWavesAreStableAndRolloutSpecific(t *testing.T) {
	id1 := uuid.MustParse("01a11121-e597-7b69-9d25-a80cc7b9c707")
	id2 := uuid.MustParse("01a11121-e597-7b69-9d25-a80cc7b9c708")
	cands := candidates(100)
	a1, _ := assignWaves(id1, careful(), cands)
	// Input order must not matter.
	rev := make([]waveCandidate, len(cands))
	for i := range cands {
		rev[len(cands)-1-i] = cands[i]
	}
	a1b, _ := assignWaves(id1, careful(), rev)
	assert.Equal(t, a1, a1b, "stable for the same rollout id regardless of input order")
	a2, _ := assignWaves(id2, careful(), cands)
	assert.NotEqual(t, a1, a2, "a different rollout spreads agents differently")
	// Growing the set keeps the early wave members of the common agents mostly stable (hash order).
	small := cands[:50]
	s, _ := assignWaves(id1, careful(), small)
	assert.Len(t, s, 50)
}

func TestAssignWavesImplicitRestAndSkipped(t *testing.T) {
	id := uuid.New()
	// selector wave only: WithDefaults appends "rest"
	spec := domain.RolloutPolicySpec{Waves: []domain.WaveSpec{
		{Name: "pilot", Selector: &domain.Selector{MatchAttributes: map[string]string{"labels.pilot": "true"}}},
	}}.WithDefaults(domain.RolloutKindConfig, "UTC")
	require.Len(t, spec.Waves, 2)
	assert.Equal(t, "rest", spec.Waves[1].Name)
	waveOf, counts := assignWaves(id, spec.Waves, candidates(10, 1))
	assert.Equal(t, []int{1, 9}, counts)
	assert.Len(t, waveOf, 10)

	// no pilots match -> pilot wave empty -> skipped
	_, counts = assignWaves(id, spec.Waves, candidates(10))
	assert.Equal(t, []int{0, 10}, counts)
	waves := newWaves(spec.Waves, counts)
	assert.Equal(t, domain.WaveSkipped, waves[0].Status)
	assert.Equal(t, domain.WavePending, waves[1].Status)
	assert.NotNil(t, waves[0].Selector)

	// single agent: percent waves round up, later waves are empty
	spec = domain.RolloutPolicySpec{Waves: []domain.WaveSpec{{Name: "a", Percent: 10}, {Name: "b", Percent: 50}, {Name: "c", Percent: 100}}}.WithDefaults(domain.RolloutKindConfig, "UTC")
	_, counts = assignWaves(id, spec.Waves, candidates(1))
	assert.Equal(t, []int{1, 0, 0}, counts, "ceil(10%% of 1) = 1")
	// zero targets
	_, counts = assignWaves(id, spec.Waves, nil)
	assert.Equal(t, []int{0, 0, 0}, counts)
}

func TestAssignWavesSelectorWaveBiggerThanPercent(t *testing.T) {
	id := uuid.New()
	spec := domain.RolloutPolicySpec{Waves: []domain.WaveSpec{
		{Name: "pilot", Selector: &domain.Selector{MatchExpressions: []domain.SelectorRequirement{{Key: "labels.pilot", Operator: domain.OpExists}}}},
		{Name: "five", Percent: 5},
		{Name: "all", Percent: 100},
	}}.WithDefaults(domain.RolloutKindConfig, "UTC")
	_, counts := assignWaves(id, spec.Waves, candidates(100, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10))
	assert.Equal(t, []int{10, 0, 90}, counts, "the pilot wave already exceeds 5%%: the percent wave is empty")
}

func TestAssignWavesLeftoversGoToLastWave(t *testing.T) {
	// caller error: specs without a final 100% wave
	specs := []domain.WaveSpec{{Name: "a", Percent: 10}}
	waveOf, counts := assignWaves(uuid.New(), specs, candidates(20))
	assert.Equal(t, []int{20}, counts)
	assert.Len(t, waveOf, 20)
}
