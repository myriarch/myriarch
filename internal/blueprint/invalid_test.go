// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/gitops"
)

// invalidCase describes the expected finding of testdata/invalid/<name>.
type invalidCase struct {
	name     string
	severity domain.IssueSeverity
	file     string
	line     int // 0 = not checked
	// components enables the unknown_component check with the 0.162.0 list.
	components bool
}

var invalidCases = []invalidCase{
	{name: "extends_cycle", severity: "error", file: "blueprints/a.yaml"},
	{name: "missing_parameter", severity: "error", file: "blueprints/base.yaml"},
	{name: "unknown_parameter", severity: "error", file: "fleets/all.yaml", line: 8},
	{name: "template_error", severity: "error", file: "blueprints/base.yaml", line: 21},
	{name: "unquoted_template", severity: "error", file: "blueprints/base.yaml", line: 21},
	{name: "when_not_bool", severity: "error", file: "blueprints/base.yaml", line: 29},
	{name: "undefined_component", severity: "error", file: "blueprints/base.yaml", line: 26},
	{name: "unknown_component", severity: "error", file: "blueprints/base.yaml", components: true},
	{name: "wave_invalid", severity: "error", file: "policies/p.yaml"},
	{name: "window_invalid", severity: "error", file: "policies/p.yaml"},
	{name: "blueprint_not_found", severity: "error", file: "fleets/all.yaml", line: 6},
	{name: "duplicate_object", severity: "error", file: "blueprints/base2.yaml", line: 4},
	{name: "template_syntax", severity: "error", file: "blueprints/base.yaml", line: 21},
	{name: "invalid_parameter_value", severity: "error", file: "fleets/all.yaml", line: 8},
	{name: "parameter_type_conflict", severity: "error", file: "blueprints/child.yaml"},
	{name: "policy_not_found", severity: "error", file: "fleets/all.yaml", line: 8},
	{name: "executor_not_found", severity: "error", file: "fleets/all.yaml", line: 11},
	{name: "selector_invalid", severity: "error", file: "fleets/all.yaml"},
	{name: "selection_invalid", severity: "error", file: "fleets/all.yaml", line: 11},
	{name: "executor_invalid", severity: "error", file: "executors/hook.yaml"},
	{name: "unknown_field", severity: "error", file: "fleets/all.yaml", line: 7},
	{name: "yaml_syntax", severity: "error", file: "fleets/broken.yaml"},
	{name: "invalid_component_id", severity: "error", file: "blueprints/base.yaml"},
	{name: "invalid_pipeline", severity: "error", file: "blueprints/base.yaml"},
	{name: "config_invalid_toplevel", severity: "error", file: "blueprints/base.yaml"},
	{name: "policy_invalid", severity: "error", file: "policies/p.yaml"},
	{name: "invalid_name", severity: "error", file: "fleets/all.yaml", line: 4},
	{name: "missing_field", severity: "error", file: "fleets/all.yaml"},
	{name: "unknown_kind", severity: "error", file: "fleets/all.yaml", line: 2},
	{name: "unknown_api_version", severity: "error", file: "fleets/all.yaml", line: 1},
	{name: "invalid_config_key", severity: "error", file: "blueprints/base.yaml", line: 20},
	{name: "fleet_ambiguous", severity: "warning", file: "samples/agents/a.yaml", line: 1},
	{name: "sample_unassigned", severity: "warning", file: "samples/agents/a.yaml", line: 1},
	{name: "deprecated_component", severity: "warning", file: "blueprints/base.yaml"},
	{name: "unused_component", severity: "warning", file: "blueprints/base.yaml"},
	{name: "env_syntax", severity: "warning", file: "blueprints/base.yaml"},
}

func loadComponents(t *testing.T) *domain.ComponentSet {
	t.Helper()
	raw, err := os.ReadFile("../../examples/sim/components-otelcol-contrib-0.162.0.json")
	require.NoError(t, err)
	var f struct {
		Components map[string]map[string]domain.ComponentDetails `json:"components"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	return &domain.ComponentSet{Components: f.Components}
}

func TestInvalidRepos(t *testing.T) {
	r := blueprint.NewRenderer(blueprint.Options{})
	comps := loadComponents(t)
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join("testdata", "invalid", tc.name)
			var c *domain.ComponentSet
			if tc.components {
				c = comps
			}
			res := gitops.Validate(os.DirFS(dir), ".", "test", r, c)
			var found *domain.Issue
			for i := range res.Issues {
				is := res.Issues[i]
				if is.Code == tc.name && is.Severity == tc.severity {
					found = &res.Issues[i]
					break
				}
			}
			require.NotNil(t, found, "expected issue %s; got %+v", tc.name, res.Issues)
			assert.Equal(t, tc.file, found.File, "issue: %+v", *found)
			if tc.line != 0 {
				assert.Equal(t, tc.line, found.Line, "issue: %+v", *found)
			}
			if tc.severity == domain.SeverityError {
				assert.False(t, res.Valid())
			}
		})
	}
}

// Every documented issue code that myrctl/myriarchd can produce for the
// invalid repos is covered above; the base repo itself is valid.
func TestInvalidReposBaseIsValid(t *testing.T) {
	r := blueprint.NewRenderer(blueprint.Options{})
	dir := filepath.Join("testdata", "invalid", "fleet_ambiguous")
	res := gitops.Validate(os.DirFS(dir), ".", "test", r, nil)
	for _, is := range res.Issues {
		assert.Equal(t, domain.SeverityWarning, is.Severity, "%+v", is)
	}
}
