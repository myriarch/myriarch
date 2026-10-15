// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/brand"
)

// APIVersion is the apiVersion of all GitOps objects (docs/concepts/gitops.md,
// "The object envelope").
const APIVersion = brand.APIVersion

// ObjectKind is the kind of a GitOps object.
type ObjectKind string

const (
	KindFleet         ObjectKind = "Fleet"
	KindBlueprint     ObjectKind = "Blueprint"
	KindRolloutPolicy ObjectKind = "RolloutPolicy"
	KindExecutor      ObjectKind = "Executor"
	KindInventory     ObjectKind = "Inventory"
)

// AllObjectKinds lists the kinds in dependency order (used for validation).
var AllObjectKinds = []ObjectKind{KindBlueprint, KindRolloutPolicy, KindExecutor, KindInventory, KindFleet}

// ObjectMeta is the "metadata" section of a GitOps object.
type ObjectMeta struct {
	// Name is unique per kind. DNS-label like: ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// ---------------------------------------------------------------- Fleet ----

// Fleet assigns agents (by selector) to a blueprint, parameters, a target
// collector version, an update executor and rollout policies.
type Fleet struct {
	ObjectMeta
	FleetSpec
	Source SourceRef `json:"source"`
}

// FleetSpec is the "spec" of kind Fleet.
type FleetSpec struct {
	DisplayName string `json:"displayName,omitempty" yaml:"displayName,omitempty"`
	// Priority resolves overlaps: an agent belongs to the matching fleet with
	// the highest priority. Two matching fleets with the same highest
	// priority make the agent "unassigned" (assignment error "ambiguous").
	Priority int `json:"priority" yaml:"priority"`
	// Selector on the agent AttributeView. Empty selector = all agents.
	Selector Selector `json:"selector" yaml:"selector"`
	// Blueprint is the name of the Blueprint to render.
	Blueprint string `json:"blueprint" yaml:"blueprint"`
	// Parameters are values for the blueprint parameters (override defaults).
	Parameters map[string]any `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	// Collector describes the desired collector distribution/version.
	Collector CollectorSpec `json:"collector" yaml:"collector"`
	// Update selects how version updates are executed.
	Update UpdateSpec `json:"update" yaml:"update"`
	// Rollout references the rollout policies for config and version changes.
	Rollout FleetRolloutSpec `json:"rollout" yaml:"rollout"`
	// Bridge controls whether the fleet configures OpAMP Bridge agents that its
	// selector matches. The block is omitted from the serialized spec while it
	// is unset, so existing fleets keep their content hash.
	Bridge BridgeSpec `json:"bridge,omitzero" yaml:"bridge,omitempty"`
}

// CollectorSpec is the desired collector of a fleet.
type CollectorSpec struct {
	// Distribution, default "otelcol-contrib".
	Distribution string `json:"distribution,omitempty" yaml:"distribution,omitempty"`
	// Version is the target version without "v" ("0.162.0"). Empty = versions
	// are not managed for this fleet.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
}

// ExecutorType selects the update executor.
type ExecutorType string

const (
	// ExecutorNone: versions are only reported, never changed.
	ExecutorNone ExecutorType = "none"
	// ExecutorUpdater: OpAMP PackagesAvailable offers to myriarch-updater.
	ExecutorUpdater ExecutorType = "updater"
	// ExecutorWebhook: generic HTTP webhook with HMAC signature.
	ExecutorWebhook ExecutorType = "webhook"
	// ExecutorAWX: Ansible AWX/AAP job template launch.
	ExecutorAWX ExecutorType = "awx"
	// ExecutorManual: version rollouts are created and wait for the reported
	// version without any external call (the version is changed outside of
	// myriarchd, e.g. by an image tag merge request).
	ExecutorManual ExecutorType = "manual"
)

// UpdateSpec configures version updates of a fleet.
type UpdateSpec struct {
	// Executor type; default "none".
	Executor ExecutorType `json:"executor,omitempty" yaml:"executor,omitempty"`
	// ExecutorRef names an Executor object; required for webhook and awx.
	ExecutorRef string `json:"executorRef,omitempty" yaml:"executorRef,omitempty"`
	// Params are passed to the executor (e.g. {"pipeline": "otel-k8s"}).
	Params map[string]any `json:"params,omitempty" yaml:"params,omitempty"`
	// Selection is the frame for UI/API-selected target versions (nil = not
	// allowed, same as enabled: false).
	Selection *VersionSelectionSpec `json:"selection,omitempty" yaml:"selection,omitempty"`
}

// FleetRolloutSpec references rollout policies.
type FleetRolloutSpec struct {
	// ConfigPolicy for config changes; default "default".
	ConfigPolicy string `json:"configPolicy,omitempty" yaml:"configPolicy,omitempty"`
	// VersionPolicy for version changes; default "default".
	VersionPolicy string `json:"versionPolicy,omitempty" yaml:"versionPolicy,omitempty"`
}

// BridgeSpec is spec.bridge of a Fleet: how the fleet treats the OpAMP Bridge
// agents of the OpenTelemetry Operator that its selector matches
// (docs/concepts/fleets.md, "Bridge agents").
type BridgeSpec struct {
	// Manage opts the fleet in to configure the Bridge agents that its selector
	// matches (default false). Without it a Bridge never receives remote
	// configuration from the fleet. The opt-in takes effect for Bridges in
	// standalone mode only: a Bridge in operator mode stays locked, because a
	// message that lacks the key of a managed collector deletes that resource.
	Manage bool `json:"manage" yaml:"manage,omitempty"`
}

// ------------------------------------------------------------ Blueprint ----

// Blueprint is a layered, parameterized collector configuration template.
type Blueprint struct {
	ObjectMeta
	BlueprintSpec
	Source SourceRef `json:"source"`
}

// BlueprintSpec is the "spec" of kind Blueprint. Rendering semantics are
// specified in docs/concepts/blueprints.md ("Rendering").
type BlueprintSpec struct {
	// Extends names the parent blueprint (single inheritance, no cycles).
	Extends string `json:"extends,omitempty" yaml:"extends,omitempty"`
	// Parameters declares typed parameters. A child may redeclare a parent
	// parameter only with the same type (to change default/description).
	Parameters []ParameterSpec `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	// Config is a collector configuration tree; string values may contain
	// template expressions "[[ ... ]]".
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
	// Fragments are conditional config overlays merged after Config.
	Fragments []Fragment `json:"fragments,omitempty" yaml:"fragments,omitempty"`
	// Remove lists confmap paths ("receivers::host_metrics") deleted after
	// merging this layer (Config and Fragments).
	Remove []string `json:"remove,omitempty" yaml:"remove,omitempty"`
}

// ParameterType is the type of a blueprint parameter.
type ParameterType string

const (
	ParamString   ParameterType = "string"
	ParamInt      ParameterType = "int"
	ParamNumber   ParameterType = "number"
	ParamBool     ParameterType = "bool"
	ParamDuration ParameterType = "duration" // Go duration string, rendered as string
	ParamList     ParameterType = "list"     // list of any
	ParamMap      ParameterType = "map"      // map[string]any
)

// ParameterSpec declares a blueprint parameter.
type ParameterSpec struct {
	Name        string        `json:"name" yaml:"name"`
	Type        ParameterType `json:"type" yaml:"type"`
	Description string        `json:"description,omitempty" yaml:"description,omitempty"`
	Default     any           `json:"default,omitempty" yaml:"default,omitempty"`
	// Required: rendering fails if no value results from the resolution
	// chain (default < fleet < attribute < inventory).
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`
	// Enum restricts allowed values.
	Enum []any `json:"enum,omitempty" yaml:"enum,omitempty"`
	// Pattern is an RE2 regexp the (string) value must fully match.
	Pattern string `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	// Min/Max bound int/number values (inclusive).
	Min *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	// FromAttribute takes the value from the agent AttributeView key if
	// present (converted to Type).
	FromAttribute string `json:"fromAttribute,omitempty" yaml:"fromAttribute,omitempty"`
}

// Fragment is a conditional overlay inside a blueprint.
type Fragment struct {
	Name string `json:"name" yaml:"name"`
	// When is a template expression that must evaluate to a bool, e.g.
	// `[[ eq (attr "os.type") "windows" ]]`. Empty = always.
	When   string         `json:"when,omitempty" yaml:"when,omitempty"`
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
	Remove []string       `json:"remove,omitempty" yaml:"remove,omitempty"`
}

// -------------------------------------------------------- RolloutPolicy ----

// RolloutPolicy controls how changes are rolled out to the agents of a fleet.
type RolloutPolicy struct {
	ObjectMeta
	RolloutPolicySpec
	Source SourceRef `json:"source"`
}

// FailureAction is what happens when a wave exceeds its failure threshold.
type FailureAction string

const (
	FailurePause    FailureAction = "pause"
	FailureRollback FailureAction = "rollback"
)

// RolloutPolicySpec is the "spec" of kind RolloutPolicy. Engine semantics
// are specified in docs/concepts/rollouts.md ("Which policy a fleet uses" and the
// sections that follow).
type RolloutPolicySpec struct {
	// Waves in order. If the last wave does not reach 100 percent an implicit
	// final wave "rest" (100%) is appended.
	Waves []WaveSpec `json:"waves" yaml:"waves"`
	// MaxConcurrent caps targets in progress at the same time across the
	// rollout (0 = unlimited).
	MaxConcurrent int `json:"maxConcurrent,omitempty" yaml:"maxConcurrent,omitempty"`
	// HealthWait: a target must stay online and healthy this long after
	// reporting success before it counts as succeeded. Default 60s.
	HealthWait Duration `json:"healthWait,omitempty" yaml:"healthWait,omitempty"`
	// ApplyTimeout: max time from start until success is reported. Default
	// 10m (config) / 30m (version).
	ApplyTimeout Duration `json:"applyTimeout,omitempty" yaml:"applyTimeout,omitempty"`
	// OfflineGracePeriod: how long a wave waits for offline targets before
	// marking them "skipped". Default 15m.
	OfflineGracePeriod Duration `json:"offlineGracePeriod,omitempty" yaml:"offlineGracePeriod,omitempty"`
	// FailureThreshold per wave.
	FailureThreshold FailureThreshold `json:"failureThreshold" yaml:"failureThreshold"`
	// OnFailure action when the threshold is reached. Default "pause".
	OnFailure FailureAction `json:"onFailure,omitempty" yaml:"onFailure,omitempty"`
	// MaintenanceWindows restrict when targets may START. Empty = any time.
	MaintenanceWindows []MaintenanceWindow `json:"maintenanceWindows,omitempty" yaml:"maintenanceWindows,omitempty"`
	// TimezoneAttribute is the agent attribute holding the IANA time zone
	// used for windows with timezone "agent". Default "site.timezone".
	TimezoneAttribute string `json:"timezoneAttribute,omitempty" yaml:"timezoneAttribute,omitempty"`
	// DefaultTimezone is used when the agent has no (valid) timezone
	// attribute. Default: myriarchd config rollouts.defaultTimezone.
	DefaultTimezone string `json:"defaultTimezone,omitempty" yaml:"defaultTimezone,omitempty"`
}

// WaveSpec defines one wave. Exactly one of Selector or Percent is set.
type WaveSpec struct {
	Name string `json:"name" yaml:"name"`
	// Selector: the wave contains all remaining targets matching it
	// (explicit canary, e.g. labels.pilot=true).
	Selector *Selector `json:"selector,omitempty" yaml:"selector,omitempty"`
	// Percent (1..100): the wave grows the cumulative share of all targets
	// to this percentage (ceil). Remaining targets are ordered by
	// sha256(rolloutID||agentID) for a stable pseudo-random spread.
	Percent int `json:"percent,omitempty" yaml:"percent,omitempty"`
	// RequireApproval: the wave does not start before an operator approves.
	RequireApproval bool `json:"requireApproval,omitempty" yaml:"requireApproval,omitempty"`
}

// FailureThreshold triggers OnFailure for a wave when failed targets reach
// MaxFailed (absolute, default 1) OR, if MaxFailedPercent > 0, when
// failed*100 >= MaxFailedPercent*waveTargets.
type FailureThreshold struct {
	MaxFailed        int `json:"maxFailed,omitempty" yaml:"maxFailed,omitempty"`
	MaxFailedPercent int `json:"maxFailedPercent,omitempty" yaml:"maxFailedPercent,omitempty"`
}

// MaintenanceWindow is a recurring local-time window.
type MaintenanceWindow struct {
	// Days: Mon Tue Wed Thu Fri Sat Sun. Empty = every day. The day refers to
	// the day the window STARTS.
	Days []string `json:"days,omitempty" yaml:"days,omitempty"`
	// Start "HH:MM" (24h, local time).
	Start string `json:"start" yaml:"start"`
	// End "HH:MM". End <= Start means the window crosses midnight.
	End string `json:"end" yaml:"end"`
	// Timezone: "agent" (default) or an IANA zone name ("Europe/Brussels").
	Timezone string `json:"timezone,omitempty" yaml:"timezone,omitempty"`
}

// DefaultApplyTimeout returns the apply timeout used when a policy does not
// set one: 10m for config rollouts, 30m for version rollouts.
func DefaultApplyTimeout(kind RolloutKind) time.Duration {
	if kind == RolloutKindVersion {
		return 30 * time.Minute
	}
	return 10 * time.Minute
}

// DefaultRolloutPolicyName is used when a fleet references no policy. If no
// RolloutPolicy object with this name exists, DefaultRolloutPolicy() is used.
const DefaultRolloutPolicyName = "default"

// DefaultRolloutPolicy returns the built-in policy: one wave of 100%, at most
// 50 concurrent targets, 60s health wait, pause on the first failure.
// ApplyTimeout is left 0, which means the kind-specific default
// (DefaultApplyTimeout).
func DefaultRolloutPolicy() RolloutPolicy {
	return RolloutPolicy{
		ObjectMeta: ObjectMeta{Name: DefaultRolloutPolicyName, Description: "built-in default policy"},
		RolloutPolicySpec: RolloutPolicySpec{
			Waves:              []WaveSpec{{Name: "all", Percent: 100}},
			MaxConcurrent:      50,
			HealthWait:         Duration(60 * time.Second),
			OfflineGracePeriod: Duration(15 * time.Minute),
			FailureThreshold:   FailureThreshold{MaxFailed: 1},
			OnFailure:          FailurePause,
			TimezoneAttribute:  AttrSiteTimezone,
		},
	}
}

// ------------------------------------------------------------- Executor ----

// Executor configures an external update executor (webhook / AWX). Secrets
// are never stored in Git: they are referenced by environment variable or
// file name on the myriarchd side.
type Executor struct {
	ObjectMeta
	ExecutorSpec
	Source SourceRef `json:"source"`
}

// ExecutorSpec is the "spec" of kind Executor.
type ExecutorSpec struct {
	Type    ExecutorType         `json:"type" yaml:"type"` // webhook|awx
	Webhook *WebhookExecutorSpec `json:"webhook,omitempty" yaml:"webhook,omitempty"`
	AWX     *AWXExecutorSpec     `json:"awx,omitempty" yaml:"awx,omitempty"`
}

// WebhookExecutorSpec configures the generic webhook executor.
type WebhookExecutorSpec struct {
	URL     string            `json:"url" yaml:"url"`
	Method  string            `json:"method,omitempty" yaml:"method,omitempty"` // default POST
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	// SecretEnv / SecretFile name the HMAC-SHA256 secret on the myriarchd host.
	SecretEnv  string   `json:"secretEnv,omitempty" yaml:"secretEnv,omitempty"`
	SecretFile string   `json:"secretFile,omitempty" yaml:"secretFile,omitempty"`
	Timeout    Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"` // default 30s
	// BatchSize: max targets per call (default 100).
	BatchSize int `json:"batchSize,omitempty" yaml:"batchSize,omitempty"`
}

// AWXExecutorSpec configures the Ansible AWX / Automation Platform executor.
type AWXExecutorSpec struct {
	// URL is the AWX base URL, e.g. https://awx.example.com
	URL           string `json:"url" yaml:"url"`
	JobTemplateID int    `json:"jobTemplateId" yaml:"jobTemplateId"`
	// APIPrefix is the path below URL under which the controller API lives.
	// Default "/api/v2/"; Ansible Automation Platform 2.5 and later serves the
	// controller below "/api/controller/v2/".
	APIPrefix string `json:"apiPrefix,omitempty" yaml:"apiPrefix,omitempty"`
	// TokenEnv / TokenFile name the OAuth2 bearer token on the myriarchd host.
	TokenEnv  string `json:"tokenEnv,omitempty" yaml:"tokenEnv,omitempty"`
	TokenFile string `json:"tokenFile,omitempty" yaml:"tokenFile,omitempty"`
	// LimitAttribute: agent attribute used to build the AWX "limit"
	// (comma separated host list). Default "host.name".
	LimitAttribute string `json:"limitAttribute,omitempty" yaml:"limitAttribute,omitempty"`
	// ExtraVars are passed to the job; string values may use the executor
	// template context (docs/concepts/executors.md, "AWX").
	ExtraVars map[string]any `json:"extraVars,omitempty" yaml:"extraVars,omitempty"`
	CAFile    string         `json:"caFile,omitempty" yaml:"caFile,omitempty"`
	Timeout   Duration       `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	BatchSize int            `json:"batchSize,omitempty" yaml:"batchSize,omitempty"`
}

// DefaultAWXAPIPrefix is the controller API prefix used when apiPrefix is empty.
const DefaultAWXAPIPrefix = "/api/v2/"

// EffectiveAPIPrefix returns the API prefix with exactly one trailing slash,
// or DefaultAWXAPIPrefix when none is configured.
func (a *AWXExecutorSpec) EffectiveAPIPrefix() string {
	if a == nil {
		return DefaultAWXAPIPrefix
	}
	p := strings.TrimRight(a.APIPrefix, "/")
	if p == "" {
		return DefaultAWXAPIPrefix
	}
	return p + "/"
}

// ------------------------------------------------------------ Inventory ----

// Inventory adds server-side labels and parameter overrides to agents
// matched by exact attribute values.
type Inventory struct {
	ObjectMeta
	InventorySpec
	Source SourceRef `json:"source"`
}

// InventorySpec is the "spec" of kind Inventory.
type InventorySpec struct {
	Entries []InventoryEntry `json:"entries" yaml:"entries"`
}

// InventoryEntry applies to agents whose AttributeView contains all Match
// key/values. Entries are applied in order (Inventory objects sorted by
// name, entries in file order); later entries override earlier ones.
type InventoryEntry struct {
	Match      map[string]string `json:"match" yaml:"match"`
	Labels     map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Parameters map[string]any    `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

// ------------------------------------------------------------- Snapshot ----

// GitOpsSnapshot is the complete, validated set of declarative objects of one
// GitOps revision. The syncer replaces the stored snapshot atomically.
type GitOpsSnapshot struct {
	Commit          string          `json:"commit"`
	Fleets          []Fleet         `json:"fleets"`
	Blueprints      []Blueprint     `json:"blueprints"`
	RolloutPolicies []RolloutPolicy `json:"rolloutPolicies"`
	Executors       []Executor      `json:"executors"`
	Inventories     []Inventory     `json:"inventories"`
}
