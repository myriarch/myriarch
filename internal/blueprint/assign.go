// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// InventoryResult is the outcome of applying inventory entries to an agent.
type InventoryResult struct {
	// Labels are server-side labels added by matching entries (without the
	// "labels." prefix).
	Labels map[string]string
	// Parameters are parameter overrides of matching entries.
	Parameters map[string]any
}

// ApplyInventory applies all inventory entries whose match is satisfied by
// attrs (docs/concepts/inventory.md): Inventory objects sorted by name,
// entries in file order, later matches override earlier ones per key. attrs
// is the attribute view WITHOUT "labels.*" keys (they are ignored if given).
func ApplyInventory(snap *domain.GitOpsSnapshot, attrs map[string]string) InventoryResult {
	res := InventoryResult{Labels: map[string]string{}, Parameters: map[string]any{}}
	if snap == nil || len(snap.Inventories) == 0 {
		return res
	}
	invs := make([]*domain.Inventory, len(snap.Inventories))
	for i := range snap.Inventories {
		invs[i] = &snap.Inventories[i]
	}
	sort.SliceStable(invs, func(i, j int) bool { return invs[i].Name < invs[j].Name })
	for _, inv := range invs {
		for _, e := range inv.Entries {
			if len(e.Match) == 0 || !entryMatches(e.Match, attrs) {
				continue
			}
			for k, v := range e.Labels {
				res.Labels[k] = v
			}
			for k, v := range e.Parameters {
				res.Parameters[k] = normalizeTree(v)
			}
		}
	}
	return res
}

func entryMatches(match map[string]string, attrs map[string]string) bool {
	for k, want := range match {
		if strings.HasPrefix(k, domain.LabelPrefix) {
			return false // match only sees agent attributes
		}
		if got, ok := attrs[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// ViewFrom builds an attribute view from agent attributes and server labels.
func ViewFrom(attrs, labels map[string]string) domain.AttributeView {
	v := make(domain.AttributeView, len(attrs)+len(labels))
	for k, val := range attrs {
		if !strings.HasPrefix(k, domain.LabelPrefix) { // reserved for server labels
			v[k] = val
		}
	}
	for k, val := range labels {
		v[domain.LabelPrefix+k] = val
	}
	return v
}

// SplitView separates a view into plain attributes and labels.
func SplitView(view map[string]string) (attrs, labels map[string]string) {
	attrs, labels = map[string]string{}, map[string]string{}
	for k, v := range view {
		if strings.HasPrefix(k, domain.LabelPrefix) {
			labels[strings.TrimPrefix(k, domain.LabelPrefix)] = v
		} else {
			attrs[k] = v
		}
	}
	return attrs, labels
}

// FleetMatch is the outcome of fleet matching for one attribute view.
type FleetMatch struct {
	// Fleet is the winning fleet (nil if unassigned or ambiguous).
	Fleet *domain.Fleet
	// Candidates are all matching fleet names by priority (desc), then name.
	Candidates []string
	// Error is set when the assignment is ambiguous.
	Error string
}

// MatchFleet evaluates fleet selectors and priorities (docs/concepts/fleets.md,
// "Assignment and priority"): the highest priority wins; equal highest priorities make the agent
// unassigned with an "ambiguous" error.
func MatchFleet(snap *domain.GitOpsSnapshot, view domain.AttributeView) FleetMatch {
	var cands []*domain.Fleet
	if snap != nil {
		for i := range snap.Fleets {
			f := &snap.Fleets[i]
			if f.Selector.Matches(view) {
				cands = append(cands, f)
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Priority != cands[j].Priority {
			return cands[i].Priority > cands[j].Priority
		}
		return cands[i].Name < cands[j].Name
	})
	m := FleetMatch{Candidates: make([]string, len(cands))}
	for i, f := range cands {
		m.Candidates[i] = f.Name
	}
	switch {
	case len(cands) == 0:
	case len(cands) > 1 && cands[0].Priority == cands[1].Priority:
		var tied []string
		for _, f := range cands {
			if f.Priority == cands[0].Priority {
				tied = append(tied, f.Name)
			}
		}
		m.Error = fmt.Sprintf("ambiguous: fleets %s have equal priority %d", strings.Join(tied, ", "), cands[0].Priority)
	default:
		m.Fleet = cands[0]
	}
	return m
}

// ---------------------------------------------------------- window helpers --

// ParseClock parses "HH:MM" (24h) into minutes since midnight.
func ParseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h, ok1 := twoDigits(s[0:2])
	m, ok2 := twoDigits(s[3:5])
	if !ok1 || !ok2 || h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func twoDigits(s string) (int, bool) {
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}

var weekdayNames = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

// ParseWeekday parses Mon..Sun (case-insensitive).
func ParseWeekday(s string) (time.Weekday, bool) {
	d, ok := weekdayNames[strings.ToLower(strings.TrimSpace(s))]
	return d, ok
}

// Chain resolves the blueprint chain of a blueprint (root first). Problems
// (missing blueprint, extends cycle) are returned as issues.
func Chain(snap *domain.GitOpsSnapshot, name string) ([]*domain.Blueprint, domain.Issues) {
	if snap == nil {
		return nil, domain.Issues{{Severity: domain.SeverityError, Code: "blueprint_not_found", Message: fmt.Sprintf("blueprint %q does not exist", name)}}
	}
	return resolveChain(snap, name, "")
}

// MergedParameters returns the effective parameter declarations of a
// blueprint chain (docs/concepts/blueprints.md, "Parameters": redeclarations replace the
// parent's, order of first declaration). Type conflicts are reported as
// issues; the conflicting redeclaration is ignored.
func MergedParameters(chain []*domain.Blueprint) ([]domain.ParameterSpec, domain.Issues) {
	decls, issues := declareParams(chain)
	out := make([]domain.ParameterSpec, 0, len(decls.order))
	for _, n := range decls.order {
		out = append(out, decls.byName[n])
	}
	return out, issues
}
