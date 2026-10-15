// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/docgen"
)

func readChartMiniSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "chartschema", "source.yaml"))
	require.NoError(t, err)
	return string(b)
}

// chartMiniConfig is the subtree configuration of the golden test: three keys are left out, one in every notation of the path.
func chartMiniConfig() ChartConfigSource {
	return ChartConfigSource{
		Root:        reflect.TypeOf(ChartMini{}),
		Origin:      "the mini configuration",
		SourceFiles: []string{"miniconfig_test.go", "chartschema_types_test.go"},
		Description: "The mini configuration, rendered into the file of the mini server.",
		Excluded: []ChartExclusion{
			{Path: "server.password", Reason: "a secret"},
			{Path: "server.tls.secrets.<name>.env", Reason: "the environment is not offered"},
			{Path: "notify.channels[].url", Reason: "a secret"},
			{Path: "dev", Reason: "the development mode does not exist"},
		},
	}
}

func chartMiniSource(source string) ChartSchemaSource {
	return ChartSchemaSource{Source: []byte(source), SourceName: "testdata/chartschema/source.yaml", Config: chartMiniConfig()}
}

func TestGoldenChartSchema(t *testing.T) {
	page, err := RenderChartValuesSchema(chartMiniSource(readChartMiniSource(t)))
	require.NoError(t, err)

	golden := filepath.Join("testdata", "golden", "chartschema", ChartSchemaFile)
	if updating() {
		require.NoError(t, docgen.WriteFile(golden, page))
	} else {
		assert.Empty(t, docgen.DiffFile(golden, page), "the golden file is out of date: run REFDOC_UPDATE=1 go test ./internal/refdoc and review the diff")
	}

	// the output is valid JSON, in the order of the source, with the subtree where the placeholder was
	require.True(t, json.Valid(page))
	var doc struct {
		Comment    string `json:"$comment"`
		Properties map[string]struct {
			Type       string `json:"type"`
			Additional *bool  `json:"additionalProperties"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(page, &doc))
	assert.Contains(t, doc.Comment, "testdata/chartschema/source.yaml")
	assert.Contains(t, doc.Comment, "the mini configuration")
	cfg := doc.Properties["config"]
	assert.Equal(t, "object", cfg.Type)
	require.NotNil(t, cfg.Additional)
	assert.False(t, *cfg.Additional, "every object of the subtree is closed")

	s := string(page)
	assert.NotContains(t, s, chartConfigMarker, "the placeholder is replaced")
	assert.True(t, strings.HasPrefix(s, "{\n  \"$schema\": "), "the order of the source is kept")
	assert.Less(t, strings.Index(s, `"$schema"`), strings.Index(s, `"$comment"`), "the comment follows $schema")
	assert.Less(t, strings.Index(s, `"$comment"`), strings.Index(s, `"title"`))
	assert.True(t, strings.HasSuffix(s, "}\n") && !strings.HasSuffix(s, "\n\n"), "exactly one newline at the end")
	for i, line := range strings.Split(s, "\n") {
		assert.False(t, strings.HasSuffix(line, " "), "line %d ends with a space", i+1)
	}
	// left out: a secret, a field of the map entries, a field of the list entries, a section, and a field that is not part of the file format
	for _, key := range []string{`"password"`, `"env"`, `"url"`, `"dev"`, `"dataDir"`, `"debug"`} {
		assert.NotContains(t, s, key)
	}
	assert.Contains(t, s, `"allowedHosts"`, "the other field of the map entries stays")
	assert.Contains(t, s, `"minimum": 0`, "an unsigned field cannot be negative")
	assert.Contains(t, s, "<angle brackets> & an ampersand", "angle brackets and ampersands are not escaped")
}

func TestChartSchemaSubtree(t *testing.T) {
	page, err := RenderChartValuesSchema(chartMiniSource(readChartMiniSource(t)))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(page, &doc))
	at := func(path ...string) any {
		var v any = doc
		for _, p := range path {
			m, ok := v.(map[string]any)
			require.True(t, ok, "%v: not an object", path)
			v = m[p]
		}
		return v
	}
	cfg := []string{"properties", "config"}
	assert.Equal(t, "object", at(append(cfg, "type")...))
	assert.Contains(t, at(append(cfg, "description")...), "The chart does not offer these keys: server.password (a secret), server.tls.secrets.<name>.env (the environment is not offered), notify.channels[].url (a secret), dev (the development mode does not exist).")

	server := append(cfg, "properties", "server", "properties")
	assert.Equal(t, "string", at(append(server, "listenAddr", "type")...))
	assert.Equal(t, "`listenAddr` is the address of the listener, as host:port (default \":8080\"). The value *must* not use a [scheme] and the setting_name is escaped.", at(append(server, "listenAddr", "description")...), "the Go name becomes the key, nothing is escaped")
	assert.Equal(t, durationPattern, at(append(server, "timeout", "pattern")...))
	assert.Equal(t, "integer", at(append(server, "maxBodyBytes", "type")...))
	assert.Equal(t, "number", at(append(server, "ratio", "type")...))
	assert.Equal(t, "boolean", at(append(server, "quiet", "type")...))
	assert.Equal(t, "string", at(append(server, "mode", "type")...), "a named string type is a string")
	assert.Equal(t, map[string]any{"type": "string"}, at(append(server, "hosts", "items")...))
	assert.Equal(t, map[string]any{"type": "string"}, at(append(server, "labels", "additionalProperties")...), "an entry of a map is not nullable: a null would reach the configuration file")
	assert.Equal(t, "array", at(append(server, "routes", "additionalProperties", "type")...), "a map of string lists; an entry is not nullable")
	assert.Equal(t, map[string]any{"type": "string"}, at(append(server, "routes", "additionalProperties", "items")...))
	assert.Nil(t, at(append(server, "password")...), "left out")
	assert.Nil(t, at(append(server, "debug")...), "yaml:\"-\" is not part of the file format")
	assert.Equal(t, "`backup` is the backup of the mini server; a field of this type relies on this comment.", at(append(server, "backup", "description")...), "a struct field without a comment takes the comment of its type")
	assert.Equal(t, "`verbose` adds detail to the output (documented on the line of the field).", at(append(server, "verbose", "description")...), "the comment on the line of a field counts")

	secrets := append(server, "tls", "properties", "secrets")
	assert.Equal(t, "object", at(append(secrets, "type")...))
	assert.Equal(t, "object", at(append(secrets, "additionalProperties", "type")...), "an entry of a map is not nullable")
	assert.Equal(t, false, at(append(secrets, "additionalProperties", "additionalProperties")...), "the entries of a map are closed objects")
	assert.Nil(t, at(append(secrets, "additionalProperties", "properties", "env")...), "a field of the map entries is left out")
	assert.NotNil(t, at(append(secrets, "additionalProperties", "properties", "allowedHosts")...))

	channels := append(cfg, "properties", "notify", "properties", "channels")
	assert.Equal(t, "array", at(append(channels, "type")...))
	assert.Equal(t, false, at(append(channels, "items", "additionalProperties")...))
	assert.NotNil(t, at(append(channels, "items", "properties", "name")...))
	assert.Nil(t, at(append(channels, "items", "properties", "url")...), "a field of the list entries is left out")
	assert.Nil(t, at(append(cfg, "properties", "dev")...), "a whole section is left out")

	shapes := append(cfg, "properties", "shapes", "properties")
	assert.Equal(t, map[string]any{"type": "integer", "minimum": float64(0), "description": "`port` is an unsigned port number."}, at(append(shapes, "port")...))
	assert.Equal(t, "integer", at(append(shapes, "retries", "type")...))
	assert.Nil(t, at(append(shapes, "retries", "minimum")...))
}

func TestChartSchemaDurationPattern(t *testing.T) {
	re := regexp.MustCompile(durationPattern)
	for _, ok := range []string{"0", "+0", "-0", "30s", "1m30s", "720h", "1.5h", ".5s", "1.s", "100ms", "5us", "7ns", "-5m", "+1h"} {
		assert.Regexp(t, re, ok, ok)
	}
	for _, bad := range []string{"", "30", "0.5", "1d", "30 s", "s", ".s", "${TIMEOUT}", "1h30", "m", "30S", "1 h", "1h-5m", "0s0"} {
		assert.NotRegexp(t, re, bad, bad)
	}
}

func TestChartSchemaQuoting(t *testing.T) {
	assert.Equal(t, `"a<b>&c"`, quoteJSON("a<b>&c"))
	assert.Equal(t, `"q\"b\\n\n\t\r"`, quoteJSON("q\"b\\n\n\t\r"))
	// control characters and the two line separators are escaped; the escapes are built with string(rune(...)) because the
	// source file must not contain the separators themselves
	lineSep, paraSep := string(rune(0x2028)), string(rune(0x2029))
	assert.Equal(t, `"\u0001\u007f\u2028\u2029"`, quoteJSON("\x01\x7f"+lineSep+paraSep))
	accented, euro := string(rune(0xe4)), string(rune(0x20ac))
	assert.Equal(t, `"`+accented+euro+`"`, quoteJSON(accented+euro), "other characters stay as they are")
}

// TestChartSchemaRefusesWhatItCannotRepresent: the generator stops, with the path, at a structure that it cannot turn into a
// schema, instead of leaving the setting out.
func TestChartSchemaRefusesWhatItCannotRepresent(t *testing.T) {
	source := readChartMiniSource(t)
	run := func(root reflect.Type, files []string, mutate func(*ChartConfigSource)) error {
		src := chartMiniSource(source)
		src.Config.Root, src.Config.SourceFiles, src.Config.Excluded = root, files, nil
		if mutate != nil {
			mutate(&src.Config)
		}
		_, err := RenderChartValuesSchema(src)
		return err
	}
	types := []string{"chartschema_types_test.go"}

	cases := map[string]struct {
		root reflect.Type
		want []string
	}{
		"list of numbers": {reflect.TypeOf(ChartBadSlice{}), []string{"numbers", "[]int", "not supported"}},
		"map of numbers":  {reflect.TypeOf(ChartBadMap{}), []string{"counts", "map[string]int", "not supported"}},
		"time stamp":      {reflect.TypeOf(ChartBadTime{}), []string{"when", "time.Time", "not supported"}},
		"no comment":      {reflect.TypeOf(ChartBadNoComment{}), []string{"name has no description", "ChartBadNoComment.Name"}},
		"open backtick":   {reflect.TypeOf(ChartBadBacktick{}), []string{"name", "unpaired backtick"}},
		"nested":          {reflect.TypeOf(ChartBadNested{}), []string{"inner.numbers", "[]int"}},
		"not a struct":    {reflect.TypeOf(""), []string{"must be a struct"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := run(c.root, types, nil)
			require.Error(t, err)
			for _, w := range c.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}

	t.Run("source file without the declaration", func(t *testing.T) {
		err := run(reflect.TypeOf(ChartBadNoComment{}), []string{"miniconfig_test.go"}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no declaration of the type ChartBadNoComment")
	})

	exclusions := map[string]struct {
		excluded []ChartExclusion
		want     string
	}{
		"stale path":     {[]ChartExclusion{{Path: "server.nothing", Reason: "gone"}}, `the exclusion "server.nothing" matches no setting`},
		"path of a leaf": {[]ChartExclusion{{Path: "server.listenAddr.x", Reason: "gone"}}, `matches no setting`},
		"twice":          {[]ChartExclusion{{Path: "dev", Reason: "a"}, {Path: "dev", Reason: "b"}}, `listed twice`},
		"no reason":      {[]ChartExclusion{{Path: "dev", Reason: " "}}, `needs a path and a reason`},
		"no path":        {[]ChartExclusion{{Reason: "x"}}, `needs a path and a reason`},
	}
	for name, c := range exclusions {
		t.Run("exclusion "+name, func(t *testing.T) {
			err := run(reflect.TypeOf(ChartMini{}), []string{"miniconfig_test.go", "chartschema_types_test.go"}, func(cfg *ChartConfigSource) { cfg.Excluded = c.excluded })
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}

	t.Run("origin and description are required", func(t *testing.T) {
		files := []string{"miniconfig_test.go", "chartschema_types_test.go"}
		require.Error(t, run(reflect.TypeOf(ChartMini{}), files, func(cfg *ChartConfigSource) { cfg.Origin = "" }))
		require.Error(t, run(reflect.TypeOf(ChartMini{}), files, func(cfg *ChartConfigSource) { cfg.Description = "" }))
	})
}

// TestChartSchemaRefusesASourceItCannotRead: what the hand-written part may say is what JSON can say, and the placeholder of the
// generated subtree is unambiguous.
func TestChartSchemaRefusesASourceItCannotRead(t *testing.T) {
	good := readChartMiniSource(t)
	cases := map[string]struct {
		source string
		want   []string
	}{
		"empty":                {"", []string{"the file is empty"}},
		"only a comment":       {"# nothing\n", []string{"the file is empty"}},
		"two documents":        {good + "---\nx: 1\n", []string{"exactly one YAML document"}},
		"not yaml":             {"properties: [\n", []string{"source.yaml"}},
		"list at the root":     {"- a\n- b\n", []string{"must be a mapping"}},
		"anchor":               {strings.Replace(good, "description: Number of replicas.", "description: &d Number of replicas.", 1), []string{"anchors and aliases", "source.yaml:"}},
		"alias":                {strings.Replace(good, "description: Number of replicas.", "description: &d Number of replicas.", 1) + "extra: *d\n", []string{"anchors and aliases"}},
		"merge key":            {"properties:\n  <<: {a: 1}\n  config: {x-generated: the mini configuration}\n", []string{"merge keys"}},
		"key that is a number": {"properties:\n  1: {type: string}\n  config: {x-generated: the mini configuration}\n", []string{"a key must be a string"}},
		"duplicate key":        {strings.Replace(good, "title: Mini chart values", "title: Mini chart values\ntitle: Again", 1), []string{`the key "title" is set twice`}},
		"time stamp":           {strings.Replace(good, "title: Mini chart values", "title: 2026-10-14", 1), []string{"!!timestamp", "quote it"}},
		"infinite number":      {strings.Replace(good, "minimum: 1", "minimum: .inf", 1), []string{"not a finite number"}},
		"huge integer":         {strings.Replace(good, "minimum: 1", "minimum: 9223372036854775808", 1), []string{"fits 64 bits"}},
		"no properties":        {"type: object\n", []string{"the root has no properties"}},
		"no placeholder":       {strings.Replace(good, "  config:\n    x-generated: the mini configuration\n", "", 1), []string{"properties.config must consist of the placeholder"}},
		"wrong origin":         {strings.Replace(good, "x-generated: the mini configuration", "x-generated: somewhere else", 1), []string{"properties.config must consist of the placeholder x-generated: the mini configuration"}},
		"constraint beside it": {strings.Replace(good, "    x-generated: the mini configuration", "    x-generated: the mini configuration\n    type: object", 1), []string{"and nothing else"}},
		"placeholder twice":    {strings.Replace(good, "  host:\n", "  host:\n    x-generated: the mini configuration\n", 1), []string{"must appear once", "/$defs/host/x-generated"}},
		"comment in source":    {strings.Replace(good, "title: Mini chart values", "title: Mini chart values\n$comment: mine", 1), []string{"must not set $comment"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := RenderChartValuesSchema(chartMiniSource(c.source))
			require.Error(t, err)
			for _, w := range c.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}

	t.Run("the good source renders", func(t *testing.T) {
		_, err := RenderChartValuesSchema(chartMiniSource(good))
		require.NoError(t, err)
	})
	t.Run("a source without $schema gets the comment first", func(t *testing.T) {
		page, err := RenderChartValuesSchema(chartMiniSource(strings.Replace(good, "$schema: https://json-schema.org/draft/2020-12/schema\n", "", 1)))
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(page), "{\n  \"$comment\": "))
	})
}
