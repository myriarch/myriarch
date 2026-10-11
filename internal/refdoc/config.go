// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/configload"
)

// ConfigFile is the name of the generated configuration page in docs/reference.
const ConfigFile = "configuration.md"

// ConfigSource is everything RenderConfiguration needs.
type ConfigSource struct {
	// Root is the type of the configuration structure; its fields are the sections of the page.
	Root reflect.Type
	// Defaults and DevDefaults are the values of Root that the server and "myriarchd dev" start from.
	Defaults, DevDefaults reflect.Value
	// SourceFiles are the Go files with the declarations of the structure; the doc comments of the types and fields are the
	// descriptions.
	SourceFiles []string
	// Sources is the text for the introduction of the page, for example "the configuration struct in
	// `internal/config/config.go` and its doc comments".
	Sources string
	// EnvPaths holds the dotted YAML paths that an environment variable can set (EnvPathSet); the other settings get a note.
	EnvPaths map[string]bool
}

// EnvPathSet returns the set of paths for ConfigSource.EnvPaths.
func EnvPathSet(overrides []configload.EnvOverride) map[string]bool {
	set := make(map[string]bool, len(overrides))
	for _, o := range overrides {
		set[o.Path] = true
	}
	return set
}

// ---------------------------------------------------------------- comments ---

// fieldDoc is the comment of one struct field.
type fieldDoc struct {
	doc  string // the comment block above the field
	line string // the comment on the line of the field
	pos  token.Position
}

// typeDoc is the comment of a struct type and of its fields.
type typeDoc struct {
	doc    string
	pos    token.Position
	fields map[string]fieldDoc
}

// comments holds the doc comments of the struct types of some Go files, by type name.
type comments struct {
	types map[string]*typeDoc
}

// loadComments parses the files and collects the doc comments of their struct types and fields.
func loadComments(files []string) (*comments, error) {
	c := &comments{types: map[string]*typeDoc{}}
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			return nil, err
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
				doc := ts.Doc
				if doc == nil {
					doc = gd.Doc
				}
				if _, dup := c.types[ts.Name.Name]; dup {
					return nil, fmt.Errorf("refdoc: the type %s is declared twice in %v", ts.Name.Name, files)
				}
				td := &typeDoc{doc: doc.Text(), pos: fset.Position(ts.Pos()), fields: map[string]fieldDoc{}}
				for _, fld := range st.Fields.List {
					for _, name := range fld.Names {
						td.fields[name.Name] = fieldDoc{doc: fld.Doc.Text(), line: fld.Comment.Text(), pos: fset.Position(name.Pos())}
					}
				}
				c.types[ts.Name.Name] = td
			}
		}
	}
	return c, nil
}

// leadingName replaces the Go name at the start of a comment by a code span with the YAML key; Go convention starts a doc
// comment with the name of the declaration.
func leadingName(text, goName, key string) string {
	text = strings.TrimSpace(text)
	if goName != "" && strings.HasPrefix(text, goName) {
		rest := text[len(goName):]
		if rest == "" || !isIdentRune(rune(rest[0])) {
			return "`" + key + "`" + rest
		}
	}
	return text
}

func isIdentRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// structElem returns the struct type that a field type stands for: the type itself, or the element of a list or map. It is nil
// for every other type.
func structElem(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Slice || t.Kind() == reflect.Map || t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t != timeType {
		return t
	}
	return nil
}

var (
	durationType = reflect.TypeOf(time.Duration(0))
	timeType     = reflect.TypeOf(time.Time{})
)

// ---------------------------------------------------------------- rendering ---

type configGen struct {
	src ConfigSource
	doc *comments
}

type configRow struct{ key, typ, def, desc string }

// RenderConfiguration renders docs/reference/configuration.md.
func RenderConfiguration(src ConfigSource) ([]byte, error) {
	if src.Root.Kind() != reflect.Struct || src.Defaults.Type() != src.Root || src.DevDefaults.Type() != src.Root {
		return nil, fmt.Errorf("refdoc: Root, Defaults and DevDefaults must be of the same struct type")
	}
	doc, err := loadComments(src.SourceFiles)
	if err != nil {
		return nil, err
	}
	g := &configGen{src: src, doc: doc}

	var b strings.Builder
	b.WriteString(frontMatter("Server configuration", 50, "Every key of the myriarchd configuration file, with its type, default and effect."))
	b.WriteString("This page is generated from " + src.Sources + " by `make docs-gen`. Do not edit it by hand: `make docs-check` fails when it differs from the code.\n\n")
	b.WriteString(loadingText)

	for i := 0; i < src.Root.NumField(); i++ {
		sf := src.Root.Field(i)
		key := configload.YAMLKey(sf)
		if key == "" || !sf.IsExported() {
			continue
		}
		if sf.Type.Kind() != reflect.Struct {
			return nil, fmt.Errorf("refdoc: %s.%s: a top-level key must be a struct", src.Root.Name(), sf.Name)
		}
		td := g.doc.types[sf.Type.Name()]
		if td == nil || strings.TrimSpace(td.doc) == "" {
			return nil, fmt.Errorf("refdoc: the type %s has no doc comment", sf.Type.Name())
		}
		intro, err := inline(leadingName(td.doc, sf.Type.Name(), key))
		if err != nil {
			return nil, fmt.Errorf("refdoc: %s: %w", sf.Type.Name(), err)
		}
		if key == "dev" {
			intro += " Used only by `" + brand.ServerBinary + " dev`."
		}
		var rows []configRow
		if err := g.walk(sf.Type, src.Defaults.Field(i), src.DevDefaults.Field(i), key, false, &rows); err != nil {
			return nil, err
		}
		b.WriteString("## " + key + "\n\n" + intro + "\n\n")
		var cells [][]string
		for _, r := range rows {
			cells = append(cells, []string{r.key, r.typ, r.def, r.desc})
		}
		b.WriteString(table([]string{"Key", "Type", "Default", "Description"}, cells) + "\n")
	}
	return finish(b.String())
}

// walk adds the rows of the fields of struct type t (depth first, in declaration order). prefix is the dotted path of the
// struct. In element mode (the fields of the elements of a list or map) there are no defaults and no environment notes.
func (g *configGen) walk(t reflect.Type, def, dev reflect.Value, prefix string, element bool, rows *[]configRow) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		yamlKey := configload.YAMLKey(sf)
		if !sf.IsExported() || yamlKey == "" {
			continue
		}
		path := prefix + "." + yamlKey
		if element {
			path = prefix + yamlKey
		}
		typ, err := typeCell(sf.Type)
		if err != nil {
			return fmt.Errorf("refdoc: %s: %w", path, err)
		}
		desc, err := g.describe(t.Name(), sf, yamlKey, path, element)
		if err != nil {
			return err
		}
		fdef, fdev := def.Field(i), dev.Field(i)

		switch {
		case sf.Type.Kind() == reflect.Struct && sf.Type != timeType:
			*rows = append(*rows, configRow{code(path), typ, "-", desc})
			if err := g.walk(sf.Type, fdef, fdev, path, element, rows); err != nil {
				return err
			}
		case structElem(sf.Type) != nil:
			*rows = append(*rows, configRow{code(path), typ, "none", desc})
			elemPrefix := path + "[]."
			if sf.Type.Kind() == reflect.Map {
				elemPrefix = path + ".<name>."
			}
			elem := structElem(sf.Type)
			if err := g.walk(elem, reflect.Zero(elem), reflect.Zero(elem), elemPrefix, true, rows); err != nil {
				return err
			}
		default:
			cellDef := "none"
			if !element {
				d, err := formatValue(yamlKey, fdef)
				if err != nil {
					return fmt.Errorf("refdoc: %s: %w", path, err)
				}
				if !reflect.DeepEqual(fdef.Interface(), fdev.Interface()) {
					dd, err := formatValue(yamlKey, fdev)
					if err != nil {
						return fmt.Errorf("refdoc: %s (dev): %w", path, err)
					}
					d += " (dev: " + dd + ")"
				}
				cellDef = d
			}
			*rows = append(*rows, configRow{code(path), typ, cellDef, desc})
		}
	}
	return nil
}

// describe returns the description cell of a field: its doc comment (else the comment on its line, else the doc comment of its
// struct type) with the leading Go name replaced by the YAML key, and the notes about secrets and the environment.
func (g *configGen) describe(owner string, sf reflect.StructField, yamlKey, path string, element bool) (string, error) {
	td := g.doc.types[owner]
	if td == nil {
		return "", fmt.Errorf("refdoc: no declaration of the type %s found in %v", owner, g.src.SourceFiles)
	}
	fd := td.fields[sf.Name]
	text, name := fd.doc, sf.Name
	if strings.TrimSpace(text) == "" {
		text = fd.line
	}
	if strings.TrimSpace(text) == "" {
		if et := structElem(sf.Type); et != nil {
			if etd := g.doc.types[et.Name()]; etd != nil {
				text, name = etd.doc, et.Name()
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("refdoc: %s has no description: add a doc comment to the field %s.%s", path, owner, sf.Name)
	}
	out, err := inline(leadingName(text, name, yamlKey))
	if err != nil {
		return "", fmt.Errorf("refdoc: %s: %w", path, err)
	}
	if sf.Type.Kind() == reflect.String && configload.IsSecretKey(yamlKey) {
		out += " Secret: `" + brand.ServerBinary + " config print` shows `<redacted>`."
	}
	isStruct := sf.Type.Kind() == reflect.Struct && sf.Type != timeType
	if !element && !isStruct && !g.src.EnvPaths[path] {
		out += " Not settable through an environment variable."
	}
	return out, nil
}

// typeCell names the type of a setting.
func typeCell(t reflect.Type) (string, error) {
	switch {
	case t == durationType:
		return "duration", nil
	case t == timeType:
		return "", fmt.Errorf("time.Time is not supported")
	}
	switch t.Kind() {
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer", nil
	case reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Struct:
		return "object", nil
	case reflect.Slice:
		switch {
		case t.Elem().Kind() == reflect.String:
			return "list of strings", nil
		case t.Elem().Kind() == reflect.Struct:
			return "list of objects", nil
		}
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			switch {
			case t.Elem().Kind() == reflect.String:
				return "map of strings", nil
			case t.Elem().Kind() == reflect.Slice && t.Elem().Elem().Kind() == reflect.String:
				return "map of string lists", nil
			case t.Elem().Kind() == reflect.Struct:
				return "map of objects", nil
			}
		}
	}
	return "", fmt.Errorf("the type %s is not supported by the configuration reference", t)
}

// formatValue renders a default value for the Default column: "empty" for an empty string, list or map, a code span otherwise.
func formatValue(key string, v reflect.Value) (string, error) {
	if v.Type() == durationType {
		return code(formatDuration(time.Duration(v.Int()))), nil
	}
	switch v.Kind() {
	case reflect.String:
		if v.Len() == 0 {
			return "empty", nil
		}
		return code(strconv.Quote(v.String())), nil
	case reflect.Bool:
		return code(strconv.FormatBool(v.Bool())), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return withUnit(key, v.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return withUnit(key, int64(v.Uint())), nil
	case reflect.Float32, reflect.Float64:
		return code(strconv.FormatFloat(v.Float(), 'f', -1, 64)), nil
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			break
		}
		if v.Len() == 0 {
			return "empty", nil
		}
		items := make([]string, v.Len())
		for i := range items {
			items[i] = strconv.Quote(v.Index(i).String())
		}
		return code("[" + strings.Join(items, ", ") + "]"), nil
	case reflect.Map:
		if v.Len() == 0 {
			return "empty", nil
		}
	}
	return "", fmt.Errorf("no default value format for %s", v.Type())
}

// withUnit renders an integer; sizes (keys ending in Bytes or Size) that are a multiple of 1024 get the binary unit.
func withUnit(key string, n int64) string {
	s := code(strconv.FormatInt(n, 10))
	if n <= 0 || n%1024 != 0 || !(strings.HasSuffix(key, "Bytes") || strings.HasSuffix(key, "Size")) {
		return s
	}
	for _, u := range []struct {
		name string
		size int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if n%u.size == 0 {
			return s + " (" + strconv.FormatInt(n/u.size, 10) + " " + u.name + ")"
		}
	}
	return s
}

// formatDuration renders a duration in Go syntax without trailing zero units: 720h, 2h, 1m30s, 0s.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// loadingText is the fixed introduction: how the loader treats the file. Every statement is verified against
// internal/configload.
const loadingText = "## How the configuration is loaded\n\n" +
	"`" + brand.ServerBinary + " serve`, `" + brand.ServerBinary + " dev`, `" + brand.ServerBinary + " migrate`, `" + brand.ServerBinary + " config validate` and `" + brand.ServerBinary + " config print` load the configuration the same way:\n\n" +
	"- The configuration file is YAML. An unknown key is an error.\n" +
	"- The loader starts from the defaults in the **Default** column. `" + brand.ServerBinary + " dev` starts from other defaults, shown as \"(dev: ...)\" where they differ.\n" +
	"- In string values, `${VAR}` and `${VAR:-default}` are replaced after the file is decoded, with the value of the environment variable of the process. An unset or empty variable gives the default, or the empty string when there is none.\n" +
	"- Then the environment variables `MYRIARCH_<PATH>` override single settings, see [environment variables](environment-variables.md#configuration-overrides). They win over the file.\n" +
	"- `database.dsnFile` and `notifications.channels[].urlFile` are always read at startup, `auth.oidc.clientSecretFile` only while `auth.oidc.enabled` is `true` and `gitops.webhookSecretFile` only while `gitops.enabled` is `true`. The content of the file (without surrounding white space) wins over the inline value next to it. A file that cannot be read or is empty is an error. The other `...File` settings name files (certificates, keys, tokens, CA bundles) that the components read themselves.\n" +
	"- Durations use Go syntax such as `30s`, `5m` or `720h`. A plain integer is rejected, and `d` is not a unit: write `720h` for 30 days.\n" +
	"- [`" + brand.ServerBinary + " config print`](cli/myriarchd_config_print.md) shows the effective configuration with secrets redacted, [`" + brand.ServerBinary + " config validate`](cli/myriarchd_config_validate.md) checks a file.\n" +
	"- [`" + brand.ServerBinary + " dev`](cli/myriarchd_dev.md) sets some keys from its flags: `--listen` sets `server.listenAddr` (and `server.publicUrl`, unless the file or the environment sets it), `--gitops-dir` sets `gitops.repoUrl` to `dir:<directory>`, `--data-dir` sets `dev.dataDir` (and with it `artifacts.dir` to `<data-dir>/artifacts` and `gitops.cloneDir` to `<data-dir>/gitops`), `--releases-fixtures` sets `releases.fixtureDir`, `--replica-id` sets `ha.replicaId` and `--dsn` sets `database.dsn`.\n" +
	"\n" +
	"How to read the tables: **Type** `duration` is a string in Go syntax. **Default** `empty` is the empty string, an empty list or an empty map, `-` marks a group of keys (the defaults are on its keys), `none` means that a list or map has no entries and its fields have no default of their own, and a size in bytes shows its binary unit in parentheses. A key such as `notifications.channels[].name` is a field of every entry of the list, and `<name>` in `executors.secrets.<name>.env` stands for the key of the map entry.\n\n"
