// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package metrics_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/brand"
)

const promImportPath = "github.com/prometheus/client_golang/prometheus"

// TestMetricNamesUseTheProjectNamespace keeps every metric name a full string
// literal (so it stays searchable) and checks that each starts with the
// project namespace: myriarch_ for the server, myriarch_sim_ for the
// simulator. It parses the production sources, so a new metric with a
// different prefix fails here.
func TestMetricNamesUseTheProjectNamespace(t *testing.T) {
	root := ".." // internal/
	fset := token.NewFileSet()
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		promName := ""
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p != promImportPath {
				continue
			}
			promName = "prometheus"
			if imp.Name != nil {
				promName = imp.Name.Name
			}
		}
		if promName == "" {
			return nil
		}
		want := brand.MetricsNamespace + "_"
		if rel, rerr := filepath.Rel(root, path); rerr == nil && strings.HasPrefix(filepath.ToSlash(rel), "sim/") {
			want = brand.SimMetricsNamespace + "_"
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != promName || !strings.HasSuffix(sel.Sel.Name, "Opts") {
				return true
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Name" {
					continue
				}
				pos := fset.Position(kv.Pos())
				bl, ok := kv.Value.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					t.Errorf("%s: metric Name must be a string literal", pos)
					continue
				}
				name, _ := strconv.Unquote(bl.Value)
				found++
				if !strings.HasPrefix(name, want) {
					t.Errorf("%s: metric %q must start with %q", pos, name, want)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 30 {
		t.Fatalf("found only %d metric definitions, the scan looks broken", found)
	}
}
