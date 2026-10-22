// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Markers that enclose the generated values table in the README of the chart.
const (
	ChartValuesTableBegin = "<!-- BEGIN GENERATED VALUES TABLE (make docs-gen; do not edit) -->"
	ChartValuesTableEnd   = "<!-- END GENERATED VALUES TABLE -->"
)

// chartValuesMaxDefault is the longest default that the table prints; a longer one is replaced by a pointer to values.yaml.
const chartValuesMaxDefault = 60

// RenderChartValuesTable renders the table of the values of a chart from its values schema (JSON) and its values.yaml: one row for
// every value with its type, its default and its description. An object with properties is expanded into the rows of its
// properties; the block named in collapse (the generated configuration block) is one row that points to its reference.
// The rows follow the order of the schema.
func RenderChartValuesTable(schema, values []byte, collapse map[string]string) ([]byte, error) {
	var sroot, vroot yaml.Node
	if err := yaml.Unmarshal(schema, &sroot); err != nil {
		return nil, fmt.Errorf("values schema: %w", err)
	}
	if len(sroot.Content) == 0 {
		return nil, errors.New("values schema: empty document")
	}
	if err := yaml.Unmarshal(values, &vroot); err != nil {
		return nil, fmt.Errorf("values.yaml: %w", err)
	}
	var vdoc *yaml.Node
	if len(vroot.Content) > 0 {
		vdoc = vroot.Content[0]
	}
	t := &valuesTable{root: sroot.Content[0], collapse: collapse}
	var b bytes.Buffer
	b.WriteString("| Value | Type | Default | Description |\n|---|---|---|---|\n")
	if err := t.rows(&b, t.root, vdoc, "", 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

type valuesTable struct {
	root     *yaml.Node
	collapse map[string]string
}

func nodeGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// resolve follows a local $ref ("#/$defs/name") and merges nothing else: the node that carries the $ref keeps its own description.
func (t *valuesTable) resolve(n *yaml.Node) (*yaml.Node, error) {
	for depth := 0; n != nil; depth++ {
		ref := nodeGet(n, "$ref")
		if ref == nil {
			return n, nil
		}
		if depth > 8 || !strings.HasPrefix(ref.Value, "#/$defs/") {
			return nil, fmt.Errorf("values schema: unsupported $ref %q", ref.Value)
		}
		next := nodeGet(nodeGet(t.root, "$defs"), strings.TrimPrefix(ref.Value, "#/$defs/"))
		if next == nil {
			return nil, fmt.Errorf("values schema: unknown $ref %q", ref.Value)
		}
		n = next
	}
	return nil, errors.New("values schema: empty node")
}

func (t *valuesTable) rows(b *bytes.Buffer, node, def *yaml.Node, path string, depth int) error {
	props := nodeGet(node, "properties")
	if props == nil {
		return nil
	}
	for i := 0; i+1 < len(props.Content); i += 2 {
		key := props.Content[i].Value
		raw := props.Content[i+1]
		p := key
		if path != "" {
			p = path + "." + key
		}
		sub, err := t.resolve(raw)
		if err != nil {
			return err
		}
		subDef := nodeGet(def, key)
		if note, ok := t.collapse[p]; ok {
			fmt.Fprintf(b, "| `%s` | object | | %s |\n", p, tableCell(note))
			continue
		}
		if sub.Kind == yaml.MappingNode && nodeGet(sub, "properties") != nil && depth < 4 {
			if err := t.rows(b, sub, subDef, p, depth+1); err != nil {
				return err
			}
			continue
		}
		desc := nodeGet(raw, "description")
		if desc == nil {
			desc = nodeGet(sub, "description")
		}
		d := ""
		if desc != nil {
			d = desc.Value
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", p, t.schemaType(sub), renderDefault(subDef), tableCell(d))
	}
	return nil
}

func (t *valuesTable) schemaType(n *yaml.Node) string {
	if tn := nodeGet(n, "type"); tn != nil {
		if tn.Kind == yaml.SequenceNode {
			var parts []string
			for _, c := range tn.Content {
				parts = append(parts, c.Value)
			}
			return strings.Join(parts, " or ")
		}
		return tn.Value
	}
	if nodeGet(n, "enum") != nil || nodeGet(n, "const") != nil {
		return "enum"
	}
	for _, k := range []string{"anyOf", "oneOf"} {
		alts := nodeGet(n, k)
		if alts == nil {
			continue
		}
		var parts []string
		for _, a := range alts.Content {
			r, err := t.resolve(a)
			if err != nil {
				continue
			}
			if p := t.schemaType(r); !contains(parts, p) {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, " or ")
	}
	return "any"
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// renderDefault prints the value of values.yaml as compact JSON in code font, or "unset" when values.yaml does not set it.
func renderDefault(n *yaml.Node) string {
	if n == nil {
		return "unset"
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return "see `values.yaml`"
	}
	out, err := json.Marshal(v)
	if err != nil || len(out) > chartValuesMaxDefault {
		return "see `values.yaml`"
	}
	return "`" + strings.ReplaceAll(string(out), "|", `\|`) + "`"
}

// tableCell makes a description one line of a Markdown table cell. A "<" outside code spans is escaped, so that a
// placeholder such as <fullname> is not read as raw HTML.
func tableCell(s string) string {
	parts := strings.Split(cell(s), "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = strings.ReplaceAll(parts[i], "<", `\<`)
	}
	return strings.ReplaceAll(strings.Join(parts, "`"), "|", `\|`)
}

// ReplaceChartValuesTable puts table between the markers of readme. It fails when a marker is missing or misplaced.
func ReplaceChartValuesTable(readme, table []byte) ([]byte, error) {
	begin := bytes.Index(readme, []byte(ChartValuesTableBegin))
	end := bytes.Index(readme, []byte(ChartValuesTableEnd))
	if begin < 0 || end < begin {
		return nil, fmt.Errorf("the README lacks the markers %q and %q (in this order)", ChartValuesTableBegin, ChartValuesTableEnd)
	}
	var out bytes.Buffer
	out.Write(readme[:begin])
	out.WriteString(ChartValuesTableBegin + "\n\n")
	out.Write(table)
	out.WriteString("\n")
	out.Write(readme[end:])
	return out.Bytes(), nil
}
