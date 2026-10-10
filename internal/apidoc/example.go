// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// maxExampleDepth bounds the nesting of a generated example.
const maxExampleDepth = 6

// formatPlaceholders are the example values per string format.
var formatPlaceholders = map[string]string{
	"uuid":      "00000000-0000-4000-8000-000000000000",
	"date-time": "2026-01-01T00:00:00Z",
	"date":      "2026-01-01",
	"time":      "00:00:00Z",
	"duration":  "30s",
	"uri":       "https://example.com/",
	"url":       "https://example.com/",
	"email":     "user@example.com",
	"hostname":  "host.example.com",
	"ipv4":      "192.0.2.1",
	"ipv6":      "2001:db8::1",
}

// jnode is a JSON value with ordered object members.
type jnode struct {
	kind   byte // 'o' object, 'a' array, 'l' literal
	lit    string
	keys   []string
	fields []*jnode
	items  []*jnode
}

func literal(v any) *jnode {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return &jnode{kind: 'l', lit: "null"}
	}
	return &jnode{kind: 'l', lit: strings.TrimRight(b.String(), "\n")}
}

// write renders the value with two-space indentation.
func (n *jnode) write(b *strings.Builder, indent int) {
	pad := strings.Repeat("  ", indent+1)
	switch n.kind {
	case 'l':
		b.WriteString(n.lit)
	case 'a':
		if len(n.items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, it := range n.items {
			b.WriteString(pad)
			it.write(b, indent+1)
			if i < len(n.items)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("  ", indent) + "]")
	case 'o':
		if len(n.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range n.keys {
			b.WriteString(pad + literal(k).lit + ": ")
			n.fields[i].write(b, indent+1)
			if i < len(n.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("  ", indent) + "}")
	}
}

// exampleBuilder derives a deterministic example value from a schema: the example, else the default, else the first value of
// an enum, else a placeholder per format or type. A reference that is already being expanded is cut off (the member or element
// is left out), so a self-referencing type terminates.
type exampleBuilder struct {
	spec      *Spec
	skipRead  bool // request bodies leave out read-only members
	skipWrite bool // responses leave out write-only members
}

// ExampleJSON returns the example of a schema as indented JSON (no trailing newline).
func (s *Spec) ExampleJSON(sc *Schema, request bool) (string, error) {
	g := &exampleBuilder{spec: s, skipRead: request, skipWrite: !request}
	n, err := g.value(sc, nil, 0)
	if err != nil {
		return "", err
	}
	if n == nil {
		n = &jnode{kind: 'l', lit: "null"}
	}
	var b strings.Builder
	n.write(&b, 0)
	return b.String(), nil
}

func (g *exampleBuilder) value(sc *Schema, stack []string, depth int) (*jnode, error) {
	if sc == nil {
		return nil, nil
	}
	if sc.Ref != "" {
		name, target, err := g.spec.schemaRef(sc.Ref)
		if err != nil {
			return nil, err
		}
		for _, s := range stack {
			if s == name {
				return nil, nil
			}
		}
		if sc.Example != nil || sc.Default != nil {
			return literal(firstNonNil(sc.Example, sc.Default)), nil
		}
		return g.value(target, append(append([]string{}, stack...), name), depth)
	}
	if sc.Example != nil {
		return literal(normalize(sc.Example)), nil
	}
	if sc.Default != nil {
		return literal(normalize(sc.Default)), nil
	}
	if len(sc.Enum) > 0 {
		return literal(normalize(sc.Enum[0])), nil
	}
	if depth >= maxExampleDepth {
		return nil, nil
	}
	switch {
	case sc.Type == "array" || (sc.Type == "" && sc.Items != nil):
		out := &jnode{kind: 'a'}
		it, err := g.value(sc.Items, stack, depth+1)
		if err != nil {
			return nil, err
		}
		if it != nil {
			out.items = append(out.items, it)
		}
		return out, nil
	case sc.Type == "object" || sc.Type == "" && (len(sc.Properties.Keys) > 0 || sc.AdditionalProperties != nil):
		out := &jnode{kind: 'o'}
		for _, k := range sc.Properties.Keys {
			p := sc.Properties.Values[k]
			if p == nil || (g.skipRead && g.isReadOnly(p)) || (g.skipWrite && g.isWriteOnly(p)) {
				continue
			}
			v, err := g.value(p, stack, depth+1)
			if err != nil {
				return nil, err
			}
			if v != nil {
				out.keys = append(out.keys, k)
				out.fields = append(out.fields, v)
			}
		}
		if len(out.keys) == 0 && sc.AdditionalProperties != nil && sc.AdditionalProperties.Schema != nil {
			v, err := g.value(sc.AdditionalProperties.Schema, stack, depth+1)
			if err != nil {
				return nil, err
			}
			if v != nil {
				out.keys = append(out.keys, "key")
				out.fields = append(out.fields, v)
			}
		}
		return out, nil
	case sc.Type == "integer" || sc.Type == "number":
		return literal(0), nil
	case sc.Type == "boolean":
		return literal(false), nil
	case sc.Type == "string" || sc.Type == "":
		if p, ok := formatPlaceholders[sc.Format]; ok {
			return literal(p), nil
		}
		return literal("string"), nil
	}
	return nil, fmt.Errorf("apidoc: no example for the type %q", sc.Type)
}

func (g *exampleBuilder) isReadOnly(sc *Schema) bool {
	return g.flag(sc, func(s *Schema) bool { return s.ReadOnly })
}
func (g *exampleBuilder) isWriteOnly(sc *Schema) bool {
	return g.flag(sc, func(s *Schema) bool { return s.WriteOnly })
}

func (g *exampleBuilder) flag(sc *Schema, f func(*Schema) bool) bool {
	if f(sc) {
		return true
	}
	if sc.Ref != "" {
		if _, t, err := g.spec.schemaRef(sc.Ref); err == nil {
			return f(t)
		}
	}
	return false
}

func firstNonNil(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

// normalize turns the values that the YAML decoder produces into values that encoding/json writes with a stable key order
// (maps with non-string keys become string-keyed).
func normalize(v any) any {
	switch t := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = normalize(x)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = normalize(x)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	}
	return v
}
