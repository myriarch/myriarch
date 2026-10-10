// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// ghServer fakes the GitHub API (releases list) and the raw content host.
type ghServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	// handlers
	etag        string
	releasesRaw []byte
	status      int // forced status for the releases endpoint
	headers     map[string]string
	raw         map[string]string // path -> body
	now         time.Time
}

func newGH(t *testing.T) *ghServer {
	g := &ghServer{raw: map[string]string{}, headers: map[string]string{}, etag: `W/"abc123"`}
	b, err := os.ReadFile(filepath.Join(fixtures, "github/repos/open-telemetry/opentelemetry-collector-releases/releases.json"))
	require.NoError(t, err)
	g.releasesRaw = b
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.reqs = append(g.reqs, r.Clone(context.Background()))
		status := g.status
		hdr := g.headers
		g.mu.Unlock()
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v3/repos/") && strings.HasSuffix(r.URL.Path, "/releases"):
			if status != 0 {
				http.Error(w, `{"message":"API rate limit exceeded"}`, status)
				return
			}
			if r.Header.Get("If-None-Match") == g.etag {
				w.Header().Set("ETag", g.etag)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", g.etag)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(g.releasesRaw)
		case strings.HasPrefix(r.URL.Path, "/raw/"):
			g.mu.Lock()
			body, ok := g.raw[strings.TrimPrefix(r.URL.Path, "/raw")]
			g.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	// changelogs from the fixtures, served like raw.githubusercontent.com (only at tag v0.162.0)
	for _, repo := range []string{"opentelemetry-collector", "opentelemetry-collector-contrib"} {
		c, err := os.ReadFile(filepath.Join(fixtures, "raw/open-telemetry", repo, "v0.162.0/CHANGELOG.md"))
		require.NoError(t, err)
		g.raw["/open-telemetry/"+repo+"/v0.162.0/CHANGELOG.md"] = string(c)
	}
	return g
}

func (g *ghServer) requests() []*http.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*http.Request(nil), g.reqs...)
}

func (g *ghServer) releasesRequests() []*http.Request {
	var out []*http.Request
	for _, r := range g.requests() {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			out = append(out, r)
		}
	}
	return out
}

func httpEnv(t *testing.T, g *ghServer, mut ...func(c *config.ReleasesConfig)) *env {
	return newEnv(t, append([]func(c *config.ReleasesConfig){func(c *config.ReleasesConfig) {
		c.FixtureDir = ""
		c.GitHubAPIURL = g.srv.URL + "/api/v3" // GitHub Enterprise style
		c.RawBaseURL = g.srv.URL + "/raw"
	}}, mut...)...)
}

func TestHTTPPollSendsTokenAndETagAndHonours304(t *testing.T) {
	g := newGH(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("ghp_secret\n"), 0o600))
	e := httpEnv(t, g, func(c *config.ReleasesConfig) { c.TokenFile = tokenFile })

	require.NoError(t, e.tr.Poll(context.Background()))
	reqs := g.releasesRequests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, "/api/v3/repos/open-telemetry/opentelemetry-collector-releases/releases", r.URL.Path, "GHES API base URL")
	assert.Equal(t, "per_page=30", r.URL.RawQuery, "always the same parameters, otherwise the ETag does not match")
	assert.Equal(t, "Bearer ghp_secret", r.Header.Get("Authorization"))
	assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
	assert.Empty(t, r.Header.Get("If-None-Match"))
	assert.Len(t, e.releases(t, domain.DistributionContrib), 5)

	// ETag is stored in the KV store under release/etag/<repo>
	v, err := e.st.KV().Get(context.Background(), "release/etag/open-telemetry/opentelemetry-collector-releases")
	require.NoError(t, err)
	assert.Equal(t, `W/"abc123"`, string(v))

	// second poll: conditional request, 304, nothing is processed
	before := len(g.requests())
	notifBefore := len(e.notif.all())
	require.NoError(t, e.tr.Poll(context.Background()))
	reqs = g.releasesRequests()
	require.Len(t, reqs, 2)
	assert.Equal(t, `W/"abc123"`, reqs[1].Header.Get("If-None-Match"))
	assert.Equal(t, before+1, len(g.requests()), "no changelog downloads on 304")
	assert.Len(t, e.notif.all(), notifBefore)
	st, err := e.tr.GetStatus(context.Background())
	require.NoError(t, err)
	assert.True(t, st.NotModified)
	assert.True(t, st.Authenticated)
	assert.Equal(t, 1.0, metricValue(t, e.reg, "myriarch_release_polls_total", map[string]string{"result": "not_modified"}))

	// the ETag survives a restart/leader change: a new tracker on the same store sends it
	e2 := httpEnv(t, g, func(c *config.ReleasesConfig) { c.TokenFile = tokenFile })
	e2.st = e.st
	e2.tr.deps.Store = e.st
	require.NoError(t, e2.tr.Poll(context.Background()))
	reqs = g.releasesRequests()
	assert.Equal(t, `W/"abc123"`, reqs[len(reqs)-1].Header.Get("If-None-Match"))
}

func TestHTTPChangelogsAtTagWithFallbackToNewestTag(t *testing.T) {
	g := newGH(t)
	e := httpEnv(t, g)
	require.NoError(t, e.tr.Poll(context.Background()))
	var raws []string
	for _, r := range g.requests() {
		if strings.HasPrefix(r.URL.Path, "/raw/") {
			raws = append(raws, r.URL.Path)
		}
	}
	// newest tag first: its file serves all older versions, no further downloads needed
	assert.ElementsMatch(t, []string{
		"/raw/open-telemetry/opentelemetry-collector/v0.162.0/CHANGELOG.md",
		"/raw/open-telemetry/opentelemetry-collector-contrib/v0.162.0/CHANGELOG.md",
	}, raws, "one download per repository; the changelog file at the newest tag contains the older sections")
	r, err := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.160.0")
	require.NoError(t, err)
	assert.NotEmpty(t, r.BreakingChanges)

	// A 404 for the tag of an older version falls back to the newest tag.
	g2 := newGH(t)
	e2 := httpEnv(t, g2, func(c *config.ReleasesConfig) { c.Distributions = []string{domain.DistributionContrib} })
	// only an OLD release is new in this store; its tag has no changelog -> fallback to the newest known tag
	for _, d := range []string{domain.DistributionContrib} {
		full := newEnv(t)
		require.NoError(t, full.tr.Poll(context.Background()))
		for _, rel := range full.releases(t, d) {
			if rel.Version != "0.159.0" {
				_, err := e2.st.Releases().Upsert(context.Background(), rel)
				require.NoError(t, err)
			}
		}
	}
	require.NoError(t, e2.tr.Poll(context.Background()))
	r159, err := e2.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.159.0")
	require.NoError(t, err)
	assert.NotEmpty(t, r159.BreakingChanges)
	var tried []string
	for _, rq := range g2.requests() {
		if strings.Contains(rq.URL.Path, "opentelemetry-collector-contrib/") && strings.HasPrefix(rq.URL.Path, "/raw/") {
			tried = append(tried, rq.URL.Path)
		}
	}
	assert.Equal(t, []string{
		"/raw/open-telemetry/opentelemetry-collector-contrib/v0.159.0/CHANGELOG.md",
		"/raw/open-telemetry/opentelemetry-collector-contrib/v0.162.0/CHANGELOG.md",
	}, tried, "404 at the release tag, then the newest tag")
}

func TestHTTPRateLimitBacksOffAndRecovers(t *testing.T) {
	g := newGH(t)
	e := httpEnv(t, g)
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	e.tr.now = func() time.Time { return clock }

	g.mu.Lock()
	g.status = http.StatusForbidden
	g.headers = map[string]string{
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     fmt.Sprint(clock.Add(10 * time.Minute).Unix()),
	}
	g.mu.Unlock()
	err := e.tr.Poll(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
	assert.Contains(t, err.Error(), "releases.tokenFile", "unauthenticated: recommend a token")
	st, _ := e.tr.GetStatus(context.Background())
	assert.True(t, st.RateLimited)
	require.NotNil(t, st.BackoffUntil)
	assert.Equal(t, clock.Add(10*time.Minute), *st.BackoffUntil, "wait until x-ratelimit-reset")
	assert.Contains(t, st.LastError, "rate limit")
	assert.Equal(t, 1.0, metricValue(t, e.reg, "myriarch_release_polls_total", map[string]string{"result": "rate_limited"}))

	// during the backoff no request is made
	n := len(g.releasesRequests())
	clock = clock.Add(5 * time.Minute)
	err = e.tr.Poll(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paused")
	assert.Len(t, g.releasesRequests(), n)

	// after the reset time polling resumes
	g.mu.Lock()
	g.status, g.headers = 0, map[string]string{}
	g.mu.Unlock()
	clock = clock.Add(6 * time.Minute)
	require.NoError(t, e.tr.Poll(context.Background()))
	assert.Len(t, g.releasesRequests(), n+1)
	st, _ = e.tr.GetStatus(context.Background())
	assert.False(t, st.RateLimited)
	assert.Empty(t, st.LastError)
}

func TestHTTPRetryAfterAndRemainingZeroOn200(t *testing.T) {
	g := newGH(t)
	e := httpEnv(t, g)
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	e.tr.now = func() time.Time { return clock }
	g.mu.Lock()
	g.status = http.StatusTooManyRequests
	g.headers = map[string]string{"Retry-After": "120"}
	g.mu.Unlock()
	require.Error(t, e.tr.Poll(context.Background()))
	st, _ := e.tr.GetStatus(context.Background())
	require.NotNil(t, st.BackoffUntil)
	assert.Equal(t, clock.Add(2*time.Minute), *st.BackoffUntil)

	// a successful response that used up the quota also pauses (until reset), but the page is processed
	g2 := newGH(t)
	g2.headers = map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(clock.Add(time.Hour).Unix())}
	e2 := httpEnv(t, g2)
	e2.tr.now = func() time.Time { return clock }
	require.NoError(t, e2.tr.Poll(context.Background()))
	assert.Len(t, e2.releases(t, domain.DistributionContrib), 5)
	require.Error(t, e2.tr.Poll(context.Background()))
	st, _ = e2.tr.GetStatus(context.Background())
	assert.True(t, st.RateLimited)
	require.NotNil(t, st.RateRemaining)
	assert.Equal(t, 0, *st.RateRemaining)
}

func TestHTTPErrors(t *testing.T) {
	g := newGH(t)
	e := httpEnv(t, g)
	g.mu.Lock()
	g.status = http.StatusInternalServerError
	g.mu.Unlock()
	err := e.tr.Poll(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
	st, _ := e.tr.GetStatus(context.Background())
	assert.Contains(t, st.LastError, "HTTP 500")
	assert.False(t, st.RateLimited)
	assert.Empty(t, e.releases(t, domain.DistributionContrib))

	// unreadable server: the URL (which may contain credentials) is not echoed
	e2 := newEnv(t, func(c *config.ReleasesConfig) {
		c.FixtureDir = ""
		c.GitHubAPIURL = "http://user:pass@127.0.0.1:1/api"
	})
	err = e2.tr.Poll(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "pass")

	// broken JSON must not poison the ETag
	g3 := newGH(t)
	g3.releasesRaw = []byte("{not json")
	e3 := httpEnv(t, g3)
	require.Error(t, e3.tr.Poll(context.Background()))
	_, kerr := e3.st.KV().Get(context.Background(), "release/etag/open-telemetry/opentelemetry-collector-releases")
	assert.ErrorIs(t, kerr, domain.ErrNotFound)

	// missing token file does not prevent polling
	g4 := newGH(t)
	e4 := httpEnv(t, g4, func(c *config.ReleasesConfig) { c.TokenFile = filepath.Join(t.TempDir(), "missing") })
	require.NoError(t, e4.tr.Poll(context.Background()))
	assert.Empty(t, g4.releasesRequests()[0].Header.Get("Authorization"))
}
