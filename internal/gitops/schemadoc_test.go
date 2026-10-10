// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/domain"
)

const schemaDoc = "docs/reference/gitops-schema.md"

// TestGitOpsSchemaReference keeps docs/reference/gitops-schema.md complete: every YAML field of
// the five kinds and every issue code of the validation packages must be documented, and the
// tables must not name fields or codes that no longer exist.
func TestGitOpsSchemaReference(t *testing.T) {
	raw, err := os.ReadFile("../../" + schemaDoc)
	if err != nil {
		t.Fatal(err)
	}
	sections := splitSections(string(raw))
	common := firstColumn(sections["Common fields"], "[^`]+")
	meta := map[string]bool{}
	for kind, typ := range map[string]reflect.Type{
		"Fleet": reflect.TypeOf(domain.Fleet{}), "Blueprint": reflect.TypeOf(domain.Blueprint{}),
		"RolloutPolicy": reflect.TypeOf(domain.RolloutPolicy{}), "Executor": reflect.TypeOf(domain.Executor{}),
		"Inventory": reflect.TypeOf(domain.Inventory{}),
	} {
		body, ok := sections[kind]
		if !ok {
			t.Errorf("%s has no section \"## %s\": add the heading with a field table", schemaDoc, kind)
			continue
		}
		want := map[string]bool{}
		objectPaths(typ, want)
		have := firstColumn(body, "[^`]+")
		for p := range want {
			table, where := have, kind
			if strings.HasPrefix(p, "metadata.") {
				table, where, meta[p] = common, "Common fields", true
			}
			if !table[p] {
				t.Errorf("field %s is not documented: add a row for `%s` to the \"## %s\" table in %s", p, p, where, schemaDoc)
			}
		}
		for p := range have {
			if strings.HasPrefix(p, "metadata.") || (strings.HasPrefix(p, "spec.") && !want[p]) {
				t.Errorf("%s lists `%s` in \"## %s\", but the Go schema of %s has no such field there: remove or move the row", schemaDoc, p, kind, kind)
			}
		}
	}
	for p := range common {
		if strings.HasPrefix(p, "metadata.") && !meta[p] {
			t.Errorf("%s lists `%s` in \"## Common fields\", but ObjectMeta has no such field: remove the row", schemaDoc, p)
		}
	}

	documented := firstColumn(sections["Validation and issue codes"], "[a-z][a-z0-9_]*")
	emitted := emittedIssueCodes(t, "../blueprint", ".")
	for c := range emitted {
		if !documented[c] {
			t.Errorf("issue code %q is emitted by internal/blueprint or internal/gitops but missing in the issue-code table: add a row to \"## Validation and issue codes\" in %s", c, schemaDoc)
		}
	}
	for c := range documented {
		if !emitted[c] {
			t.Errorf("issue code %q is documented in %s but no longer emitted by internal/blueprint or internal/gitops: remove the row", c, schemaDoc)
		}
	}
}

// objectPaths collects the YAML paths of a kind relative to the object: the embedded ObjectMeta
// becomes "metadata.*", the embedded *Spec struct "spec.*". The other top-level fields (Source)
// are not part of the YAML.
func objectPaths(typ reflect.Type, out map[string]bool) {
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); f.Anonymous {
			prefix := "spec"
			if f.Type == reflect.TypeOf(domain.ObjectMeta{}) {
				prefix = "metadata"
			}
			walkStruct(f.Type, prefix, out)
		}
	}
}

// walkStruct adds every yaml-tagged field below prefix; slices of structs get the suffix "[]".
// Maps, interfaces, scalars and named scalar types (domain.Duration, ExecutorType, ...) are leaves.
func walkStruct(typ reflect.Type, prefix string, out map[string]bool) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			walkStruct(f.Type, prefix, out)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name[:1]) + f.Name[1:]
		}
		path := prefix + "." + name
		out[path] = true
		ft, isSlice := f.Type, f.Type.Kind() == reflect.Slice
		if isSlice {
			ft = ft.Elem()
		}
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			if isSlice {
				path += "[]"
			}
			walkStruct(ft, path, out)
		}
	}
}

// splitSections maps the text of every H2 heading to the lines below it (H3 stays inside).
func splitSections(doc string) map[string]string {
	out := map[string]string{}
	var cur string
	for _, line := range strings.Split(doc, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			cur = strings.TrimSpace(h)
		} else if cur != "" {
			out[cur] += line + "\n"
		}
	}
	return out
}

// firstColumn returns the backticked entries (matching pattern) in the first column of the
// table rows of a section. A cell may list several entries.
func firstColumn(section, pattern string) map[string]bool {
	out := map[string]bool{}
	entry := regexp.MustCompile("`(" + pattern + ")`")
	for _, line := range strings.Split(section, "\n") {
		if cell, _, ok := strings.Cut(strings.TrimPrefix(line, "|"), "|"); ok && strings.HasPrefix(line, "|") {
			for _, m := range entry.FindAllStringSubmatch(cell, -1) {
				out[m[1]] = true
			}
		}
	}
	return out
}

// emittedIssueCodes parses the non-test files of the given packages and collects the string
// literals used as issue codes: (i) the value of a Code: key in an Issue composite literal
// (identified by its Severity and Code keys, the type may be elided), (ii) the argument at the
// position of a parameter named "code" of a function, method, function literal or function-typed
// parameter, (iii) a string assigned to a variable named "code", (iv) the second argument of a
// call whose first argument is a Severity constant (callbacks of unnamed function type).
func emittedIssueCodes(t *testing.T, dirs ...string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range pkgs {
			names := make([]string, 0, len(pkg.Files))
			for n := range pkg.Files {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				files = append(files, pkg.Files[n])
			}
		}
	}
	codeParam := map[string]int{} // function name -> index of its "code" parameter
	record := func(name string, ft *ast.FuncType) {
		idx := 0
		for _, field := range ft.Params.List {
			if len(field.Names) == 0 {
				idx++
			}
			for _, n := range field.Names {
				if n.Name == "code" {
					codeParam[name] = idx
				}
				idx++
			}
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				record(x.Name.Name, x.Type)
			case *ast.AssignStmt:
				for i, rhs := range x.Rhs {
					if lit, ok := rhs.(*ast.FuncLit); ok && i < len(x.Lhs) {
						if id, ok := x.Lhs[i].(*ast.Ident); ok {
							record(id.Name, lit.Type)
						}
					}
				}
			case *ast.Field:
				if ft, ok := x.Type.(*ast.FuncType); ok {
					for _, n := range x.Names {
						record(n.Name, ft)
					}
				}
			}
			return true
		})
	}
	out := map[string]bool{}
	add := func(e ast.Expr) {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out[s] = true
			}
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				var code ast.Expr
				severity := false
				for _, el := range x.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok {
							severity = severity || k.Name == "Severity"
							if k.Name == "Code" {
								code = kv.Value
							}
						}
					}
				}
				if severity && code != nil {
					add(code)
				}
			case *ast.CallExpr:
				if idx, ok := codeParam[lastName(x.Fun)]; ok && idx < len(x.Args) {
					add(x.Args[idx])
				}
				if len(x.Args) >= 2 && strings.HasPrefix(lastName(x.Args[0]), "Severity") {
					add(x.Args[1])
				}
			case *ast.AssignStmt:
				for i, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == "code" && i < len(x.Rhs) {
						add(x.Rhs[i])
					}
				}
			}
			return true
		})
	}
	return out
}

// lastName returns the last identifier of an expression (pkg.Name, recv.method, Name).
func lastName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}
