// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package myrctl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/myrctl"
)

const (
	testdataRepo = "../testdata/gitops"
	invalidDir   = "../blueprint/testdata/invalid"
	goldenDir    = "../blueprint/testdata/golden"
	components   = "../../examples/sim/components-otelcol-contrib-0.162.0.json"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = myrctl.Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

type report struct {
	Valid    bool           `json:"valid"`
	Errors   int            `json:"errors"`
	Warnings int            `json:"warnings"`
	Objects  map[string]int `json:"objects"`
	Samples  int            `json:"samples"`
	Issues   domain.Issues  `json:"issues"`
}

func TestValidateExampleRepoIsValid(t *testing.T) {
	code, out, errs := run(t, "validate", testdataRepo)
	assert.Equal(t, 0, code, out+errs)
	assert.Contains(t, out, "OK: 5 Blueprint, 2 Executor, 5 Fleet, 1 Inventory, 3 RolloutPolicy; 6 samples; 0 errors, 0 warnings")
	assert.Empty(t, errs)

	code, out, _ = run(t, "validate", "--output", "json", "--components", components, testdataRepo)
	require.Equal(t, 0, code, out)
	var rep report
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.True(t, rep.Valid)
	assert.Equal(t, 5, rep.Objects["Fleet"])
	assert.Equal(t, 6, rep.Samples)
	assert.NotNil(t, rep.Issues)
	assert.Contains(t, out, `"issues": []`, "never null")
}

func TestValidateDefaultsToCurrentDirectory(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	abs, err := filepath.Abs(testdataRepo)
	require.NoError(t, err)
	require.NoError(t, os.Chdir(abs))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	code, out, errs := run(t, "validate")
	assert.Equal(t, 0, code, out+errs)
}

// Every negative repo yields exit 1 and its documented issue code (warning
// cases exit 0, unless --strict). Mirrors internal/blueprint/invalid_test.go.
func TestValidateNegativeRepos(t *testing.T) {
	cases := []struct {
		name       string
		severity   domain.IssueSeverity
		components bool
	}{
		{"extends_cycle", "error", false}, {"missing_parameter", "error", false}, {"unknown_parameter", "error", false},
		{"template_error", "error", false}, {"unquoted_template", "error", false}, {"when_not_bool", "error", false},
		{"undefined_component", "error", false}, {"unknown_component", "error", true}, {"wave_invalid", "error", false},
		{"window_invalid", "error", false}, {"blueprint_not_found", "error", false}, {"duplicate_object", "error", false},
		{"template_syntax", "error", false}, {"invalid_parameter_value", "error", false}, {"parameter_type_conflict", "error", false},
		{"policy_not_found", "error", false}, {"executor_not_found", "error", false}, {"selector_invalid", "error", false},
		{"selection_invalid", "error", false},
		{"executor_invalid", "error", false}, {"unknown_field", "error", false}, {"yaml_syntax", "error", false},
		{"invalid_component_id", "error", false}, {"invalid_pipeline", "error", false}, {"config_invalid_toplevel", "error", false},
		{"policy_invalid", "error", false}, {"invalid_name", "error", false}, {"missing_field", "error", false},
		{"unknown_kind", "error", false}, {"unknown_api_version", "error", false}, {"invalid_config_key", "error", false},
		{"fleet_ambiguous", "warning", false}, {"sample_unassigned", "warning", false}, {"deprecated_component", "warning", false},
		{"unused_component", "warning", false}, {"env_syntax", "warning", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(invalidDir, tc.name)
			args := []string{"validate", "--output", "json"}
			if tc.components {
				args = append(args, "--components", components)
			}
			code, out, errs := run(t, append(args, dir)...)
			var rep report
			require.NoError(t, json.Unmarshal([]byte(out), &rep), "stdout: %s stderr: %s", out, errs)
			var found bool
			for _, is := range rep.Issues {
				if is.Code == tc.name && is.Severity == tc.severity {
					found = true
				}
			}
			assert.True(t, found, "expected %s/%s in %+v", tc.severity, tc.name, rep.Issues)
			if tc.severity == domain.SeverityError {
				assert.Equal(t, 1, code, "validation errors exit with 1")
				assert.False(t, rep.Valid)
				assert.Greater(t, rep.Errors, 0)
			} else {
				assert.Equal(t, 0, code, "warnings do not fail")
				assert.True(t, rep.Valid)
				assert.Greater(t, rep.Warnings, 0)
				strict, _, _ := run(t, "validate", "--strict", dir)
				assert.Equal(t, 1, strict, "--strict fails on warnings")
			}
		})
	}
}

func TestValidateUnknownComponentNeedsComponentList(t *testing.T) {
	dir := filepath.Join(invalidDir, "unknown_component")
	code, out, _ := run(t, "validate", dir)
	assert.Equal(t, 0, code, "without --components the check is off: %s", out)
	code, out, _ = run(t, "validate", "--components", components, dir)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "error unknown_component")
}

func TestValidateTextOutputHasFileAndLine(t *testing.T) {
	code, out, _ := run(t, "validate", filepath.Join(invalidDir, "unknown_parameter"))
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "fleets/all.yaml:8: error unknown_parameter:")
	assert.Contains(t, out, "FAILED:")
}

func TestUsageErrorsExitWithTwo(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	file := filepath.Join(t.TempDir(), "f.yaml")
	require.NoError(t, os.WriteFile(file, []byte("x: 1\n"), 0o600))
	badComps := filepath.Join(t.TempDir(), "c.json")
	require.NoError(t, os.WriteFile(badComps, []byte("{not json"), 0o600))

	for name, args := range map[string][]string{
		"missing directory":  {"validate", missing},
		"file as directory":  {"validate", file},
		"too many args":      {"validate", "a", "b"},
		"unknown flag":       {"validate", "--bogus"},
		"unknown command":    {"frobnicate"},
		"bad output":         {"validate", "-o", "xml", testdataRepo},
		"missing components": {"validate", "--components", missing, testdataRepo},
		"broken components":  {"validate", "--components", badComps, testdataRepo},
		"render no target":   {"render", "--dir", testdataRepo},
		"render unknown":     {"render", "--dir", testdataRepo, "--sample", "nope"},
		"render fleet":       {"render", "--dir", testdataRepo, "--sample", "edge-nl-1001", "--fleet", "nope"},
		"render both":        {"render", "--dir", testdataRepo, "--sample", "edge-nl-1001", "--attr", "a=b"},
		"render bad attr":    {"render", "--dir", testdataRepo, "--attr", "novalue"},
		"render bad param":   {"render", "--dir", testdataRepo, "--sample", "edge-nl-1001", "--param", "novalue"},
		"render no match":    {"render", "--dir", testdataRepo, "--attr", "site.type=moon"},
		"render dir":         {"render", "--dir", missing, "--sample", "x"},
		"render bad output":  {"render", "--dir", testdataRepo, "--sample", "edge-nl-1001", "-o", "xml"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errs := run(t, args...)
			assert.Equal(t, 2, code, "stdout: %s stderr: %s", out, errs)
			assert.Contains(t, errs, "myrctl:")
		})
	}
}

func TestRenderMatchesGoldenFilesForAllSamples(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(testdataRepo, "samples", "agents"))
	require.NoError(t, err)
	require.Len(t, entries, 6)
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".yaml")
		t.Run(name, func(t *testing.T) {
			code, out, errs := run(t, "render", "--dir", testdataRepo, "--sample", name)
			require.Equal(t, 0, code, errs)
			want, err := os.ReadFile(filepath.Join(goldenDir, name+".yaml"))
			require.NoError(t, err)
			assert.Equal(t, string(want), out, "byte for byte like the golden file (and like myriarchd)")

			// JSON form: hash and parameters of the golden result
			code, out, _ = run(t, "render", "--dir", testdataRepo, "--sample", name, "-o", "json")
			require.Equal(t, 0, code)
			var got struct {
				Fleet, Hash, Config string
				Parameters          map[string]any
				ParameterSources    map[string]string
				Issues              []any
			}
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			assert.Equal(t, string(want), got.Config)
			assert.Equal(t, domain.ConfigHash(want).String(), got.Hash)
			assert.NotEmpty(t, got.Fleet)
			assert.NotEmpty(t, got.Parameters)
			assert.NotNil(t, got.Issues)
		})
	}
}

func TestRenderFromAttributesAndOverrides(t *testing.T) {
	// the attributes of sample edge-nl-1001 given on the command line
	raw, err := os.ReadFile(filepath.Join(testdataRepo, "samples", "agents", "edge-nl-1001.yaml"))
	require.NoError(t, err)
	var sample struct {
		Attributes map[string]any `yaml:"attributes"`
	}
	require.NoError(t, yamlUnmarshal(raw, &sample))
	args := []string{"render", "--dir", testdataRepo}
	for k, v := range sample.Attributes {
		args = append(args, "--attr", k+"="+toString(v))
	}
	code, out, errs := run(t, args...)
	require.Equal(t, 0, code, errs)
	want, err := os.ReadFile(filepath.Join(goldenDir, "edge-nl-1001.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(want), out)

	// explicit fleet; --param overrides a parameter (highest precedence)
	code, out, errs = run(t, "render", "--dir", testdataRepo, "--sample", "edge-nl-1001", "--fleet", "edge-nl", "--param", "gatewayEndpoint=override.example.internal:4317", "-o", "json")
	require.Equal(t, 0, code, errs)
	var got struct {
		Fleet            string
		Parameters       map[string]any
		ParameterSources map[string]string
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "edge-nl", got.Fleet)
	assert.Equal(t, "override.example.internal:4317", got.Parameters["gatewayEndpoint"])
	assert.Equal(t, "override", got.ParameterSources["gatewayEndpoint"])
}

func TestRenderWithErrorsExitsWithOne(t *testing.T) {
	dir := filepath.Join(invalidDir, "template_error")
	code, out, errs := run(t, "render", "--dir", dir, "--sample", "a")
	assert.Equal(t, 1, code)
	assert.Empty(t, out, "no config is printed for a failing render")
	assert.Contains(t, errs, "error template_error")
}

func TestRenderComponentsCheck(t *testing.T) {
	dir := filepath.Join(invalidDir, "unknown_component")
	code, _, _ := run(t, "render", "--dir", dir, "--sample", "a")
	assert.Equal(t, 0, code)
	code, _, errs := run(t, "render", "--dir", dir, "--sample", "a", "--components", components)
	assert.Equal(t, 1, code)
	assert.Contains(t, errs, "unknown_component")
}

func TestVersion(t *testing.T) {
	code, out, _ := run(t, "version")
	assert.Equal(t, 0, code)
	assert.NotEmpty(t, strings.TrimSpace(out))
}

func TestExitCode(t *testing.T) {
	assert.Equal(t, 0, myrctl.ExitCode(nil))
	assert.Equal(t, 1, myrctl.ExitCode(&myrctl.ExitError{Code: 1}))
	assert.Equal(t, 2, myrctl.ExitCode(assert.AnError))
}

// The generic GitOps starter template (gitops-template/) stays valid against
// the component list of the collector version it is written for (the check of
// its CI job: validate --strict). The test fails if the template is missing.
func TestStarterTemplateIsValid(t *testing.T) {
	const template = "../../gitops-template/config"
	if _, err := os.Stat(template); err != nil {
		t.Fatalf("the starter template is missing: %v", err)
	}
	code, out, errs := run(t, "validate", "--strict", "--components", components, template)
	assert.Equal(t, 0, code, out+errs)
	assert.Empty(t, errs)
}

// The frame for UI-selected versions (spec.update.selection) of the example
// repository is valid, and myrctl reports its rules like myriarchd does.
func TestValidateVersionSelectionFrame(t *testing.T) {
	code, out, errs := run(t, "validate", "--strict", "--output", "json", testdataRepo)
	require.Equal(t, 0, code, out+errs)
	var rep report
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.True(t, rep.Valid)
	assert.Zero(t, rep.Warnings)

	// a typo inside the block is an unknown field, a bad range is selection_invalid
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS(testdataRepo)))
	fleet := filepath.Join(dir, "fleets", "edge-be.yaml")
	raw, err := os.ReadFile(fleet)
	require.NoError(t, err)
	bad := strings.Replace(string(raw), "versionRange:", "versionrange:", 1)
	require.NotEqual(t, string(raw), bad, "the example fleet uses a version range")
	require.NoError(t, os.WriteFile(fleet, []byte(bad), 0o644))
	code, out, _ = run(t, "validate", "--output", "json", dir)
	assert.Equal(t, 1, code)
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	var codes []string
	for _, is := range rep.Issues {
		codes = append(codes, is.Code)
	}
	assert.Contains(t, codes, "unknown_field")

	bad = strings.Replace(string(raw), `">=0.161.0, <=0.162.0"`, `">=nonsense"`, 1)
	require.NoError(t, os.WriteFile(fleet, []byte(bad), 0o644))
	code, out, _ = run(t, "validate", "--output", "json", dir)
	assert.Equal(t, 1, code)
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	codes = nil
	for _, is := range rep.Issues {
		codes = append(codes, is.Code)
	}
	assert.Contains(t, codes, "selection_invalid")
}
