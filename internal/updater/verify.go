// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// hashReader returns the SHA-256 of everything r yields.
func hashReader(r io.Reader) ([]byte, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// SignatureVerifier verifies the signature that came with a package offer.
// It is called with the SHA-256 of the already downloaded artifact; it must
// not trust anything else about the artifact.
type SignatureVerifier interface {
	// Verify returns nil if signature authenticates the artifact with the
	// given SHA-256 digest as the release artifact for version ("v0.163.0" or
	// "0.163.0").
	Verify(signature []byte, sha256Digest []byte, version string) error
}

// ErrLegacySignature is returned for the upstream draft signature format
// "<b64 cert> <b64 sig>", which cannot be verified offline.
var ErrLegacySignature = errors.New("legacy signature format not supported (needs online Rekor)")

// SigstoreVerifier verifies Sigstore bundles (cosign keyless) with sigstore-go.
type SigstoreVerifier struct {
	// TrustedRootFile is a sigstore trusted_root.json (offline verification).
	// Empty = fetch the trusted root via TUF from the public Sigstore
	// instance (needs network and a writable cache).
	TrustedRootFile string
	// CertificateIdentityRegexp matches the Fulcio certificate SAN
	// (anchored, e.g. DefaultCertificateIdentityRegexp).
	CertificateIdentityRegexp string
	// CertificateOIDCIssuer is the exact OIDC issuer.
	CertificateOIDCIssuer string
	// TUFCacheDir is the TUF cache directory for online mode (a service
	// account often has no usable $HOME).
	TUFCacheDir string
	// RequireSignature rejects empty signatures. If false an empty
	// signature is accepted (a non-empty one is still fully verified).
	RequireSignature bool

	mu       sync.Mutex
	verifier *verify.Verifier
}

// Verify implements SignatureVerifier.
func (v *SigstoreVerifier) Verify(signature []byte, digest []byte, version string) error {
	sig := bytes.TrimSpace(signature)
	if len(sig) == 0 {
		if v.RequireSignature {
			return errors.New("signature required but the package offer carries none")
		}
		return nil
	}
	if sig[0] != '{' {
		return ErrLegacySignature
	}
	if len(digest) != sha256.Size {
		return fmt.Errorf("artifact digest must be %d bytes, got %d", sha256.Size, len(digest))
	}
	ver := strings.TrimPrefix(version, "v")
	if !semverRE.MatchString(ver) {
		return fmt.Errorf("invalid version %q", version)
	}

	verifier, err := v.getVerifier()
	if err != nil {
		return err
	}
	b := &bundle.Bundle{}
	if err := b.UnmarshalJSON(sig); err != nil {
		return fmt.Errorf("parse sigstore bundle: %w", err)
	}
	san, err := verify.NewSANMatcher("", v.CertificateIdentityRegexp)
	if err != nil {
		return fmt.Errorf("certificate identity: %w", err)
	}
	iss, err := verify.NewIssuerMatcher(v.CertificateOIDCIssuer, "")
	if err != nil {
		return fmt.Errorf("certificate issuer: %w", err)
	}
	id, err := verify.NewCertificateIdentity(san, iss, certificate.Extensions{})
	if err != nil {
		return fmt.Errorf("certificate identity: %w", err)
	}
	res, err := verifier.Verify(b, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digest),
		verify.WithCertificateIdentity(id),
	))
	if err != nil {
		return fmt.Errorf("sigstore verification failed: %w", err)
	}
	// The identity regexp accepts any release tag; bind the signature to the
	// version we are about to install.
	var gotSAN string
	if res.Signature != nil && res.Signature.Certificate != nil {
		gotSAN = res.Signature.Certificate.SubjectAlternativeName
	}
	if want := "@refs/tags/v" + ver; !strings.HasSuffix(gotSAN, want) {
		return fmt.Errorf("signature identity %q is not bound to release tag v%s", gotSAN, ver)
	}
	return nil
}

func (v *SigstoreVerifier) getVerifier() (*verify.Verifier, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verifier != nil {
		return v.verifier, nil
	}
	var tm root.TrustedMaterial
	var err error
	if v.TrustedRootFile != "" {
		tm, err = root.NewTrustedRootFromPath(v.TrustedRootFile)
		if err != nil {
			return nil, fmt.Errorf("load trusted root %s: %w", v.TrustedRootFile, err)
		}
	} else {
		// Not cached on failure: the network may come back.
		opts := tuf.DefaultOptions()
		if v.TUFCacheDir != "" {
			opts = opts.WithCachePath(v.TUFCacheDir)
		}
		tm, err = root.FetchTrustedRootWithOptions(opts)
		if err != nil {
			return nil, fmt.Errorf("fetch sigstore trusted root via TUF: %w", err)
		}
	}
	ver, err := verify.NewVerifier(tm,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return nil, fmt.Errorf("create sigstore verifier: %w", err)
	}
	v.verifier = ver
	return ver, nil
}

// digestEqual compares two digests in constant time.
func digestEqual(a, b []byte) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1
}
