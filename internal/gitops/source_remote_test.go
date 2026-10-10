// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The clone always follows gitops.repoUrl: a changed URL with the same clone
// directory must apply the new repository, not the previous origin.
func TestGitCloneFollowsTheConfiguredRepository(t *testing.T) {
	requireGit(t)
	workA := workRepo(t, "")
	workB := workRepo(t, "")
	runGit(t, workB, "commit", "-q", "--allow-empty", "-m", "extra commit in B")
	headB := runGit(t, workB, "rev-parse", "HEAD")
	cloneDir := filepath.Join(t.TempDir(), "clone")

	fa := gitFixture(t, "file://"+workA, "")
	fa.cfg.CloneDir = cloneDir
	require.NoError(t, fa.build(t).SyncNow(context.Background(), "startup"))
	assert.Equal(t, runGit(t, workA, "rev-parse", "HEAD"), fa.status(t).AppliedCommit.SHA)

	fb := gitFixture(t, "file://"+workB, "")
	fb.cfg.CloneDir = cloneDir
	require.NoError(t, fb.build(t).SyncNow(context.Background(), "startup"))
	st := fb.status(t)
	assert.Equal(t, headB, st.AppliedCommit.SHA, "the configured repository is applied")
	assert.Equal(t, "file://"+workB, st.Source)
}

// A pre-existing clone whose origin differs from gitops.repoUrl is replaced
// by a fresh clone of the configured repository.
func TestGitExistingCloneWithAnotherOriginIsReplaced(t *testing.T) {
	requireGit(t)
	workA := workRepo(t, "")
	workB := workRepo(t, "")
	cloneDir := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(cloneDir), "clone", "-q", "file://"+workA, cloneDir)
	require.Equal(t, "file://"+workA, runGit(t, cloneDir, "config", "remote.origin.url"))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "stale.txt"), []byte("x"), 0o644))

	f := gitFixture(t, "file://"+workB, "")
	f.cfg.CloneDir = cloneDir
	require.NoError(t, f.build(t).SyncNow(context.Background(), "startup"))
	assert.Equal(t, runGit(t, workB, "rev-parse", "HEAD"), f.status(t).AppliedCommit.SHA)
	assert.Equal(t, "file://"+workB, runGit(t, cloneDir, "config", "remote.origin.url"))
}
