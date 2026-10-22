// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pgtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Environment variables that select the database environment of a test run
// (the DB test matrix of the CI workflow sets them; locally any combination
// works):
//
//   - PGTEST_TLS=1: the embedded server accepts TLS connections only with a
//     freshly generated certificate, and the tests connect with
//     sslmode=verify-full. With an external server (PGTEST_DSN) the sslmode of
//     the DSN decides.
//   - PGTEST_COLLATION=<locale>: the template database and with it every test
//     database is created with this collation instead of the C collation of the
//     embedded server, for example en_US.UTF-8 (operating system locale) or
//     icu:en-US (ICU locale, needs a server built with ICU). The locale must be
//     available on the machine; a server that does not know it makes the run
//     fail instead of silently testing the C collation.
const (
	TLSEnv       = "PGTEST_TLS"
	CollationEnv = "PGTEST_COLLATION"
)

var localeName = regexp.MustCompile(`^(icu:)?[A-Za-z0-9_.@-]+$`)

// collationClause returns the CREATE DATABASE options of PGTEST_COLLATION ("" =
// the default collation of the server).
func collationClause() (string, error) {
	v := strings.TrimSpace(os.Getenv(CollationEnv))
	if v == "" {
		return "", nil
	}
	if !localeName.MatchString(v) {
		return "", fmt.Errorf("%s=%q is not a locale name", CollationEnv, v)
	}
	if loc, ok := strings.CutPrefix(v, "icu:"); ok {
		return fmt.Sprintf("TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER icu ICU_LOCALE '%s' LC_COLLATE 'C' LC_CTYPE 'C'", loc), nil
	}
	return fmt.Sprintf("TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER libc LC_COLLATE '%s' LC_CTYPE '%s'", v, v), nil
}

// tlsFiles is the certificate material of an embedded server with TLS.
type tlsFiles struct{ cert, key string }

// writeTLSFiles generates a self-signed certificate for 127.0.0.1 and localhost
// in dir. The certificate is its own trust anchor for sslmode=verify-full.
func writeTLSFiles(dir string) (tlsFiles, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tlsFiles{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "pgtest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tlsFiles{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tlsFiles{}, err
	}
	f := tlsFiles{cert: filepath.Join(dir, "server.crt"), key: filepath.Join(dir, "server.key")}
	if err := os.WriteFile(f.cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tlsFiles{}, err
	}
	// PostgreSQL refuses a private key that others can read
	if err := os.WriteFile(f.key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tlsFiles{}, err
	}
	return f, nil
}

// sslQuery returns the query string of the DSN of the embedded server.
func (s *Server) sslQuery() string {
	if s.tlsCert == "" {
		return "sslmode=disable"
	}
	q := url.Values{}
	q.Set("sslmode", "verify-full")
	q.Set("sslrootcert", s.tlsCert)
	return q.Encode()
}

// TLS reports whether the embedded server was started with TLS (PGTEST_TLS).
func (s *Server) TLS() bool { return s.tlsCert != "" }
