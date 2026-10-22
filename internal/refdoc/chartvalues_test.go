// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/docgen"
)

// chartConfigTableNote is the row that stands for the generated config block in the README table.
const chartConfigTableNote = "The configuration file of the server (`myriarchd.yaml`); keys, types and descriptions are those of the " +
	"[configuration reference](../../../docs/reference/configuration.md). The chart defaults are in `values.yaml`."

func productionValuesTable(t *testing.T) (readme []byte, path string) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join(chartDir, ChartSchemaFile))
	require.NoError(t, err)
	values, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
	require.NoError(t, err)
	table, err := RenderChartValuesTable(schema, values, map[string]string{"config": chartConfigTableNote})
	require.NoError(t, err)
	path = filepath.Join(chartDir, "README.md")
	old, err := os.ReadFile(path)
	require.NoError(t, err)
	readme, err = ReplaceChartValuesTable(old, table)
	require.NoError(t, err)
	return readme, path
}

// TestChartValuesTable keeps the values table in deploy/helm/myriarch/README.md equal to the one that is rendered from
// values.schema.json and values.yaml. REFDOC_UPDATE=1 (make docs-gen) rewrites it.
func TestChartValuesTable(t *testing.T) {
	readme, path := productionValuesTable(t)
	if updating() {
		require.NoError(t, docgen.WriteFile(path, readme))
		return
	}
	if d := docgen.DiffFile(path, readme); d != "" {
		t.Fatalf("the values table in deploy/helm/myriarch/README.md is out of date (run make docs-gen and commit): %s", d)
	}
}

func TestRenderChartValuesTable(t *testing.T) {
	schema := `{"$defs":{"port":{"type":"integer","description":"A port."}},
	  "properties":{
	    "a":{"type":"object","properties":{"b":{"$ref":"#/$defs/port"},"c":{"type":["string","null"],"description":"x | y\n  z"}}},
	    "config":{"type":"object","properties":{"big":{"type":"string"}}},
	    "d":{"type":"array"}}}`
	values := "a:\n  b: 8080\nconfig:\n  big: x\n"
	got, err := RenderChartValuesTable([]byte(schema), []byte(values), map[string]string{"config": "see reference"})
	require.NoError(t, err)
	assert.Equal(t, "| Value | Type | Default | Description |\n|---|---|---|---|\n"+
		"| `a.b` | integer | `8080` | A port. |\n"+
		"| `a.c` | string or null | unset | x \\| y z |\n"+
		"| `config` | object | | see reference |\n"+
		"| `d` | array | unset |  |\n", string(got))
}

func TestReplaceChartValuesTable(t *testing.T) {
	in := []byte("head\n" + ChartValuesTableBegin + "\nold\n" + ChartValuesTableEnd + "\ntail\n")
	out, err := ReplaceChartValuesTable(in, []byte("| t |\n"))
	require.NoError(t, err)
	assert.Equal(t, "head\n"+ChartValuesTableBegin+"\n\n| t |\n\n"+ChartValuesTableEnd+"\ntail\n", string(out))
	again, err := ReplaceChartValuesTable(out, []byte("| t |\n"))
	require.NoError(t, err)
	assert.Equal(t, string(out), string(again), "replacing is idempotent")
	_, err = ReplaceChartValuesTable([]byte("no markers"), nil)
	assert.Error(t, err)
}
