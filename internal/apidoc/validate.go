// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package apidoc

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// schemaKeywords is the allowlist of schema keywords that the generator renders. Everything else is refused. A keyword on the
// list is rendered in every place where Validate accepts it (the test TestEveryKeywordIsRendered checks that); where it would be
// dropped, Validate refuses it with the JSON pointer instead (see the contexts below).
var schemaKeywords = map[string]bool{
	"type": true, "format": true, "enum": true, "default": true, "example": true, "description": true,
	"properties": true, "required": true, "items": true, "additionalProperties": true, "minimum": true, "maximum": true,
	"minLength": true, "maxLength": true, "minItems": true, "maxItems": true, "uniqueItems": true, "pattern": true,
	"$ref": true, "deprecated": true, "readOnly": true, "writeOnly": true,
}

// Schema contexts. A component schema (ctxRoot) may have properties and required. A property (ctxProperty) is described by its
// row of the table. The element schema of items and the value schema of additionalProperties (ctxNested) are described by
// sentences of the field that holds them, including their description. The schema of a parameter, a request body or a
// response (ctxInline) is described by the row of the parameter or the response, so it has no description of its own. Nested
// and inline schemas have no properties and no required (named types only).
const (
	ctxRoot = iota
	ctxProperty
	ctxNested
	ctxInline
)

var methods = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true}

type validator struct {
	p     problems
	names map[string]map[string]bool // kind -> component names
}

// Validate checks that the document stays within the subset of OpenAPI 3.1 that Render supports: the allowlisted keywords
// (type, format, enum, default, example, description, title, properties, required, items, additionalProperties, minimum,
// maximum, minLength, maxLength, minItems, maxItems, uniqueItems, pattern, $ref, deprecated, readOnly, writeOnly), local
// references to components, named object types (properties only in component schemas), single-paragraph property
// descriptions and exactly one server with a base path. The error lists the JSON pointer of every offending node.
func Validate(spec []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return fmt.Errorf("apidoc: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("apidoc: the document is not a mapping")
	}
	v := &validator{names: map[string]map[string]bool{}}
	root := doc.Content[0]
	top := v.object(root, "", "openapi", "info", "servers", "security", "tags", "paths", "components")
	if n := top["openapi"]; n == nil || !strings.HasPrefix(n.Value, "3.1") {
		v.p.add("/openapi", "the document must be OpenAPI 3.1")
	}
	comps := v.object(top["components"], "/components", "schemas", "parameters", "responses", "securitySchemes")
	for kind, n := range comps {
		v.names[kind] = map[string]bool{}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				v.names[kind][n.Content[i].Value] = true
			}
		}
	}
	v.info(top["info"])
	v.servers(top["servers"])
	v.list(top["tags"], "/tags", func(n *yaml.Node, ptr string) { v.object(n, ptr, "name", "description") })
	v.list(top["security"], "/security", func(n *yaml.Node, ptr string) { v.objectAny(n, ptr) })
	v.each(comps["securitySchemes"], "/components/securitySchemes", func(n *yaml.Node, ptr string) {
		v.object(n, ptr, "type", "in", "name", "scheme", "description")
	})
	v.each(comps["schemas"], "/components/schemas", func(n *yaml.Node, ptr string) { v.schema(n, ptr, ctxRoot) })
	v.each(comps["parameters"], "/components/parameters", func(n *yaml.Node, ptr string) { v.parameter(n, ptr) })
	v.each(comps["responses"], "/components/responses", func(n *yaml.Node, ptr string) { v.response(n, ptr) })
	v.each(top["paths"], "/paths", func(n *yaml.Node, ptr string) { v.pathItem(n, ptr) })
	if n := top["paths"]; n != nil && n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if !strings.HasPrefix(n.Content[i].Value, "/") {
				v.p.add(ptrJoin("/paths", n.Content[i].Value), "a path must start with \"/\"")
			}
		}
	}
	return v.p.err()
}

// object checks that n is a mapping with keys from allowed and returns its entries by key. A nil n yields an empty map.
func (v *validator) object(n *yaml.Node, ptr string, allowed ...string) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if n == nil {
		return out
	}
	if n.Kind != yaml.MappingNode {
		v.p.add(orRoot(ptr), "a mapping is expected")
		return out
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		if !ok[k] {
			v.p.add(ptrJoin(ptr, k), "the keyword %q is not supported", k)
			continue
		}
		out[k] = n.Content[i+1]
	}
	return out
}

// objectAny checks that n is a mapping and returns its entries.
func (v *validator) objectAny(n *yaml.Node, ptr string) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if n.Kind != yaml.MappingNode {
		v.p.add(orRoot(ptr), "a mapping is expected")
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = n.Content[i+1]
	}
	return out
}

func orRoot(ptr string) string {
	if ptr == "" {
		return "/"
	}
	return ptr
}

func (v *validator) list(n *yaml.Node, ptr string, f func(*yaml.Node, string)) {
	if n == nil {
		return
	}
	if n.Kind != yaml.SequenceNode {
		v.p.add(ptr, "a sequence is expected")
		return
	}
	for i, c := range n.Content {
		f(c, fmt.Sprintf("%s/%d", ptr, i))
	}
}

func (v *validator) each(n *yaml.Node, ptr string, f func(*yaml.Node, string)) {
	if n == nil {
		return
	}
	if n.Kind != yaml.MappingNode {
		v.p.add(ptr, "a mapping is expected")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		f(n.Content[i+1], ptrJoin(ptr, n.Content[i].Value))
	}
}

func (v *validator) info(n *yaml.Node) {
	m := v.object(n, "/info", "title", "version", "summary", "description", "contact", "license")
	v.prose(m["description"], "/info/description")
	v.object(m["contact"], "/info/contact", "name", "url", "email")
	v.object(m["license"], "/info/license", "name", "identifier", "url")
}

// servers checks that the contract has exactly one server whose URL is a base path ("/api/v1"): the generator names it on every
// page and puts it in front of every path.
func (v *validator) servers(n *yaml.Node) {
	if n == nil || n.Kind != yaml.SequenceNode || len(n.Content) != 1 {
		v.p.add("/servers", "exactly one server is expected")
		return
	}
	m := v.object(n.Content[0], "/servers/0", "url", "description")
	if u := m["url"]; u == nil || !strings.HasPrefix(u.Value, "/") || (len(u.Value) > 1 && strings.HasSuffix(u.Value, "/")) {
		v.p.add("/servers/0/url", "the URL must be a base path that starts with \"/\" and does not end with one")
	}
}

// prose checks a Markdown description that is rendered as it is: it must not contain headings (they would break the
// structure of the page).
func (v *validator) prose(n *yaml.Node, ptr string) {
	if n == nil {
		return
	}
	fence := false
	for _, line := range strings.Split(n.Value, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if !fence && strings.HasPrefix(line, "#") {
			v.p.add(ptr, "a description must not contain a Markdown heading")
			return
		}
	}
}

func (v *validator) pathItem(n *yaml.Node, ptr string) {
	if n.Kind != yaml.MappingNode {
		v.p.add(ptr, "a mapping is expected")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i].Value, n.Content[i+1]
		kp := ptrJoin(ptr, k)
		switch {
		case k == "parameters":
			v.list(val, kp, func(c *yaml.Node, p string) { v.parameter(c, p) })
		case methods[k]:
			v.operation(val, kp)
		default:
			v.p.add(kp, "the keyword %q is not supported", k)
		}
	}
}

func (v *validator) operation(n *yaml.Node, ptr string) {
	m := v.object(n, ptr, "operationId", "tags", "summary", "description", "security", "x-permission", "parameters", "requestBody", "responses")
	for _, req := range []string{"operationId", "summary", "x-permission", "responses"} {
		if m[req] == nil {
			v.p.add(ptr, "%q is missing", req)
		}
	}
	if t := m["tags"]; t == nil || t.Kind != yaml.SequenceNode || len(t.Content) != 1 {
		v.p.add(ptrJoin(ptr, "tags"), "exactly one tag is expected")
	}
	v.prose(m["description"], ptrJoin(ptr, "description"))
	v.list(m["parameters"], ptrJoin(ptr, "parameters"), func(c *yaml.Node, p string) { v.parameter(c, p) })
	if rb := m["requestBody"]; rb != nil {
		rm := v.object(rb, ptrJoin(ptr, "requestBody"), "description", "required", "content")
		v.content(rm["content"], ptrJoin(ptr, "requestBody", "content"))
	}
	v.each(m["responses"], ptrJoin(ptr, "responses"), func(c *yaml.Node, p string) { v.response(c, p) })
}

func (v *validator) content(n *yaml.Node, ptr string) {
	v.each(n, ptr, func(c *yaml.Node, p string) {
		m := v.object(c, p, "schema")
		if s := m["schema"]; s != nil {
			v.schema(s, ptrJoin(p, "schema"), ctxInline)
		}
	})
}

func (v *validator) parameter(n *yaml.Node, ptr string) {
	m := v.object(n, ptr, "$ref", "name", "in", "required", "description", "style", "explode", "schema")
	if r := m["$ref"]; r != nil {
		v.ref(r, ptrJoin(ptr, "$ref"), "parameters")
		return
	}
	for _, req := range []string{"name", "in", "schema"} {
		if m[req] == nil {
			v.p.add(ptr, "%q is missing", req)
		}
	}
	if s := m["schema"]; s != nil {
		v.schema(s, ptrJoin(ptr, "schema"), ctxInline)
	}
}

func (v *validator) response(n *yaml.Node, ptr string) {
	m := v.object(n, ptr, "$ref", "description", "content")
	if r := m["$ref"]; r != nil {
		v.ref(r, ptrJoin(ptr, "$ref"), "responses")
		return
	}
	v.content(m["content"], ptrJoin(ptr, "content"))
}

// ref checks a local reference of the given kind.
func (v *validator) ref(n *yaml.Node, ptr, kind string) {
	name, ok := refName(n.Value, kind)
	switch {
	case n.Kind != yaml.ScalarNode || !ok:
		v.p.add(ptr, "only local references to #/components/%s/<name> are supported, found %q", kind, n.Value)
	case !v.names[kind][name]:
		v.p.add(ptr, "the reference %q does not resolve", n.Value)
	}
}

func (v *validator) schema(n *yaml.Node, ptr string, ctx int) {
	if n.Kind != yaml.MappingNode {
		v.p.add(ptr, "a mapping is expected")
		return
	}
	var props, required *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i].Value, n.Content[i+1]
		kp := ptrJoin(ptr, k)
		if !schemaKeywords[k] {
			v.p.add(kp, "the keyword %q is not supported", k)
			continue
		}
		switch k {
		case "$ref":
			v.ref(val, kp, "schemas")
		case "type":
			if val.Kind != yaml.ScalarNode {
				v.p.add(kp, "a type array is not supported")
			}
		case "properties":
			props = val
			if ctx != ctxRoot {
				v.p.add(kp, "an inline nested object is not supported: give the object a name in components/schemas")
			}
		case "items":
			v.schema(val, kp, ctxNested)
		case "additionalProperties":
			if val.Kind == yaml.MappingNode {
				v.schema(val, kp, ctxNested)
			}
		case "required":
			required = val
			if ctx != ctxRoot {
				v.p.add(kp, "required is only supported next to properties in a component schema")
			}
		case "description":
			switch {
			case ctx == ctxInline:
				v.p.add(kp, "a description is not rendered here: describe the parameter, request body or response instead")
			case ctx == ctxProperty && strings.Contains(strings.TrimSpace(val.Value), "\n\n"):
				v.p.add(kp, "a property description must be a single paragraph")
			}
		}
	}
	if props != nil && ctx == ctxRoot {
		v.each(props, ptrJoin(ptr, "properties"), func(c *yaml.Node, p string) { v.schema(c, p, ctxProperty) })
	}
	if required != nil && ctx == ctxRoot {
		v.requiredNames(required, ptrJoin(ptr, "required"), props)
	}
}

// requiredNames checks that every required name is a property of the schema (a required list without properties is not rendered).
func (v *validator) requiredNames(req *yaml.Node, ptr string, props *yaml.Node) {
	known := map[string]bool{}
	if props != nil && props.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(props.Content); i += 2 {
			known[props.Content[i].Value] = true
		}
	}
	v.list(req, ptr, func(c *yaml.Node, p string) {
		if !known[c.Value] {
			v.p.add(p, "the required name %q is not a property of the schema", c.Value)
		}
	})
}
