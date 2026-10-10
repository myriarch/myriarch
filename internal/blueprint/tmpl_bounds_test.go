// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"runtime"
	"strings"
	"testing"
	"text/template"
	"text/template/parse"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

// GIT-4: output size and execution cost of a template are bounded on EVERY
// path. The tests of this file check the paths that turn a value into text
// (printing an action, the string helpers, the formatting helpers, error
// messages) and the time budget.

// transientAllocCap is far below what building the text of the values used in
// these tests would take (some hundred MiB): the limits must apply BEFORE a
// value is serialized, not after the (capped) output has refused the result.
const transientAllocCap = 8 << 20

// allocatedBytes returns how many bytes f allocates in total (garbage
// included). The tests of this package do not run in parallel.
func allocatedBytes(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// sharedDoubling returns template actions that build $l, a list whose text
// is far larger than the value itself: its parts are shared, so the value is
// small in memory while its serialization has 2^(n+1) copies of .params.big
// (n = 12: 8192 x 10 KiB). The limits apply to the size of the text and are
// checked before the text is built; this value shows the difference.
func sharedDoubling(n int) string {
	src := `[[ $l := list .params.big .params.big ]]`
	for i := 0; i < n; i++ {
		src += `[[ $l = list $l $l ]]`
	}
	return src
}

func boundsData() map[string]any {
	return map[string]any{
		"params": map[string]any{
			"big":    strings.Repeat("a", 10<<10),
			"huge":   strings.Repeat("b", maxTemplateOutput+1),
			"many":   make([]any, 2000),
			"list":   []any{"a", "b"},
			"labels": map[string]any{"k": "v"},
			"names":  []any{"x", "y"},
			"n":      3,
			"f":      1.5,
			"on":     true,
			"s":      "text",
			"nilv":   nil,
		},
		"agent": map[string]any{"id": "", "attributes": map[string]string{"site.id": "1"}},
	}
}

// evalWith evaluates one template string like a value of a blueprint.
func evalWith(r *Renderer, expr string) (any, error) {
	out, err := r.EvalValue(map[string]any{"v": expr}, boundsData())
	if err != nil {
		return nil, err
	}
	return out.(map[string]any)["v"], nil
}

// GIT-4: an action that prints a value goes through text/template's own
// formatting, which builds the whole text of a list or map. Lists and maps
// are therefore never printed implicitly: the template must convert them with
// one of the bounded helpers (toJson, join, str).
func TestPrintingCompositesIsRefusedBeforeTheyAreFormatted(t *testing.T) {
	r := NewRenderer(Options{})

	// a value whose text would be 80 MiB
	var err error
	alloc := allocatedBytes(func() { _, err = evalWith(r, sharedDoubling(12)+`x[[ $l ]]`) })
	assert.Less(t, alloc, uint64(transientAllocCap), "the value must be refused before its text is built")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot print a list directly")

	// the rule does not depend on the size: small lists and maps are refused too
	for _, tc := range []struct{ name, expr, kind string }{
		{"list", `x[[ .params.list ]]`, "list"},
		{"map", `x[[ .params.labels ]]`, "map"},
		{"two actions", `[[ .params.list ]]-[[ .params.list ]]`, "list"},
		{"element of a range", `[[ range .params.names ]][[ list . ]][[ end ]]`, "list"},
		{"inside with", `[[ with .params.list ]][[ . ]][[ end ]]`, "list"},
		{"attributes", `x[[ .agent.attributes ]]`, "map"},
		{"result of dict", `x[[ dict "a" 1 ]]`, "map"},
	} {
		_, err := evalWith(r, tc.expr)
		if assert.Error(t, err, tc.name) {
			assert.Contains(t, err.Error(), "cannot print a "+tc.kind+" directly", tc.name)
			assert.Contains(t, err.Error(), "toJson, join or str", tc.name)
		}
	}
	// ... also in map keys
	_, err = r.EvalValue(map[string]any{"[[ .params.list ]]": 1}, boundsData())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot print a list directly")

	// a string above the limit is refused
	_, err = evalWith(r, `x[[ .params.huge ]]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template output exceeds")

	// scalars print as before, and so do the documented conversions
	for expr, want := range map[string]string{
		`x[[ .params.s ]]`:                           "xtext",
		`x[[ .params.n ]]`:                           "x3",
		`x[[ .params.f ]]`:                           "x1.5",
		`x[[ .params.on ]]`:                          "xtrue",
		`x[[ .params.nilv ]]`:                        "x<no value>",
		`x[[ index .params.list 1 ]]`:                "xb",
		`x[[ index .params.labels "k" ]]`:            "xv",
		`[[ range .params.names ]][[ . ]],[[ end ]]`: "x,y,",
		`x[[ join "," .params.list ]]`:               "xa,b",
		`x[[ toJson .params.list ]]`:                 `x["a","b"]`,
		`x[[ str .params.labels ]]`:                  `x{"k":"v"}`,
		`x[[ len .params.list ]]`:                    "x2",
		`x[[ 75.0 ]]`:                                "x75",
		`x[[ 1000000.0 ]]`:                           "x1e+06",
	} {
		got, err := evalWith(r, expr)
		if assert.NoError(t, err, expr) {
			assert.Equal(t, want, got, expr)
		}
	}
	// a whole expression keeps its type (the typed path serializes with toJson)
	got, err := evalWith(r, `[[ .params.list ]]`)
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, got)
}

// The same rule reaches the author of a blueprint as a template_error with a
// message that points to the helpers and does not mention the internals of
// the template engine.
func TestPrintingACompositeIsATemplateErrorInRender(t *testing.T) {
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", []domain.ParameterSpec{
		{Name: "hosts", Type: domain.ParamList, Default: []any{"a", "b"}},
	}, minimalConfig(map[string]any{
		"extensions": map[string]any{"x": map[string]any{"endpoint": "http://[[ .params.hosts ]]/path"}},
	}), nil, nil)}}
	res := renderOne(t, snap, RenderInput{Blueprint: "b"})
	require.True(t, res.Issues.HasErrors())
	is := res.Issues[0]
	assert.Equal(t, "template_error", is.Code)
	assert.Contains(t, is.Message, "cannot print a list directly")
	assert.Contains(t, is.Message, "toJson, join or str")
	assert.NotContains(t, is.Message, "_printGuard")
	assert.NotContains(t, is.Message, "error calling")
	assert.Empty(t, res.Body)

	// the suggested conversions render
	for _, repl := range []string{`[[ join "," .params.hosts ]]`, `[[ toJson .params.hosts ]]`, `[[ index .params.hosts 0 ]]`} {
		snap.Blueprints[0].Config["extensions"].(map[string]any)["x"].(map[string]any)["endpoint"] = "http://" + repl + "/path"
		res = renderOne(t, snap, RenderInput{Blueprint: "b"})
		assert.False(t, res.Issues.HasErrors(), "%s: %+v", repl, res.Issues)
	}
}

// GIT-4: contains, hasPrefix and hasSuffix serialize non-string values like
// the other string helpers: bounded, before the text is built.
func TestContainsHasPrefixHasSuffixAreBounded(t *testing.T) {
	r := NewRenderer(Options{})
	for _, fn := range []string{"contains", "hasPrefix", "hasSuffix"} {
		var err error
		alloc := allocatedBytes(func() { _, err = evalWith(r, sharedDoubling(12)+`[[ `+fn+` "zzz" $l ]]`) })
		assert.Less(t, alloc, uint64(transientAllocCap), "%s must refuse the value before its text is built", fn)
		require.Error(t, err, fn)
		assert.Contains(t, err.Error(), "template output exceeds", fn)

		// strings above the limit are refused like in the other string helpers
		_, err = evalWith(r, `[[ `+fn+` "b" .params.huge ]]`)
		require.Error(t, err, fn)
		assert.Contains(t, err.Error(), "template output exceeds", fn)
	}

	// ordinary use is unchanged: strings, numbers and (as JSON) lists and maps
	for expr, want := range map[string]bool{
		`[[ contains "bc" "abcd" ]]`:              true,
		`[[ contains "x" "abcd" ]]`:               false,
		`[[ hasPrefix "ab" "abcd" ]]`:             true,
		`[[ hasPrefix "bc" "abcd" ]]`:             false,
		`[[ hasSuffix "cd" "abcd" ]]`:             true,
		`[[ hasSuffix "bc" "abcd" ]]`:             false,
		`[[ contains "3" .params.n ]]`:            true,
		`[[ hasPrefix "1." .params.f ]]`:          true,
		`[[ contains "tru" .params.on ]]`:         true,
		`[[ contains "\"b\"" .params.list ]]`:     true,
		`[[ hasPrefix "[" .params.list ]]`:        true,
		`[[ hasSuffix "]" .params.list ]]`:        true,
		`[[ hasPrefix "{\"k\"" .params.labels ]]`: true,
		`[[ .params.s | contains "ex" ]]`:         true,
		`[[ .params.s | hasPrefix "te" ]]`:        true,
		`[[ .params.s | hasSuffix "xt" ]]`:        true,
		`[[ contains "x" .params.nilv ]]`:         false,
		`[[ hasPrefix "" .params.nilv ]]`:         true,
	} {
		got, err := evalWith(r, expr)
		if assert.NoError(t, err, expr) {
			assert.Equal(t, want, got, expr)
		}
	}
}

// GIT-4: the formatting helpers bound ALL their arguments together (not each
// one on its own) and a printf format cannot repeat or widen its arguments.
func TestFormattingHelpersAreBounded(t *testing.T) {
	r := NewRenderer(Options{})
	manyArgs := strings.Repeat(" .params.big", 300) // 300 x 10 KiB

	for _, fn := range []string{"print", "println", "html", "js", "urlquery"} {
		var err error
		alloc := allocatedBytes(func() { _, err = evalWith(r, `[[ `+fn+manyArgs+` ]]`) })
		assert.Less(t, alloc, uint64(transientAllocCap), fn)
		require.Error(t, err, fn)
		assert.Contains(t, err.Error(), "template output exceeds", fn)
	}

	for _, tc := range []struct{ name, expr, why string }{
		{"many arguments", `[[ printf "` + strings.Repeat("%s", 300) + `"` + manyArgs + ` ]]`, "template output exceeds"},
		{"verb repeated through an argument index", `[[ printf "` + strings.Repeat("%[1]s", 300) + `" .params.big ]]`, "explicit argument indexes"},
		{"width taken from an argument index", `[[ printf "` + strings.Repeat("%[1]*d", 100) + `" 1000000 1 ]]`, "explicit argument indexes"},
		{"precision taken from an argument index", `[[ printf "%.[1]*f" 1000000 1.5 ]]`, "explicit argument indexes"},
		{"width taken from an argument", `[[ printf "%*d" 1000000 1 ]]`, "'*' width or precision"},
		{"huge width", `[[ printf "%1000000d" 1 ]]`, "width or precision above"},
		{"huge precision", `[[ printf "%.1000000f" 1.5 ]]`, "width or precision above"},
		{"more verbs than arguments", `[[ printf "` + strings.Repeat("%d", 300) + `" 1 ]]`, "300 verbs but only 1 arguments"},
	} {
		var err error
		alloc := allocatedBytes(func() { _, err = evalWith(r, tc.expr) })
		assert.Less(t, alloc, uint64(transientAllocCap), tc.name)
		if assert.Error(t, err, tc.name) {
			assert.Contains(t, err.Error(), tc.why, tc.name)
		}
	}

	// ordinary formats keep working
	for expr, want := range map[string]string{
		`[[ printf "%s-%d" "a" 3 ]]`:                   "a-3",
		`[[ printf "%05d|%-4s|%.2f|%%" 42 "ab" 1.5 ]]`: "00042|ab  |1.50|%",
		`[[ printf "%+d % d %x %q" 5 5 255 "q" ]]`:     `+5  5 ff "q"`,
		`[[ printf "%v" .params.list ]]`:               "[a b]",
		`[[ printf "%d%%" 50 ]]`:                       "50%",
		`[[ printf "100%" ]]`:                          "100%!(NOVERB)",
		`[[ printf "%s" "a" "b" ]]`:                    "a%!(EXTRA string=b)",
		`[[ printf "no verbs" ]]`:                      "no verbs",
		`[[ print "a" 1 2 "b" ]]`:                      "a1 2b",
	} {
		got, err := evalWith(r, expr)
		if assert.NoError(t, err, expr) {
			assert.Equal(t, want, got, expr)
		}
	}
}

// GIT-4: the error of dict about a key that is not a string names the type of
// the key; it does not format the key.
func TestDictErrorDoesNotFormatItsArguments(t *testing.T) {
	r := NewRenderer(Options{})
	var err error
	alloc := allocatedBytes(func() { _, err = evalWith(r, sharedDoubling(12)+`[[ dict $l 1 ]]`) })
	assert.Less(t, alloc, uint64(transientAllocCap))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dict: key 1 must be a string, got list")
	assert.Less(t, len(err.Error()), 1024)

	_, err = evalWith(r, `[[ dict "a" 1 2 3 ]]x`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dict: key 2 must be a string, got integer")
	_, err = evalWith(r, `[[ dict "a" ]]x`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "odd number of arguments")
}

func TestToStrIsBoundedAndBudgetAware(t *testing.T) {
	var nilBudget *budget // parse time: only the per-call caps apply
	for name, tc := range map[string]struct {
		in   any
		want string
	}{
		"nil":      {nil, ""},
		"string":   {"abc", "abc"},
		"true":     {true, "true"},
		"int":      {-7, "-7"},
		"int64":    {int64(1) << 40, "1099511627776"},
		"float":    {1.5, "1.5"},
		"bytes":    {[]byte("xy"), "xy"},
		"stringer": {time.Second, "1s"},
		"list":     {[]any{"a", 1, nil}, `["a",1,null]`},
		"map":      {map[string]any{"b": 1, "a": []any{true}}, `{"a":[true],"b":1}`},
		"strings":  {map[string]string{"k": "<v>"}, `{"k":"<v>"}`}, // no HTML escaping
	} {
		for _, b := range []*budget{nilBudget, newBudget(time.Hour)} {
			got, err := b.toStr(tc.in)
			if assert.NoError(t, err, name) {
				assert.Equal(t, tc.want, got, name)
			}
		}
	}

	// the size is checked before the text is built
	big := strings.Repeat("a", 10<<10)
	shared := []any{big, big}
	for i := 0; i < 12; i++ {
		shared = []any{shared, shared}
	}
	var err error
	alloc := allocatedBytes(func() { _, err = newBudget(time.Hour).toStr(shared) })
	require.ErrorIs(t, err, errTemplateOutput)
	assert.Less(t, alloc, uint64(1<<20))
	_, err = newBudget(time.Hour).toStr(strings.Repeat("a", maxTemplateOutput+1))
	require.ErrorIs(t, err, errTemplateOutput)
	_, err = nilBudget.toStr(make([]byte, maxTemplateOutput+1))
	require.ErrorIs(t, err, errTemplateOutput)
	got, err := newBudget(time.Hour).toStr(strings.Repeat("a", maxTemplateOutput))
	require.NoError(t, err)
	assert.Len(t, got, maxTemplateOutput)

	// the text it produces counts against the work budget of the render
	b := newBudget(time.Hour)
	part := []any{strings.Repeat("a", 30000)}
	calls := 0
	for ; err == nil && calls < 1000; calls++ {
		_, err = b.toStr(part)
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template helper functions produce more than")
	assert.Less(t, calls, maxRenderWork/30000+5)
	// ... while a string that is passed on as it is produces nothing
	b = newBudget(time.Hour)
	for i := 0; i < 1000; i++ {
		_, err = b.toStr(big)
		require.NoError(t, err)
	}
	assert.Zero(t, b.bytes)

	// and so does the time it takes
	for _, in := range []any{"s", 1, []any{"a"}} {
		_, err = newBudget(-time.Second).toStr(in)
		if assert.Error(t, err, "%v", in) {
			assert.Contains(t, err.Error(), "time budget", "%v", in)
		}
	}
}

// GIT-4: the 5 s budget of a template string is consulted by every executed
// pipeline, not only by range and by the helpers.
func TestTemplateTimeBudgetIsConsultedOnEveryPath(t *testing.T) {
	r := NewRenderer(Options{})
	r.renderTime = -time.Second // exhausted before the first action runs

	for name, expr := range map[string]string{
		"printing a string":        `x[[ .params.s ]]`,
		"printing a number":        `[[ .params.n ]]x`,
		"declaration":              `[[ $x := 1 ]]ok`,
		"declaration, then use":    `[[ $x := 1 ]]ok[[ $x ]]`,
		"assignment":               `[[ $x := 1 ]][[ $x = 2 ]]ok`,
		"if":                       `[[ if true ]]a[[ end ]]`,
		"else if":                  `[[ if false ]]a[[ else if true ]]b[[ end ]]`,
		"with":                     `[[ with .params.s ]]a[[ end ]]`,
		"helper":                   `[[ contains "a" "abc" ]]x`,
		"string helper":            `x[[ lower "ABC" ]]`,
		"range":                    `[[ range .params.names ]]a[[ end ]]`,
		"whole expression (typed)": `[[ .params.n ]]`,
	} {
		_, err := evalWith(r, expr)
		if assert.Error(t, err, name) {
			assert.Contains(t, err.Error(), "time budget", name)
		}
	}

	// a template that only runs through declarations and conditions (no
	// helper, no output) is stopped when its budget is over
	fast := NewRenderer(Options{})
	fast.renderTime = 20 * time.Millisecond
	src := `[[ range .params.many ]]` + strings.Repeat(`[[ $x := 1 ]][[ if true ]][[ end ]]`, 5000) + `[[ end ]]done`
	start := time.Now()
	_, err := evalWith(fast, src)
	elapsed := time.Since(start)
	require.Error(t, err, "the loop of 2000 x 10000 pipelines must not run to the end")
	assert.Contains(t, err.Error(), "time budget")
	assert.Less(t, elapsed, 5*time.Second)

	// the default budget leaves ordinary templates alone
	got, err := evalWith(NewRenderer(Options{}), `[[ $x := 1 ]][[ if true ]]ok[[ end ]][[ .params.n ]]`)
	require.NoError(t, err)
	assert.Equal(t, "ok3", got)
	assert.Equal(t, maxRenderTime, NewRenderer(Options{}).timeBudget())
}

// The instrumentation of the pipelines (checkTree) is transparent: values
// pass through unchanged and every construct behaves as in text/template.
func TestGuardedPipelinesBehaveLikeTextTemplate(t *testing.T) {
	r := NewRenderer(Options{})
	for expr, want := range map[string]string{
		`[[ $a := 1 ]][[ $b := add $a 2 ]]sum=[[ $b ]]`:                                         "sum=3",
		`[[ $a := 1 ]][[ $a = add $a 10 ]]a=[[ $a ]]`:                                           "a=11",
		`[[ if eq .params.n 3 ]]three[[ else ]]other[[ end ]]`:                                  "three",
		`[[ if .params.nilv ]]a[[ else ]]b[[ end ]]`:                                            "b",
		`[[ if .params.list ]]a[[ else ]]b[[ end ]]`:                                            "a",
		`[[ if and .params.on (gt .params.n 1) ]]a[[ end ]]`:                                    "a",
		`[[ if false ]]a[[ else if .params.on ]]b[[ else ]]c[[ end ]]`:                          "b",
		`[[ with .params.s ]][[ . ]]![[ end ]]`:                                                 "text!",
		`[[ with .params.nilv ]]a[[ else ]]b[[ end ]]`:                                          "b",
		`[[ with $x := .params.n ]][[ $x ]][[ end ]]`:                                           "3",
		`[[ if $x := .params.n ]][[ $x ]][[ end ]]`:                                             "3",
		`[[ range $i, $e := .params.names ]][[ if $i ]],[[ end ]][[ $e ]][[ end ]]`:             "x,y",
		`[[ range .params.names ]][[ if eq . "x" ]][[ continue ]][[ end ]][[ . ]][[ end ]]`:     "y",
		`[[ range .params.names ]][[ . ]][[ break ]][[ end ]]`:                                  "x",
		`[[ range .params.nilv ]]a[[ else ]]empty[[ end ]]`:                                     "empty",
		`[[ range $k, $v := .params.labels ]][[ $k ]]=[[ $v ]][[ end ]]`:                        "k=v",
		`[[ $l := list ]][[ range .params.names ]][[ $l = list $l . ]][[ end ]][[ toJson $l ]]`: `[[[],"x"],"y"]`,
		`[[ .params.s | upper | printf "%s!" ]]`:                                                "TEXT!",
		`[[- " a " | trim -]] b`:                                                                "ab",
		`[[ "[[" ]]`:                                                                            "[[",
		`[[ $n := int "7" ]][[ $n ]]`:                                                           "7",
		`[[ print 1 "a" ]]`:                                                                     "1a",
		`[[ default "d" .params.nilv ]]`:                                                        "d",
		`[[ required "need" .params.s ]]`:                                                       "text",
	} {
		got, err := evalWith(r, expr)
		if assert.NoError(t, err, expr) {
			// whole expressions are typed: compare their text form
			assert.Equal(t, want, toText(got), expr)
		}
	}
	// runtime errors keep their messages
	_, err := evalWith(r, `[[ div 1 0 ]]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "division by zero")
	_, err = evalWith(r, `[[ .params.nope ]]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no entry for key")
}

// toText renders the result of a template string for comparisons.
func toText(v any) string {
	s, err := newBudget(time.Hour).toStr(v)
	if err != nil {
		return err.Error()
	}
	return s
}

// guardOf returns the name of the internal function that ends a pipeline ("" if
// the pipeline does not end in a call of one of them).
func guardOf(p *parse.PipeNode) string {
	if p == nil || len(p.Cmds) == 0 {
		return ""
	}
	last := p.Cmds[len(p.Cmds)-1]
	if len(last.Args) == 1 {
		if id, ok := last.Args[0].(*parse.IdentifierNode); ok {
			switch id.Ident {
			case rangeGuardName, printGuardName, tickName:
				return id.Ident
			}
		}
	}
	return ""
}

// Both forms of a template string (the interpolation and the typed form of a
// whole expression) are routed through the guards: every printing action
// through printGuard, every range through rangeGuard, every other pipeline
// through tick.
func TestEveryPipelineOfAParsedTemplateIsGuarded(t *testing.T) {
	for _, src := range []string{
		`[[ .x ]]`,
		`a[[ .x ]]b`,
		`[[ $y := 1 ]][[ if $y ]][[ $y ]][[ else if .z ]][[ .z | upper ]][[ else ]]n[[ end ]]`,
		`[[ range $i, $e := .l ]][[ with $e ]][[ . ]][[ end ]][[ break ]][[ else ]][[ $x := 2 ]][[ end ]]`,
		`[[ with .a ]][[ . ]][[ else with .b ]][[ . ]][[ end ]]`,
	} {
		e := buildEntry(src)
		require.NoError(t, e.err, src)
		forms := []*template.Template{e.tmpl}
		if e.whole {
			forms = append(forms, e.typed)
		}
		for _, tt := range forms {
			guarded := 0
			var walk func(n parse.Node)
			walk = func(n parse.Node) {
				switch x := n.(type) {
				case *parse.ListNode:
					if x != nil {
						for _, c := range x.Nodes {
							walk(c)
						}
					}
				case *parse.ActionNode:
					want := printGuardName
					if len(x.Pipe.Decl) > 0 {
						want = tickName
					}
					assert.Equal(t, want, guardOf(x.Pipe), "%s: %s", src, x)
					guarded++
				case *parse.IfNode:
					assert.Equal(t, tickName, guardOf(x.Pipe), "%s: %s", src, x.Pipe)
					guarded++
					walk(x.List)
					walk(x.ElseList)
				case *parse.WithNode:
					assert.Equal(t, tickName, guardOf(x.Pipe), "%s: %s", src, x.Pipe)
					guarded++
					walk(x.List)
					walk(x.ElseList)
				case *parse.RangeNode:
					assert.Equal(t, rangeGuardName, guardOf(x.Pipe), "%s: %s", src, x.Pipe)
					guarded++
					walk(x.List)
					walk(x.ElseList)
				}
			}
			walk(tt.Tree.Root)
			assert.Positive(t, guarded, src)
		}
	}
}

// GIT-4: error messages of the helpers quote untrusted values (parameters,
// agent attributes, texts) only in a shortened form or name their type.
func TestHelperErrorsDoNotQuoteUntrustedValuesInFull(t *testing.T) {
	r := NewRenderer(Options{})
	data := boundsData()
	data["agent"] = map[string]any{"id": "", "attributes": map[string]string{}}
	eval := func(expr string) error {
		_, err := r.EvalValue(map[string]any{"v": expr}, data)
		return err
	}
	for name, expr := range map[string]string{
		"int":       `x[[ int .params.big ]]`,
		"float":     `x[[ float .params.big ]]`,
		"bool":      `x[[ bool .params.big ]]`,
		"add":       `x[[ add .params.big 1 ]]`,
		"attr":      `x[[ attr .params.big ]]`,
		"param":     `x[[ param .params.big ]]`,
		"required":  `x[[ required .params.big .params.nilv ]]`,
		"int list":  `x[[ int .params.list ]]`,
		"float map": `x[[ float .params.labels ]]`,
		"join type": `x[[ join "," .params.s ]]`,
	} {
		err := eval(expr)
		if assert.Error(t, err, name) {
			assert.Less(t, len(err.Error()), 512, "%s: %v", name, err)
			assert.NotContains(t, err.Error(), strings.Repeat("a", 300), name)
		}
	}
	// the type is named, never the Go type
	err := eval(`x[[ int .params.list ]]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot convert a list")
	assert.NotContains(t, err.Error(), "[]interface")

	// the beginning of the value is still quoted: the author finds the problem
	err = eval(`x[[ int "abc" ]]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"abc" is not an integer`)
}
