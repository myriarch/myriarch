// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/myriarch/myriarch/internal/domain"
)

// section is one version block of a CHANGELOG.md with its breaking changes
// and deprecations (docs/design/opamp-server.md, Release tracker).
type section struct {
	Version      string // "0.162.0" (the token with major 0 and no pre-release)
	Breaking     []entry
	Deprecations []entry
}

// entry is one bullet.
type entry struct {
	Component string
	Text      string
	Issues    []int
}

var (
	versionTokenRE = regexp.MustCompile(`v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?`)
	// ``- `component`: note (#123, #456)``
	bulletRE = regexp.MustCompile("^- `([^`]+)`: (.*?)(?: \\((#\\d+(?:, ?#\\d+)*)\\))?\\s*$")
	issueRE  = regexp.MustCompile(`#(\d+)`)
)

// releaseVersion extracts the release version from a "## ..." heading:
// the first token with major version 0 and without pre-release suffix
// ("## v1.68.0/v0.162.0", "## v1.1.0/v0.162.0", "## v0.161.0",
// "## v0.73.0/v1.0.0-rc7"; "## v1.0.0-rcv0018/v0.89.0" -> 0.89.0).
func releaseVersion(heading string) string {
	for _, m := range versionTokenRE.FindAllStringSubmatch(heading, -1) {
		if m[1] == "0" && m[4] == "" {
			return m[1] + "." + m[2] + "." + m[3]
		}
	}
	return ""
}

type sectionKind int

const (
	sectionOther sectionKind = iota
	sectionBreaking
	sectionDeprecations
)

func classify(heading string) sectionKind {
	h := strings.ToLower(heading)
	switch {
	case strings.Contains(h, "breaking changes"):
		return sectionBreaking
	case strings.Contains(h, "deprecations"):
		return sectionDeprecations
	}
	return sectionOther
}

// parseChangelog parses a CHANGELOG.md (core or contrib) into sections.
func parseChangelog(md string) []section {
	var out []section
	var cur *section
	kind := sectionOther
	var ent *entry
	var sub []string

	flush := func() {
		if ent == nil {
			return
		}
		text := ent.Text
		for len(sub) > 0 && strings.TrimSpace(sub[len(sub)-1]) == "" {
			sub = sub[:len(sub)-1]
		}
		if len(sub) > 0 {
			text += "\n" + strings.Join(sub, "\n")
		}
		ent.Text = strings.TrimSpace(text)
		if cur != nil {
			switch kind {
			case sectionBreaking:
				cur.Breaking = append(cur.Breaking, *ent)
			case sectionDeprecations:
				cur.Deprecations = append(cur.Deprecations, *ent)
			}
		}
		ent, sub = nil, nil
	}
	startSection := func(version string) {
		flush()
		out = append(out, section{Version: version})
		cur = &out[len(out)-1]
		kind = sectionOther
	}

	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			heading := strings.TrimPrefix(line, "## ")
			if v := releaseVersion(heading); v != "" {
				startSection(v)
			} else if k := classify(heading); k != sectionOther && cur != nil {
				// legacy: "## 🛑 Breaking changes 🛑" on level 2 inside a version block
				kind = k
			} else {
				cur, kind = nil, sectionOther // block without a release version is ignored
			}
		case strings.HasPrefix(line, "### "):
			flush()
			kind = classify(strings.TrimPrefix(line, "### "))
		case strings.HasPrefix(line, "- "):
			flush()
			if cur == nil || kind == sectionOther {
				continue
			}
			ent = parseBullet(line)
		case ent != nil && strings.HasPrefix(line, "  "):
			sub = append(sub, strings.TrimPrefix(line, "  "))
		case ent != nil && line == "":
			flush()
		}
	}
	flush()
	return out
}

func parseBullet(line string) *entry {
	if m := bulletRE.FindStringSubmatch(line); m != nil {
		e := &entry{Component: m[1], Text: m[2]}
		for _, im := range issueRE.FindAllStringSubmatch(m[3], -1) {
			if n, err := strconv.Atoi(im[1]); err == nil {
				e.Issues = append(e.Issues, n)
			}
		}
		return e
	}
	return &entry{Text: strings.TrimSpace(strings.TrimPrefix(line, "- "))}
}

// toDomain converts entries of a section to ChangelogEntry values.
func toDomain(source, version string, es []entry) []domain.ChangelogEntry {
	out := make([]domain.ChangelogEntry, 0, len(es))
	for _, e := range es {
		out = append(out, domain.ChangelogEntry{
			Source: source, File: "CHANGELOG.md", Version: version, Component: e.Component, Text: e.Text, Issues: e.Issues,
		})
	}
	return out
}

// sortVersions sorts "0.162.0" strings ascending (semantic version order).
func sortVersions(vs []string) {
	sort.SliceStable(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
}

func versionLess(a, b string) bool {
	va, ea := semver.NewVersion(a)
	vb, eb := semver.NewVersion(b)
	if ea != nil || eb != nil {
		return a < b
	}
	return va.LessThan(vb)
}
