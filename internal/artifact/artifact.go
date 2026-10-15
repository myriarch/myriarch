// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package artifact implements the artifact cache: download release assets
// from upstream (or an internal mirror), verify SHA-256 (<asset>.sha256) and
// the sigstore bundle (<asset>.sigstore.json, cosign keyless, sigstore-go),
// store them on local disk per replica, and serve them to myriarch-updaters with
// short-lived signed download tokens (HTTP Range supported).
package artifact

import (
	"context"
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
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/store"
)

const (
	// maxArchiveBytes caps a single download.
	maxArchiveBytes = 2 << 30
	// maxSmallFile caps checksum and bundle downloads.
	maxSmallFile     = 1 << 20
	ensureTimeout    = 45 * time.Minute
	smallTimeout     = 2 * time.Minute
	touchInterval    = 5 * time.Minute
	prefetchInterval = 10 * time.Minute
)

// Deps are the dependencies of the cache.
type Deps struct {
	Store      store.Store
	Bus        eventbus.Bus
	HTTPClient *http.Client
	// PublicURL is config.ServerConfig.PublicURL (absolute download URLs).
	PublicURL string
	// DownloadSecret is the HMAC key for download tokens.
	DownloadSecret []byte
	Logger         *slog.Logger
	Registerer     prometheus.Registerer
	// Policy (optional) is the outbound destination policy used for the proxy
	// transport; the shared HTTPClient carries it otherwise.
	Policy *netguard.Policy
	// InstallEnabled is install.enabled: the cache mints and accepts install grants only while the installation
	// from the server is on. A grant that was minted before the setting was turned off (a restart in between) is
	// refused with 401 however long it would still live; the zero value is off.
	InstallEnabled bool
	// InstallGrantTTL is the lifetime of an install grant (install.grantTtl;
	// 0 = no expiry, development only).
	InstallGrantTTL time.Duration
}

// InstallDir is the subdirectory of the cache directory in which internal/install keeps the installer bundles. The
// cache leaves it alone: Evict does not count or remove its files.
const InstallDir = "install"

// downloadIdleTimeout aborts a download that delivers no data for this long.
const downloadIdleTimeout = 60 * time.Second

// idleTimeoutOverride shortens downloadIdleTimeout in tests.
var idleTimeoutOverride time.Duration

// maxConcurrentDownloads bounds simultaneous archive downloads per replica.
const maxConcurrentDownloads = 3

// defaultRedirectHosts are the hosts upstream downloads may be redirected to
// (GitHub release assets).
var defaultRedirectHosts = []string{"github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "*.githubusercontent.com"}

// Cache is the artifact cache.
type Cache struct {
	cfg  config.ArtifactsConfig
	deps Deps
	log  *slog.Logger
	// now is the clock (tests).
	now func() time.Time

	dir     string
	baseURL string
	small   *http.Client // checksum, bundle: with timeout
	large   *http.Client // archives: no overall timeout
	// tuf fetches the Sigstore TUF metadata: same transport as the downloads
	// (proxy, trust store, credentials of the mirror for its own origin),
	// default redirect rules.
	tuf  *http.Client
	auth *sourceAuth // credentials of the mirror (nil = none)

	// agentBases are the parsed artifacts.agentBaseUrls (agentbase.go).
	agentBases []agentBase

	ver *bundleVerifier // nil if signatures are not required
	// verOnce builds the verifier that Verifier hands out when signatures of the artifacts are not required.
	verOnce  sync.Once
	verifier *Verifier
	verErr   error
	// verifyFn verifies a bundle; replaced in tests (real archives are too big
	// to ship, and real bundles only match the real archives).
	verifyFn func(bundleJSON, digest []byte, dist, version string) (*verified, error)

	ensureGroup singleflight.Group
	fetchGroup  singleflight.Group
	dlSem       chan struct{}

	verifiedMu sync.Mutex
	verified   map[string]fileStamp

	touchMu   sync.Mutex
	lastTouch map[string]time.Time

	// downloads started by Prepare that have not finished yet, by spec key (prepare.go)
	prepMu    sync.Mutex
	preparing map[string]struct{}
	prepWG    sync.WaitGroup

	downloads *prometheus.CounterVec
}

// NewCache creates the cache (creates cfg.Dir).
func NewCache(cfg config.ArtifactsConfig, deps Deps) (*Cache, error) {
	c := &Cache{cfg: cfg, deps: deps, now: func() time.Time { return time.Now().UTC() }, lastTouch: map[string]time.Time{},
		dlSem: make(chan struct{}, maxConcurrentDownloads), verified: map[string]fileStamp{}}
	c.log = deps.Logger
	if c.log == nil {
		c.log = slog.Default()
	}
	c.log = c.log.With("component", "artifact")
	c.dir = cfg.Dir
	if c.dir == "" {
		c.dir = filepath.Join(os.TempDir(), brand.ServerBinary+"-artifacts")
	}
	if err := os.MkdirAll(filepath.Join(c.dir, ".tmp"), 0o750); err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	c.cleanTmp()
	bases, err := parseAgentBases(cfg.AgentBaseURLs)
	if err != nil {
		return nil, err
	}
	c.agentBases = bases
	c.baseURL = strings.TrimRight(cfg.SourceBaseURL, "/")
	if c.baseURL == "" {
		c.baseURL = strings.TrimRight(config.Default().Artifacts.SourceBaseURL, "/")
	}
	if cfg.MaxCacheBytes <= 0 {
		c.cfg.MaxCacheBytes = config.Default().Artifacts.MaxCacheBytes
	}

	base := deps.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	small, large := *base, *base
	small.Timeout = smallTimeout
	large.Timeout = 0
	if cfg.ProxyURL != "" {
		pu, err := url.Parse(cfg.ProxyURL)
		if err != nil || pu.Host == "" {
			return nil, fmt.Errorf("artifacts.proxyUrl %q is invalid", redactURL(cfg.ProxyURL))
		}
		var tr *http.Transport
		if deps.Policy != nil {
			tr = deps.Policy.TransportWithProxy(pu)
		} else {
			tr = http.DefaultTransport.(*http.Transport).Clone()
			tr.Proxy = http.ProxyURL(pu)
		}
		small.Transport, large.Transport = tr, tr
	}
	// Credentials for the mirror: added at the transport, only to requests with
	// the origin of sourceBaseUrl (also after redirects).
	auth, err := newSourceAuth(cfg.SourceAuth, c.baseURL)
	if err != nil {
		return nil, err
	}
	if auth != nil {
		c.auth = auth
		small.Transport, large.Transport = auth.wrap(small.Transport), auth.wrap(large.Transport)
	}
	// Redirects: at most 5 hops, https only and only to the release asset
	// hosts (or the configured mirror itself).
	redirect := c.redirectPolicy()
	small.CheckRedirect, large.CheckRedirect = redirect, redirect
	c.small, c.large = &small, &large
	c.tuf = &http.Client{Transport: small.Transport, Timeout: smallTimeout, CheckRedirect: netguard.RedirectPolicy(netguard.MaxRedirects, nil)}

	if cfg.Verify.RequireSignature {
		v, err := newBundleVerifier(cfg.Verify, c.dir, c.tuf)
		if err != nil {
			return nil, err
		}
		c.ver = v
		c.verifyFn = v.verifyBundle
	}

	c.downloads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_artifact_downloads_total", Help: "Artifact downloads served to agents by result.",
	}, []string{"result"})
	if deps.Registerer != nil {
		if err := deps.Registerer.Register(c.downloads); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				if cv, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
					c.downloads = cv
				}
			}
		}
	}
	return c, nil
}

// redirectPolicy is the CheckRedirect of the upstream clients.
func (c *Cache) redirectPolicy() func(*http.Request, []*http.Request) error {
	return RedirectPolicy(c.cfg.AllowedRedirectHosts, c.baseURL)
}

// RedirectPolicy is the CheckRedirect of a client that downloads release assets from sourceBaseURL: at most five hops,
// https targets only, and only the hosts of allowedHosts (artifacts.allowedRedirectHosts; the GitHub asset hosts when it
// is empty) and the host of sourceBaseURL itself. A plain-http source may redirect within itself (lab setups). The
// artifact cache and the installer bundles use it, so both follow the same rule.
func RedirectPolicy(allowedHosts []string, sourceBaseURL string) func(*http.Request, []*http.Request) error {
	hosts := append([]string(nil), allowedHosts...)
	if len(hosts) == 0 {
		hosts = append(hosts, defaultRedirectHosts...)
	}
	var base *url.URL
	if u, err := url.Parse(sourceBaseURL); err == nil {
		base = u
		hosts = append(hosts, u.Hostname())
	}
	httpsOnly := netguard.RedirectPolicy(netguard.MaxRedirects, hosts)
	return func(req *http.Request, via []*http.Request) error {
		// a plain-http mirror may redirect within itself (lab setups)
		if base != nil && req.URL.Scheme == base.Scheme && req.URL.Host == base.Host && len(via) < netguard.MaxRedirects {
			return nil
		}
		return httpsOnly(req, via)
	}
}

// OpenNoFollow opens a file of the cache directory for reading without following a symbolic link at the final path
// element (on Windows a link is refused after the open).
func OpenNoFollow(path string) (*os.File, error) { return openNoFollow(path) }

// SignatureRequired reports whether artifacts must carry a verified sigstore
// bundle (artifacts.verify.requireSignature).
func (c *Cache) SignatureRequired() bool { return c.cfg.Verify.RequireSignature }

// Dir is the directory of the cache (artifacts.dir, or the default below the temporary directory).
func (c *Cache) Dir() string { return c.dir }

// Verifier returns the verifier of Sigstore bundles with the trust of the cache: the trusted root of artifacts.verify (a
// file, or TUF through the HTTP client of the cache) and no identity of its own; the caller names the identity it
// accepts. It exists also where artifacts.verify.requireSignature is off, because a bundle that is not an upstream
// artifact (the installer bundles of the release of this project) is verified whatever that setting says. The error
// is that of an unusable trusted root or identity setting.
func (c *Cache) Verifier() (*Verifier, error) {
	c.verOnce.Do(func() {
		v := c.ver
		if v == nil {
			var err error
			if v, err = newBundleVerifier(c.cfg.Verify, c.dir, c.tuf); err != nil {
				c.verErr = err
				return
			}
		}
		c.verifier = &Verifier{v: v}
	})
	return c.verifier, c.verErr
}

// DownloadTokenTTL is the lifetime of download tokens (0 = unlimited).
func (c *Cache) DownloadTokenTTL() time.Duration { return c.cfg.DownloadTokenTTL }

// cleanTmp removes stale temporary downloads of a previous run.
func (c *Cache) cleanTmp() {
	entries, err := os.ReadDir(filepath.Join(c.dir, ".tmp"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(c.dir, ".tmp", e.Name()))
		}
	}
}

// ------------------------------------------------------------------- specs ----

// spec identifies one artifact.
type spec struct {
	dist, version, os, arch string
	filename, format, tag   string
}

var (
	validOS   = map[string]bool{"linux": true, "windows": true, "darwin": true, "aix": true}
	archAlias = map[string]string{
		"x86_64": "amd64", "x64": "amd64", "aarch64": "arm64", "x86": "386", "i386": "386", "i686": "386",
		"arm32": "armv7", "arm": "armv7", "armv7l": "armv7", "ppc64le": "ppc64le",
	}
	archRE = regexp.MustCompile(`^[a-z0-9]{2,12}$`)
)

// NormalizeArch maps host.arch values (OpenTelemetry semantic conventions and
// uname spellings) to the architecture names of the release assets.
func NormalizeArch(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	if n, ok := archAlias[a]; ok {
		return n
	}
	return a
}

// NormalizeOS lower-cases the os.type of an agent.
func NormalizeOS(o string) string { return strings.ToLower(strings.TrimSpace(o)) }

// newSpec validates the inputs and derives asset names
// (docs/design/upstream-notes.md, Release assets and signatures > Asset names
// and platforms):
// otelcol-contrib_<ver>_<os>_<arch>.tar.gz (tar.gz for ALL operating systems),
// opampsupervisor_<ver>_<os>_<arch>[.exe] under the tag cmd/opampsupervisor/v<ver>.
func newSpec(dist, version, osName, arch string) (spec, error) {
	version = domain.NormalizeVersion(version)
	if _, err := semver.StrictNewVersion(version); err != nil {
		return spec{}, fmt.Errorf("%w: invalid version %q", domain.ErrInvalidArgument, version)
	}
	osName, arch = NormalizeOS(osName), NormalizeArch(arch)
	if !validOS[osName] {
		return spec{}, fmt.Errorf("%w: unsupported operating system %q", domain.ErrInvalidArgument, osName)
	}
	if !archRE.MatchString(arch) {
		return spec{}, fmt.Errorf("%w: invalid architecture %q", domain.ErrInvalidArgument, arch)
	}
	s := spec{dist: dist, version: version, os: osName, arch: arch}
	switch dist {
	case domain.DistributionContrib:
		s.filename = fmt.Sprintf("otelcol-contrib_%s_%s_%s.tar.gz", version, osName, arch)
		s.format = "tar.gz"
		s.tag = "v" + version
	case domain.DistributionSupervisor:
		s.filename = fmt.Sprintf("opampsupervisor_%s_%s_%s", version, osName, arch)
		if osName == "windows" {
			s.filename += ".exe"
		}
		s.format = "binary"
		s.tag = "cmd/opampsupervisor/v" + version
	default:
		return spec{}, fmt.Errorf("%w: unsupported distribution %q", domain.ErrInvalidArgument, dist)
	}
	return s, nil
}

func (s spec) key() string { return strings.Join([]string{s.dist, s.version, s.os, s.arch}, "|") }

func (c *Cache) sourceURL(s spec, suffix string) string {
	return c.baseURL + "/" + s.tag + "/" + s.filename + suffix
}

// path is the local file of an artifact. Verified files are content
// addressed (<dir>/sha256/<hex>): a file is always the bytes whose hash was
// verified, and two records can never overwrite each other's file.
func (c *Cache) path(a *domain.Artifact) string {
	if len(a.SHA256) == 32 {
		return c.hashPath(a.SHA256)
	}
	return filepath.Join(c.dir, a.Distribution, a.Version, a.Filename)
}

func (c *Cache) hashPath(sum []byte) string {
	return filepath.Join(c.dir, "sha256", hex.EncodeToString(sum))
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid>"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// -------------------------------------------------------------------- ensure ----

// Ensure returns a ready, verified artifact, downloading it if needed.
// Concurrent calls for the same artifact download it once.
func (c *Cache) Ensure(ctx context.Context, distribution, version, os, arch string) (*domain.Artifact, error) {
	s, err := newSpec(distribution, version, os, arch)
	if err != nil {
		return nil, err
	}
	ch := c.ensureGroup.DoChan(s.key(), func() (any, error) {
		// The work is shared: it must not die with the first caller's context.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ensureTimeout)
		defer cancel()
		return c.ensure(wctx, s)
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		a := *(r.Val.(*domain.Artifact))
		return &a, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Cache) ensure(ctx context.Context, s spec) (*domain.Artifact, error) {
	arts := c.deps.Store.Artifacts()
	rec, err := arts.Find(ctx, s.dist, s.version, s.os, s.arch, s.format)
	switch {
	case err == nil:
		// a record that would not be served (no pinned hash, no verified bundle) is fetched again
		if c.servable(rec) {
			return rec, nil
		}
	case !errors.Is(err, domain.ErrNotFound):
		return nil, fmt.Errorf("find artifact: %w", err)
	}

	now := c.now()
	rec, err = arts.Upsert(ctx, domain.Artifact{
		ID: uuid.Must(uuid.NewV7()), Distribution: s.dist, Version: s.version, OS: s.os, Arch: s.arch, Format: s.format,
		Filename: s.filename, SourceURL: c.sourceURL(s, ""), Status: domain.ArtifactDownloading, CreatedAt: now,
	})
	if err != nil {
		return nil, fmt.Errorf("record artifact: %w", err)
	}
	c.publish(ctx, rec)

	ready, rerr := c.fetchAndVerify(ctx, s, rec)
	if rerr != nil {
		c.log.Error("artifact download failed", "distribution", s.dist, "version", s.version, "os", s.os, "arch", s.arch, "error", rerr)
		failed := *rec
		failed.Status = domain.ArtifactFailed
		failed.Error = truncate(rerr.Error(), 500)
		var ve *verifyError
		if errors.As(rerr, &ve) {
			failed.VerificationError = truncate(ve.Error(), 500)
		}
		failed.SignatureVerified = false
		if stored, uerr := arts.Upsert(ctx, failed); uerr != nil {
			c.log.Error("record artifact failure failed", "error", uerr)
		} else {
			c.publish(ctx, stored)
		}
		return nil, rerr
	}
	c.log.Info("artifact ready", "distribution", s.dist, "version", s.version, "os", s.os, "arch", s.arch,
		"size", ready.Size, "sha256", ready.SHA256.Short(), "identity", ready.SignatureIdentity)
	c.publish(ctx, ready)
	return ready, nil
}

// verifyError marks signature problems (stored as VerificationError).
type verifyError struct{ err error }

func (e *verifyError) Error() string { return e.err.Error() }
func (e *verifyError) Unwrap() error { return e.err }

func (c *Cache) publish(ctx context.Context, a *domain.Artifact) {
	if c.deps.Bus == nil {
		return
	}
	if err := c.deps.Bus.Publish(ctx, eventbus.ArtifactChanged, eventbus.ArtifactChangedPayload{ArtifactID: a.ID, Status: a.Status}); err != nil {
		c.log.Debug("publish artifact.changed failed", "error", err)
	}
}

// fetchAndVerify downloads checksum, bundle and archive, verifies them and
// moves the archive into place.
func (c *Cache) fetchAndVerify(ctx context.Context, s spec, rec *domain.Artifact) (*domain.Artifact, error) {
	var expected []byte
	if c.cfg.Verify.RequireChecksum || c.cfg.Verify.RequireSignature {
		txt, err := c.getSmall(ctx, c.sourceURL(s, ".sha256"))
		if err != nil {
			return nil, fmt.Errorf("download checksum: %w", err)
		}
		first := strings.Fields(txt)
		if len(first) == 0 {
			return nil, fmt.Errorf("checksum file %s is empty", redactURL(c.sourceURL(s, ".sha256")))
		}
		expected, err = hexDigest(first[0])
		if err != nil {
			return nil, fmt.Errorf("checksum file %s: %w", redactURL(c.sourceURL(s, ".sha256")), err)
		}
	}

	var bundleJSON []byte
	var identity string
	verified := false
	if c.cfg.Verify.RequireSignature {
		b, err := c.getSmall(ctx, c.sourceURL(s, ".sigstore.json"))
		if err != nil {
			return nil, &verifyError{fmt.Errorf("download sigstore bundle: %w", err)}
		}
		bundleJSON = []byte(b)
		if expected != nil { // verify before downloading 100 MB
			res, err := c.verifyFn(bundleJSON, expected, s.dist, s.version)
			if err != nil {
				return nil, &verifyError{err}
			}
			identity, verified = res.Identity, true
		}
	}

	tmp, sum, size, err := c.download(ctx, c.sourceURL(s, ""))
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	if expected != nil && !equalBytes(sum, expected) {
		return nil, fmt.Errorf("checksum mismatch: downloaded archive has SHA-256 %x but %s announces %x", sum, redactURL(c.sourceURL(s, ".sha256")), expected)
	}
	if c.cfg.Verify.RequireSignature && !verified {
		res, err := c.verifyFn(bundleJSON, sum, s.dist, s.version)
		if err != nil {
			return nil, &verifyError{err}
		}
		identity, verified = res.Identity, true
	}

	final := c.hashPath(sum)
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		return nil, fmt.Errorf("chmod artifact: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, fmt.Errorf("move artifact into place: %w", err)
	}

	done := *rec
	done.Status, done.Error, done.VerificationError = domain.ArtifactReady, "", ""
	done.Size, done.SHA256 = size, domain.Hash(sum)
	done.SignatureVerified, done.SignatureIdentity = verified, identity
	done.SigstoreBundle = bundleJSON
	stored, err := c.deps.Store.Artifacts().Upsert(ctx, done)
	if err != nil {
		return nil, fmt.Errorf("record artifact: %w", err)
	}
	return stored, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := range a {
		d |= a[i] ^ b[i]
	}
	return d == 0
}

// getSmall downloads a small text file.
func (c *Cache) getSmall(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, smallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("request %s: %w", redactURL(rawURL), err)
	}
	resp, err := c.small.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", redactURL(rawURL), cleanURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", c.statusError(rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSmallFile+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", redactURL(rawURL), err)
	}
	if len(body) > maxSmallFile {
		return "", fmt.Errorf("%s is larger than %d bytes", redactURL(rawURL), maxSmallFile)
	}
	return string(body), nil
}

func cleanURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err // strips the URL (it may carry credentials in userinfo)
	}
	return err
}
