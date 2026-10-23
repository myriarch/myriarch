// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

// upstream simulates the release asset host.
type upstream struct {
	srv      *httptest.Server
	mu       sync.Mutex
	files    map[string][]byte // path -> body
	requests map[string]int
	status   map[string]int // path -> forced status
	delay    time.Duration
	// gate, when set, holds every request until it is closed
	gate chan struct{}
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{files: map[string][]byte{}, requests: map[string]int{}, status: map[string]int{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requests[r.URL.Path]++
		body, ok := u.files[r.URL.Path]
		forced := u.status[r.URL.Path]
		delay := u.delay
		gate := u.gate
		u.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		switch {
		case forced != 0:
			http.Error(w, "forced", forced)
		case !ok:
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// block holds every request of the upstream until the returned function is called (it may be called more than once).
func (u *upstream) block() (release func()) {
	g := make(chan struct{})
	u.mu.Lock()
	u.gate = g
	u.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(g) }) }
}

func (u *upstream) put(path string, body []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.files[path] = body
}

func (u *upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests[path]
}

// release publishes archive + checksum + bundle for a contrib asset.
func (u *upstream) release(version, os_, arch string, archive, bundle []byte) string {
	name := fmt.Sprintf("otelcol-contrib_%s_%s_%s.tar.gz", version, os_, arch)
	base := "/v" + version + "/" + name
	sum := sha256.Sum256(archive)
	u.put(base, archive)
	u.put(base+".sha256", []byte(hex.EncodeToString(sum[:])))
	u.put(base+".sigstore.json", bundle)
	return base
}

type fixture struct {
	t      *testing.T
	up     *upstream
	st     *memstore.Store
	bus    eventbus.Bus
	cache  *Cache
	dir    string
	secret []byte
	reg    *prometheus.Registry
	verify atomic.Int32
	cfg    config.ArtifactsConfig
	deps   Deps
}

// otherCache builds a second cache on the same directory/store with changed dependencies.
func (f *fixture) otherCache(mut func(d *Deps)) *Cache {
	d := f.deps
	d.Registerer = nil
	mut(&d)
	c, err := NewCache(f.cfg, d)
	require.NoError(f.t, err)
	return c
}

func newFixture(t *testing.T, mut ...func(cfg *config.ArtifactsConfig)) *fixture {
	t.Helper()
	f := &fixture{t: t, up: newUpstream(t), st: memstore.New(), bus: eventbus.NewMemory("t", 0), dir: t.TempDir(), secret: []byte("0123456789abcdef0123456789abcdef"), reg: prometheus.NewRegistry()}
	t.Cleanup(func() { f.bus.Close() })
	cfg := config.Default().Artifacts
	cfg.Dir = f.dir
	cfg.SourceBaseURL = f.up.srv.URL
	cfg.Verify.TrustedRootFile = filepath.Join(sigDir, "trusted_root.json")
	cfg.MaxCacheBytes = 1 << 30
	for _, m := range mut {
		m(&cfg)
	}
	f.cfg = cfg
	f.deps = Deps{
		Store: f.st, Bus: f.bus, HTTPClient: http.DefaultClient, PublicURL: "https://fleet.example.com",
		DownloadSecret: f.secret, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: f.reg,
	}
	c, err := NewCache(cfg, f.deps)
	require.NoError(t, err)
	f.cache = c
	// Background downloads (Prepare, StartPrefetch) write into f.dir; wait for them before t.TempDir removes it.
	t.Cleanup(func() {
		wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		assert.NoError(t, c.WaitPrepared(wctx))
	})
	// Real archives are too big to ship, real bundles only match real
	// archives: the flow tests stub the verification (verify_test.go covers
	// the real verification with real bundles).
	if c.ver != nil {
		c.verifyFn = func(bundle, digest []byte, dist, version string) (*verified, error) {
			f.verify.Add(1)
			if string(bundle) == "reject" {
				return nil, fmt.Errorf("sigstore verification failed: bad signature")
			}
			return &verified{Identity: "https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v" + version}, nil
		}
	}
	return f
}

func fakeArchive(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestEnsureDownloadsVerifiesAndStores(t *testing.T) {
	f := newFixture(t)
	archive := fakeArchive(5000)
	base := f.up.release("0.162.0", "linux", "amd64", archive, []byte(`{"bundle":true}`))
	events, cancel := f.bus.Subscribe(eventbus.ArtifactChanged)
	defer cancel()

	a, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	sum := sha256.Sum256(archive)
	assert.Equal(t, domain.ArtifactReady, a.Status)
	assert.Equal(t, "otelcol-contrib_0.162.0_linux_amd64.tar.gz", a.Filename)
	assert.Equal(t, "tar.gz", a.Format)
	assert.Equal(t, int64(5000), a.Size)
	assert.Equal(t, domain.Hash(sum[:]), a.SHA256)
	assert.True(t, a.SignatureVerified)
	assert.Contains(t, a.SignatureIdentity, "base-release.yaml@refs/tags/v0.162.0")
	assert.Equal(t, []byte(`{"bundle":true}`), a.SigstoreBundle, "the bundle is stored for the updaters")
	assert.Equal(t, f.up.srv.URL+base, a.SourceURL)
	assert.Empty(t, a.Error)
	assert.NotEqual(t, uuid.Nil, a.ID)

	onDisk, err := os.ReadFile(f.cache.path(a))
	assert.Equal(t, filepath.Join(f.dir, "sha256", a.SHA256.String()), f.cache.path(a), "content addressed storage")
	require.NoError(t, err)
	assert.Equal(t, archive, onDisk)
	tmp, _ := os.ReadDir(filepath.Join(f.dir, ".tmp"))
	assert.Empty(t, tmp, "no temporary files remain")

	stored, err := f.st.Artifacts().Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactReady, stored.Status)

	// downloading, then ready are announced
	var statuses []domain.ArtifactStatus
	for len(events) > 0 {
		var pl eventbus.ArtifactChangedPayload
		require.NoError(t, (<-events).Decode(&pl))
		assert.Equal(t, a.ID, pl.ArtifactID)
		statuses = append(statuses, pl.Status)
	}
	assert.Equal(t, []domain.ArtifactStatus{domain.ArtifactDownloading, domain.ArtifactReady}, statuses)

	// second call: from the database, nothing is downloaded again
	a2, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "v0.162.0", "LINUX", "x86_64")
	require.NoError(t, err)
	assert.Equal(t, a.ID, a2.ID, "inputs are normalized (v prefix, case, x86_64 -> amd64)")
	assert.Equal(t, 1, f.up.count(base))
	assert.Equal(t, int32(1), f.verify.Load())
}

func TestEnsureWindowsUsesTarGzToo(t *testing.T) {
	f := newFixture(t)
	f.up.release("0.162.0", "windows", "amd64", fakeArchive(100), []byte("b"))
	a, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "windows", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "otelcol-contrib_0.162.0_windows_amd64.tar.gz", a.Filename)
	assert.Equal(t, "tar.gz", a.Format)
}

func TestAssetNames(t *testing.T) {
	s, err := newSpec("opampsupervisor", "0.162.0", "windows", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "opampsupervisor_0.162.0_windows_amd64.exe", s.filename)
	assert.Equal(t, "cmd/opampsupervisor/v0.162.0", s.tag)
	assert.Equal(t, "binary", s.format)
	s, err = newSpec("opampsupervisor", "0.162.0", "linux", "arm64")
	require.NoError(t, err)
	assert.Equal(t, "opampsupervisor_0.162.0_linux_arm64", s.filename)

	s, err = newSpec("otelcol-contrib", "0.162.0", "linux", "x86")
	require.NoError(t, err)
	assert.Equal(t, "otelcol-contrib_0.162.0_linux_386.tar.gz", s.filename)
	assert.Equal(t, "v0.162.0", s.tag)

	for _, bad := range [][4]string{
		{"otelcol-contrib", "latest", "linux", "amd64"},
		{"otelcol-contrib", "0.162", "linux", "amd64"},
		{"otelcol-contrib", "../0.162.0", "linux", "amd64"},
		{"otelcol", "0.162.0", "linux", "amd64"},
		{"otelcol-contrib", "0.162.0", "plan9", "amd64"},
		{"otelcol-contrib", "0.162.0", "linux", "../../etc"},
		{"otelcol-contrib", "0.162.0", "linux", ""},
	} {
		_, err := newSpec(bad[0], bad[1], bad[2], bad[3])
		assert.ErrorIs(t, err, domain.ErrInvalidArgument, "%v", bad)
	}
	assert.Equal(t, "amd64", NormalizeArch(" X86_64 "))
	assert.Equal(t, "arm64", NormalizeArch("aarch64"))
	assert.Equal(t, "386", NormalizeArch("x86"))
	assert.Equal(t, "armv7", NormalizeArch("arm32"))
}

func TestChecksumMismatchFailsAndLeavesNothing(t *testing.T) {
	f := newFixture(t)
	archive := fakeArchive(1000)
	base := f.up.release("0.162.0", "linux", "amd64", archive, []byte("b"))
	wrong := sha256.Sum256([]byte("something else"))
	f.up.put(base+".sha256", []byte(hex.EncodeToString(wrong[:])+"  otelcol-contrib_0.162.0_linux_amd64.tar.gz\n"))

	_, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
	a, ferr := f.st.Artifacts().Find(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64", "tar.gz")
	require.NoError(t, ferr)
	assert.Equal(t, domain.ArtifactFailed, a.Status)
	assert.Contains(t, a.Error, "checksum mismatch")
	assert.False(t, a.SignatureVerified)
	_, serr := os.Stat(filepath.Join(f.dir, "otelcol-contrib", "0.162.0", a.Filename))
	assert.True(t, os.IsNotExist(serr))
	tmp, _ := os.ReadDir(filepath.Join(f.dir, ".tmp"))
	assert.Empty(t, tmp)

	// After the upstream is fixed the next Ensure succeeds (failed is not terminal).
	f.up.release("0.162.0", "linux", "amd64", archive, []byte("b"))
	a2, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactReady, a2.Status)
	assert.Equal(t, a.ID, a2.ID, "same record is reused")
	assert.Empty(t, a2.Error)
}

func TestSignatureRejectionIsStoredAsVerificationError(t *testing.T) {
	f := newFixture(t)
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(100), []byte("reject"))
	_, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	a, _ := f.st.Artifacts().Find(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64", "tar.gz")
	assert.Equal(t, domain.ArtifactFailed, a.Status)
	assert.Contains(t, a.VerificationError, "sigstore verification failed")
	assert.False(t, a.SignatureVerified)
	// the archive was never downloaded: the signature is checked first (digest from .sha256)
	assert.Equal(t, 0, f.up.count("/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz"))
}

// End to end with the real verifier: the real v0.162.0 bundle and checksum
// of the linux/amd64 archive, but a fake archive body: the checksum check
// rejects it. With a matching fake checksum the REAL signature verification
// rejects the bundle (digest mismatch).
func TestRealVerifierRejectsFakeArchives(t *testing.T) {
	f := newFixture(t)
	f.cache.verifyFn = f.cache.ver.verifyBundle // the real thing
	realBundle := readSig(t, contribBundle)
	base := "/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz"
	f.up.put(base, fakeArchive(2000))
	f.up.put(base+".sigstore.json", realBundle)

	// (1) real checksum file (digest of the real archive): signature checks out, archive does not
	f.up.put(base+".sha256", readSig(t, contribSHA))
	_, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")

	// (2) fake checksum that matches the fake archive: the real bundle does not cover it
	sum := sha256.Sum256(fakeArchive(2000))
	f.up.put(base+".sha256", []byte(hex.EncodeToString(sum[:])))
	_, err = f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sigstore verification failed")
	a, _ := f.st.Artifacts().Find(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64", "tar.gz")
	assert.NotEmpty(t, a.VerificationError)
	assert.Equal(t, domain.ArtifactFailed, a.Status)

	// (3) the real bundle for another version is rejected by the tag binding
	other := "/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz"
	f.up.put(other, fakeArchive(10))
	f.up.put(other+".sigstore.json", realBundle)
	f.up.put(other+".sha256", readSig(t, contribSHA))
	_, err = f.cache.Ensure(context.Background(), "otelcol-contrib", "0.161.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not bound to tag v0.161.0")
}

func TestDownloadErrors(t *testing.T) {
	cases := map[string]func(f *fixture, base string){
		"checksum 404": func(f *fixture, base string) { f.up.mu.Lock(); delete(f.up.files, base+".sha256"); f.up.mu.Unlock() },
		"bundle 404": func(f *fixture, base string) {
			f.up.mu.Lock()
			delete(f.up.files, base+".sigstore.json")
			f.up.mu.Unlock()
		},
		"archive 404":      func(f *fixture, base string) { f.up.mu.Lock(); delete(f.up.files, base); f.up.mu.Unlock() },
		"archive 500":      func(f *fixture, base string) { f.up.mu.Lock(); f.up.status[base] = 500; f.up.mu.Unlock() },
		"checksum garbage": func(f *fixture, base string) { f.up.put(base+".sha256", []byte("not a hash")) },
		"checksum empty":   func(f *fixture, base string) { f.up.put(base+".sha256", nil) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			base := f.up.release("0.162.0", "linux", "amd64", fakeArchive(100), []byte("b"))
			mut(f, base)
			_, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
			require.Error(t, err)
			a, _ := f.st.Artifacts().Find(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64", "tar.gz")
			require.NotNil(t, a)
			assert.Equal(t, domain.ArtifactFailed, a.Status)
			assert.NotEmpty(t, a.Error)
		})
	}
}

func TestTruncatedDownloadIsRejected(t *testing.T) {
	f := newFixture(t)
	archive := fakeArchive(100000)
	f.up.release("0.162.0", "linux", "amd64", archive, []byte("b"))
	// A server that announces the full length but cuts the stream.
	var cut atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.up.mu.Lock()
		body := f.up.files[r.URL.Path]
		f.up.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".tar.gz") && cut.Load() {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body[:len(body)/2])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	cut.Store(true)
	f2 := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.SourceBaseURL = srv.URL })
	f2.up = f.up
	_, err := f2.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	tmp, _ := os.ReadDir(filepath.Join(f2.dir, ".tmp"))
	assert.Empty(t, tmp)
}

func TestConcurrentEnsureDownloadsOnce(t *testing.T) {
	f := newFixture(t)
	base := f.up.release("0.162.0", "linux", "amd64", fakeArchive(50000), []byte("b"))
	f.up.delay = 50 * time.Millisecond
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, 12)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
			if assert.NoError(t, err) {
				ids[i] = a.ID
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, f.up.count(base), "one download for all callers")
	assert.Equal(t, int32(1), f.verify.Load())
	for _, id := range ids {
		assert.Equal(t, ids[0], id)
	}
}

func TestEnsureHonoursCallerContextButFinishesTheSharedWork(t *testing.T) {
	f := newFixture(t)
	base := f.up.release("0.162.0", "linux", "amd64", fakeArchive(1000), []byte("b"))
	f.up.delay = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := f.cache.Ensure(ctx, "otelcol-contrib", "0.162.0", "linux", "amd64")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	// the shared download continues and completes for the next caller
	a, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactReady, a.Status)
	assert.Equal(t, 1, f.up.count(base))
}

func TestChecksumAndSignatureCanBeDisabled(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) {
		cfg.Verify.RequireChecksum = false
		cfg.Verify.RequireSignature = false
	})
	f.up.put("/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz", fakeArchive(300))
	a, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	assert.False(t, a.SignatureVerified)
	sum := sha256.Sum256(fakeArchive(300))
	assert.Equal(t, domain.Hash(sum[:]), a.SHA256, "the computed hash is pinned")
}

func TestSignatureWithoutChecksumFileUsesComputedDigest(t *testing.T) {
	f := newFixture(t, func(cfg *config.ArtifactsConfig) { cfg.Verify.RequireChecksum = false })
	// RequireSignature still true: the checksum file is needed to verify before the download, so it is read anyway
	f.up.release("0.162.0", "linux", "amd64", fakeArchive(300), []byte("b"))
	a, err := f.cache.Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	assert.True(t, a.SignatureVerified)
}

func TestSupervisorAssets(t *testing.T) {
	f := newFixture(t)
	body := fakeArchive(500)
	sum := sha256.Sum256(body)
	base := "/cmd/opampsupervisor/v0.162.0/opampsupervisor_0.162.0_windows_amd64.exe"
	f.up.put(base, body)
	f.up.put(base+".sha256", []byte(hex.EncodeToString(sum[:])))
	f.up.put(base+".sigstore.json", []byte("b"))
	a, err := f.cache.Ensure(context.Background(), "opampsupervisor", "0.162.0", "windows", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "binary", a.Format)
	assert.Equal(t, "opampsupervisor_0.162.0_windows_amd64.exe", a.Filename)
}

func TestNewCacheValidation(t *testing.T) {
	cfg := config.Default().Artifacts
	cfg.Dir = t.TempDir()
	cfg.ProxyURL = "::bad"
	_, err := NewCache(cfg, Deps{})
	assert.Error(t, err)
	cfg.ProxyURL = ""
	cfg.Verify.TrustedRootFile = "/does/not/exist.json"
	_, err = NewCache(cfg, Deps{})
	assert.Error(t, err, "unusable trusted root is a startup error")
	cfg.Verify.RequireSignature = false
	c, err := NewCache(cfg, Deps{})
	require.NoError(t, err)
	assert.NotNil(t, c)
	_, err = os.Stat(filepath.Join(cfg.Dir, ".tmp"))
	assert.NoError(t, err)
}

func sumHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
