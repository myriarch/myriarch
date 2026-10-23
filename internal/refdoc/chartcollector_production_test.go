// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/docgen"
)

// collectorChartDir is the directory of the collector chart, seen from internal/refdoc.
var collectorChartDir = filepath.Join(repoRoot, "deploy", "helm", "myriarch-collector")

// TestCollectorChartValuesSchema keeps deploy/helm/myriarch-collector/values.schema.json equal to the schema that is converted from
// values.schema.source.yaml. The chart has no generated configuration block. REFDOC_UPDATE=1 (make docs-gen) rewrites it.
func TestCollectorChartValuesSchema(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(collectorChartDir, "values.schema.source.yaml"))
	require.NoError(t, err)
	page, err := RenderChartValuesSchema(ChartSchemaSource{Source: src, SourceName: "deploy/helm/myriarch-collector/values.schema.source.yaml"})
	require.NoError(t, err)
	path := filepath.Join(collectorChartDir, ChartSchemaFile)
	if updating() {
		require.NoError(t, docgen.WriteFile(path, page))
		return
	}
	if d := docgen.DiffFile(path, page); d != "" {
		t.Fatalf("deploy/helm/myriarch-collector/%s is out of date (run make docs-gen and commit): %s", ChartSchemaFile, d)
	}
}

// TestCollectorChartValuesTable keeps the values table in deploy/helm/myriarch-collector/README.md equal to the one that is rendered
// from values.schema.json and values.yaml. REFDOC_UPDATE=1 (make docs-gen) rewrites it.
func TestCollectorChartValuesTable(t *testing.T) {
	schema, err := os.ReadFile(filepath.Join(collectorChartDir, ChartSchemaFile))
	require.NoError(t, err)
	values, err := os.ReadFile(filepath.Join(collectorChartDir, "values.yaml"))
	require.NoError(t, err)
	table, err := RenderChartValuesTable(schema, values, nil)
	require.NoError(t, err)
	path := filepath.Join(collectorChartDir, "README.md")
	old, err := os.ReadFile(path)
	require.NoError(t, err)
	readme, err := ReplaceChartValuesTable(old, table)
	require.NoError(t, err)
	if updating() {
		require.NoError(t, docgen.WriteFile(path, readme))
		return
	}
	if d := docgen.DiffFile(path, readme); d != "" {
		t.Fatalf("the values table in deploy/helm/myriarch-collector/README.md is out of date (run make docs-gen and commit): %s", d)
	}
}
