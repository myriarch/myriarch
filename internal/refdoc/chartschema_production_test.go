// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/configload"
	"github.com/myriarch/myriarch/internal/docgen"
)

// chartDir is the directory of the server chart, seen from internal/refdoc.
var chartDir = filepath.Join(repoRoot, "deploy", "helm", "myriarch")

// chartConfigExclusions are the keys of the configuration that the chart does not offer. The first two the chart generates itself
// from the top-level values, the next two do not exist in this chart (it always starts `myriarchd serve`), and the rest are
// secrets as literal values: the chart reads secrets from files of a mounted Secret and refuses to render them into the ConfigMap.
var chartConfigExclusions = []ChartExclusion{
	{Path: "auth.bootstrapAdmin", Reason: "use the top-level bootstrapAdmin values"},
	{Path: "opamp.enrollmentTokens", Reason: "use the top-level enrollmentTokens values"},
	{Path: "auth.devLogin", Reason: "the chart never starts the development mode"},
	{Path: "dev", Reason: "the chart never starts the development mode"},
	{Path: "database.dsn", Reason: "a secret, use database.dsnFile"},
	{Path: "auth.oidc.clientSecret", Reason: "a secret, use auth.oidc.clientSecretFile"},
	{Path: "gitops.webhookSecret", Reason: "a secret, use gitops.webhookSecretFile"},
	{Path: "notifications.channels[].url", Reason: "a secret, use notifications.channels[].urlFile"},
}

func productionChartSchemaSource(t *testing.T) ChartSchemaSource {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(chartDir, "values.schema.source.yaml"))
	require.NoError(t, err)
	return ChartSchemaSource{
		Source:     src,
		SourceName: "deploy/helm/myriarch/values.schema.source.yaml",
		Config: ChartConfigSource{
			Root:        reflect.TypeOf(config.Config{}),
			Origin:      "internal/config",
			SourceFiles: []string{"../config/config.go"},
			Description: "The configuration file of the server (myriarchd.yaml), which the chart renders into a ConfigMap. Keys, types and " +
				"descriptions come from the configuration structure and are the ones of docs/reference/configuration.md; a default that a " +
				"description names is the default of the server, the defaults of the chart are in values.yaml.",
			Excluded: chartConfigExclusions,
		},
	}
}

// TestChartValuesSchema keeps deploy/helm/myriarch/values.schema.json equal to the schema that is assembled from
// values.schema.source.yaml and the configuration structure. REFDOC_UPDATE=1 (make docs-gen) rewrites it.
func TestChartValuesSchema(t *testing.T) {
	page, err := RenderChartValuesSchema(productionChartSchemaSource(t))
	require.NoError(t, err)
	path := filepath.Join(chartDir, ChartSchemaFile)
	if updating() {
		require.NoError(t, docgen.WriteFile(path, page))
		return
	}
	if d := docgen.DiffFile(path, page); d != "" {
		t.Fatalf("deploy/helm/myriarch/%s is out of date (run make docs-gen and commit): %s", ChartSchemaFile, d)
	}
}

// chartConfigLeaves returns the dotted path and the schema of every property below the config block of the generated schema.
func chartConfigLeaves(t *testing.T) map[string]map[string]any {
	t.Helper()
	page, err := RenderChartValuesSchema(productionChartSchemaSource(t))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(page, &doc))
	cfg := doc["properties"].(map[string]any)["config"].(map[string]any)
	out := map[string]map[string]any{}
	var walk func(node map[string]any, path string)
	walk = func(node map[string]any, path string) {
		if props, ok := node["properties"].(map[string]any); ok {
			for k, v := range props {
				child := v.(map[string]any)
				p := k
				if path != "" {
					p = path + "." + k
				}
				out[p] = child
				walk(child, p)
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			walk(items, path+"[]")
		}
		if add, ok := node["additionalProperties"].(map[string]any); ok {
			walk(add, path+".<name>")
		}
	}
	walk(cfg, "")
	return out
}

// TestChartSchemaOffersNoSecret is the control that keeps secrets out of the values: no property of the config block is one of the
// keys that the loader redacts, and no string setting that names a token, a secret, a password or a DSN holds anything but a path
// (a file, a directory) or a prefix. A new setting of that kind has to be excluded, or written as a file setting, on purpose.
func TestChartSchemaOffersNoSecret(t *testing.T) {
	leaves := chartConfigLeaves(t)
	require.Greater(t, len(leaves), 150, "the config block looks incomplete")
	secretWords := []string{"token", "secret", "password", "dsn"}
	var offending []string
	for path, node := range leaves {
		key := path[strings.LastIndexAny(path, ".]")+1:]
		if configload.IsSecretKey(key) {
			offending = append(offending, path+" (a key that the loader redacts)")
			continue
		}
		if node["type"] != "string" {
			continue
		}
		if node["pattern"] == durationPattern {
			continue // a time span, such as downloadTokenTtl, holds no token
		}
		lower := strings.ToLower(key)
		for _, w := range secretWords {
			if strings.Contains(lower, w) && !strings.HasSuffix(key, "File") && !strings.HasSuffix(key, "Dir") && !strings.HasSuffix(key, "Prefix") {
				offending = append(offending, path+" (a string that names a "+w+" but is neither a file nor a directory nor a prefix)")
			}
		}
	}
	sort.Strings(offending)
	assert.Empty(t, offending, "the schema offers a secret as a value: exclude the key in chartConfigExclusions or use a file setting")

	for _, e := range chartConfigExclusions {
		assert.NotContains(t, leaves, e.Path, "%s is excluded", e.Path)
	}
	for _, k := range []string{"database.dsnFile", "auth.oidc.clientSecretFile", "gitops.webhookSecretFile", "notifications.channels[].urlFile"} {
		assert.Contains(t, leaves, k, "the file setting that replaces an excluded secret is offered")
	}
}

// TestChartSchemaDescribesEverySetting: the description is the only documentation of a setting in the schema, so none is empty.
func TestChartSchemaDescribesEverySetting(t *testing.T) {
	for path, node := range chartConfigLeaves(t) {
		d, _ := node["description"].(string)
		assert.NotEmpty(t, strings.TrimSpace(d), "%s has no description", path)
	}
}
