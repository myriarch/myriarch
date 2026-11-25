// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSameContentSeesChangeRequest(t *testing.T) {
	a := &TargetProgress{ChangeRequest: &ChangeRequest{Provider: "github", Ref: "github-pr:example/deploy#1", State: ChangeRequestOpen}}
	b := &TargetProgress{ChangeRequest: &ChangeRequest{Provider: "github", Ref: "github-pr:example/deploy#1", State: ChangeRequestOpen}}
	assert.True(t, a.SameContent(b))
	b.ChangeRequest.State = ChangeRequestMerged
	assert.False(t, a.SameContent(b))
	b.ChangeRequest.State = ChangeRequestOpen
	b.ChangeRequest.MergeCommit = "abc"
	assert.False(t, a.SameContent(b))
	assert.False(t, a.SameContent(&TargetProgress{}))
	assert.False(t, (&TargetProgress{}).SameContent(a))
}

func TestProgressOfCopiesChangeRequest(t *testing.T) {
	tgt := RolloutTarget{Status: TargetInProgress, Progress: &TargetProgress{
		ChangeRequest: &ChangeRequest{Ref: "gitlab-mr:example/deploy!2", State: ChangeRequestOpen},
	}}
	p := ProgressOf(tgt)
	p.ChangeRequest.State = ChangeRequestClosed
	assert.Equal(t, ChangeRequestOpen, tgt.Progress.ChangeRequest.State)
}

func TestIsChangeRequestRef(t *testing.T) {
	assert.True(t, IsChangeRequestRef("github-pr:example/deploy#1"))
	assert.True(t, IsChangeRequestRef("gitlab-mr:example/deploy!1"))
	assert.False(t, IsChangeRequestRef("awx-job:9"))
	assert.False(t, IsChangeRequestRef(""))
}
