// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package assignment

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/gitops"
)

func addBridge(e *env, mode string) domain.AgentID {
	return e.addAgent("edge-nl-1001", func(a *domain.Agent) {
		a.Kind = domain.AgentKindBridge
		if mode != "" {
			a.NonIdentifyingAttributes[domain.AttrBridgeMode] = mode
		}
	})
}

func setManage(t *testing.T, e *env, manage bool) {
	t.Helper()
	snap, _ := gitops.Parse(os.DirFS("../testdata/gitops"), ".", "commit-2")
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == "edge-nl" {
			snap.Fleets[i].Bridge.Manage = manage
		}
	}
	_, err := e.st.GitOps().ApplySnapshot(context.Background(), *snap, domain.GitOpsSync{})
	require.NoError(t, err)
}

// A Bridge is a member of the fleet that selects it, but its fleet renders and
// sets no configuration unless the fleet opted in (ADR-0037).
func TestBridgeIsNotRenderedAConfigWithoutTheOptIn(t *testing.T) {
	e := newEnv(t)
	id := addBridge(e, "standalone")
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "edge-nl", as.FleetName)
	assert.Contains(t, as.Error, "spec.bridge.manage")
	assert.True(t, as.AssignedConfigHash.IsZero())
	assert.False(t, as.DesiredChanged)

	ag := e.st.Agent(id)
	assert.Equal(t, "edge-nl", ag.Assignment.FleetName)
	assert.True(t, ag.Assignment.DesiredConfigHash.IsZero())
	assert.Empty(t, ag.Assignment.TargetVersion, "a Bridge has no target version")
}

func TestBridgeInStandaloneModeIsAssignedWithTheOptIn(t *testing.T) {
	e := newEnv(t)
	setManage(t, e, true)
	id := addBridge(e, "standalone")
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Empty(t, as.Error)
	assert.False(t, as.AssignedConfigHash.IsZero())
	assert.True(t, as.DesiredChanged)
	assert.False(t, e.st.Agent(id).Assignment.DesiredConfigHash.IsZero())
}

func TestBridgeInOperatorModeStaysLockedWithTheOptIn(t *testing.T) {
	e := newEnv(t)
	setManage(t, e, true)
	for _, mode := range []string{"operator", ""} {
		id := addBridge(e, mode)
		as, err := e.svc.AssignAgent(context.Background(), id)
		require.NoError(t, err)
		assert.NotEmpty(t, as.Error, mode)
		assert.True(t, e.st.Agent(id).Assignment.DesiredConfigHash.IsZero(), mode)
	}
}
