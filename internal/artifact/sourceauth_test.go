// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

// FX-6: the credentials of artifacts.sourceAuth reach the mirror, and only the
// origin of artifacts.sourceBaseUrl.

// mirror is a TLS server that holds release assets and requires credentials.
type mirror struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	// want is the required Authorization header ("" = none required).
	want string
	// redirectTo sends every GET to this base URL (an object store on another host).
	redirectTo string
	seen       []seenRequest
}

type seenRequest struct{ path, auth string }

func newMirror(t *testing.T) *mirror {
	m := &mirror{files: map[string][]byte{}}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mirror) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.seen = append(m.seen, seenRequest{r.URL.Path, r.Header.Get("Authorization")})
	want, redirect := m.want, m.redirectTo
	body, ok := m.files[r.URL.Path]
	m.mu.Unlock()
	if want != "" && r.Header.Get("Authorization") != want {
		w.Header().Set("WWW-Authenticate", `Basic realm="mirror"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if redirect != "" {
		http.Redirect(w, r, redirect+r.URL.Path, http.StatusFound)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(body)
}

func (m *mirror) put(path string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = body
}

func (m *mirror) requests() []seenRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]seenRequest(nil), m.seen...)
}

// release stores the files of one contrib asset.
func (m *mirror) release(version string, archive []byte) {
	base := "/v" + version + "/otelcol-contrib_" + version + "_linux_amd64.tar.gz"
	sum := sha256.Sum256(archive)
	m.put(base, archive)
	m.put(base+".sha256", []byte(hex.EncodeToString(sum[:])))
	m.put(base+".sigstore.json", []byte(`{"bundle":true}`))
}

func writeSecret(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// newMirrorCache creates a cache whose upstream is the mirror (trusted through
// the client of the mirror's TLS server) with the signature check stubbed.
func newMirrorCache(t *testing.T, base string, client *http.Client, mut func(cfg *config.ArtifactsConfig)) (*Cache, *memstore.Store) {
	t.Helper()
	cfg := config.Default().Artifacts
	cfg.Dir = t.TempDir()
	cfg.SourceBaseURL = base
	cfg.MaxCacheBytes = 1 << 30
	cfg.Verify.TrustedRootFile = filepath.Join(sigDir, "trusted_root.json")
	if mut != nil {
		mut(&cfg)
	}
	st := memstore.New()
	c, err := NewCache(cfg, Deps{
		Store: st, HTTPClient: client, PublicURL: "https://fleet.example.com",
		DownloadSecret: []byte("0123456789abcdef0123456789abcdef"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	if c.ver != nil {
		c.verifyFn = func(_, _ []byte, _, version string) (*verified, error) {
			return &verified{Identity: "https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v" + version}, nil
		}
	}
	return c, st
}

func TestSourceAuthBearerAndBasic(t *testing.T) {
	archive := fakeArchive(3000)
	for name, tc := range map[string]struct {
		auth config.ArtifactSourceAuth
		want string
	}{
		"bearer token (Artifactory)": {config.ArtifactSourceAuth{TokenFile: writeSecret(t, "tok-123\n")}, "Bearer tok-123"},
		"basic (Nexus)": {config.ArtifactSourceAuth{Username: "svc-myriarch", PasswordFile: writeSecret(t, " p4ss \n")},
			"Basic " + base64.StdEncoding.EncodeToString([]byte("svc-myriarch:p4ss"))},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMirror(t)
			m.want = tc.want
			m.release("0.162.0", archive)
			c, _ := newMirrorCache(t, m.srv.URL, m.srv.Client(), func(cfg *config.ArtifactsConfig) { cfg.SourceAuth = tc.auth })

			a, err := c.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
			require.NoError(t, err)
			assert.Equal(t, domain.ArtifactReady, a.Status)
			// the checksum, the bundle and the archive all carried the credentials
			reqs := m.requests()
			require.Len(t, reqs, 3)
			for _, r := range reqs {
				assert.Equal(t, tc.want, r.auth, r.path)
			}
		})
	}
}

func TestSourceAuthWrongOrMissingCredentialsGiveAClearError(t *testing.T) {
	m := newMirror(t)
	m.want = "Bearer right"
	m.release("0.162.0", fakeArchive(100))

	// wrong token
	c, st := newMirrorCache(t, m.srv.URL, m.srv.Client(), func(cfg *config.ArtifactsConfig) {
		cfg.SourceAuth = config.ArtifactSourceAuth{TokenFile: writeSecret(t, "wrong-secret-value")}
	})
	_, err := c.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
	assert.Contains(t, err.Error(), "artifacts.sourceAuth")
	assert.NotContains(t, err.Error(), "wrong-secret-value", "the token never appears in errors")
	a, ferr := st.Artifacts().Find(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64", "tar.gz")
	require.NoError(t, ferr)
	assert.Equal(t, domain.ArtifactFailed, a.Status)
	assert.Contains(t, a.Error, "HTTP 401")
	assert.Contains(t, a.Error, "the mirror rejected the credentials of artifacts.sourceAuth")

	// no credentials configured at all
	c2, _ := newMirrorCache(t, m.srv.URL, m.srv.Client(), nil)
	_, err = c2.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
	assert.Contains(t, err.Error(), "set artifacts.sourceAuth")
}

// The credentials never go to a redirect target on another host (or port):
// the mirror hands out the files from an object store.
func TestSourceAuthIsNotSentToRedirectTargets(t *testing.T) {
	store := newMirror(t) // another origin, no authentication
	archive := fakeArchive(2000)
	store.release("0.162.0", archive)
	m := newMirror(t)
	m.want = "Bearer mirror-token"
	m.redirectTo = store.srv.URL

	c, _ := newMirrorCache(t, m.srv.URL, m.srv.Client(), func(cfg *config.ArtifactsConfig) {
		cfg.SourceAuth = config.ArtifactSourceAuth{TokenFile: writeSecret(t, "mirror-token")}
	})
	a, err := c.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactReady, a.Status)

	for _, r := range m.requests() {
		assert.Equal(t, "Bearer mirror-token", r.auth, "the mirror itself is asked with the credentials: %s", r.path)
	}
	require.NotEmpty(t, store.requests())
	for _, r := range store.requests() {
		assert.Empty(t, r.auth, "the object store behind the redirect must not receive the credentials: %s", r.path)
	}
}

func TestSourceAuthIsReadAgainWhenTheFileChanges(t *testing.T) {
	m := newMirror(t)
	m.release("0.162.0", fakeArchive(100))
	m.release("0.163.0", fakeArchive(200))
	file := writeSecret(t, "old-token")
	c, _ := newMirrorCache(t, m.srv.URL, m.srv.Client(), func(cfg *config.ArtifactsConfig) {
		cfg.SourceAuth = config.ArtifactSourceAuth{TokenFile: file}
	})
	m.want = "Bearer old-token"
	_, err := c.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)

	// rotation: the mirror accepts the new token, myriarchd picks it up without a restart
	m.mu.Lock()
	m.want = "Bearer new-token-which-is-longer"
	m.mu.Unlock()
	require.NoError(t, os.WriteFile(file, []byte("new-token-which-is-longer\n"), 0o600))
	_, err = c.Ensure(context.Background(), "otelcol-contrib", "0.163.0", "linux", "amd64")
	require.NoError(t, err)
}

func TestSourceAuthConfigurationErrors(t *testing.T) {
	tok := writeSecret(t, "t")
	empty := writeSecret(t, "  \n")
	for name, tc := range map[string]struct {
		auth config.ArtifactSourceAuth
		want string
	}{
		"token and basic":             {config.ArtifactSourceAuth{TokenFile: tok, Username: "u", PasswordFile: tok}, "not both"},
		"user without password":       {config.ArtifactSourceAuth{Username: "u"}, "together with passwordFile"},
		"password without user":       {config.ArtifactSourceAuth{PasswordFile: tok}, "together with passwordFile"},
		"token file missing":          {config.ArtifactSourceAuth{TokenFile: filepath.Join(t.TempDir(), "nope")}, "artifacts.sourceAuth.tokenFile: cannot read file"},
		"password file missing":       {config.ArtifactSourceAuth{Username: "u", PasswordFile: filepath.Join(t.TempDir(), "nope")}, "artifacts.sourceAuth.passwordFile: cannot read file"},
		"token file empty":            {config.ArtifactSourceAuth{TokenFile: empty}, "file is empty"},
		"password file empty (basic)": {config.ArtifactSourceAuth{Username: "u", PasswordFile: empty}, "file is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default().Artifacts
			cfg.Dir = t.TempDir()
			cfg.SourceAuth = tc.auth
			_, err := NewCache(cfg, Deps{Store: memstore.New()})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestSourceAuthOrigin(t *testing.T) {
	tokenFile := writeSecret(t, "tok")
	a, err := newSourceAuth(config.ArtifactSourceAuth{TokenFile: tokenFile}, "https://Mirror.example.com/artifactory/repo")
	require.NoError(t, err)
	var got []string
	rt := a.wrap(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	}))
	for url, want := range map[string]string{
		"https://mirror.example.com/x":         "Bearer tok", // host names are case-insensitive, default port 443
		"https://mirror.example.com:443/x":     "Bearer tok",
		"https://mirror.example.com:8443/x":    "",
		"http://mirror.example.com/x":          "",
		"https://mirror.example.com.evil.io/x": "",
		"https://evil.example.com/x":           "",
		"https://sub.mirror.example.com/x":     "",
	} {
		got = nil
		req, _ := http.NewRequest("GET", url, nil)
		_, err := rt.RoundTrip(req)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, want, got[0], url)
		assert.Empty(t, req.Header.Get("Authorization"), "the caller's request is not modified")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// FX-7: the Sigstore TUF requests use the HTTP client of the cache (here: it
// trusts the TLS certificate of the mirror, which the default client of
// net/http would not), go to the configured repository and carry the
// credentials of the mirror only if the repository has the origin of the mirror.
func TestTUFUsesTheClientAndTheConfiguredRepository(t *testing.T) {
	m := newMirror(t)
	m.want = "Bearer mirror-token"
	other := newMirror(t) // a TUF mirror on another origin

	build := func(tufURL string) *Cache {
		c, _ := newMirrorCache(t, m.srv.URL, m.srv.Client(), func(cfg *config.ArtifactsConfig) {
			cfg.SourceAuth = config.ArtifactSourceAuth{TokenFile: writeSecret(t, "mirror-token")}
			cfg.Verify.TrustedRootFile = ""
			cfg.Verify.TUFRepositoryURL = tufURL
		})
		require.NotNil(t, c.ver)
		return c
	}

	// the repository on the origin of the mirror: with its credentials
	c := build(m.srv.URL + "/sigstore-tuf/")
	_, err := c.ver.loadRoot() // the mirror holds no TUF metadata: the fetch fails, the requests are what counts
	require.Error(t, err)
	assert.Contains(t, err.Error(), "via TUF from "+m.srv.URL+"/sigstore-tuf")
	reqs := m.requests()
	require.NotEmpty(t, reqs, "the TUF client must reach the mirror (TLS trust of the client)")
	for _, r := range reqs {
		assert.True(t, strings.HasPrefix(r.path, "/sigstore-tuf/"), "requests go below the configured base URL: %s", r.path)
		assert.Equal(t, "Bearer mirror-token", r.auth, r.path)
	}
	assert.True(t, anyPath(reqs, "timestamp.json") || anyPath(reqs, "root.json"), "TUF metadata is requested: %v", reqs)

	// the repository on another origin: no credentials
	c = build(other.srv.URL + "/tuf")
	// (the client of the cache only trusts the certificate of the mirror; httptest uses one
	// certificate for all its servers, so the TLS handshake succeeds)
	_, err = c.ver.loadRoot()
	require.Error(t, err)
	reqs = other.requests()
	require.NotEmpty(t, reqs)
	for _, r := range reqs {
		assert.Empty(t, r.auth, "credentials of the mirror must not go to another origin: %s", r.path)
		assert.True(t, strings.HasPrefix(r.path, "/tuf/"), r.path)
	}
}

func anyPath(reqs []seenRequest, suffix string) bool {
	for _, r := range reqs {
		if strings.HasSuffix(r.path, suffix) {
			return true
		}
	}
	return false
}

func TestTUFOptions(t *testing.T) {
	v, err := newBundleVerifier(config.ArtifactVerifyConfig{RequireSignature: true}, t.TempDir(), nil)
	require.NoError(t, err)
	def := v.tufOptions()
	assert.Equal(t, "https://tuf-repo-cdn.sigstore.dev", def.RepositoryBaseURL, "default: the public Sigstore instance")
	assert.NotNil(t, def.Fetcher)
	assert.Contains(t, v.tufBase(), "tuf-repo-cdn.sigstore.dev")

	v, err = newBundleVerifier(config.ArtifactVerifyConfig{RequireSignature: true, TUFRepositoryURL: "https://mirror.example.com/repo/sigstore-tuf/"}, t.TempDir(), nil)
	require.NoError(t, err)
	o := v.tufOptions()
	assert.Equal(t, "https://mirror.example.com/repo/sigstore-tuf", o.RepositoryBaseURL)
	assert.Equal(t, def.Root, o.Root, "the embedded Sigstore root stays the trust anchor")

	_, err = newBundleVerifier(config.ArtifactVerifyConfig{RequireSignature: true, TUFRepositoryURL: "ftp://x"}, t.TempDir(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tufRepositoryUrl")

	// trustedRootFile wins: no TUF at all
	v, err = newBundleVerifier(config.ArtifactVerifyConfig{RequireSignature: true, TUFRepositoryURL: "https://x.example.com", TrustedRootFile: filepath.Join(sigDir, "trusted_root.json")}, t.TempDir(), nil)
	require.NoError(t, err)
	tm, err := v.loadRoot()
	require.NoError(t, err)
	assert.NotNil(t, tm)
}
