// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/myriarch/myriarch/internal/domain"
)

// maxMessageValue caps how much of an untrusted value (a parameter, an agent
// attribute, a message given by a template) an error message quotes.
const maxMessageValue = 40

// maxRequiredMessage caps the message of required (written by the author of the
// template, but it may be built from values).
const maxRequiredMessage = 200

// quoteShort quotes the beginning of an untrusted string for an error message.
func quoteShort(s string) string { return strconv.Quote(truncate(s, maxMessageValue)) }

const (
	leftDelim  = "[["
	rightDelim = "]]"
	// maxTemplateOutput caps the output of one template string.
	maxTemplateOutput = 64 << 10
	// maxPrintfWidth caps width and precision of printf verbs.
	maxPrintfWidth = 256
	// maxRangeDepth caps the nesting of range actions.
	maxRangeDepth = 3
)

// errTemplateOutput is returned when a template produces too much output.
var errTemplateOutput = fmt.Errorf("template output exceeds %d bytes", maxTemplateOutput)

// cappedBuffer is a bytes.Buffer that refuses to grow beyond maxTemplateOutput.
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxTemplateOutput {
		return 0, errTemplateOutput
	}
	return b.Buffer.Write(p)
}

// safePrintf is printf with bounded output: the width and precision of the
// verbs are capped, '*' widths and explicit argument indexes ("%[1]d") are not
// available, and a format uses at most as many verbs as it has arguments. So
// every verb prints its own argument (which argsWithin has bounded), and a
// format string cannot make fmt produce more than its arguments justify.
func safePrintf(format string, args ...any) (string, error) {
	if err := checkPrintfFormat(format, len(args)); err != nil {
		return "", err
	}
	return fmt.Sprintf(format, args...), nil
}

// checkPrintfFormat walks the verbs of a printf format string.
func checkPrintfFormat(format string, nargs int) error {
	verbs := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 { // flags
			i++
		}
		for part := 0; part < 2; part++ { // width, then precision
			if part == 1 {
				if i >= len(format) || format[i] != '.' {
					break
				}
				i++
			}
			if i < len(format) && format[i] == '*' {
				return errors.New("printf: '*' width or precision is not allowed")
			}
			start := i
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				i++
			}
			if i > start {
				if n, err := strconv.Atoi(format[start:i]); err != nil || n > maxPrintfWidth {
					return fmt.Errorf("printf: width or precision above %d is not allowed", maxPrintfWidth)
				}
			}
		}
		if i >= len(format) { // "%" at the end: no verb
			break
		}
		switch format[i] {
		case '[':
			return errors.New("printf: explicit argument indexes are not allowed")
		case '%': // literal percent sign, takes no argument
			continue
		}
		verbs++
	}
	if verbs > nargs {
		return fmt.Errorf("printf: the format has %d verbs but only %d arguments", verbs, nargs)
	}
	return nil
}

// checkTree rejects constructs with unbounded cost (range over integers,
// deeply nested ranges, template calls) and routes the pipelines of the
// template through internal functions that run at execution time:
//   - range pipelines through rangeGuard, which accepts only lists and maps
//     and counts their elements against the per-render budget,
//   - actions that print through printGuard, which refuses lists and maps
//     (they must be converted with a bounded helper) and checks the time
//     budget,
//   - all other pipelines (declarations, if, with) through tick, which checks
//     the time budget.
//
// So every executed pipeline consults the time budget, whatever it does.
func checkTree(t *template.Template) error {
	for _, tt := range t.Templates() {
		if tt.Tree == nil || tt.Tree.Root == nil {
			continue
		}
		if tt.Name() != t.Name() {
			return errors.New("define and block are not allowed")
		}
		if err := checkNode(tt.Tree.Root, 0); err != nil {
			return err
		}
	}
	return nil
}

var intFuncs = map[string]bool{"int": true, "add": true, "sub": true, "mul": true, "div": true, "len": true}

func checkNode(n parse.Node, depth int) error {
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return nil
		}
		for _, c := range x.Nodes {
			if err := checkNode(c, depth); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		if len(x.Pipe.Decl) == 0 {
			appendCall(x.Pipe, printGuardName) // the value is written to the output
		} else {
			appendCall(x.Pipe, tickName) // variable declaration or assignment
		}
	case *parse.IfNode:
		return checkBranch(&x.BranchNode, depth)
	case *parse.WithNode:
		return checkBranch(&x.BranchNode, depth)
	case *parse.TemplateNode:
		return errors.New("template and block are not allowed")
	case *parse.RangeNode:
		if depth+1 > maxRangeDepth {
			return fmt.Errorf("range is nested deeper than %d levels", maxRangeDepth)
		}
		if x.Pipe != nil && len(x.Pipe.Cmds) > 0 {
			if cmd := x.Pipe.Cmds[len(x.Pipe.Cmds)-1]; len(cmd.Args) > 0 {
				switch a := cmd.Args[0].(type) {
				case *parse.NumberNode:
					return errors.New("range over a number is not allowed")
				case *parse.IdentifierNode:
					if intFuncs[a.Ident] {
						return fmt.Errorf("range over the result of %q (an integer) is not allowed", a.Ident)
					}
				case *parse.PipeNode:
					if len(a.Cmds) > 0 && len(a.Cmds[0].Args) > 0 {
						if id, ok := a.Cmds[0].Args[0].(*parse.IdentifierNode); ok && intFuncs[id.Ident] {
							return fmt.Errorf("range over the result of %q (an integer) is not allowed", id.Ident)
						}
					}
				}
			}
		}
		appendCall(x.Pipe, rangeGuardName)
		if err := checkNode(x.List, depth+1); err != nil {
			return err
		}
		if x.ElseList != nil {
			return checkNode(x.ElseList, depth+1)
		}
	}
	return nil
}

func checkBranch(b *parse.BranchNode, depth int) error {
	appendCall(b.Pipe, tickName)
	if err := checkNode(b.List, depth); err != nil {
		return err
	}
	if b.ElseList != nil {
		return checkNode(b.ElseList, depth)
	}
	return nil
}

// tmplEntry is a parsed template string.
type tmplEntry struct {
	err error
	// tmpl renders the string as interpolation (result stays a string).
	tmpl *template.Template
	// whole: the string is exactly one "[[ EXPR ]]" action.
	whole bool
	// typed renders "[[ toJson (EXPR) ]]" for typed whole-expression results.
	typed *template.Template
}

// evalCtx binds the template data and the context dependent functions.
type evalCtx struct {
	r     *Renderer
	data  map[string]any
	attrs domain.AttributeView
	// params is the resolved parameter map (also reachable as .params).
	params map[string]any
	// path is the confmap path currently evaluated (error messages).
	path string
}

func newEvalCtx(r *Renderer, data map[string]any, attrs domain.AttributeView, params map[string]any) *evalCtx {
	return &evalCtx{r: r, data: data, attrs: attrs, params: params}
}

// templateData builds the "." of a template (docs/concepts/blueprints.md, "Templates").
func templateData(in RenderInput, leaf string, params map[string]any) map[string]any {
	agentID := ""
	if in.Agent.ID != nil {
		agentID = in.Agent.ID.String()
	}
	attrs := map[string]string(in.Agent.Attributes)
	if attrs == nil {
		attrs = map[string]string{}
	}
	fleetName := ""
	fleetLabels := map[string]string{}
	if in.Fleet != nil {
		fleetName = in.Fleet.Name
		for k, v := range in.Fleet.Labels {
			fleetLabels[k] = v
		}
	}
	return map[string]any{
		"params":    params,
		"agent":     map[string]any{"id": agentID, "attributes": attrs},
		"fleet":     map[string]any{"name": fleetName, "labels": fleetLabels},
		"blueprint": map[string]any{"name": leaf},
	}
}

// ------------------------------------------------------------- parsing ----

var staticFuncs = template.FuncMap{
	"default": fnDefault,
	"required": func(msg string, v any) (any, error) {
		if isEmpty(v) {
			return nil, errors.New(truncate(msg, maxRequiredMessage))
		}
		return v, nil
	},
	"dict":  fnDict,
	"int":   fnInt,
	"float": fnFloat,
	"bool":  fnBool,
	"add":   func(a, b any) (int, error) { return arith(a, b, func(x, y int64) (int64, error) { return x + y, nil }) },
	"sub":   func(a, b any) (int, error) { return arith(a, b, func(x, y int64) (int64, error) { return x - y, nil }) },
	"mul":   func(a, b any) (int, error) { return arith(a, b, func(x, y int64) (int64, error) { return x * y, nil }) },
	"div":   func(a, b any) (int, error) { return arith(a, b, divInts) },
}

func divInts(x, y int64) (int64, error) {
	if y == 0 {
		return 0, errors.New("division by zero")
	}
	return x / y, nil
}

// stubFuncs are the context dependent functions at parse time; the real
// implementations are bound per evaluation.
var stubFuncs = template.FuncMap{
	"attr":    func(string) (string, error) { return "", errors.New("attr: no context") },
	"attrOr":  func(string, string) string { return "" },
	"hasAttr": func(string) bool { return false },
	"param":   func(string) (any, error) { return nil, errors.New("param: no context") },
}

// ctxFuncs binds the context dependent functions.
func (c *evalCtx) ctxFuncs() template.FuncMap {
	return template.FuncMap{
		"attr": func(key string) (string, error) {
			if v, ok := c.attrs[key]; ok {
				return v, nil
			}
			return "", fmt.Errorf("attr: agent has no attribute %s", quoteShort(key))
		},
		"attrOr": func(key, def string) string {
			if v, ok := c.attrs[key]; ok {
				return v
			}
			return def
		},
		"hasAttr": func(key string) bool {
			_, ok := c.attrs[key]
			return ok
		},
		"param": func(name string) (any, error) {
			if v, ok := c.params[name]; ok {
				return v, nil
			}
			return nil, fmt.Errorf("param: parameter %s has no value", quoteShort(name))
		},
	}
}

func newTemplate(name, src string) (*template.Template, error) {
	t := template.New(name).Delims(leftDelim, rightDelim).Option("missingkey=error").Funcs(staticFuncs).Funcs(boundedFuncs(nil)).Funcs(stubFuncs)
	return t.Parse(src)
}

// parseEntry parses src (cached).
func (r *Renderer) parseEntry(src string) *tmplEntry {
	if e, ok := r.tmplCache.Get(src); ok {
		return e
	}
	e := buildEntry(src)
	r.tmplCache.Add(src, e)
	return e
}

func buildEntry(src string) *tmplEntry {
	e := &tmplEntry{}
	t, err := newTemplate("t", src)
	if err != nil {
		e.err = err
		return e
	}
	if err := checkTree(t); err != nil {
		e.err = err
		return e
	}
	e.tmpl = t
	trimmed := strings.TrimSpace(src)
	if strings.HasPrefix(trimmed, leftDelim) && strings.HasSuffix(trimmed, rightDelim) && t.Tree != nil && t.Tree.Root != nil {
		var actions int
		single := true
		for _, n := range t.Tree.Root.Nodes {
			switch x := n.(type) {
			case *parse.ActionNode:
				actions++
			case *parse.TextNode:
				if len(bytes.TrimSpace(x.Text)) > 0 {
					single = false
				}
			default:
				single = false
			}
		}
		if single && actions == 1 {
			inner := trimmed[len(leftDelim) : len(trimmed)-len(rightDelim)]
			inner = strings.TrimSpace(inner)
			inner = strings.TrimPrefix(inner, "- ")
			inner = strings.TrimSuffix(inner, " -")
			typed, terr := newTemplate("typed", leftDelim+" toJson ("+inner+") "+rightDelim)
			if terr == nil {
				// the typed form is a template of its own: same checks, same guards
				if err := checkTree(typed); err != nil {
					e.err = err
					return e
				}
				e.whole = true
				e.typed = typed
			}
		}
	}
	return e
}

// ---------------------------------------------------------- evaluation ----

// templateError distinguishes syntax from execution errors.
type templateError struct {
	syntax bool
	err    error
}

func (e *templateError) Error() string { return e.err.Error() }
func (e *templateError) Unwrap() error { return e.err }

func (c *evalCtx) exec(t *template.Template) (string, error) {
	ct, err := t.Clone()
	if err != nil {
		return "", &templateError{err: err}
	}
	ct.Funcs(c.ctxFuncs()).Funcs(boundedFuncs(newBudget(c.r.timeBudget())))
	var buf cappedBuffer
	if err := ct.Execute(&buf, c.data); err != nil {
		return "", &templateError{err: err}
	}
	return buf.String(), nil
}

// evalString evaluates one string value: typed for whole expressions, a
// string otherwise.
func (c *evalCtx) evalString(s string) (any, error) {
	if !strings.Contains(s, leftDelim) {
		return s, nil
	}
	e := c.r.parseEntry(s)
	if e.err != nil {
		return nil, &templateError{syntax: true, err: e.err}
	}
	if e.whole {
		out, err := c.exec(e.typed)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(strings.NewReader(out))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, &templateError{err: fmt.Errorf("expression result is not representable: %v", err)}
		}
		return domain.NormalizeNumbers(v), nil
	}
	return c.exec(e.tmpl)
}

// evalKey evaluates a map key (always interpolation, always a string).
func (c *evalCtx) evalKey(k string) (string, error) {
	if !strings.Contains(k, leftDelim) {
		return k, nil
	}
	e := c.r.parseEntry(k)
	if e.err != nil {
		return "", &templateError{syntax: true, err: e.err}
	}
	return c.exec(e.tmpl)
}

// treeIssue is an error with the confmap path of the offending node.
type treeIssue struct {
	path   string
	syntax bool
	err    error
	dupKey bool
}

// evalTree evaluates all strings and keys of tree. It never modifies tree and
// returns every problem it finds (bounded).
func (c *evalCtx) evalTree(tree map[string]any) (map[string]any, []treeIssue) {
	var issues []treeIssue
	out := c.evalMap(tree, "", &issues)
	return out, issues
}

const maxTreeIssues = 25

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + pathSeparator + key
}

func (c *evalCtx) evalMap(m map[string]any, path string, issues *[]treeIssue) map[string]any {
	out := make(map[string]any, len(m))
	keys := sortedKeys(m)
	for _, k := range keys {
		v := m[k]
		nk, err := c.evalKey(k)
		if err != nil {
			c.report(issues, joinPath(path, k), err)
			continue
		}
		if _, dup := out[nk]; dup {
			if len(*issues) < maxTreeIssues {
				*issues = append(*issues, treeIssue{path: joinPath(path, nk), dupKey: true, err: fmt.Errorf("duplicate key %q after template evaluation", nk)})
			}
			continue
		}
		out[nk] = c.evalValue(v, joinPath(path, nk), issues)
	}
	return out
}

func (c *evalCtx) evalValue(v any, path string, issues *[]treeIssue) any {
	switch x := v.(type) {
	case string:
		r, err := c.evalString(x)
		if err != nil {
			c.report(issues, path, err)
			return nil
		}
		return r
	case map[string]any:
		return c.evalMap(x, path, issues)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = c.evalValue(e, path+"["+strconv.Itoa(i)+"]", issues)
		}
		return out
	default:
		return v
	}
}

func (c *evalCtx) report(issues *[]treeIssue, path string, err error) {
	if len(*issues) >= maxTreeIssues {
		return
	}
	var te *templateError
	syntax := errors.As(err, &te) && te.syntax
	*issues = append(*issues, treeIssue{path: path, syntax: syntax, err: err})
}

// EvalValue evaluates all "[[ ... ]]" templates inside v (a tree of maps,
// lists and scalars) with the given data context ("." in the templates). It
// is used for executor extraVars (docs/concepts/executors.md, "AWX"): the functions
// attr/param are available but see empty contexts. v is not modified.
func (r *Renderer) EvalValue(v any, data map[string]any) (any, error) {
	c := newEvalCtx(r, data, domain.AttributeView{}, map[string]any{})
	var issues []treeIssue
	out := c.evalValue(normalizeTree(v), "", &issues)
	if len(issues) > 0 {
		return nil, fmt.Errorf("%s: %w", issues[0].path, issues[0].err)
	}
	return out, nil
}

// CheckTemplates parses every template string in v and returns the syntax
// errors by path (no evaluation).
func (r *Renderer) CheckTemplates(v any) map[string]error {
	errs := map[string]error{}
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case string:
			if strings.Contains(x, leftDelim) {
				if e := r.parseEntry(x); e.err != nil {
					errs[path] = e.err
				}
			}
		case map[string]any:
			for k, e := range x {
				if strings.Contains(k, leftDelim) {
					if te := r.parseEntry(k); te.err != nil {
						errs[joinPath(path, k)] = te.err
					}
				}
				walk(e, joinPath(path, k))
			}
		case map[any]any:
			for k, e := range x {
				walk(e, joinPath(path, fmt.Sprint(k)))
			}
		case []any:
			for i, e := range x {
				walk(e, path+"["+strconv.Itoa(i)+"]")
			}
		}
	}
	walk(v, "")
	return errs
}

// ------------------------------------------------------------ functions ----

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() == 0
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

func fnDefault(def, v any) any {
	if isEmpty(v) {
		return def
	}
	return v
}

func fnDict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict: odd number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d must be a string, got %s", i/2+1, describe(kv[i]))
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

func fnInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, errors.New("int: cannot convert NaN or infinity")
		}
		return int(x), nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("int: %s is not an integer", quoteShort(x))
		}
		return i, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("int: cannot convert a %s", describe(v))
}

func fnFloat(v any) (float64, error) {
	switch x := v.(type) {
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case float64:
		return x, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, fmt.Errorf("float: %s is not a number", quoteShort(x))
		}
		return f, nil
	}
	return 0, fmt.Errorf("float: cannot convert a %s", describe(v))
}

func fnBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(x))
		if err != nil {
			return false, fmt.Errorf("bool: %s is not a boolean", quoteShort(x))
		}
		return b, nil
	case int:
		return x != 0, nil
	case float64:
		return x != 0, nil
	}
	return false, fmt.Errorf("bool: cannot convert a %s", describe(v))
}

func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case int32:
		return int64(x), nil
	case float64:
		if x != math.Trunc(x) {
			return 0, errors.New("the number is not an integer")
		}
		return int64(x), nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s is not an integer", quoteShort(x))
		}
		return i, nil
	}
	return 0, fmt.Errorf("a %s is not an integer", describe(v))
}

func arith(a, b any, f func(x, y int64) (int64, error)) (int, error) {
	x, err := toInt64(a)
	if err != nil {
		return 0, err
	}
	y, err := toInt64(b)
	if err != nil {
		return 0, err
	}
	r, err := f(x, y)
	if err != nil {
		return 0, err
	}
	return int(r), nil
}

func fnToJSON(v any) (string, error) {
	b, err := jsonNoEscape(normalizeForJSON(v))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// normalizeForJSON makes sure map keys sort deterministically and values
// are plain JSON types.
func normalizeForJSON(v any) any {
	switch x := v.(type) {
	case map[string]string:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[k] = x[k]
		}
		return out
	case domain.AttributeView:
		return normalizeForJSON(map[string]string(x))
	}
	return v
}

// appendCall appends a call of the internal function name to a pipeline: the
// value of the pipeline passes through it unchanged (or the call fails).
func appendCall(p *parse.PipeNode, name string) {
	if p == nil {
		return
	}
	p.Cmds = append(p.Cmds, &parse.CommandNode{
		NodeType: parse.NodeCommand,
		Pos:      p.Pos,
		Args:     []parse.Node{parse.NewIdentifier(name).SetPos(p.Pos)},
	})
}
