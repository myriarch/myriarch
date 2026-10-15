// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaValidator checks a value (decoded JSON: map[string]any, []any, string, float64, bool and nil) against a JSON Schema. It
// implements the keywords that the values schema of the Helm chart uses, with their meaning in draft 2020-12, so that go test
// can check the schema on every platform without the helm binary. ci/tests/helm-messages-test.sh checks the same schema with the
// real Helm. A keyword that the validator does not know is reported, never ignored: a new keyword in the schema either gets an
// implementation here or fails the test.
type schemaValidator struct {
	root map[string]any
	re   map[string]*regexp.Regexp
}

func newSchemaValidator(root map[string]any) *schemaValidator {
	return &schemaValidator{root: root, re: map[string]*regexp.Regexp{}}
}

// annotationKeywords carry no assertion.
var annotationKeywords = map[string]bool{
	"$schema": true, "$comment": true, "$id": true, "$defs": true, "title": true, "description": true, "default": true,
	"examples": true, "deprecated": true, "readOnly": true, "writeOnly": true,
}

// Validate returns the problems of the value as "<JSON pointer>: <message>", sorted, or nothing when the value is valid.
func (v *schemaValidator) Validate(value any) []string {
	problems := v.check(value, v.root, "")
	sort.Strings(problems)
	return problems
}

// valid reports whether the value satisfies the schema, for anyOf, oneOf and not.
func (v *schemaValidator) valid(value, schema any) bool { return len(v.check(value, schema, "")) == 0 }

func (v *schemaValidator) check(value, schema any, ptr string) []string {
	at := func(format string, args ...any) []string {
		return []string{ptr + ": " + fmt.Sprintf(format, args...)}
	}
	switch s := schema.(type) {
	case bool:
		if s {
			return nil
		}
		return at("false schema")
	case map[string]any:
		return v.checkObject(value, s, ptr)
	}
	return at("the schema is neither an object nor a boolean: %T", schema)
}

func (v *schemaValidator) checkObject(value any, s map[string]any, ptr string) []string {
	var out []string
	fail := func(format string, args ...any) { out = append(out, ptr+": "+fmt.Sprintf(format, args...)) }
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		arg := s[k]
		switch k {
		case "$ref":
			target, err := v.resolve(arg)
			if err != nil {
				fail("%v", err)
				continue
			}
			out = append(out, v.check(value, target, ptr)...)
		case "type":
			if !typeMatches(value, arg) {
				fail("is %s, want %v", jsonTypeOf(value), arg)
			}
		case "enum":
			found := false
			for _, e := range arg.([]any) {
				if reflect.DeepEqual(e, value) {
					found = true
				}
			}
			if !found {
				fail("%v is not one of %v", value, arg)
			}
		case "const":
			if !reflect.DeepEqual(arg, value) {
				fail("%v is not %v", value, arg)
			}
		case "pattern":
			if str, ok := value.(string); ok && !v.regexp(arg.(string)).MatchString(str) {
				fail("%q does not match %s", str, arg)
			}
		case "minLength":
			if str, ok := value.(string); ok && float64(utf8.RuneCountInString(str)) < arg.(float64) {
				fail("%q is shorter than %v", str, arg)
			}
		case "maxLength":
			if str, ok := value.(string); ok && float64(utf8.RuneCountInString(str)) > arg.(float64) {
				fail("%q is longer than %v", str, arg)
			}
		case "minimum":
			if n, ok := value.(float64); ok && n < arg.(float64) {
				fail("%v is below %v", n, arg)
			}
		case "maximum":
			if n, ok := value.(float64); ok && n > arg.(float64) {
				fail("%v is above %v", n, arg)
			}
		case "required":
			if obj, ok := value.(map[string]any); ok {
				for _, name := range arg.([]any) {
					if _, present := obj[name.(string)]; !present {
						fail("is missing the property %s", name)
					}
				}
			}
		case "properties":
			obj, ok := value.(map[string]any)
			if !ok {
				continue
			}
			for name, sub := range arg.(map[string]any) {
				if member, present := obj[name]; present {
					out = append(out, v.check(member, sub, ptr+"/"+escapePointer(name))...)
				}
			}
		case "patternProperties":
			obj, ok := value.(map[string]any)
			if !ok {
				continue
			}
			patterns := arg.(map[string]any)
			for _, pattern := range sortedSchemaKeys(patterns) {
				re := v.regexp(pattern)
				for _, name := range sortedSchemaKeys(obj) {
					if re.MatchString(name) {
						out = append(out, v.check(obj[name], patterns[pattern], ptr+"/"+escapePointer(name))...)
					}
				}
			}
		case "additionalProperties":
			obj, ok := value.(map[string]any)
			if !ok {
				continue
			}
			props, _ := s["properties"].(map[string]any)
			patterns, _ := s["patternProperties"].(map[string]any)
			for name, member := range obj {
				if _, known := props[name]; known {
					continue
				}
				if v.matchesAnyPattern(name, patterns) {
					continue
				}
				if arg == false {
					fail("additional property %q is not allowed", name)
				} else if arg != true {
					out = append(out, v.check(member, arg, ptr+"/"+escapePointer(name))...)
				}
			}
		case "items":
			if list, ok := value.([]any); ok {
				for i, e := range list {
					out = append(out, v.check(e, arg, fmt.Sprintf("%s/%d", ptr, i))...)
				}
			}
		case "minItems":
			if list, ok := value.([]any); ok && float64(len(list)) < arg.(float64) {
				fail("has fewer than %v items", arg)
			}
		case "maxItems":
			if list, ok := value.([]any); ok && float64(len(list)) > arg.(float64) {
				fail("has more than %v items", arg)
			}
		case "uniqueItems":
			if list, ok := value.([]any); ok && arg == true {
				for i := range list {
					for j := i + 1; j < len(list); j++ {
						if reflect.DeepEqual(list[i], list[j]) {
							fail("items %d and %d are equal", i, j)
						}
					}
				}
			}
		case "anyOf":
			matched := false
			for _, sub := range arg.([]any) {
				matched = matched || v.valid(value, sub)
			}
			if !matched {
				fail("matches none of the alternatives")
			}
		case "oneOf":
			n := 0
			for _, sub := range arg.([]any) {
				if v.valid(value, sub) {
					n++
				}
			}
			if n != 1 {
				fail("matches %d of the alternatives, want exactly one", n)
			}
		case "allOf":
			for _, sub := range arg.([]any) {
				out = append(out, v.check(value, sub, ptr)...)
			}
		case "not":
			if v.valid(value, arg) {
				fail("matches a schema that it must not match")
			}
		default:
			if !annotationKeywords[k] {
				fail("the schema uses the keyword %q, which the test validator does not implement: add it to deploy_chart_schema_validator_test.go", k)
			}
		}
	}
	return out
}

// matchesAnyPattern reports whether a property name matches a key of patternProperties.
func (v *schemaValidator) matchesAnyPattern(name string, patterns map[string]any) bool {
	for pattern := range patterns {
		if v.regexp(pattern).MatchString(name) {
			return true
		}
	}
	return false
}

func sortedSchemaKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (v *schemaValidator) regexp(pattern string) *regexp.Regexp {
	if re, ok := v.re[pattern]; ok {
		return re
	}
	re := regexp.MustCompile(pattern)
	v.re[pattern] = re
	return re
}

// resolve follows a local reference ("#/$defs/name").
func (v *schemaValidator) resolve(ref any) (any, error) {
	s, ok := ref.(string)
	if !ok || !strings.HasPrefix(s, "#/") {
		return nil, fmt.Errorf("the reference %v is not a local JSON pointer", ref)
	}
	var cur any = v.root
	for _, tok := range strings.Split(s[2:], "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the reference %s does not resolve", s)
		}
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		if cur, ok = m[tok]; !ok {
			return nil, fmt.Errorf("the reference %s does not resolve", s)
		}
	}
	return cur, nil
}

func escapePointer(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

func jsonTypeOf(value any) string {
	switch x := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", value)
}

// typeMatches implements the keyword type: a name or a list of names; an integer is also a number.
func typeMatches(value, want any) bool {
	names := []any{want}
	if list, ok := want.([]any); ok {
		names = list
	}
	got := jsonTypeOf(value)
	for _, n := range names {
		if n == got || (n == "number" && got == "integer") {
			return true
		}
	}
	return false
}

// ------------------------------------------------------- the validator itself ---

// TestSchemaValidatorKeywords: the validator is test infrastructure, so its keywords have tests of their own. A validator that
// accepted too much would make the tests of the chart schema pass for the wrong reason.
func TestSchemaValidatorKeywords(t *testing.T) {
	schema := map[string]any{
		"$defs": map[string]any{"port": map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(65535)}},
	}
	v := newSchemaValidator(schema)
	parse := func(s string) any { return mustJSON(t, s) }
	cases := []struct {
		name, schema, value string
		want                string // substring of the first problem, empty = valid
	}{
		{"type ok", `{"type": "string"}`, `"a"`, ""},
		{"type wrong", `{"type": "string"}`, `1`, "is integer, want string"},
		{"integer accepts 3.0", `{"type": "integer"}`, `3.0`, ""},
		{"integer refuses 3.5", `{"type": "integer"}`, `3.5`, "is number, want integer"},
		{"number accepts an integer", `{"type": "number"}`, `3`, ""},
		{"type list", `{"type": ["string", "null"]}`, `null`, ""},
		{"type list refuses", `{"type": ["string", "null"]}`, `true`, "is boolean"},
		{"enum", `{"enum": ["a", "b"]}`, `"c"`, "is not one of"},
		{"enum number", `{"enum": [1, 2]}`, `2`, ""},
		{"const", `{"const": "x"}`, `"y"`, "is not x"},
		{"pattern matches", `{"pattern": "^a+$"}`, `"aaa"`, ""},
		{"pattern refuses", `{"pattern": "^a+$"}`, `"ab"`, "does not match"},
		{"pattern ignores a number", `{"pattern": "^a+$"}`, `1`, ""},
		{"maxLength", `{"maxLength": 2}`, `"abc"`, "longer"},
		{"minimum", `{"minimum": 1}`, `0`, "below"},
		{"maximum", `{"maximum": 1}`, `2`, "above"},
		{"required", `{"required": ["a"]}`, `{}`, "missing the property a"},
		{"required ok", `{"required": ["a"]}`, `{"a": null}`, ""},
		{"additionalProperties false", `{"properties": {"a": {}}, "additionalProperties": false}`, `{"a": 1, "b": 2}`, `additional property "b"`},
		{"additionalProperties true", `{"properties": {"a": {}}, "additionalProperties": true}`, `{"a": 1, "b": 2}`, ""},
		{"additionalProperties schema", `{"additionalProperties": {"type": "string"}}`, `{"a": "x", "b": 2}`, "/b: is integer"},
		{"property schema", `{"properties": {"a": {"type": "string"}}}`, `{"a": 1}`, "/a: is integer"},
		{"pointer escapes", `{"additionalProperties": {"type": "string"}}`, `{"a/b": 1}`, "/a~1b:"},
		{"patternProperties refuses a name", `{"patternProperties": {"^helm": false}}`, `{"ok": 1, "helm.sh/hook": null}`, `/helm.sh~1hook: false schema`},
		{"patternProperties ok", `{"patternProperties": {"^helm": false}}`, `{"ok": 1, "x.helm": null}`, ""},
		{"patternProperties applies the schema to the value", `{"patternProperties": {"^n": {"type": "integer"}}}`, `{"n1": "x", "m": "y"}`, "/n1: is string"},
		{"patternProperties ignores a value that is no object", `{"patternProperties": {"^a": false}}`, `["a"]`, ""},
		{"additionalProperties leaves the names of a pattern alone", `{"patternProperties": {"^n": {}}, "additionalProperties": false}`, `{"n1": 1}`, ""},
		{"additionalProperties still refuses the others", `{"patternProperties": {"^n": {}}, "additionalProperties": false}`, `{"n1": 1, "x": 2}`, `additional property "x"`},
		{"items", `{"items": {"type": "string"}}`, `["a", 1]`, "/1: is integer"},
		{"minItems", `{"minItems": 1}`, `[]`, "fewer"},
		{"maxItems", `{"maxItems": 1}`, `[1, 2]`, "more"},
		{"uniqueItems", `{"uniqueItems": true}`, `[1, 1]`, "equal"},
		{"anyOf ok", `{"anyOf": [{"type": "string"}, {"type": "null"}]}`, `null`, ""},
		{"anyOf refuses", `{"anyOf": [{"type": "string"}, {"type": "null"}]}`, `1`, "matches none"},
		{"oneOf refuses two", `{"oneOf": [{"type": "number"}, {"type": "integer"}]}`, `1`, "matches 2"},
		{"oneOf ok", `{"oneOf": [{"type": "string"}, {"type": "integer"}]}`, `1`, ""},
		{"allOf", `{"allOf": [{"type": "string"}, {"minLength": 3}]}`, `"ab"`, "shorter"},
		{"not", `{"not": {"pattern": "^x"}}`, `"xy"`, "must not match"},
		{"not ok", `{"not": {"pattern": "^x"}}`, `"y"`, ""},
		{"ref", `{"$ref": "#/$defs/port"}`, `0`, "below"},
		{"ref ok", `{"$ref": "#/$defs/port", "description": "sibling"}`, `80`, ""},
		{"ref missing", `{"$ref": "#/$defs/none"}`, `1`, "does not resolve"},
		{"ref remote", `{"$ref": "https://example.com/x"}`, `1`, "not a local JSON pointer"},
		{"false schema", `false`, `1`, "false schema"},
		{"required and properties without additionalProperties", `{"properties": {"a": {"type": "string"}}, "required": ["a"]}`, `{"a": 1}`, "/a: is integer"},
		{"true schema", `true`, `1`, ""},
		{"annotations are ignored", `{"title": "t", "description": "d", "default": 1, "$comment": "c", "examples": [1], "deprecated": true}`, `1`, ""},
		{"unknown keyword", `{"format": "uri"}`, `"x"`, `the keyword "format"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := v.check(parse(c.value), parse(c.schema), "")
			if c.want == "" {
				assert.Empty(t, problems)
				return
			}
			require.NotEmpty(t, problems)
			assert.Contains(t, strings.Join(problems, "\n"), c.want)
		})
	}
}

// TestSchemaValidatorCountsCharacters: a length is counted in characters, not in bytes.
func TestSchemaValidatorCountsCharacters(t *testing.T) {
	euro := string(rune(0x20ac)) // three bytes, one character
	v := newSchemaValidator(map[string]any{})
	assert.NotEmpty(t, v.check(euro, map[string]any{"minLength": float64(2)}, ""))
	assert.Empty(t, v.check(euro, map[string]any{"maxLength": float64(1)}, ""))
}
