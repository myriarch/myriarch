// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

const sigDir = "testdata/sigstore"

func readSig(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sigDir, name))
	require.NoError(t, err)
	return b
}

func testVerifier(t *testing.T) *bundleVerifier {
	t.Helper()
	cfg := config.Default().Artifacts.Verify
	cfg.TrustedRootFile = filepath.Join(sigDir, "trusted_root.json")
	v, err := newBundleVerifier(cfg, t.TempDir(), nil)
	require.NoError(t, err)
	return v
}

func digestOf(t *testing.T, name string) []byte {
	t.Helper()
	d, err := hexDigest(string(readSig(t, name)))
	require.NoError(t, err)
	return d
}

const (
	contribBundle = "otelcol-contrib_0.162.0_linux_amd64.tar.gz.sigstore.json"
	contribSHA    = "otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256"
	superBundle   = "opampsupervisor_0.162.0_linux_amd64.sigstore.json"
	superSHA      = "opampsupervisor_0.162.0_linux_amd64.sha256"
)

// Real bundles of the 0.162.0 release are verified offline against the real
// trusted root (no network involved).
func TestVerifyRealContribBundleOffline(t *testing.T) {
	v := testVerifier(t)
	res, err := v.verifyBundle(readSig(t, contribBundle), digestOf(t, contribSHA), domain.DistributionContrib, "0.162.0")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v0.162.0", res.Identity)
}

func TestVerifyRealSupervisorBundleOffline(t *testing.T) {
	v := testVerifier(t)
	res, err := v.verifyBundle(readSig(t, superBundle), digestOf(t, superSHA), domain.DistributionSupervisor, "0.162.0")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-binary-release.yaml@refs/tags/v0.162.0", res.Identity,
		"the supervisor SAN uses refs/tags/v0.162.0, not cmd/opampsupervisor/v0.162.0")
}

func TestVerifyChecksumFileBundle(t *testing.T) {
	// The .sha256 file itself is signed too (digest of the small file).
	v := testVerifier(t)
	d := sha256Sum(readSig(t, contribSHA))
	_, err := v.verifyBundle(readSig(t, contribBundle+"")[:0:0], d[:], "x", "0.162.0")
	assert.Error(t, err, "empty bundle")
	b := readSig(t, "otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256.sigstore.json")
	_, err = v.verifyBundle(b, d[:], domain.DistributionContrib, "0.162.0")
	require.NoError(t, err)
}

func TestVerifyRejects(t *testing.T) {
	v := testVerifier(t)
	good := readSig(t, contribBundle)
	digest := digestOf(t, contribSHA)

	t.Run("wrong digest", func(t *testing.T) {
		bad := append([]byte(nil), digest...)
		bad[0] ^= 0xff
		_, err := v.verifyBundle(good, bad, domain.DistributionContrib, "0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "digest")
	})
	t.Run("digest of the supervisor binary", func(t *testing.T) {
		_, err := v.verifyBundle(good, digestOf(t, superSHA), domain.DistributionContrib, "0.162.0")
		assert.Error(t, err)
	})
	t.Run("version other than the tag of the signing workflow", func(t *testing.T) {
		_, err := v.verifyBundle(good, digest, domain.DistributionContrib, "0.161.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not bound to tag v0.161.0")
	})
	t.Run("supervisor tag form is not accepted", func(t *testing.T) {
		_, err := v.verifyBundle(readSig(t, superBundle), digestOf(t, superSHA), domain.DistributionSupervisor, "cmd/opampsupervisor/v0.162.0")
		assert.Error(t, err)
	})
	t.Run("supervisor bundle for the contrib distribution (wrong workflow)", func(t *testing.T) {
		_, err := v.verifyBundle(readSig(t, superBundle), digestOf(t, superSHA), domain.DistributionContrib, "0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base-release.yaml required for otelcol-contrib")
	})
	t.Run("contrib bundle for the supervisor distribution (wrong workflow)", func(t *testing.T) {
		_, err := v.verifyBundle(good, digest, domain.DistributionSupervisor, "0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base-binary-release.yaml required for opampsupervisor")
	})
	t.Run("identity regexp of another repository", func(t *testing.T) {
		cfg := config.Default().Artifacts.Verify
		cfg.TrustedRootFile = filepath.Join(sigDir, "trusted_root.json")
		cfg.CertificateIdentityRegexp = `^https://github\.com/evil/repo/\.github/workflows/.*$`
		v2, err := newBundleVerifier(cfg, t.TempDir(), nil)
		require.NoError(t, err)
		_, err = v2.verifyBundle(good, digest, domain.DistributionContrib, "0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sigstore verification failed")
	})
	t.Run("other OIDC issuer", func(t *testing.T) {
		cfg := config.Default().Artifacts.Verify
		cfg.TrustedRootFile = filepath.Join(sigDir, "trusted_root.json")
		cfg.CertificateOIDCIssuer = "https://accounts.example.com"
		v2, err := newBundleVerifier(cfg, t.TempDir(), nil)
		require.NoError(t, err)
		_, err = v2.verifyBundle(good, digest, domain.DistributionContrib, "0.162.0")
		assert.Error(t, err)
	})
	t.Run("tampered bundle", func(t *testing.T) {
		bad := []byte(strings.Replace(string(good), `"signature": "`, `"signature": "AAAA`, 1))
		if string(bad) == string(good) { // compact JSON spelling
			bad = []byte(strings.Replace(string(good), `"signature":"`, `"signature":"AAAA`, 1))
		}
		require.NotEqual(t, string(good), string(bad), "test must actually modify the bundle")
		_, err := v.verifyBundle(bad, digest, domain.DistributionContrib, "0.162.0")
		assert.Error(t, err)
	})
	t.Run("garbage bundle", func(t *testing.T) {
		_, err := v.verifyBundle([]byte("{not json"), digest, domain.DistributionContrib, "0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid sigstore bundle")
	})
	t.Run("bad digest length", func(t *testing.T) {
		_, err := v.verifyBundle(good, digest[:10], domain.DistributionContrib, "0.162.0")
		assert.Error(t, err)
	})
}

func TestTrustedRootProblemsAreReported(t *testing.T) {
	cfg := config.Default().Artifacts.Verify
	cfg.TrustedRootFile = filepath.Join(t.TempDir(), "missing.json")
	_, err := newBundleVerifier(cfg, t.TempDir(), nil)
	assert.Error(t, err, "an unusable trusted root is a startup error")

	cfg = config.Default().Artifacts.Verify
	cfg.CertificateIdentityRegexp = "(["
	_, err = newBundleVerifier(cfg, t.TempDir(), nil)
	assert.Error(t, err)
}

func TestHexDigest(t *testing.T) {
	d, err := hexDigest("FCC063749F730F8C21FE29F2D340FF174F5F1C5885BD3156FB6C985A3036FCC3\n")
	require.NoError(t, err)
	assert.Len(t, d, 32)
	for _, bad := range []string{"", "abc", strings.Repeat("g", 64), strings.Repeat("a", 63), strings.Repeat("a", 66)} {
		_, err := hexDigest(bad)
		assert.Error(t, err, bad)
	}
}

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

// An empty trusted root (e.g. `cosign trusted-root create` without flags) loads
// without error in sigstore-go but can verify nothing: it must be refused at
// startup instead of failing every download later.
func TestLoadTrustedRootFileRefusesEmptyRoot(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(empty, []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1"}`), 0o600))
	_, err := LoadTrustedRootFile(empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no trust material")
	assert.Contains(t, err.Error(), "cosign initialize")

	cfg := config.Default().Artifacts.Verify
	cfg.TrustedRootFile = empty
	_, err = newBundleVerifier(cfg, t.TempDir(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifacts.verify.trustedRootFile")

	tr, err := LoadTrustedRootFile(filepath.Join(sigDir, "trusted_root.json"))
	require.NoError(t, err)
	assert.NotEmpty(t, tr.FulcioCertificateAuthorities())
}

// ------------------------------------------------------------------ the exported verifier ----

const upstreamContribSAN = "https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v0.162.0"

func upstreamIdentity() Identity {
	return Identity{
		SANRegexp: `^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v0\.162\.0$`,
		Issuer:    GitHubActionsIssuer,
		Tag:       "v0.162.0",
	}
}

// The verifier that the installer bundles use accepts exactly the identity that the caller names.
func TestExportedVerifierChecksTheIdentityOfTheCaller(t *testing.T) {
	v := &Verifier{v: testVerifier(t)}
	good, digest := readSig(t, contribBundle), digestOf(t, contribSHA)

	san, err := v.Verify(good, digest, upstreamIdentity())
	require.NoError(t, err)
	assert.Equal(t, upstreamContribSAN, san)

	t.Run("without a tag binding any release of the workflow does", func(t *testing.T) {
		id := upstreamIdentity()
		id.Tag = ""
		_, err := v.Verify(good, digest, id)
		require.NoError(t, err)
	})
	t.Run("another tag", func(t *testing.T) {
		id := upstreamIdentity()
		id.Tag = "v0.161.0"
		_, err := v.Verify(good, digest, id)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not bound to tag v0.161.0")
	})
	t.Run("a subject of another repository", func(t *testing.T) {
		id := upstreamIdentity()
		id.SANRegexp = `^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.162\.0$`
		_, err := v.Verify(good, digest, id)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sigstore verification failed")
	})
	t.Run("a subject of another workflow of the same repository", func(t *testing.T) {
		id := upstreamIdentity()
		id.SANRegexp = `^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/release\.yml@refs/tags/v0\.162\.0$`
		_, err := v.Verify(good, digest, id)
		require.Error(t, err)
	})
	t.Run("another issuer", func(t *testing.T) {
		id := upstreamIdentity()
		id.Issuer = "https://accounts.example.com"
		_, err := v.Verify(good, digest, id)
		require.Error(t, err)
	})
	t.Run("a digest of another file", func(t *testing.T) {
		_, err := v.Verify(good, digestOf(t, superSHA), upstreamIdentity())
		require.Error(t, err)
	})
	t.Run("a pattern that matches only a part of the subject", func(t *testing.T) {
		// the caller forgot the anchors: sigstore-go would accept it, the verifier does not
		for _, re := range []string{
			`opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v0\.162\.0`,
			`^https://github\.com/open-telemetry/opentelemetry-collector-releases/`,
			`/\.github/workflows/base-release\.yaml@`,
		} {
			id := upstreamIdentity()
			id.SANRegexp = re
			_, err := v.Verify(good, digest, id)
			require.Error(t, err, re)
			assert.Contains(t, err.Error(), "as a whole", re) // sigstore-go accepted it, the check of the whole name did not
		}
		for _, re := range []string{
			// a pattern that spans the whole subject is fine, anchored or not
			`https://github\.com/open-telemetry/opentelemetry-collector-releases/.*`,
			`^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v0\.162\.0$|^never$`,
		} {
			id := upstreamIdentity()
			id.SANRegexp = re
			san, err := v.Verify(good, digest, id)
			require.NoError(t, err, re)
			assert.Equal(t, upstreamContribSAN, san)
		}
	})
	t.Run("incomplete identities", func(t *testing.T) {
		for _, id := range []Identity{{}, {SANRegexp: `^x$`}, {Issuer: GitHubActionsIssuer}} {
			_, err := v.Verify(good, digest, id)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "needs a subject pattern and an issuer")
		}
	})
	t.Run("a pattern that does not compile", func(t *testing.T) {
		id := upstreamIdentity()
		id.SANRegexp = "(["
		_, err := v.Verify(good, digest, id)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "identity pattern")
	})
	t.Run("a bundle that is not one", func(t *testing.T) {
		_, err := v.Verify([]byte("{not json"), digest, upstreamIdentity())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid sigstore bundle")
	})
}

// The cache hands out a verifier also where signatures of the artifacts are not required, and one with the trusted
// root of artifacts.verify.
func TestCacheVerifier(t *testing.T) {
	for _, required := range []bool{true, false} {
		cfg := config.Default().Artifacts
		cfg.Dir = t.TempDir()
		cfg.Verify.RequireSignature = required
		cfg.Verify.TrustedRootFile = filepath.Join(sigDir, "trusted_root.json")
		c, err := NewCache(cfg, Deps{Store: nil})
		require.NoError(t, err)
		assert.Equal(t, cfg.Dir, c.Dir())
		v, err := c.Verifier()
		require.NoError(t, err, "requireSignature=%v", required)
		again, _ := c.Verifier()
		assert.Same(t, v, again, "one verifier, built once")
		_, err = v.Verify(readSig(t, contribBundle), digestOf(t, contribSHA), upstreamIdentity())
		require.NoError(t, err, "requireSignature=%v", required)
	}

	// an unusable trusted root is reported to the caller that needs the verifier, not at the start of a cache that
	// does not require signatures
	cfg := config.Default().Artifacts
	cfg.Dir = t.TempDir()
	cfg.Verify.RequireSignature = false
	cfg.Verify.TrustedRootFile = filepath.Join(t.TempDir(), "missing.json")
	c, err := NewCache(cfg, Deps{Store: nil})
	require.NoError(t, err)
	_, err = c.Verifier()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifacts.verify.trustedRootFile")
}
