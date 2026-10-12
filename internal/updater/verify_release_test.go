// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
)

// The keyless bundle of checksums.txt of the published release v0.1.0-rc.3, byte-identical to the release assets
// checksums.txt and checksums.txt.sigstore.json (testdata/sigstore/myriarch_0.1.0-rc.3_*). The digests are the SHA-256
// values that GitHub reports for the two assets of the immutable release.
const (
	rcVersion          = "0.1.0-rc.3"
	rcSums             = "myriarch_0.1.0-rc.3_checksums.txt"
	rcSumsSHA256       = "12d7c8ed1c017712c216dd8f5b6711a2396d70aca87261956323f825864f0ee8"
	rcSumsBundleSHA256 = "9370ec37e83f8d18bd0cf3de0eafea0b6418c90355c28cf81167ad37d3e390d9"
)

// releaseIdentity is the identity that the installers pass for a version (install-linux.sh cert_identity_release,
// install-windows.ps1 Get-ReleaseCertIdentity): the release workflow of the repository at the tag v<version>, anchored.
func releaseIdentity(version string) string {
	return "^" + regexp.QuoteMeta(brand.Repository) + `/\.github/workflows/release\.yml@refs/tags/v` + regexp.QuoteMeta(version) + "$"
}

// releaseIdentityAnyTag accepts every release tag, so that only the version binding of Verify decides about the version.
var releaseIdentityAnyTag = "^" + regexp.QuoteMeta(brand.Repository) + `/\.github/workflows/release\.yml@refs/tags/v[0-9A-Za-z.+-]+$`

func loadRCFixture(t *testing.T) (sums, bundle []byte) {
	t.Helper()
	sums, err := os.ReadFile(filepath.Join(sigDir, rcSums))
	require.NoError(t, err)
	bundle, err = os.ReadFile(filepath.Join(sigDir, rcSums+".sigstore.json"))
	require.NoError(t, err)
	// byte for byte the release assets (a checkout that converts line endings changes checksums.txt)
	require.Equal(t, rcSumsSHA256, hex.EncodeToString(sha(sums)), "fixture %s differs from the release asset", rcSums)
	require.Equal(t, rcSumsBundleSHA256, hex.EncodeToString(sha(bundle)), "fixture %s.sigstore.json differs from the release asset", rcSums)
	return sums, bundle
}

func releaseVerifier(identity string) *SigstoreVerifier {
	return &SigstoreVerifier{
		TrustedRootFile:           filepath.Join(sigDir, "trusted_root.json"),
		CertificateIdentityRegexp: identity,
		CertificateOIDCIssuer:     DefaultCertificateOIDCIssuer,
		RequireSignature:          true,
	}
}

func TestReleaseBundleVerifiesForItsVersion(t *testing.T) {
	sums, bundle := loadRCFixture(t)
	digest := sha(sums)
	for _, id := range []string{releaseIdentity(rcVersion), releaseIdentityAnyTag} {
		v := releaseVerifier(id)
		require.NoError(t, v.Verify(bundle, digest, "v"+rcVersion), id)
		require.NoError(t, v.Verify(bundle, digest, rcVersion), id)
	}
}

func TestReleaseBundleIsBoundToItsVersion(t *testing.T) {
	sums, bundle := loadRCFixture(t)
	digest := sha(sums)
	for _, other := range []string{"0.1.0-rc.2", "0.1.0", "0.1.0-rc.30", "10.1.0-rc.3"} {
		t.Run(other, func(t *testing.T) {
			// the identity of the other version (what the installers pass) does not match the certificate
			err := releaseVerifier(releaseIdentity(other)).Verify(bundle, digest, "v"+other)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "sigstore verification failed")
			// a pattern that accepts every tag: the binding of the signature to the requested version refuses it
			err = releaseVerifier(releaseIdentityAnyTag).Verify(bundle, digest, "v"+other)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not bound to release tag v"+other)
		})
	}
}

func TestReleaseBundleRefusesATamperedChecksumsFile(t *testing.T) {
	sums, bundle := loadRCFixture(t)
	// the line of LICENSE points to another file: the signature over checksums.txt no longer matches
	tampered := []byte(string(sums))
	i := indexOf(tampered, "  LICENSE\n")
	require.Positive(t, i)
	other := sha256.Sum256([]byte("another file\n"))
	copy(tampered[i-64:i], hex.EncodeToString(other[:]))
	require.NotEqual(t, sums, tampered)
	for _, id := range []string{releaseIdentity(rcVersion), releaseIdentityAnyTag} {
		err := releaseVerifier(id).Verify(bundle, sha(tampered), "v"+rcVersion)
		require.Error(t, err, id)
		assert.Contains(t, err.Error(), "sigstore verification failed")
	}
}

func TestReleaseBundleRefusedForTheUpstreamIdentity(t *testing.T) {
	sums, bundle := loadRCFixture(t)
	err := releaseVerifier(DefaultCertificateIdentityRegexp).Verify(bundle, sha(sums), "v"+rcVersion)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sigstore verification failed")
}

// verify-bundle as the installers call it with an installed myriarch-updater as the verifier.
func TestCLIVerifyBundleReleaseFixture(t *testing.T) {
	sums, _ := loadRCFixture(t)
	args := func(version, identity, artifact string) []string {
		return []string{"verify-bundle",
			"--bundle", filepath.Join(sigDir, rcSums+".sigstore.json"),
			"--artifact", artifact,
			"--version", "v" + version,
			"--identity-regexp", identity,
			"--issuer", DefaultCertificateOIDCIssuer,
			"--trusted-root", filepath.Join(sigDir, "trusted_root.json")}
	}
	good := filepath.Join(sigDir, rcSums)
	out, err := runCLI(t, args(rcVersion, releaseIdentity(rcVersion), good)...)
	require.NoError(t, err)
	assert.Contains(t, out, "OK: bundle authenticates sha256:"+rcSumsSHA256+" as a release artifact of v"+rcVersion)

	for _, other := range []string{"0.1.0-rc.2", "0.1.0"} {
		_, err = runCLI(t, args(other, releaseIdentity(other), good)...)
		require.Error(t, err, other)
		assert.Contains(t, err.Error(), "sigstore verification failed", other)
		_, err = runCLI(t, args(other, releaseIdentityAnyTag, good)...)
		require.Error(t, err, other)
		assert.Contains(t, err.Error(), "not bound to release tag v"+other, other)
	}

	tampered := filepath.Join(t.TempDir(), "checksums.txt")
	require.NoError(t, os.WriteFile(tampered, append(append([]byte(nil), sums...), []byte("0000000000000000000000000000000000000000000000000000000000000000  extra\n")...), 0o644))
	_, err = runCLI(t, args(rcVersion, releaseIdentity(rcVersion), tampered)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sigstore verification failed")
}
