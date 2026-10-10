// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/gitops"
)

const testdataRepo = "../testdata/gitops"

var update = flag.Bool("update", false, "rewrite testdata/golden from the current render output")

func loadExample(t testing.TB) (*domain.GitOpsSnapshot, []blueprint.Sample) {
	t.Helper()
	fsys := os.DirFS(testdataRepo)
	snap, issues := gitops.Parse(fsys, ".", "testcommit")
	require.Empty(t, issues, "example repo must parse cleanly: %+v", issues)
	samples, sissues := gitops.ParseSamples(fsys, ".")
	require.Empty(t, sissues)
	require.Len(t, samples, 6)
	return snap, samples
}

// renderSample reproduces what the assignment service does for an agent.
func renderSample(t testing.TB, r *blueprint.Renderer, snap *domain.GitOpsSnapshot, sm blueprint.Sample, comps *domain.ComponentSet) (domain.RenderResult, string) {
	t.Helper()
	inv := blueprint.ApplyInventory(snap, sm.Attributes)
	labels := map[string]string{}
	for k, v := range sm.Labels {
		labels[k] = v
	}
	for k, v := range inv.Labels {
		labels[k] = v
	}
	view := blueprint.ViewFrom(sm.Attributes, labels)
	m := blueprint.MatchFleet(snap, view)
	require.NotNil(t, m.Fleet, "sample %s must be assigned (%s)", sm.Name, m.Error)
	res, err := r.Render(blueprint.RenderInput{
		Snapshot: snap, Fleet: m.Fleet, Agent: blueprint.AgentInput{Attributes: view},
		InventoryParameters: inv.Parameters, Components: comps,
	})
	require.NoError(t, err)
	return res, m.Fleet.Name
}

// writeGolden rewrites the golden files of one sample (run the test with
// -update after an intended change of the test data or the renderer).
func writeGolden(t *testing.T, name string, res domain.RenderResult) {
	t.Helper()
	dir := filepath.Join("testdata", "golden")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), res.Body, 0o644))
	raw, err := json.MarshalIndent(res, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".result.json"), append(raw, '\n'), 0o644))
}

func TestGoldenRenderOfAllSamples(t *testing.T) {
	snap, samples := loadExample(t)
	r := blueprint.NewRenderer(blueprint.Options{})
	for _, sm := range samples {
		t.Run(sm.Name, func(t *testing.T) {
			res, _ := renderSample(t, r, snap, sm, nil)
			require.False(t, res.Issues.HasErrors(), "%+v", res.Issues)

			if *update {
				writeGolden(t, sm.Name, res)
			}

			wantBody, err := os.ReadFile(filepath.Join("testdata", "golden", sm.Name+".yaml"))
			require.NoError(t, err)
			assert.Equal(t, string(wantBody), string(res.Body), "body must match the golden file byte for byte")

			wantJSON, err := os.ReadFile(filepath.Join("testdata", "golden", sm.Name+".result.json"))
			require.NoError(t, err)
			gotJSON, err := json.Marshal(res)
			require.NoError(t, err)
			assert.JSONEq(t, string(wantJSON), string(gotJSON))
			assert.Equal(t, domain.ConfigHash(wantBody).String(), res.Hash.String())
		})
	}
}

// The snapshot as the store returns it (JSON round trip with
// UnmarshalJSONPreservingInts) must render identical bodies and hashes.
func TestGoldenRoundTripThroughJSON(t *testing.T) {
	snap, samples := loadExample(t)
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	var back domain.GitOpsSnapshot
	require.NoError(t, domain.UnmarshalJSONPreservingInts(raw, &back))

	r := blueprint.NewRenderer(blueprint.Options{})
	for _, sm := range samples {
		want, _ := renderSample(t, r, snap, sm, nil)
		got, _ := renderSample(t, r, &back, sm, nil)
		assert.Equal(t, string(want.Body), string(got.Body), sm.Name)
		assert.Equal(t, want.Hash.String(), got.Hash.String(), sm.Name)
		assert.Equal(t, want.Layers, got.Layers, sm.Name)
	}
}

func TestExampleRepoValidates(t *testing.T) {
	snap, samples := loadExample(t)
	r := blueprint.NewRenderer(blueprint.Options{})
	issues := r.ValidateSnapshot(snap, blueprint.ValidateOptions{Samples: samples})
	assert.False(t, issues.HasErrors(), "%+v", issues)
}
