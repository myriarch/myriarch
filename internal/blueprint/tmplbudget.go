// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const (
	// maxRangeItems caps the number of collection elements one template
	// string may iterate over (summed over all range actions of one render).
	maxRangeItems = 10000
	// maxRenderWork caps the bytes all helper functions of one render may
	// produce together.
	maxRenderWork = 8 << 20
	// maxRenderTime is the wall clock budget of one template string.
	maxRenderTime = 5 * time.Second
	// The internal functions below are appended to the pipelines of a parsed
	// template (see checkTree), so that every executed pipeline passes a
	// check at execution time:
	//   - rangeGuardName: the pipeline of a range action (lists and maps only,
	//     elements are counted),
	//   - printGuardName: an action whose value is written to the output,
	//   - tickName: every other pipeline (declarations, if, with).
	rangeGuardName = "_rangeGuard"
	printGuardName = "_printGuard"
	tickName       = "_tick"
)

// budget bounds the cost of one template execution: range iterations, bytes
// produced by helpers and wall clock time. A nil budget only applies the
// per-call caps (parse time).
type budget struct {
	items    int
	bytes    int
	deadline time.Time
}

// newBudget starts the budget of one template execution; limit is the wall
// clock time it may take.
func newBudget(limit time.Duration) *budget { return &budget{deadline: time.Now().Add(limit)} }

func (b *budget) expired() error {
	if b != nil && time.Now().After(b.deadline) {
		return errors.New("template evaluation exceeds its time budget")
	}
	return nil
}

func (b *budget) chargeItems(n int) error {
	if b == nil {
		return nil
	}
	b.items += n
	if b.items > maxRangeItems {
		return fmt.Errorf("template iterates over more than %d elements", maxRangeItems)
	}
	return b.expired()
}

func (b *budget) chargeBytes(n int) error {
	if b == nil {
		return nil
	}
	b.bytes += n
	if b.bytes > maxRenderWork {
		return fmt.Errorf("template helper functions produce more than %d bytes", maxRenderWork)
	}
	return b.expired()
}

// result validates the output of a string helper.
func (b *budget) result(s string) (string, error) {
	if len(s) > maxTemplateOutput {
		return "", errTemplateOutput
	}
	if err := b.chargeBytes(len(s)); err != nil {
		return "", err
	}
	return s, nil
}

// sizeWithin reports whether the serialized size of v stays below limit. The
// walk stops as soon as the limit is exceeded, so values that share
// sub-structures cannot be expanded beyond it.
func sizeWithin(v any, limit int) bool { return sizeWithinAll(limit, v) }

// sizeWithinAll is sizeWithin for several values that share one limit.
func sizeWithinAll(limit int, vs ...any) bool {
	left := limit
	var walk func(rv reflect.Value) bool
	walk = func(rv reflect.Value) bool {
		left -= 2
		if left < 0 {
			return false
		}
		switch rv.Kind() {
		case reflect.Invalid:
			return true
		case reflect.String:
			left -= rv.Len()
			return left >= 0
		case reflect.Interface, reflect.Pointer:
			if rv.IsNil() {
				return true
			}
			return walk(rv.Elem())
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				if !walk(rv.Index(i)) {
					return false
				}
			}
		case reflect.Map:
			it := rv.MapRange()
			for it.Next() {
				if !walk(it.Key()) || !walk(it.Value()) {
					return false
				}
			}
		case reflect.Struct:
			for i := 0; i < rv.NumField(); i++ {
				if rv.Type().Field(i).IsExported() && !walk(rv.Field(i)) {
					return false
				}
			}
		default:
			left -= 8
		}
		return left >= 0
	}
	for _, v := range vs {
		if !walk(reflect.ValueOf(v)) {
			return false
		}
	}
	return true
}

// toStr is the serializer behind the string helpers (contains, lower, join,
// ...): it converts v to its string form. Composite values become compact
// JSON; their size is checked BEFORE anything is built, no result exceeds
// maxTemplateOutput, and the bytes it produces count against the budget (as
// does the time it takes).
func (b *budget) toStr(v any) (string, error) {
	var s string
	produced := true
	switch x := v.(type) {
	case nil:
	case string:
		s, produced = x, false
	case bool:
		s = strconv.FormatBool(x)
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	case float64:
		s = strconv.FormatFloat(x, 'g', -1, 64)
	case []byte:
		if len(x) > maxTemplateOutput {
			return "", errTemplateOutput
		}
		s = string(x)
	case fmt.Stringer:
		s = x.String()
	default:
		if !sizeWithin(v, maxTemplateOutput) {
			return "", errTemplateOutput
		}
		if j, err := jsonNoEscape(v); err == nil {
			s = string(j)
		} else {
			s = fmt.Sprint(v)
		}
	}
	if len(s) > maxTemplateOutput {
		return "", errTemplateOutput
	}
	var err error
	if produced {
		err = b.chargeBytes(len(s))
	} else {
		err = b.expired()
	}
	if err != nil {
		return "", err
	}
	return s, nil
}

// checkPrintable validates the value of an action that writes to the output:
// scalars are printed as they are; lists and maps are never converted to text
// implicitly (the formatting of text/template would build their whole text
// before any limit could apply) and must be converted by one of the bounded
// helpers first.
func checkPrintable(v any) error {
	switch x := v.(type) {
	case nil, bool, int, int64, float64:
		return nil
	case string:
		if len(x) > maxTemplateOutput {
			return errTemplateOutput
		}
		return nil
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.String:
		if rv.Len() > maxTemplateOutput {
			return errTemplateOutput
		}
		return nil
	}
	return fmt.Errorf("cannot print a %s directly: convert it with toJson, join or str", compositeName(v))
}

// compositeName names the kind of a value that cannot be printed.
func compositeName(v any) string {
	switch reflect.ValueOf(v).Kind() {
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Map:
		return "map"
	}
	return describe(v)
}

// argsWithin checks that the arguments of a formatting helper stay within
// maxTemplateOutput TOGETHER, before they are formatted.
func argsWithin(args []any) error {
	if !sizeWithinAll(maxTemplateOutput, args...) {
		return errTemplateOutput
	}
	return nil
}

// boundedFuncs returns the helper functions whose results or iteration counts
// are bounded. b may be nil (parse time).
func boundedFuncs(b *budget) template.FuncMap {
	// strFn lifts a string transformation to a bounded helper.
	strFn := func(f func(string) string) func(any) (string, error) {
		return func(v any) (string, error) {
			s, err := b.toStr(v)
			if err != nil {
				return "", err
			}
			return b.result(f(s))
		}
	}
	return template.FuncMap{
		rangeGuardName: func(v any) (any, error) {
			if v == nil {
				return []any{}, nil
			}
			switch rv := reflect.ValueOf(v); rv.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map:
				if err := b.chargeItems(rv.Len()); err != nil {
					return nil, err
				}
				return v, nil
			}
			return nil, fmt.Errorf("range over a %s is not allowed (only lists and maps)", describe(v))
		},
		printGuardName: func(v any) (any, error) {
			if err := b.expired(); err != nil {
				return nil, err
			}
			if err := checkPrintable(v); err != nil {
				return nil, err
			}
			return v, nil
		},
		tickName: func(v any) (any, error) { return v, b.expired() },
		"lower":  strFn(strings.ToLower),
		"upper":  strFn(strings.ToUpper),
		"trim":   strFn(strings.TrimSpace),
		"str":    strFn(func(s string) string { return s }),
		"contains": func(sub string, v any) (bool, error) {
			s, err := b.toStr(v)
			if err != nil {
				return false, err
			}
			return strings.Contains(s, sub), nil
		},
		"hasPrefix": func(p string, v any) (bool, error) {
			s, err := b.toStr(v)
			if err != nil {
				return false, err
			}
			return strings.HasPrefix(s, p), nil
		},
		"hasSuffix": func(x string, v any) (bool, error) {
			s, err := b.toStr(v)
			if err != nil {
				return false, err
			}
			return strings.HasSuffix(s, x), nil
		},
		"trimPrefix": func(p string, v any) (string, error) {
			s, err := b.toStr(v)
			if err != nil {
				return "", err
			}
			return b.result(strings.TrimPrefix(s, p))
		},
		"trimSuffix": func(x string, v any) (string, error) {
			s, err := b.toStr(v)
			if err != nil {
				return "", err
			}
			return b.result(strings.TrimSuffix(s, x))
		},
		"replace": func(old, repl string, v any) (string, error) {
			if old == "" {
				return "", errors.New("replace: the string to replace must not be empty")
			}
			s, err := b.toStr(v)
			if err != nil {
				return "", err
			}
			if size := len(s) + strings.Count(s, old)*(len(repl)-len(old)); size > maxTemplateOutput {
				return "", errTemplateOutput
			}
			return b.result(strings.ReplaceAll(s, old, repl))
		},
		"join": func(sep string, list any) (string, error) {
			var items []any
			switch x := list.(type) {
			case []any:
				items = x
			case []string:
				items = make([]any, len(x))
				for i, e := range x {
					items[i] = e
				}
			case nil:
			default:
				return "", fmt.Errorf("join: expected a list, got %s", describe(list))
			}
			total := 0
			parts := make([]string, len(items))
			for i, e := range items {
				s, err := b.toStr(e)
				if err != nil {
					return "", err
				}
				parts[i] = s
				if total += len(s) + len(sep); total > maxTemplateOutput {
					return "", errTemplateOutput
				}
			}
			return b.result(strings.Join(parts, sep))
		},
		"split": func(sep string, v any) ([]any, error) {
			s, err := b.toStr(v)
			if err != nil {
				return nil, err
			}
			if strings.Count(s, sep)+1 > maxRangeItems {
				return nil, fmt.Errorf("split: more than %d parts", maxRangeItems)
			}
			if err := b.chargeBytes(len(s)); err != nil {
				return nil, err
			}
			parts := strings.Split(s, sep)
			out := make([]any, len(parts))
			for i, p := range parts {
				out[i] = p
			}
			return out, nil
		},
		"list": func(items ...any) ([]any, error) {
			if len(items) > maxRangeItems {
				return nil, fmt.Errorf("list: more than %d elements", maxRangeItems)
			}
			if items == nil {
				return []any{}, nil
			}
			return items, nil
		},
		"toJson": func(v any) (string, error) {
			if !sizeWithin(v, maxTemplateOutput) {
				return "", errTemplateOutput
			}
			s, err := fnToJSON(v)
			if err != nil {
				return "", err
			}
			return b.result(s)
		},
		"printf": func(format string, args ...any) (string, error) {
			if err := argsWithin(args); err != nil {
				return "", err
			}
			s, err := safePrintf(format, args...)
			if err != nil {
				return "", err
			}
			return b.result(s)
		},
		"print": func(args ...any) (string, error) {
			if err := argsWithin(args); err != nil {
				return "", err
			}
			return b.result(fmt.Sprint(args...))
		},
		"println": func(args ...any) (string, error) {
			if err := argsWithin(args); err != nil {
				return "", err
			}
			return b.result(fmt.Sprintln(args...))
		},
		"html": func(args ...any) (string, error) {
			if err := argsWithin(args); err != nil {
				return "", err
			}
			return b.result(template.HTMLEscaper(args...))
		},
		"js": func(args ...any) (string, error) {
			if err := argsWithin(args); err != nil {
				return "", err
			}
			return b.result(template.JSEscaper(args...))
		},
		"urlquery": func(args ...any) (string, error) {
			if err := argsWithin(args); err != nil {
				return "", err
			}
			return b.result(template.URLQueryEscaper(args...))
		},
	}
}
