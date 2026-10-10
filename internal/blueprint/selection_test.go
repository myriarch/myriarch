// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/gitops"
)

const selectionBlueprint = `apiVersion: myriarch.dev/v1alpha1
kind: Blueprint
metadata:
  name: base
spec:
  config:
    receivers:
      otlp:
        protocols:
          grpc: {}
    exporters:
      debug: {}
    service:
      pipelines:
        traces:
          receivers: [otlp]
          exporters: [debug]
`

const selectionPolicy = `apiVersion: myriarch.dev/v1alpha1
kind: RolloutPolicy
metadata:
  name: gateway-version
spec:
  waves:
    - name: all
      percent: 100
`

func selectionRepo(fleetSpec string) fstest.MapFS {
	return fstest.MapFS{
		"blueprints/base.yaml":     {Data: []byte(selectionBlueprint)},
		"policies/gw.yaml":         {Data: []byte(selectionPolicy)},
		"fleets/gw.yaml":           {Data: []byte("apiVersion: myriarch.dev/v1alpha1\nkind: Fleet\nmetadata:\n  name: gw\nspec:\n  blueprint: base\n" + fleetSpec)},
		"samples/agents/gw-a.yaml": {Data: []byte("name: gw-a\nattributes:\n  host.name: gw-a\n")},
	}
}

func validateSelection(t *testing.T, fleetSpec string) domain.Issues {
	t.Helper()
	res := gitops.Validate(selectionRepo(fleetSpec), ".", "test", blueprint.NewRenderer(blueprint.Options{}), nil)
	var out domain.Issues
	for _, is := range res.Issues {
		if strings.HasPrefix(is.Object, "Fleet/") {
			out = append(out, is)
		}
	}
	return out
}

func find(is domain.Issues, code string, sev domain.IssueSeverity) *domain.Issue {
	for i := range is {
		if is[i].Code == code && is[i].Severity == sev {
			return &is[i]
		}
	}
	return nil
}

// TestSelectionValidation covers the rules of docs/concepts/fleets.md ("The frame") one by one.
func TestSelectionValidation(t *testing.T) {
	type tc struct {
		name     string
		spec     string
		code     string
		severity domain.IssueSeverity
		contains string // message substring ("" with code "" = no issue expected)
		path     string
	}
	cases := []tc{
		{name: "valid range", spec: `  collector: {version: 0.161.0}
  update:
    executor: updater
    selection: {enabled: true, versionRange: ">=0.161.0, <=0.162.0"}
`},
		{name: "valid list with webhook free executor manual", spec: `  collector: {version: 0.161.0}
  update:
    executor: manual
    selection: {enabled: true, versions: [0.161.0, 0.162.0], allowDowngrade: true, requireReason: true}
`},
		{name: "valid without git target when the selection is enabled", spec: `  update:
    executor: updater
    selection: {enabled: true, versionRange: ">=0.161.0"}
`},
		{name: "neither range nor list", spec: `  update:
    executor: updater
    selection: {enabled: true}
`, code: "selection_invalid", severity: domain.SeverityError, contains: "exactly one of update.selection.versionRange or update.selection.versions is required", path: "spec.update.selection"},
		{name: "both range and list", spec: `  update:
    executor: updater
    selection: {enabled: true, versionRange: ">=0.161.0", versions: [0.161.0]}
`, code: "selection_invalid", severity: domain.SeverityError, contains: "exactly one of"},
		{name: "range not parsable", spec: `  update:
    executor: updater
    selection: {enabled: true, versionRange: ">=foo"}
`, code: "selection_invalid", severity: domain.SeverityError, contains: `versionRange ">=foo" is not a valid semantic version constraint`, path: "spec.update.selection.versionRange"},
		{name: "v prefix in the list", spec: `  update:
    executor: updater
    selection: {enabled: true, versions: [0.161.0, v0.162.0]}
`, code: "selection_invalid", severity: domain.SeverityError, contains: `versions[1] "v0.162.0" must be a semantic version without "v"`, path: "spec.update.selection.versions[1]"},
		{name: "empty list entry", spec: `  update:
    executor: updater
    selection: {enabled: true, versions: ["", 0.162.0]}
`, code: "selection_invalid", severity: domain.SeverityError, contains: `versions[0] "" must be a semantic version`},
		{name: "short version in the list", spec: `  update:
    executor: updater
    selection: {enabled: true, versions: ["0.162"]}
`, code: "selection_invalid", severity: domain.SeverityError, contains: "must be a semantic version"},
		{name: "duplicate in the list", spec: `  update:
    executor: updater
    selection: {enabled: true, versions: [0.161.0, 0.161.0]}
`, code: "selection_invalid", severity: domain.SeverityError, contains: `versions[1] "0.161.0" is listed twice`},
		{name: "executor none", spec: `  update:
    executor: none
    selection: {enabled: true, versionRange: ">=0.161.0"}
`, code: "selection_invalid", severity: domain.SeverityError, contains: "UI-triggered updates need an update executor (update.executor is none)"},
		{name: "no executor", spec: `  update:
    selection: {enabled: true, versionRange: ">=0.161.0"}
`, code: "selection_invalid", severity: domain.SeverityError, contains: "update.executor is none"},
		{name: "git target outside the range", spec: `  collector: {version: 0.160.0}
  update:
    executor: updater
    selection: {enabled: true, versionRange: ">=0.161.0, <=0.162.0"}
`, code: "selection_invalid", severity: domain.SeverityError, contains: "collector.version 0.160.0 is outside update.selection (>=0.161.0, <=0.162.0)"},
		{name: "git target outside the list", spec: `  collector: {version: 0.161.5}
  update:
    executor: updater
    selection: {enabled: true, versions: [0.161.0, 0.162.0]}
`, code: "selection_invalid", severity: domain.SeverityError, contains: "is outside update.selection ([0.161.0, 0.162.0])"},
		{name: "unknown rollout policy", spec: `  update:
    executor: updater
    selection: {enabled: true, versionRange: ">=0.161.0", rolloutPolicy: nope}
`, code: "policy_not_found", severity: domain.SeverityError, contains: `rollout policy "nope" does not exist`, path: "spec.update.selection.rolloutPolicy"},
		{name: "known rollout policy", spec: `  update:
    executor: updater
    selection: {enabled: true, versionRange: ">=0.161.0", rolloutPolicy: gateway-version}
`},
		{name: "disabled with a frame", spec: `  update:
    executor: updater
    selection: {enabled: false, versionRange: ">=0.161.0"}
`, code: "selection_invalid", severity: domain.SeverityWarning, contains: "update.selection is disabled: versionRange/versions are ignored"},
		{name: "disabled block alone is silent", spec: `  collector: {version: 0.161.0}
  update:
    executor: updater
    selection: {enabled: false}
`},
		{name: "executor manual with executorRef", spec: `  collector: {version: 0.161.0}
  update:
    executor: manual
    executorRef: something
`, code: "executor_invalid", severity: domain.SeverityWarning, contains: `executorRef "something" is ignored for executor "manual"`},
		{name: "updater without version and without frame still warns", spec: `  update:
    executor: updater
`, code: "missing_field", severity: domain.SeverityWarning, contains: "update.executor is updater but collector.version is empty"},
		{name: "manual without version and without frame warns", spec: `  update:
    executor: manual
`, code: "missing_field", severity: domain.SeverityWarning, contains: "update.executor is manual but collector.version is empty"},
		{name: "unknown executor lists manual", spec: `  update:
    executor: ansible
`, code: "executor_invalid", severity: domain.SeverityError, contains: "(none, updater, webhook, awx, manual)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := validateSelection(t, c.spec)
			if c.code == "" {
				for _, is := range issues {
					assert.Failf(t, "unexpected issue", "%+v", is)
				}
				return
			}
			is := find(issues, c.code, c.severity)
			require.NotNil(t, is, "expected %s %s, got %+v", c.severity, c.code, issues)
			assert.Contains(t, is.Message, c.contains)
			if c.path != "" {
				assert.Equal(t, c.path, is.Path)
			}
			assert.Equal(t, "Fleet/gw", is.Object)
			assert.Equal(t, "fleets/gw.yaml", is.File)
		})
	}
}

// TestSelectionTypoIsUnknownField: the strict parser rejects misspelt keys
// inside the new block.
func TestSelectionTypoIsUnknownField(t *testing.T) {
	issues := validateSelection(t, `  update:
    executor: updater
    selection:
      enabled: true
      versionrange: ">=0.161.0"
`)
	is := find(issues, "unknown_field", domain.SeverityError)
	require.NotNil(t, is, "%+v", issues)
	assert.Contains(t, is.Message, "versionrange")
	assert.Contains(t, is.Message, "versionRange", "the message suggests the right spelling")
	assert.Equal(t, 11, is.Line)
}

// TestSelectionParsesIntoTheFleet: the typed value arrives in the snapshot.
func TestSelectionParsesIntoTheFleet(t *testing.T) {
	res := gitops.Validate(selectionRepo(`  collector: {version: 0.161.0}
  update:
    executor: updater
    selection:
      enabled: true
      versionRange: ">=0.161.0, <=0.162.0"
      allowDowngrade: true
      requireReason: true
      rolloutPolicy: gateway-version
`), ".", "test", blueprint.NewRenderer(blueprint.Options{}), nil)
	require.True(t, res.Valid(), "%+v", res.Issues)
	var fleet *domain.Fleet
	for i := range res.Snapshot.Fleets {
		fleet = &res.Snapshot.Fleets[i]
	}
	require.NotNil(t, fleet)
	sel := fleet.Update.Selection
	require.NotNil(t, sel)
	assert.Equal(t, domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.161.0, <=0.162.0", AllowDowngrade: true, RequireReason: true, RolloutPolicy: "gateway-version"}, *sel)
}
