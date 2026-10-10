// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/myriarch/myriarch/internal/brand"
)

// Well-known attribute keys.
//
// The "site.*" attributes are the myriarch attribute contract: install
// scripts write them into the supervisor config under
// agent.description.non_identifying_attributes, the supervisor reports them as
// non-identifying attributes, and fleets/policies select on them.
const (
	// OpenTelemetry semantic conventions reported by the collector's opamp
	// extension (identifying) ...
	AttrServiceName       = "service.name"
	AttrServiceVersion    = "service.version"
	AttrServiceInstanceID = "service.instance.id"
	// ... and non-identifying.
	AttrOSType        = "os.type"
	AttrOSDescription = "os.description"
	AttrHostName      = "host.name"
	AttrHostArch      = "host.arch"

	// Myriarch site contract (non-identifying, set at install time).
	AttrSiteType     = "site.type"     // edge|office|datacenter|k8s|... (free text, lower-case)
	AttrSiteID       = "site.id"       // site number / site code, string ("1001")
	AttrSiteName     = "site.name"     // human readable
	AttrSiteCountry  = "site.country"  // ISO 3166-1 alpha-2 ("NL")
	AttrSiteRegion   = "site.region"   // organisation specific region
	AttrSiteTimezone = "site.timezone" // IANA time zone ("Europe/Amsterdam")
	AttrEnvironment  = "deployment.environment.name"

	// myriarch-updater (non-identifying) attributes.
	// AttrUpdaterCollectorUID links an updater to the collector agent it
	// manages: value is the supervisor's instance_uid (UUID string, read from
	// <supervisor storage>/persistent_state.yaml key "instance_id").
	AttrUpdaterCollectorUID = brand.AttrCollectorInstanceUID
	// AttrUpdaterInstallMethod is "binary" (versioned dirs + swap on a host)
	// or "container" (versions on a volume, supervisor as child process).
	AttrUpdaterInstallMethod = brand.AttrUpdaterInstallMethod
	// AttrUpdaterServerEndpoint is the scheme://host:port of the OpAMP
	// endpoint the updater is configured with ("ws://myriarchd:8080"). It
	// selects the download base URL of an offer (artifacts.agentBaseUrls).
	AttrUpdaterServerEndpoint = brand.AttrUpdaterServerEndpoint
	// AttrUpdaterImageCollectorVersion is the collector version of the
	// container image an updater in container mode runs in.
	AttrUpdaterImageCollectorVersion = brand.AttrUpdaterImageCollectorVersion
	// AttrUpdaterServiceName is the OS service name of the supervisor the
	// updater restarts ("otelcol-supervisor").
	AttrUpdaterServiceName = brand.AttrUpdaterSupervisorService

	// LabelPrefix addresses server-side labels in selectors: "labels.pilot".
	LabelPrefix = "labels."
)

// AttributeView is the flattened attribute map an agent is matched against
// (selectors) and rendered with (templates):
//
//	identifying attributes, then non-identifying attributes (identifying win on
//	key conflicts), plus server labels under "labels.<key>".
//
// Build it with NewAttributeView.
type AttributeView map[string]string

// NewAttributeView builds the view from an agent.
func NewAttributeView(a *Agent) AttributeView {
	v := make(AttributeView, len(a.IdentifyingAttributes)+len(a.NonIdentifyingAttributes)+len(a.Labels))
	// The "labels." namespace belongs to the server: attributes reported by
	// the agent with that prefix are ignored (defense in depth, the OpAMP
	// layer already drops them).
	for k, val := range a.NonIdentifyingAttributes {
		if !strings.HasPrefix(k, LabelPrefix) {
			v[k] = val
		}
	}
	for k, val := range a.IdentifyingAttributes {
		if !strings.HasPrefix(k, LabelPrefix) {
			v[k] = val
		}
	}
	for k, val := range a.Labels {
		v[LabelPrefix+k] = val
	}
	return v
}

// Get returns the value and whether the key exists.
func (v AttributeView) Get(key string) (string, bool) {
	s, ok := v[key]
	return s, ok
}

// SelectorOperator is the operator of a SelectorRequirement.
type SelectorOperator string

const (
	// OpIn: attribute exists and its value is one of Values.
	OpIn SelectorOperator = "In"
	// OpNotIn: attribute missing OR value not in Values.
	OpNotIn SelectorOperator = "NotIn"
	// OpExists: attribute exists (Values must be empty).
	OpExists SelectorOperator = "Exists"
	// OpDoesNotExist: attribute missing (Values must be empty).
	OpDoesNotExist SelectorOperator = "DoesNotExist"
	// OpGlob: attribute exists and matches one of Values as path.Match
	// pattern ("08*", "store-??").
	OpGlob SelectorOperator = "Glob"
)

// SelectorRequirement is one expression of a Selector.
type SelectorRequirement struct {
	Key      string           `json:"key" yaml:"key"`
	Operator SelectorOperator `json:"operator" yaml:"operator"`
	Values   []string         `json:"values,omitempty" yaml:"values,omitempty"`
}

// Selector selects agents by attributes. All MatchAttributes and all
// MatchExpressions must match (logical AND). An empty selector matches every
// agent. Keys refer to AttributeView keys (attributes, or "labels.<key>").
type Selector struct {
	// MatchAttributes requires exact equality for each key.
	MatchAttributes map[string]string `json:"matchAttributes,omitempty" yaml:"matchAttributes,omitempty"`
	// MatchExpressions are set-based requirements.
	MatchExpressions []SelectorRequirement `json:"matchExpressions,omitempty" yaml:"matchExpressions,omitempty"`
}

// IsEmpty reports whether the selector has no requirements (matches all).
func (s Selector) IsEmpty() bool {
	return len(s.MatchAttributes) == 0 && len(s.MatchExpressions) == 0
}

// Validate checks operators and values.
func (s Selector) Validate() error {
	for i, r := range s.MatchExpressions {
		if strings.TrimSpace(r.Key) == "" {
			return fmt.Errorf("%w: matchExpressions[%d]: key is required", ErrValidation, i)
		}
		switch r.Operator {
		case OpIn, OpNotIn:
			if len(r.Values) == 0 {
				return fmt.Errorf("%w: matchExpressions[%d]: operator %s requires values", ErrValidation, i, r.Operator)
			}
		case OpGlob:
			if len(r.Values) == 0 {
				return fmt.Errorf("%w: matchExpressions[%d]: operator Glob requires values", ErrValidation, i)
			}
			for _, p := range r.Values {
				if _, err := path.Match(p, ""); err != nil {
					return fmt.Errorf("%w: matchExpressions[%d]: invalid glob %q", ErrValidation, i, p)
				}
			}
		case OpExists, OpDoesNotExist:
			if len(r.Values) != 0 {
				return fmt.Errorf("%w: matchExpressions[%d]: operator %s must not have values", ErrValidation, i, r.Operator)
			}
		default:
			return fmt.Errorf("%w: matchExpressions[%d]: unknown operator %q", ErrValidation, i, r.Operator)
		}
	}
	return nil
}

// Matches evaluates the selector against an attribute view. Invalid
// selectors (see Validate) never match.
func (s Selector) Matches(v AttributeView) bool {
	if s.Validate() != nil {
		return false
	}
	for k, want := range s.MatchAttributes {
		got, ok := v[k]
		if !ok || got != want {
			return false
		}
	}
	for _, r := range s.MatchExpressions {
		got, ok := v[r.Key]
		switch r.Operator {
		case OpIn:
			if !ok || !contains(r.Values, got) {
				return false
			}
		case OpNotIn:
			if ok && contains(r.Values, got) {
				return false
			}
		case OpExists:
			if !ok {
				return false
			}
		case OpDoesNotExist:
			if ok {
				return false
			}
		case OpGlob:
			if !ok {
				return false
			}
			matched := false
			for _, p := range r.Values {
				if m, _ := path.Match(p, got); m {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

// String renders a compact human readable form, e.g.
// `site.type=edge, os.type In (windows), labels.pilot Exists`.
func (s Selector) String() string {
	if s.IsEmpty() {
		return "<all>"
	}
	keys := make([]string, 0, len(s.MatchAttributes))
	for k := range s.MatchAttributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+len(s.MatchExpressions))
	for _, k := range keys {
		parts = append(parts, k+"="+s.MatchAttributes[k])
	}
	for _, r := range s.MatchExpressions {
		if len(r.Values) > 0 {
			parts = append(parts, fmt.Sprintf("%s %s (%s)", r.Key, r.Operator, strings.Join(r.Values, ", ")))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s", r.Key, r.Operator))
		}
	}
	return strings.Join(parts, ", ")
}

func contains(vs []string, s string) bool {
	for _, v := range vs {
		if v == s {
			return true
		}
	}
	return false
}
