// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/netguard"
)

// ------------------------------------------------------------------- fixtures ----

const (
	// The assets checksums.txt and checksums.txt.sigstore.json of the published release v0.1.0 of the project, byte for
	// byte (GitHub reports the digests below for the two assets of the immutable release), and the offline trusted
	// root that the artifact tests use. Real signature, real certificate, no network.
	sigDir          = "testdata/sigstore"
	trustedRootPath = "../artifact/testdata/sigstore/trusted_root.json"
	releaseSums     = "myriarch_0.1.0_checksums.txt"
	releaseSumsSHA  = "30c607b8e569174b71ea8b6e3810ed28d5b685cc9d1f0aaf4769684ec161ced4"
	releaseBundle   = "myriarch_0.1.0_checksums.txt.sigstore.json"
	releaseBundleSH = "cf2e1e799c8885f96e67730aa90e248c6b554d579afed069f8131124d5fc532a"
	// a bundle of the upstream release workflow over a small file (the artifact tests use them)
	upstreamBlob   = "../artifact/testdata/sigstore/otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256"
	upstreamBundle = "../artifact/testdata/sigstore/otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256.sigstore.json"
)

// quiet is a logger that writes nowhere: the tests look at the state, not at the log.
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// loadRelease reads the fixture of the release v0.1.0 and checks that it is what the release published.
func loadRelease(t *testing.T) (sums, bundle []byte) {
	t.Helper()
	sums = readFile(t, filepath.Join(sigDir, releaseSums))
	bundle = readFile(t, filepath.Join(sigDir, releaseBundle))
	require.Equal(t, releaseSumsSHA, sumHex(sums), "the fixture %s differs from the release asset (a checkout that converts line endings changes it)", releaseSums)
	require.Equal(t, releaseBundleSH, sumHex(bundle), "the fixture %s differs from the release asset", releaseBundle)
	return sums, bundle
}

// realVerifier verifies with the Sigstore trust of the artifact cache, offline.
func realVerifier(t *testing.T) BundleVerifier {
	t.Helper()
	cfg := config.Default().Artifacts
	cfg.Dir = t.TempDir()
	cfg.Verify.TrustedRootFile = trustedRootPath
	c, err := artifact.NewCache(cfg, artifact.Deps{})
	require.NoError(t, err)
	return CacheVerifier(c)
}

// fakeVerifier accepts what it is told to and records the calls.
type fakeVerifier struct {
	mu    sync.Mutex
	err   error
	calls []verifyCall
}

type verifyCall struct {
	bundle, digest []byte
	id             artifact.Identity
}

func (f *fakeVerifier) Verify(bundle, digest []byte, id artifact.Identity) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, verifyCall{bundle: bundle, digest: append([]byte(nil), digest...), id: id})
	if f.err != nil {
		return "", f.err
	}
	return "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/" + id.Tag, nil
}

func (f *fakeVerifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// releaseSource is the server of install.bundleSourceBaseUrl: it serves files by path and counts the requests.
type releaseSource struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	files  map[string][]byte
	hits   map[string]int
	status map[string]int
	reqs   []*http.Request
}

func newReleaseSource(t *testing.T) *releaseSource {
	t.Helper()
	r := &releaseSource{t: t, files: map[string][]byte{}, hits: map[string]int{}, status: map[string]int{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits[req.URL.Path]++
		r.reqs = append(r.reqs, req)
		body, ok := r.files[req.URL.Path]
		forced := r.status[req.URL.Path]
		r.mu.Unlock()
		switch {
		case forced != 0:
			w.WriteHeader(forced)
		case !ok:
			http.NotFound(w, req)
		default:
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *releaseSource) put(path string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[path] = body
}

func (r *releaseSource) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[path]
}

func (r *releaseSource) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.hits {
		n += c
	}
	return n
}

// archiveHits counts the requests for archives.
func (r *releaseSource) archiveHits() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for p, c := range r.hits {
		if strings.Contains(p, brandUpdater) {
			n += c
		}
	}
	return n
}

const brandUpdater = "myriarch-updater_"

var allPlatforms = []Platform{{"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}}

// syntheticRelease is a release whose archives are small and whose checksums.txt lists them (and other assets, among
// them the bare binary with the name of the archive without its extension).
type syntheticRelease struct {
	version  string
	archives map[string][]byte // by file name
	sums     []byte
}

func buildRelease(version string, plats []Platform) syntheticRelease {
	r := syntheticRelease{version: version, archives: map[string][]byte{}}
	var lines []string
	lines = append(lines, sumHex([]byte("license"))+"  LICENSE")
	names := []string{}
	for _, p := range plats {
		name := BundleFilename(version, p)
		names = append(names, name)
		r.archives[name] = bytes.Repeat([]byte(name+"|"), 40)
		// the bare binary of the same platform: another asset, another hash
		bare := strings.TrimSuffix(strings.TrimSuffix(name, ".tar.gz"), ".zip")
		lines = append(lines, sumHex([]byte("bare "+bare))+"  "+bare)
	}
	sort.Strings(names)
	for _, n := range names {
		lines = append(lines, sumHex(r.archives[n])+"  "+n)
		lines = append(lines, sumHex([]byte("sbom "+n))+"  "+n+".sbom.json")
	}
	lines = append(lines, sumHex([]byte("daemon"))+"  myriarchd_"+version+"_linux_amd64.tar.gz")
	r.sums = []byte(strings.Join(lines, "\n") + "\n")
	return r
}

// publish puts the release on the source under /v<version>/.
func (r *releaseSource) publish(rel syntheticRelease) {
	r.put("/v"+rel.version+"/checksums.txt", rel.sums)
	r.put("/v"+rel.version+"/checksums.txt.sigstore.json", []byte(`{"fake":"bundle"}`))
	for name, body := range rel.archives {
		r.put("/v"+rel.version+"/"+name, body)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type bundlesEnv struct {
	t     *testing.T
	dir   string
	src   *releaseSource
	ver   *fakeVerifier
	clock *fakeClock
	rel   syntheticRelease
	b     *Bundles
}

const testVersion = "0.2.0"

// newBundlesEnv builds the bundles of a server of version 0.2.0 whose release source serves a synthetic release and
// whose verifier accepts everything, unless mut changes that.
func newBundlesEnv(t *testing.T, mut ...func(e *bundlesEnv, d *BundleDeps)) *bundlesEnv {
	t.Helper()
	e := &bundlesEnv{t: t, dir: t.TempDir(), src: newReleaseSource(t), ver: &fakeVerifier{}, clock: newFakeClock()}
	e.rel = buildRelease(testVersion, allPlatforms)
	e.src.publish(e.rel)
	d := BundleDeps{Logger: quiet(), Version: testVersion, Dir: e.dir, BaseURL: e.src.srv.URL, Platforms: allPlatforms, Verifier: e.ver,
		HTTPClient: e.src.srv.Client(), now: e.clock.Now}
	for _, m := range mut {
		m(e, &d)
	}
	var err error
	e.b, err = NewBundles(d)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = e.b.Wait(ctx) // nothing writes into the directory of the test after it ended
	})
	return e
}

// prepare starts the preparation and waits for it.
func (e *bundlesEnv) prepare() {
	e.t.Helper()
	e.b.Prepare()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(e.t, e.b.Wait(ctx))
}

func (e *bundlesEnv) bundle(os, arch string) Bundle {
	e.t.Helper()
	b, ok := e.b.Find(Platform{os, arch})
	require.True(e.t, ok, "%s/%s", os, arch)
	return b
}

func (e *bundlesEnv) storeFiles() []string {
	e.t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.dir, artifact.InstallDir, "sha256"))
	require.NoError(e.t, err)
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func (e *bundlesEnv) tmpFiles() []string {
	e.t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.dir, artifact.InstallDir, ".tmp"))
	require.NoError(e.t, err)
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func readAll(t *testing.T, f *os.File) []byte {
	t.Helper()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return b
}

// ------------------------------------------------------------ versions and identity ----

func TestIsReleaseVersion(t *testing.T) {
	for _, v := range []string{"0.2.0", "0.2.0-rc.1", "0.2.0-rc.0", "0.2.1", "0.10.0", "1.0.0", "1.2.3-rc.10", "10.20.30"} {
		assert.True(t, IsReleaseVersion(v), v)
	}
	for _, v := range []string{
		"", "dev", "0.0.0-dev", "0.1.0", "0.1.9", "0.1.0-rc.4", "0.2.0-0", "v0.2.0", "0.2", "1.0", "0.2.0+build", "0.2.0-dev", "0.2.0-beta.1",
		"0.2.0-rc", "0.2.0-rc.01", "0.2.0-rc.1.2", "0.2.0-3-g1234567", "0.2.0-rc.1-dirty", "v0.2.0-3-g1234567-dirty", "00.2.0", "0.02.0", "0.2.00",
		" 0.2.0", "0.2.0 ", "0.2.0\n", "0.2.0/../x", "0.2.0-rc.1+x",
	} {
		assert.False(t, IsReleaseVersion(v), "%q", v)
	}
}

func TestReleaseIdentityIsDerivedFromTheRepository(t *testing.T) {
	id := ReleaseIdentity("0.2.0-rc.1")
	assert.Equal(t, `^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.2\.0-rc\.1$`, id.SANRegexp)
	assert.Equal(t, "https://token.actions.githubusercontent.com", id.Issuer)
	assert.Equal(t, "v0.2.0-rc.1", id.Tag)
	// the repository comes from the brand package: a fork that changes it gets its own identity
	assert.Equal(t, "^"+regexp.QuoteMeta(brand.Repository)+`/\.github/workflows/release\.yml@refs/tags/v0\.2\.0-rc\.1$`, id.SANRegexp)
	re := regexp.MustCompile(id.SANRegexp)
	assert.True(t, re.MatchString("https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.0-rc.1"))
	for _, other := range []string{
		"https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.0-rc.10",
		"https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.0",
		"https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/heads/main",
		"https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.0-rc.1/x",
		"https://github.com/myriarch/myriarch2/.github/workflows/release.yml@refs/tags/v0.2.0-rc.1",
		"https://github.com/other/myriarch/.github/workflows/release.yml@refs/tags/v0.2.0-rc.1",
		"https://github.com/myriarch/myriarch/.github/workflows/ci.yml@refs/tags/v0.2.0-rc.1",
		"x https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.0-rc.1",
	} {
		assert.False(t, re.MatchString(other), other)
	}
}

// The real signature of checksums.txt of v0.1.0 is accepted for v0.1.0 and for nothing else.
func TestReleaseIdentityAgainstTheRealSignature(t *testing.T) {
	sums, bundle := loadRelease(t)
	digest := sha256.Sum256(sums)
	v := realVerifier(t)

	san, err := v.Verify(bundle, digest[:], ReleaseIdentity("0.1.0"))
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.1.0", san)

	for _, other := range []string{"0.1.1", "0.1.0-rc.3", "0.10.0", "0.2.0", "1.1.0", "0.1.0-rc.1"} {
		_, err := v.Verify(bundle, digest[:], ReleaseIdentity(other))
		require.Error(t, err, other)
		assert.Contains(t, err.Error(), "sigstore verification failed", other)
	}
	// another repository or workflow in the pattern: not the identity of the signature
	for name, re := range map[string]string{
		"other repository": `^https://github\.com/evil/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.1\.0$`,
		"other workflow":   `^https://github\.com/myriarch/myriarch/\.github/workflows/ci\.yml@refs/tags/v0\.1\.0$`,
		"other host":       `^https://git.example.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.1\.0$`,
	} {
		id := ReleaseIdentity("0.1.0")
		id.SANRegexp = re
		_, err := v.Verify(bundle, digest[:], id)
		require.Error(t, err, name)
	}
	// another issuer
	id := ReleaseIdentity("0.1.0")
	id.Issuer = "https://accounts.example.com"
	_, err = v.Verify(bundle, digest[:], id)
	require.Error(t, err)
	// another file
	tampered := append(append([]byte(nil), sums...), []byte("0000000000000000000000000000000000000000000000000000000000000000  extra\n")...)
	d2 := sha256.Sum256(tampered)
	_, err = v.Verify(bundle, d2[:], ReleaseIdentity("0.1.0"))
	require.Error(t, err)
	// the bundle of the upstream release workflow is not the bundle of this project
	blob := readFile(t, upstreamBlob)
	d3 := sha256.Sum256(blob)
	_, err = v.Verify(readFile(t, upstreamBundle), d3[:], ReleaseIdentity("0.162.0"))
	require.Error(t, err)
}

func TestBundleFilenames(t *testing.T) {
	assert.Equal(t, "myriarch-updater_0.2.0_linux_amd64.tar.gz", BundleFilename("0.2.0", Platform{"linux", "amd64"}))
	assert.Equal(t, "myriarch-updater_0.2.0-rc.1_linux_arm64.tar.gz", BundleFilename("0.2.0-rc.1", Platform{"linux", "arm64"}))
	assert.Equal(t, "myriarch-updater_0.2.0_windows_amd64.zip", BundleFilename("0.2.0", Platform{"windows", "amd64"}))
	for _, ok := range []string{
		"myriarch-updater_0.2.0_linux_amd64.tar.gz", "myriarch-updater_0.2.0-rc.1_windows_amd64.zip", "myriarch-updater_0.0.0-dev_linux_arm64.tar.gz",
	} {
		assert.True(t, ValidBundleFilename(ok), ok)
	}
	for _, bad := range []string{
		"", "checksums.txt", "myriarch-updater_0.2.0_linux_amd64", "myriarch-updater_0.2.0_linux_amd64.zip", "myriarch-updater_0.2.0_windows_amd64.tar.gz",
		"myriarch-updater_0.2.0_darwin_arm64.tar.gz", "myriarch-updater_0.2.0_linux_386.tar.gz", "myriarch-updater_x_linux_amd64.tar.gz",
		"../myriarch-updater_0.2.0_linux_amd64.tar.gz", "myriarch-updater_0.2.0_linux_amd64.tar.gz/../x", "myriarch-updater_0.2.0_linux_amd64.tar.gz.sha256",
		"myriarch-updater_0.2.0_linux_amd64.tar.gz\n", "xmyriarch-updater_0.2.0_linux_amd64.tar.gz", "myriarch-updater_v0.2.0_linux_amd64.tar.gz",
		"myriarch-updater_" + strings.Repeat("1", 120) + "_linux_amd64.tar.gz",
	} {
		assert.False(t, ValidBundleFilename(bad), "%q", bad)
	}
}

// ------------------------------------------------------------------- the release ----

func TestBundlesPrepareTheBundlesOfTheRelease(t *testing.T) {
	e := newBundlesEnv(t)
	st := e.b.State()
	assert.Equal(t, BundleSourceRelease, st.Source)
	assert.Equal(t, testVersion, st.Version)
	require.Len(t, st.Items, 3)
	for _, it := range st.Items {
		assert.Equal(t, BundlePending, it.Status)
	}
	assert.Zero(t, e.src.total(), "nothing is fetched before somebody asks")

	e.prepare()

	st = e.b.State()
	require.Len(t, st.Items, 3)
	for i, p := range allPlatforms {
		it := st.Items[i]
		name := BundleFilename(testVersion, p)
		assert.Equal(t, Bundle{OS: p.OS, Arch: p.Arch, Version: testVersion, Filename: name, Status: BundleReady,
			SHA256: sumHex(e.rel.archives[name]), Size: int64(len(e.rel.archives[name]))}, it)
		path := filepath.Join(e.dir, artifact.InstallDir, "sha256", it.SHA256)
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
		assert.Equal(t, e.rel.archives[name], readFile(t, path))
	}
	assert.Len(t, e.storeFiles(), 3)
	assert.Empty(t, e.tmpFiles(), "the downloads were moved or removed")

	// the signature was checked once, over the digest of checksums.txt that was downloaded, with the identity of the
	// release workflow at the tag of this version
	require.Equal(t, 1, e.ver.count())
	call := e.ver.calls[0]
	assert.Equal(t, sha256.Sum256(e.rel.sums), [32]byte(call.digest))
	assert.Equal(t, ReleaseIdentity(testVersion), call.id)
	assert.Equal(t, []byte(`{"fake":"bundle"}`), call.bundle)
	// one request for each of the two documents and one for each archive, and no other
	assert.Equal(t, 5, e.src.total())
	for _, p := range []string{"/v0.2.0/checksums.txt", "/v0.2.0/checksums.txt.sigstore.json"} {
		assert.Equal(t, 1, e.src.count(p), p)
	}
	for _, r := range e.src.reqs {
		assert.Contains(t, r.Header.Get("User-Agent"), "myriarchd/")
		assert.Empty(t, r.Header.Get("Authorization"), "the source gets no credential")
	}

	// a second call has nothing left to do
	e.prepare()
	assert.Equal(t, 5, e.src.total())
	assert.Equal(t, 1, e.ver.count())
}

func TestBundlesDeliverTheBytesOfTheRelease(t *testing.T) {
	e := newBundlesEnv(t)
	e.prepare()
	for _, p := range allPlatforms {
		name := BundleFilename(testVersion, p)
		f, b, err := e.b.Open(name)
		require.NoError(t, err, name)
		assert.Equal(t, e.rel.archives[name], readAll(t, f), "the bytes of the release, unchanged")
		assert.Equal(t, BundleReady, b.Status)
		assert.Equal(t, sumHex(e.rel.archives[name]), b.SHA256)
		require.NoError(t, f.Close())
	}
	_, _, err := e.b.Open("myriarch-updater_0.2.0_darwin_arm64.tar.gz")
	assert.ErrorIs(t, err, ErrBundleNotFound)
	_, _, err = e.b.Open("../../etc/passwd")
	assert.ErrorIs(t, err, ErrBundleNotFound)
	_, _, err = e.b.Open("checksums.txt")
	assert.ErrorIs(t, err, ErrBundleNotFound)
	_, _, err = e.b.Open("myriarch-updater_0.1.0_linux_amd64.tar.gz")
	assert.ErrorIs(t, err, ErrBundleNotFound, "the bundles of another version are not offered")
}

// A bundle that is not ready yet is a refusal with Retry-After, and the request starts the preparation.
func TestBundlesAreNotDeliveredBeforeTheyAreReady(t *testing.T) {
	e := newBundlesEnv(t)
	name := BundleFilename(testVersion, Platform{"linux", "amd64"})
	f, _, err := e.b.Open(name)
	require.Nil(t, f)
	ref := asRefusal(t, err, domain.ErrUnavailable, CodeBundlePreparing)
	assert.Equal(t, retryBundlePreparing, ref.RetryAfter)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, e.b.Wait(ctx))
	f, _, err = e.b.Open(name)
	require.NoError(t, err, "the request prepared the bundles")
	f.Close()
}

func asRefusal(t *testing.T, err error, class error, code string) *Refusal {
	t.Helper()
	var r *Refusal
	require.ErrorAs(t, err, &r)
	assert.ErrorIs(t, err, class)
	assert.Equal(t, code, r.Code)
	return r
}

// A restart finds the archives on the disk, hashes them again and downloads only the two small documents.
func TestBundlesReuseTheStoreOfAnEarlierRun(t *testing.T) {
	e := newBundlesEnv(t)
	e.prepare()
	require.Equal(t, 3, e.src.archiveHits())

	again, err := NewBundles(BundleDeps{Logger: quiet(), Version: testVersion, Dir: e.dir, BaseURL: e.src.srv.URL, Platforms: allPlatforms, Verifier: e.ver,
		HTTPClient: e.src.srv.Client(), now: e.clock.Now})
	require.NoError(t, err)
	again.Prepare()
	require.NoError(t, again.Wait(context.Background()))
	assert.Equal(t, 3, e.src.archiveHits(), "no archive was downloaded again")
	assert.Equal(t, 2, e.src.count("/v0.2.0/checksums.txt"), "checksums.txt was fetched and verified again")
	assert.Equal(t, 2, e.src.count("/v0.2.0/checksums.txt.sigstore.json"), "and so was its bundle")
	assert.Equal(t, 2, e.ver.count())
	for _, it := range again.State().Items {
		assert.Equal(t, BundleReady, it.Status)
	}

	// an archive on the disk that is not what its line says is not trusted: it is downloaded again
	name := BundleFilename(testVersion, Platform{"linux", "amd64"})
	path := filepath.Join(e.dir, artifact.InstallDir, "sha256", sumHex(e.rel.archives[name]))
	require.NoError(t, os.WriteFile(path, []byte("damaged"), 0o640))
	third, err := NewBundles(BundleDeps{Logger: quiet(), Version: testVersion, Dir: e.dir, BaseURL: e.src.srv.URL, Platforms: allPlatforms, Verifier: e.ver,
		HTTPClient: e.src.srv.Client(), now: e.clock.Now})
	require.NoError(t, err)
	third.Prepare()
	require.NoError(t, third.Wait(context.Background()))
	assert.Equal(t, 4, e.src.archiveHits())
	assert.Equal(t, e.rel.archives[name], readFile(t, path))
}

// ------------------------------------------------------------------ failures ----

// A signature that does not prove the release workflow of this project leaves every bundle failed, and no archive is
// even requested.
func TestBundlesFailWhenTheSignatureIsRefused(t *testing.T) {
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) {
		e.ver.err = errors.New("sigstore verification failed: no matching CertificateIdentity found")
	})
	e.prepare()
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleFailed, it.Status)
		assert.Contains(t, it.Error, "not signed by the release workflow of this project")
		assert.Contains(t, it.Error, "no matching CertificateIdentity")
		assert.Empty(t, it.SHA256)
	}
	assert.Zero(t, e.src.archiveHits(), "no archive is downloaded for a checksums.txt that nobody vouches for")
	assert.Empty(t, e.storeFiles())
	assert.Empty(t, e.tmpFiles())

	name := BundleFilename(testVersion, Platform{"linux", "amd64"})
	f, _, err := e.b.Open(name)
	require.Nil(t, f)
	ref := asRefusal(t, err, domain.ErrUnavailable, CodeBundleFailed)
	assert.Equal(t, retryBundleFailed, ref.RetryAfter)
	assert.Contains(t, ref.Detail, "linux/amd64")
}

func TestBundlesRetryAFailureAfterAMinute(t *testing.T) {
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) { e.ver.err = errors.New("no good") })
	e.prepare()
	require.Equal(t, 1, e.ver.count())

	// asking again within a minute does not go to the source again
	for i := 0; i < 5; i++ {
		e.clock.Advance(10 * time.Second)
		e.prepare()
	}
	assert.Equal(t, 1, e.ver.count())
	assert.Equal(t, 2, e.src.total())

	// after the minute it does; when the source is well again the bundles become ready
	e.clock.Advance(20 * time.Second)
	e.ver.mu.Lock()
	e.ver.err = nil
	e.ver.mu.Unlock()
	e.prepare()
	assert.Equal(t, 2, e.ver.count())
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleReady, it.Status, it.Error)
	}
}

func TestBundlesFailWhenTheSourceIsNotAvailable(t *testing.T) {
	cases := map[string]func(e *bundlesEnv){
		"no checksums.txt":     func(e *bundlesEnv) { e.src.status["/v0.2.0/checksums.txt"] = http.StatusNotFound },
		"no bundle":            func(e *bundlesEnv) { e.src.status["/v0.2.0/checksums.txt.sigstore.json"] = http.StatusNotFound },
		"server error":         func(e *bundlesEnv) { e.src.status["/v0.2.0/checksums.txt"] = http.StatusBadGateway },
		"checksums too large":  func(e *bundlesEnv) { e.b.maxSums = 100 },
		"bundle too large":     func(e *bundlesEnv) { e.b.maxSig = 5 },
		"source of the others": func(e *bundlesEnv) { delete(e.src.files, "/v0.2.0/checksums.txt") },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := newBundlesEnv(t)
			mut(e)
			e.prepare()
			for _, it := range e.b.State().Items {
				assert.Equal(t, BundleFailed, it.Status)
				assert.NotEmpty(t, it.Error)
			}
			assert.Zero(t, e.ver.count(), "nothing was verified")
			assert.Zero(t, e.src.archiveHits())
		})
	}
}

func TestBundlesFailOneArchiveButNotTheOthers(t *testing.T) {
	e := newBundlesEnv(t)
	bad := BundleFilename(testVersion, Platform{"linux", "arm64"})
	e.src.status["/v0.2.0/"+bad] = http.StatusNotFound
	e.prepare()
	for _, it := range e.b.State().Items {
		if it.Filename == bad {
			assert.Equal(t, BundleFailed, it.Status)
			assert.Contains(t, it.Error, "HTTP status 404")
			continue
		}
		assert.Equal(t, BundleReady, it.Status, it.Filename)
	}
	assert.Len(t, e.storeFiles(), 2)
}

func TestBundlesFailWhenTheArchiveIsNotTheOneInTheLine(t *testing.T) {
	e := newBundlesEnv(t)
	name := BundleFilename(testVersion, Platform{"windows", "amd64"})
	e.src.put("/v0.2.0/"+name, []byte("not the archive that was signed"))
	e.prepare()
	b := e.bundle("windows", "amd64")
	assert.Equal(t, BundleFailed, b.Status)
	assert.Contains(t, b.Error, "its line in the signed checksums.txt says "+sumHex(e.rel.archives[name]))
	assert.Contains(t, b.Error, sumHex([]byte("not the archive that was signed")))
	assert.Len(t, e.storeFiles(), 2, "the file was not stored")
	assert.Empty(t, e.tmpFiles())
	assert.Equal(t, BundleReady, e.bundle("linux", "amd64").Status)
	f, _, err := e.b.Open(name)
	require.Nil(t, f)
	asRefusal(t, err, domain.ErrUnavailable, CodeBundleFailed)
}

func TestBundlesFailWhenAnArchiveIsTooLarge(t *testing.T) {
	e := newBundlesEnv(t)
	e.b.maxArchive = 100
	e.prepare()
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleFailed, it.Status)
		assert.Contains(t, it.Error, "larger than 100 bytes")
	}
	assert.Empty(t, e.storeFiles())
	assert.Empty(t, e.tmpFiles())
}

// A line must be there exactly once: a missing line and a repeated line (also with the same hash) fail the bundle.
func TestBundlesNeedExactlyOneLineForTheArchive(t *testing.T) {
	name := BundleFilename(testVersion, Platform{"linux", "amd64"})
	line := func(e *bundlesEnv) string { return sumHex(e.rel.archives[name]) + "  " + name }
	cases := map[string]struct {
		edit func(e *bundlesEnv, sums string) string
		want string
	}{
		"missing": {func(e *bundlesEnv, sums string) string { return strings.Replace(sums, line(e)+"\n", "", 1) }, "has no line for " + name},
		"twice":   {func(e *bundlesEnv, sums string) string { return sums + line(e) + "\n" }, "has 2 lines for " + name},
		"twice with another hash": {func(e *bundlesEnv, sums string) string {
			return sums + sumHex([]byte("other")) + "  " + name + "\n"
		}, "has 2 lines for " + name},
		"short hash": {func(e *bundlesEnv, sums string) string {
			return strings.Replace(sums, line(e), sumHex(e.rel.archives[name])[:60]+"  "+name, 1)
		}, "has no line for " + name},
		"one space": {func(e *bundlesEnv, sums string) string {
			return strings.Replace(sums, line(e), sumHex(e.rel.archives[name])+" x"+name, 1)
		}, "has no line for " + name},
		"only the name of another asset": {func(e *bundlesEnv, sums string) string {
			return strings.Replace(sums, line(e), sumHex(e.rel.archives[name])+"  "+name+".sbom.json", 1)
		}, "has no line for " + name},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			e := newBundlesEnv(t)
			e.src.put("/v0.2.0/checksums.txt", []byte(c.edit(e, string(e.rel.sums))))
			e.prepare()
			b := e.bundle("linux", "amd64")
			assert.Equal(t, BundleFailed, b.Status)
			assert.Contains(t, b.Error, c.want)
			// the other platforms are not affected, and the archive was not even requested
			assert.Equal(t, BundleReady, e.bundle("windows", "amd64").Status)
			assert.Zero(t, e.src.count("/v0.2.0/"+name))
		})
	}
}

func TestBundlesUseTheLineOfTheArchiveAndNotOfALookAlike(t *testing.T) {
	// the line of the bare binary and the SBOM carry other hashes; the archive is checked against its own line
	e := newBundlesEnv(t)
	e.prepare()
	b := e.bundle("linux", "amd64")
	assert.Equal(t, BundleReady, b.Status)
	assert.Equal(t, sumHex(e.rel.archives[b.Filename]), b.SHA256)
	// an upper case hash in the line is the same hash
	name := BundleFilename(testVersion, Platform{"linux", "arm64"})
	upper := strings.ToUpper(sumHex(e.rel.archives[name]))
	e2 := newBundlesEnv(t)
	e2.src.put("/v0.2.0/checksums.txt", []byte(strings.Replace(string(e2.rel.sums), sumHex(e2.rel.archives[name]), upper, 1)))
	e2.prepare()
	assert.Equal(t, BundleReady, e2.bundle("linux", "arm64").Status)
}

// With the real signature of v0.1.0 (the verifier is the real one, offline) the bundles are taken only for the release
// that the signature belongs to, and only archives that match their lines.
func TestBundlesWithTheRealSignature(t *testing.T) {
	sums, bundle := loadRelease(t)
	lowered := func(version string) func(e *bundlesEnv, d *BundleDeps) {
		return func(e *bundlesEnv, d *BundleDeps) {
			d.Version, d.minVersion, d.Verifier = version, "0.1.0-0", realVerifier(t)
			e.src.put("/v"+version+"/checksums.txt", sums)
			e.src.put("/v"+version+"/checksums.txt.sigstore.json", bundle)
			for _, p := range allPlatforms {
				e.src.put("/v"+version+"/"+BundleFilename(version, p), []byte("an archive that is not the one of the release"))
			}
		}
	}

	t.Run("the signature of the release is accepted and the archives are checked against the lines", func(t *testing.T) {
		e := newBundlesEnv(t, lowered("0.1.0"))
		e.prepare()
		for _, it := range e.b.State().Items {
			assert.Equal(t, BundleFailed, it.Status)
			assert.NotContains(t, it.Error, "not signed", "the signature was fine: "+it.Error)
			assert.Contains(t, it.Error, "its line in the signed checksums.txt says", it.Filename)
		}
		// the real lines: the Linux archive of v0.1.0 is 99dfcd50...
		assert.Contains(t, e.bundle("linux", "amd64").Error, "99dfcd5094e4114ab1d1f478af7d4100019c8bd896f62db16c471858be914ebf")
		assert.Contains(t, e.bundle("windows", "amd64").Error, "cc8f8b579c4648a1f287fb9546cadc79783c174a464c05aa405a306a4b4db8d6")
		assert.Empty(t, e.storeFiles())
	})
	t.Run("the signature of another version is refused", func(t *testing.T) {
		e := newBundlesEnv(t, lowered("0.1.1"))
		e.prepare()
		for _, it := range e.b.State().Items {
			assert.Equal(t, BundleFailed, it.Status)
			assert.Contains(t, it.Error, "not signed by the release workflow of this project")
			assert.Contains(t, it.Error, "sigstore verification failed")
		}
		assert.Zero(t, e.src.archiveHits())
		assert.Empty(t, e.storeFiles())
	})
	t.Run("a release candidate cannot use the signature of the release", func(t *testing.T) {
		e := newBundlesEnv(t, lowered("0.1.0-rc.3"))
		e.prepare()
		for _, it := range e.b.State().Items {
			assert.Equal(t, BundleFailed, it.Status)
		}
		assert.Zero(t, e.src.archiveHits())
	})
	t.Run("a changed checksums.txt is refused", func(t *testing.T) {
		e := newBundlesEnv(t, lowered("0.1.0"))
		// the line of the Linux archive points to the bytes of an archive of the attacker
		evil := strings.Replace(string(sums), "99dfcd5094e4114ab1d1f478af7d4100019c8bd896f62db16c471858be914ebf", sumHex([]byte("evil")), 1)
		require.NotEqual(t, string(sums), evil)
		e.src.put("/v0.1.0/checksums.txt", []byte(evil))
		e.src.put("/v0.1.0/"+BundleFilename("0.1.0", Platform{"linux", "amd64"}), []byte("evil"))
		e.prepare()
		b := e.bundle("linux", "amd64")
		assert.Equal(t, BundleFailed, b.Status)
		assert.Contains(t, b.Error, "not signed by the release workflow of this project")
		assert.Zero(t, e.src.archiveHits())
		assert.Empty(t, e.storeFiles())
	})
	t.Run("a bundle of another workflow is refused", func(t *testing.T) {
		e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) {
			d.Version, d.minVersion, d.Verifier = "0.162.0", "0.100.0-0", realVerifier(t)
			// the upstream workflow signed this small file; it is served as if it was checksums.txt of this project
			e.src.put("/v0.162.0/checksums.txt", readFile(t, upstreamBlob))
			e.src.put("/v0.162.0/checksums.txt.sigstore.json", readFile(t, upstreamBundle))
		})
		e.prepare()
		b := e.bundle("linux", "amd64")
		assert.Equal(t, BundleFailed, b.Status)
		assert.Contains(t, b.Error, "not signed by the release workflow of this project")
		assert.Contains(t, b.Error, "sigstore verification failed")
		assert.Zero(t, e.src.archiveHits())
	})
	t.Run("garbage instead of a bundle is refused", func(t *testing.T) {
		e := newBundlesEnv(t, lowered("0.1.0"))
		e.src.put("/v0.1.0/checksums.txt.sigstore.json", []byte("{not json"))
		e.prepare()
		b := e.bundle("linux", "amd64")
		assert.Equal(t, BundleFailed, b.Status)
		assert.Contains(t, b.Error, "invalid sigstore bundle")
	})
}

// -------------------------------------------------------------------- delivery ----

// An archive that was changed on the disk is not delivered: it is discarded and fetched again.
func TestBundlesHashTheArchiveAgainBeforeEveryDelivery(t *testing.T) {
	name := BundleFilename(testVersion, Platform{"linux", "amd64"})
	damage := map[string]func(t *testing.T, path string){
		"changed in place": func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = f.WriteAt([]byte("X"), 3)
			require.NoError(t, err)
			require.NoError(t, f.Close())
		},
		"replaced by another file of the same size": func(t *testing.T, path string) {
			b := readFile(t, path)
			b[0] ^= 0xff
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, b, 0o640))
		},
		"truncated": func(t *testing.T, path string) { require.NoError(t, os.Truncate(path, 10)) },
		"emptied":   func(t *testing.T, path string) { require.NoError(t, os.Truncate(path, 0)) },
		"removed":   func(t *testing.T, path string) { require.NoError(t, os.Remove(path)) },
		"replaced by a directory": func(t *testing.T, path string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o750))
		},
	}
	if runtime.GOOS != "windows" {
		damage["replaced by a link"] = func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "..", "target")
			require.NoError(t, os.WriteFile(target, bytes.Repeat([]byte(name+"|"), 40), 0o640)) // the same bytes: only the link is wrong
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Symlink(target, path))
		}
	}
	for label, do := range damage {
		t.Run(label, func(t *testing.T) {
			e := newBundlesEnv(t)
			e.prepare()
			b := e.bundle("linux", "amd64")
			path := filepath.Join(e.dir, artifact.InstallDir, "sha256", b.SHA256)
			do(t, path)

			f, _, err := e.b.Open(name)
			require.Nil(t, f, "damaged bytes are never handed out")
			asRefusal(t, err, domain.ErrUnavailable, CodeBundlePreparing)

			// the damaged bundle is fetched and checked again
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			require.NoError(t, e.b.Wait(ctx))
			f, got, err := e.b.Open(name)
			require.NoError(t, err)
			defer f.Close()
			assert.Equal(t, e.rel.archives[name], readAll(t, f))
			assert.Equal(t, BundleReady, got.Status)
			assert.Equal(t, 4, e.src.archiveHits(), "one more download of this archive")
		})
	}
}

func TestBundleOpenedForDeliveryCannotBeSwappedUnderTheReader(t *testing.T) {
	// the file that is returned is the one that was hashed: a replacement of the path afterwards does not change it
	e := newBundlesEnv(t)
	e.prepare()
	name := BundleFilename(testVersion, Platform{"linux", "amd64"})
	f, b, err := e.b.Open(name)
	require.NoError(t, err)
	defer f.Close()
	path := filepath.Join(e.dir, artifact.InstallDir, "sha256", b.SHA256)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("replacement"), 0o640))
	assert.Equal(t, e.rel.archives[name], readAll(t, f))
}

func TestBundlesLocalBuildsGetNone(t *testing.T) {
	for _, v := range []string{"0.0.0-dev", "v0.2.0-3-g1234567-dirty", "0.2.0-3-g1234567", "0.1.0", "0.2.0+dirty", "", "dev"} {
		t.Run(v, func(t *testing.T) {
			src := newReleaseSource(t)
			dir := t.TempDir()
			b, err := NewBundles(BundleDeps{Logger: quiet(), Version: v, Dir: dir, BaseURL: src.srv.URL, Platforms: allPlatforms, Verifier: &fakeVerifier{}})
			require.NoError(t, err)
			st := b.State()
			assert.Equal(t, BundleSourceNone, st.Source)
			assert.Equal(t, ReasonLocalBuild, st.Reason)
			assert.Empty(t, st.Version)
			assert.NotNil(t, st.Items, "an empty list, not null")
			assert.Empty(t, st.Items)
			b.Prepare()
			require.NoError(t, b.Wait(context.Background()))
			assert.Zero(t, src.total(), "a local build does not go to the release source")
			_, err = os.Stat(filepath.Join(dir, artifact.InstallDir))
			assert.True(t, os.IsNotExist(err), "and creates no directory")
			_, _, err = b.Open(BundleFilename(v, Platform{"linux", "amd64"}))
			assert.ErrorIs(t, err, ErrBundleNotFound)
			_, ok := b.Find(Platform{"linux", "amd64"})
			assert.False(t, ok)
			js, err := json.Marshal(st)
			require.NoError(t, err)
			assert.JSONEq(t, `{"source":"none","reason":"local_build","items":[]}`, string(js))
		})
	}
}

func TestBundlesOfAReleaseCandidate(t *testing.T) {
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) {
		d.Version = "0.2.0-rc.1"
		e.rel = buildRelease("0.2.0-rc.1", allPlatforms)
		e.src.publish(e.rel)
	})
	assert.Equal(t, BundleSourceRelease, e.b.State().Source)
	e.prepare()
	require.Equal(t, 1, e.ver.count())
	assert.Equal(t, "v0.2.0-rc.1", e.ver.calls[0].id.Tag)
	assert.Equal(t, `^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.2\.0-rc\.1$`, e.ver.calls[0].id.SANRegexp)
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleReady, it.Status, it.Error)
		assert.Contains(t, it.Filename, "0.2.0-rc.1")
	}
}

func TestBundlesOnlyForTheConfiguredPlatforms(t *testing.T) {
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) { d.Platforms = []Platform{{"linux", "arm64"}} })
	e.prepare()
	st := e.b.State()
	require.Len(t, st.Items, 1)
	assert.Equal(t, "arm64", st.Items[0].Arch)
	assert.Equal(t, BundleReady, st.Items[0].Status)
	assert.Equal(t, 3, e.src.total())
	_, ok := e.b.Find(Platform{"windows", "amd64"})
	assert.False(t, ok)
	_, _, err := e.b.Open(BundleFilename(testVersion, Platform{"windows", "amd64"}))
	assert.ErrorIs(t, err, ErrBundleNotFound)
}

// -------------------------------------------------------------------- the source ----

func TestBundlesFollowTheRedirectRules(t *testing.T) {
	// a source that redirects to an http target of another origin: refused (https only)
	other := newReleaseSource(t)
	e := newBundlesEnv(t)
	e.src.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+r.URL.Path, http.StatusFound)
	})
	e.prepare()
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleFailed, it.Status)
		assert.Contains(t, it.Error, "only https targets are followed")
	}
	assert.Zero(t, other.total(), "the redirect was not followed")
}

func TestBundlesSourceMayRedirectWithinItself(t *testing.T) {
	// a mirror that sends the clients to another path on its own origin (lab setups over plain http)
	e := newBundlesEnv(t)
	inner := e.src.srv.Config.Handler
	e.src.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v0.2.0/") {
			http.Redirect(w, r, "/mirror"+strings.TrimPrefix(r.URL.Path, "/v0.2.0"), http.StatusFound)
			return
		}
		r.URL.Path = "/v0.2.0" + strings.TrimPrefix(r.URL.Path, "/mirror")
		inner.ServeHTTP(w, r)
	})
	e.prepare()
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleReady, it.Status, it.Error)
	}
}

func TestBundlesNeverShowAURLWithCredentials(t *testing.T) {
	src := newReleaseSource(t)
	src.srv.Close() // the connection fails
	base := strings.Replace(src.srv.URL, "http://", "http://", 1)
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) { d.BaseURL = base + "/path?token=SECRET" })
	e.prepare()
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleFailed, it.Status)
		assert.NotContains(t, it.Error, "SECRET")
		assert.NotContains(t, it.Error, "token=")
	}
}

func TestNewBundlesChecksItsSettings(t *testing.T) {
	good := BundleDeps{Logger: quiet(), Version: testVersion, Dir: t.TempDir(), BaseURL: "https://github.com/myriarch/myriarch/releases/download", Platforms: allPlatforms, Verifier: &fakeVerifier{}}
	_, err := NewBundles(good)
	require.NoError(t, err)

	for name, mut := range map[string]func(d *BundleDeps){
		"no directory":               func(d *BundleDeps) { d.Dir = "" },
		"no verifier":                func(d *BundleDeps) { d.Verifier = nil },
		"relative source":            func(d *BundleDeps) { d.BaseURL = "/releases" },
		"source with credentials":    func(d *BundleDeps) { d.BaseURL = "https://user:pw@example.com/releases" },
		"source of another scheme":   func(d *BundleDeps) { d.BaseURL = "ftp://example.com/releases" },
		"source without a host":      func(d *BundleDeps) { d.BaseURL = "https:///releases" },
		"a directory without dev":    func(d *BundleDeps) { d.Directory = t.TempDir() },
		"an invalid proxy":           func(d *BundleDeps) { d.ProxyURL = "::" },
		"a proxy without a host":     func(d *BundleDeps) { d.ProxyURL = "proxy" },
		"a directory without dev 2":  func(d *BundleDeps) { d.Directory, d.DevMode = "/tmp/x", false },
		"a directory in the install": func(d *BundleDeps) { d.Directory = filepath.Join(d.Dir, "x") },
	} {
		t.Run(name, func(t *testing.T) {
			d := good
			d.Dir = t.TempDir()
			mut(&d)
			_, err := NewBundles(d)
			require.Error(t, err)
		})
	}
	// the refusal of the directory names the reason
	d := good
	d.Directory = t.TempDir()
	_, err = NewBundles(d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only in development mode")
}

// ------------------------------------------------------------------ the directory ----

func writeArchive(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0o644))
}

func newDirectoryBundles(t *testing.T, dir string, mut ...func(d *BundleDeps)) (*Bundles, string) {
	t.Helper()
	cache := t.TempDir()
	d := BundleDeps{Logger: quiet(), Version: "0.0.0-dev", Dir: cache, Platforms: allPlatforms, Directory: dir, DevMode: true}
	for _, m := range mut {
		m(&d)
	}
	b, err := NewBundles(d)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Wait(context.Background()) })
	return b, cache
}

func TestBundlesFromADirectory(t *testing.T) {
	dir := t.TempDir()
	want := map[string][]byte{}
	for _, p := range allPlatforms {
		name := BundleFilename("0.3.0-dev", p)
		want[name] = []byte("archive " + name)
		writeArchive(t, dir, name, want[name])
	}
	// files that are not bundles of the platforms are left alone
	writeArchive(t, dir, "checksums.txt", []byte("x"))
	writeArchive(t, dir, "myriarch-updater_0.3.0-dev_darwin_arm64.tar.gz", []byte("x"))
	writeArchive(t, dir, "myriarch-updater_0.2.0_linux_amd64.tar.gz", []byte("an older version"))
	writeArchive(t, dir, "myriarch-updater_not-a-version_linux_amd64.tar.gz", []byte("x"))

	b, cache := newDirectoryBundles(t, dir)
	st := b.State()
	assert.Equal(t, BundleSourceDirectory, st.Source)
	assert.Equal(t, "0.3.0-dev", st.Version, "the highest version in the directory")
	require.Len(t, st.Items, 3)
	for _, it := range st.Items {
		assert.Equal(t, BundleReady, it.Status, it.Error)
		assert.Equal(t, sumHex(want[it.Filename]), it.SHA256)
		f, got, err := b.Open(it.Filename)
		require.NoError(t, err)
		assert.Equal(t, want[it.Filename], readAll(t, f))
		assert.Equal(t, it.SHA256, got.SHA256)
		f.Close()
		// the copy in the store is private to the server: it has the mode of the others
		info, err := os.Stat(filepath.Join(cache, artifact.InstallDir, "sha256", it.SHA256))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	}
	_, _, err := b.Open("myriarch-updater_0.2.0_linux_amd64.tar.gz")
	assert.ErrorIs(t, err, ErrBundleNotFound, "only the bundles of the version in use")
}

func TestBundlesFromADirectoryFollowChanges(t *testing.T) {
	dir := t.TempDir()
	name := BundleFilename("0.3.0-dev", Platform{"linux", "amd64"})
	writeArchive(t, dir, name, []byte("first build"))
	b, cache := newDirectoryBundles(t, dir, func(d *BundleDeps) { d.Platforms = []Platform{{"linux", "amd64"}} })
	first := b.State().Items[0]
	assert.Equal(t, sumHex([]byte("first build")), first.SHA256)

	// a new build in the directory is taken at the next call, and the old copy stays until it is tidied
	writeArchive(t, dir, name, []byte("second build"))
	b.Prepare()
	second := b.State().Items[0]
	assert.Equal(t, sumHex([]byte("second build")), second.SHA256)
	f, _, err := b.Open(name)
	require.NoError(t, err)
	assert.Equal(t, []byte("second build"), readAll(t, f))
	f.Close()
	_, err = os.Stat(filepath.Join(cache, artifact.InstallDir, "sha256", first.SHA256))
	assert.NoError(t, err)

	// a newer version appears
	newer := BundleFilename("0.3.1-dev", Platform{"linux", "amd64"})
	writeArchive(t, dir, newer, []byte("newer"))
	b.Prepare()
	assert.Equal(t, "0.3.1-dev", b.State().Version)
	assert.Equal(t, newer, b.State().Items[0].Filename)
}

func TestBundlesFromADirectoryReportWhatIsWrong(t *testing.T) {
	t.Run("a missing platform fails that bundle only", func(t *testing.T) {
		dir := t.TempDir()
		writeArchive(t, dir, BundleFilename("0.3.0", Platform{"linux", "amd64"}), []byte("a"))
		b, _ := newDirectoryBundles(t, dir)
		byOS := map[string]Bundle{}
		for _, it := range b.State().Items {
			byOS[it.OS+"/"+it.Arch] = it
		}
		assert.Equal(t, BundleReady, byOS["linux/amd64"].Status)
		assert.Equal(t, BundleFailed, byOS["linux/arm64"].Status)
		assert.Contains(t, byOS["linux/arm64"].Error, "has no file myriarch-updater_0.3.0_linux_arm64.tar.gz")
	})
	t.Run("an empty directory", func(t *testing.T) {
		b, _ := newDirectoryBundles(t, t.TempDir())
		for _, it := range b.State().Items {
			assert.Equal(t, BundleFailed, it.Status)
			assert.Contains(t, it.Error, "holds no archive")
		}
		assert.Empty(t, b.State().Version)
	})
	t.Run("a directory that is not there", func(t *testing.T) {
		b, _ := newDirectoryBundles(t, filepath.Join(t.TempDir(), "missing"))
		for _, it := range b.State().Items {
			assert.Equal(t, BundleFailed, it.Status)
			assert.Contains(t, it.Error, "cannot read the bundle directory")
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("a link is not followed", func(t *testing.T) {
			dir := t.TempDir()
			real := filepath.Join(t.TempDir(), "real.tar.gz")
			require.NoError(t, os.WriteFile(real, []byte("elsewhere"), 0o644))
			name := BundleFilename("0.3.0", Platform{"linux", "amd64"})
			require.NoError(t, os.Symlink(real, filepath.Join(dir, name)))
			b, _ := newDirectoryBundles(t, dir, func(d *BundleDeps) { d.Platforms = []Platform{{"linux", "amd64"}} })
			it := b.State().Items[0]
			assert.Equal(t, BundleFailed, it.Status)
			assert.Contains(t, it.Error, "not a regular file")
		})
	}
	t.Run("a directory instead of a file", func(t *testing.T) {
		dir := t.TempDir()
		name := BundleFilename("0.3.0", Platform{"linux", "amd64"})
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o755))
		b, _ := newDirectoryBundles(t, dir, func(d *BundleDeps) { d.Platforms = []Platform{{"linux", "amd64"}} })
		assert.Equal(t, BundleFailed, b.State().Items[0].Status)
	})
}

// A directory is a development tool: it is hashed again before the delivery like every other bundle.
func TestBundlesFromADirectoryAreHashedAgainBeforeDelivery(t *testing.T) {
	dir := t.TempDir()
	name := BundleFilename("0.3.0", Platform{"linux", "amd64"})
	writeArchive(t, dir, name, []byte("good"))
	b, cache := newDirectoryBundles(t, dir, func(d *BundleDeps) { d.Platforms = []Platform{{"linux", "amd64"}} })
	it := b.State().Items[0]
	require.NoError(t, os.WriteFile(filepath.Join(cache, artifact.InstallDir, "sha256", it.SHA256), []byte("evil"), 0o640))
	f, _, err := b.Open(name)
	require.Nil(t, f)
	require.Error(t, err)
	// the directory is read again and the store repaired
	f, _, err = b.Open(name)
	require.NoError(t, err)
	assert.Equal(t, []byte("good"), readAll(t, f))
	f.Close()
}

// ----------------------------------------------------------------------- tidying ----

func TestBundlesRemoveTheArchivesOfAnEarlierVersionAfterADay(t *testing.T) {
	e := newBundlesEnv(t)
	store := filepath.Join(e.dir, artifact.InstallDir, "sha256")
	orphan := sumHex([]byte("old release"))
	young := sumHex([]byte("a release that another replica serves"))
	for _, n := range []string{orphan, young} {
		require.NoError(t, os.WriteFile(filepath.Join(store, n), []byte("x"), 0o640))
	}
	require.NoError(t, os.WriteFile(filepath.Join(store, "not-a-digest"), []byte("x"), 0o640))
	old := e.clock.Now().Add(-25 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(store, orphan), old, old))
	require.NoError(t, os.Chtimes(filepath.Join(store, "not-a-digest"), old, old))
	// the clock of the test is the clock of the server: the young file is a few minutes old
	recent := e.clock.Now().Add(-5 * time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(store, young), recent, recent))

	e.prepare()
	_, err := os.Stat(filepath.Join(store, orphan))
	assert.True(t, os.IsNotExist(err), "the archive of an earlier version is removed")
	assert.FileExists(t, filepath.Join(store, young))
	assert.FileExists(t, filepath.Join(store, "not-a-digest"), "only files that are named like a digest are the business of the server")
	for _, it := range e.b.State().Items {
		assert.FileExists(t, filepath.Join(store, it.SHA256), "a bundle in use is never removed")
	}
}

// ------------------------------------------------------------------- concurrency ----

func TestBundlesPrepareOnceForManyCallers(t *testing.T) {
	e := newBundlesEnv(t)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.b.Prepare()
			_, _, _ = e.b.Open(BundleFilename(testVersion, Platform{"linux", "amd64"}))
			_ = e.b.State()
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, e.b.Wait(ctx))
	assert.Equal(t, 1, e.src.count("/v0.2.0/checksums.txt"), "one preparation, however many callers")
	assert.Equal(t, 3, e.src.archiveHits())
	assert.Equal(t, 1, e.ver.count())
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleReady, it.Status, it.Error)
	}
}

func TestBundlesRunPreparesAfterTheStartAndStopsWithTheServer(t *testing.T) {
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) { d.startDelay = 20 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.b.Run(ctx) }()
	require.Eventually(t, func() bool {
		for _, it := range e.b.State().Items {
			if it.Status != BundleReady {
				return false
			}
		}
		return true
	}, 20*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end with the context")
	}

	// a server without bundles has nothing to do and ends with the context as well
	none, err := NewBundles(BundleDeps{Logger: quiet(), Version: "0.0.0-dev", Dir: t.TempDir()})
	require.NoError(t, err)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- none.Run(ctx2) }()
	cancel2()
	require.NoError(t, <-done2)

	// and a context that ends before the pause is over prepares nothing
	e2 := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) { d.startDelay = time.Hour })
	ctx3, cancel3 := context.WithCancel(context.Background())
	cancel3()
	require.NoError(t, e2.b.Run(ctx3))
	assert.Zero(t, e2.src.total())
}

func TestBundleStateIsJSON(t *testing.T) {
	e := newBundlesEnv(t)
	e.prepare()
	js, err := json.Marshal(e.b.State())
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(js, &got))
	assert.Equal(t, "release", got["source"])
	assert.Equal(t, testVersion, got["version"])
	assert.NotContains(t, got, "reason")
	items := got["items"].([]any)
	require.Len(t, items, 3)
	first := items[0].(map[string]any)
	assert.Equal(t, "ready", first["status"])
	assert.Equal(t, fmt.Sprint(len(e.rel.archives[first["filename"].(string)])), fmt.Sprint(first["size"]))
	assert.NotContains(t, first, "error")
}

// The end of the server stops a preparation that is under way and waits for it: it writes into the cache directory, which
// must not change under a process that is gone.
func TestBundlesRunStopsAPreparationWithTheServer(t *testing.T) {
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) { d.startDelay = 10 * time.Millisecond })
	started := make(chan struct{}, 8)
	inner := e.src.srv.Config.Handler
	e.src.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, brandUpdater) {
			started <- struct{}{}
			<-r.Context().Done() // the archive never arrives; the client gives up with the server
			return
		}
		inner.ServeHTTP(w, r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.b.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("the preparation did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not wait for the preparation, or the preparation did not stop")
	}
	for _, it := range e.b.State().Items {
		assert.NotEqual(t, BundlePreparing, it.Status, "nothing is left under way")
		assert.NotEqual(t, BundleReady, it.Status)
	}
	assert.Empty(t, e.tmpFiles())
}

// The source is a connection of the server like every other: the destination policy checks the address, so a source on
// a network that the policy denies (here the loopback address of the test server) is not contacted at all, and the
// operator opens it with outbound.allowedCidrs.
func TestBundlesGoThroughTheOutboundPolicy(t *testing.T) {
	policy, err := netguard.New(nil)
	require.NoError(t, err)
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) {
		d.HTTPClient, d.Policy = policy.Client(time.Minute), policy
	})
	e.prepare()
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleFailed, it.Status)
		assert.Contains(t, it.Error, "not allowed by the outbound policy", "the policy names the reason: "+it.Error)
	}
	assert.Zero(t, e.src.total(), "the connection was refused before a byte was sent")
	assert.Zero(t, e.ver.count())

	opened, err := netguard.New([]string{"127.0.0.0/8"})
	require.NoError(t, err)
	e2 := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) {
		d.HTTPClient, d.Policy = opened.Client(time.Minute), opened
	})
	e2.prepare()
	for _, it := range e2.b.State().Items {
		assert.Equal(t, BundleReady, it.Status, it.Error)
	}
}

// A proxy of artifacts.proxyUrl carries the requests of the source, and a setting that is no proxy is refused at the
// start.
func TestBundlesUseTheProxyOfTheArtifacts(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		http.NotFound(w, r)
	}))
	t.Cleanup(proxy.Close)
	e := newBundlesEnv(t, func(e *bundlesEnv, d *BundleDeps) {
		d.HTTPClient = nil
		d.ProxyURL = proxy.URL
		d.BaseURL = "http://source.example.test/releases"
	})
	e.prepare()
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen, "the request went to the proxy")
	assert.Equal(t, "http://source.example.test/releases/v0.2.0/checksums.txt", seen[0])
	assert.Zero(t, e.src.total())
	for _, it := range e.b.State().Items {
		assert.Equal(t, BundleFailed, it.Status)
	}
}
