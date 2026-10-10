// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// RenderedConfig is a rendered (desired) collector configuration, stored
// content-addressed by Hash = ConfigHash(Body). Many agents share the same
// rendered config, so it is stored once.
type RenderedConfig struct {
	Hash        Hash   `json:"hash"`
	Body        []byte `json:"-"`
	ContentType string `json:"contentType"`
	// Provenance of the first render that produced this hash (informational).
	FleetName     string    `json:"fleetName,omitempty"`
	BlueprintName string    `json:"blueprintName,omitempty"`
	GitCommit     string    `json:"gitCommit,omitempty"`
	Size          int       `json:"size"`
	CreatedAt     time.Time `json:"createdAt"`
}

// EffectiveConfig is a config body reported by an agent (OpAMP
// EffectiveConfig, config map key ""), stored content-addressed by
// SHA256(Body). NOTE: the collector reports the fully expanded config
// (${env:...} resolved; configopaque values shown as "[REDACTED]") including
// default values, so it is NOT byte-comparable with the rendered config.
type EffectiveConfig struct {
	Hash        Hash      `json:"hash"`
	Body        []byte    `json:"-"`
	ContentType string    `json:"contentType"`
	CreatedAt   time.Time `json:"createdAt"`
}

// RenderResult is the outcome of rendering a blueprint for one agent.
type RenderResult struct {
	Fleet     string `json:"fleet,omitempty"`
	Blueprint string `json:"blueprint"`
	// Layers lists the blueprint chain root-first ("base", "edge-windows")
	// and applied fragments ("edge-windows#iis").
	Layers []string `json:"layers"`
	// Parameters are the fully resolved parameter values.
	Parameters map[string]any `json:"parameters"`
	// ParameterSources maps parameter -> "default"|"fleet"|"attribute"|
	// "inventory"|"override" (override = preview-only overrides).
	ParameterSources map[string]string `json:"parameterSources"`
	// Body is the canonical YAML (see docs/concepts/blueprints.md, "Canonical form and hash").
	Body []byte `json:"-"`
	Hash Hash   `json:"hash"`
	// Issues is never nil (empty list when the render is clean).
	Issues Issues `json:"issues"`
}

// OpAMP AgentCapabilities bits (mirrors protobufs.AgentCapabilities so that
// packages without an opamp-go dependency can decode the bitmask).
const (
	CapReportsStatus                  uint64 = 0x1
	CapAcceptsRemoteConfig            uint64 = 0x2
	CapReportsEffectiveConfig         uint64 = 0x4
	CapAcceptsPackages                uint64 = 0x8
	CapReportsPackageStatuses         uint64 = 0x10
	CapReportsOwnTraces               uint64 = 0x20
	CapReportsOwnMetrics              uint64 = 0x40
	CapReportsOwnLogs                 uint64 = 0x80
	CapAcceptsOpAMPConnectionSettings uint64 = 0x100
	CapAcceptsOtherConnectionSettings uint64 = 0x200
	CapAcceptsRestartCommand          uint64 = 0x400
	CapReportsHealth                  uint64 = 0x800
	CapReportsRemoteConfig            uint64 = 0x1000
	CapReportsHeartbeat               uint64 = 0x2000
	CapReportsAvailableComponents     uint64 = 0x4000
	CapReportsConnectionSettingsStat  uint64 = 0x8000
)

var capabilityNames = []struct {
	bit  uint64
	name string
}{
	{CapReportsStatus, "ReportsStatus"},
	{CapAcceptsRemoteConfig, "AcceptsRemoteConfig"},
	{CapReportsEffectiveConfig, "ReportsEffectiveConfig"},
	{CapAcceptsPackages, "AcceptsPackages"},
	{CapReportsPackageStatuses, "ReportsPackageStatuses"},
	{CapReportsOwnTraces, "ReportsOwnTraces"},
	{CapReportsOwnMetrics, "ReportsOwnMetrics"},
	{CapReportsOwnLogs, "ReportsOwnLogs"},
	{CapAcceptsOpAMPConnectionSettings, "AcceptsOpAMPConnectionSettings"},
	{CapAcceptsOtherConnectionSettings, "AcceptsOtherConnectionSettings"},
	{CapAcceptsRestartCommand, "AcceptsRestartCommand"},
	{CapReportsHealth, "ReportsHealth"},
	{CapReportsRemoteConfig, "ReportsRemoteConfig"},
	{CapReportsHeartbeat, "ReportsHeartbeat"},
	{CapReportsAvailableComponents, "ReportsAvailableComponents"},
	{CapReportsConnectionSettingsStat, "ReportsConnectionSettingsStatus"},
}

// CapabilityNames decodes an AgentCapabilities bitmask into enum names
// (without the "AgentCapabilities_" prefix), in bit order.
func CapabilityNames(bits uint64) []string {
	out := make([]string, 0, 8)
	for _, c := range capabilityNames {
		if bits&c.bit != 0 {
			out = append(out, c.name)
		}
	}
	return out
}

// HasCapability reports whether bits contains capability c.
func HasCapability(bits, c uint64) bool { return bits&c != 0 }
