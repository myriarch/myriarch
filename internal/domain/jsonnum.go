// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
)

// UnmarshalJSONPreservingInts decodes JSON into v like json.Unmarshal, but all
// numbers that end up in interface-typed values (any, map[string]any,
// []any - e.g. FleetSpec.Parameters, ParameterSpec.Default, Blueprint.Config)
// become int when they are integral and fit into int, otherwise float64.
//
// Plain json.Unmarshal would turn every such number into float64, which
// breaks typed blueprint parameters and makes the canonical YAML differ
// (yaml.v3 renders 1e+07 instead of 10000000) between myrctl (YAML source)
// and myriarchd (snapshot loaded from the database). Store implementations MUST
// use this function (or NormalizeNumbers) when reading GitOps objects back.
func UnmarshalJSONPreservingInts(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	normalizeValue(reflect.ValueOf(v))
	return nil
}

// NormalizeNumbers converts json.Number values in a generic tree
// (map[string]any / []any / scalars) to int or float64 (see
// UnmarshalJSONPreservingInts) and returns the converted tree.
func NormalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		return numberValue(x)
	case map[string]any:
		for k, e := range x {
			x[k] = NormalizeNumbers(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = NormalizeNumbers(e)
		}
		return x
	}
	return v
}

func numberValue(n json.Number) any {
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil && i >= math.MinInt && i <= math.MaxInt {
		return int(i)
	}
	f, err := n.Float64()
	if err != nil {
		return string(n)
	}
	if f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 && !bytes.ContainsAny([]byte(n), ".eE") {
		return int(f)
	}
	return f
}

// normalizeValue walks structs, pointers, slices, arrays and maps and
// replaces json.Number values stored in interface-typed locations.
func normalizeValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			normalizeValue(v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		inner := v.Elem()
		if inner.Type() == reflect.TypeOf(json.Number("")) || inner.Kind() == reflect.Map || inner.Kind() == reflect.Slice {
			if v.CanSet() {
				v.Set(reflect.ValueOf(NormalizeNumbers(inner.Interface())))
			}
			return
		}
		normalizeValue(inner)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				normalizeValue(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			normalizeValue(v.Index(i))
		}
	case reflect.Map:
		if v.Type().Elem().Kind() == reflect.Interface {
			for _, k := range v.MapKeys() {
				e := v.MapIndex(k)
				if e.IsNil() {
					continue
				}
				v.SetMapIndex(k, reflect.ValueOf(NormalizeNumbers(e.Interface())))
			}
			return
		}
		if v.Type().Elem().Kind() == reflect.Struct || v.Type().Elem().Kind() == reflect.Pointer {
			for _, k := range v.MapKeys() {
				e := v.MapIndex(k)
				if e.Kind() == reflect.Pointer {
					normalizeValue(e)
				}
			}
		}
	}
}
