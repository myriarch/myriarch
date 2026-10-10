// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package live

import (
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
)

// AC-20: every event type has a declared permission and is delivered per client permission only.
func TestEveryEventTypeHasAPermission(t *testing.T) {
	for _, ev := range []string{dto.LiveAgentUpdated, dto.LiveAgentRemoved, dto.LiveRolloutUpdated, dto.LiveGitOpsSynced, dto.LiveReleaseNew, dto.LiveStatsChanged, dto.LiveResync} {
		if eventPermissions[ev] == "" {
			t.Errorf("event %q has no permission", ev)
		}
	}
}

func TestBroadcastChecksPermissionPerClient(t *testing.T) {
	h := New(nil, nil, nil)
	mk := func(p *authz.Principal) *client {
		c := &client{ch: make(chan []byte, 8), recheck: make(chan struct{}, 1), principal: p}
		h.clients[c] = struct{}{}
		return c
	}
	viewer := mk(&authz.Principal{Type: authz.PrincipalToken, ID: "v", Role: domain.RoleViewer})
	nobody := mk(&authz.Principal{Type: authz.PrincipalToken, ID: "n", Role: ""})
	h.send(dto.LiveStatsChanged, struct{}{})
	h.send("secret.admin.event", struct{}{}) // not declared: delivered to nobody
	select {
	case <-viewer.ch:
	case <-time.After(time.Second):
		t.Fatal("viewer must receive the declared event")
	}
	select {
	case f := <-viewer.ch:
		t.Fatalf("undeclared event delivered: %s", f)
	default:
	}
	select {
	case f := <-nobody.ch:
		t.Fatalf("a principal without the permission received: %s", f)
	default:
	}
}
