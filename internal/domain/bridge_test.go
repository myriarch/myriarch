// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// The values are the ones the Bridge reports (docs/design/upstream-notes.md); a
// change of a constant would stop the recognition of every Bridge.
func TestBridgeConstants(t *testing.T) {
	if BridgeServiceName != "io.opentelemetry.operator-opamp-bridge" {
		t.Errorf("BridgeServiceName = %q", BridgeServiceName)
	}
	if AttrBridgeMode != "opentelemetry.io/opamp.bridge.mode" {
		t.Errorf("AttrBridgeMode = %q", AttrBridgeMode)
	}
	if BridgeModeOperator != "operator" || BridgeModeStandalone != "standalone" || BridgeModeUnknown != "" {
		t.Errorf("modes: %q %q %q", BridgeModeOperator, BridgeModeStandalone, BridgeModeUnknown)
	}
}

func TestIsOpAMPBridge(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]string
		want  bool
		mode  BridgeMode
	}{
		{"nil", nil, false, BridgeModeUnknown},
		{"a collector", map[string]string{AttrServiceName: "otelcol-contrib", "os.type": "linux"}, false, BridgeModeUnknown},
		{"an updater", map[string]string{AttrServiceName: UpdaterServiceName}, false, BridgeModeUnknown},
		{"the service name alone (a release before 0.157.0)", map[string]string{AttrServiceName: BridgeServiceName}, true, BridgeModeUnknown},
		{"the service name and operator mode", map[string]string{AttrServiceName: BridgeServiceName, AttrBridgeMode: "operator"}, true, BridgeModeOperator},
		{"standalone mode with the name of the workload", map[string]string{AttrServiceName: "payments-collector", AttrBridgeMode: "standalone"}, true, BridgeModeStandalone},
		{"standalone mode with the service name of operator mode", map[string]string{AttrServiceName: BridgeServiceName, AttrBridgeMode: "standalone"}, true, BridgeModeStandalone},
		{"a collector that reports the mode attribute", map[string]string{AttrServiceName: "otelcol-contrib", AttrBridgeMode: "standalone"}, true, BridgeModeStandalone},
		{"a mode this release does not know", map[string]string{AttrBridgeMode: "cluster"}, true, BridgeModeUnknown},
		{"an empty mode", map[string]string{AttrBridgeMode: ""}, true, BridgeModeUnknown},
		{"another case is another value", map[string]string{AttrBridgeMode: "Standalone"}, true, BridgeModeUnknown},
		{"a service name that only contains the name", map[string]string{AttrServiceName: BridgeServiceName + "-proxy"}, false, BridgeModeUnknown},
		{"the mode as a server label is no mode attribute", map[string]string{"labels." + AttrBridgeMode: "standalone"}, false, BridgeModeUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsOpAMPBridge(c.attrs); got != c.want {
				t.Errorf("IsOpAMPBridge = %v, want %v", got, c.want)
			}
			if got := BridgeModeOf(c.attrs); got != c.mode {
				t.Errorf("BridgeModeOf = %q, want %q", got, c.mode)
			}
		})
	}
}

// An unset spec.bridge is not serialized, so the content hash of an existing
// fleet (the SHA-256 of the JSON of its spec) does not change when the field
// exists; the opt-in is part of the JSON.
func TestFleetSpecSerializesBridgeOnlyWhenSet(t *testing.T) {
	plain, err := json.Marshal(FleetSpec{Blueprint: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "bridge") {
		t.Errorf("an unset bridge block is serialized: %s", plain)
	}
	explicit, err := json.Marshal(FleetSpec{Blueprint: "b", Bridge: BridgeSpec{Manage: false}})
	if err != nil {
		t.Fatal(err)
	}
	if string(explicit) != string(plain) {
		t.Errorf("manage: false changes the serialization: %s", explicit)
	}
	opted, err := json.Marshal(FleetSpec{Blueprint: "b", Bridge: BridgeSpec{Manage: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(opted), `"bridge":{"manage":true}`) {
		t.Errorf("the opt-in is missing: %s", opted)
	}
	var back FleetSpec
	if err := json.Unmarshal(opted, &back); err != nil || !back.Bridge.Manage {
		t.Errorf("round trip: %+v %v", back.Bridge, err)
	}
	var old FleetSpec
	if err := json.Unmarshal(plain, &old); err != nil || old.Bridge.Manage {
		t.Errorf("a stored spec without the block reads as not managed: %+v %v", old.Bridge, err)
	}
}

// A contradiction between the identifying and the non-identifying attributes
// resolves to the stricter reading: only agreeing values give standalone.
func TestBridgeModeOfAgentFailsClosedOnContradiction(t *testing.T) {
	cases := []struct {
		name  string
		ident map[string]string
		nonid map[string]string
		want  BridgeMode
	}{
		{"non-identifying only", nil, map[string]string{AttrBridgeMode: "standalone"}, BridgeModeStandalone},
		{"identifying only", map[string]string{AttrBridgeMode: "standalone"}, nil, BridgeModeStandalone},
		{"both agree", map[string]string{AttrBridgeMode: "standalone"}, map[string]string{AttrBridgeMode: "standalone"}, BridgeModeStandalone},
		{"operator wins non-identifying", map[string]string{AttrBridgeMode: "standalone"}, map[string]string{AttrBridgeMode: "operator"}, BridgeModeUnknown},
		{"operator wins identifying", map[string]string{AttrBridgeMode: "operator"}, map[string]string{AttrBridgeMode: "standalone"}, BridgeModeUnknown},
		{"none", nil, nil, BridgeModeUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Agent{IdentifyingAttributes: c.ident, NonIdentifyingAttributes: c.nonid}
			if got := BridgeModeOfAgent(a); got != c.want {
				t.Errorf("mode %q, want %q", got, c.want)
			}
			f := &Fleet{ObjectMeta: ObjectMeta{Name: "f"}, FleetSpec: FleetSpec{Bridge: BridgeSpec{Manage: true}}}
			if locked := BridgeConfigLock(a, f) != ""; locked != (c.want != BridgeModeStandalone) {
				t.Errorf("locked %v with the opt-in", locked)
			}
		})
	}
}
