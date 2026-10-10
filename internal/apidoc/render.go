// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	// IndexFile is the name of the generated section index.
	IndexFile = "_index.md"
	// DataTypesFile is the name of the page with the component schemas.
	DataTypesFile = "data-types.md"

	indexWeight = 30
	// specPath is the contract that the pages are generated from (named in the generated texts).
	specPath = "api/openapi.yaml"
)

// Options configure Render.
type Options struct {
	// RoleFor returns the lowest role that holds the permission of an operation (x-permission). The permissions "public"
	// (no login) and "custom" (the operation checks its own credential) are handled by the generator and never asked for. For
	// any other permission ok=false is an error: the contract names a permission that the server does not know.
	RoleFor func(permission string) (role string, ok bool)
}

// roleOrder is the order of the roles in the permission table: lowest first. Unknown roles follow in alphabetical order.
var roleOrder = []string{"viewer", "operator", "admin"}

// titleOverrides are the page titles of tags whose capitalized name is not what the product calls them.
var titleOverrides = map[string]string{
	"auth":   "Authentication",
	"gitops": "GitOps",
	"live":   "Live updates",
}

var proseHeading = regexp.MustCompile(`^\*\*([^*\n]+)\*\*(.*)$`)

type tagPage struct {
	Tag   Tag
	File  string
	Title string
	Ops   []*Operation
}

type renderer struct {
	spec  *Spec
	opts  Options
	tags  []*tagPage
	byTag map[string]*tagPage
	// opPage maps an operation to its page for links
	problemStatuses map[string][]string // shared response name -> HTTP statuses
}

// Render returns the generated pages by file name: the section index _index.md, one page per tag (file name: the tag in
// kebab case) and data-types.md. The result is deterministic.
func Render(spec *Spec, opts Options) (map[string][]byte, error) {
	if opts.RoleFor == nil {
		return nil, fmt.Errorf("apidoc: Options.RoleFor is required")
	}
	r := &renderer{spec: spec, opts: opts, byTag: map[string]*tagPage{}, problemStatuses: map[string][]string{}}
	files := map[string][]byte{}
	if err := r.collect(); err != nil {
		return nil, err
	}
	for i, tp := range r.tags {
		page, err := r.renderTag(tp, 10*(i+1))
		if err != nil {
			return nil, fmt.Errorf("apidoc: tag %s: %w", tp.Tag.Name, err)
		}
		files[tp.File] = page
	}
	dt, err := r.renderDataTypes(10 * (len(r.tags) + 1))
	if err != nil {
		return nil, err
	}
	files[DataTypesFile] = dt
	idx, err := r.renderIndex()
	if err != nil {
		return nil, err
	}
	files[IndexFile] = idx
	for name, b := range files {
		if bytes.Contains(b, []byte("{{<")) || bytes.Contains(b, []byte("{{%")) {
			return nil, fmt.Errorf("apidoc: %s contains a Hugo shortcode delimiter", name)
		}
	}
	return files, nil
}

// collect assigns the operations to the pages of their tags and records the statuses of the shared responses.
func (r *renderer) collect() error {
	taken := map[string]string{IndexFile: "the index", DataTypesFile: "the data types"}
	for _, t := range r.spec.Tags {
		tp := &tagPage{Tag: t, File: kebab(t.Name) + ".md", Title: tagTitle(t.Name)}
		if prev, dup := taken[tp.File]; dup {
			return fmt.Errorf("apidoc: the tag %q produces the file %s that %s uses", t.Name, tp.File, prev)
		}
		taken[tp.File] = "the tag " + t.Name
		r.tags = append(r.tags, tp)
		r.byTag[t.Name] = tp
	}
	ids := map[string]bool{}
	for _, op := range r.spec.Operations {
		if ids[op.OperationID] {
			return fmt.Errorf("apidoc: duplicate operationId %q", op.OperationID)
		}
		ids[op.OperationID] = true
		tp := r.byTag[op.Tags[0]]
		if tp == nil {
			return fmt.Errorf("apidoc: %s %s uses the undeclared tag %q", op.Method, op.Path, op.Tags[0])
		}
		tp.Ops = append(tp.Ops, op)
		for _, code := range op.Responses.Keys {
			resp := op.Responses.Values[code]
			if resp.Ref != "" {
				name, _ := refName(resp.Ref, "responses")
				if !contains(r.problemStatuses[name], code) {
					r.problemStatuses[name] = append(r.problemStatuses[name], code)
				}
			}
		}
	}
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- helpers --------------------------------------------------------------------------------------------------------------

// kebab turns a camelCase tag into a file name stem (savedFilters -> saved-filters).
func kebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tagTitle turns a camelCase tag into a page title (rolloutPolicies -> Rollout policies).
func tagTitle(s string) string {
	if t, ok := titleOverrides[s]; ok {
		return t
	}
	var words []string
	cur := strings.Builder{}
	for _, r := range s {
		if unicode.IsUpper(r) && cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
		cur.WriteRune(unicode.ToLower(r))
	}
	words = append(words, cur.String())
	t := strings.Join(words, " ")
	return strings.ToUpper(t[:1]) + t[1:]
}

// slug returns the GitHub anchor of a heading text.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimRight(b.String(), "\n")
}

func frontMatter(b *strings.Builder, title string, weight int, description string) {
	fmt.Fprintf(b, "---\ntitle: %s\nweight: %d\ndescription: %s\n---\n\n", quote(title), weight, quote(description))
}

func sentence(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// escapeAngle replaces "<" by "&lt;" outside of inline code spans: raw angle brackets would be taken for HTML.
func escapeAngle(s string) string {
	var b strings.Builder
	code := false
	for _, r := range s {
		switch {
		case r == '`':
			code = !code
			b.WriteRune(r)
		case r == '<' && !code:
			b.WriteString("&lt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cell makes s fit into a Markdown table cell.
func cell(s string) string {
	return strings.ReplaceAll(escapeAngle(strings.Join(strings.Fields(s), " ")), "|", `\|`)
}

// prose returns a description as Markdown for the page: trailing space is removed, angle brackets outside code are escaped
// (not inside fenced blocks).
func prose(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	fence := false
	for i, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
		} else if !fence {
			l = escapeAngle(l)
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

func code(s string) string {
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

// table writes a Markdown table; the cells are used as they are.
func table(b *strings.Builder, header []string, rows [][]string) {
	b.WriteString("| " + strings.Join(header, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(header)) + "\n")
	for _, row := range rows {
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	b.WriteString("\n")
}

// literalText formats a default, example or enum value for a code span.
func literalText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return "null"
	}
	return literal(normalize(v)).lit
}

// ---- types ----------------------------------------------------------------------------------------------------------------

// typeText describes the type of a schema; base is the prefix of links to data types ("" on the data types page itself).
func (r *renderer) typeText(sc *Schema, base string) string {
	switch {
	case sc == nil:
		return ""
	case sc.Ref != "":
		name, _ := refName(sc.Ref, "schemas")
		return fmt.Sprintf("[%s](%s#%s)", name, base, slug(name))
	case sc.Type == "array":
		if sc.Items == nil {
			return "`array`"
		}
		return "array of " + r.typeText(sc.Items, base)
	case sc.Type == "object" || (sc.Type == "" && sc.AdditionalProperties != nil):
		ap := sc.AdditionalProperties
		switch {
		case len(sc.Properties.Keys) > 0 || ap == nil || !ap.Allowed:
			return "`object`"
		case ap.Schema != nil:
			return "map of `string` to " + r.typeText(ap.Schema, base)
		default:
			return "map of `string` to any"
		}
	case sc.Type == "":
		return "any"
	}
	return "`" + sc.Type + "`"
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// facts lists the format, constraints, default and example of a schema as short sentences.
func facts(sc *Schema) []string {
	if sc == nil {
		return nil
	}
	var out []string
	if sc.Format != "" {
		out = append(out, "Format "+code(sc.Format)+".")
	}
	if len(sc.Enum) > 0 && sc.Ref == "" {
		vals := make([]string, len(sc.Enum))
		for i, v := range sc.Enum {
			vals[i] = code(literalText(v))
		}
		out = append(out, "One of "+strings.Join(vals, ", ")+".")
	}
	switch {
	case sc.Minimum != nil && sc.Maximum != nil:
		out = append(out, fmt.Sprintf("Range %s to %s.", fmtNum(*sc.Minimum), fmtNum(*sc.Maximum)))
	case sc.Minimum != nil:
		out = append(out, "Minimum "+fmtNum(*sc.Minimum)+".")
	case sc.Maximum != nil:
		out = append(out, "Maximum "+fmtNum(*sc.Maximum)+".")
	}
	switch {
	case sc.MinLength != nil && sc.MaxLength != nil:
		out = append(out, fmt.Sprintf("%d to %s.", *sc.MinLength, plural(*sc.MaxLength, "character", "characters")))
	case sc.MinLength != nil:
		out = append(out, "At least "+plural(*sc.MinLength, "character", "characters")+".")
	case sc.MaxLength != nil:
		out = append(out, "At most "+plural(*sc.MaxLength, "character", "characters")+".")
	}
	switch {
	case sc.MinItems != nil && sc.MaxItems != nil:
		out = append(out, fmt.Sprintf("%d to %s.", *sc.MinItems, plural(*sc.MaxItems, "item", "items")))
	case sc.MinItems != nil:
		out = append(out, "At least "+plural(*sc.MinItems, "item", "items")+".")
	case sc.MaxItems != nil:
		out = append(out, "At most "+plural(*sc.MaxItems, "item", "items")+".")
	}
	if sc.UniqueItems {
		out = append(out, "The items are unique.")
	}
	if sc.Pattern != "" {
		out = append(out, "Pattern "+code(sc.Pattern)+".")
	}
	if sc.Default != nil {
		out = append(out, "Default "+code(literalText(sc.Default))+".")
	}
	if sc.Example != nil {
		out = append(out, "Example "+code(literalText(sc.Example))+".")
	}
	if sc.Deprecated {
		out = append(out, "Deprecated.")
	}
	if sc.ReadOnly {
		out = append(out, "Read-only.")
	}
	if sc.WriteOnly {
		out = append(out, "Write-only.")
	}
	if sc.Items != nil {
		out = append(out, nestedFacts("Items", sc.Items)...)
	}
	if ap := sc.AdditionalProperties; ap != nil && ap.Schema != nil {
		out = append(out, nestedFacts("Values", ap.Schema)...)
	}
	return out
}

// nestedFacts describes the element schema of an array or the value schema of a map as one sentence group that starts with the
// label: the description followed by the facts of the schema (its own element or value schemas included). The type of a nested
// schema is part of the type text of the field already.
func nestedFacts(label string, it *Schema) []string {
	var parts []string
	described := false
	if d := strings.TrimSpace(it.Description); d != "" {
		parts = append(parts, escapeAngle(sentence(d)))
		described = true
	}
	parts = append(parts, facts(it)...)
	if len(parts) == 0 {
		return nil
	}
	if !described { // the first sentence is a fact ("One of ..."): it continues the label
		parts[0] = strings.ToLower(parts[0][:1]) + parts[0][1:]
	}
	return []string{label + ": " + strings.Join(parts, " ")}
}

// mediaType describes the schema of a request or response body: the type text, followed by the facts of the schema (a format such as
// binary, for example).
func (r *renderer) mediaType(sc *Schema) string {
	t := r.typeText(sc, DataTypesFile)
	if f := facts(sc); len(f) > 0 {
		t += ". " + strings.Join(f, " ")
	}
	return t
}

// describe returns the description of a schema as one table cell: the description followed by the facts.
func describe(sc *Schema, extra ...string) string {
	var parts []string
	if sc != nil && strings.TrimSpace(sc.Description) != "" {
		parts = append(parts, sentence(sc.Description))
	}
	parts = append(parts, extra...)
	parts = append(parts, facts(sc)...)
	return cell(strings.Join(parts, " "))
}

// ---- operations -----------------------------------------------------------------------------------------------------------

// basePath is the path that the paths of the operations are relative to (the only server of the contract, see Validate).
func (r *renderer) basePath() string { return r.spec.Servers[0].URL }

func (r *renderer) roleOf(perm string) (string, error) {
	role, ok := r.opts.RoleFor(perm)
	if !ok || role == "" {
		return "", fmt.Errorf("unknown permission %q", perm)
	}
	return role, nil
}

func (r *renderer) renderTag(tp *tagPage, weight int) ([]byte, error) {
	var b strings.Builder
	frontMatter(&b, tp.Title, weight, sentence(tp.Tag.Description))
	base := "All paths are relative to the base path " + code(r.basePath())
	if d := strings.TrimSpace(r.spec.Servers[0].Description); d != "" {
		base += " (" + escapeAngle(strings.Join(strings.Fields(d), " ")) + ")"
	}
	b.WriteString(base + ". Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](" + DataTypesFile + ").\n\n")
	// summary table
	rows := make([][]string, 0, len(tp.Ops))
	headings := map[string]string{}
	for _, op := range tp.Ops {
		s := slug(op.Summary)
		if prev, dup := headings[s]; dup {
			return nil, fmt.Errorf("the operations %s and %s have the same heading anchor %q", prev, op.OperationID, s)
		}
		if reserved.MatchString(s) {
			return nil, fmt.Errorf("the summary of %s collides with a generated heading (%q)", op.OperationID, op.Summary)
		}
		headings[s] = op.OperationID
		var perm string
		if op.Permission == "public" || op.Permission == "custom" {
			perm = code(op.Permission)
		} else {
			role, err := r.roleOf(op.Permission)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", op.OperationID, err)
			}
			perm = code(op.Permission) + " (" + role + ")"
		}
		rows = append(rows, []string{fmt.Sprintf("[%s](#%s)", cell(op.Summary), s), code(op.Method + " " + op.Path), perm})
	}
	table(&b, []string{"Operation", "Request", "Permission"}, rows)
	for _, op := range tp.Ops {
		if err := r.renderOperation(&b, op); err != nil {
			return nil, fmt.Errorf("%s: %w", op.OperationID, err)
		}
	}
	return finish(&b), nil
}

var reserved = regexp.MustCompile(`^(parameters|request-body|responses)(-[0-9]+)?$`)

func finish(b *strings.Builder) []byte {
	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}

// authText describes how an operation is authenticated when that differs from the default of the contract (the global security
// requirement, which the index describes). It is empty for an operation that uses the default.
func (r *renderer) authText(op *Operation) string {
	switch {
	case op.Permission == "public":
		return "No login is needed."
	case op.Permission == "custom":
		return "Neither a session nor an API token is checked: the operation checks its own credential (see below)."
	case op.Security != nil && len(*op.Security) == 0:
		return "No authentication."
	}
	if op.Security == nil {
		return ""
	}
	var names []string
	for _, req := range *op.Security {
		for _, k := range sortedKeys(req) {
			names = append(names, code(k))
		}
	}
	return "Authentication: " + strings.Join(names, " or ") + "."
}

func (r *renderer) renderOperation(b *strings.Builder, op *Operation) error {
	fmt.Fprintf(b, "## %s\n\n", escapeAngle(op.Summary))
	fmt.Fprintf(b, "`%s %s%s`\n\n", op.Method, r.basePath(), op.Path)
	var perm string
	switch op.Permission {
	case "public", "custom":
		perm = "Permission " + code(op.Permission)
	default:
		role, err := r.roleOf(op.Permission)
		if err != nil {
			return err
		}
		perm = "Permission " + code(op.Permission) + " (lowest role: " + role + ")"
	}
	meta := "Operation " + code(op.OperationID) + ". " + perm + "."
	if a := r.authText(op); a != "" {
		meta += " " + a
	}
	b.WriteString(meta + "\n\n")
	if d := prose(op.Description); d != "" {
		b.WriteString(d + "\n\n")
	}
	// parameters
	if len(op.Parameters) > 0 {
		b.WriteString("### Parameters\n\n")
		rows := make([][]string, 0, len(op.Parameters))
		for _, p := range op.Parameters {
			rp, err := r.spec.parameter(p)
			if err != nil {
				return err
			}
			req := "no"
			if rp.Required {
				req = "yes"
			}
			var extra []string
			if rp.Schema != nil && rp.Schema.Type == "array" && rp.In == "query" && (rp.Explode == nil || *rp.Explode) {
				extra = append(extra, "Repeat the parameter for several values.")
			}
			rows = append(rows, []string{code(rp.Name), rp.In, r.typeText(rp.Schema, DataTypesFile), req, cell(sentence(rp.Description) + " " + strings.Join(append(extra, facts(rp.Schema)...), " "))})
		}
		table(b, []string{"Name", "In", "Type", "Required", "Description"}, rows)
	}
	// request body
	if rb := op.RequestBody; rb != nil {
		b.WriteString("### Request body\n\n")
		req := "optional"
		if rb.Required {
			req = "required"
		}
		if d := strings.TrimSpace(rb.Description); d != "" {
			b.WriteString(prose(d) + "\n\n")
		}
		for _, mt := range rb.Content.Keys {
			m := rb.Content.Values[mt]
			fmt.Fprintf(b, "Content type %s, %s. Type: %s\n\n", code(mt), req, sentence(r.mediaType(m.Schema)))
			if mt == "application/json" || strings.HasSuffix(mt, "+json") {
				ex, err := r.spec.ExampleJSON(m.Schema, true)
				if err != nil {
					return err
				}
				b.WriteString("```json\n" + ex + "\n```\n\n")
			}
		}
	}
	// responses
	b.WriteString("### Responses\n\n")
	codes := append([]string{}, op.Responses.Keys...)
	sort.SliceStable(codes, func(i, j int) bool { return codes[i] < codes[j] })
	rows := make([][]string, 0, len(codes))
	for _, c := range codes {
		_, resp, err := r.spec.response(op.Responses.Values[c])
		if err != nil {
			return err
		}
		var types, ctypes []string
		for _, mt := range resp.Content.Keys {
			ctypes = append(ctypes, code(mt))
			types = append(types, r.mediaType(resp.Content.Values[mt].Schema))
		}
		none := func(s []string) string {
			if len(s) == 0 {
				return "-"
			}
			return strings.Join(s, ", ")
		}
		rows = append(rows, []string{c, cell(resp.Description), none(ctypes), none(types)})
	}
	table(b, []string{"Status", "Description", "Content type", "Type"}, rows)
	return nil
}

// ---- data types -----------------------------------------------------------------------------------------------------------

func (r *renderer) renderDataTypes(weight int) ([]byte, error) {
	var b strings.Builder
	frontMatter(&b, "Data types", weight, "Every schema of the REST API with its fields, types, constraints and values.")
	b.WriteString("The named types of the [REST API](_index.md), in alphabetical order. Field names are in the JSON spelling (camelCase). A field marked required is always present; required maps and arrays are never `null`.\n\n")
	names := append([]string{}, r.spec.Schemas.Keys...)
	sort.Slice(names, func(i, j int) bool {
		a, c := strings.ToLower(names[i]), strings.ToLower(names[j])
		if a != c {
			return a < c
		}
		return names[i] < names[j]
	})
	seen := map[string]string{}
	for _, name := range names {
		s := slug(name)
		if prev, dup := seen[s]; dup {
			return nil, fmt.Errorf("apidoc: the schemas %s and %s have the same heading anchor %q", prev, name, s)
		}
		seen[s] = name
		sc := r.spec.Schemas.Values[name]
		fmt.Fprintf(&b, "## %s\n\n", name)
		if d := strings.TrimSpace(sc.Description); d != "" {
			b.WriteString(prose(d) + "\n\n")
		}
		r.renderSchemaBody(&b, sc)
	}
	return finish(&b), nil
}

func (r *renderer) renderSchemaBody(b *strings.Builder, sc *Schema) {
	switch {
	case len(sc.Properties.Keys) > 0:
		req := map[string]bool{}
		for _, n := range sc.Required {
			req[n] = true
		}
		rows := make([][]string, 0, len(sc.Properties.Keys))
		for _, k := range sc.Properties.Keys {
			p := sc.Properties.Values[k]
			field := code(k)
			if req[k] {
				field += " (required)"
			}
			rows = append(rows, []string{field, r.typeText(p, ""), describe(p)})
		}
		table(b, []string{"Field", "Type", "Description"}, rows)
		if ap := sc.AdditionalProperties; ap != nil && ap.Allowed {
			further := &Schema{Type: "object", AdditionalProperties: ap}
			b.WriteString("Further fields are accepted: " + r.typeText(further, "") + ".")
			if f := facts(further); len(f) > 0 {
				b.WriteString(" " + strings.Join(f, " "))
			}
			b.WriteString("\n\n")
		}
		own := *sc
		own.AdditionalProperties = nil // described above
		if f := facts(&own); len(f) > 0 {
			b.WriteString(strings.Join(f, " ") + "\n\n")
		}
	case len(sc.Enum) > 0:
		fmt.Fprintf(b, "Type: %s. Values:\n\n", r.typeText(sc, ""))
		for _, v := range sc.Enum {
			fmt.Fprintf(b, "- %s\n", code(literalText(v)))
		}
		b.WriteString("\n")
		rest := *sc
		rest.Enum = nil
		if f := facts(&rest); len(f) > 0 {
			b.WriteString(strings.Join(f, " ") + "\n\n")
		}
	default:
		line := "Type: " + r.typeText(sc, "") + "."
		rest := facts(sc)
		if len(rest) > 0 {
			line += " " + strings.Join(rest, " ")
		}
		b.WriteString(line + "\n\n")
	}
}

// ---- index ----------------------------------------------------------------------------------------------------------------

type descSection struct {
	Title string
	Body  string
}

var parenthesized = regexp.MustCompile(`^\(([^)]*)\):\s*(.*)$`)

// headingRest returns the text that follows a pseudo-heading on its line. A parenthesized remark before the colon
// ("(not part of this document): text") becomes the start of the sentence.
func headingRest(rest string) string {
	rest = strings.TrimSpace(rest)
	if m := parenthesized.FindStringSubmatch(rest); m != nil && m[1] != "" {
		return strings.ToUpper(m[1][:1]) + m[1][1:] + ": " + m[2]
	}
	return strings.TrimSpace(strings.TrimPrefix(rest, ":"))
}

// splitDescription splits info.description at its bold pseudo-headings ("**Conventions**") into the introduction and the
// sections. A remainder on the heading line (": text") becomes the first line of the body. Fenced blocks are left alone.
func splitDescription(desc string) (string, []descSection) {
	var intro []string
	type sec struct {
		title string
		lines []string
	}
	var secs []*sec
	fence := false
	for _, line := range strings.Split(strings.TrimSpace(desc), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
		}
		if m := proseHeading.FindStringSubmatch(line); m != nil && !fence {
			s := &sec{title: strings.TrimSpace(m[1])}
			if rest := headingRest(m[2]); rest != "" {
				s.lines = append(s.lines, rest)
			}
			secs = append(secs, s)
			continue
		}
		if len(secs) > 0 {
			secs[len(secs)-1].lines = append(secs[len(secs)-1].lines, line)
		} else {
			intro = append(intro, line)
		}
	}
	out := make([]descSection, len(secs))
	for i, s := range secs {
		out[i] = descSection{Title: s.title, Body: strings.TrimSpace(strings.Join(s.lines, "\n"))}
	}
	return strings.TrimSpace(strings.Join(intro, "\n")), out
}

func (r *renderer) renderIndex() ([]byte, error) {
	var b strings.Builder
	frontMatter(&b, "REST API", indexWeight,
		"The OpenAPI 3.1 contract of the management API under /api/v1: authentication, roles, error format and every operation with its permission.")
	info := r.spec.Info
	b.WriteString("Generated from `" + specPath + "` (" + escapeAngle(info.Title) + ", contract version " + code(info.Version) + ").")
	if s := strings.TrimSpace(info.Summary); s != "" {
		b.WriteString(" " + escapeAngle(sentence(s)))
	}
	b.WriteString("\n\n")
	intro, secs := splitDescription(info.Description)
	if intro != "" {
		b.WriteString(prose(intro) + "\n\n")
	}
	emitGenerated := func() error {
		if err := r.renderRoles(&b); err != nil {
			return err
		}
		return r.renderErrors(&b)
	}
	hasAuth := false
	for _, s := range secs {
		if s.Title == "Authentication" {
			hasAuth = true
		}
	}
	for _, s := range secs {
		fmt.Fprintf(&b, "## %s\n\n", s.Title)
		if s.Body != "" {
			b.WriteString(prose(s.Body) + "\n\n")
		}
		if s.Title == "Authentication" {
			r.renderSchemes(&b)
			if err := emitGenerated(); err != nil {
				return nil, err
			}
		}
	}
	if !hasAuth {
		b.WriteString("## Authentication\n\n")
		r.renderSchemes(&b)
		if err := emitGenerated(); err != nil {
			return nil, err
		}
	}
	b.WriteString("## Operations by tag\n\n")
	rows := make([][]string, 0, len(r.tags))
	for _, tp := range r.tags {
		rows = append(rows, []string{fmt.Sprintf("[%s](%s)", tp.Title, tp.File), cell(tp.Tag.Description), strconv.Itoa(len(tp.Ops))})
	}
	table(&b, []string{"Tag", "Description", "Operations"}, rows)
	fmt.Fprintf(&b, "The schemas that the operations use are on the page [Data types](%s).\n\n", DataTypesFile)
	r.renderAbout(&b)
	return finish(&b), nil
}

// renderAbout names the license and the contact of the contract.
func (r *renderer) renderAbout(b *strings.Builder) {
	info := r.spec.Info
	var rows [][]string
	if l := info.License; l.Name != "" || l.Identifier != "" || l.URL != "" {
		text := cell(l.Name)
		if l.URL != "" && text != "" {
			text = "[" + text + "](" + l.URL + ")"
		}
		if l.Identifier != "" {
			if text != "" {
				text += ", "
			}
			text += "SPDX " + code(l.Identifier)
		}
		rows = append(rows, []string{"License", text})
	}
	if c := info.Contact; c.Name != "" || c.URL != "" || c.Email != "" {
		var parts []string
		name := cell(c.Name)
		switch {
		case c.URL != "" && name != "":
			parts = append(parts, "["+name+"]("+c.URL+")")
		case c.URL != "":
			parts = append(parts, c.URL)
		case name != "":
			parts = append(parts, name)
		}
		if c.Email != "" {
			parts = append(parts, c.Email)
		}
		rows = append(rows, []string{"Contact", strings.Join(parts, ", ")})
	}
	if len(rows) == 0 {
		return
	}
	b.WriteString("## About the contract\n\n")
	table(b, []string{"Item", "Value"}, rows)
}

// renderSchemes describes the security schemes of the contract.
func (r *renderer) renderSchemes(b *strings.Builder) {
	if len(r.spec.SecuritySchemes.Keys) == 0 {
		return
	}
	b.WriteString("### Security schemes\n\n")
	rows := make([][]string, 0, len(r.spec.SecuritySchemes.Keys))
	for _, k := range r.spec.SecuritySchemes.Keys {
		s := r.spec.SecuritySchemes.Values[k]
		var how string
		switch s.Type {
		case "apiKey":
			how = "API key in the " + s.In + " " + code(s.Name)
		case "http":
			how = "HTTP " + code(s.Scheme) + " authentication"
		default:
			how = code(s.Type)
		}
		rows = append(rows, []string{code(k), how, cell(s.Description)})
	}
	table(b, []string{"Scheme", "Type", "Description"}, rows)
}

// renderRoles writes the table of permissions with the lowest role that holds each of them.
func (r *renderer) renderRoles(b *strings.Builder) error {
	counts := map[string]int{}
	for _, op := range r.spec.Operations {
		counts[op.Permission]++
	}
	type row struct {
		perm, role string
		rank       int
		n          int
	}
	var rows []row
	for _, p := range sortedKeys(counts) {
		if p == "public" || p == "custom" {
			continue
		}
		role, err := r.roleOf(p)
		if err != nil {
			return err
		}
		rank := len(roleOrder)
		for i, ro := range roleOrder {
			if ro == role {
				rank = i
			}
		}
		rows = append(rows, row{p, role, rank, counts[p]})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		if rows[i].rank == len(roleOrder) && rows[i].role != rows[j].role {
			return rows[i].role < rows[j].role
		}
		return rows[i].perm < rows[j].perm
	})
	b.WriteString("## Roles and permissions\n\n")
	b.WriteString("Every operation names the permission that it needs. The table shows the lowest role that holds each permission; a role also holds the permissions of the roles below it. It is derived from the authorization code of the server, so it cannot drift from it.\n\n")
	out := make([][]string, 0, len(rows)+2)
	for _, x := range rows {
		out = append(out, []string{code(x.perm), x.role, strconv.Itoa(x.n)})
	}
	if n := counts["public"]; n > 0 {
		out = append(out, []string{"`public`", "none, no login", strconv.Itoa(n)})
	}
	if n := counts["custom"]; n > 0 {
		out = append(out, []string{"`custom`", "none, the operation checks its own credential", strconv.Itoa(n)})
	}
	table(b, []string{"Permission", "Lowest role", "Operations"}, out)
	return nil
}

// renderErrors describes the problem type and the shared error responses.
func (r *renderer) renderErrors(b *strings.Builder) error {
	problem, ok := r.spec.Schemas.Get("Problem")
	if !ok {
		return nil
	}
	b.WriteString("## Errors\n\n")
	b.WriteString("Errors are RFC 9457 problem details with the content type `application/problem+json` and the type [Problem](" + DataTypesFile + "#problem). The member `code` is a stable, machine-readable error code.\n\n")
	req := map[string]bool{}
	for _, n := range problem.Required {
		req[n] = true
	}
	rows := make([][]string, 0, len(problem.Properties.Keys))
	for _, k := range problem.Properties.Keys {
		p := problem.Properties.Values[k]
		field := code(k)
		if req[k] {
			field += " (required)"
		}
		rows = append(rows, []string{field, r.typeText(p, DataTypesFile), describe(p)})
	}
	table(b, []string{"Field", "Type", "Description"}, rows)
	if c, ok := problem.Properties.Get("code"); ok && len(c.Enum) > 0 {
		vals := make([]string, len(c.Enum))
		for i, v := range c.Enum {
			vals[i] = code(literalText(v))
		}
		b.WriteString("The codes: " + strings.Join(vals, ", ") + ".\n\n")
	}
	if len(r.problemStatuses) > 0 {
		var shared [][]string
		for _, name := range r.spec.Responses.Keys {
			codes := r.problemStatuses[name]
			if len(codes) == 0 {
				continue
			}
			resp := r.spec.Responses.Values[name]
			if _, isProblem := resp.Content.Get("application/problem+json"); !isProblem {
				continue
			}
			sort.Strings(codes)
			shared = append(shared, []string{strings.Join(codes, ", "), cell(resp.Description)})
		}
		sort.SliceStable(shared, func(i, j int) bool { return shared[i][0] < shared[j][0] })
		if len(shared) > 0 {
			b.WriteString("The responses that many operations share:\n\n")
			table(b, []string{"Status", "Meaning"}, shared)
		}
	}
	ex, err := r.spec.ExampleJSON(&Schema{Ref: "#/components/schemas/Problem"}, false)
	if err != nil {
		return err
	}
	b.WriteString("Example:\n\n```json\n" + ex + "\n```\n\n")
	return nil
}
