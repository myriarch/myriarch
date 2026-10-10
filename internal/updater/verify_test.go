// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real upstream v0.162.0 bundles (testdata/sigstore, see
// docs/design/upstream-notes.md, Verifying Sigstore bundles in Go).
const sigDir = "testdata/sigstore"

type sigFixture struct {
	digest      []byte
	bundle      []byte
	supBundle   []byte
	supDigest   []byte
	trustedRoot string
}

func loadSigFixture(t *testing.T) sigFixture {
	t.Helper()
	rd := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join(sigDir, n))
		require.NoError(t, err)
		return b
	}
	dec := func(b []byte) []byte {
		d, err := hex.DecodeString(string(trimSpaceBytes(b)))
		require.NoError(t, err)
		return d
	}
	return sigFixture{
		digest:      dec(rd("otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256")),
		bundle:      rd("otelcol-contrib_0.162.0_linux_amd64.tar.gz.sigstore.json"),
		supBundle:   rd("opampsupervisor_0.162.0_linux_amd64.sigstore.json"),
		supDigest:   dec(rd("opampsupervisor_0.162.0_linux_amd64.sha256")),
		trustedRoot: filepath.Join(sigDir, "trusted_root.json"),
	}
}

func contribVerifier(trustedRoot string) *SigstoreVerifier {
	return &SigstoreVerifier{
		TrustedRootFile:           trustedRoot,
		CertificateIdentityRegexp: DefaultCertificateIdentityRegexp,
		CertificateOIDCIssuer:     DefaultCertificateOIDCIssuer,
		RequireSignature:          true,
	}
}

func TestSigstoreVerifyOfflineSuccess(t *testing.T) {
	f := loadSigFixture(t)
	v := contribVerifier(f.trustedRoot)
	require.NoError(t, v.Verify(f.bundle, f.digest, "v0.162.0"))
	require.NoError(t, v.Verify(f.bundle, f.digest, "0.162.0"), "version without v")
	// the .sha256 file itself is signed too (digest of the checksum file) - different artifact, same flow
	sum := loadSigFixtureBundle(t, "otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256.sigstore.json")
	require.NoError(t, v.Verify(sum, sha([]byte(hex.EncodeToString(f.digest))), "v0.162.0"))
}

func loadSigFixtureBundle(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join(sigDir, name))
	require.NoError(t, err)
	return b
}

func TestSigstoreVerifyNegativeCases(t *testing.T) {
	f := loadSigFixture(t)
	v := contribVerifier(f.trustedRoot)

	t.Run("wrong version (tag mismatch)", func(t *testing.T) {
		err := v.Verify(f.bundle, f.digest, "v0.161.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not bound to release tag v0.161.0")
	})
	t.Run("wrong digest", func(t *testing.T) {
		bad := append([]byte(nil), f.digest...)
		bad[0] ^= 0xff
		err := v.Verify(f.bundle, bad, "v0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sigstore verification failed")
	})
	t.Run("wrong identity regexp", func(t *testing.T) {
		w := contribVerifier(f.trustedRoot)
		w.CertificateIdentityRegexp = `^https://github\.com/evil/repo/\.github/workflows/x\.yaml@refs/tags/v[0-9.]+$`
		err := w.Verify(f.bundle, f.digest, "v0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sigstore verification failed")
	})
	t.Run("supervisor bundle does not pass the contrib identity", func(t *testing.T) {
		err := v.Verify(f.supBundle, f.supDigest, "v0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sigstore verification failed")
	})
	t.Run("supervisor bundle passes with the binary-release identity", func(t *testing.T) {
		w := contribVerifier(f.trustedRoot)
		w.CertificateIdentityRegexp = `^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-binary-release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`
		require.NoError(t, w.Verify(f.supBundle, f.supDigest, "v0.162.0"))
	})
	t.Run("wrong issuer", func(t *testing.T) {
		w := contribVerifier(f.trustedRoot)
		w.CertificateOIDCIssuer = "https://accounts.google.com"
		require.Error(t, w.Verify(f.bundle, f.digest, "v0.162.0"))
	})
	t.Run("garbage bundle", func(t *testing.T) {
		err := v.Verify([]byte(`{"mediaType": "nope"}`), f.digest, "v0.162.0")
		require.Error(t, err)
	})
	t.Run("tampered bundle signature", func(t *testing.T) {
		tampered := append([]byte(nil), f.bundle...)
		i := indexOf(tampered, `"signature":"`)
		require.Positive(t, i)
		j := i + len(`"signature":"`) + 5
		if tampered[j] == 'A' {
			tampered[j] = 'B'
		} else {
			tampered[j] = 'A'
		}
		require.Error(t, v.Verify(tampered, f.digest, "v0.162.0"))
	})
	t.Run("invalid version string", func(t *testing.T) {
		require.Error(t, v.Verify(f.bundle, f.digest, "../v0.162.0"))
	})
	t.Run("short digest", func(t *testing.T) {
		require.Error(t, v.Verify(f.bundle, f.digest[:10], "v0.162.0"))
	})
	t.Run("missing trusted root", func(t *testing.T) {
		w := contribVerifier("/does/not/exist.json")
		err := w.Verify(f.bundle, f.digest, "v0.162.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "trusted root")
	})
}

func TestSigstoreEmptyAndLegacySignatures(t *testing.T) {
	f := loadSigFixture(t)
	v := contribVerifier(f.trustedRoot)
	err := v.Verify(nil, f.digest, "v0.162.0")
	require.Error(t, err, "requireSignature with empty signature")
	assert.Contains(t, err.Error(), "signature required")

	v.RequireSignature = false
	require.NoError(t, v.Verify(nil, f.digest, "v0.162.0"))
	require.NoError(t, v.Verify([]byte("  \n"), f.digest, "v0.162.0"))
	// a present signature is verified even if not required
	require.Error(t, v.Verify(f.bundle, f.digest, "v0.100.0"))

	// legacy upstream draft format "<b64 cert> <b64 sig>"
	err = v.Verify([]byte("LS0tLS1CRUdJTg== MEUCIQ"), f.digest, "v0.162.0")
	require.ErrorIs(t, err, ErrLegacySignature)
	assert.Contains(t, err.Error(), "needs online Rekor")
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}
