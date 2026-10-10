// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package clientassert builds the client assertions of the OAuth 2.0 client
// authentication method private_key_jwt (RFC 7523, OpenID Connect Core 9):
// instead of a client secret, myriarchd proves its identity to the token endpoint
// of the identity provider with a short-lived JWT that is signed with the
// private key of a certificate registered at the identity provider (Microsoft
// Entra ID: "Certificates & secrets" of the app registration).
//
// The JWT carries the header fields Entra ID needs to find the certificate
// (x5t and x5t#S256), the claims aud (token endpoint), iss and sub (client
// id), jti, iat, nbf and exp (5 minutes). The key is RSA (PKCS#1 or PKCS#8),
// the algorithm RS256. The files are read again when they change, so a
// certificate can be rotated without restarting myriarchd.
package clientassert

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // x5t is the SHA-1 thumbprint that the identity provider looks the certificate up by (RFC 7515 4.1.7)
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Type is the value of the client_assertion_type parameter.
const Type = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// Lifetime is the validity of one assertion.
const Lifetime = 5 * time.Minute

// Signer signs client assertions with the key of a certificate.
type Signer struct {
	certFile, keyFile string

	mu     sync.Mutex
	loaded *material
	stamp  [2]fileStamp
}

type fileStamp struct {
	mod  time.Time
	size int64
}

// material is the parsed content of the two files.
type material struct {
	key     *rsa.PrivateKey
	cert    *x509.Certificate
	x5t     string
	x5tS256 string
}

// New loads the certificate and the key and checks that they belong together.
func New(certFile, keyFile string) (*Signer, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("clientCertificateFile and clientKeyFile must be set together")
	}
	s := &Signer{certFile: certFile, keyFile: keyFile}
	if _, err := s.material(); err != nil {
		return nil, err
	}
	return s, nil
}

// Check loads and verifies the files without keeping them (configuration
// validation).
func Check(certFile, keyFile string) error {
	_, err := New(certFile, keyFile)
	return err
}

// Thumbprint returns the SHA-1 thumbprint (hex) of the certificate, the value
// Entra ID shows for a certificate.
func (s *Signer) Thumbprint() (string, error) {
	m, err := s.material()
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(m.cert.Raw) //nolint:gosec // identifier, not a security function
	return fmt.Sprintf("%X", sum), nil
}

// NotAfter returns the end of the validity of the certificate.
func (s *Signer) NotAfter() (time.Time, error) {
	m, err := s.material()
	if err != nil {
		return time.Time{}, err
	}
	return m.cert.NotAfter, nil
}

// Assertion returns a signed client assertion for the token endpoint audience.
func (s *Signer) Assertion(clientID, audience string, now time.Time) (string, error) {
	if clientID == "" || audience == "" {
		return "", errors.New("client assertion: client id and audience are required")
	}
	m, err := s.material()
	if err != nil {
		return "", err
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "x5t": m.x5t, "x5t#S256": m.x5tS256})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"aud": audience, "iss": clientID, "sub": clientID,
		"jti": base64.RawURLEncoding.EncodeToString(jti),
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(Lifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign client assertion: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// material returns the parsed files, reading them again if they changed.
func (s *Signer) material() (*material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stamp [2]fileStamp
	for i, f := range []string{s.certFile, s.keyFile} {
		st, err := os.Stat(f)
		if err != nil {
			if s.loaded != nil {
				return s.loaded, nil // keep working with the last good material
			}
			return nil, fmt.Errorf("client certificate: %w", pathless(err))
		}
		stamp[i] = fileStamp{st.ModTime(), st.Size()}
	}
	if s.loaded != nil && stamp == s.stamp {
		return s.loaded, nil
	}
	m, err := load(s.certFile, s.keyFile)
	if err != nil {
		if s.loaded != nil {
			return s.loaded, nil // a half-written rotation must not lock everybody out
		}
		return nil, err
	}
	s.loaded, s.stamp = m, stamp
	return m, nil
}

func pathless(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func load(certFile, keyFile string) (*material, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read client certificate file: %w", pathless(err))
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read client key file: %w", pathless(err))
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("client key file: %w", err)
	}
	var cert *x509.Certificate
	found := false
	for rest := certPEM; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		found = true
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			continue
		}
		// the leaf: the certificate of the key (a chain may be given in any order)
		if pub, ok := c.PublicKey.(*rsa.PublicKey); ok && pub.Equal(&key.PublicKey) {
			cert = c
			break
		}
	}
	switch {
	case cert != nil:
	case found:
		return nil, errors.New("client certificate file: no certificate matches the private key of clientKeyFile")
	default:
		return nil, errors.New("client certificate file contains no PEM certificate")
	}
	s1 := sha1.Sum(cert.Raw) //nolint:gosec // thumbprint, see above
	s256 := sha256.Sum256(cert.Raw)
	return &material{
		key: key, cert: cert,
		x5t:     base64.RawURLEncoding.EncodeToString(s1[:]),
		x5tS256: base64.RawURLEncoding.EncodeToString(s256[:]),
	}, nil
}

// parseKey reads an RSA private key (PKCS#1 or PKCS#8) from PEM.
func parseKey(data []byte) (*rsa.PrivateKey, error) {
	for rest := data; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		switch b.Type {
		case "RSA PRIVATE KEY":
			k, err := x509.ParsePKCS1PrivateKey(b.Bytes)
			if err != nil {
				return nil, errors.New("cannot parse the PKCS#1 key (encrypted keys are not supported)")
			}
			return k, nil
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
			if err != nil {
				return nil, errors.New("cannot parse the PKCS#8 key")
			}
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("the key is not an RSA key (private_key_jwt with RS256 needs RSA)")
			}
			return rk, nil
		case "ENCRYPTED PRIVATE KEY":
			return nil, errors.New("encrypted keys are not supported: remove the passphrase")
		case "EC PRIVATE KEY":
			return nil, errors.New("the key is not an RSA key (private_key_jwt with RS256 needs RSA)")
		}
	}
	return nil, errors.New("contains no PEM private key (RSA, PKCS#1 or PKCS#8)")
}
