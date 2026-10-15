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

// Tests of the warning bridge_selected (docs/reference/gitops-schema.md): a
// fleet whose selector also matches an OpAMP Bridge agent, and does not set
// spec.bridge.manage, would never configure it.

const bridgeBlueprint = `apiVersion: myriarch.dev/v1alpha1
kind: Blueprint
metadata: {name: base}
spec:
  config:
    receivers: {otlp: {protocols: {grpc: {}}}}
    exporters: {debug: {}}
    service:
      pipelines:
        traces: {receivers: [otlp], exporters: [debug]}
`

// bridgeFleet returns a Fleet document with the given selector, written as the
// YAML below "spec:", and optionally the opt-in.
func bridgeFleet(name, selector string, manage bool) string {
	doc := "apiVersion: myriarch.dev/v1alpha1\nkind: Fleet\nmetadata: {name: " + name + "}\nspec:\n  blueprint: base\n"
	if selector != "" {
		doc += "  selector:\n" + selector
	}
	if manage {
		doc += "  bridge:\n    manage: true\n"
	}
	return doc
}

func validateBridgeRepo(t *testing.T, files map[string]string) *gitops.Result {
	t.Helper()
	fsys := fstest.MapFS{"blueprints/base.yaml": {Data: []byte(bridgeBlueprint)}}
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return gitops.Validate(fsys, ".", "test", blueprint.NewRenderer(blueprint.Options{}), nil)
}

func bridgeIssues(res *gitops.Result) domain.Issues {
	var out domain.Issues
	for _, is := range res.Issues {
		if is.Code == "bridge_selected" {
			out = append(out, is)
		}
	}
	return out
}

func bridgeCodesOf(res *gitops.Result) []string {
	out := []string{}
	for _, is := range res.Issues {
		out = append(out, is.Code)
	}
	return out
}

func TestBridgeSelectedWarnsForSelectorsThatMatchABridge(t *testing.T) {
	for name, selector := range map[string]string{
		"no selector":         "",
		"empty selector":      "    matchAttributes: {}\n",
		"only NotIn":          "    matchExpressions:\n      - {key: site.type, operator: NotIn, values: [edge]}\n",
		"only DoesNotExist":   "    matchExpressions:\n      - {key: os.type, operator: DoesNotExist}\n",
		"both negative kinds": "    matchExpressions:\n      - {key: site.type, operator: NotIn, values: [edge]}\n      - {key: site.id, operator: DoesNotExist}\n",
		"exists on an attribute a Bridge reports": "    matchExpressions:\n      - {key: host.name, operator: Exists}\n",
		"glob on any value":                       "    matchExpressions:\n      - {key: service.name, operator: Glob, values: ['*']}\n",
		"the mode attribute":                      "    matchExpressions:\n      - {key: opentelemetry.io/opamp.bridge.mode, operator: Exists}\n",
	} {
		t.Run(name, func(t *testing.T) {
			res := validateBridgeRepo(t, map[string]string{"fleets/all.yaml": bridgeFleet("all", selector, false)})
			got := bridgeIssues(res)
			require.Len(t, got, 1, "%v", bridgeCodesOf(res))
			is := got[0]
			assert.Equal(t, domain.SeverityWarning, is.Severity)
			assert.Equal(t, "fleets/all.yaml", is.File)
			assert.Equal(t, "Fleet/all", is.Object)
			assert.Equal(t, "spec.selector", is.Path)
			assert.Positive(t, is.Line)
			assert.Contains(t, is.Message, "spec.bridge.manage")
			assert.True(t, res.Valid(), "a warning does not make the repository invalid")
		})
	}
}

func TestBridgeSelectedIsSilentWithTheOptIn(t *testing.T) {
	for name, selector := range map[string]string{
		"no selector": "",
		"only NotIn":  "    matchExpressions:\n      - {key: site.type, operator: NotIn, values: [edge]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			res := validateBridgeRepo(t, map[string]string{"fleets/all.yaml": bridgeFleet("all", selector, true)})
			assert.Empty(t, bridgeIssues(res))
			assert.Empty(t, res.Issues)
		})
	}
}

func TestBridgeSelectedIsSilentForSelectorsThatNeedWhatABridgeDoesNotReport(t *testing.T) {
	for name, selector := range map[string]string{
		"site.type":                       "    matchAttributes: {site.type: edge}\n",
		"os.type":                         "    matchAttributes: {os.type: linux}\n",
		"os.type with a set":              "    matchExpressions:\n      - {key: os.type, operator: In, values: [linux, windows]}\n",
		"a server label":                  "    matchAttributes: {labels.pilot: 'true'}\n",
		"the service name of a collector": "    matchAttributes: {service.name: otelcol-contrib}\n",
		"a glob on the host name":         "    matchExpressions:\n      - {key: host.name, operator: Glob, values: ['edge-*']}\n",
		"a value of the host name":        "    matchAttributes: {host.name: edge-1.example.com}\n",
		"a site attribute exists":         "    matchExpressions:\n      - {key: site.id, operator: Exists}\n",
		"a negative and a positive":       "    matchAttributes: {os.type: linux}\n    matchExpressions:\n      - {key: site.type, operator: NotIn, values: [edge]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			res := validateBridgeRepo(t, map[string]string{"fleets/all.yaml": bridgeFleet("all", selector, false)})
			assert.Empty(t, bridgeIssues(res), "%v", res.Issues)
		})
	}
}

// The message names the modes that the selector matches and does not promise
// what no fleet does: a Bridge in operator mode stays locked with the opt-in.
func TestBridgeSelectedNamesTheMatchedModes(t *testing.T) {
	cases := map[string]struct {
		selector string
		want     []string
		notWant  []string
	}{
		"operator only": {"    matchAttributes: {service.name: io.opentelemetry.operator-opamp-bridge}\n",
			[]string{"operator mode", "no fleet configures"}, []string{"standalone", "set spec.bridge.manage"}},
		"standalone only": {"    matchExpressions:\n      - {key: k8s.workload.type, operator: Exists}\n",
			[]string{"standalone mode"}, []string{"operator mode", "operator and standalone"}},
		"both": {"", []string{"operator and standalone mode", "only Bridges in standalone mode"}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := validateBridgeRepo(t, map[string]string{"fleets/all.yaml": bridgeFleet("all", tc.selector, false)})
			got := bridgeIssues(res)
			require.Len(t, got, 1, "%v", bridgeCodesOf(res))
			for _, w := range tc.want {
				assert.Contains(t, got[0].Message, w)
			}
			for _, w := range tc.notWant {
				assert.NotContains(t, got[0].Message, w)
			}
		})
	}
}

func TestBridgeSelectedAppliesTheInventoryToTheSyntheticBridge(t *testing.T) {
	const inventory = `apiVersion: myriarch.dev/v1alpha1
kind: Inventory
metadata: {name: bridges}
spec:
  entries:
    - match: {opentelemetry.io/opamp.bridge.mode: operator}
      labels: {role: bridge}
`
	selector := "    matchAttributes: {labels.role: bridge}\n"
	with := validateBridgeRepo(t, map[string]string{"fleets/all.yaml": bridgeFleet("all", selector, false), "inventories/bridges.yaml": inventory})
	require.Len(t, bridgeIssues(with), 1, "%v", bridgeCodesOf(with))
	assert.Contains(t, bridgeIssues(with)[0].Message, "operator mode")
	without := validateBridgeRepo(t, map[string]string{"fleets/all.yaml": bridgeFleet("all", selector, false)})
	assert.Empty(t, bridgeIssues(without), "a label the inventory does not give a Bridge is a runtime matter")
}

func TestBridgeSelectedOnlyTheFleetWithoutTheOptInWarns(t *testing.T) {
	res := validateBridgeRepo(t, map[string]string{
		"fleets/a-all.yaml":     bridgeFleet("a-all", "", false),
		"fleets/b-bridges.yaml": bridgeFleet("b-bridges", "    matchExpressions:\n      - {key: opentelemetry.io/opamp.bridge.mode, operator: Exists}\n", true),
		"fleets/c-linux.yaml":   bridgeFleet("c-linux", "    matchAttributes: {os.type: linux}\n", false),
	})
	got := bridgeIssues(res)
	require.Len(t, got, 1, "%v", bridgeCodesOf(res))
	assert.Equal(t, "Fleet/a-all", got[0].Object)
}

const bridgeSample = `name: bridge
attributes:
  service.name: io.opentelemetry.operator-opamp-bridge
  opentelemetry.io/opamp.bridge.mode: operator
  os.family: linux
  host.name: opamp-bridge-5d8f7c9b4-x2k7p
`

// A blueprint that cannot render for a Bridge sample proves that the sample is
// not rendered: it reads an attribute that no Bridge reports.
const bridgeNeedsSiteBlueprint = `apiVersion: myriarch.dev/v1alpha1
kind: Blueprint
metadata: {name: base}
spec:
  config:
    receivers: {otlp: {protocols: {grpc: {}}}}
    exporters:
      otlp_grpc/gw:
        endpoint: '[[ attr "site.id" ]].example.com:4317'
    service:
      pipelines:
        traces: {receivers: [otlp], exporters: [otlp_grpc/gw]}
`

func TestBridgeSampleIsCheckedButNeverRendered(t *testing.T) {
	files := func(manage bool, selector string) map[string]string {
		return map[string]string{
			"blueprints/base.yaml":       bridgeNeedsSiteBlueprint,
			"fleets/all.yaml":            bridgeFleet("all", selector, manage),
			"samples/agents/bridge.yaml": bridgeSample,
		}
	}

	t.Run("matches a fleet without the opt-in", func(t *testing.T) {
		res := validateBridgeRepo(t, files(false, ""))
		got := bridgeIssues(res)
		require.Len(t, got, 2, "the fleet and the sample: %v", bridgeCodesOf(res))
		var sample domain.Issue
		for _, is := range got {
			if strings.HasPrefix(is.Object, "Sample/") {
				sample = is
			}
		}
		assert.Equal(t, "Sample/bridge", sample.Object)
		assert.Equal(t, "samples/agents/bridge.yaml", sample.File)
		assert.Equal(t, 1, sample.Line)
		assert.Equal(t, domain.SeverityWarning, sample.Severity)
		assert.Contains(t, sample.Message, `sample agent "bridge"`)
		assert.Contains(t, sample.Message, `fleet "all"`)
		assert.Contains(t, sample.Message, "operator mode")
		assert.NotContains(t, bridgeCodesOf(res), "template_error", "a Bridge sample is not rendered")
		assert.NotContains(t, bridgeCodesOf(res), "sample_unassigned")
		assert.True(t, res.Valid())
	})
	t.Run("matches a fleet with the opt-in", func(t *testing.T) {
		res := validateBridgeRepo(t, files(true, ""))
		assert.Empty(t, res.Issues, "no warning, and the sample is not rendered")
	})
	t.Run("matches no fleet", func(t *testing.T) {
		res := validateBridgeRepo(t, files(false, "    matchAttributes: {site.type: edge}\n"))
		assert.Empty(t, res.Issues, "a Bridge sample does not have to match a fleet: no sample_unassigned")
	})
	t.Run("a sample that is no Bridge is rendered and has to match as before", func(t *testing.T) {
		f := files(false, "    matchAttributes: {os.type: windows}\n")
		f["samples/agents/linux.yaml"] = "name: linux\nattributes: {os.type: linux, host.name: host-a}\n"
		res := validateBridgeRepo(t, f)
		assert.Contains(t, bridgeCodesOf(res), "sample_unassigned")
		assert.Empty(t, bridgeIssues(res))
	})
	t.Run("a standalone sample is recognised by the mode attribute alone", func(t *testing.T) {
		f := files(false, "")
		f["samples/agents/bridge.yaml"] = "name: bridge\nattributes:\n  service.name: payments-collector\n  opentelemetry.io/opamp.bridge.mode: standalone\n"
		res := validateBridgeRepo(t, f)
		var sample *domain.Issue
		for i, is := range bridgeIssues(res) {
			if strings.HasPrefix(is.Object, "Sample/") {
				sample = &bridgeIssues(res)[i]
			}
		}
		require.NotNil(t, sample, "%v", res.Issues)
		assert.Contains(t, sample.Message, "standalone mode")
		assert.NotContains(t, bridgeCodesOf(res), "template_error")
	})
	t.Run("a sample that reports no known mode", func(t *testing.T) {
		f := files(false, "")
		f["samples/agents/bridge.yaml"] = "name: bridge\nattributes:\n  service.name: io.opentelemetry.operator-opamp-bridge\n"
		res := validateBridgeRepo(t, f)
		var msg string
		for _, is := range bridgeIssues(res) {
			if strings.HasPrefix(is.Object, "Sample/") {
				msg = is.Message
			}
		}
		assert.Contains(t, msg, "mode not reported")
	})
}

func TestBridgeSelectedDoesNotCoverInvalidSelectors(t *testing.T) {
	res := validateBridgeRepo(t, map[string]string{"fleets/all.yaml": bridgeFleet("all", "    matchExpressions:\n      - {key: site.type, operator: NotIn}\n", false)})
	assert.Contains(t, bridgeCodesOf(res), "selector_invalid")
	assert.Empty(t, bridgeIssues(res), "an invalid selector matches nothing")
}
