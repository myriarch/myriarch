// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
)

const maxFileSize = 4 << 20

// maxNodeVisit bounds the strict check of one repository (variable for tests).
var maxNodeVisit = 500000

// Limits bounds the work done for one repository revision. Zero fields use
// the defaults of DefaultLimits.
type Limits struct {
	// MaxFiles is the maximum number of YAML files.
	MaxFiles int
	// MaxFileBytes is the maximum size of one file (checked before reading).
	MaxFileBytes int64
	// MaxTotalBytes is the maximum total size of all YAML files.
	MaxTotalBytes int64
	// Deadline (zero = none) ends parsing and validation of a revision: when it
	// passes, an error issue is reported and the rest is skipped.
	Deadline time.Time
}

// DefaultLimits are the limits used by Parse and Validate.
var DefaultLimits = Limits{MaxFiles: 2000, MaxFileBytes: maxFileSize, MaxTotalBytes: 64 << 20}

func (l Limits) withDefaults() Limits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultLimits.MaxFiles
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = DefaultLimits.MaxFileBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = DefaultLimits.MaxTotalBytes
	}
	return l
}

func (l Limits) expired() bool { return !l.Deadline.IsZero() && time.Now().After(l.Deadline) }

// readLimited reads a regular file of at most max bytes (it never reads
// more than max+1 bytes, whatever the file claims to be).
func readLimited(fsys fs.FS, name string, max int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("file is larger than %d bytes", max)
	}
	return b, nil
}

// notRegular reports whether a directory entry is a symbolic link or another
// non-regular file (device, socket, pipe). Such entries are never read.
func notRegular(d fs.DirEntry) bool { return d.Type()&fs.ModeType != 0 }

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ObjectPos locates a parsed object in its source file (for line numbers of
// issues found later, e.g. by ValidateSnapshot).
type ObjectPos struct {
	File string
	// Line of the "kind:" key (document start).
	Line int
	// Node is the document's root mapping.
	Node *yaml.Node
}

// ParsedRepo is the detailed result of ParseDetailed.
type ParsedRepo struct {
	Snapshot *domain.GitOpsSnapshot
	Issues   domain.Issues
	// Objects maps "<Kind>/<name>" to the source position.
	Objects map[string]ObjectPos
}

// Parse reads all *.yaml/*.yml files below root of fsys (skipping
// directories starting with "." and the "samples" directory), decodes the
// multi-document YAML objects and returns the snapshot plus syntax/schema
// issues (with file and line). commit is stored in every SourceRef.
func Parse(fsys fs.FS, root, commit string) (*domain.GitOpsSnapshot, domain.Issues) {
	p := ParseDetailed(fsys, root, commit)
	return p.Snapshot, p.Issues
}

// ParseDetailed is Parse plus the source positions of all objects.
func ParseDetailed(fsys fs.FS, root, commit string) *ParsedRepo {
	return ParseDetailedLimited(fsys, root, commit, DefaultLimits)
}

// ParseDetailedLimited is ParseDetailed with explicit limits: the number and
// size of files are bounded (sizes are checked before a file is read) and
// symbolic links and other non-regular files are reported as errors instead of
// being followed.
func ParseDetailedLimited(fsys fs.FS, root, commit string, lim Limits) *ParsedRepo {
	lim = lim.withDefaults()
	root = cleanRoot(root)
	pr := &ParsedRepo{
		Snapshot: &domain.GitOpsSnapshot{
			Commit:          commit,
			Fleets:          []domain.Fleet{},
			Blueprints:      []domain.Blueprint{},
			RolloutPolicies: []domain.RolloutPolicy{},
			Executors:       []domain.Executor{},
			Inventories:     []domain.Inventory{},
		},
		Issues:  domain.Issues{},
		Objects: map[string]ObjectPos{},
	}
	p := &parser{pr: pr, commit: commit, first: map[string]string{}, lim: lim}

	var files []string
	var total int64
	samplesDir := path.Join(root, "samples")
	err := fs.WalkDir(fsys, root, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if fp != root && (strings.HasPrefix(d.Name(), ".") || fp == samplesDir) {
				return fs.SkipDir
			}
			return nil
		}
		if notRegular(d) {
			p.errorf("not_regular_file", fp, 0, "", "", "symbolic links and special files are not allowed in the repository")
			return nil
		}
		ext := strings.ToLower(path.Ext(fp))
		if ext == ".yaml" || ext == ".yml" {
			if len(files) >= lim.MaxFiles {
				if len(files) == lim.MaxFiles {
					p.errorf("too_many_files", fp, 0, "", "", "the repository contains more than %d YAML files", lim.MaxFiles)
					files = append(files, "") // report once
				}
				return nil
			}
			if info, err := d.Info(); err == nil {
				if info.Size() > lim.MaxFileBytes {
					p.errorf("file_too_large", fp, 0, "", "", "file is larger than %d bytes", lim.MaxFileBytes)
					return nil
				}
				total += info.Size()
				if total > lim.MaxTotalBytes {
					p.errorf("repository_too_large", fp, 0, "", "", "the YAML files exceed %d bytes in total", lim.MaxTotalBytes)
					return fs.SkipAll
				}
			}
			files = append(files, fp)
		}
		return nil
	})
	if err != nil {
		pr.Issues = append(pr.Issues, domain.Issue{Severity: domain.SeverityError, Code: "yaml_syntax", Message: fmt.Sprintf("cannot read repository directory %q: %v", root, err)})
		return pr
	}
	sort.Strings(files)
	for _, f := range files {
		if f == "" {
			continue
		}
		if lim.expired() {
			p.errorf("time_budget_exceeded", f, 0, "", "", "parsing the repository took longer than the time budget; the remaining files were skipped")
			break
		}
		p.parseFile(fsys, f)
	}
	sortSnapshot(pr.Snapshot)
	return pr
}

func cleanRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return "."
	}
	return path.Clean(root)
}

func sortSnapshot(s *domain.GitOpsSnapshot) {
	sort.SliceStable(s.Fleets, func(i, j int) bool { return s.Fleets[i].Name < s.Fleets[j].Name })
	sort.SliceStable(s.Blueprints, func(i, j int) bool { return s.Blueprints[i].Name < s.Blueprints[j].Name })
	sort.SliceStable(s.RolloutPolicies, func(i, j int) bool { return s.RolloutPolicies[i].Name < s.RolloutPolicies[j].Name })
	sort.SliceStable(s.Executors, func(i, j int) bool { return s.Executors[i].Name < s.Executors[j].Name })
	sort.SliceStable(s.Inventories, func(i, j int) bool { return s.Inventories[i].Name < s.Inventories[j].Name })
}

type parser struct {
	pr     *ParsedRepo
	commit string
	// first maps "<Kind>/<name>" to "file:line" of the first definition.
	first  map[string]string
	visits int
	lim    Limits
	// budgetReported: the node budget error was reported already.
	budgetReported bool
}

func (p *parser) issue(sev domain.IssueSeverity, code, msg, file string, line int, object, fieldPath string) {
	p.pr.Issues = append(p.pr.Issues, domain.Issue{Severity: sev, Code: code, Message: msg, File: file, Line: line, Object: object, Path: fieldPath})
}

func (p *parser) errorf(code, file string, line int, object, fieldPath, format string, args ...any) {
	p.issue(domain.SeverityError, code, fmt.Sprintf(format, args...), file, line, object, fieldPath)
}

var yamlLineRE = regexp.MustCompile(`line (\d+)`)

func lineFromError(err error) int {
	if m := yamlLineRE.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func (p *parser) parseFile(fsys fs.FS, file string) {
	data, err := readLimited(fsys, file, p.lim.MaxFileBytes)
	if err != nil {
		p.errorf("yaml_syntax", file, 0, "", "", "cannot read file: %v", err)
		return
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			p.errorf("yaml_syntax", file, lineFromError(err), "", "", "%s", cleanYAMLError(err))
			return // the remaining documents of the file cannot be read reliably
		}
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
			continue
		}
		body := doc.Content[0]
		if body.Kind == yaml.ScalarNode && body.Tag == "!!null" {
			continue
		}
		p.parseObject(file, body)
	}
}

func cleanYAMLError(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, "yaml: ")
	return s
}

// derefAlias resolves alias nodes.
func derefAlias(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < 32; i++ {
		n = n.Alias
	}
	return n
}

type mapEntry struct {
	key, val *yaml.Node
}

// mappingEntries lists the entries of a mapping, expanding "<<" merge keys.
func mappingEntries(n *yaml.Node) []mapEntry {
	n = derefAlias(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var out []mapEntry
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag == "!!merge" || (k.Kind == yaml.ScalarNode && k.Value == "<<" && k.Style == 0) {
			vv := derefAlias(v)
			if vv != nil && vv.Kind == yaml.SequenceNode {
				for _, e := range vv.Content {
					out = append(out, mappingEntries(e)...)
				}
			} else {
				out = append(out, mappingEntries(vv)...)
			}
			continue
		}
		out = append(out, mapEntry{k, v})
	}
	return out
}

func (p *parser) parseObject(file string, body *yaml.Node) {
	line := body.Line
	if body.Kind != yaml.MappingNode {
		p.errorf("missing_field", file, line, "", "", "document must be a mapping with apiVersion, kind, metadata and spec")
		return
	}
	var apiVersion, kind, meta, spec *mapEntry
	for _, e := range mappingEntries(body) {
		e := e
		switch e.key.Value {
		case "apiVersion":
			apiVersion = &e
		case "kind":
			kind = &e
		case "metadata":
			meta = &e
		case "spec":
			spec = &e
		default:
			p.errorf("unknown_field", file, e.key.Line, "", e.key.Value, "unknown field %q at the top level of an object (allowed: apiVersion, kind, metadata, spec)%s", e.key.Value, suggest(e.key.Value, []string{"apiVersion", "kind", "metadata", "spec"}))
		}
	}
	bad := false
	if apiVersion == nil {
		p.errorf("unknown_api_version", file, line, "", "apiVersion", "apiVersion is missing (expected %q)", domain.APIVersion)
		bad = true
	} else if v := derefAlias(apiVersion.val); v.Kind != yaml.ScalarNode || v.Value != domain.APIVersion {
		p.errorf("unknown_api_version", file, apiVersion.val.Line, "", "apiVersion", "unknown apiVersion %q (expected %q)", v.Value, domain.APIVersion)
		bad = true
	}
	var k domain.ObjectKind
	if kind == nil {
		p.errorf("unknown_kind", file, line, "", "kind", "kind is missing (Fleet, Blueprint, RolloutPolicy, Executor, Inventory)")
		return
	}
	kv := derefAlias(kind.val)
	k = domain.ObjectKind(kv.Value)
	known := false
	for _, ak := range domain.AllObjectKinds {
		if ak == k {
			known = true
		}
	}
	if !known {
		p.errorf("unknown_kind", file, kind.val.Line, "", "kind", "unknown kind %q (Fleet, Blueprint, RolloutPolicy, Executor, Inventory)", kv.Value)
		return
	}
	if meta == nil {
		p.errorf("missing_field", file, line, string(k), "metadata", "metadata is missing")
		return
	}
	var om domain.ObjectMeta
	mnode := derefAlias(meta.val)
	if mnode.Kind != yaml.MappingNode {
		p.errorf("missing_field", file, meta.key.Line, string(k), "metadata", "metadata must be a mapping")
		return
	}
	p.strictCheck(file, "", mnode, reflect.TypeOf(om), "metadata")
	if err := mnode.Decode(&om); err != nil {
		p.decodeErrors(file, "", mnode, err, "metadata")
		return
	}
	if om.Name == "" {
		p.errorf("missing_field", file, meta.key.Line, string(k), "metadata.name", "metadata.name is required")
		return
	}
	object := string(k) + "/" + om.Name
	if !nameRE.MatchString(om.Name) {
		p.errorf("invalid_name", file, nameLine(mnode), object, "metadata.name", "name %q is invalid (must match %s)", om.Name, nameRE.String())
		bad = true
	}
	if bad {
		return
	}
	if spec == nil {
		p.errorf("missing_field", file, line, object, "spec", "spec is missing")
		return
	}
	snode := derefAlias(spec.val)
	if snode.Kind == yaml.ScalarNode && snode.Tag == "!!null" {
		snode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: spec.key.Line}
	}
	if snode.Kind != yaml.MappingNode {
		p.errorf("missing_field", file, spec.key.Line, object, "spec", "spec must be a mapping")
		return
	}

	// Duplicate detection (first definition wins).
	if prev, ok := p.first[object]; ok {
		p.errorf("duplicate_object", file, nameLine(mnode), object, "metadata.name", "%s is defined more than once (first at %s)", object, prev)
		return
	}
	p.first[object] = fmt.Sprintf("%s:%d", file, nameLine(mnode))

	src := domain.SourceRef{Path: file, Commit: p.commit}
	ok := p.decodeSpec(file, k, om, object, snode, src)
	if !ok {
		delete(p.first, object)
		return
	}
	p.pr.Objects[object] = ObjectPos{File: file, Line: line, Node: body}
}

func nameLine(meta *yaml.Node) int {
	for _, e := range mappingEntries(meta) {
		if e.key.Value == "name" {
			return e.val.Line
		}
	}
	return meta.Line
}

// decodeSpec strictly decodes spec into the typed object and appends it to
// the snapshot.
func (p *parser) decodeSpec(file string, kind domain.ObjectKind, om domain.ObjectMeta, object string, snode *yaml.Node, src domain.SourceRef) bool {
	var target any
	switch kind {
	case domain.KindFleet:
		target = &domain.FleetSpec{}
	case domain.KindBlueprint:
		target = &domain.BlueprintSpec{}
	case domain.KindRolloutPolicy:
		target = &domain.RolloutPolicySpec{}
	case domain.KindExecutor:
		target = &domain.ExecutorSpec{}
	case domain.KindInventory:
		target = &domain.InventorySpec{}
	}
	p.visits = 0
	p.strictCheck(file, object, snode, reflect.TypeOf(target).Elem(), "spec")
	p.scanFlowTemplates(file, object, snode, "spec")
	if kind == domain.KindBlueprint {
		p.scanConfigKeys(file, object, snode)
	}
	if err := snode.Decode(target); err != nil {
		p.decodeErrors(file, object, snode, err, "spec")
		return false
	}
	// Content hash over the canonical JSON of the spec.
	cj, err := json.Marshal(target)
	if err != nil {
		p.errorf("invalid_field", file, snode.Line, object, "spec", "spec cannot be serialized: %v", err)
		return false
	}
	h := sha256.Sum256(cj)
	src.ContentHash = domain.Hash(h[:])

	snap := p.pr.Snapshot
	switch s := target.(type) {
	case *domain.FleetSpec:
		s.Parameters = normMap(s.Parameters)
		s.Update.Params = normMap(s.Update.Params)
		snap.Fleets = append(snap.Fleets, domain.Fleet{ObjectMeta: om, FleetSpec: *s, Source: src})
	case *domain.BlueprintSpec:
		s.Config = normMap(s.Config)
		for i := range s.Parameters {
			s.Parameters[i].Default = blueprint.NormalizeTree(s.Parameters[i].Default)
			for j := range s.Parameters[i].Enum {
				s.Parameters[i].Enum[j] = blueprint.NormalizeTree(s.Parameters[i].Enum[j])
			}
		}
		for i := range s.Fragments {
			s.Fragments[i].Config = normMap(s.Fragments[i].Config)
		}
		snap.Blueprints = append(snap.Blueprints, domain.Blueprint{ObjectMeta: om, BlueprintSpec: *s, Source: src})
	case *domain.RolloutPolicySpec:
		snap.RolloutPolicies = append(snap.RolloutPolicies, domain.RolloutPolicy{ObjectMeta: om, RolloutPolicySpec: *s, Source: src})
	case *domain.ExecutorSpec:
		if s.AWX != nil {
			s.AWX.ExtraVars = normMap(s.AWX.ExtraVars)
		}
		snap.Executors = append(snap.Executors, domain.Executor{ObjectMeta: om, ExecutorSpec: *s, Source: src})
	case *domain.InventorySpec:
		for i := range s.Entries {
			s.Entries[i].Parameters = normMap(s.Entries[i].Parameters)
		}
		snap.Inventories = append(snap.Inventories, domain.Inventory{ObjectMeta: om, InventorySpec: *s, Source: src})
	}
	return true
}

func normMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := blueprint.NormalizeTree(m).(map[string]any)
	return out
}

// decodeErrors converts yaml decode errors into issues with lines.
func (p *parser) decodeErrors(file, object string, n *yaml.Node, err error, fieldPath string) {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, msg := range te.Errors {
			p.errorf("invalid_field", file, orLine(lineFromError(errors.New(msg)), n.Line), object, fieldPath, "%s", strings.TrimSpace(yamlLineRE.ReplaceAllString(strings.TrimPrefix(msg, "line "), "")))
		}
		return
	}
	p.errorf("invalid_field", file, n.Line, object, fieldPath, "%s", cleanYAMLError(err))
}

func orLine(l, def int) int {
	if l > 0 {
		return l
	}
	return def
}

// ---------------------------------------------------------- strict check --

// strictCheck walks the node tree against the yaml tags of t and reports
// unknown fields (yaml.v3 Node.Decode has no KnownFields option). Free-form
// areas (map[string]any, any) are not descended.
func (p *parser) strictCheck(file, object string, n *yaml.Node, t reflect.Type, fieldPath string) {
	p.visits++
	if p.visits > maxNodeVisit {
		if !p.budgetReported {
			p.budgetReported = true
			p.errorf("repository_too_complex", file, n.Line, object, fieldPath, "the repository has more than %d YAML nodes: strict checks were stopped (split or simplify the files)", maxNodeVisit)
		}
		return
	}
	n = derefAlias(n)
	if n == nil {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return // reported by Decode
		}
		fields := yamlFields(t)
		names := make([]string, 0, len(fields))
		for k := range fields {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, e := range mappingEntries(n) {
			ft, ok := fields[e.key.Value]
			if !ok {
				p.errorf("unknown_field", file, e.key.Line, object, joinFieldPath(fieldPath, e.key.Value),
					"unknown field %q in %s%s", e.key.Value, fieldPath, suggest(e.key.Value, names))
				continue
			}
			p.strictCheck(file, object, e.val, ft, joinFieldPath(fieldPath, e.key.Value))
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return
		}
		for i, c := range n.Content {
			p.strictCheck(file, object, c, t.Elem(), fmt.Sprintf("%s[%d]", fieldPath, i))
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return
		}
		if t.Elem().Kind() == reflect.Interface {
			return // free-form
		}
		for _, e := range mappingEntries(n) {
			p.strictCheck(file, object, e.val, t.Elem(), fieldPath+"::"+e.key.Value)
		}
	}
}

func joinFieldPath(base, key string) string { return base + "." + key }

func yamlFields(t reflect.Type) map[string]reflect.Type {
	m := map[string]reflect.Type{}
	collectFields(t, m)
	return m
}

func collectFields(t reflect.Type, m map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" || strings.Contains(opts, "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				collectFields(ft, m)
				continue
			}
		}
		if name == "" {
			name = strings.ToLower(f.Name[:1]) + f.Name[1:]
		}
		m[name] = f.Type
	}
}

func suggest(got string, candidates []string) string {
	best, bestD := "", 3
	for _, c := range candidates {
		if d := levenshtein(strings.ToLower(got), strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// ----------------------------------------------------- template detection --

// scanFlowTemplates reports unquoted "[[ ... ]]": a flow sequence whose only
// element is a flow sequence with exactly one scalar (YAML read the template
// delimiters as nested flow sequences).
func (p *parser) scanFlowTemplates(file, object string, n *yaml.Node, fieldPath string) {
	p.visits++
	if p.visits > maxNodeVisit || n == nil {
		return
	}
	if n.Kind == yaml.AliasNode {
		return // the anchor definition is scanned where it is defined
	}
	if n.Kind == yaml.SequenceNode && n.Style&yaml.FlowStyle != 0 && len(n.Content) == 1 {
		inner := n.Content[0]
		if inner.Kind == yaml.SequenceNode && inner.Style&yaml.FlowStyle != 0 && len(inner.Content) == 1 && inner.Content[0].Kind == yaml.ScalarNode {
			expr := strings.TrimSpace(inner.Content[0].Value)
			p.errorf("unquoted_template", file, n.Line, object, fieldPath,
				"value [[ %s ]] is not quoted: YAML reads \"[[\" as nested lists; write \"[[ %s ]]\" in quotes", expr, expr)
			return
		}
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p.scanFlowTemplates(file, object, n.Content[i+1], fieldPath+"::"+n.Content[i].Value)
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for i, c := range n.Content {
			p.scanFlowTemplates(file, object, c, fmt.Sprintf("%s[%d]", fieldPath, i))
		}
	}
}

// scanConfigKeys reports "::" in config keys of a blueprint (confmap
// interprets "::" as path separator): spec.config and fragments[].config.
func (p *parser) scanConfigKeys(file, object string, spec *yaml.Node) {
	var walk func(n *yaml.Node, fp string)
	walk = func(n *yaml.Node, fp string) {
		p.visits++
		if p.visits > maxNodeVisit {
			return
		}
		n = derefAlias(n)
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.MappingNode:
			for _, e := range mappingEntries(n) {
				if strings.Contains(e.key.Value, "::") {
					p.errorf("invalid_config_key", file, e.key.Line, object, fp+"::"+e.key.Value,
						"config key %q contains \"::\", which confmap interprets as path separator", e.key.Value)
				}
				walk(e.val, fp+"::"+e.key.Value)
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, fmt.Sprintf("%s[%d]", fp, i))
			}
		}
	}
	for _, e := range mappingEntries(spec) {
		switch e.key.Value {
		case "config":
			walk(e.val, "spec.config")
		case "fragments":
			if fr := derefAlias(e.val); fr.Kind == yaml.SequenceNode {
				for i, f := range fr.Content {
					for _, fe := range mappingEntries(f) {
						if fe.key.Value == "config" {
							walk(fe.val, fmt.Sprintf("spec.fragments[%d].config", i))
						}
					}
				}
			}
		}
	}
}
