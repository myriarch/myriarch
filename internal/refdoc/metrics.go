// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/myriarch/myriarch/internal/brand"
)

// MetricsFile is the name of the generated metrics page in docs/reference.
const MetricsFile = "metrics.md"

const promImportPath = "github.com/prometheus/client_golang/prometheus"

// Metric is one metric definition found in the Go sources.
type Metric struct {
	Name string
	// Type is counter, gauge, histogram or summary.
	Type    string
	Help    string
	Labels  []string
	Buckets []float64
	// Objectives are the quantiles of a summary, in ascending order.
	Objectives []float64
	// Package is the directory of the definition, relative to internal/ (relative to the root for cmd/).
	Package string
	// Binary is the program that exposes the metric: myriarchd or myriarch-sim.
	Binary string
}

// kinds maps a constructor of the Prometheus client to the type of the metric and its options type.
var kinds = map[string]struct {
	typ, opts string
	vec       bool
}{
	"NewCounter":      {"counter", "CounterOpts", false},
	"NewCounterVec":   {"counter", "CounterOpts", true},
	"NewGauge":        {"gauge", "GaugeOpts", false},
	"NewGaugeVec":     {"gauge", "GaugeOpts", true},
	"NewHistogram":    {"histogram", "HistogramOpts", false},
	"NewHistogramVec": {"histogram", "HistogramOpts", true},
	"NewSummary":      {"summary", "SummaryOpts", false},
	"NewSummaryVec":   {"summary", "SummaryOpts", true},
}

// ScanMetrics finds the metric definitions in the non-test Go files below root/internal and root/cmd. Every definition must be
// a literal call of a constructor of the Prometheus client with an options literal, so that the reference can be generated from
// the syntax tree; anything else is reported as an error that names the file and line. The result is sorted by name.
func ScanMetrics(root string) ([]Metric, error) {
	fset := token.NewFileSet()
	var metrics []Metric
	var errs []error
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && p == filepath.Join(root, top) {
					return fs.SkipDir
				}
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "testdata" || rel == "internal/refdoc" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			ms, ferrs := scanFile(fset, f, rel)
			metrics = append(metrics, ms...)
			errs = append(errs, ferrs...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	byName := map[string]Metric{}
	for _, m := range metrics {
		if old, dup := byName[m.Name]; dup {
			a, b := old, m
			a.Package, b.Package = "", ""
			if fmt.Sprint(a) != fmt.Sprint(b) {
				return nil, fmt.Errorf("the metric %s is defined twice with different definitions (%s and %s)", m.Name, old.Package, m.Package)
			}
			continue
		}
		byName[m.Name] = m
	}
	out := make([]Metric, 0, len(byName))
	for _, m := range byName {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// scanFile finds the metric definitions of one file. rel is the path relative to the root, with forward slashes.
func scanFile(fset *token.FileSet, f *ast.File, rel string) ([]Metric, []error) {
	alias := ""
	var errs []error
	at := func(n ast.Node) string { return fmt.Sprintf("%s:%d", rel, fset.Position(n.Pos()).Line) }
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		switch {
		case strings.HasPrefix(p, promImportPath+"/promauto"):
			errs = append(errs, fmt.Errorf("%s: prometheus/promauto is not supported by the metrics reference; extend internal/refdoc", at(imp)))
		case p == promImportPath:
			alias = "prometheus"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			if alias == "." || alias == "_" {
				errs = append(errs, fmt.Errorf("%s: the import of the Prometheus client as %q is not supported by the metrics reference; extend internal/refdoc", at(imp), alias))
				alias = ""
			}
		}
	}
	if alias == "" {
		return nil, errs
	}
	pkg := strings.TrimPrefix(filepath.ToSlash(filepath.Dir(rel)), "internal/")
	var out []Metric
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != alias {
			return true
		}
		name := sel.Sel.Name
		kind, supported := kinds[name]
		switch {
		case supported:
			m, err := parseMetric(call, alias, name, kind.typ, kind.opts, kind.vec)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", at(call), err))
				return true
			}
			m.Package = pkg
			m.Binary = brand.ServerBinary
			if strings.HasPrefix(m.Name, brand.SimMetricsNamespace+"_") {
				m.Binary = brand.SimBinary
			}
			out = append(out, m)
		case name == "NewRegistry":
		case strings.HasPrefix(name, "New") || name == "MustNewConstMetric":
			errs = append(errs, fmt.Errorf("%s: prometheus.%s is not supported by the metrics reference; extend internal/refdoc", at(call), name))
		}
		return true
	})
	return out, errs
}

// parseMetric reads the arguments of one constructor call.
func parseMetric(call *ast.CallExpr, alias, ctor, typ, optsType string, vec bool) (Metric, error) {
	m := Metric{Type: typ}
	want := 1
	if vec {
		want = 2
	}
	if len(call.Args) != want {
		return m, fmt.Errorf("prometheus.%s takes %d arguments", ctor, want)
	}
	lit, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return m, fmt.Errorf("the first argument of prometheus.%s must be a prometheus.%s literal", ctor, optsType)
	}
	if sel, ok := lit.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != optsType || !isIdent(sel.X, alias) {
		return m, fmt.Errorf("the first argument of prometheus.%s must be a prometheus.%s literal", ctor, optsType)
	}
	seen := map[string]bool{}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return m, fmt.Errorf("the prometheus.%s literal must use field names", optsType)
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil {
			return m, fmt.Errorf("the prometheus.%s literal has a key that is not a field name", optsType)
		}
		seen[key.Name] = true
		switch key.Name {
		case "Name":
			s, err := stringLiteral(kv.Value)
			if err != nil {
				return m, fmt.Errorf("Name: %w", err)
			}
			m.Name = s
		case "Help":
			s, err := stringLiteral(kv.Value)
			if err != nil {
				return m, fmt.Errorf("Help: %w", err)
			}
			m.Help = s
		case "Buckets":
			if typ != "histogram" {
				return m, fmt.Errorf("Buckets is only supported for histograms")
			}
			v, err := floatList(kv.Value)
			if err != nil {
				return m, fmt.Errorf("Buckets: %w", err)
			}
			m.Buckets = v
		case "Objectives":
			if typ != "summary" {
				return m, fmt.Errorf("Objectives is only supported for summaries")
			}
			ml, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				return m, fmt.Errorf("Objectives must be a map literal")
			}
			for _, e := range ml.Elts {
				pair, ok := e.(*ast.KeyValueExpr)
				if !ok {
					return m, fmt.Errorf("Objectives must be a map literal")
				}
				q, err := numberLiteral(pair.Key)
				if err != nil {
					return m, fmt.Errorf("Objectives: %w", err)
				}
				m.Objectives = append(m.Objectives, q)
			}
			sort.Float64s(m.Objectives)
		default:
			return m, fmt.Errorf("%s in the prometheus.%s literal is not supported by the metrics reference (only Name, Help and Buckets or Objectives); extend internal/refdoc", key.Name, optsType)
		}
	}
	if m.Name == "" {
		return m, fmt.Errorf("the prometheus.%s literal needs a Name", optsType)
	}
	if m.Help == "" {
		return m, fmt.Errorf("%s: the prometheus.%s literal needs a non-empty Help", m.Name, optsType)
	}
	if !strings.HasSuffix(m.Help, ".") {
		return m, fmt.Errorf("%s: Help must end with a period: %q", m.Name, m.Help)
	}
	if typ == "histogram" && !seen["Buckets"] {
		return m, fmt.Errorf("%s: a histogram needs explicit Buckets", m.Name)
	}
	if vec {
		ll, ok := call.Args[1].(*ast.CompositeLit)
		if !ok || !isStringSlice(ll.Type) {
			return m, fmt.Errorf("%s: the label names must be a []string literal", m.Name)
		}
		for _, e := range ll.Elts {
			s, err := stringLiteral(e)
			if err != nil {
				return m, fmt.Errorf("%s: label name: %w", m.Name, err)
			}
			m.Labels = append(m.Labels, s)
		}
	}
	return m, nil
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isStringSlice(e ast.Expr) bool {
	at, ok := e.(*ast.ArrayType)
	return ok && at.Len == nil && isIdent(at.Elt, "string")
}

func stringLiteral(e ast.Expr) (string, error) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", fmt.Errorf("must be a string literal")
	}
	return strconv.Unquote(bl.Value)
}

func numberLiteral(e ast.Expr) (float64, error) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || (bl.Kind != token.INT && bl.Kind != token.FLOAT) {
		return 0, fmt.Errorf("must be a number literal")
	}
	return strconv.ParseFloat(bl.Value, 64)
}

func floatList(e ast.Expr) ([]float64, error) {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("must be a []float64 literal")
	}
	at, ok := cl.Type.(*ast.ArrayType)
	if !ok || at.Len != nil || !isIdent(at.Elt, "float64") {
		return nil, fmt.Errorf("must be a []float64 literal")
	}
	var out []float64
	for _, el := range cl.Elts {
		v, err := numberLiteral(el)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// RenderMetrics renders docs/reference/metrics.md from the metrics found by ScanMetrics.
func RenderMetrics(metrics []Metric) ([]byte, error) {
	var b strings.Builder
	b.WriteString(frontMatter("Metrics", 70, "The Prometheus metrics of the server and the simulator, with type, labels and help text."))
	b.WriteString("This page is generated from the metric definitions in the Go sources by `make docs-gen`. Do not edit it by hand: `make docs-check` fails when it differs from the code.\n\n")

	b.WriteString("## Endpoints\n\n")
	b.WriteString("- `" + brand.ServerBinary + "` serves `/metrics` in the Prometheus text format. The address is `server.metricsListenAddr` (default `\":9090\"`). When it is empty, `/metrics` is served on `server.listenAddr`, which is what `" + brand.ServerBinary + " dev` does. `telemetry.metrics` switches the endpoint off. The endpoint has no authentication: restrict access to it with a network policy or a firewall. The Helm chart exposes it as the service port `metrics` (9090) and creates a `ServiceMonitor` when `serviceMonitor.enabled` is `true`.\n")
	b.WriteString("- `" + brand.SimBinary + "` serves its metrics on the address of the flag `--metrics-listen`; without the flag it serves none.\n")
	b.WriteString("- `" + brand.UpdaterBinary + "` and `" + brand.CLIBinary + "` serve no metrics.\n")
	b.WriteString("- The gauges `myriarch_agents` and `myriarch_config_sync` are computed by the leader replica only. Aggregate them across replicas with `max()`.\n\n")

	for _, bin := range []string{brand.ServerBinary, brand.SimBinary} {
		var rows [][]string
		for _, m := range metrics {
			if m.Binary != bin {
				continue
			}
			labels := make([]string, len(m.Labels))
			for i, l := range m.Labels {
				labels[i] = code(l)
			}
			help, err := inline(m.Help)
			if err != nil {
				return nil, fmt.Errorf("refdoc: %s: %w", m.Name, err)
			}
			switch m.Type {
			case "histogram":
				help += " Buckets: " + joinFloats(m.Buckets) + ". Series: `_bucket`, `_sum`, `_count`."
			case "summary":
				help += " Quantiles: " + joinFloats(m.Objectives) + ". Series: the quantiles, `_sum`, `_count`."
			}
			rows = append(rows, []string{code(m.Name), m.Type, strings.Join(labels, ", "), code(m.Package), help})
		}
		b.WriteString("## " + bin + "\n\n")
		if len(rows) == 0 {
			b.WriteString("No metrics.\n\n")
			continue
		}
		b.WriteString(table([]string{"Metric", "Type", "Labels", "Package", "Description"}, rows) + "\n")
	}

	b.WriteString("## Runtime metrics\n\n")
	b.WriteString("The registries of `" + brand.ServerBinary + "` and `" + brand.SimBinary + "` also contain the Go runtime collector (`go_*`) and the process collector (`process_*`) of the Prometheus Go client. They are not listed here.\n")
	return finish(b.String())
}

func joinFloats(v []float64) string {
	s := make([]string, len(v))
	for i, f := range v {
		s[i] = strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strings.Join(s, ", ")
}
