// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GIT-2: a hostile Git server cannot stream an unbounded response.
func TestGitHTTPResponsesAreLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 10_000)))
	}))
	defer srv.Close()
	c := &http.Client{Transport: &limitTransport{rt: http.DefaultTransport, max: 1000}}
	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	assert.ErrorIs(t, err, errResponseTooLarge)
	assert.LessOrEqual(t, n, int64(1000))

	small := &http.Client{Transport: &limitTransport{rt: http.DefaultTransport, max: 100_000}}
	resp2, err := small.Get(srv.URL)
	require.NoError(t, err)
	defer resp2.Body.Close()
	b, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	assert.Len(t, b, 10_000)
}

// GIT-2: the clone is limited (also checked after the checkout).
func TestGitCloneIsLimitedInSize(t *testing.T) {
	requireGit(t)
	work := workRepo(t, "")
	f := gitFixture(t, fileURL(work), "")
	f.cfg.MaxRepoBytes = 2000
	s := f.build(t)
	err := s.SyncNow(context.Background(), "startup")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maxRepoBytes")
	// the oversized clone is removed
	_, statErr := dirSize(f.cfg.CloneDir)
	assert.NoError(t, statErr)
	if size, _ := dirSize(f.cfg.CloneDir); size > 0 {
		t.Fatalf("oversized clone left behind: %d bytes", size)
	}

	// the same repository is fine with the default limit and is cloned shallow
	f2 := gitFixture(t, fileURL(work), "")
	s2 := f2.build(t)
	require.NoError(t, s2.SyncNow(context.Background(), "startup"))
	assert.Equal(t, "ok", f2.status(t).State)
	assert.FileExists(t, f2.cfg.CloneDir+"/.git/shallow", "depth 1: no history is downloaded")
}
