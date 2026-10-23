// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

// Prepare is what the install plan calls: it never waits for a download, it starts one in the background and
// reports the state until the artifact is ready.

const prepAsset = "/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz"

func (f *fixture) prepare() (*domain.Artifact, Readiness, error) {
	return f.cache.Prepare(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
}

func (f *fixture) waitPrepared() {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(f.t, f.cache.WaitPrepared(ctx))
}

func TestPrepareStartsOneDownloadAndReportsPending(t *testing.T) {
	f := newFixture(t)
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(5000), []byte("bundle"))
	release := f.up.block()
	t.Cleanup(release)

	a, state, err := f.prepare()
	require.NoError(t, err)
	assert.Nil(t, a)
	assert.Equal(t, PreparePending, state)
	// while the download runs, asking again starts nothing
	for i := 0; i < 5; i++ {
		a, state, err = f.prepare()
		require.NoError(t, err)
		assert.Nil(t, a)
		assert.Equal(t, PreparePending, state)
	}
	release()
	f.waitPrepared()
	assert.Equal(t, 1, f.up.count(prepAsset), "one download")

	a, state, err = f.prepare()
	require.NoError(t, err)
	assert.Equal(t, PrepareReady, state)
	require.NotNil(t, a)
	assert.Equal(t, domain.ArtifactReady, a.Status)
	assert.True(t, a.SignatureVerified)
	assert.Len(t, a.SHA256, 32)
	assert.Equal(t, 1, f.up.count(prepAsset), "a ready artifact is not downloaded again")
}

func TestPrepareManyCallersStartOneDownload(t *testing.T) {
	f := newFixture(t)
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(5000), []byte("bundle"))
	release := f.up.block()
	t.Cleanup(release)
	var wg sync.WaitGroup
	states := make(chan Readiness, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, st, err := f.prepare()
			assert.NoError(t, err)
			states <- st
		}()
	}
	wg.Wait()
	close(states)
	for st := range states {
		assert.Equal(t, PreparePending, st, "the upstream answers nothing yet")
	}
	release()
	f.waitPrepared()
	assert.Equal(t, 1, f.up.count(prepAsset))
}

func TestPrepareDoesNotWaitForTheDownload(t *testing.T) {
	f := newFixture(t)
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(5000), []byte("bundle"))
	release := f.up.block() // the upstream answers nothing until the test lets it
	t.Cleanup(release)

	a, state, err := f.prepare()
	require.NoError(t, err)
	assert.Nil(t, a)
	assert.Equal(t, PreparePending, state, "Prepare returned while the download could not finish")

	// waiting can be given up
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, f.cache.WaitPrepared(ctx), context.DeadlineExceeded)

	release()
	f.waitPrepared()
	_, state, err = f.prepare()
	require.NoError(t, err)
	assert.Equal(t, PrepareReady, state)
}

// A failed attempt is recorded, not repeated before a minute has passed, and tried again afterwards.
func TestPrepareDoesNotRetryAFailureBeforeTheBackoff(t *testing.T) {
	f := newFixture(t) // the upstream has no release: the checksum is a 404
	_, state, err := f.prepare()
	require.NoError(t, err)
	assert.Equal(t, PreparePending, state)
	f.waitPrepared()
	rec, err := f.st.Artifacts().Find(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64", "tar.gz")
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactFailed, rec.Status)
	requests := f.up.count(prepAsset + ".sha256")
	require.Equal(t, 1, requests)

	for i := 0; i < 5; i++ {
		a, state, err := f.prepare()
		require.NoError(t, err)
		assert.Nil(t, a)
		assert.Equal(t, PrepareFailed, state)
	}
	assert.Equal(t, requests, f.up.count(prepAsset+".sha256"), "the upstream is not asked again within the backoff")

	f.cache.now = func() time.Time { return time.Now().UTC().Add(2 * time.Minute) }
	_, state, err = f.prepare()
	require.NoError(t, err)
	assert.Equal(t, PreparePending, state, "after the backoff the attempt is repeated")
	f.waitPrepared()
	assert.Equal(t, requests+1, f.up.count(prepAsset+".sha256"))

	// the release appears upstream: the next attempt succeeds
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(5000), []byte("bundle"))
	f.cache.now = func() time.Time { return time.Now().UTC().Add(4 * time.Minute) }
	_, state, err = f.prepare()
	require.NoError(t, err)
	assert.Equal(t, PreparePending, state)
	f.waitPrepared()
	a, state, err := f.prepare()
	require.NoError(t, err)
	assert.Equal(t, PrepareReady, state)
	assert.Equal(t, domain.ArtifactReady, a.Status)
}

// A download that another replica runs (a recent record in the status downloading) is not started a second
// time; the record of a replica that is gone is.
func TestPrepareLeavesTheDownloadOfAnotherReplicaAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.st.Artifacts().Upsert(ctx, domain.Artifact{
		ID: uuid.Must(uuid.NewV7()), Distribution: "otelcol-contrib", Version: "0.162.0", OS: "linux", Arch: "amd64", Format: "tar.gz",
		Filename: "otelcol-contrib_0.162.0_linux_amd64.tar.gz", Status: domain.ArtifactDownloading,
	})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		a, state, err := f.prepare()
		require.NoError(t, err)
		assert.Nil(t, a)
		assert.Equal(t, PreparePending, state)
	}
	f.waitPrepared()
	assert.Zero(t, f.up.count(prepAsset+".sha256"), "nothing was requested: the other replica is on it")

	f.cache.now = func() time.Time { return time.Now().UTC().Add(11 * time.Minute) }
	_, state, err := f.prepare()
	require.NoError(t, err)
	assert.Equal(t, PreparePending, state)
	f.waitPrepared()
	assert.Equal(t, 1, f.up.count(prepAsset+".sha256"), "the record of a replica that is gone is taken over")
}

// A record that claims to be ready but lacks what makes it servable is not ready.
func TestPrepareDoesNotTrustAnUnverifiedReadyRecord(t *testing.T) {
	f := newFixture(t)
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(5000), []byte("bundle"))
	ctx := context.Background()
	a, err := f.cache.Ensure(ctx, "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	_, state, err := f.prepare()
	require.NoError(t, err)
	require.Equal(t, PrepareReady, state)

	for name, mut := range map[string]func(a *domain.Artifact){
		"no signature": func(a *domain.Artifact) { a.SignatureVerified = false },
		"no hash":      func(a *domain.Artifact) { a.SHA256 = nil },
	} {
		bad := *a
		mut(&bad)
		_, err := f.st.Artifacts().Upsert(ctx, bad)
		require.NoError(t, err)
		got, state, err := f.prepare()
		require.NoError(t, err, name)
		assert.Nil(t, got, name)
		assert.Equal(t, PreparePending, state, name)
		f.waitPrepared()
		got, state, err = f.prepare()
		require.NoError(t, err, name)
		assert.Equal(t, PrepareReady, state, name)
		assert.True(t, got.SignatureVerified, name)
	}
}

func TestPrepareRejectsWhatItCannotDownload(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for name, c := range map[string][4]string{
		"unknown system":  {"otelcol-contrib", "0.162.0", "plan9", "amd64"},
		"odd arch":        {"otelcol-contrib", "0.162.0", "linux", "x/y"},
		"not a version":   {"otelcol-contrib", "latest", "linux", "amd64"},
		"empty version":   {"otelcol-contrib", "", "linux", "amd64"},
		"unknown release": {"otelcol", "0.162.0", "linux", "amd64"},
	} {
		a, state, err := f.cache.Prepare(ctx, c[0], c[1], c[2], c[3])
		assert.ErrorIs(t, err, domain.ErrInvalidArgument, name)
		assert.Nil(t, a, name)
		assert.Equal(t, PrepareFailed, state, name)
	}
	f.waitPrepared()
	assert.Zero(t, f.up.count(prepAsset), "nothing was requested")
	// aliases of the platform are accepted like in Ensure
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(2000), []byte("b"))
	_, state, err := f.cache.Prepare(ctx, "otelcol-contrib", "v0.162.0", "Linux", "x86_64")
	require.NoError(t, err)
	assert.Equal(t, PreparePending, state)
	f.waitPrepared()
	a, state, err := f.cache.Prepare(ctx, "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, PrepareReady, state)
	assert.Equal(t, "amd64", a.Arch)
}
