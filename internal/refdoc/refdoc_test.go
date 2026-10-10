// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/configload"
	"github.com/myriarch/myriarch/internal/docgen"
	"github.com/myriarch/myriarch/internal/netguard"
)

// updating reports whether the generated files are rewritten instead of compared (make docs-gen).
func updating() bool { return os.Getenv("REFDOC_UPDATE") == "1" }

// repoRoot is the root of the repository, seen from internal/refdoc.
const repoRoot = "../.."

func productionConfigSource() ConfigSource {
	return ConfigSource{
		Root:        reflect.TypeOf(config.Config{}),
		Defaults:    reflect.ValueOf(config.Default()),
		DevDefaults: reflect.ValueOf(config.DevDefaults()),
		SourceFiles: []string{"../config/config.go"},
		Sources:     "the configuration struct in `internal/config/config.go`, its defaults and its doc comments",
		EnvPaths:    EnvPathSet(configload.EnvOverrides()),
	}
}

// productionPages renders the three pages from the sources of the repository.
func productionPages(t *testing.T) map[string][]byte {
	t.Helper()
	pages := map[string][]byte{}
	var err error
	pages[ConfigFile], err = RenderConfiguration(productionConfigSource())
	require.NoError(t, err)

	vars, err := ScanTemplateVars(filepath.Join(repoRoot, "deploy"))
	require.NoError(t, err)
	pages[EnvironmentFile], err = RenderEnvironment(configload.EnvOverrides(), vars)
	require.NoError(t, err)

	ms, err := ScanMetrics(repoRoot)
	require.NoError(t, err)
	require.Greater(t, len(ms), 30, "the scan of the metric definitions looks broken")
	pages[MetricsFile], err = RenderMetrics(ms)
	require.NoError(t, err)
	return pages
}

// TestReferencePages keeps docs/reference/{configuration,environment-variables,metrics}.md equal to the generated pages.
// REFDOC_UPDATE=1 (make docs-gen) rewrites them.
func TestReferencePages(t *testing.T) {
	for name, page := range productionPages(t) {
		path := filepath.Join(repoRoot, "docs", "reference", name)
		if updating() {
			require.NoError(t, docgen.WriteFile(path, page))
			continue
		}
		if d := docgen.DiffFile(path, page); d != "" {
			t.Fatalf("docs/reference/%s is out of date (run make docs-gen and commit): %s", name, d)
		}
	}
}

// TestPagesAreWellFormed checks what every generated page must satisfy.
func TestPagesAreWellFormed(t *testing.T) {
	titles := map[string]string{ConfigFile: "Server configuration", EnvironmentFile: "Environment variables", MetricsFile: "Metrics"}
	for name, page := range productionPages(t) {
		s := string(page)
		assert.True(t, strings.HasPrefix(s, "---\ntitle: \""+titles[name]+"\"\nweight: "), name)
		assert.Regexp(t, `^---\ntitle: "[^"]+"\nweight: \d+\ndescription: "[^"]+"\n---\n\nThis page is generated from .+ by `+"`make docs-gen`"+`\. Do not edit it by hand: `+"`make docs-check`"+` fails when it differs from the code\.\n\n## `, s, name)
		assert.True(t, strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\n\n"), "%s ends with exactly one newline", name)
		assert.NotRegexp(t, `(?m)^# `, s, "%s has no H1", name)
		assert.NotRegexp(t, `\{\{\s*[<%]`, s, name)
		for i, line := range strings.Split(s, "\n") {
			assert.False(t, strings.HasSuffix(line, " "), "%s:%d ends with a space", name, i+1)
			if strings.HasPrefix(line, "|") {
				assert.NotContains(t, line, "||", "%s:%d has an empty cell border", name, i+1)
			}
		}
	}
}

// TestNoMetricsInTheToolsThatServeNone keeps the statement of the metrics page true: the updater and myrctl define no metrics.
func TestNoMetricsInTheToolsThatServeNone(t *testing.T) {
	ms, err := ScanMetrics(repoRoot)
	require.NoError(t, err)
	for _, m := range ms {
		assert.NotContains(t, []string{"updater", "myrctl"}, m.Package, m.Name)
		assert.Contains(t, []string{brand.ServerBinary, brand.SimBinary}, m.Binary, m.Name)
	}
}

// ------------------------------------------------------------ doc comments ---

// goIdentifierAllowlist holds names of configuration types or fields that are also ordinary words of the product.
var goIdentifierAllowlist = map[string]bool{"OpAMP": true, "GitOps": true}

var (
	wordRE       = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*`)
	dottedGoPath = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])[A-Z][A-Za-z0-9]*\.[A-Z][A-Za-z0-9]*`)
	innerCapital = regexp.MustCompile(`[a-z0-9][A-Z]|^[A-Z]{2,}[a-z]`)
)

// hasInnerCapital reports whether a Go name is written in mixed case (CertFile, UIConfig), as opposed to a word (Path) or an
// acronym (TLS).
func hasInnerCapital(name string) bool { return innerCapital.MatchString(name) }

// firstWord returns the identifier at the start of a comment and the rest.
func firstWord(text string) (string, string) {
	text = strings.TrimSpace(text)
	i := 0
	for i < len(text) && (text[i] == '_' || text[i] >= '0' && text[i] <= '9' || text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z') {
		i++
	}
	return text[:i], text[i:]
}

// TestConfigCommentsAreDocumentation enforces the rules for the doc comments of internal/config/config.go, which are the
// descriptions of docs/reference/configuration.md: every field with a YAML key has a doc comment (or relies on the doc comment
// of its struct type), the comment starts with the Go name, and the text names no Go identifier of the package (write the YAML
// key in backticks instead).
func TestConfigCommentsAreDocumentation(t *testing.T) {
	const file = "../config/config.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	require.NoError(t, err)

	// the Go identifiers of the package: type names and field names written in mixed case
	structDoc := map[string]bool{}
	identifiers := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			if hasInnerCapital(ts.Name.Name) {
				identifiers[ts.Name.Name] = true
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				doc := ts.Doc
				if doc == nil {
					doc = gd.Doc
				}
				structDoc[ts.Name.Name] = strings.TrimSpace(doc.Text()) != ""
				for _, fld := range st.Fields.List {
					for _, n := range fld.Names {
						if n.IsExported() && hasInnerCapital(n.Name) {
							identifiers[n.Name] = true
						}
					}
				}
			}
		}
	}
	for name := range goIdentifierAllowlist {
		delete(identifiers, name)
	}

	var problems []string
	report := func(pos token.Pos, format string, args ...any) {
		p := fset.Position(pos)
		problems = append(problems, fmt.Sprintf("%s:%d: %s", file, p.Line, fmt.Sprintf(format, args...)))
	}
	checkText := func(pos token.Pos, goName, text string) {
		first, rest := firstWord(text)
		if first != goName {
			report(pos, "the comment must start with %q, it starts with %q", goName, first)
		}
		for _, m := range dottedGoPath.FindAllString(rest, -1) {
			report(pos, "the comment names the Go path %s: write the YAML key in backticks", strings.TrimLeft(m, " (\"`"))
		}
		for _, w := range wordRE.FindAllString(rest, -1) {
			if identifiers[w] {
				report(pos, "the comment names the Go identifier %s: write the YAML key in backticks", w)
			}
		}
	}

	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			if doc := ts.Doc; doc != nil || gd.Doc != nil {
				if doc == nil {
					doc = gd.Doc
				}
				checkText(doc.Pos(), ts.Name.Name, doc.Text())
			} else {
				report(ts.Pos(), "the type %s has no doc comment", ts.Name.Name)
			}
			for _, fld := range st.Fields.List {
				if fld.Tag == nil || len(fld.Names) == 0 {
					continue
				}
				tag, _ := strconv.Unquote(fld.Tag.Value)
				key, _, _ := strings.Cut(reflect.StructTag(tag).Get("yaml"), ",")
				if key == "" || key == "-" {
					continue
				}
				for _, n := range fld.Names {
					if !n.IsExported() {
						continue
					}
					if fld.Doc != nil && strings.TrimSpace(fld.Doc.Text()) != "" {
						checkText(fld.Doc.Pos(), n.Name, fld.Doc.Text())
						continue
					}
					if id, ok := fld.Type.(*ast.Ident); ok && structDoc[id.Name] {
						continue // the doc comment of the struct type describes the field
					}
					report(n.Pos(), "the field %s.%s (%s) has no doc comment: describe what the setting does", ts.Name.Name, n.Name, key)
				}
			}
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("the doc comments of %s do not follow the rules (see docs/development.md, generated references):\n%s", file, strings.Join(problems, "\n"))
	}
}

// ------------------------------------------------------ environment reads ---

// goFiles returns the production .go files below root/cmd and root/internal (no tests, no test helpers, no testdata).
func goFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(p)
			if d.IsDir() {
				if d.Name() == "testdata" || strings.HasSuffix(rel, "internal/testutil") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		require.NoError(t, err)
	}
	return files
}

// TestEveryEnvironmentReadIsDocumented finds the environment reads of the production code and requires an entry in the
// registry of internal/refdoc/env.go for each: a string literal passed to os.Getenv, os.LookupEnv or a function called getenv,
// and every constant of internal/brand whose name ends in Env.
func TestEveryEnvironmentReadIsDocumented(t *testing.T) {
	documented := documentedEnvNames()
	fset := token.NewFileSet()
	found := map[string][]string{}
	for _, file := range goFiles(t) {
		f, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			isRead := false
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fun.X.(*ast.Ident); ok && x.Name == "os" && (fun.Sel.Name == "Getenv" || fun.Sel.Name == "LookupEnv") {
					isRead = true
				}
			case *ast.Ident:
				isRead = fun.Name == "getenv"
			}
			if !isRead {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				name, _ := strconv.Unquote(lit.Value)
				found[name] = append(found[name], fmt.Sprintf("%s:%d", filepath.ToSlash(file), fset.Position(call.Pos()).Line))
			}
			return true
		})
	}

	// the constants of internal/brand
	f, err := parser.ParseFile(fset, filepath.Join(repoRoot, "internal", "brand", "brand.go"), nil, 0)
	require.NoError(t, err)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if !strings.HasSuffix(n.Name, "Env") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				require.True(t, ok, "brand.%s must be a string literal", n.Name)
				name, _ := strconv.Unquote(lit.Value)
				found[name] = append(found[name], "internal/brand/brand.go ("+n.Name+")")
			}
		}
	}

	require.Contains(t, found, "PGTEST_CACHE_DIR", "the scan looks broken: devpg reads PGTEST_CACHE_DIR")
	for name, places := range found {
		assert.True(t, documented[name], "the environment variable %s (%s) is not documented: add it to envReads in internal/refdoc/env.go", name, strings.Join(places, ", "))
	}
	// the registry names only variables that exist: a stale entry is as wrong as a missing one. The scan sees the literal reads
	// and the brand constants; the proxy variables are read through netguard.ProxyEnvVars and the others by the standard library.
	known := map[string]bool{"NO_PROXY": true, "no_proxy": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true}
	for _, n := range netguard.ProxyEnvVars() {
		known[n] = true
	}
	for _, r := range envReads {
		assert.NotEmpty(t, r.Names)
		assert.NotEmpty(t, r.ReadBy)
		assert.NotEmpty(t, r.Effect)
		for _, n := range r.Names {
			_, scanned := found[n]
			assert.True(t, scanned || known[n], "envReads documents %s, which no production code reads: remove the entry from internal/refdoc/env.go", n)
		}
	}
}

// TestDeployTemplateVariablesHaveMeaning keeps the table of the template variables complete and free of stale entries.
func TestDeployTemplateVariablesHaveMeaning(t *testing.T) {
	vars, err := ScanTemplateVars(filepath.Join(repoRoot, "deploy"))
	require.NoError(t, err)
	used := map[string]bool{}
	for _, v := range vars {
		used[v.Name] = true
		assert.Contains(t, templateVarMeaning, v.Name, "the templates use ${env:%s} (%v): explain it in templateVarMeaning (internal/refdoc/env.go)", v.Name, v.Files)
		assert.NotEmpty(t, v.Files)
	}
	for name := range templateVarMeaning {
		assert.True(t, used[name], "templateVarMeaning explains %s, which no deployment template uses", name)
	}
	// a ${env:VAR} inside a comment is not a reference
	assert.False(t, used["VAR"])
	assert.Contains(t, used, brand.EnrollmentTokenEnv)
}

func TestScanTemplateVarsIgnoresCommentsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "supervisor", "a"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "updater"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "other"), 0o755))
	write := func(rel, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
	}
	write("supervisor/a/s.yaml", "# ${env:IN_COMMENT}\nkey: \"${env:ONE}\"\nother: ${env:TWO:-fallback} and ${env:ONE}\n")
	write("updater/u.yaml", "  # ${env:INDENTED_COMMENT}\ntoken: ${env:ONE}\n")
	write("other/o.yaml", "x: ${env:NOT_SCANNED}\n")
	got, err := ScanTemplateVars(dir)
	require.NoError(t, err)
	base := filepath.Base(dir)
	assert.Equal(t, []TemplateVar{
		{Name: "ONE", Files: []string{base + "/supervisor/a/s.yaml", base + "/updater/u.yaml"}},
		{Name: "TWO", Files: []string{base + "/supervisor/a/s.yaml"}},
	}, got)

	_, err = RenderEnvironment(nil, got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ONE")
}

// ------------------------------------------------------------------ golden ---

// checkGolden compares the page with testdata/golden/<name>/<name>.md, or rewrites it with REFDOC_UPDATE=1. Every golden
// test has a directory of its own because docgen.Write removes the other Markdown files of its directory.
func checkGolden(t *testing.T, name string, page []byte) {
	t.Helper()
	dir := filepath.Join("testdata", "golden", name)
	files := map[string][]byte{name + ".md": page}
	if updating() {
		require.NoError(t, docgen.Write(dir, files))
		return
	}
	assert.Empty(t, docgen.Diff(dir, files), "the golden file is out of date: run REFDOC_UPDATE=1 go test ./internal/refdoc and review the diff")
}

func TestGoldenConfiguration(t *testing.T) {
	src := ConfigSource{
		Root:        reflect.TypeOf(MiniConfig{}),
		Defaults:    reflect.ValueOf(miniDefaults()),
		DevDefaults: reflect.ValueOf(miniDevDefaults()),
		SourceFiles: []string{"miniconfig_test.go"},
		Sources:     "the mini configuration of the test",
		EnvPaths: map[string]bool{
			"server.listenAddr": true, "server.timeout": true, "server.grace": true, "server.maxBodyBytes": true,
			"server.cacheSize": true, "server.workers": true, "server.ratio": true, "server.hosts": true, "server.password": true,
			"server.mode": true, "server.quiet": true, "server.verbose": true, "server.tls.certFile": true,
			"server.backup.enabled": true, "dev.dataDir": true,
		},
	}
	page, err := RenderConfiguration(src)
	require.NoError(t, err)
	s := string(page)
	assert.NotContains(t, s, "MiniServer", "no Go identifier in the page")
	assert.NotContains(t, s, "debug", "yaml:\"-\" is skipped")
	checkGolden(t, "configuration", page)
}

func TestGoldenMetrics(t *testing.T) {
	ms, err := ScanMetrics("testdata/metrics/ok")
	require.NoError(t, err)
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.Name
	}
	assert.NotContains(t, names, "myriarch_sim_in_a_test", "test files are not scanned")
	assert.Equal(t, 1, strings.Count(strings.Join(names, " "), "myriarch_sample_requests_total"), "an identical definition counts once")
	page, err := RenderMetrics(ms)
	require.NoError(t, err)
	checkGolden(t, "metrics", page)
}

// ----------------------------------------------------------------- refusal ---

func TestScanMetricsRefusesWhatItCannotRead(t *testing.T) {
	cases := map[string][]string{
		"non-literal-name": {"bad.go:", "Name: must be a string literal"},
		"new-desc":         {"bad.go:", "prometheus.NewDesc is not supported by the metrics reference; extend internal/refdoc"},
		"missing-help":     {"bad.go:", "myriarch_bad", "non-empty Help"},
		"namespace":        {"bad.go:", "Namespace", "not supported"},
		"promauto":         {"bad.go:", "promauto is not supported by the metrics reference; extend internal/refdoc"},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ScanMetrics(filepath.Join("testdata", "metrics", "bad", name))
			require.Error(t, err)
			assert.Regexp(t, `internal/bad/bad\.go:\d+: `, err.Error())
			for _, w := range want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestScanMetricsChecksTheDefinitions(t *testing.T) {
	scan := func(t *testing.T, sources ...string) error {
		t.Helper()
		root := t.TempDir()
		for i, src := range sources {
			dir := filepath.Join(root, "internal", "p"+strconv.Itoa(i))
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "p.go"), []byte("package p\n\nimport \"github.com/prometheus/client_golang/prometheus\"\n\n"+src+"\n"), 0o644))
		}
		_, err := ScanMetrics(root)
		return err
	}
	ok := `var _ = prometheus.NewCounter(prometheus.CounterOpts{Name: "myriarch_x_total", Help: "X."})`

	require.NoError(t, scan(t, ok))
	assert.ErrorContains(t, scan(t, `var _ = prometheus.NewCounter(prometheus.CounterOpts{Name: "myriarch_x_total", Help: "No period"})`), "must end with a period")
	assert.ErrorContains(t, scan(t, `var _ = prometheus.NewGauge(prometheus.CounterOpts{Name: "myriarch_x", Help: "X."})`), "must be a prometheus.GaugeOpts literal")
	assert.ErrorContains(t, scan(t, `var _ = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "myriarch_x", Help: "X."}, labels)`), "[]string literal")
	assert.ErrorContains(t, scan(t, `var _ = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "myriarch_x", Help: "X."})`), "explicit Buckets")
	assert.ErrorContains(t, scan(t, `var _ = prometheus.NewGauge(prometheus.GaugeOpts{Name: "myriarch_x", Help: "X.", ConstLabels: nil})`), "ConstLabels")
	assert.ErrorContains(t, scan(t, `var _ = prometheus.MustNewConstMetric(nil, 0, 0)`), "MustNewConstMetric is not supported")
	assert.ErrorContains(t, scan(t, `var _ = prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "myriarch_x", Help: "X."}, nil)`), "NewGaugeFunc is not supported")

	// the same name with the same definition is fine, with another one it is an error
	require.NoError(t, scan(t, ok, ok))
	assert.ErrorContains(t, scan(t, ok, `var _ = prometheus.NewGauge(prometheus.GaugeOpts{Name: "myriarch_x_total", Help: "X."})`), "defined twice")
}

func TestInlineEscapesMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"line one\n\tline two", "line one line two"},
		{"a <base> and [b] and *c* and d_e | f", `a \<base\> and \[b\] and \*c\* and d\_e \| f`},
		{"code `<base>/<tag> | x_y` stays", "code `<base>/<tag> \\| x_y` stays"},
		{"see docs/operations/authentication.md.", `see [docs/operations/authentication.md](../operations/authentication.md).`},
		{"see docs/reference/gitops-schema.md", `see [docs/reference/gitops-schema.md](gitops-schema.md)`},
		{"no docs in `docs/x.md`", "no docs in `docs/x.md`"},
		{"{{< shortcode >}}", `{{\< shortcode \>}}`},
		{`e.g. "https://fleet.example.com". Required`, "e.g. `\"https://fleet.example.com\"`. Required"},
		{`default https://api.github.com; GHE: "https://ghe.example.com/api/v3")`, "default `https://api.github.com`; GHE: `\"https://ghe.example.com/api/v3\"`)"},
		{"see https://example.com/a_b. Next", "see `https://example.com/a_b`. Next"},
		{`value "https://x.test/y.`, "value \"`https://x.test/y`."},
		{"already `https://x.test` and https://y.test", "already `https://x.test` and `https://y.test`"},
	}
	for _, c := range cases {
		got, err := inline(c.in)
		require.NoError(t, err)
		assert.Equal(t, c.want, got, c.in)
	}
	_, err := inline("unpaired ` backtick")
	assert.Error(t, err)

	_, err = finish("text {{< raw >}} text")
	assert.Error(t, err, "a shortcode opener in the output is refused")
}

func TestFormatting(t *testing.T) {
	for in, want := range map[string]string{"0s": "0s", "30s": "30s", "1m0s": "1m", "1m30s": "1m30s", "2h0m0s": "2h", "720h0m0s": "720h", "1h10m0s": "1h10m", "1h0m5s": "1h0m5s", "100h0m0s": "100h"} {
		d, err := time.ParseDuration(in)
		require.NoError(t, err)
		assert.Equal(t, want, formatDuration(d), in)
	}
	assert.Equal(t, "`4194304` (4 MiB)", withUnit("maxMessageSize", 4<<20))
	assert.Equal(t, "`21474836480` (20 GiB)", withUnit("maxCacheBytes", 20<<30))
	assert.Equal(t, "`1536`", withUnit("maxBytes", 1536), "no unit unless the size is a multiple of 1024")
	assert.Equal(t, "`2048` (2 KiB)", withUnit("maxBytes", 2048))
	assert.Equal(t, "`1500`", withUnit("cacheSize", 1500))
	assert.Equal(t, "`10000`", withUnit("maxConnectionsPerReplica", 10000))
	assert.Equal(t, "`0`", withUnit("maxBytes", 0))
}
