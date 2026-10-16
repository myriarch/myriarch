// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/testutil/enrollstore"
)

// The install grant (ADR-0046) is the credential of a host that is not enrolled yet: bound to one artifact and
// one enrollment token, valid while that token is valid, and not interchangeable with the token of a package
// offer.

type installFixture struct {
	*serveFixture
	tokens *enrollstore.Store
	tok    domain.EnrollmentToken
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newInstallFixture(t *testing.T, mut ...func(cfg *config.ArtifactsConfig)) *installFixture {
	t.Helper()
	s := newServeFixture(t, mut...)
	s.deps.InstallEnabled = true // the other caches of the tests are built on these dependencies
	tokens := enrollstore.Wrap(s.st)
	logs := &syncBuffer{}
	s.cache = s.otherCache(func(d *Deps) {
		d.Store, d.Registerer, d.InstallGrantTTL = tokens, s.reg, time.Hour
		d.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /api/v1/artifacts/{id}/content/{filename}", s.cache.ServeContent)
	tok := tokens.Create(domain.EnrollmentToken{Name: "edge", CreatedAt: time.Now()})
	return &installFixture{serveFixture: s, tokens: tokens, tok: tok, logs: logs}
}

func (f *installFixture) grant() map[string]string {
	f.t.Helper()
	g, err := f.cache.InstallGrant(f.art, f.tok.ID)
	require.NoError(f.t, err)
	return map[string]string{"Authorization": "Bearer " + g}
}

func (f *installFixture) denied(name string, hdr map[string]string, path string) {
	f.t.Helper()
	w := f.get(path, hdr)
	assert.Equal(f.t, http.StatusUnauthorized, w.Code, name)
	assert.Contains(f.t, w.Header().Get("Content-Type"), "problem+json", name)
	assert.Contains(f.t, w.Header().Get("WWW-Authenticate"), "Bearer", name)
	assert.NotContains(f.t, w.Body.String(), "myrd_", name+": the credential is never echoed")
	var p api.Problem
	require.NoError(f.t, json.Unmarshal(w.Body.Bytes(), &p), name)
	assert.Equal(f.t, "unauthorized", p.Code, name)
}

func TestInstallGrantServesTheArtifactItsChecksumAndItsBundle(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()

	w := f.get(f.path(), hdr)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, f.archive, w.Body.Bytes())
	assert.Equal(t, "application/gzip", w.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))

	// a resumed download works like the one of an updater
	rng := map[string]string{"Authorization": hdr["Authorization"], "Range": "bytes=100-199"}
	w = f.get(f.path(), rng)
	require.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, f.archive[100:200], w.Body.Bytes())

	// the checksum comes from the record, in the form of sha256sum
	w = f.get(f.path()+".sha256", hdr)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, f.art.SHA256.String()+"  "+f.art.Filename+"\n", w.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	assert.Regexp(t, `^[0-9a-f]{64}  otelcol-contrib_0\.162\.0_linux_amd64\.tar\.gz\n$`, w.Body.String())

	// and the Sigstore bundle, byte for byte as it was verified
	w = f.get(f.path()+".sigstore.json", hdr)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "bundle", w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// the grant needs no package offer
	require.NoError(t, f.st.Packages().ClearOffers(context.Background(), f.agent))
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code)
	assert.GreaterOrEqual(t, counter(t, f.fixture, "ok"), 5.0)
}

func TestInstallGrantServesNothingElse(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()
	for _, name := range []string{
		f.art.Filename + ".sha256.sigstore.json", f.art.Filename + ".sha512", f.art.Filename + ".SHA256", "other-name.tar.gz",
		f.art.Filename + "%2F..", f.art.Filename + ".sha256%20", ".sha256",
	} {
		w := f.get("/api/v1/artifacts/"+f.art.ID.String()+"/content/"+name, hdr)
		assert.Equal(t, http.StatusNotFound, w.Code, name)
	}
	// no bundle on record: nothing to serve
	noBundle := *f.art
	noBundle.SigstoreBundle = nil
	f.cache.cfg.Verify.RequireSignature = false
	noBundle.SignatureVerified = false
	_, err := f.st.Artifacts().Upsert(context.Background(), noBundle)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, f.get(f.path()+".sigstore.json", hdr).Code)
	assert.Equal(t, http.StatusOK, f.get(f.path()+".sha256", hdr).Code, "the checksum is still on record")
}

func TestInstallGrantTokenFormat(t *testing.T) {
	f := newInstallFixture(t)
	g := strings.TrimPrefix(f.grant()["Authorization"], "Bearer ")
	require.True(t, strings.HasPrefix(g, "myrd_"))
	p, m, ok := strings.Cut(strings.TrimPrefix(g, "myrd_"), ".")
	require.True(t, ok)
	payload, err := base64.RawURLEncoding.DecodeString(p)
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Equal(t, map[string]any{"a": f.art.ID.String(), "e": f.tok.ID.String(), "k": "i", "t": claims["t"]}, claims,
		"artifact, enrollment token, kind and time; no agent")
	assert.InDelta(t, float64(time.Now().Unix()), claims["t"], 5)
	mac, err := base64.RawURLEncoding.DecodeString(m)
	require.NoError(t, err)
	assert.Equal(t, f.cache.mac(payload), mac, "HMAC-SHA256 over the JSON payload")
	assert.Regexp(t, `^myrd_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`, g, "the pattern of the secret scanning")
}

// Neither credential can be used as the other. The kind is part of the signed payload.
func TestInstallGrantAndOfferTokenAreNotInterchangeable(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()

	// the token of an offer does not get the companion files, and stays bound to its offer
	assert.Equal(t, http.StatusOK, f.get(f.path(), f.headers).Code)
	assert.Equal(t, http.StatusNotFound, f.get(f.path()+".sha256", f.headers).Code)
	assert.Equal(t, http.StatusNotFound, f.get(f.path()+".sigstore.json", f.headers).Code)
	require.NoError(t, f.st.Packages().ClearOffers(context.Background(), f.agent))
	f.denied("offer token without offer", f.headers, f.path())
	f.offer(f.url)
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code)

	sign := func(claims any) map[string]string {
		tok, err := f.cache.sign(claims)
		require.NoError(t, err)
		return map[string]string{"Authorization": "Bearer " + tok}
	}
	// an install grant payload without its kind is the payload of an offer token for no agent
	f.denied("grant without kind", sign(struct {
		A uuid.UUID `json:"a"`
		E uuid.UUID `json:"e"`
		T int64     `json:"t"`
	}{f.art.ID, f.tok.ID, time.Now().Unix()}), f.path())
	// a payload of an offer token with the kind of a grant has no enrollment token
	f.denied("offer with the grant kind", sign(struct {
		A uuid.UUID `json:"a"`
		G uuid.UUID `json:"g"`
		T int64     `json:"t"`
		K string    `json:"k"`
	}{f.art.ID, f.agent, time.Now().Unix(), "i"}), f.path())
	// an agent with an offer and an enrollment token in one payload: the kind decides, so each half is checked
	// as what it says it is (here: a grant with a token that does not exist)
	f.denied("agent and unknown token as grant", sign(struct {
		A uuid.UUID `json:"a"`
		G uuid.UUID `json:"g"`
		E uuid.UUID `json:"e"`
		T int64     `json:"t"`
		K string    `json:"k"`
	}{f.art.ID, f.agent, uuid.New(), time.Now().Unix(), "i"}), f.path())
	// unknown kinds
	for _, k := range []string{"u", "I", "install", " i", "i "} {
		f.denied("kind "+k, sign(installClaims{Artifact: f.art.ID, Issued: time.Now().Unix(), Kind: k, Token: f.tok.ID}), f.path())
	}
	// a grant needs a token
	f.denied("grant with the nil token", sign(installClaims{Artifact: f.art.ID, Issued: time.Now().Unix(), Kind: "i"}), f.path())
	// the grant does not serve another artifact
	other := uuid.New()
	f.denied("grant of another artifact", sign(installClaims{Artifact: other, Issued: time.Now().Unix(), Kind: "i", Token: f.tok.ID}), f.path())
	// a grant signed with another key, and a changed signature
	foreign := f.otherCache(func(d *Deps) { d.DownloadSecret = []byte("another-secret-another-secret-12") })
	g, err := foreign.InstallGrant(f.art, f.tok.ID)
	require.NoError(t, err)
	f.denied("another secret", map[string]string{"Authorization": "Bearer " + g}, f.path())
	good := strings.TrimPrefix(hdr["Authorization"], "Bearer ")
	dot := strings.LastIndexByte(good, '.')
	f.denied("tampered mac", map[string]string{"Authorization": "Bearer " + good[:dot+1] + "AAAA" + good[dot+5:]}, f.path())
	f.denied("tampered payload", map[string]string{"Authorization": "Bearer myrd_AAAA" + strings.TrimPrefix(good, "myrd_")}, f.path())
	f.denied("no header", nil, f.path())
	f.denied("not bearer", map[string]string{"Authorization": "Basic " + good}, f.path())
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code, "the real grant still works")
	assert.GreaterOrEqual(t, counter(t, f.fixture, "unauthorized"), 14.0)
}

// The grant is valid only while its enrollment token is: revoked, expired and removed tokens stop it at once,
// without a hint which of them it was.
func TestInstallGrantStopsWithItsEnrollmentToken(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code)

	// expiry of the token
	soon := time.Now().Add(30 * time.Minute) // before the grant expires itself (1h)
	tok := f.tokens.Create(domain.EnrollmentToken{Name: "short", ExpiresAt: &soon})
	g, err := f.cache.InstallGrant(f.art, tok.ID)
	require.NoError(t, err)
	short := map[string]string{"Authorization": "Bearer " + g}
	assert.Equal(t, http.StatusOK, f.get(f.path(), short).Code)
	f.cache.now = func() time.Time { return soon.Add(time.Second) }
	w := f.get(f.path(), short)
	require.Equal(t, http.StatusUnauthorized, w.Code, "an expired token")
	msgs := []string{w.Body.String()}
	f.cache.now = func() time.Time { return time.Now().UTC() }

	// revocation
	f.tokens.Revoke(f.tok.ID, time.Now())
	w = f.get(f.path(), hdr)
	require.Equal(t, http.StatusUnauthorized, w.Code, "a revoked token")
	f.denied("revoked", hdr, f.path()+".sha256")
	f.denied("revoked", hdr, f.path()+".sigstore.json")
	msgs = append(msgs, w.Body.String())

	// removal
	f.tokens.Delete(f.tok.ID)
	w = f.get(f.path(), hdr)
	require.Equal(t, http.StatusUnauthorized, w.Code, "a removed token")
	msgs = append(msgs, w.Body.String())

	detail := func(body string) string {
		var p api.Problem
		require.NoError(t, json.Unmarshal([]byte(body), &p))
		return p.Detail
	}
	assert.Equal(t, detail(msgs[0]), detail(msgs[1]), "revoked and expired look the same")
	assert.Equal(t, detail(msgs[1]), detail(msgs[2]), "revoked and removed look the same")
	assert.NotContains(t, detail(msgs[1]), f.tok.ID.String())
}

// A credential stops working with the feature that issues it. A grant that was minted before a restart with
// install.enabled=false is refused with 401 although its enrollment token is valid and its lifetime has not passed,
// also when the lifetime is not checked any more (install.grantTtl is not validated while the installation is off,
// and 0 means no expiry). The token of a package offer is not affected, and no new grant is minted.
func TestInstallGrantIsRefusedWhileTheInstallationIsOff(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()
	for _, name := range []string{"", ".sha256", ".sigstore.json"} {
		require.Equal(t, http.StatusOK, f.get(f.path()+name, hdr).Code, "the grant works while the installation is on: "+name)
	}

	for _, ttl := range []time.Duration{time.Hour, 0} {
		off := f.otherCache(func(d *Deps) { d.Store, d.InstallEnabled, d.InstallGrantTTL = f.tokens, false, ttl })
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v1/artifacts/{id}/content/{filename}", off.ServeContent)
		on := f.mux
		f.mux = mux
		for _, name := range []string{"", ".sha256", ".sigstore.json"} {
			f.denied("a grant while the installation is off, lifetime "+ttl.String()+": "+name, hdr, f.path()+name)
		}
		assert.Equal(t, http.StatusOK, f.get(f.path(), f.headers).Code, "the token of a package offer is not a grant")
		_, err := off.InstallGrant(f.art, f.tok.ID)
		assert.ErrorContains(t, err, "install.enabled", "no new grant is minted")
		f.mux = on
	}

	// a cache that nobody told about the installation does not take grants either
	zero := f.otherCache(func(d *Deps) { d.Store, d.InstallGrantTTL = f.tokens, time.Hour; d.InstallEnabled = false })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts/{id}/content/{filename}", zero.ServeContent)
	f.mux = mux
	f.denied("the zero value is off", hdr, f.path())
}

// A store failure on the token is a 503 and does not serve the file.
func TestInstallGrantTokenLookupFailureIsUnavailable(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()
	broken := f.otherCache(func(d *Deps) { d.Store = brokenTokenStore{d.Store}; d.InstallGrantTTL = time.Hour })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts/{id}/content/{filename}", broken.ServeContent)
	f.mux = mux
	w := f.get(f.path(), hdr)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.NotContains(t, w.Body.String(), "hunter2", "the cause stays in the log")
	assert.NotContains(t, w.Body.String(), "connection refused")
	assert.Equal(t, http.StatusServiceUnavailable, f.get(f.path()+".sha256", hdr).Code, "the checksum is not served either")
}

func TestInstallGrantExpires(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()
	f.cache.now = func() time.Time { return time.Now().UTC().Add(59 * time.Minute) }
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code)
	f.cache.now = func() time.Time { return time.Now().UTC().Add(61 * time.Minute) }
	w := f.get(f.path(), hdr)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "expired")

	// no lifetime (development): the grant lives as long as its token
	f.cache.deps.InstallGrantTTL = 0
	f.cache.now = func() time.Time { return time.Now().UTC().Add(1000 * time.Hour) }
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code)
	// and the lifetime of an offer token is a setting of its own
	f.cache.cfg.DownloadTokenTTL = time.Hour
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code, "artifacts.downloadTokenTtl does not apply to a grant")
}

// A grant is minted only for an artifact that would be served: ready, with a pinned hash and a verified bundle.
func TestInstallGrantOnlyForVerifiedArtifacts(t *testing.T) {
	f := newInstallFixture(t)
	for name, mut := range map[string]func(a *domain.Artifact){
		"downloading":   func(a *domain.Artifact) { a.Status = domain.ArtifactDownloading },
		"failed":        func(a *domain.Artifact) { a.Status = domain.ArtifactFailed },
		"no hash":       func(a *domain.Artifact) { a.SHA256 = nil },
		"short hash":    func(a *domain.Artifact) { a.SHA256 = a.SHA256[:16] },
		"not verified":  func(a *domain.Artifact) { a.SignatureVerified = false },
		"pending state": func(a *domain.Artifact) { a.Status = "" },
	} {
		a := *f.art
		mut(&a)
		_, err := f.cache.InstallGrant(&a, f.tok.ID)
		assert.Error(t, err, name)
	}
	_, err := f.cache.InstallGrant(nil, f.tok.ID)
	assert.Error(t, err)
	_, err = f.cache.InstallGrant(f.art, uuid.Nil)
	assert.Error(t, err, "a grant needs an enrollment token")
	noSecret := f.otherCache(func(d *Deps) { d.DownloadSecret = nil })
	_, err = noSecret.InstallGrant(f.art, f.tok.ID)
	assert.Error(t, err)

	// a grant that exists is useless once the artifact stops being servable
	hdr := f.grant()
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code)
	bad := *f.art
	bad.Status = domain.ArtifactFailed
	_, err = f.st.Artifacts().Upsert(context.Background(), bad)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, f.get(f.path(), hdr).Code)
	assert.Equal(t, http.StatusNotFound, f.get(f.path()+".sha256", hdr).Code)
	assert.Equal(t, http.StatusNotFound, f.get(f.path()+".sigstore.json", hdr).Code)
	unverified := *f.art
	unverified.SignatureVerified = false
	_, err = f.st.Artifacts().Upsert(context.Background(), unverified)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, f.get(f.path(), hdr).Code, "an artifact without a verified bundle is not served when signatures are required")
}

func TestInstallURL(t *testing.T) {
	f := newInstallFixture(t)
	u, err := f.cache.InstallURL(f.art)
	require.NoError(t, err)
	assert.Equal(t, f.url, u, "the URL of the artifact, the same for offers and grants")
	assert.Equal(t, "https://fleet.example.com/api/v1/artifacts/"+f.art.ID.String()+"/content/otelcol-contrib_0.162.0_linux_amd64.tar.gz", u)
	noURL := f.otherCache(func(d *Deps) { d.PublicURL = "" })
	_, err = noURL.InstallURL(f.art)
	assert.Error(t, err)
	// agentBaseUrls never apply to a host that is not enrolled
	withBases := f.otherCache(func(d *Deps) { d.PublicURL = "https://fleet.example.com/" })
	u2, err := withBases.InstallURL(f.art)
	require.NoError(t, err)
	assert.Equal(t, u, u2, "a trailing slash of the public URL is dropped")
}

// Only the ID of the enrollment token is logged, never a credential.
func TestInstallGrantIsNeverLogged(t *testing.T) {
	f := newInstallFixture(t)
	hdr := f.grant()
	assert.Equal(t, http.StatusOK, f.get(f.path(), hdr).Code)
	f.denied("bad", map[string]string{"Authorization": "Bearer " + strings.TrimPrefix(hdr["Authorization"], "Bearer ") + "x"}, f.path())
	logs := f.logs.String()
	assert.Contains(t, logs, "artifact served with an install grant")
	assert.Contains(t, logs, f.tok.ID.String())
	assert.NotContains(t, logs, "myrd_")
	assert.NotContains(t, logs, strings.TrimPrefix(hdr["Authorization"], "Bearer "))
	assert.NotContains(t, logs, "Bearer")
}

func TestInstallGrantDoesNotChangeTheTokenOfAnOffer(t *testing.T) {
	// the offer token keeps its three fields: older updaters and replicas parse it as before
	f := newInstallFixture(t)
	tok := strings.TrimPrefix(f.headers["Authorization"], "Bearer ")
	p, _, _ := strings.Cut(strings.TrimPrefix(tok, "myrd_"), ".")
	payload, err := base64.RawURLEncoding.DecodeString(p)
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Len(t, claims, 3)
	assert.Contains(t, claims, "a")
	assert.Contains(t, claims, "g")
	assert.Contains(t, claims, "t")
	cl, err := f.cache.parseToken(tok)
	require.NoError(t, err)
	assert.Equal(t, "", cl.Kind)
	assert.Equal(t, f.agent, cl.Agent)
	assert.Equal(t, uuid.Nil, cl.Token)
}

// brokenTokenStore fails every read of an enrollment token.
type brokenTokenStore struct{ store.Store }

func (b brokenTokenStore) Identity() store.IdentityStore { return brokenIdentity{b.Store.Identity()} }

type brokenIdentity struct{ store.IdentityStore }

func (brokenIdentity) GetEnrollmentToken(context.Context, uuid.UUID) (*domain.EnrollmentToken, error) {
	return nil, errors.New("connection refused (password=hunter2)")
}
