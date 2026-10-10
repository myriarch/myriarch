// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/version"
)

// trustedRootTTL is how long a TUF-fetched trusted root is reused.
const trustedRootTTL = 24 * time.Hour

// expectedWorkflow pins the signing workflow per distribution
// (docs/design/upstream-notes.md, Release assets and signatures > Signatures):
// contrib archives are signed by base-release.yaml, supervisor binaries by
// base-binary-release.yaml (both for the tag v<version>).
var expectedWorkflow = map[string]string{
	domain.DistributionContrib:    "base-release.yaml",
	domain.DistributionSupervisor: "base-binary-release.yaml",
}

// bundleVerifier verifies sigstore bundles with identity pinning.
type bundleVerifier struct {
	cfg      config.ArtifactVerifyConfig
	tufCache string
	sanRE    *regexp.Regexp
	// client carries the TUF requests (nil = the default client of net/http).
	client *http.Client

	mu       sync.Mutex
	trusted  root.TrustedMaterial
	loadedAt time.Time
	// fetchRoot is replaced in tests.
	fetchRoot func() (root.TrustedMaterial, error)
}

func newBundleVerifier(cfg config.ArtifactVerifyConfig, dir string, client *http.Client) (*bundleVerifier, error) {
	v := &bundleVerifier{cfg: cfg, tufCache: filepath.Join(dir, "tuf"), client: client}
	if cfg.TUFRepositoryURL != "" {
		u, err := url.Parse(cfg.TUFRepositoryURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, errors.New("artifacts.verify.tufRepositoryUrl must be an absolute http(s) URL")
		}
	}
	if cfg.CertificateOIDCIssuer == "" {
		v.cfg.CertificateOIDCIssuer = "https://token.actions.githubusercontent.com"
	}
	if v.cfg.CertificateIdentityRegexp == "" {
		v.cfg.CertificateIdentityRegexp = config.Default().Artifacts.Verify.CertificateIdentityRegexp
	}
	re, err := regexp.Compile(v.cfg.CertificateIdentityRegexp)
	if err != nil {
		return nil, fmt.Errorf("artifacts.verify.certificateIdentityRegexp: %w", err)
	}
	v.sanRE = re
	v.fetchRoot = v.loadRoot
	if cfg.TrustedRootFile != "" {
		// Fail at startup (not at the first download) if the file is unusable.
		if _, err := LoadTrustedRootFile(cfg.TrustedRootFile); err != nil {
			return nil, fmt.Errorf("artifacts.verify.trustedRootFile: %w", err)
		}
	}
	return v, nil
}

// LoadTrustedRootFile loads a Sigstore trusted_root.json and refuses a file that
// cannot verify anything. The sigstore-go loader accepts a document without any
// trust material (for example the output of `cosign trusted-root create`
// without flags); every verification would then fail closed and block the
// version rollouts, while startup and `myriarchd config validate` looked fine.
// The verifier below requires a Fulcio certificate chain, signed certificate
// timestamps (CT log) and a Rekor transparency log entry, so the root must
// contain all three.
func LoadTrustedRootFile(path string) (*root.TrustedRoot, error) {
	tr, err := root.NewTrustedRootFromPath(path)
	if err != nil {
		return nil, err
	}
	var missing []string
	if len(tr.FulcioCertificateAuthorities()) == 0 {
		missing = append(missing, "a Fulcio certificate authority")
	}
	if len(tr.CTLogs()) == 0 {
		missing = append(missing, "a certificate transparency log")
	}
	if len(tr.RekorLogs()) == 0 {
		missing = append(missing, "a Rekor transparency log")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the trusted root contains no trust material (missing %s); export it with `cosign initialize` and use ~/.sigstore/root/tuf-repo-cdn.sigstore.dev/targets/trusted_root.json (not `cosign trusted-root create` without flags)", strings.Join(missing, ", "))
	}
	return tr, nil
}

// loadRoot loads the trusted root from the configured file (offline) or via TUF.
func (v *bundleVerifier) loadRoot() (root.TrustedMaterial, error) {
	if v.cfg.TrustedRootFile != "" {
		tr, err := LoadTrustedRootFile(v.cfg.TrustedRootFile)
		if err != nil {
			return nil, fmt.Errorf("load trusted root %s: %w", v.cfg.TrustedRootFile, err)
		}
		return tr, nil
	}
	tr, err := root.FetchTrustedRootWithOptions(v.tufOptions())
	if err != nil {
		return nil, fmt.Errorf("fetch sigstore trusted root via TUF from %s: %w", v.tufBase(), err)
	}
	return tr, nil
}

// tufBase is the repository the trusted root is fetched from.
func (v *bundleVerifier) tufBase() string {
	if v.cfg.TUFRepositoryURL != "" {
		return redactURL(v.cfg.TUFRepositoryURL)
	}
	return tuf.DefaultMirror
}

// tufOptions are the options of the TUF client. The requests go through the
// HTTP client of the artifact cache, so the proxy environment, the trust store
// of outbound.caFile, the destination policy and (for the origin of the
// mirror) its credentials apply to them like to every other outbound
// connection. A configured repository (a 1:1 mirror of the Sigstore TUF
// repository) is verified against the embedded Sigstore root, which stays the
// trust anchor.
func (v *bundleVerifier) tufOptions() *tuf.Options {
	f := fetcher.NewDefaultFetcher()
	f.SetHTTPUserAgent(version.UserAgent(brand.ServerBinary))
	if v.client != nil {
		f.SetHTTPClient(v.client)
	}
	opts := tuf.DefaultOptions().WithCachePath(v.tufCache).WithFetcher(f)
	if v.cfg.TUFRepositoryURL != "" {
		opts = opts.WithRepositoryBaseURL(strings.TrimRight(v.cfg.TUFRepositoryURL, "/")).WithRoot(tuf.DefaultRoot())
	}
	return opts
}

func (v *bundleVerifier) trustedMaterial() (root.TrustedMaterial, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.trusted != nil && time.Since(v.loadedAt) < trustedRootTTL {
		return v.trusted, nil
	}
	tm, err := v.fetchRoot()
	if err != nil {
		if v.trusted != nil {
			return v.trusted, nil // keep working with the last known root
		}
		return nil, err
	}
	v.trusted, v.loadedAt = tm, time.Now()
	return tm, nil
}

// Result of a successful verification.
type verified struct {
	// Identity is the certificate SAN (workflow + ref).
	Identity string
}

// verifyBundle checks that bundleJSON is a valid sigstore bundle over the
// artifact with the given SHA-256 digest, signed by the pinned workflow
// identity for tag v<version> (docs/design/opamp-server.md, Artifact cache).
func (v *bundleVerifier) verifyBundle(bundleJSON, digest []byte, dist, version string) (*verified, error) {
	if len(digest) != sha256.Size {
		return nil, fmt.Errorf("artifact digest must be %d bytes", sha256.Size)
	}
	tm, err := v.trustedMaterial()
	if err != nil {
		return nil, err
	}
	b := &bundle.Bundle{Bundle: new(protobundle.Bundle)}
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("invalid sigstore bundle: %w", err)
	}
	sv, err := verify.NewVerifier(tm,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1))
	if err != nil {
		return nil, fmt.Errorf("create sigstore verifier: %w", err)
	}
	id, err := verify.NewShortCertificateIdentity(v.cfg.CertificateOIDCIssuer, "", "", v.cfg.CertificateIdentityRegexp)
	if err != nil {
		return nil, fmt.Errorf("certificate identity: %w", err)
	}
	res, err := sv.Verify(b, verify.NewPolicy(verify.WithArtifactDigest("sha256", digest), verify.WithCertificateIdentity(id)))
	if err != nil {
		return nil, fmt.Errorf("sigstore verification failed: %w", err)
	}
	if res.Signature == nil || res.Signature.Certificate == nil {
		return nil, errors.New("sigstore verification returned no certificate identity")
	}
	san := res.Signature.Certificate.SubjectAlternativeName
	// The tag the workflow ran on must be the artifact version: this stops a
	// valid signature of another release from being replayed.
	if want := "@refs/tags/v" + version; !strings.HasSuffix(san, want) {
		return nil, fmt.Errorf("signature identity %q is not bound to tag v%s", san, version)
	}
	if wf, ok := expectedWorkflow[dist]; ok && strings.Contains(san, "/.github/workflows/base-") && !strings.Contains(san, "/"+wf+"@") {
		return nil, fmt.Errorf("signature identity %q was not issued by workflow %s required for %s", san, wf, dist)
	}
	return &verified{Identity: san}, nil
}

func hexDigest(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != sha256.Size {
		return nil, fmt.Errorf("not a SHA-256 hex digest: %q", truncate(s, 80))
	}
	return b, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
