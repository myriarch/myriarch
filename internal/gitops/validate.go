// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
)

// Result is the outcome of validating a repository revision.
type Result struct {
	Snapshot *domain.GitOpsSnapshot
	Samples  []blueprint.Sample
	// Issues are all findings (parse, samples, semantic validation), with
	// file and line where known, sorted by file and line.
	Issues domain.Issues
}

// Valid reports whether the repository has no error issues.
func (r *Result) Valid() bool { return !r.Issues.HasErrors() }

// softParseCodes are parse findings that do not prevent semantic validation
// (the snapshot is complete).
var softParseCodes = map[string]bool{"unknown_field": true, "unquoted_template": true, "invalid_config_key": true}

// Validate parses the repository and validates it exactly like myriarchd does
// before it applies a revision: strict parse, samples, ValidateSnapshot
// (which renders every fleet for every matching sample). Used by the syncer
// and by myrctl, so CI and myriarchd agree.
func Validate(fsys fs.FS, root, commit string, r *blueprint.Renderer, components *domain.ComponentSet) *Result {
	return ValidateLimited(fsys, root, commit, r, components, DefaultLimits)
}

// ValidateLimited is Validate with explicit limits (file counts and sizes, time
// budget) as configured for the syncer.
func ValidateLimited(fsys fs.FS, root, commit string, r *blueprint.Renderer, components *domain.ComponentSet, lim Limits) *Result {
	pr := ParseDetailedLimited(fsys, root, commit, lim)
	samples, sIssues := ParseSamplesLimited(fsys, root, lim)
	res := &Result{Snapshot: pr.Snapshot, Samples: samples}
	issues := append(domain.Issues{}, pr.Issues...)
	issues = append(issues, sIssues...)

	structural := false
	for _, is := range pr.Issues {
		if is.Severity == domain.SeverityError && !softParseCodes[is.Code] {
			structural = true
		}
	}
	for _, is := range sIssues {
		if is.Severity == domain.SeverityError {
			structural = true
		}
	}
	if !structural {
		type key struct{ code, file, object string }
		parse := map[key]bool{}
		for _, is := range pr.Issues {
			if softParseCodes[is.Code] {
				parse[key{is.Code, is.File, is.Object}] = true
			}
		}
		for _, is := range r.ValidateSnapshot(pr.Snapshot, blueprint.ValidateOptions{Samples: samples, Components: components, Deadline: lim.Deadline}) {
			if parse[key{is.Code, is.File, is.Object}] {
				continue
			}
			issues = append(issues, is)
		}
	}
	for i := range issues {
		if issues[i].Line == 0 && issues[i].File != "" {
			issues[i].Line = pr.lineFor(issues[i])
		}
	}
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Severity != b.Severity {
			return a.Severity == domain.SeverityError
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	res.Issues = issues
	return res
}

// lineFor finds the source line of an issue from its object and field path.
func (pr *ParsedRepo) lineFor(is domain.Issue) int {
	pos, ok := pr.Objects[is.Object]
	if !ok || pos.File != is.File || pos.Node == nil {
		return 0
	}
	if is.Path == "" {
		return pos.Line
	}
	if l := lineOfPath(pos.Node, is.Path); l > 0 {
		return l
	}
	return pos.Line
}

// lineOfPath resolves a field path ("spec.waves[1].percent",
// "spec.parameters::gatewayEndpoint", "spec.config::receivers::otlp") in a
// document node and returns the line of the deepest resolvable element.
func lineOfPath(doc *yaml.Node, fieldPath string) int {
	cur := derefAlias(doc)
	best := 0
	step := func(key string) bool {
		if cur == nil || cur.Kind != yaml.MappingNode {
			return false
		}
		for _, e := range mappingEntries(cur) {
			if e.key.Value == key {
				best = e.key.Line
				cur = derefAlias(e.val)
				return true
			}
		}
		return false
	}
	index := func(i int) bool {
		if cur == nil || cur.Kind != yaml.SequenceNode || i < 0 || i >= len(cur.Content) {
			return false
		}
		cur = derefAlias(cur.Content[i])
		best = cur.Line
		return true
	}
	// consume "key[3][4]" / "key".
	consume := func(tok string) bool {
		name := tok
		var idx []int
		if i := strings.IndexByte(tok, '['); i >= 0 {
			name = tok[:i]
			rest := tok[i:]
			for len(rest) > 0 && rest[0] == '[' {
				j := strings.IndexByte(rest, ']')
				if j < 0 {
					return false
				}
				n, err := strconv.Atoi(rest[1:j])
				if err != nil {
					return false
				}
				idx = append(idx, n)
				rest = rest[j+1:]
			}
		}
		if name != "" && !step(name) {
			return false
		}
		for _, n := range idx {
			if !index(n) {
				return false
			}
		}
		return true
	}

	parts := strings.Split(fieldPath, "::")
	for i, part := range parts {
		if i == 0 {
			for _, tok := range strings.Split(part, ".") {
				if !consume(tok) {
					return best
				}
			}
			continue
		}
		if !consume(part) {
			return best
		}
	}
	return best
}
