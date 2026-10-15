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

// chartConfigLeaves returns the dotted path and the schema of every property below the config block of the production schema.
func chartConfigLeaves(t *testing.T) map[string]map[string]any {
	t.Helper()
	page, err := RenderChartValuesSchema(productionChartSchemaSource(t))
	require.NoError(t, err)
	return chartLeavesOf(t, page)
}

// chartLeavesOf returns the dotted path and the schema of every property below the config block of a generated schema. A list of
// entries contributes path[] for the entries and a map path.<name>.
func chartLeavesOf(t *testing.T, page []byte) map[string]map[string]any {
	t.Helper()
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

// chartSecretWords are the words that make the name of a setting the name of a secret. A name is matched in lower case, anywhere in
// it. The list errs on the side of too many: a setting that has one of them in its name and holds no secret says so in
// chartSecretNamedSettings.
var chartSecretWords = []string{"token", "secret", "password", "dsn", "key", "credential", "passphrase", "bearer"}

// chartPathSuffixes end the name of a setting that holds a path or a prefix and not the secret: it says where a secret is.
var chartPathSuffixes = []string{"File", "Dir", "Prefix"}

// chartSecretNamedSettings are the settings whose names contain a word of chartSecretWords although they hold no secret, by path,
// with the reason. They are listed one by one, not by a part of the name, so a new setting is never exempt because of its name.
var chartSecretNamedSettings = map[string]string{
	"executors.secretEnvHosts":  "the destinations that an environment variable of an executor may be sent to",
	"executors.secretFileHosts": "the destinations that a secret file of an executor may be sent to",
}

// chartSchemaType returns the type name of a node of the config block, which has exactly one.
func chartSchemaType(node map[string]any) string {
	t, _ := node["type"].(string)
	return t
}

// chartHoldsText reports whether a setting can hold text that might be a secret: a string that is not a time span, a list of
// strings, or a map of strings or of lists of strings. A list or a map of structures holds text only in its own settings, which the
// check looks at one by one.
func chartHoldsText(node map[string]any) bool {
	switch chartSchemaType(node) {
	case "string":
		return node["pattern"] != durationPattern
	case "array":
		items, _ := node["items"].(map[string]any)
		return items != nil && chartHoldsText(items)
	case "object":
		values, _ := node["additionalProperties"].(map[string]any)
		return values != nil && chartHoldsText(values)
	}
	return false
}

// chartSecretWord returns the first word of chartSecretWords in the name of a setting, or "".
func chartSecretWord(key string) string {
	lower := strings.ToLower(key)
	for _, w := range chartSecretWords {
		if strings.Contains(lower, w) {
			return w
		}
	}
	return ""
}

// chartSecretFindings returns one line for every setting of the leaves (see chartLeavesOf) that offers a secret as a value, sorted: a
// key that the loader redacts, whatever its shape, and every setting that can hold text and has a secret word in its name unless it
// is a path or a prefix or is listed in chartSecretNamedSettings.
func chartSecretFindings(leaves map[string]map[string]any) []string {
	var findings []string
leaves:
	for path, node := range leaves {
		key := path[strings.LastIndexAny(path, ".]")+1:]
		if configload.IsSecretKey(key) {
			findings = append(findings, path+" (a key that the loader redacts)")
			continue
		}
		if _, listed := chartSecretNamedSettings[path]; listed || !chartHoldsText(node) {
			continue
		}
		for _, suffix := range chartPathSuffixes {
			if strings.HasSuffix(key, suffix) {
				continue leaves
			}
		}
		if w := chartSecretWord(key); w != "" {
			findings = append(findings, path+" (can hold text and names a "+w+", but is neither a file nor a directory nor a prefix)")
		}
	}
	sort.Strings(findings)
	return findings
}

// TestChartSchemaOffersNoSecret is the control that keeps secrets out of the values: no property of the config block is one of the
// keys that the loader redacts, and no setting that can hold text (a string, a list of strings, a map of strings or of lists of
// strings) names a token, a secret, a password, a key, a credential, a passphrase, a bearer token or a DSN, unless it holds a path (a
// file, a directory) or a prefix. A new setting of that kind has to be excluded, or written as a file setting, on purpose; one that
// only has such a word in its name is listed in chartSecretNamedSettings with the reason.
func TestChartSchemaOffersNoSecret(t *testing.T) {
	leaves := chartConfigLeaves(t)
	require.Greater(t, len(leaves), 150, "the config block looks incomplete")
	assert.Empty(t, chartSecretFindings(leaves), "the schema offers a secret as a value: exclude the key in chartConfigExclusions or use a file setting")

	// an entry of chartSecretNamedSettings that is not needed any more is removed, so that the list does not grow into an exemption
	for path, why := range chartSecretNamedSettings {
		node, ok := leaves[path]
		require.True(t, ok, "%s is listed in chartSecretNamedSettings but is not a setting of the config block: remove it", path)
		key := path[strings.LastIndexAny(path, ".]")+1:]
		assert.True(t, chartHoldsText(node) && chartSecretWord(key) != "", "%s is listed in chartSecretNamedSettings but would not be reported: remove it", path)
		assert.NotEmpty(t, strings.TrimSpace(why), "%s needs a reason", path)
	}

	for _, e := range chartConfigExclusions {
		assert.NotContains(t, leaves, e.Path, "%s is excluded", e.Path)
	}
	for _, k := range []string{"database.dsnFile", "auth.oidc.clientSecretFile", "gitops.webhookSecretFile", "notifications.channels[].urlFile"} {
		assert.Contains(t, leaves, k, "the file setting that replaces an excluded secret is offered")
	}
}

// TestChartSchemaSecretCheckSeesEveryShape runs the settings of ChartLeaky and ChartHarmless through the generator and the check of
// the secrets. A check that looked at strings only, or at a short list of words, would let a list or a map of strings named tokens,
// passwords or credentials pass; this test fails for such a check.
func TestChartSchemaSecretCheckSeesEveryShape(t *testing.T) {
	findingsOf := func(root reflect.Type) []string {
		src := chartMiniSource(readChartMiniSource(t))
		src.Config.Root, src.Config.SourceFiles, src.Config.Excluded = root, []string{"chartschema_types_test.go"}, nil
		page, err := RenderChartValuesSchema(src)
		require.NoError(t, err)
		return chartSecretFindings(chartLeavesOf(t, page))
	}
	paths := func(findings []string) []string {
		out := make([]string, len(findings))
		for i, f := range findings {
			out[i], _, _ = strings.Cut(f, " ")
		}
		return out
	}

	assert.Equal(t, []string{
		"apiKey",                   // a string, the word key
		"byName.<name>.bearer",     // a string in a map of structures, the word bearer
		"byName.<name>.passphrase", // the word passphrase
		"credentials",              // a map of lists of strings, the word credential
		"items[].bearer",           // a string in a list of structures
		"items[].passphrase",       //
		"nested.bearer",            // a string in a structure
		"nested.passphrase",        //
		"passwords",                // a map of strings, the word password
		"tokens",                   // a list of strings, the word token
		"url",                      // a key that the loader redacts
	}, paths(findingsOf(reflect.TypeOf(ChartLeaky{}))))

	assert.Empty(t, findingsOf(reflect.TypeOf(ChartHarmless{})), "files, directories, prefixes, time spans, numbers and switches are no secrets, and neither are names without a secret word")
}

// TestChartSchemaDescribesEverySetting: the description is the only documentation of a setting in the schema, so none is empty.
func TestChartSchemaDescribesEverySetting(t *testing.T) {
	for path, node := range chartConfigLeaves(t) {
		d, _ := node["description"].(string)
		assert.NotEmpty(t, strings.TrimSpace(d), "%s has no description", path)
	}
}
