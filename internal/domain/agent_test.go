// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import "testing"

func TestConfigSyncState(t *testing.T) {
	h1, h2 := Hash{1}, Hash{2}
	cases := []struct {
		name string
		a    Agent
		want ConfigSyncState
	}{
		{"unassigned", Agent{}, ConfigSyncNone},
		{"in_sync", Agent{Assignment: AgentAssignment{FleetName: "f", AssignedConfigHash: h1, DesiredConfigHash: h1},
			RemoteConfig: RemoteConfigState{Status: RemoteConfigApplied, LastHash: h1}}, ConfigSyncInSync},
		{"pending", Agent{Assignment: AgentAssignment{FleetName: "f", AssignedConfigHash: h1, DesiredConfigHash: h1},
			RemoteConfig: RemoteConfigState{Status: RemoteConfigApplying, LastHash: h1}}, ConfigSyncPending},
		{"drift", Agent{Assignment: AgentAssignment{FleetName: "f", AssignedConfigHash: h2, DesiredConfigHash: h1},
			RemoteConfig: RemoteConfigState{Status: RemoteConfigApplied, LastHash: h1}}, ConfigSyncDrift},
		{"failed", Agent{Assignment: AgentAssignment{FleetName: "f", AssignedConfigHash: h1, DesiredConfigHash: h1, FailedConfigHash: h1},
			RemoteConfig: RemoteConfigState{Status: RemoteConfigFailed, LastHash: h1}}, ConfigSyncFailed},
	}
	for _, c := range cases {
		if got := c.a.ConfigSyncState(); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestSelectorMatches(t *testing.T) {
	v := AttributeView{"site.type": "edge", "site.id": "1001", "labels.pilot": "true"}
	sel := Selector{
		MatchAttributes:  map[string]string{"site.type": "edge"},
		MatchExpressions: []SelectorRequirement{{Key: "site.id", Operator: OpGlob, Values: []string{"10*"}}, {Key: "labels.pilot", Operator: OpExists}},
	}
	if !sel.Matches(v) {
		t.Fatal("expected match")
	}
	if (Selector{MatchExpressions: []SelectorRequirement{{Key: "x", Operator: "Bogus"}}}).Matches(v) {
		t.Fatal("invalid selector must not match")
	}
	if !(Selector{}).Matches(v) {
		t.Fatal("empty selector matches all")
	}
}

func TestAllPackagesHashStable(t *testing.T) {
	a := PackageOffer{Name: "", Version: "v0.163.0", DownloadURL: "https://x/a.tar.gz", ContentHash: Hash{1}}
	b := PackageOffer{Name: "opampsupervisor", Version: "v0.163.0", DownloadURL: "https://x/b", ContentHash: Hash{2}}
	if !AllPackagesHash([]PackageOffer{a, b}).Equal(AllPackagesHash([]PackageOffer{b, a})) {
		t.Fatal("hash must not depend on order")
	}
	if AllPackagesHash(nil).IsZero() {
		t.Fatal("empty set must still produce a hash")
	}
}
