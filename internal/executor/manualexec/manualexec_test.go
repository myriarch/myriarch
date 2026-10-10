// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package manualexec_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/executor/manualexec"
)

func TestManualExecutor(t *testing.T) {
	ex := manualexec.New()
	assert.Equal(t, domain.ExecutorManual, ex.Type())
	assert.Empty(t, ex.Validate(domain.UpdateSpec{Executor: domain.ExecutorManual}, nil))

	a, b := domain.AgentID(uuid.New()), domain.AgentID(uuid.New())
	res, err := ex.Start(context.Background(), executor.UpdateRequest{
		RolloutID: uuid.New(), ToVersion: "0.162.0", Targets: []executor.Target{{AgentID: a}, {AgentID: b}},
	})
	require.NoError(t, err)
	require.Len(t, res, 2)
	for i, id := range []domain.AgentID{a, b} {
		assert.Equal(t, id, res[i].AgentID, "one result per target, in order")
		assert.True(t, res[i].Started)
		assert.Equal(t, manualexec.ExternalRef, res[i].ExternalRef)
		assert.Empty(t, res[i].Error)
		assert.False(t, res[i].RetryLater)
	}
	res, err = ex.Start(context.Background(), executor.UpdateRequest{})
	require.NoError(t, err)
	assert.Empty(t, res)

	assert.NoError(t, ex.Cancel(context.Background(), executor.CancelRequest{AgentIDs: []domain.AgentID{a}}))
}
