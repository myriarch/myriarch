// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// serveFixture has a ready artifact and an agent with an offer for it.
type serveFixture struct {
	*fixture
	art     *domain.Artifact
	agent   domain.AgentID
	url     string
	headers map[string]string
	archive []byte
	mux     *http.ServeMux
}

func newServeFixture(t *testing.T, mut ...func(cfg *config.ArtifactsConfig)) *serveFixture {
	t.Helper()
	f := newFixture(t, mut...)
	archive := fakeArchive(100000)
	f.up.release("0.162.0", "linux", "amd64", archive, []byte("bundle"))
	a, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	s := &serveFixture{fixture: f, art: a, agent: uuid.Must(uuid.NewV7()), archive: archive}
	u, h, err := f.cache.DownloadURL(a, s.agent)
	require.NoError(t, err)
	s.url, s.headers = u, h
	s.offer(u)
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /api/v1/artifacts/{id}/content/{filename}", f.cache.ServeContent)
	return s
}

func (s *serveFixture) offer(downloadURL string) {
	require.NoError(s.t, s.st.Packages().SetOffers(context.Background(), s.agent, []domain.PackageOffer{{
		AgentID: s.agent, Version: "v0.162.0", DownloadURL: downloadURL, ContentHash: s.art.SHA256,
	}}))
}

func (s *serveFixture) get(path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

func (s *serveFixture) path() string {
	return strings.TrimPrefix(s.url, "https://fleet.example.com")
}

func counter(t *testing.T, f *fixture, result string) float64 {
	t.Helper()
	mfs, err := f.reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "myriarch_artifact_downloads_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestDownloadURLAndToken(t *testing.T) {
	s := newServeFixture(t)
	assert.Equal(t, "https://fleet.example.com/api/v1/artifacts/"+s.art.ID.String()+"/content/otelcol-contrib_0.162.0_linux_amd64.tar.gz", s.url)
	auth := s.headers["Authorization"]
	require.True(t, strings.HasPrefix(auth, "Bearer myrd_"))
	tok := strings.TrimPrefix(auth, "Bearer ")
	p, m, ok := strings.Cut(strings.TrimPrefix(tok, "myrd_"), ".")
	require.True(t, ok)
	payload, err := base64.RawURLEncoding.DecodeString(p)
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Equal(t, s.art.ID.String(), claims["a"])
	assert.Equal(t, s.agent.String(), claims["g"])
	assert.InDelta(t, float64(time.Now().Unix()), claims["t"], 5)
	mac, err := base64.RawURLEncoding.DecodeString(m)
	require.NoError(t, err)
	assert.Equal(t, s.cache.mac(payload), mac, "HMAC-SHA256 over the JSON payload")
	assert.Len(t, mac, sha256.Size)

	// no public URL / no secret
	c2 := s.otherCache(func(d *Deps) { d.PublicURL = "" })
	_, _, err = c2.DownloadURL(s.art, s.agent)
	assert.Error(t, err)
	c3 := s.otherCache(func(d *Deps) { d.DownloadSecret = nil })
	_, _, err = c3.DownloadURL(s.art, s.agent)
	assert.Error(t, err)
}

func TestServeContentFullAndRange(t *testing.T) {
	s := newServeFixture(t)
	w := s.get(s.path(), s.headers)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, s.archive, w.Body.Bytes())
	assert.Equal(t, "application/gzip", w.Header().Get("Content-Type"))
	assert.Equal(t, "bytes", w.Header().Get("Accept-Ranges"))
	assert.Contains(t, w.Header().Get("Content-Disposition"), "otelcol-contrib_0.162.0_linux_amd64.tar.gz")
	assert.Equal(t, `"sha256-`+s.art.SHA256.String()+`"`, w.Header().Get("ETag"))

	hdr := map[string]string{"Authorization": s.headers["Authorization"], "Range": "bytes=100-199"}
	w = s.get(s.path(), hdr)
	require.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, s.archive[100:200], w.Body.Bytes())
	assert.Equal(t, "bytes 100-199/100000", w.Header().Get("Content-Range"))

	// resume from the middle to the end
	hdr["Range"] = "bytes=99000-"
	w = s.get(s.path(), hdr)
	require.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, s.archive[99000:], w.Body.Bytes())

	hdr["Range"] = "bytes=999999-"
	w = s.get(s.path(), hdr)
	assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, w.Code)

	// HEAD
	r := httptest.NewRequest(http.MethodHead, s.path(), nil)
	r.Header.Set("Authorization", s.headers["Authorization"])
	w = httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/artifacts/{id}/content/{filename}", s.cache.ServeContent)
	mux.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "100000", w.Header().Get("Content-Length"))
	assert.Empty(t, w.Body.Bytes())

	assert.GreaterOrEqual(t, counter(t, s.fixture, "ok"), 4.0)
}

func TestServeContentAuthentication(t *testing.T) {
	s := newServeFixture(t)
	unauth := func(name string, hdr map[string]string, path string) {
		t.Helper()
		w := s.get(path, hdr)
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
		assert.Contains(t, w.Header().Get("Content-Type"), "problem+json", name)
		assert.Contains(t, w.Header().Get("WWW-Authenticate"), "Bearer", name)
		assert.NotContains(t, w.Body.String(), "myrd_", name+": the token is never echoed")
		var p api.Problem
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
		assert.Equal(t, "unauthorized", p.Code)
	}
	unauth("no header", nil, s.path())
	unauth("wrong scheme", map[string]string{"Authorization": "Basic abc"}, s.path())
	unauth("garbage", map[string]string{"Authorization": "Bearer myrd_garbage"}, s.path())
	unauth("wrong prefix", map[string]string{"Authorization": "Bearer myra_" + strings.TrimPrefix(s.headers["Authorization"], "Bearer myrd_")}, s.path())

	// tampered MAC
	tok := strings.TrimPrefix(s.headers["Authorization"], "Bearer ")
	dot := strings.LastIndexByte(tok, '.')
	tampered := tok[:dot+1] + "AAAA" + tok[dot+5:]
	unauth("tampered mac", map[string]string{"Authorization": "Bearer " + tampered}, s.path())

	// token signed with another secret
	other := s.otherCache(func(d *Deps) { d.DownloadSecret = []byte("another-secret-another-secret-12") })
	otherTok, err := other.signToken(s.art.ID, s.agent, time.Now())
	require.NoError(t, err)
	unauth("other secret", map[string]string{"Authorization": "Bearer " + otherTok}, s.path())

	// token for another artifact id than the path
	otherArt := uuid.Must(uuid.NewV7())
	wrongArtTok, _ := s.cache.signToken(otherArt, s.agent, time.Now())
	unauth("token of another artifact", map[string]string{"Authorization": "Bearer " + wrongArtTok}, s.path())

	// a token for an agent without an offer for this artifact
	stranger := uuid.Must(uuid.NewV7())
	strangerTok, _ := s.cache.signToken(s.art.ID, stranger, time.Now())
	unauth("agent without offer", map[string]string{"Authorization": "Bearer " + strangerTok}, s.path())

	// offer withdrawn: the token stops working immediately
	require.NoError(t, s.st.Packages().ClearOffers(context.Background(), s.agent))
	unauth("offer cleared", s.headers, s.path())
	// offer for ANOTHER artifact does not help
	s.offer("https://fleet.example.com/api/v1/artifacts/" + otherArt.String() + "/content/x.tar.gz")
	unauth("offer for another artifact", s.headers, s.path())
	// offer back
	s.offer(s.url)
	assert.Equal(t, http.StatusOK, s.get(s.path(), s.headers).Code)

	assert.GreaterOrEqual(t, counter(t, s.fixture, "unauthorized"), 8.0)
}

func TestServeContentTokenTTL(t *testing.T) {
	// default: no expiry (offers are resent verbatim, updaters may come back later)
	s := newServeFixture(t, func(cfg *config.ArtifactsConfig) { cfg.DownloadTokenTTL = 0 })
	old, _ := s.cache.signToken(s.art.ID, s.agent, time.Now().Add(-365*24*time.Hour))
	assert.Equal(t, http.StatusOK, s.get(s.path(), map[string]string{"Authorization": "Bearer " + old}).Code, "TTL 0 = no expiry")

	s2 := newServeFixture(t, func(cfg *config.ArtifactsConfig) { cfg.DownloadTokenTTL = time.Hour })
	assert.Equal(t, http.StatusOK, s2.get(s2.path(), s2.headers).Code)
	old, _ = s2.cache.signToken(s2.art.ID, s2.agent, time.Now().Add(-2*time.Hour))
	w := s2.get(s2.path(), map[string]string{"Authorization": "Bearer " + old})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "expired")
}

func TestServeContentNotFound(t *testing.T) {
	s := newServeFixture(t)
	w := s.get("/api/v1/artifacts/"+s.art.ID.String()+"/content/other-name.tar.gz", s.headers)
	assert.Equal(t, http.StatusNotFound, w.Code, "the filename must match")
	w = s.get("/api/v1/artifacts/not-a-uuid/content/x", s.headers)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// a failed / downloading artifact is not served
	bad := *s.art
	bad.Status = domain.ArtifactFailed
	_, err := s.st.Artifacts().Upsert(context.Background(), bad)
	require.NoError(t, err)
	w = s.get(s.path(), s.headers)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestServeContentRefetchesMissingFileAndPinsTheHash(t *testing.T) {
	s := newServeFixture(t)
	file := s.cache.path(s.art)
	require.NoError(t, os.Remove(file))
	base := "/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz"
	before := s.up.count(base)

	w := s.get(s.path(), s.headers)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, s.archive, w.Body.Bytes())
	assert.Equal(t, before+1, s.up.count(base), "fetched from the source on demand (another replica prepared the artifact)")
	_, err := os.Stat(file)
	assert.NoError(t, err)
	// the second request is served from disk
	assert.Equal(t, http.StatusOK, s.get(s.path(), s.headers).Code)
	assert.Equal(t, before+1, s.up.count(base))

	// Upstream replaced the asset (releases are mutable): the pinned SHA-256 wins and the new bytes are refused.
	require.NoError(t, os.Remove(file))
	s.up.put(base, fakeArchive(100000)[:99999])
	w = s.get(s.path(), s.headers)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	_, err = os.Stat(file)
	assert.True(t, os.IsNotExist(err), "the changed file is not stored")
	same := append(fakeArchive(100000)[:99999], 0xAA) // same size, different content
	s.up.put(base, same)
	assert.Equal(t, http.StatusServiceUnavailable, s.get(s.path(), s.headers).Code)
	// upstream restored
	s.up.put(base, s.archive)
	assert.Equal(t, http.StatusOK, s.get(s.path(), s.headers).Code)
	assert.GreaterOrEqual(t, counter(t, s.fixture, "error"), 2.0)
}

func TestServeContentConcurrentRefetchDownloadsOnce(t *testing.T) {
	s := newServeFixture(t)
	require.NoError(t, os.Remove(s.cache.path(s.art)))
	base := "/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz"
	before := s.up.count(base)
	s.up.delay = 50 * time.Millisecond
	done := make(chan int, 8)
	for i := 0; i < 8; i++ {
		go func() { done <- s.get(s.path(), s.headers).Code }()
	}
	for i := 0; i < 8; i++ {
		assert.Equal(t, http.StatusOK, <-done)
	}
	assert.Equal(t, before+1, s.up.count(base))
}

// ----------------------------------------------------------------- prefetch ----

func TestPrefetchOnceEnsuresPlatformsAndProtectsTargetVersions(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) {
		cfg.PrefetchPlatforms = []string{"linux/amd64", "windows/amd64"}
		cfg.MaxCacheBytes = 3000
	})
	ctx := context.Background()
	// files are content addressed: every archive needs its own content
	unique := func(n int, tag byte) []byte { b := fakeArchive(n); b[0] = tag; return b }
	for i, p := range [][2]string{{"linux", "amd64"}, {"windows", "amd64"}, {"linux", "arm64"}} {
		f.up.release("0.162.0", p[0], p[1], unique(1000, byte(i+1)), []byte("b"))
	}
	f.up.release("0.161.0", "linux", "amd64", unique(1500, 11), []byte("b")) // old, evictable
	f.up.release("0.160.0", "linux", "amd64", unique(1500, 12), []byte("b"))
	for _, v := range []string{"0.161.0", "0.160.0"} {
		_, err := f.cache.Ensure(ctx, "otelcol-contrib", v, "linux", "amd64")
		require.NoError(t, err)
	}
	// make the two old files clearly older than the rest
	old := time.Now().Add(-48 * time.Hour)
	filePath := func(v string) string {
		rec, err := f.st.Artifacts().Find(ctx, "otelcol-contrib", v, "linux", "amd64", "tar.gz")
		require.NoError(t, err)
		return f.cache.path(rec)
	}
	for _, v := range []string{"0.161.0", "0.160.0"} {
		require.NoError(t, os.Chtimes(filePath(v), old, old))
	}
	require.NoError(t, os.Chtimes(filePath("0.160.0"), old.Add(-time.Hour), old.Add(-time.Hour)))

	// fleets: edge uses the updater (members on linux/arm64 add a platform), office uses AWX (not prefetched)
	snap := domain.GitOpsSnapshot{Commit: "c", Fleets: []domain.Fleet{
		{ObjectMeta: domain.ObjectMeta{Name: "edge"}, FleetSpec: domain.FleetSpec{Collector: domain.CollectorSpec{Version: "0.162.0"}, Update: domain.UpdateSpec{Executor: domain.ExecutorUpdater}}},
		{ObjectMeta: domain.ObjectMeta{Name: "office"}, FleetSpec: domain.FleetSpec{Collector: domain.CollectorSpec{Version: "0.163.0"}, Update: domain.UpdateSpec{Executor: domain.ExecutorAWX, ExecutorRef: "x"}}},
		{ObjectMeta: domain.ObjectMeta{Name: "unmanaged"}, FleetSpec: domain.FleetSpec{Update: domain.UpdateSpec{Executor: domain.ExecutorUpdater}}},
	}}
	_, err := f.st.GitOps().ApplySnapshot(ctx, snap, domain.GitOpsSync{})
	require.NoError(t, err)
	f.st.PutAgent(domain.Agent{ID: uuid.Must(uuid.NewV7()), Kind: domain.AgentKindCollector, OSType: "linux", Arch: "arm64",
		Assignment: domain.AgentAssignment{FleetName: "edge"}, IdentifyingAttributes: map[string]string{}, NonIdentifyingAttributes: map[string]string{}, Labels: map[string]string{}})

	require.NoError(t, f.cache.PrefetchOnce(ctx))
	list, _, err := f.st.Artifacts().List(ctx, store.ArtifactFilter{Version: "0.162.0"})
	require.NoError(t, err)
	var got []string
	for _, a := range list {
		got = append(got, a.OS+"/"+a.Arch)
		assert.Equal(t, domain.ArtifactReady, a.Status)
	}
	assert.ElementsMatch(t, []string{"linux/amd64", "windows/amd64", "linux/arm64"}, got, "configured platforms + member platforms")
	l163, _, _ := f.st.Artifacts().List(ctx, store.ArtifactFilter{Version: "0.163.0"})
	assert.Empty(t, l163, "AWX fleets do not use the artifact cache")

	// 3 x 1000 (target) + 2 x 1500 = 6000 > 3000: the oldest non-target files are evicted, targets stay.
	_, err = os.Stat(filePath("0.162.0"))
	assert.NoError(t, err, "target versions are never evicted")
	_, err = os.Stat(filePath("0.160.0"))
	assert.True(t, os.IsNotExist(err), "oldest evicted first")
	_, err = os.Stat(filePath("0.161.0"))
	assert.True(t, os.IsNotExist(err), "evicted until the cache fits... (3000 bytes of protected files remain)")
}

// TestPrefetchOnceFollowsTheEffectiveTarget: a version chosen in the UI is
// prefetched (and protected from eviction) next to the Git target; a fleet on
// hold prefetches nothing but still protects its Git version.
func TestPrefetchOnceFollowsTheEffectiveTarget(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.PrefetchPlatforms = []string{"linux/amd64"} })
	ctx := context.Background()
	for i, v := range []string{"0.161.0", "0.162.0", "0.163.0"} {
		b := fakeArchive(1000)
		b[0] = byte(i + 1)
		f.up.release(v, "linux", "amd64", b, []byte("b"))
	}
	fleet := func(name, git string) domain.Fleet {
		return domain.Fleet{ObjectMeta: domain.ObjectMeta{Name: name}, FleetSpec: domain.FleetSpec{
			Collector: domain.CollectorSpec{Version: git}, Update: domain.UpdateSpec{Executor: domain.ExecutorUpdater}}}
	}
	_, err := f.st.GitOps().ApplySnapshot(ctx, domain.GitOpsSnapshot{Commit: "c", Fleets: []domain.Fleet{fleet("gw", "0.161.0"), fleet("held", "0.161.0")}}, domain.GitOpsSync{})
	require.NoError(t, err)
	sel := func(fleet, version string, state domain.VersionSelectionState) {
		_, err := f.st.FleetVersions().Put(ctx, domain.FleetVersionSelection{Fleet: fleet, Version: version, State: state, SelectedBy: "user:x", SelectedAt: time.Now()}, nil)
		require.NoError(t, err)
	}
	sel("gw", "0.162.0", domain.SelectionActive)
	sel("held", "0.163.0", domain.SelectionInvalidated)

	require.NoError(t, f.cache.PrefetchOnce(ctx))
	have := func(v string) bool {
		list, _, err := f.st.Artifacts().List(ctx, store.ArtifactFilter{Version: v})
		require.NoError(t, err)
		return len(list) > 0
	}
	assert.True(t, have("0.162.0"), "the UI selection is the effective target")
	assert.False(t, have("0.163.0"), "an invalidated selection is no target")
	assert.False(t, have("0.161.0"), "the Git target of fleets that are not on it is not fetched")
}

func TestEvictRespectsLimitAndLRU(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.MaxCacheBytes = 2500 })
	mk := func(ver string, age time.Duration) string {
		p := filepath.Join(f.dir, "otelcol-contrib", ver, "otelcol-contrib_"+ver+"_linux_amd64.tar.gz")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, make([]byte, 1000), 0o644))
		ts := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(p, ts, ts))
		return p
	}
	a := mk("0.1.0", 3*time.Hour)
	b := mk("0.2.0", 2*time.Hour)
	c := mk("0.3.0", time.Hour)
	require.NoError(t, f.cache.Evict(context.Background(), nil))
	_, errA := os.Stat(a)
	_, errB := os.Stat(b)
	_, errC := os.Stat(c)
	assert.True(t, os.IsNotExist(errA), "least recently used goes first")
	assert.NoError(t, errB)
	assert.NoError(t, errC)

	// nothing to do below the limit
	require.NoError(t, f.cache.Evict(context.Background(), nil))
	_, errB = os.Stat(b)
	assert.NoError(t, errB)
}

func TestStartPrefetchAndDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(200), []byte("b"))
	f.up.release("0.162.0", "windows", "amd64", fakeArchive(200), []byte("b"))

	list, err := f.cache.StartPrefetch(ctx, "otelcol-contrib", "0.162.0", []string{"linux/amd64", "windows/amd64"})
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, a := range list {
		assert.Contains(t, []domain.ArtifactStatus{domain.ArtifactDownloading, domain.ArtifactReady}, a.Status)
		assert.NotEqual(t, uuid.Nil, a.ID)
	}
	require.Eventually(t, func() bool {
		l, _, _ := f.st.Artifacts().List(ctx, store.ArtifactFilter{Status: []domain.ArtifactStatus{domain.ArtifactReady}})
		return len(l) == 2
	}, 5*time.Second, 10*time.Millisecond)

	// ready artifacts are returned as they are
	again, err := f.cache.StartPrefetch(ctx, "otelcol-contrib", "0.162.0", []string{"linux/amd64"})
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactReady, again[0].Status)
	assert.Equal(t, list[0].ID, again[0].ID)

	_, err = f.cache.StartPrefetch(ctx, "otelcol-contrib", "0.162.0", []string{"linux"})
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)
	_, err = f.cache.StartPrefetch(ctx, "otelcol-contrib", "nope", []string{"linux/amd64"})
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)
	_, err = f.cache.StartPrefetch(ctx, "otelcol", "0.162.0", []string{"linux/amd64"})
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)

	// defaults come from the configuration
	f.cache.cfg.PrefetchPlatforms = []string{"linux/amd64"}
	def, err := f.cache.StartPrefetch(ctx, "otelcol-contrib", "0.162.0", nil)
	require.NoError(t, err)
	assert.Len(t, def, 1)
	_, plan, err := f.cache.StartPrefetchResolved(ctx, "otelcol-contrib", "0.162.0", nil)
	require.NoError(t, err)
	assert.Equal(t, PrefetchPlan{Platforms: []string{"linux/amd64"}, Source: PlatformsFromConfig}, plan)
	_, plan, err = f.cache.StartPrefetchResolved(ctx, "otelcol-contrib", "0.162.0", []string{"windows/amd64"})
	require.NoError(t, err)
	assert.Equal(t, PrefetchPlan{Platforms: []string{"windows/amd64"}, Source: PlatformsFromRequest}, plan)
	f.cache.cfg.PrefetchPlatforms = nil
	_, plan, err = f.cache.StartPrefetchResolved(ctx, "otelcol-contrib", "0.162.0", nil)
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)
	assert.Equal(t, PlatformsFromConnected, plan.Source)

	// delete removes record and file
	id := list[0].ID
	rec, _ := f.st.Artifacts().Get(ctx, id)
	file := f.cache.path(rec)
	require.FileExists(t, file)
	require.NoError(t, f.cache.Delete(ctx, id))
	_, err = f.st.Artifacts().Get(ctx, id)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = os.Stat(file)
	assert.True(t, os.IsNotExist(err))
	assert.ErrorIs(t, f.cache.Delete(ctx, id), domain.ErrNotFound)
}

func TestRunPrefetchStopsOnCancel(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.cache.RunPrefetch(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("RunPrefetch did not stop")
	}
}
