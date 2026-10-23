// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
)

// AC-16: files are stored content addressed and are re-verified before they are served.
func TestServedFilesAreContentAddressedAndVerified(t *testing.T) {
	s := newServeFixture(t)
	file := s.cache.path(s.art)
	assert.Equal(t, filepath.Join(s.dir, "sha256", s.art.SHA256.String()), file)
	assert.Equal(t, http.StatusOK, s.get(s.path(), s.headers).Code)

	// the file on disk is replaced after the verification (same size): it is detected and fetched again
	tampered := append([]byte(nil), s.archive...)
	tampered[10] ^= 0xff
	before := s.up.count("/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz")
	require.NoError(t, os.WriteFile(file, tampered, 0o640))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(file, future, future)) // even with a modification time that looks plausible
	w := s.get(s.path(), s.headers)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, s.archive, w.Body.Bytes(), "the pinned bytes are served, not the replaced file")
	assert.Equal(t, before+1, s.up.count("/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz"), "the file was fetched again")

	// a symbolic link in place of the file is not followed
	outside := filepath.Join(t.TempDir(), "evil")
	require.NoError(t, os.WriteFile(outside, tampered, 0o640))
	require.NoError(t, os.Remove(file))
	require.NoError(t, os.Symlink(outside, file))
	w = s.get(s.path(), s.headers)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, s.archive, w.Body.Bytes())
}

func TestUnverifiedRecordsAreNotServed(t *testing.T) {
	s := newServeFixture(t)
	ctx := context.Background()
	rec, err := s.st.Artifacts().Get(ctx, s.art.ID)
	require.NoError(t, err)

	unsigned := *rec
	unsigned.SignatureVerified = false
	_, err = s.st.Artifacts().Upsert(ctx, unsigned)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, s.get(s.path(), s.headers).Code, "signatures are required: an unverified record is not served")

	noHash := *rec
	noHash.SignatureVerified, noHash.SHA256 = true, nil
	_, err = s.st.Artifacts().Upsert(ctx, noHash)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, s.get(s.path(), s.headers).Code, "a record without a pinned SHA-256 is not served")

	_, err = s.st.Artifacts().Upsert(ctx, *rec)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, s.get(s.path(), s.headers).Code)
}

// AC-18: the source URL is derived from the current configuration, not from the stored record.
func TestRefetchUsesTheConfiguredSourceNotTheStoredURL(t *testing.T) {
	s := newServeFixture(t)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the stored source URL must not be fetched: %s", r.URL)
		http.NotFound(w, r)
	}))
	defer evil.Close()
	rec, _ := s.st.Artifacts().Get(context.Background(), s.art.ID)
	poisoned := *rec
	poisoned.SourceURL = evil.URL + "/payload.tar.gz"
	_, err := s.st.Artifacts().Upsert(context.Background(), poisoned)
	require.NoError(t, err)
	require.NoError(t, os.Remove(s.cache.path(s.art)))
	w := s.get(s.path(), s.headers)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, s.archive, w.Body.Bytes())
}

// AC-18: redirects stay on https and on the release hosts (or the configured mirror).
func TestRedirectPolicyOfTheUpstreamClients(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.SourceBaseURL = "https://mirror.example.com/releases" })
	check := f.cache.redirectPolicy()
	req := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	for _, ok := range []string{
		"https://objects.githubusercontent.com/github-production-release-asset/x",
		"https://release-assets.githubusercontent.com/x",
		"https://github.com/open-telemetry/x/releases/download/v1/x.tar.gz",
		"https://mirror.example.com/other/path",
	} {
		assert.NoError(t, check(req(ok), nil), ok)
	}
	for _, bad := range []string{
		"http://objects.githubusercontent.com/x",   // downgrade
		"https://evil.example.net/x",               // foreign host
		"https://169.254.169.254/latest/meta-data", // metadata
		"https://githubusercontent.com.evil.net/x", // look-alike
		"ftp://github.com/x",
	} {
		assert.Error(t, check(req(bad), nil), bad)
	}
	assert.Error(t, check(req("https://github.com/x"), make([]*http.Request, 5)), "hop limit")

	// custom list: replaces the GitHub defaults
	f2 := newFixture(t, func(cfg *config.ArtifactsConfig) {
		cfg.SourceBaseURL = "https://mirror.example.com/r"
		cfg.AllowedRedirectHosts = []string{"cdn.example.com", "*.cdn.example.net"}
	})
	check = f2.cache.redirectPolicy()
	assert.NoError(t, check(req("https://cdn.example.com/x"), nil))
	assert.NoError(t, check(req("https://eu.cdn.example.net/x"), nil))
	assert.Error(t, check(req("https://objects.githubusercontent.com/x"), nil))

	// a plain-http mirror may redirect within itself only
	f3 := newFixture(t)
	check = f3.cache.redirectPolicy()
	assert.NoError(t, check(req(f3.up.srv.URL+"/elsewhere"), nil))
	assert.Error(t, check(req("http://127.0.0.1:1/x"), nil))

	// end to end: an upstream that redirects to a foreign host is refused
	var hit atomic.Bool
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer foreign.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, foreign.URL+"/x", http.StatusFound) }))
	defer redir.Close()
	f4 := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.SourceBaseURL = redir.URL })
	_, err := f4.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.False(t, hit.Load(), "the redirect target must not be contacted")
}

// AC-15: the offer stores only the URL; the token is minted when it is sent and carries a bounded lifetime.
func TestDownloadHeadersAreMintedPerUse(t *testing.T) {
	s := newServeFixture(t, func(cfg *config.ArtifactsConfig) { cfg.DownloadTokenTTL = time.Hour })
	assert.Equal(t, time.Hour, s.cache.DownloadTokenTTL())
	assert.True(t, s.cache.SignatureRequired())
	h, err := s.cache.DownloadHeaders(s.url, s.agent)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, s.get(s.path(), h).Code)
	// a token minted for another agent has no offer behind it
	other, err := s.cache.DownloadHeaders(s.url, domain.AgentID(uuid.New()))
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, s.get(s.path(), other).Code)
	for _, bad := range []string{"https://fleet.example.com/other", "https://fleet.example.com/api/v1/artifacts/not-a-uuid/content/x"} {
		_, err := s.cache.DownloadHeaders(bad, s.agent)
		assert.Error(t, err, bad)
	}
	// the default is a bounded lifetime
	assert.Equal(t, 24*time.Hour, config.Default().Artifacts.DownloadTokenTTL)
}

// AC-16: deleting an artifact removes the file on every replica.
func TestDeleteIsPropagatedToOtherReplicas(t *testing.T) {
	s := newServeFixture(t)
	other := s.otherCache(func(d *Deps) {}) // second replica on the same (shared in the test) directory layout
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = other.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	events, cancelSub := s.bus.Subscribe(eventbus.ArtifactChanged)
	defer cancelSub()
	file := s.cache.path(s.art)
	require.FileExists(t, file)
	// the file of the "other replica" is a copy at the same content addressed path: model it by keeping the file
	// and removing only the record locally
	require.NoError(t, s.cache.Delete(ctx, s.art.ID))
	var sawDeleted bool
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev := <-events:
			var p eventbus.ArtifactChangedPayload
			if ev.Decode(&p) == nil && p.Status == domain.ArtifactDeleted && p.SHA256 == s.art.SHA256.String() {
				sawDeleted = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	assert.True(t, sawDeleted, "deletion is published with the content hash")
	_, err := os.Stat(file)
	assert.True(t, os.IsNotExist(err))

	// the replica handler removes the file when it only sees the event
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o750))
	require.NoError(t, os.WriteFile(file, []byte("stale copy"), 0o640))
	require.NoError(t, s.bus.Publish(ctx, eventbus.ArtifactChanged, eventbus.ArtifactChangedPayload{ArtifactID: s.art.ID, Status: domain.ArtifactDeleted, SHA256: s.art.SHA256.String()}))
	deadline2 := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline2) {
			t.Fatal("the other replica did not remove its copy")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}

// OUT-3: stalled downloads are aborted by the idle deadline, concurrent downloads are bounded.
func TestDownloadsAreBoundedAndIdleDeadlineApplies(t *testing.T) {
	f := newFixture(t)
	assert.Equal(t, maxConcurrentDownloads, cap(f.cache.dlSem))
	// a stalled upstream (sends headers, then nothing) is aborted
	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer stall.Close()
	prev := idleTimeoutOverride
	idleTimeoutOverride = 300 * time.Millisecond
	defer func() { idleTimeoutOverride = prev }()
	start := time.Now()
	_, _, _, err := f.cache.download(context.Background(), stall.URL+"/x")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}

// The redirect rule is exported for the installer bundles, which come from a source of their own: the hosts of the
// request, or the GitHub asset hosts, and the host of the source itself.
func TestRedirectPolicyOfASource(t *testing.T) {
	req := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	check := RedirectPolicy(nil, "https://mirror.example.com/myriarch/releases")
	for _, ok := range []string{"https://objects.githubusercontent.com/x", "https://mirror.example.com/elsewhere", "https://github.com/myriarch/myriarch/releases/download/v0.2.0/x"} {
		assert.NoError(t, check(req(ok), nil), ok)
	}
	for _, bad := range []string{"http://mirror.example.com/x", "https://evil.example.net/x", "https://169.254.169.254/x", "https://mirror.example.com.evil.net/x"} {
		assert.Error(t, check(req(bad), nil), bad)
	}
	assert.Error(t, check(req("https://github.com/x"), make([]*http.Request, 5)), "hop limit")

	// a list of hosts replaces the GitHub defaults; the host of the source stays
	check = RedirectPolicy([]string{"cdn.example.com"}, "https://mirror.example.com/r")
	assert.NoError(t, check(req("https://cdn.example.com/x"), nil))
	assert.NoError(t, check(req("https://mirror.example.com/x"), nil))
	assert.Error(t, check(req("https://objects.githubusercontent.com/x"), nil))

	// a source that is not a URL has no host of its own
	check = RedirectPolicy(nil, "::")
	assert.NoError(t, check(req("https://github.com/x"), nil))
	assert.Error(t, check(req("https://mirror.example.com/x"), nil))
}

func TestOpenNoFollowRefusesALink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	require.NoError(t, os.WriteFile(real, []byte("x"), 0o644))
	f, err := OpenNoFollow(real)
	require.NoError(t, err)
	f.Close()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symbolic links")
	}
	_, err = OpenNoFollow(link)
	assert.Error(t, err, "a link in place of the file is not followed")
	_, err = OpenNoFollow(filepath.Join(dir, "missing"))
	assert.Error(t, err)
}
