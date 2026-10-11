// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The de-privileged preflight is ended at its timeout, including children it
// spawned (needs root to switch accounts, otherwise skipped).
func TestOSExecutorAsEndsChildTreeAtTimeout(t *testing.T) {
	if !canDropPrivileges() {
		t.Skip("needs root")
	}
	exe, err := osExecutorAs(strconv.Itoa(os.Getuid()))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	_, err = exe(ctx, nil, "/bin/sh", "-c", "sleep 60 & wait")
	assert.Error(t, err)
	assert.Less(t, time.Since(begin), 15*time.Second)
}

// An account that does not resolve is refused before anything runs.
func TestOSExecutorAsRejectsUnknownAccount(t *testing.T) {
	exe, err := osExecutorAs("myriarch-no-such-account.invalid")
	require.Error(t, err)
	assert.Nil(t, exe)
}
