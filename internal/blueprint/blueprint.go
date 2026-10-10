// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package blueprint renders, merges and validates collector configurations
// from GitOps blueprints (semantics: docs/concepts/blueprints.md).
//
// Pure functions only (no I/O, no store, no environment), so
// they are shared by myriarchd and the myrctl CLI and give identical results.
package blueprint

import (
	"time"
	_ "time/tzdata" // maintenance window validation must not depend on the host's zoneinfo

	"github.com/myriarch/myriarch/internal/domain"
	lru "github.com/myriarch/myriarch/internal/lru"
)

// Options configure a Renderer.
type Options struct {
	// ComponentAliases maps kind ("receivers", "processors", "exporters",
	// "extensions", "connectors") -> deprecated type name -> current name,
	// e.g. "exporters" -> "otlp" -> "otlp_grpc" (while receiver "otlp" is
	// current). nil = built-in table for the supported collector versions
	// (testdata/component-aliases-0.162.0.json is the reference).
	ComponentAliases map[string]map[string]string
}

// Renderer renders blueprints. Safe for concurrent use.
type Renderer struct {
	aliases map[string]map[string]string
	// reverse maps kind -> current name -> deprecated names.
	reverse map[string]map[string][]string

	// tmplCache caches parsed templates by source text (bounded).
	tmplCache *lru.Cache[string, *tmplEntry]
	// mergeCache caches the merged (not yet template-evaluated) layer tree by
	// blueprint chain content hashes and active fragments (bounded). Values
	// are immutable.
	mergeCache *lru.Cache[string, map[string]any]
	// reCache caches compiled parameter patterns.
	reCache *lru.Cache[string, *regexpEntry]

	// renderTime is the wall clock budget of one template string (zero:
	// maxRenderTime; tests shorten it or set a negative value, which is
	// exhausted from the start).
	renderTime time.Duration
}

// timeBudget returns the wall clock budget of one template string.
func (r *Renderer) timeBudget() time.Duration {
	if r.renderTime == 0 {
		return maxRenderTime
	}
	return r.renderTime
}

// NewRenderer creates a renderer.
func NewRenderer(opts Options) *Renderer {
	aliases := opts.ComponentAliases
	if aliases == nil {
		aliases = builtinAliases
	}
	rev := make(map[string]map[string][]string, len(aliases))
	for kind, m := range aliases {
		rm := make(map[string][]string, len(m))
		for old, cur := range m {
			rm[cur] = append(rm[cur], old)
		}
		rev[kind] = rm
	}
	tc, _ := lru.New[string, *tmplEntry](8192)
	mc, _ := lru.New[string, map[string]any](256)
	rc, _ := lru.New[string, *regexpEntry](256)
	return &Renderer{aliases: aliases, reverse: rev, tmplCache: tc, mergeCache: mc, reCache: rc}
}

// AgentInput is the agent a config is rendered for.
type AgentInput struct {
	// ID is optional (nil for hypothetical agents in previews).
	ID *domain.AgentID
	// Attributes is the AttributeView incl. "labels.<key>" (inventory labels
	// already applied).
	Attributes domain.AttributeView
}

// RenderInput is one render request.
type RenderInput struct {
	Snapshot *domain.GitOpsSnapshot
	// Fleet supplies parameter values (may be nil for previews).
	Fleet *domain.Fleet
	// Blueprint name; defaults to Fleet.Blueprint.
	Blueprint string
	Agent     AgentInput
	// InventoryParameters are parameter overrides from inventory entries.
	InventoryParameters map[string]any
	// Overrides are preview-only parameter overrides (highest precedence,
	// source "override").
	Overrides map[string]any
	// Components, if non-nil, enables the component availability check of
	// ValidateConfig (unknown_component) for the rendered body. myriarchd passes
	// the agent's reported component set (when known).
	Components *domain.ComponentSet
}

// Sample is a sample agent from "<repo>/samples/agents/*.yaml" (format in
// docs/reference/gitops-schema.md, "Sample agents").
type Sample struct {
	Name string
	// File is the repository relative path, Line the line of "name:".
	File string
	Line int
	// Attributes are agent attributes (keys without "labels.").
	Attributes map[string]string
	// Labels act like enrollment-token labels: inventory labels override
	// them on equal keys.
	Labels map[string]string
}

// ValidateOptions configure ValidateSnapshot.
type ValidateOptions struct {
	// Samples are rendered with the fleet they match (inventory applied).
	Samples []Sample
	// Components, if non-nil, enables unknown_component checks for all
	// sample renders (myrctl validate --components).
	Components *domain.ComponentSet
	// Deadline (zero = none): sample renders stop when it passes and a
	// time_budget_exceeded error is reported.
	Deadline time.Time
}
