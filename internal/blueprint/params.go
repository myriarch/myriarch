// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

var paramNameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

type regexpEntry struct {
	re  *regexp.Regexp
	err error
}

// fullMatchRegexp compiles pattern anchored at both ends (cached).
func (r *Renderer) fullMatchRegexp(pattern string) (*regexp.Regexp, error) {
	if e, ok := r.reCache.Get(pattern); ok {
		return e.re, e.err
	}
	re, err := regexp.Compile(`^(?:` + pattern + `)$`)
	r.reCache.Add(pattern, &regexpEntry{re: re, err: err})
	return re, err
}

// validParamType reports whether t is a known parameter type.
func validParamType(t domain.ParameterType) bool {
	switch t {
	case domain.ParamString, domain.ParamInt, domain.ParamNumber, domain.ParamBool,
		domain.ParamDuration, domain.ParamList, domain.ParamMap:
		return true
	}
	return false
}

// coerceValue converts a typed value (from YAML/JSON: defaults, fleet,
// inventory, preview overrides) to the Go type of the parameter type. The
// value must already have the right type; "number" additionally accepts
// integers and "int" accepts integral floats (JSON numbers of REST previews).
func coerceValue(t domain.ParameterType, v any) (any, error) {
	switch t {
	case domain.ParamString:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected a string, got %s (quote values like \"0123\")", describe(v))
		}
		return s, nil
	case domain.ParamInt:
		switch x := v.(type) {
		case int:
			return x, nil
		case int64:
			return int(x), nil
		case int32:
			return int(x), nil
		case uint64:
			if x <= math.MaxInt64 {
				return int(x), nil
			}
		case float64:
			if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
				return int(x), nil
			}
		case json.Number:
			if i, err := x.Int64(); err == nil {
				return int(i), nil
			}
		}
		return nil, fmt.Errorf("expected an integer, got %s", describe(v))
	case domain.ParamNumber:
		switch x := v.(type) {
		case int:
			return float64(x), nil
		case int64:
			return float64(x), nil
		case int32:
			return float64(x), nil
		case uint64:
			return float64(x), nil
		case float64:
			return x, nil
		case float32:
			return float64(x), nil
		case json.Number:
			if f, err := x.Float64(); err == nil {
				return f, nil
			}
		}
		return nil, fmt.Errorf("expected a number, got %s", describe(v))
	case domain.ParamBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected a boolean, got %s", describe(v))
		}
		return b, nil
	case domain.ParamDuration:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected a duration string like \"30s\", got %s", describe(v))
		}
		if _, err := time.ParseDuration(s); err != nil {
			return nil, fmt.Errorf("invalid duration %s (use Go syntax like 30s, 5m, 1h30m)", quoteShort(s))
		}
		return s, nil
	case domain.ParamList:
		switch x := v.(type) {
		case []any:
			return normalizeTree(x), nil
		case []string:
			return normalizeTree(x), nil
		}
		return nil, fmt.Errorf("expected a list, got %s", describe(v))
	case domain.ParamMap:
		if m, ok := asMap(v); ok {
			return normalizeTree(m), nil
		}
		if m, ok := v.(map[string]string); ok {
			return normalizeTree(m), nil
		}
		return nil, fmt.Errorf("expected a map, got %s", describe(v))
	}
	return nil, fmt.Errorf("unknown parameter type %q", t)
}

// coerceString converts an attribute value (always a string) to the type.
func coerceString(t domain.ParameterType, s string) (any, error) {
	switch t {
	case domain.ParamString:
		return s, nil
	case domain.ParamInt:
		i, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("attribute value %s is not an integer", quoteShort(s))
		}
		return i, nil
	case domain.ParamNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("attribute value %s is not a number", quoteShort(s))
		}
		return f, nil
	case domain.ParamBool:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("attribute value %s is not a boolean", quoteShort(s))
		}
		return b, nil
	case domain.ParamDuration:
		if _, err := time.ParseDuration(strings.TrimSpace(s)); err != nil {
			return nil, fmt.Errorf("attribute value %s is not a duration", quoteShort(s))
		}
		return strings.TrimSpace(s), nil
	case domain.ParamList:
		var v []any
		if err := decodeJSONNumbers(s, &v); err != nil {
			return nil, fmt.Errorf("attribute value is not a JSON list: %v", err)
		}
		return domain.NormalizeNumbers(v), nil
	case domain.ParamMap:
		var v map[string]any
		if err := decodeJSONNumbers(s, &v); err != nil {
			return nil, fmt.Errorf("attribute value is not a JSON object: %v", err)
		}
		return domain.NormalizeNumbers(v), nil
	}
	return nil, fmt.Errorf("unknown parameter type %q", t)
}

func decodeJSONNumbers(s string, v any) error {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func describe(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int64, int32, uint64:
		return "integer"
	case float64, float32:
		return "number"
	case []any:
		return "list"
	case map[string]any, map[any]any:
		return "map"
	default:
		return fmt.Sprintf("%T", x)
	}
}

// canonicalJSON renders a value for comparisons and messages.
func canonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// checkConstraints applies enum, pattern and min/max to a coerced value.
func (r *Renderer) checkConstraints(spec domain.ParameterSpec, v any) error {
	if len(spec.Enum) > 0 {
		found := false
		want := canonicalJSON(v)
		for _, e := range spec.Enum {
			ce, err := coerceValue(spec.Type, e)
			if err != nil {
				ce = e
			}
			if canonicalJSON(ce) == want {
				found = true
				break
			}
		}
		if !found {
			vals := make([]string, 0, len(spec.Enum))
			for _, e := range spec.Enum {
				vals = append(vals, canonicalJSON(e))
			}
			return fmt.Errorf("value %s is not one of the allowed values [%s]", truncate(canonicalJSON(v), 80), truncate(strings.Join(vals, ", "), 400))
		}
	}
	if spec.Pattern != "" {
		if s, ok := v.(string); ok {
			re, err := r.fullMatchRegexp(spec.Pattern)
			if err != nil {
				return fmt.Errorf("invalid pattern %q: %v", spec.Pattern, err)
			}
			if !re.MatchString(s) {
				return fmt.Errorf("value %s does not match pattern %q", quoteShort(s), spec.Pattern)
			}
		}
	}
	var num float64
	isNum := true
	switch x := v.(type) {
	case int:
		num = float64(x)
	case float64:
		num = x
	default:
		isNum = false
	}
	if isNum {
		if spec.Min != nil && num < *spec.Min {
			return fmt.Errorf("value %v is below the minimum %v", num, *spec.Min)
		}
		if spec.Max != nil && num > *spec.Max {
			return fmt.Errorf("value %v is above the maximum %v", num, *spec.Max)
		}
	}
	return nil
}

// paramDecls holds the effective declarations of a blueprint chain in order
// of first declaration.
type paramDecls struct {
	byName map[string]domain.ParameterSpec
	// owner is the blueprint whose declaration is effective.
	owner map[string]*domain.Blueprint
	order []string
}

// declareParams collects parameters root -> leaf (docs/concepts/blueprints.md, "Parameters"):
// a redeclaration replaces the previous declaration completely but must keep
// the type.
func declareParams(chain []*domain.Blueprint) (paramDecls, domain.Issues) {
	d := paramDecls{byName: map[string]domain.ParameterSpec{}, owner: map[string]*domain.Blueprint{}}
	var issues domain.Issues
	for _, bp := range chain {
		for i, p := range bp.Parameters {
			if prev, ok := d.byName[p.Name]; ok {
				if prev.Type != p.Type {
					issues = append(issues, domain.Issue{
						Severity: domain.SeverityError, Code: "parameter_type_conflict",
						Message: fmt.Sprintf("parameter %q is declared as %s in an ancestor but redeclared as %s in blueprint %q", p.Name, prev.Type, p.Type, bp.Name),
						File:    bp.Source.Path, Object: "Blueprint/" + bp.Name, Path: fmt.Sprintf("spec.parameters[%d]", i),
					})
					continue
				}
			} else {
				d.order = append(d.order, p.Name)
			}
			d.byName[p.Name] = p
			d.owner[p.Name] = bp
		}
	}
	return d, issues
}

type resolvedParams struct {
	values  map[string]any
	sources map[string]string
}

// resolveParams resolves the effective parameter values: default < fleet <
// attribute < inventory < override (docs/concepts/blueprints.md, "Parameters": where a value comes from).
func (r *Renderer) resolveParams(decls paramDecls, in RenderInput, leaf *domain.Blueprint) (resolvedParams, domain.Issues) {
	res := resolvedParams{values: map[string]any{}, sources: map[string]string{}}
	var issues domain.Issues
	fleetName := ""
	var fleetFile string
	if in.Fleet != nil {
		fleetName, fleetFile = in.Fleet.Name, in.Fleet.Source.Path
	}
	paramIssue := func(code, source, name, msg string) {
		is := domain.Issue{Severity: domain.SeverityError, Code: code, Message: msg, Path: "params." + name}
		switch source {
		case "fleet":
			is.Object, is.File, is.Path = "Fleet/"+fleetName, fleetFile, "spec.parameters::"+name
		case "default":
			if bp := decls.owner[name]; bp != nil {
				is.Object, is.File = "Blueprint/"+bp.Name, bp.Source.Path
			}
		}
		issues = append(issues, is)
	}

	set := func(name, source string, spec domain.ParameterSpec, raw any, fromString bool) {
		var v any
		var err error
		if fromString {
			v, err = coerceString(spec.Type, raw.(string))
		} else {
			v, err = coerceValue(spec.Type, raw)
		}
		if err != nil {
			paramIssue("invalid_parameter_value", source, name, fmt.Sprintf("parameter %q (%s, from %s): %v", name, spec.Type, source, err))
			return
		}
		res.values[name] = v
		res.sources[name] = source
	}

	for _, name := range decls.order {
		spec := decls.byName[name]
		if !validParamType(spec.Type) {
			paramIssue("invalid_parameter", "default", name, fmt.Sprintf("parameter %q has unknown type %q", name, spec.Type))
			continue
		}
		if spec.Default != nil {
			if _, err := coerceValue(spec.Type, spec.Default); err != nil {
				paramIssue("invalid_parameter", "default", name, fmt.Sprintf("default of parameter %q does not match type %s: %v", name, spec.Type, err))
			} else {
				set(name, "default", spec, spec.Default, false)
			}
		}
		if in.Fleet != nil {
			if raw, ok := in.Fleet.Parameters[name]; ok {
				set(name, "fleet", spec, raw, false)
			}
		}
		if spec.FromAttribute != "" {
			if s, ok := in.Agent.Attributes[spec.FromAttribute]; ok {
				set(name, "attribute", spec, s, true)
			}
		}
		if raw, ok := in.InventoryParameters[name]; ok {
			set(name, "inventory", spec, raw, false)
		}
		if raw, ok := in.Overrides[name]; ok {
			set(name, "override", spec, raw, false)
		}
	}

	// Unknown values.
	if in.Fleet != nil {
		for _, k := range sortedKeys(in.Fleet.Parameters) {
			if _, ok := decls.byName[k]; !ok {
				issues = append(issues, domain.Issue{
					Severity: domain.SeverityError, Code: "unknown_parameter",
					Message: fmt.Sprintf("fleet %q sets parameter %q which blueprint %q does not declare", fleetName, k, leaf.Name),
					File:    fleetFile, Object: "Fleet/" + fleetName, Path: "spec.parameters::" + k,
				})
			}
		}
	}
	for _, k := range sortedKeys(in.InventoryParameters) {
		if _, ok := decls.byName[k]; !ok {
			issues = append(issues, domain.Issue{
				Severity: domain.SeverityWarning, Code: "unknown_parameter",
				Message: fmt.Sprintf("inventory parameter %q is not declared by blueprint %q and is ignored", k, leaf.Name),
				Path:    "inventory.parameters." + k,
			})
		}
	}
	for _, k := range sortedKeys(in.Overrides) {
		if _, ok := decls.byName[k]; !ok {
			issues = append(issues, domain.Issue{
				Severity: domain.SeverityError, Code: "unknown_parameter",
				Message: fmt.Sprintf("override parameter %q is not declared by blueprint %q", k, leaf.Name),
				Path:    "overrides." + k,
			})
		}
	}

	// Constraints and required.
	for _, name := range decls.order {
		spec := decls.byName[name]
		v, ok := res.values[name]
		if !ok {
			if spec.Required {
				paramIssue("missing_parameter", "default", name, fmt.Sprintf("required parameter %q has no value (set it in the fleet, inventory, an attribute or a default)", name))
			}
			continue
		}
		if err := r.checkConstraints(spec, v); err != nil {
			paramIssue("invalid_parameter_value", res.sources[name], name, fmt.Sprintf("parameter %q (from %s): %v", name, res.sources[name], err))
		}
	}
	return res, issues
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonNoEscape marshals without HTML escaping.
func jsonNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
