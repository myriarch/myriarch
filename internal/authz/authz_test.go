// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"testing"

	"github.com/myriarch/myriarch/internal/domain"
)

// fleets:set_version (select or clear the UI target version of a fleet) is an
// operator permission; viewers only read.
func TestFleetVersionSetPermission(t *testing.T) {
	for role, want := range map[domain.Role]bool{domain.RoleViewer: false, domain.RoleOperator: true, domain.RoleAdmin: true, "": false} {
		if got := RoleHas(role, PermFleetVersionSet); got != want {
			t.Errorf("role %q: fleets:set_version = %v, want %v", role, got, want)
		}
	}
	if PermFleetVersionSet != "fleets:set_version" {
		t.Errorf("permission name: %q", PermFleetVersionSet)
	}
	operator := &Principal{Type: PrincipalUser, ID: "u", Role: domain.RoleOperator}
	viewer := &Principal{Type: PrincipalToken, ID: "t", Role: domain.RoleViewer}
	if !operator.Can(PermFleetVersionSet) || viewer.Can(PermFleetVersionSet) {
		t.Error("principals follow their role")
	}
	if !System.Can(PermFleetVersionSet) {
		t.Error("the system principal may do everything")
	}
	// every role keeps the permissions it had: the new one is only added
	for _, p := range []Permission{PermRead, PermAgentsOperate, PermRolloutsOperate, PermAuditRead} {
		if !RoleHas(domain.RoleOperator, p) {
			t.Errorf("operator lost %s", p)
		}
	}
}
