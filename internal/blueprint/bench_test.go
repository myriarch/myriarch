// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
)

// renderFleet renders the edge-nl fleet for n distinct agents (per-agent
// values: site.id, host name) like the assignment service does.
func renderFleet(t testing.TB, n int) time.Duration {
	snap, samples := loadExample(t)
	var tmpl blueprint.Sample
	for _, s := range samples {
		if s.Name == "edge-nl-1001" {
			tmpl = s
		}
	}
	var fleet *domain.Fleet
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == "edge-nl" {
			fleet = &snap.Fleets[i]
		}
	}
	require.NotNil(t, fleet)
	r := blueprint.NewRenderer(blueprint.Options{})
	start := time.Now()
	hashes := map[string]bool{}
	for i := 0; i < n; i++ {
		attrs := map[string]string{}
		for k, v := range tmpl.Attributes {
			attrs[k] = v
		}
		attrs["site.id"] = fmt.Sprintf("%04d", i)
		attrs["host.name"] = fmt.Sprintf("EDGE-%04d-01", i)
		inv := blueprint.ApplyInventory(snap, attrs)
		view := blueprint.ViewFrom(attrs, inv.Labels)
		res, err := r.Render(blueprint.RenderInput{Snapshot: snap, Fleet: fleet, Agent: blueprint.AgentInput{Attributes: view}, InventoryParameters: inv.Parameters})
		require.NoError(t, err)
		require.False(t, res.Issues.HasErrors())
		hashes[res.Hash.String()] = true
	}
	require.Greater(t, len(hashes), n/2, "per-agent site.id makes configs differ")
	return time.Since(start)
}

func BenchmarkRenderFleet5000(b *testing.B) {
	for i := 0; i < b.N; i++ {
		renderFleet(b, 5000)
	}
}

// Acceptance: rendering a fleet of 5000 agents takes < 5 s.
func TestRenderFleet5000UnderFiveSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test")
	}
	budget := 5 * time.Second
	if raceEnabled {
		budget *= 6 // the race detector slows down by roughly 5-10x
	}
	d := renderFleet(t, 5000)
	t.Logf("rendered 5000 agents in %s", d)
	require.Less(t, d, budget)
}
