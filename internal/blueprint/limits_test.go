// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

// GIT-4: template execution has bounded output and bounded cost.
func TestTemplateCostIsBounded(t *testing.T) {
	r := NewRenderer(Options{})
	eval := func(expr string) (any, error) {
		return r.EvalValue(map[string]any{"v": expr}, map[string]any{"params": map[string]any{"n": 3, "list": []any{"a", "b"}}, "agent": map[string]any{"id": 7}})
	}
	rejected := []struct{ name, expr, why string }{
		{"range over a literal", "[[ range 100000000 ]]x[[ end ]]", "range over a number"},
		{"range over an int conversion", "[[ range int 1000000000 ]]x[[ end ]]", "integer"},
		{"range over arithmetic", "[[ range (mul 100000 100000) ]]x[[ end ]]", "integer"},
		{"range over len", "[[ range (len .params.list) ]]x[[ end ]]", "integer"},
		{"nested ranges beyond the limit", "[[ range .params.list ]][[ range $.params.list ]][[ range $.params.list ]][[ range $.params.list ]]x[[ end ]][[ end ]][[ end ]][[ end ]]", "nested deeper"},
		{"huge printf width", `[[ printf "%1000000000d" 1 ]]`, "width or precision"},
		{"huge printf precision", `[[ printf "%.1000000000f" 1.5 ]]`, "width or precision"},
		{"star width", `[[ printf "%*d" 99999999 1 ]]`, "not allowed"},
		{"range over a variable holding an integer", "[[ $n := int 5 ]][[ range $n ]]x[[ end ]]", "range over"},
		{"range over a parameter integer", "[[ range .params.n ]]x[[ end ]]", "range over"},
		{"range over a string", `[[ range "abc" ]]x[[ end ]]`, "range over"},
		{"range over a huge variable integer", "[[ $n := int 1000000000 ]][[ range $n ]]x[[ end ]]", "range over"},
		{"range over a field integer", "[[ range .agent.id ]]x[[ end ]]", "range over"},
		{"empty replace pattern", `[[ replace "" "xx" "abc" ]]`, "must not be empty"},
		{"define is refused", `[[ define "x" ]]y[[ end ]]z`, "not allowed"},
		{"template call is refused", `[[ template "x" ]]`, ""},
		{"output beyond the limit", `[[ range .params.list ]][[ range $.params.list ]][[ range $.params.list ]]` + strings.Repeat("0123456789", 7000) + `[[ end ]][[ end ]][[ end ]]`, "template output exceeds"},
	}
	for _, tc := range rejected {
		out, err := eval(tc.expr)
		if assert.Error(t, err, "%s must be rejected (got %v)", tc.name, out) {
			assert.Contains(t, err.Error(), tc.why, tc.name)
		}
	}
	// ordinary templates keep working
	for _, ok := range []string{
		`[[ range .params.list ]][[ . ]],[[ end ]]`,
		`[[ printf "%05d" 42 ]]`,
		`[[ printf "%-10s|" "a" ]]`,
		`[[ printf "%.2f" 1.5 ]]`,
		`[[ range .params.list ]][[ range $.params.list ]][[ . ]][[ end ]][[ end ]]`,
	} {
		_, err := eval(ok)
		assert.NoError(t, err, ok)
	}
}

func TestSafePrintf(t *testing.T) {
	s, err := safePrintf("%s-%d", "a", 3)
	require.NoError(t, err)
	assert.Equal(t, "a-3", s)
	_, err = safePrintf("%1000d", 1)
	assert.Error(t, err)
	_, err = safePrintf("%.999s", "x")
	assert.Error(t, err)
	_, err = safePrintf("%*d", 5, 1)
	assert.Error(t, err)
	_, err = safePrintf("%256d", 1)
	assert.NoError(t, err)

	// every verb prints its own argument: no argument indexes, no more verbs than arguments
	for _, format := range []string{"%[1]d", "%[1]*d", "%.[1]*f", "%5[1]d", "%d%[1]d"} {
		_, err = safePrintf(format, 1)
		if assert.Error(t, err, format) {
			assert.Contains(t, err.Error(), "explicit argument indexes", format)
		}
	}
	// (the formats are variables: they are wrong on purpose)
	for format, args := range map[string][]any{"%s%s": {"a"}, "%d%d%d": {1, 2}, "%s": nil} {
		_, err = safePrintf(format, args...)
		if assert.Error(t, err, format) {
			assert.Contains(t, err.Error(), "verbs but only", format)
		}
	}
	for _, tc := range []struct {
		format string
		args   []any
		want   string
	}{
		{"%d%%|%5%|%s", []any{5, "x"}, "5%|%|x"}, // a literal percent sign takes no argument
		{"100%", nil, "100%!(NOVERB)"},
		{"%s", []any{"a", "b"}, "a%!(EXTRA string=b)"}, // extra arguments are bounded by argsWithin
	} {
		s, err = safePrintf(tc.format, tc.args...)
		require.NoError(t, err, tc.format)
		assert.Equal(t, tc.want, s, tc.format)
	}
}

// GIT-4: the canonical body of a rendered config is capped.
func TestRenderedBodyIsCapped(t *testing.T) {
	big := strings.Repeat("x", 60<<10) // below the per-string limit, but many of them add up
	cfg := map[string]any{}
	for i := 0; i < 40; i++ {
		cfg["key"+strings.Repeat("k", i)] = big
	}
	snap := &domain.GitOpsSnapshot{Blueprints: []domain.Blueprint{bp("b", "", nil, minimalConfig(map[string]any{"extensions": map[string]any{"x": cfg}}), nil, nil)}}
	fleet := domain.Fleet{ObjectMeta: domain.ObjectMeta{Name: "f"}, FleetSpec: domain.FleetSpec{Blueprint: "b"}}
	snap.Fleets = []domain.Fleet{fleet}
	_, err := NewRenderer(Options{}).Render(RenderInput{Snapshot: snap, Fleet: &snap.Fleets[0]})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the limit")
}

func TestExecutorTimeoutIsCappedInValidation(t *testing.T) {
	mk := func(timeout time.Duration) domain.Issues {
		snap := &domain.GitOpsSnapshot{Executors: []domain.Executor{{
			ObjectMeta: domain.ObjectMeta{Name: "hook"},
			ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorWebhook, Webhook: &domain.WebhookExecutorSpec{
				URL: "https://ci.example.com/hook", SecretEnv: "MYRIARCH_EXEC_X", Timeout: domain.Duration(timeout)}},
		}}}
		return NewRenderer(Options{}).ValidateSnapshot(snap, ValidateOptions{})
	}
	for _, is := range mk(5 * time.Minute) {
		if is.Code == "executor_invalid" {
			t.Fatalf("5m is allowed: %+v", is)
		}
	}
	found := false
	for _, is := range mk(time.Hour) {
		found = found || (is.Code == "executor_invalid" && strings.Contains(is.Message, "must not exceed"))
	}
	if !found {
		t.Fatal("a timeout above the cap must be rejected")
	}
}

// GIT-4: helper results and iteration counts are bounded inside one render.
func TestTemplateHelpersAreBounded(t *testing.T) {
	r := NewRenderer(Options{})
	big := strings.Repeat("a", 2000)
	data := map[string]any{"params": map[string]any{
		"big":   big,
		"many":  make([]any, 6000),
		"names": []any{"a", "b"},
		"n":     3,
	}, "agent": map[string]any{"id": ""}}
	eval := func(expr string) (any, error) {
		out, err := r.EvalValue(map[string]any{"v": expr}, data)
		if err != nil {
			return nil, err
		}
		return out.(map[string]any)["v"], nil
	}

	// amplification of a 2 KB value through replace stays below the cap
	_, err := eval(`[[ replace "a" .params.big .params.big ]]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template output exceeds")
	// a doubling list cannot be expanded past the cap by toJson, join or printf
	for _, tail := range []string{`toJson $l`, `join "," $l`, `printf "%v" $l`, `str $l`} {
		src := `[[ $l := list .params.big .params.big ]]`
		for i := 0; i < 20; i++ {
			src += `[[ $l = list $l $l ]]`
		}
		_, err = eval(src + `[[ ` + tail + ` ]]`)
		require.Error(t, err, tail)
		assert.Contains(t, err.Error(), "template output exceeds", tail)
	}
	// iterating two nested collections exceeds the element budget
	_, err = eval(`[[ range .params.many ]][[ range $.params.many ]]x[[ end ]][[ end ]]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iterates over more than")
	// ranges over lists and maps keep working
	got, err := eval(`[[ range $i, $n := .params.names ]][[ $i ]][[ $n ]][[ end ]]`)
	require.NoError(t, err)
	assert.Equal(t, "0a1b", got)
	got, err = eval(`[[ range $k, $v := dict "x" 1 "y" 2 ]][[ $k ]][[ $v ]][[ end ]]`)
	require.NoError(t, err)
	assert.Equal(t, "x1y2", got)
	// a list is an expression result: the typed whole-expression path works
	got, err = eval(`[[ split "," "a,b,c" ]]`)
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b", "c"}, got)
	// split into more parts than allowed
	_, err = eval(`[[ split "" .params.big ]][[ range (split "" (printf "%s%s%s%s%s%s" .params.big .params.big .params.big .params.big .params.big .params.big)) ]]x[[ end ]]`)
	require.Error(t, err)
}
