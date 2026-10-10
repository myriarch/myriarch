// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
)

// `myriarchd dev` without parameters reads the quickstart GitOps repository.
func TestDevDefaultGitOpsDirIsQuickstart(t *testing.T) {
	assert.Equal(t, "dir:examples/quickstart", config.DevDefaults().GitOps.RepoURL)

	root := newRootCommand()
	dev, _, err := root.Find([]string{"dev"})
	require.NoError(t, err)
	require.Equal(t, "dev", dev.Name())
	flag := dev.Flags().Lookup("gitops-dir")
	require.NotNil(t, flag, "myriarchd dev has a --gitops-dir flag")
	assert.Equal(t, "examples/quickstart", flag.DefValue)
}

// The default directory exists in the repository (the dev command resolves it
// relative to the repository root) and contains the GitOps object directories.
func TestDevDefaultGitOpsDirExists(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "quickstart")
	for _, sub := range []string{"blueprints", "fleets", "policies", "inventory"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		require.NoError(t, err, sub)
		assert.True(t, info.IsDir(), sub)
	}
}
