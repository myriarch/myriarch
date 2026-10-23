// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// windowsRename behaves like MoveFileEx on Windows when the target exists and is being replaced or read.
var renameMu sync.Mutex

func windowsRename(oldpath, newpath string) error {
	renameMu.Lock()
	defer renameMu.Unlock()
	if _, err := os.Lstat(newpath); err == nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: errors.New("Access is denied.")}
	}
	return os.Rename(oldpath, newpath)
}

func tmpFiles(t *testing.T, f *fixture) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(f.dir, ".tmp"))
	require.NoError(t, err)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestEnsureSameContentUnderTwoArtifactsWhereRenameFailsOnAnExistingTarget(t *testing.T) {
	f := newFixture(t)
	// both downloads reach the rename before one of them moves its file, as when they run at the same time
	var arrived sync.WaitGroup
	arrived.Add(2)
	f.cache.rename = func(oldpath, newpath string) error {
		arrived.Done()
		arrived.Wait()
		return windowsRename(oldpath, newpath)
	}
	archive := fakeArchive(5000)
	f.up.release("0.162.0", "linux", "amd64", archive, []byte("bundle"))
	f.up.release("0.162.0", "linux", "arm64", archive, []byte("bundle"))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, arch := range []string{"amd64", "arm64"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", arch)
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	list, _, err := f.st.Artifacts().List(context.Background(), store.ArtifactFilter{Status: []domain.ArtifactStatus{domain.ArtifactReady}})
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, list[0].SHA256, list[1].SHA256)
	want := sha256.Sum256(archive)
	assert.Equal(t, want[:], []byte(list[0].SHA256))
	assert.Empty(t, tmpFiles(t, f))
}

func TestMoveIntoPlaceTargetWithOtherContentStaysAnError(t *testing.T) {
	f := newFixture(t)
	f.cache.rename = windowsRename
	dir := t.TempDir()
	final, tmp := filepath.Join(dir, "final"), filepath.Join(dir, "tmp")
	require.NoError(t, os.WriteFile(final, []byte("other content"), 0o640))
	require.NoError(t, os.WriteFile(tmp, []byte("new content"), 0o640))
	sum := sha256.Sum256([]byte("new content"))

	err := f.cache.moveIntoPlace(tmp, final, sum[:])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Access is denied")
	got, rerr := os.ReadFile(final)
	require.NoError(t, rerr)
	assert.Equal(t, "other content", string(got))
}

func TestMoveIntoPlaceTargetIsADirectoryStaysAnError(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	final, tmp := filepath.Join(dir, "final"), filepath.Join(dir, "tmp")
	require.NoError(t, os.Mkdir(final, 0o750))
	require.NoError(t, os.WriteFile(tmp, []byte("new content"), 0o640))
	sum := sha256.Sum256([]byte("new content"))
	require.Error(t, f.cache.moveIntoPlace(tmp, final, sum[:]))
}

func TestEnsureRecordsFailedWhenTargetHoldsOtherContent(t *testing.T) {
	f := newFixture(t)
	f.cache.rename = windowsRename
	archive := fakeArchive(5000)
	f.up.release("0.162.0", "linux", "amd64", archive, []byte("bundle"))
	sum := sha256.Sum256(archive)
	final := f.cache.hashPath(sum[:])
	require.NoError(t, os.MkdirAll(filepath.Dir(final), 0o750))
	require.NoError(t, os.WriteFile(final, []byte("tampered"), 0o640))

	_, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "move artifact into place")
	got, rerr := os.ReadFile(final)
	require.NoError(t, rerr)
	assert.Equal(t, "tampered", string(got))
	list, _, err := f.st.Artifacts().List(context.Background(), store.ArtifactFilter{Status: []domain.ArtifactStatus{domain.ArtifactFailed}})
	require.NoError(t, err)
	assert.Len(t, list, 1)
}
