// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/domain"
)

// Render renders the config. Problems (missing parameters, template errors,
// invalid result) are returned in RenderResult.Issues with an empty Body; the
// error is reserved for programming errors.
func (r *Renderer) Render(in RenderInput) (domain.RenderResult, error) {
	res := domain.RenderResult{
		Layers:           []string{},
		Parameters:       map[string]any{},
		ParameterSources: map[string]string{},
		Issues:           domain.Issues{},
	}
	if in.Snapshot == nil {
		return res, errors.New("blueprint.Render: snapshot is required")
	}
	if in.Fleet != nil {
		res.Fleet = in.Fleet.Name
	}
	name := in.Blueprint
	if name == "" && in.Fleet != nil {
		name = in.Fleet.Blueprint
	}
	res.Blueprint = name
	if name == "" {
		res.Issues = append(res.Issues, domain.Issue{Severity: domain.SeverityError, Code: "missing_field", Message: "no blueprint given", Path: "spec.blueprint"})
		return res, nil
	}

	fail := func(is ...domain.Issue) (domain.RenderResult, error) {
		res.Issues = append(res.Issues, is...)
		res.Body, res.Hash = nil, nil
		return res, nil
	}

	chain, cerr := resolveChain(in.Snapshot, name, fleetRef(in.Fleet))
	if len(cerr) > 0 {
		return fail(cerr...)
	}
	leaf := chain[len(chain)-1]

	decls, dissues := declareParams(chain)
	if len(dissues) > 0 {
		return fail(dissues...)
	}
	params, pissues := r.resolveParams(decls, in, leaf)
	res.Issues = append(res.Issues, pissues...)
	res.Parameters = params.values
	res.ParameterSources = params.sources
	if res.Issues.HasErrors() {
		res.Body, res.Hash = nil, nil
		res.Layers = chainNames(chain)
		return res, nil
	}

	data := templateData(in, leaf.Name, params.values)
	ec := newEvalCtx(r, data, in.Agent.Attributes, params.values)

	// Step 3: fragments' "when", layer names, merge.
	active := make([][]bool, len(chain))
	var wissues domain.Issues
	for i, bp := range chain {
		active[i] = make([]bool, len(bp.Fragments))
		for j, fr := range bp.Fragments {
			on, is := ec.evalWhen(bp, j, fr)
			if is != nil {
				wissues = append(wissues, *is)
				continue
			}
			active[i][j] = on
		}
	}
	if len(wissues) > 0 {
		res.Layers = chainNames(chain)
		return fail(wissues...)
	}
	res.Layers = layerNames(chain, active)

	merged := r.mergeChain(chain, active)

	// Step 4: templates.
	tree, tissues := ec.evalTree(merged)
	if len(tissues) > 0 {
		for _, ti := range tissues {
			code := "template_error"
			if ti.syntax {
				code = "template_syntax"
			}
			is := domain.Issue{
				Severity: domain.SeverityError, Code: code,
				Message: fmt.Sprintf("%s: %v", ti.path, cleanTemplateErr(ti.err)),
				Path:    ti.path, Object: "Blueprint/" + leaf.Name, File: leaf.Source.Path,
			}
			if bp, fp, ok := locatePath(chain, active, ti.path); ok {
				is.Object, is.File, is.Path = "Blueprint/"+bp.Name, bp.Source.Path, fp
			}
			res.Issues = append(res.Issues, is)
		}
		res.Body, res.Hash = nil, nil
		return res, nil
	}

	// Step 5: validation of the result.
	cissues := r.validateTree(tree, in.Components)
	for i := range cissues {
		if cissues[i].Object == "" {
			cissues[i].Object = "Blueprint/" + leaf.Name
			cissues[i].File = leaf.Source.Path
			if bp, fp, ok := locatePath(chain, active, cissues[i].Path); ok {
				cissues[i].Object, cissues[i].File, cissues[i].Path = "Blueprint/"+bp.Name, bp.Source.Path, fp
			}
		}
	}
	res.Issues = append(res.Issues, cissues...)
	if res.Issues.HasErrors() {
		res.Body, res.Hash = nil, nil
		return res, nil
	}

	// Step 6: canonical serialization.
	body, err := encodeCanonical(tree)
	if err != nil {
		return res, fmt.Errorf("encode canonical yaml: %w", err)
	}
	if len(body) > MaxRenderedConfigBytes {
		return res, fmt.Errorf("rendered config of %d bytes exceeds the limit of %d bytes", len(body), MaxRenderedConfigBytes)
	}
	res.Body = body
	res.Hash = domain.ConfigHash(body)
	return res, nil
}

// MaxRenderedConfigBytes caps the canonical body of one rendered config.
const MaxRenderedConfigBytes = 1 << 20

func fleetRef(f *domain.Fleet) string {
	if f == nil {
		return ""
	}
	return f.Name
}

var (
	execErrRE = regexp.MustCompile(`^template: [^:]*:\d+(?::\d+)?: executing "[^"]*" at (<.*?>): (.*)$`)
	// guardErrRE is what text/template puts before the error of one of the
	// internal functions (checkTree) that bound a template at execution time.
	guardErrRE = regexp.MustCompile(`^error calling _[A-Za-z]+: `)
)

// cleanTemplateErr shortens text/template errors:
// `template: typed:1:15: executing "typed" at <.params.x>: msg` -> `msg (at <.params.x>)`.
// The internal functions mean nothing to the template author: their errors
// are reported without the function name and without a location in them.
func cleanTemplateErr(err error) string {
	s := err.Error()
	if m := execErrRE.FindStringSubmatch(s); m != nil {
		msg := strings.TrimSpace(m[2])
		if guard := guardErrRE.FindString(msg); guard != "" {
			return msg[len(guard):]
		}
		return msg + " (at " + m[1] + ")"
	}
	s = strings.TrimPrefix(s, "template: ")
	return s
}

// locatePath finds the blueprint of the chain (leaf first) whose config or
// active fragment config contains the confmap path and returns it together
// with the field path inside that blueprint ("spec.config::a::b").
func locatePath(chain []*domain.Blueprint, active [][]bool, confPath string) (*domain.Blueprint, string, bool) {
	segs := splitConfPath(confPath)
	if len(segs) == 0 {
		return nil, "", false
	}
	for i := len(chain) - 1; i >= 0; i-- {
		bp := chain[i]
		for j := len(bp.Fragments) - 1; j >= 0; j-- {
			if active != nil && !active[i][j] {
				continue
			}
			if hasConfPath(bp.Fragments[j].Config, segs) {
				return bp, fmt.Sprintf("spec.fragments[%d].config%s%s", j, pathSeparator, confPath), true
			}
		}
		if hasConfPath(bp.Config, segs) {
			return bp, "spec.config" + pathSeparator + confPath, true
		}
	}
	return nil, "", false
}

// splitConfPath splits "a::b[0]::c" into ["a","b","[0]","c"].
func splitConfPath(p string) []string {
	var out []string
	for _, part := range strings.Split(p, pathSeparator) {
		if part == "" {
			continue
		}
		name := part
		var idx []string
		if i := strings.IndexByte(part, '['); i > 0 {
			name = part[:i]
			rest := part[i:]
			for len(rest) > 0 && rest[0] == '[' {
				j := strings.IndexByte(rest, ']')
				if j < 0 {
					break
				}
				idx = append(idx, rest[:j+1])
				rest = rest[j+1:]
			}
		}
		out = append(out, name)
		out = append(out, idx...)
	}
	return out
}

func hasConfPath(tree map[string]any, segs []string) bool {
	var cur any = tree
	for _, seg := range segs {
		switch t := cur.(type) {
		case map[string]any:
			next, ok := t[seg]
			if !ok {
				return false
			}
			cur = next
		case []any:
			if len(seg) < 3 || seg[0] != '[' {
				return false
			}
			n, err := strconv.Atoi(seg[1 : len(seg)-1])
			if err != nil || n < 0 || n >= len(t) {
				return false
			}
			cur = t[n]
		default:
			return false
		}
	}
	return true
}

func chainNames(chain []*domain.Blueprint) []string {
	out := make([]string, len(chain))
	for i, bp := range chain {
		out[i] = bp.Name
	}
	return out
}

func layerNames(chain []*domain.Blueprint, active [][]bool) []string {
	out := []string{}
	for i, bp := range chain {
		out = append(out, bp.Name)
		for j, fr := range bp.Fragments {
			if active[i][j] {
				out = append(out, bp.Name+"#"+fr.Name)
			}
		}
	}
	return out
}

// resolveChain returns the blueprint chain root-first.
func resolveChain(snap *domain.GitOpsSnapshot, leaf, fleet string) ([]*domain.Blueprint, domain.Issues) {
	chain, is, _ := walkChain(snap, leaf, fleet)
	return chain, is
}

// walkChain is resolveChain that additionally returns the members of a
// detected extends cycle (starting with the first repeated blueprint).
func walkChain(snap *domain.GitOpsSnapshot, leaf, fleet string) ([]*domain.Blueprint, domain.Issues, []string) {
	byName := make(map[string]*domain.Blueprint, len(snap.Blueprints))
	for i := range snap.Blueprints {
		byName[snap.Blueprints[i].Name] = &snap.Blueprints[i]
	}
	var rev []*domain.Blueprint
	seen := map[string]bool{}
	var trail []string
	cur := leaf
	var referrer *domain.Blueprint
	for cur != "" {
		bp, ok := byName[cur]
		if !ok {
			is := domain.Issue{Severity: domain.SeverityError, Code: "blueprint_not_found"}
			if referrer != nil {
				is.Message = fmt.Sprintf("blueprint %q (extended by %q) does not exist", cur, referrer.Name)
				is.File, is.Object, is.Path = referrer.Source.Path, "Blueprint/"+referrer.Name, "spec.extends"
			} else {
				is.Message = fmt.Sprintf("blueprint %q does not exist", cur)
				if fleet != "" {
					is.Object, is.Path = "Fleet/"+fleet, "spec.blueprint"
				}
			}
			return nil, domain.Issues{is}, nil
		}
		trail = append(trail, cur)
		if seen[cur] {
			first := 0
			for i, n := range trail {
				if n == cur {
					first = i
					break
				}
			}
			return nil, domain.Issues{{
				Severity: domain.SeverityError, Code: "extends_cycle",
				Message: "extends cycle: " + strings.Join(trail[first:], " -> "),
				File:    bp.Source.Path, Object: "Blueprint/" + bp.Name, Path: "spec.extends",
			}}, trail[first : len(trail)-1]
		}
		seen[cur] = true
		rev = append(rev, bp)
		referrer = bp
		cur = bp.Extends
	}
	chain := make([]*domain.Blueprint, len(rev))
	for i, bp := range rev {
		chain[len(rev)-1-i] = bp
	}
	return chain, nil, nil
}

// evalWhen evaluates the "when" expression of a fragment. A nil issue means
// the result is valid.
func (c *evalCtx) evalWhen(bp *domain.Blueprint, idx int, fr domain.Fragment) (bool, *domain.Issue) {
	if strings.TrimSpace(fr.When) == "" {
		return true, nil
	}
	path := "spec.fragments[" + strconv.Itoa(idx) + "].when"
	mk := func(code, msg string) *domain.Issue {
		return &domain.Issue{Severity: domain.SeverityError, Code: code, Message: msg, File: bp.Source.Path, Object: "Blueprint/" + bp.Name, Path: path}
	}
	e := c.r.parseEntry(fr.When)
	if e.err != nil {
		return false, mk("template_syntax", fmt.Sprintf("fragment %q: when: %v", fr.Name, e.err))
	}
	if !e.whole {
		return false, mk("template_syntax", fmt.Sprintf("fragment %q: when must be exactly one \"[[ expression ]]\" evaluating to a bool", fr.Name))
	}
	v, err := c.evalString(fr.When)
	if err != nil {
		code := "template_error"
		var te *templateError
		if errors.As(err, &te) && te.syntax {
			code = "template_syntax"
		}
		return false, mk(code, fmt.Sprintf("fragment %q: when: %v", fr.Name, cleanTemplateErr(err)))
	}
	b, ok := v.(bool)
	if !ok {
		return false, mk("when_not_bool", fmt.Sprintf("fragment %q: when must evaluate to a bool, got %s", fr.Name, describe(v)))
	}
	return b, nil
}

// mergeChain merges the layers of the chain (cached when all blueprints have
// content hashes).
func (r *Renderer) mergeChain(chain []*domain.Blueprint, active [][]bool) map[string]any {
	key := mergeKey(chain, active)
	if key != "" {
		if t, ok := r.mergeCache.Get(key); ok {
			return t
		}
	}
	tree := map[string]any{}
	for i, bp := range chain {
		tree = mergeMaps(tree, bp.Config)
		for j, fr := range bp.Fragments {
			if !active[i][j] {
				continue
			}
			tree = mergeMaps(tree, fr.Config)
			removePaths(tree, fr.Remove)
		}
		removePaths(tree, bp.Remove)
	}
	if key != "" {
		r.mergeCache.Add(key, tree)
	}
	return tree
}

func mergeKey(chain []*domain.Blueprint, active [][]bool) string {
	var sb strings.Builder
	for i, bp := range chain {
		if len(bp.Source.ContentHash) == 0 {
			return ""
		}
		sb.WriteString(bp.Name)
		sb.WriteByte('@')
		sb.WriteString(hex.EncodeToString(bp.Source.ContentHash))
		sb.WriteByte(':')
		for _, a := range active[i] {
			if a {
				sb.WriteByte('1')
			} else {
				sb.WriteByte('0')
			}
		}
		sb.WriteByte(';')
	}
	return sb.String()
}

// encodeCanonical serializes a tree as canonical YAML (docs/concepts/blueprints.md,
// "Canonical form and hash"): yaml.v3, indent 2, sorted map keys.
func encodeCanonical(tree map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
