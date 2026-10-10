// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/domain"
)

func bp(name, extends string, params []domain.ParameterSpec, config map[string]any, frags []domain.Fragment, remove []string) domain.Blueprint {
	return domain.Blueprint{
		ObjectMeta: domain.ObjectMeta{Name: name},
		BlueprintSpec: domain.BlueprintSpec{
			Extends: extends, Parameters: params, Config: config, Fragments: frags, Remove: remove,
		},
		Source: domain.SourceRef{Path: "blueprints/" + name + ".yaml"},
	}
}

// minimalConfig returns a structurally valid collector config tree with the
// given extra receiver settings.
func minimalConfig(extra map[string]any) map[string]any {
	cfg := map[string]any{
		"receivers": map[string]any{"otlp": map[string]any{}},
		"exporters": map[string]any{"debug": map[string]any{}},
		"service": map[string]any{"pipelines": map[string]any{
			"traces": map[string]any{"receivers": []any{"otlp"}, "exporters": []any{"debug"}},
		}},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func renderOne(t *testing.T, snap *domain.GitOpsSnapshot, in RenderInput) domain.RenderResult {
	t.Helper()
	in.Snapshot = snap
	res, err := NewRenderer(Options{}).Render(in)
	require.NoError(t, err)
	return res
}

func decodeBody(t *testing.T, res domain.RenderResult) map[string]any {
	t.Helper()
	require.False(t, res.Issues.HasErrors(), "%+v", res.Issues)
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(res.Body, &m))
	return m
}

func TestTypedExpressions(t *testing.T) {
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", []domain.ParameterSpec{
		{Name: "port", Type: domain.ParamInt, Default: 8080},
		{Name: "ratio", Type: domain.ParamNumber, Default: 0.5},
		{Name: "on", Type: domain.ParamBool, Default: true},
		{Name: "siteId", Type: domain.ParamString, Default: "0123"},
		{Name: "paths", Type: domain.ParamList, Default: []any{"a", "b"}},
		{Name: "labels", Type: domain.ParamMap, Default: map[string]any{"k": "v"}},
		{Name: "every", Type: domain.ParamDuration, Default: "30s"},
	}, minimalConfig(map[string]any{
		"extensions": map[string]any{"x": map[string]any{
			"port":                     "[[ .params.port ]]",
			"ratio":                    "[[ .params.ratio ]]",
			"on":                       "[[ .params.on ]]",
			"site":                     "[[ .params.siteId ]]",
			"paths":                    "[[ .params.paths ]]",
			"labels":                   "[[ .params.labels ]]",
			"every":                    "[[ .params.every ]]",
			"url":                      "https://[[ .params.siteId ]].example.com:[[ .params.port ]]",
			"env":                      "${env:TOKEN}",
			"escaped":                  "$$1",
			"literal":                  "[[ \"[[\" ]]",
			"piped":                    "[[ .params.siteId | default \"x\" | upper ]]",
			"arith":                    "[[ add .params.port 1 ]]",
			"cond":                     "[[ if .params.on ]]yes[[ else ]]no[[ end ]]",
			"hasattr":                  "[[ hasAttr \"site.id\" ]]",
			"attr":                     "[[ attrOr \"missing\" \"fallback\" ]]",
			"joined":                   "[[ join \",\" .params.paths ]]",
			"zero":                     "[[ int \"007\" ]]",
			"[[ .params.siteId ]]-key": "v",
		}},
	}), nil, nil)}}
	res := renderOne(t, snap, RenderInput{Blueprint: "b", Agent: AgentInput{Attributes: domain.AttributeView{"site.id": "0123"}}})
	got := decodeBody(t, res)
	x := got["extensions"].(map[string]any)["x"].(map[string]any)

	assert.Equal(t, 8080, x["port"])
	assert.Equal(t, 0.5, x["ratio"])
	assert.Equal(t, true, x["on"])
	assert.Equal(t, "0123", x["site"], "leading zero string must stay a string")
	assert.Equal(t, []any{"a", "b"}, x["paths"])
	assert.Equal(t, map[string]any{"k": "v"}, x["labels"])
	assert.Equal(t, "30s", x["every"])
	assert.Equal(t, "https://0123.example.com:8080", x["url"])
	assert.Equal(t, "${env:TOKEN}", x["env"])
	assert.Equal(t, "$$1", x["escaped"])
	assert.Equal(t, "[[", x["literal"])
	assert.Equal(t, "0123", x["piped"])
	assert.Equal(t, 8081, x["arith"])
	assert.Equal(t, "yes", x["cond"])
	assert.Equal(t, true, x["hasattr"])
	assert.Equal(t, "fallback", x["attr"])
	assert.Equal(t, "a,b", x["joined"])
	assert.Equal(t, 7, x["zero"])
	assert.Equal(t, "v", x["0123-key"])
	// Canonical YAML keeps "0123" quoted so it stays a string on the agent.
	assert.Contains(t, string(res.Body), `site: "0123"`)
}

func TestTemplateErrors(t *testing.T) {
	mk := func(tmpl string) *domain.GitOpsSnapshot {
		return &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", nil, minimalConfig(map[string]any{
			"extensions": map[string]any{"x": map[string]any{"v": tmpl}},
		}), nil, nil)}}
	}
	cases := map[string]string{
		"[[ .params.nope ]]":               "template_error",
		"[[ attr \"site.id\" ]]":           "template_error",
		"[[ div 1 0 ]]":                    "template_error",
		"[[ required \"must set\" \"\" ]]": "template_error",
		"[[ .params ":                      "template_syntax",
		"[[ nofunc 1 ]]":                   "template_syntax",
	}
	for tmpl, code := range cases {
		res := renderOne(t, mk(tmpl), RenderInput{Blueprint: "b"})
		require.True(t, res.Issues.HasErrors(), tmpl)
		assert.Equal(t, code, res.Issues[0].Code, tmpl)
		assert.Empty(t, res.Body, "no body on errors")
		assert.Empty(t, res.Hash)
	}
	// duplicate keys after rendering
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", nil, minimalConfig(map[string]any{
		"extensions": map[string]any{"x": map[string]any{"a": 1, "[[ \"a\" ]]": 2}},
	}), nil, nil)}}
	res := renderOne(t, snap, RenderInput{Blueprint: "b"})
	require.True(t, res.Issues.HasErrors())
	assert.Equal(t, "template_error", res.Issues[0].Code)
	assert.Contains(t, res.Issues[0].Message, "duplicate key")
}

func TestMergeSemantics(t *testing.T) {
	base := bp("base", "", nil, minimalConfig(map[string]any{
		"extensions": map[string]any{"x": map[string]any{"list": []any{"a", "b"}, "keep": 1, "nested": map[string]any{"a": 1, "b": 2}, "zero": 5}},
	}), nil, nil)
	child := bp("child", "base", nil, map[string]any{
		"extensions": map[string]any{"x": map[string]any{"list": []any{"c"}, "nested": map[string]any{"b": 3, "c": 4}, "zero": nil}},
	}, []domain.Fragment{{Name: "f", Config: map[string]any{"extensions": map[string]any{"x": map[string]any{"frag": true}}}, Remove: []string{"extensions::x::keep", "does::not::exist"}}},
		[]string{"extensions::x::nested::a"})
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{base, child}}
	res := renderOne(t, snap, RenderInput{Blueprint: "child"})
	got := decodeBody(t, res)
	x := got["extensions"].(map[string]any)["x"].(map[string]any)
	assert.Equal(t, []any{"c"}, x["list"], "lists are replaced, not appended")
	assert.Nil(t, x["zero"], "null replaces")
	_, hasZero := x["zero"]
	assert.True(t, hasZero, "null keeps the key")
	assert.Equal(t, map[string]any{"b": 3, "c": 4}, x["nested"], "maps merge recursively, remove applies after the layer")
	assert.NotContains(t, x, "keep", "fragment remove")
	assert.Equal(t, true, x["frag"])
	assert.Equal(t, []string{"base", "child", "child#f"}, res.Layers)
	// The inputs must not be modified by rendering.
	assert.Equal(t, 1, base.Config["extensions"].(map[string]any)["x"].(map[string]any)["keep"])
}

func TestParameterResolution(t *testing.T) {
	decl := []domain.ParameterSpec{
		{Name: "p", Type: domain.ParamInt, Default: 1, FromAttribute: "site.p"},
	}
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", decl, minimalConfig(map[string]any{
		"extensions": map[string]any{"x": map[string]any{"p": "[[ .params.p ]]"}},
	}), nil, nil)}}
	fleet := &domain.Fleet{ObjectMeta: domain.ObjectMeta{Name: "f"}, FleetSpec: domain.FleetSpec{Parameters: map[string]any{"p": 2}}}
	attrs := domain.AttributeView{"site.p": "3"}

	check := func(in RenderInput, want int, src string) {
		t.Helper()
		res := renderOne(t, snap, in)
		require.False(t, res.Issues.HasErrors(), "%+v", res.Issues)
		assert.Equal(t, want, res.Parameters["p"])
		assert.Equal(t, src, res.ParameterSources["p"])
	}
	check(RenderInput{Blueprint: "b"}, 1, "default")
	check(RenderInput{Blueprint: "b", Fleet: fleet}, 2, "fleet")
	check(RenderInput{Blueprint: "b", Fleet: fleet, Agent: AgentInput{Attributes: attrs}}, 3, "attribute")
	check(RenderInput{Blueprint: "b", Fleet: fleet, Agent: AgentInput{Attributes: attrs}, InventoryParameters: map[string]any{"p": 4}}, 4, "inventory")
	check(RenderInput{Blueprint: "b", Fleet: fleet, Agent: AgentInput{Attributes: attrs}, InventoryParameters: map[string]any{"p": 4}, Overrides: map[string]any{"p": 5}}, 5, "override")
	// JSON numbers of REST previews arrive as float64.
	check(RenderInput{Blueprint: "b", Overrides: map[string]any{"p": float64(9)}}, 9, "override")

	// attribute of the wrong type
	res := renderOne(t, snap, RenderInput{Blueprint: "b", Agent: AgentInput{Attributes: domain.AttributeView{"site.p": "abc"}}})
	require.True(t, res.Issues.HasErrors())
	assert.Equal(t, "invalid_parameter_value", res.Issues[0].Code)
}

func TestParameterConstraintsAndRequired(t *testing.T) {
	min, max := 10.0, 20.0
	decl := []domain.ParameterSpec{
		{Name: "level", Type: domain.ParamString, Default: "info", Enum: []any{"info", "debug"}},
		{Name: "name", Type: domain.ParamString, Default: "ab12", Pattern: `[a-z]+[0-9]+`},
		{Name: "n", Type: domain.ParamInt, Default: 15, Min: &min, Max: &max},
		{Name: "req", Type: domain.ParamString, Required: true},
	}
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", decl, minimalConfig(nil), nil, nil)}}
	codes := func(in RenderInput) []string {
		res := renderOne(t, snap, in)
		var out []string
		for _, is := range res.Issues {
			out = append(out, is.Code)
		}
		return out
	}
	assert.Equal(t, []string{"missing_parameter"}, codes(RenderInput{Blueprint: "b"}))
	ov := func(k string, v any) RenderInput {
		return RenderInput{Blueprint: "b", Overrides: map[string]any{"req": "x", k: v}}
	}
	assert.Empty(t, codes(ov("level", "debug")))
	assert.Equal(t, []string{"invalid_parameter_value"}, codes(ov("level", "trace")))
	assert.Equal(t, []string{"invalid_parameter_value"}, codes(ov("name", "AB12")))
	assert.Equal(t, []string{"invalid_parameter_value"}, codes(ov("name", "ab12x!")))
	assert.Equal(t, []string{"invalid_parameter_value"}, codes(ov("n", 9)))
	assert.Equal(t, []string{"invalid_parameter_value"}, codes(ov("n", 21)))
	assert.Empty(t, codes(ov("n", 20)))
	assert.Equal(t, []string{"unknown_parameter"}, codes(ov("nope", 1)))
}

func TestParameterRedeclaration(t *testing.T) {
	parent := bp("parent", "", []domain.ParameterSpec{
		{Name: "a", Type: domain.ParamString, Default: "p", Required: true, Enum: []any{"p", "q"}},
		{Name: "b", Type: domain.ParamInt, Default: 1},
	}, minimalConfig(map[string]any{"extensions": map[string]any{"x": map[string]any{"a": "[[ .params.a ]]", "b": "[[ .params.b ]]"}}}), nil, nil)
	child := bp("child", "parent", []domain.ParameterSpec{
		{Name: "a", Type: domain.ParamString, Default: "free"}, // replaces the whole declaration: enum and required are gone
		{Name: "c", Type: domain.ParamBool, Default: false},
	}, nil, nil, nil)
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{parent, child}}
	res := renderOne(t, snap, RenderInput{Blueprint: "child"})
	got := decodeBody(t, res)
	assert.Equal(t, "free", got["extensions"].(map[string]any)["x"].(map[string]any)["a"])

	bad := bp("bad", "parent", []domain.ParameterSpec{{Name: "b", Type: domain.ParamString, Default: "x"}}, nil, nil, nil)
	snap = &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{parent, bad}}
	res = renderOne(t, snap, RenderInput{Blueprint: "bad"})
	require.True(t, res.Issues.HasErrors())
	assert.Equal(t, "parameter_type_conflict", res.Issues[0].Code)
}

func TestFragments(t *testing.T) {
	frag := func(when string) domain.Fragment {
		return domain.Fragment{Name: "f", When: when, Config: map[string]any{"extensions": map[string]any{"x": map[string]any{"on": true}}}}
	}
	mk := func(when string) RenderInput {
		return RenderInput{Blueprint: "b"}
	}
	build := func(when string) *domain.GitOpsSnapshot {
		return &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", []domain.ParameterSpec{{Name: "n", Type: domain.ParamInt, Default: 3}},
			minimalConfig(nil), []domain.Fragment{frag(when)}, nil)}}
	}
	// empty = always
	assert.Equal(t, []string{"b", "b#f"}, renderOne(t, build(""), mk("")).Layers)
	assert.Equal(t, []string{"b", "b#f"}, renderOne(t, build("[[ gt .params.n 1 ]]"), mk("")).Layers)
	assert.Equal(t, []string{"b"}, renderOne(t, build("[[ gt .params.n 5 ]]"), mk("")).Layers)
	res := renderOne(t, build("[[ .params.n ]]"), mk(""))
	require.True(t, res.Issues.HasErrors())
	assert.Equal(t, "when_not_bool", res.Issues[0].Code)
	res = renderOne(t, build("n > 1"), mk(""))
	require.True(t, res.Issues.HasErrors())
	assert.Equal(t, "template_syntax", res.Issues[0].Code)
}

func TestExtendsErrors(t *testing.T) {
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("a", "b", nil, nil, nil, nil), bp("b", "a", nil, nil, nil, nil), bp("c", "ghost", nil, nil, nil, nil)}}
	res := renderOne(t, snap, RenderInput{Blueprint: "a"})
	require.Len(t, res.Issues, 1)
	assert.Equal(t, "extends_cycle", res.Issues[0].Code)
	res = renderOne(t, snap, RenderInput{Blueprint: "c"})
	assert.Equal(t, "blueprint_not_found", res.Issues[0].Code)
	res = renderOne(t, snap, RenderInput{Blueprint: "zzz"})
	assert.Equal(t, "blueprint_not_found", res.Issues[0].Code)
	assert.NotNil(t, res.Issues)
}

func TestRenderResultInvariants(t *testing.T) {
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", nil, minimalConfig(nil), nil, nil)}}
	res := renderOne(t, snap, RenderInput{Blueprint: "b"})
	assert.NotNil(t, res.Issues, "Issues is never nil")
	assert.Equal(t, domain.ConfigHash(res.Body), res.Hash)
	assert.True(t, strings.HasSuffix(string(res.Body), "\n"))
	// determinism
	again := renderOne(t, snap, RenderInput{Blueprint: "b"})
	assert.Equal(t, res.Body, again.Body)
}

func TestInventoryAndFleetMatching(t *testing.T) {
	snap := &domain.GitOpsSnapshot{
		Inventories: []domain.Inventory{
			{ObjectMeta: domain.ObjectMeta{Name: "b-second"}, InventorySpec: domain.InventorySpec{Entries: []domain.InventoryEntry{
				{Match: map[string]string{"site.id": "1"}, Labels: map[string]string{"pilot": "no", "extra": "x"}, Parameters: map[string]any{"p": 2}},
			}}},
			{ObjectMeta: domain.ObjectMeta{Name: "a-first"}, InventorySpec: domain.InventorySpec{Entries: []domain.InventoryEntry{
				{Match: map[string]string{"site.id": "1"}, Labels: map[string]string{"pilot": "yes"}, Parameters: map[string]any{"p": 1, "q": 1}},
				{Match: map[string]string{"site.id": "2"}, Labels: map[string]string{"never": "1"}},
				{Match: map[string]string{"labels.pilot": "yes"}, Labels: map[string]string{"nope": "1"}},
			}}},
		},
		Fleets: []domain.Fleet{
			{ObjectMeta: domain.ObjectMeta{Name: "low"}, FleetSpec: domain.FleetSpec{Priority: 1}},
			{ObjectMeta: domain.ObjectMeta{Name: "high"}, FleetSpec: domain.FleetSpec{Priority: 10, Selector: domain.Selector{MatchAttributes: map[string]string{"site.id": "1"}}}},
			{ObjectMeta: domain.ObjectMeta{Name: "tie-a"}, FleetSpec: domain.FleetSpec{Priority: 10, Selector: domain.Selector{MatchAttributes: map[string]string{"site.id": "9"}}}},
			{ObjectMeta: domain.ObjectMeta{Name: "tie-b"}, FleetSpec: domain.FleetSpec{Priority: 10, Selector: domain.Selector{MatchAttributes: map[string]string{"site.id": "9"}}}},
		},
	}
	inv := ApplyInventory(snap, map[string]string{"site.id": "1"})
	assert.Equal(t, map[string]string{"pilot": "no", "extra": "x"}, inv.Labels, "later inventories (sorted by name) override earlier ones; labels.* match is ignored")
	assert.Equal(t, map[string]any{"p": 2, "q": 1}, inv.Parameters)

	m := MatchFleet(snap, domain.AttributeView{"site.id": "1"})
	require.NotNil(t, m.Fleet)
	assert.Equal(t, "high", m.Fleet.Name)
	assert.Equal(t, []string{"high", "low"}, m.Candidates)

	m = MatchFleet(snap, domain.AttributeView{"site.id": "9"})
	assert.Nil(t, m.Fleet)
	assert.Equal(t, "ambiguous: fleets tie-a, tie-b have equal priority 10", m.Error)

	m = MatchFleet(&domain.GitOpsSnapshot{}, domain.AttributeView{})
	assert.Nil(t, m.Fleet)
	assert.Empty(t, m.Error)
	assert.NotNil(t, m.Candidates)
}

func TestWindowHelpers(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "21:30": 21*60 + 30, "23:59": 23*60 + 59} {
		got, ok := ParseClock(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "1:30", "24:00", "12:60", "12-30", "ab:cd", "12:300"} {
		_, ok := ParseClock(bad)
		assert.False(t, ok, bad)
	}
	d, ok := ParseWeekday("tue")
	assert.True(t, ok)
	assert.Equal(t, time.Tuesday, d)
	_, ok = ParseWeekday("Funday")
	assert.False(t, ok)
}

// The Go alias table must equal the verified fixture.
func TestAliasTableMatchesFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/component-aliases-0.162.0.json")
	require.NoError(t, err)
	var f struct {
		Aliases map[string]map[string]string `json:"aliases"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	assert.Equal(t, f.Aliases, builtinAliases)
	total := 0
	for _, m := range builtinAliases {
		total += len(m)
	}
	assert.Equal(t, 56, total)
}

func comps(kind string, types ...string) *domain.ComponentSet {
	m := map[string]domain.ComponentDetails{}
	for _, t := range types {
		m[t] = domain.ComponentDetails{}
	}
	return &domain.ComponentSet{Components: map[string]map[string]domain.ComponentDetails{kind: m}}
}

func TestValidateConfig(t *testing.T) {
	r := NewRenderer(Options{})
	codes := func(body string, cs *domain.ComponentSet) []string {
		var out []string
		for _, is := range r.ValidateConfig([]byte(body), cs) {
			out = append(out, string(is.Severity)+":"+is.Code)
		}
		return out
	}
	valid := `
receivers:
  otlp: {}
  hostmetrics: {}
processors:
  batch: {}
exporters:
  otlp: {}
  debug:
connectors:
  forward: {}
extensions:
  health_check: {}
service:
  extensions: [health_check]
  pipelines:
    metrics/in:
      receivers: [otlp, hostmetrics]
      processors: [batch]
      exporters: [forward]
    metrics/out:
      receivers: [forward]
      exporters: [otlp, debug]
`
	got := codes(valid, nil)
	assert.ElementsMatch(t, []string{"warning:deprecated_component", "warning:deprecated_component"}, got, "hostmetrics (receiver alias) and exporter otlp (alias of otlp_grpc)")

	// receiver otlp is NOT an alias; exporter otlp is.
	for _, is := range r.ValidateConfig([]byte(valid), nil) {
		assert.Contains(t, []string{"receivers::hostmetrics", "exporters::otlp"}, is.Path)
	}

	assert.Equal(t, []string{"error:yaml_syntax"}, codes("a: [", nil))
	assert.Equal(t, []string{"error:config_invalid_toplevel"}, codes("- a\n- b\n", nil))
	assert.Contains(t, codes("receivers: {a/b: {}}\nservice: {pipelines: {logs: {receivers: [a/b], exporters: [x]}}}", nil), "error:undefined_component")
	assert.Contains(t, codes("receivers: {otlp: {}}\nexporters: {debug: {}}\nservice: {pipelines: {weird: {receivers: [otlp], exporters: [debug]}}}", nil), "error:invalid_pipeline")
	assert.Contains(t, codes("receivers: {otlp: {}}\nexporters: {debug: {}}\nservice: {extensions: [ghost], pipelines: {logs: {receivers: [otlp], exporters: [debug]}}}", nil), "error:undefined_component")
	assert.Contains(t, codes("receivers: {otlp: {}}\nexporters: {debug: {}}\nservice: {pipelines: {logs: {receivers: [otlp, otlp], exporters: [debug]}}}", nil), "error:invalid_pipeline")
	assert.Contains(t, codes("receivers: {'ot lp': {}}\nexporters: {debug: {}}\nservice: {pipelines: {logs: {receivers: ['ot lp'], exporters: [debug]}}}", nil), "error:invalid_component_id")

	// env syntax
	env := func(v string) []string {
		return codes("receivers: {otlp: {endpoint: '"+v+"'}}\nexporters: {debug: {}}\nservice: {pipelines: {logs: {receivers: [otlp], exporters: [debug]}}}", nil)
	}
	assert.Empty(t, env("${env:HOME}"))
	assert.Empty(t, env("${HOME}"))
	assert.Empty(t, env("${env:X:-default}"))
	assert.Empty(t, env("$${not_a_var}"))
	assert.Equal(t, []string{"warning:env_syntax"}, env("${env:UNTERMINATED"))
	assert.Equal(t, []string{"warning:env_syntax"}, env("${bogus:thing}"))
}

// The component set of an agent contains new AND old names; the checked-in
// sim list only new names: aliases must resolve in both directions.
func TestComponentAvailabilityResolvesAliasesBothWays(t *testing.T) {
	r := NewRenderer(Options{})
	cfg := func(kind, id string) string {
		tree := map[string]any{
			"receivers": map[string]any{"otlp": map[string]any{}},
			"exporters": map[string]any{"debug": map[string]any{}},
			"service": map[string]any{"pipelines": map[string]any{"logs": map[string]any{
				"receivers": []any{"otlp"}, "exporters": []any{"debug"},
			}}},
		}
		tree[kind].(map[string]any)[id] = map[string]any{}
		pipe := tree["service"].(map[string]any)["pipelines"].(map[string]any)["logs"].(map[string]any)
		switch kind {
		case "receivers":
			pipe["receivers"] = []any{"otlp", id}
		case "exporters":
			pipe["exporters"] = []any{"debug", id}
		}
		b, err := yaml.Marshal(tree)
		require.NoError(t, err)
		return string(b)
	}
	hasUnknown := func(body string, cs *domain.ComponentSet) bool {
		for _, is := range r.ValidateConfig([]byte(body), cs) {
			if is.Code == "unknown_component" {
				return true
			}
		}
		return false
	}
	// Set has only the NEW name (otelcol-contrib components, sim list): old name in config is fine.
	assert.False(t, hasUnknown(cfg("receivers", "hostmetrics"), comps("receivers", "otlp", "host_metrics")))
	assert.False(t, hasUnknown(cfg("receivers", "host_metrics"), comps("receivers", "otlp", "host_metrics")))
	// Set has only the OLD name: new name in config is fine too.
	assert.False(t, hasUnknown(cfg("receivers", "host_metrics"), comps("receivers", "otlp", "hostmetrics")))
	// Both names (what the opamp extension reports).
	assert.False(t, hasUnknown(cfg("receivers", "hostmetrics"), comps("receivers", "otlp", "hostmetrics", "host_metrics")))
	// Exporter otlp is an alias of otlp_grpc, receiver otlp is not.
	assert.False(t, hasUnknown(cfg("exporters", "otlp"), comps("exporters", "debug", "otlp_grpc")))
	assert.True(t, hasUnknown(cfg("receivers", "nonexistent"), comps("receivers", "otlp")))
	assert.True(t, hasUnknown(cfg("exporters", "nonexistent"), comps("exporters", "debug")))
	// Without a component list nothing is checked.
	assert.False(t, hasUnknown(cfg("receivers", "nonexistent"), nil))
}
