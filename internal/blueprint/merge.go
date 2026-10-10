// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"fmt"
	"strings"
	"time"
)

// pathSeparator is the confmap key separator used by "remove" paths.
const pathSeparator = "::"

// normalizeTree deep-copies v into the canonical value space of rendered
// trees (map[string]any, []any, string, bool, int, float64, nil). Maps with
// non-string keys (yaml.v3 produces map[any]any for them) get stringified
// keys; the copy shares no memory with the input.
func normalizeTree(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalizeTree(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[fmt.Sprint(k)] = normalizeTree(e)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = e
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeTree(e)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	case int64:
		return int(x)
	case int32:
		return int(x)
	case uint64:
		if x <= uint64(^uint(0)>>1) {
			return int(x)
		}
		return float64(x)
	case uint32:
		return int(x)
	case float32:
		return float64(x)
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format("2006-01-02")
		}
		return x.Format(time.RFC3339Nano)
	default:
		return v
	}
}

// mergeMaps returns a new map: dst overlaid with src (docs/concepts/blueprints.md,
// "Merge semantics"). Map + map merge recursively; everything else (lists, scalars, null)
// is replaced. Neither input is modified; the result shares no memory with
// either.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = normalizeTree(v)
	}
	for k, sv := range src {
		if sm, ok := asMap(sv); ok {
			if dm, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(dm, sm)
				continue
			}
		}
		out[k] = normalizeTree(sv)
	}
	return out
}

func asMap(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case map[any]any:
		m, _ := normalizeTree(x).(map[string]any)
		return m, true
	}
	return nil, false
}

// removePaths deletes confmap style paths ("a::b::c") from tree (in place).
// Missing paths are ignored; lists cannot be addressed.
func removePaths(tree map[string]any, paths []string) {
	for _, p := range paths {
		parts := strings.Split(p, pathSeparator)
		cur := tree
		for i, part := range parts {
			if i == len(parts)-1 {
				delete(cur, part)
				break
			}
			next, ok := cur[part].(map[string]any)
			if !ok {
				break
			}
			cur = next
		}
	}
}

// containsPathSeparator reports whether a config key contains "::".
func containsPathSeparator(k string) bool { return strings.Contains(k, pathSeparator) }

// NormalizeTree deep-copies a decoded YAML/JSON value into the canonical
// value space of rendered trees (map[string]any, []any, string, bool, int,
// float64, nil). Exported for the GitOps parser.
func NormalizeTree(v any) any { return normalizeTree(v) }
