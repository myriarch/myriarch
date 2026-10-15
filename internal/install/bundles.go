// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/netguard"
)

// The installer bundles (ADR-0046, docs/design/opamp-server.md, "Installation from the server"): the updater archive
// of the release that the server itself runs (`myriarch-updater_<version>_<os>_<arch>.tar.gz` or `.zip`: the installer
// scripts, the templates and the myriarch-updater binary), served to the hosts that are installed from the server.
//
// A bundle is the bytes of the release and nothing else: the server does not template, repack or re-encode them (the
// hash rules of App Control match byte-identical files only, and Authenticode signatures stay valid). It gets them from
// install.bundleSourceBaseUrl (layout <base>/v<version>/<asset>), but only after it verified checksums.txt of the
// release keyless against the identity of the release workflow of this project (derived from brand.Repository, the tag
// equals the version, the issuer is GitHub Actions) and found exactly one line for the archive that the archive
// matches. The archive is stored under its SHA-256 and hashed again before every delivery. A failure of any step
// leaves the bundle failed and delivers nothing.
//
// Only a release gets bundles: the version of the server is MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-rc.N, from 0.2.0 on.
// A local build gets none, except in development mode with an explicit directory (`myriarchd dev --install-bundle-dir`),
// whose archives are trusted because the developer put them there. The bundles are not rows of the artifacts table: their
// state is in memory and their bytes are files, one set per replica.

// Sources of the bundles (BundleList.Source).
const (
	// BundleSourceRelease: the release of this server, verified keyless.
	BundleSourceRelease = "release"
	// BundleSourceDirectory: a directory of a development server.
	BundleSourceDirectory = "directory"
	// BundleSourceNone: the server has no bundles (see BundleList.Reason).
	BundleSourceNone = "none"
)

// ReasonLocalBuild is the reason for BundleSourceNone: the server is not a release, so there is no release whose
// bundle it could serve.
const ReasonLocalBuild = "local_build"

// Statuses of a bundle.
const (
	// BundlePending: not fetched yet.
	BundlePending = "pending"
	// BundlePreparing: the server is fetching and checking it.
	BundlePreparing = "preparing"
	// BundleReady: stored, with a pinned SHA-256, and delivered.
	BundleReady = "ready"
	// BundleFailed: the last attempt failed (see Bundle.Error); nothing is delivered. The server tries again later.
	BundleFailed = "failed"
)

// Reasons of a refusal about the bundles (the code of the first issue of the problem document).
const (
	CodeLocalBuild      = "local_build"
	CodeBundlePreparing = "bundle_preparing"
	CodeBundleFailed    = "bundle_failed"
)

const (
	// bundleRetryAfter is how long a failed attempt blocks the next one, so that hosts that ask every few seconds
	// cannot make the server fetch from the release source in a loop.
	bundleRetryAfter = time.Minute
	// prepareTimeout bounds one preparation of all bundles.
	prepareTimeout = 20 * time.Minute
	// bundleStartDelay lets the server serve before it downloads (the same pause as the artifact prefetch).
	bundleStartDelay = 15 * time.Second
	// retryBundlePreparing and retryBundleFailed are the Retry-After values of the refusals.
	retryBundlePreparing = 5 * time.Second
	retryBundleFailed    = 30 * time.Second
	// orphanAge is how old a file of a former version has to be before the server removes it.
	orphanAge = 24 * time.Hour
	// minBundleVersion is the lowest version whose installers understand the plan of the server: 0.2.0, including
	// its release candidates (0.2.0-0 sorts before every prerelease of 0.2.0).
	minBundleVersion = "0.2.0-0"
	// maxErrorLen cuts the text of an error that is shown to the people who read the state.
	maxErrorLen = 500
)

// releaseVersionRE is the version of a release of this project that ships installer bundles: MAJOR.MINOR.PATCH, or
// with -rc.N. A build from a Git checkout (git describe) or with build metadata is not a release.
var releaseVersionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.(0|[1-9][0-9]*))?$`)

// IsReleaseVersion reports whether v is the version of a release that has installer bundles: a strict release version
// (MAJOR.MINOR.PATCH, or with -rc.N) from 0.2.0 on.
func IsReleaseVersion(v string) bool { return releaseVersionFrom(v, minBundleVersion) }

func releaseVersionFrom(v, floor string) bool {
	if !releaseVersionRE.MatchString(v) {
		return false
	}
	sv, err := semver.StrictNewVersion(v)
	if err != nil {
		return false
	}
	lowest, err := semver.StrictNewVersion(floor)
	return err == nil && sv.Compare(lowest) >= 0
}

// ReleaseIdentity is the identity that the signature of checksums.txt of the release has to prove: the release workflow
// of this project at the tag of the version, issued by GitHub Actions. It is derived from brand.Repository; neither a
// setting nor the source of the bundles can change it. The installers build the same identity for the same release.
func ReleaseIdentity(version string) artifact.Identity {
	return artifact.Identity{
		SANRegexp: "^" + regexp.QuoteMeta(brand.Repository) + `/\.github/workflows/release\.yml@refs/tags/v` + regexp.QuoteMeta(version) + "$",
		Issuer:    artifact.GitHubActionsIssuer,
		Tag:       "v" + version,
	}
}

// BundleFilename is the name of the archive for a platform in a release: tar.gz for Linux, zip for Windows
// (deploy/release/goreleaser.yaml, archive myriarch-updater).
func BundleFilename(version string, p Platform) string {
	ext := ".tar.gz"
	if p.OS == "windows" {
		ext = ".zip"
	}
	return brand.UpdaterBinary + "_" + version + "_" + p.OS + "_" + p.Arch + ext
}

// bundleFileRE matches the name of a bundle: the file name in the URL of a download and in a bundle directory.
var bundleFileRE = regexp.MustCompile(`^` + regexp.QuoteMeta(brand.UpdaterBinary) + `_([0-9A-Za-z.+-]+)_(linux|windows)_(amd64|arm64)\.(tar\.gz|zip)$`)

// ValidBundleFilename reports whether name is the name of an installer bundle, whichever version it has.
func ValidBundleFilename(name string) bool {
	m := bundleFileRE.FindStringSubmatch(name)
	if m == nil || len(name) > 128 {
		return false
	}
	if _, err := semver.StrictNewVersion(m[1]); err != nil {
		return false
	}
	return (m[2] == "windows") == (m[4] == "zip")
}

// Bundle is the installer bundle for one platform.
type Bundle struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Filename string `json:"filename"`
	// Status is BundlePending, BundlePreparing, BundleReady or BundleFailed.
	Status string `json:"status"`
	// SHA256 is the lower-case hex digest of the archive (ready only). It is the line of the archive in the signed
	// checksums.txt of the release, and the copied command pins it.
	SHA256 string `json:"sha256,omitempty"`
	// Size is the size of the archive in bytes (ready only).
	Size int64 `json:"size,omitempty"`
	// Error says why the last attempt failed (failed only).
	Error string `json:"error,omitempty"`
}

// BundleList is the state of the bundles of this server.
type BundleList struct {
	// Source is BundleSourceRelease, BundleSourceDirectory or BundleSourceNone.
	Source string `json:"source"`
	// Version is the version of the bundles: the version of this server (release), or of the archives in the directory.
	Version string `json:"version,omitempty"`
	// Reason says why there are none (BundleSourceNone only): ReasonLocalBuild.
	Reason string   `json:"reason,omitempty"`
	Items  []Bundle `json:"items"`
}

// BundleVerifier verifies a keyless Sigstore bundle against an identity that the caller names. artifact.Verifier is
// the implementation.
type BundleVerifier interface {
	Verify(bundleJSON, digest []byte, id artifact.Identity) (string, error)
}

// CacheVerifier returns the verifier of an artifact cache as a BundleVerifier. An unusable trusted root is reported by
// the first verification, so that it fails the bundles and not the start of a server that needs no bundle.
func CacheVerifier(c *artifact.Cache) BundleVerifier { return cacheVerifier{c} }

type cacheVerifier struct{ c *artifact.Cache }

func (v cacheVerifier) Verify(bundleJSON, digest []byte, id artifact.Identity) (string, error) {
	ver, err := v.c.Verifier()
	if err != nil {
		return "", err
	}
	return ver.Verify(bundleJSON, digest, id)
}

// BundleDeps are the dependencies of the bundles.
type BundleDeps struct {
	// Version is the version of this server (internal/version).
	Version string
	// Dir is the directory of the artifact cache (artifacts.dir): the bundles live in its subdirectory install.
	Dir string
	// BaseURL is install.bundleSourceBaseUrl, with the layout <base>/v<version>/<asset>.
	BaseURL string
	// Platforms are the platforms of install.platforms.
	Platforms []Platform
	// Verifier checks checksums.txt of the release.
	Verifier BundleVerifier
	// HTTPClient, Policy, ProxyURL and AllowedRedirectHosts are the outbound rules of the server: the client with
	// the destination policy, the policy for the proxy, artifacts.proxyUrl and artifacts.allowedRedirectHosts.
	HTTPClient           *http.Client
	Policy               *netguard.Policy
	ProxyURL             string
	AllowedRedirectHosts []string
	// Directory is the directory of a development server (`myriarchd dev --install-bundle-dir`) whose archives are
	// served instead of the release. It needs DevMode.
	Directory string
	DevMode   bool
	Logger    *slog.Logger

	// the following are set by tests of this package
	minVersion string
	now        func() time.Time
	startDelay time.Duration
}

// Bundles keeps the installer bundles of this server.
type Bundles struct {
	d       BundleDeps
	log     *slog.Logger
	now     func() time.Time
	source  string
	version string
	reason  string
	baseURL string
	store   string // <dir>/install/sha256: the archives under their SHA-256
	tmp     string // <dir>/install/.tmp: downloads that are not checked yet
	fetch   *fetcher
	// size limits of the downloads (the constants of fetch.go; tests lower them)
	maxSums, maxSig, maxArchive int64

	mu      sync.Mutex
	items   []*bundleItem
	running bool
	wg      sync.WaitGroup
}

type bundleItem struct {
	b        Bundle
	failedAt time.Time
}

// NewBundles decides where the bundles come from and prepares their directories. It fetches nothing (Run, Prepare and
// the first request for a bundle do), except that a bundle directory of a development server is read at once.
func NewBundles(d BundleDeps) (*Bundles, error) {
	if d.Directory != "" && !d.DevMode {
		return nil, errors.New("install: a bundle directory (--install-bundle-dir) exists only in development mode: a server in production serves the bundles of its own release")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	b := &Bundles{d: d, log: d.Logger.With("component", "install-bundles"), now: d.now,
		maxSums: maxChecksumsBytes, maxSig: maxSigstoreBytes, maxArchive: maxBundleBytes}
	if b.now == nil {
		b.now = func() time.Time { return time.Now().UTC() }
	}
	floor := d.minVersion
	if floor == "" {
		floor = minBundleVersion
	}
	switch {
	case d.Directory != "":
		b.source = BundleSourceDirectory
	case releaseVersionFrom(d.Version, floor):
		b.source, b.version = BundleSourceRelease, d.Version
	default:
		b.source, b.reason = BundleSourceNone, ReasonLocalBuild
		b.log.Info("installer bundles are not offered: this server is not a release of the project, so there is no release whose bundles it could serve (a development server can serve a directory)", "version", d.Version)
		return b, nil
	}
	if d.Dir == "" {
		return nil, errors.New("install: the directory of the artifact cache is required")
	}
	root := filepath.Join(d.Dir, artifact.InstallDir)
	b.store, b.tmp = filepath.Join(root, "sha256"), filepath.Join(root, ".tmp")
	for _, dir := range []string{b.store, b.tmp} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("install: create the bundle directory: %w", err)
		}
	}
	b.cleanTmp()

	switch b.source {
	case BundleSourceRelease:
		u, err := url.Parse(d.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return nil, errors.New("install.bundleSourceBaseUrl must be an absolute http(s) URL without credentials")
		}
		if d.Verifier == nil {
			return nil, errors.New("install: a verifier of Sigstore bundles is required")
		}
		b.baseURL = strings.TrimRight(d.BaseURL, "/")
		if b.fetch, err = newFetcher(fetchDeps{HTTPClient: d.HTTPClient, Policy: d.Policy, ProxyURL: d.ProxyURL,
			AllowedRedirectHosts: d.AllowedRedirectHosts, BaseURL: b.baseURL, TmpDir: b.tmp}); err != nil {
			return nil, err
		}
		for _, p := range d.Platforms {
			b.items = append(b.items, &bundleItem{b: Bundle{OS: p.OS, Arch: p.Arch, Version: d.Version, Filename: BundleFilename(d.Version, p), Status: BundlePending}})
		}
	case BundleSourceDirectory:
		b.Prepare()
	}
	return b, nil
}

// Source is BundleSourceRelease, BundleSourceDirectory or BundleSourceNone.
func (b *Bundles) Source() string { return b.source }

// State returns the state of the bundles.
func (b *Bundles) State() BundleList {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := BundleList{Source: b.source, Version: b.version, Reason: b.reason, Items: make([]Bundle, 0, len(b.items))}
	for _, it := range b.items {
		out.Items = append(out.Items, it.b)
	}
	return out
}

// Find returns the bundle for a platform.
func (b *Bundles) Find(p Platform) (Bundle, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.items {
		if it.b.OS == p.OS && it.b.Arch == p.Arch {
			return it.b, true
		}
	}
	return Bundle{}, false
}

// Run prepares the bundles shortly after the start, so that they are ready when somebody asks, and waits for the end
// of the server and of a preparation that is under way.
func (b *Bundles) Run(ctx context.Context) error {
	if b.source == BundleSourceNone {
		<-ctx.Done()
		return nil
	}
	delay := b.d.startDelay
	if delay == 0 {
		delay = bundleStartDelay
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(delay):
	}
	b.Prepare()
	<-ctx.Done()
	return nil
}

// Wait waits until the preparation that is under way has finished, or until ctx is done.
func (b *Bundles) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Prepare makes sure that the bundles become ready, without making the caller wait: it starts the preparation in the
// background when a bundle is pending, or failed more than a minute ago. A bundle directory of a development server is
// read at once.
func (b *Bundles) Prepare() {
	switch b.source {
	case BundleSourceDirectory:
		b.prepareDirectory()
	case BundleSourceRelease:
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.running {
			return
		}
		var todo []*bundleItem
		now := b.now()
		for _, it := range b.items {
			if it.b.Status == BundlePending || (it.b.Status == BundleFailed && now.Sub(it.failedAt) >= bundleRetryAfter) {
				todo = append(todo, it)
			}
		}
		if len(todo) == 0 {
			return
		}
		for _, it := range todo {
			it.b.Status, it.b.Error = BundlePreparing, ""
		}
		b.running = true
		b.wg.Add(1)
		go b.prepareRelease(todo)
	}
}

// ErrBundleNotFound is returned for a name that is not the name of a bundle of this server.
var ErrBundleNotFound = errors.New("install: there is no such installer bundle")

// Open opens the archive of the bundle with the given file name for delivery, after it hashed the file again and found
// the SHA-256 that was pinned when the bundle became ready. The file is positioned at its start; the caller closes it.
// An archive that is gone or changed on the disk is discarded and fetched again, and the caller gets a refusal.
func (b *Bundles) Open(filename string) (*os.File, Bundle, error) {
	b.mu.Lock()
	var it *bundleItem
	for _, x := range b.items {
		if x.b.Filename == filename {
			it = x
		}
	}
	if it == nil {
		b.mu.Unlock()
		return nil, Bundle{}, ErrBundleNotFound
	}
	snap := it.b
	b.mu.Unlock()

	switch snap.Status {
	case BundleReady:
	case BundleFailed:
		b.Prepare()
		return nil, snap, bundleFailedRefusal(snap)
	default:
		b.Prepare()
		return nil, snap, bundlePreparingRefusal()
	}
	path := filepath.Join(b.store, snap.SHA256)
	f, err := artifact.OpenNoFollow(path)
	if err != nil {
		b.lost(it, snap, path, fmt.Sprintf("cannot be opened: %v", err), false)
		return nil, snap, bundlePreparingRefusal()
	}
	ok, why := checkOpenFile(f, snap)
	if !ok {
		f.Close()
		b.lost(it, snap, path, why, true)
		return nil, snap, bundlePreparingRefusal()
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, snap, fmt.Errorf("install: rewind the bundle: %w", err)
	}
	return f, snap, nil
}

// checkOpenFile hashes the open file and compares it with the pinned values. The delivery reads the same open file
// afterwards, so a replaced path cannot change what is sent.
func checkOpenFile(f *os.File, snap Bundle) (bool, string) {
	st, err := f.Stat()
	if err != nil {
		return false, fmt.Sprintf("cannot be read: %v", err)
	}
	if !st.Mode().IsRegular() || st.Size() != snap.Size {
		return false, "is not the file that was stored"
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, fmt.Sprintf("cannot be read: %v", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != snap.SHA256 {
		return false, "does not match its SHA-256 any more"
	}
	return true, ""
}

// lost handles a stored archive that cannot be delivered: the bundle becomes pending (it is fetched and checked again),
// and the file is removed when it is there but wrong.
func (b *Bundles) lost(it *bundleItem, snap Bundle, path, why string, remove bool) {
	b.log.Error("an installer bundle on the disk cannot be delivered, it is fetched again", "file", snap.Filename, "sha256", snap.SHA256, "reason", why)
	if remove {
		_ = os.Remove(path)
	}
	b.mu.Lock()
	if it.b.Status == BundleReady && it.b.SHA256 == snap.SHA256 {
		it.b.Status, it.b.SHA256, it.b.Size, it.b.Error = BundlePending, "", 0, ""
	}
	b.mu.Unlock()
	b.Prepare()
}

// ------------------------------------------------------------------- the release ----

func (b *Bundles) assetURL(name string) string { return b.baseURL + "/v" + b.version + "/" + name }

func (b *Bundles) prepareRelease(todo []*bundleItem) {
	defer b.wg.Done()
	defer func() {
		b.mu.Lock()
		b.running = false
		b.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), prepareTimeout)
	defer cancel()
	b.log.Info("installer bundles: preparing", "version", b.version, "source", redact(b.baseURL), "platforms", len(todo))

	lines, err := b.signedChecksums(ctx)
	if err != nil {
		b.log.Warn("installer bundles: checksums.txt of the release cannot be used, no bundle is delivered", "version", b.version, "error", err)
		for _, it := range todo {
			b.finish(it, "", 0, err)
		}
		return
	}
	for _, it := range todo {
		sum, size, err := b.fetchArchive(ctx, it.b.Filename, lines)
		if err != nil {
			b.log.Warn("installer bundle failed", "file", it.b.Filename, "error", err)
		} else {
			b.log.Info("installer bundle ready", "file", it.b.Filename, "sha256", sum, "size", size)
		}
		b.finish(it, sum, size, err)
	}
	b.mu.Lock()
	keep := b.keepSet()
	b.mu.Unlock()
	b.tidy(keep)
}

// finish records the outcome of the preparation of one bundle.
func (b *Bundles) finish(it *bundleItem, sum string, size int64, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		it.b.Status, it.b.SHA256, it.b.Size, it.b.Error = BundleFailed, "", 0, limit(err.Error(), maxErrorLen)
		it.failedAt = b.now()
		return
	}
	it.b.Status, it.b.SHA256, it.b.Size, it.b.Error = BundleReady, sum, size, ""
}

// signedChecksums downloads checksums.txt of the release and its keyless bundle, verifies the signature against the
// identity of the release workflow at the tag of the version and returns the lines of the file.
func (b *Bundles) signedChecksums(ctx context.Context) (checksumLines, error) {
	sums, err := b.fetch.get(ctx, b.assetURL("checksums.txt"), b.maxSums)
	if err != nil {
		return nil, fmt.Errorf("checksums.txt: %w", err)
	}
	sig, err := b.fetch.get(ctx, b.assetURL("checksums.txt.sigstore.json"), b.maxSig)
	if err != nil {
		return nil, fmt.Errorf("checksums.txt.sigstore.json: %w", err)
	}
	digest := sha256.Sum256(sums)
	san, err := b.d.Verifier.Verify(sig, digest[:], ReleaseIdentity(b.version))
	if err != nil {
		return nil, fmt.Errorf("checksums.txt of v%s is not signed by the release workflow of this project: %w", b.version, err)
	}
	b.log.Info("installer bundles: checksums.txt verified", "version", b.version, "identity", san)
	return parseChecksums(sums), nil
}

// fetchArchive makes sure that the archive is stored under its SHA-256, the one of its line in checksums.txt, and
// returns the SHA-256 and the size. An archive of an earlier run is used when it is still what the line says.
func (b *Bundles) fetchArchive(ctx context.Context, name string, lines checksumLines) (string, int64, error) {
	want, err := lines.one(name)
	if err != nil {
		return "", 0, err
	}
	final := filepath.Join(b.store, want)
	if sum, size, err := hashStored(final); err == nil && sum == want {
		return want, size, nil
	}
	tmp, got, size, err := b.fetch.download(ctx, b.assetURL(name), b.maxArchive)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp)
	if g := hex.EncodeToString(got[:]); g != want {
		return "", 0, fmt.Errorf("%s has the SHA-256 %s, but its line in the signed checksums.txt says %s", name, g, want)
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		return "", 0, fmt.Errorf("chmod the bundle: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", 0, fmt.Errorf("move the bundle into place: %w", err)
	}
	return want, size, nil
}

// hashStored returns the SHA-256 and the size of a regular file (a link is not followed).
func hashStored(path string) (string, int64, error) {
	f, err := artifact.OpenNoFollow(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, errors.New("not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

// ------------------------------------------------------------------ checksums.txt ----

// checksumLines are the lines of a checksums.txt by file name, each with all the hashes it was given.
type checksumLines map[string][]string

var checksumLineRE = regexp.MustCompile(`^([0-9a-fA-F]{64}) [ *](\S.*)$`)

// parseChecksums reads the lines of a checksums.txt in the format of sha256sum. A line that does not have this shape
// is of no use to anyone and is left out.
func parseChecksums(raw []byte) checksumLines {
	out := checksumLines{}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := checksumLineRE.FindStringSubmatch(line); m != nil {
			out[m[2]] = append(out[m[2]], strings.ToLower(m[1]))
		}
	}
	return out
}

// one returns the hash of the one line for the file name, and an error when there is none or more than one: a file
// that is listed twice is ambiguous, whether the lines agree or not.
func (l checksumLines) one(name string) (string, error) {
	switch hashes := l[name]; len(hashes) {
	case 1:
		return hashes[0], nil
	case 0:
		return "", fmt.Errorf("the signed checksums.txt has no line for %s", name)
	default:
		return "", fmt.Errorf("the signed checksums.txt has %d lines for %s, expected exactly one", len(hashes), name)
	}
}

// ----------------------------------------------------------------- the directory ----

// prepareDirectory reads the archives of the directory of a development server: the highest version among the files
// that are named like the archives of a release, and for each platform the file of that version. Every call reads the
// directory again, so that an archive that was built in the meantime is found. The archives are regular files that the
// developer put there; they are copied into the store under their SHA-256 and delivered like the ones of a release.
func (b *Bundles) prepareDirectory() {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries, err := os.ReadDir(b.d.Directory)
	if err != nil {
		b.setDirectoryFailed(fmt.Errorf("cannot read the bundle directory: %w", err))
		return
	}
	var best *semver.Version
	for _, e := range entries {
		m := bundleFileRE.FindStringSubmatch(e.Name())
		if m == nil || !b.hasPlatform(m[2], m[3]) {
			continue
		}
		if v, err := semver.StrictNewVersion(m[1]); err == nil && (best == nil || v.GreaterThan(best)) {
			best = v
		}
	}
	if best == nil {
		b.setDirectoryFailed(fmt.Errorf("the bundle directory holds no archive named %s_<version>_<os>_<arch>.tar.gz or .zip for the platforms of install.platforms", brand.UpdaterBinary))
		return
	}
	b.version = best.Original()
	items := make([]*bundleItem, 0, len(b.d.Platforms))
	for _, p := range b.d.Platforms {
		it := &bundleItem{b: Bundle{OS: p.OS, Arch: p.Arch, Version: b.version, Filename: BundleFilename(b.version, p), Status: BundlePending}}
		sum, size, err := b.storeFromDirectory(it.b.Filename)
		if err != nil {
			it.b.Status, it.b.Error = BundleFailed, limit(err.Error(), maxErrorLen)
			it.failedAt = b.now()
			b.log.Warn("installer bundle from the directory failed", "file", it.b.Filename, "error", err)
		} else {
			it.b.Status, it.b.SHA256, it.b.Size = BundleReady, sum, size
		}
		items = append(items, it)
	}
	b.items = items
	b.tidy(b.keepSet())
}

func (b *Bundles) hasPlatform(os, arch string) bool {
	for _, p := range b.d.Platforms {
		if p.OS == os && p.Arch == arch {
			return true
		}
	}
	return false
}

func (b *Bundles) setDirectoryFailed(err error) {
	b.log.Warn("installer bundles from the directory are not available", "error", err)
	b.version = ""
	items := make([]*bundleItem, 0, len(b.d.Platforms))
	for _, p := range b.d.Platforms {
		items = append(items, &bundleItem{b: Bundle{OS: p.OS, Arch: p.Arch, Filename: "", Status: BundleFailed, Error: limit(err.Error(), maxErrorLen)}, failedAt: b.now()})
	}
	b.items = items
}

// storeFromDirectory copies a regular file of the directory into the store under its SHA-256, unless the store has the
// file already.
func (b *Bundles) storeFromDirectory(name string) (string, int64, error) {
	path := filepath.Join(b.d.Directory, name)
	st, err := os.Lstat(path)
	if err != nil {
		return "", 0, fmt.Errorf("the bundle directory has no file %s", name)
	}
	if !st.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s in the bundle directory is not a regular file (links are not followed)", name)
	}
	sum, size, err := hashStored(path)
	if err != nil {
		return "", 0, fmt.Errorf("%s in the bundle directory cannot be read: %w", name, err)
	}
	final := filepath.Join(b.store, sum)
	if have, _, err := hashStored(final); err == nil && have == sum {
		return sum, size, nil
	}
	if err := copyFile(path, final, b.tmp); err != nil {
		return "", 0, fmt.Errorf("copy %s into the bundle store: %w", name, err)
	}
	if got, _, err := hashStored(final); err != nil || got != sum {
		_ = os.Remove(final)
		return "", 0, fmt.Errorf("%s changed while it was copied", name)
	}
	return sum, size, nil
}

// copyFile copies src to dst through a temporary file in tmpDir, with the mode 0640.
func copyFile(src, dst, tmpDir string) error {
	in, err := artifact.OpenNoFollow(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(tmpDir, "cp-*")
	if err != nil {
		return err
	}
	name := out.Name()
	defer os.Remove(name)
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o640); err != nil {
		return err
	}
	return os.Rename(name, dst)
}

// ---------------------------------------------------------------------- tidying ----

// keepSet is the set of the SHA-256 values that the bundles refer to. The caller holds the lock.
func (b *Bundles) keepSet() map[string]bool {
	keep := map[string]bool{}
	for _, it := range b.items {
		if it.b.SHA256 != "" {
			keep[it.b.SHA256] = true
		}
	}
	return keep
}

// tidy removes the files of the store that no bundle refers to any more (the archives of an earlier version of the
// server), once they are a day old, so that the replica that is being replaced during a rolling update keeps its own.
func (b *Bundles) tidy(keep map[string]bool) {
	entries, err := os.ReadDir(b.store)
	if err != nil {
		return
	}
	var gone []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || keep[e.Name()] || !isHexDigest(e.Name()) || b.now().Sub(info.ModTime()) < orphanAge {
			continue
		}
		if os.Remove(filepath.Join(b.store, e.Name())) == nil {
			gone = append(gone, e.Name()[:12])
		}
	}
	if len(gone) > 0 {
		sort.Strings(gone)
		b.log.Info("installer bundles of an earlier version removed", "sha256", gone)
	}
}

// cleanTmp removes downloads of a run that ended before they were checked.
func (b *Bundles) cleanTmp() {
	entries, err := os.ReadDir(b.tmp)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(b.tmp, e.Name()))
		}
	}
}

func isHexDigest(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func limit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------- refusals ----

func bundlePreparingRefusal() *Refusal {
	r := refuse(domain.ErrUnavailable, CodeBundlePreparing, "the installer bundle is being prepared: ask again after the time in Retry-After")
	r.RetryAfter = retryBundlePreparing
	return r
}

func bundleFailedRefusal(b Bundle) *Refusal {
	r := refuse(domain.ErrUnavailable, CodeBundleFailed,
		"the installer bundle for %s/%s cannot be delivered: the last attempt to prepare it failed (%s); the server tries again", b.OS, b.Arch, b.Error)
	r.RetryAfter = retryBundleFailed
	return r
}
