// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

// Identity of the OpAMP Bridge of the OpenTelemetry Operator, as the Bridge
// reports it (docs/design/upstream-notes.md, "OpAMP Bridge: identity, modes and
// remote config"). Both values are reported by the agent itself, so recognising
// a Bridge by them is a safety measure and no authentication: an agent that
// reports one of them is treated as a Bridge (fail safe), an agent that reports
// neither is not (docs/adr/0037-opamp-bridge-agents-until-the-operator-integration.md).
const (
	// BridgeServiceName is the service.name of a Bridge in operator mode. A
	// Bridge in standalone mode reports the name of its workload instead.
	BridgeServiceName = "io.opentelemetry.operator-opamp-bridge"
	// AttrBridgeMode is the non-identifying attribute that names the mode of a
	// Bridge. Both modes report it since Operator 0.157.0; the Bridge writes it
	// after the configured attributes, so configuration cannot override it.
	AttrBridgeMode = "opentelemetry.io/opamp.bridge.mode"
)

// BridgeMode is the operating mode of an OpAMP Bridge.
type BridgeMode string

const (
	// BridgeModeOperator manages OpenTelemetryCollector resources: one OpAMP
	// client for the whole Bridge, keys of the form "<namespace>/<name>", and a
	// message that lacks a key deletes the resource. No fleet configures such a
	// Bridge, with or without spec.bridge.manage.
	BridgeModeOperator BridgeMode = "operator"
	// BridgeModeStandalone manages ConfigMaps of configured workloads: one OpAMP
	// client per workload. A fleet with spec.bridge.manage configures such a
	// Bridge.
	BridgeModeStandalone BridgeMode = "standalone"
	// BridgeModeUnknown is a Bridge that reports no known mode: a release before
	// 0.157.0, or a mode that this release does not know. It is never managed.
	BridgeModeUnknown BridgeMode = ""
)

// IsOpAMPBridge reports whether attrs, the attributes an agent reported (the
// identifying and the non-identifying ones in one map), describe an OpAMP
// Bridge of the OpenTelemetry Operator: the service name of operator mode, or
// the mode attribute with any value (a value that this release does not know
// still marks a Bridge).
func IsOpAMPBridge(attrs map[string]string) bool {
	if attrs[AttrServiceName] == BridgeServiceName {
		return true
	}
	_, ok := attrs[AttrBridgeMode]
	return ok
}

// BridgeModeOf returns the mode a Bridge reports in attrs, or BridgeModeUnknown
// when the attribute is missing or has a value this release does not know.
func BridgeModeOf(attrs map[string]string) BridgeMode {
	switch m := BridgeMode(attrs[AttrBridgeMode]); m {
	case BridgeModeOperator, BridgeModeStandalone:
		return m
	}
	return BridgeModeUnknown
}

// Reasons that the server gives when it holds back an action from a Bridge. They
// appear in events, audit entries and error messages.
const (
	// BridgeRestartRefused is the reason of the refused restart command: in
	// operator mode the command restarts the workloads of every collector
	// resource that the Bridge lists, and Kubernetes is the control for
	// restarts.
	BridgeRestartRefused = "restart commands are not sent to OpAMP Bridge agents; restart the workloads through Kubernetes"
	// BridgeVersionRefused is the reason that a Bridge is not a target of a
	// version rollout.
	BridgeVersionRefused = "OpAMP Bridge agents are not part of version rollouts; change the version through spec.image of the collector resources or a gitops-pr"
)

// BridgeModeOfAgent returns the mode that the agent reports as a Bridge.
func BridgeModeOfAgent(a *Agent) BridgeMode {
	if a == nil {
		return BridgeModeUnknown
	}
	if _, ok := a.IdentifyingAttributes[AttrBridgeMode]; ok {
		return BridgeModeOf(a.IdentifyingAttributes)
	}
	return BridgeModeOf(a.NonIdentifyingAttributes)
}

// BridgeConfigLock explains why a fleet may not send remote configuration to
// the Bridge a, or returns "" when it may. f is the fleet the Bridge is
// assigned to, or nil. Only a fleet with spec.bridge.manage configures a
// Bridge, and only one in standalone mode: a Bridge in operator mode or in an
// unknown mode stays locked with the opt-in as well (ADR-0037).
func BridgeConfigLock(a *Agent, f *Fleet) string {
	switch {
	case f == nil:
		return "the agent is an OpAMP Bridge and no fleet with spec.bridge.manage configures it"
	case !f.Bridge.Manage:
		return "the agent is an OpAMP Bridge and fleet " + f.Name + " does not set spec.bridge.manage"
	}
	switch m := BridgeModeOfAgent(a); m {
	case BridgeModeStandalone:
		return ""
	case BridgeModeOperator:
		return "the agent is an OpAMP Bridge in operator mode, which no fleet configures: a message that lacks the key of a collector resource deletes the resource"
	default:
		return "the agent is an OpAMP Bridge in a mode that this release does not know, which no fleet configures"
	}
}
