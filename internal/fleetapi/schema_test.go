// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// spec is the parsed api/openapi.yaml.
type spec struct {
	doc map[string]any
}

func loadSpec(t testing.TB) *spec {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.yaml")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	return &spec{doc: doc}
}

func (s *spec) path(parts ...string) any {
	var cur any = s.doc
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// operation locates an operation by operationId.
type operation struct {
	id, method, path, permission string
	tags                         []string
	node                         map[string]any
}

func (s *spec) operations() []operation {
	var out []operation
	paths, _ := s.doc["paths"].(map[string]any)
	for p, item := range paths {
		im, _ := item.(map[string]any)
		for method, op := range im {
			om, ok := op.(map[string]any)
			if !ok {
				continue
			}
			id, _ := om["operationId"].(string)
			if id == "" {
				continue
			}
			var tags []string
			if raw, ok := om["tags"].([]any); ok {
				for _, tg := range raw {
					if name, ok := tg.(string); ok {
						tags = append(tags, name)
					}
				}
			}
			perm, _ := om["x-permission"].(string)
			out = append(out, operation{id: id, method: strings.ToUpper(method), path: p, permission: perm, tags: tags, node: om})
		}
	}
	return out
}

// responseSchema returns the JSON schema of the response with the given status.
func (s *spec) responseSchema(op operation, status int) (map[string]any, bool) {
	responses, _ := op.node["responses"].(map[string]any)
	r, ok := responses[fmt.Sprint(status)].(map[string]any)
	if !ok {
		return nil, false
	}
	if ref, ok := r["$ref"].(string); ok {
		r = s.resolve(ref)
	}
	content, _ := r["content"].(map[string]any)
	js, _ := content["application/json"].(map[string]any)
	if js == nil {
		return nil, false
	}
	sc, _ := js["schema"].(map[string]any)
	return sc, sc != nil
}

func (s *spec) resolve(ref string) map[string]any {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	m, _ := s.path(parts...).(map[string]any)
	return m
}

// validate checks a decoded JSON value against a schema (the subset of
// OpenAPI 3.1 the contract uses: $ref, type, required, properties,
// additionalProperties, items, enum). It returns all violations.
func (s *spec) validate(v any, schema map[string]any, path string) []string {
	if ref, ok := schema["$ref"].(string); ok {
		return s.validate(v, s.resolve(ref), path)
	}
	var errs []string
	typ, _ := schema["type"].(string)
	if enum, ok := schema["enum"].([]any); ok && v != nil {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %v is not one of %v", path, v, enum))
		}
	}
	switch typ {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return append(errs, fmt.Sprintf("%s: expected object, got %T", path, v))
		}
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				name := r.(string)
				val, present := m[name]
				switch {
				case !present:
					errs = append(errs, fmt.Sprintf("%s: required property %q is missing", path, name))
				case val == nil:
					errs = append(errs, fmt.Sprintf("%s: required property %q is null", path, name))
				}
			}
		}
		for k, val := range m {
			if ps, ok := props[k].(map[string]any); ok {
				if val == nil {
					errs = append(errs, fmt.Sprintf("%s.%s: null (optional properties must be omitted, not null)", path, k))
					continue
				}
				errs = append(errs, s.validate(val, ps, path+"."+k)...)
				continue
			}
			switch ap := schema["additionalProperties"].(type) {
			case map[string]any:
				errs = append(errs, s.validate(val, ap, path+"."+k)...)
			case bool:
				if !ap {
					errs = append(errs, fmt.Sprintf("%s: unexpected property %q", path, k))
				}
			default:
				if props != nil {
					errs = append(errs, fmt.Sprintf("%s: unexpected property %q", path, k))
				}
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return append(errs, fmt.Sprintf("%s: expected array, got %T", path, v))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range a {
				errs = append(errs, s.validate(e, items, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			errs = append(errs, fmt.Sprintf("%s: expected string, got %T", path, v))
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			errs = append(errs, fmt.Sprintf("%s: expected integer, got %v", path, v))
		}
	case "number":
		if _, ok := v.(float64); !ok {
			errs = append(errs, fmt.Sprintf("%s: expected number, got %T", path, v))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			errs = append(errs, fmt.Sprintf("%s: expected boolean, got %T", path, v))
		}
	}
	return errs
}
