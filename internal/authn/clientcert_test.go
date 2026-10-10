// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testCA) issue(t *testing.T, cn string, notAfter time.Time, usage x509.ExtKeyUsage) (*x509.Certificate, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestClientCertificatesForwardedByTrustedProxy(t *testing.T) {
	ca := newCA(t, "fleet-agents-ca")
	otherCA := newCA(t, "evil-ca")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, envOpts{
		server: func(c *config.ServerConfig) {
			c.TrustedProxies = []string{"10.0.0.0/8"}
			c.TLS.ClientCAFile = caFile
		},
		// mTLS only: the certificate substitutes for the enrollment token
		opamp: func(c *config.OpAMPConfig) {
			c.ClientCertHeader = "X-Client-Cert"
			c.RequireEnrollmentToken = false
			c.RequireClientCert = true
		},
	})
	_, good := ca.issue(t, "edge-1001", time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)
	_, expired := ca.issue(t, "old", time.Now().Add(-time.Minute), x509.ExtKeyUsageClientAuth)
	_, foreign := otherCA.issue(t, "intruder", time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)
	_, serverOnly := ca.issue(t, "server-cert", time.Now().Add(time.Hour), x509.ExtKeyUsageServerAuth)

	req := func(peer, header string) *http.Request {
		r := httptest.NewRequest("GET", "/v1/opamp", nil)
		r.RemoteAddr = peer
		if header != "" {
			r.Header.Set("X-Client-Cert", header)
		}
		return r
	}
	escaped := func(p string) string { return url.QueryEscape(p) }

	// requireEnrollmentToken=false (mTLS only): a verified certificate is enough
	cred, err := e.svc.AuthenticateAgent(req("10.1.1.1:4000", escaped(good)))
	if err != nil || cred.ClientCertSubject != "edge-1001" || cred.EnrollmentTokenID != nil {
		t.Fatalf("valid forwarded certificate: %v %+v", err, cred)
	}
	// Envoy / Istio x-forwarded-client-cert style
	if cred, err := e.svc.AuthenticateAgent(req("10.1.1.1:4000", `By=spiffe://x;Hash=abc;Cert="`+escaped(good)+`";Subject="CN=edge-1001"`)); err != nil || cred.ClientCertSubject != "edge-1001" {
		t.Fatalf("envoy format: %v", err)
	}
	for name, r := range map[string]*http.Request{
		"untrusted peer forging the header": req("203.0.113.9:4000", escaped(good)),
		"foreign CA":                        req("10.1.1.1:4000", escaped(foreign)),
		"expired certificate":               req("10.1.1.1:4000", escaped(expired)),
		"wrong extended key usage":          req("10.1.1.1:4000", escaped(serverOnly)),
		"garbage":                           req("10.1.1.1:4000", "not-a-certificate"),
		"no header":                         req("10.1.1.1:4000", ""),
	} {
		if _, err := e.svc.AuthenticateAgent(r); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want ErrUnauthorized", name, err)
		}
	}

	// a valid certificate together with a bad token is still rejected
	r := req("10.1.1.1:4000", escaped(good))
	r.Header.Set("Authorization", "Bearer myre_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	if _, err := e.svc.AuthenticateAgent(r); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("bad token with valid cert: %v", err)
	}
}

func TestRequireClientCert(t *testing.T) {
	ca := newCA(t, "ca")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(caFile, ca.pem, 0o600)
	e := newEnv(t, envOpts{
		server: func(c *config.ServerConfig) { c.TrustedProxies = []string{"10.0.0.0/8"}; c.TLS.ClientCAFile = caFile },
		opamp: func(c *config.OpAMPConfig) {
			c.ClientCertHeader = "X-Client-Cert"
			c.RequireClientCert = true
			c.RequireEnrollmentToken = false // mTLS only
		},
	})
	cert, certPEM := ca.issue(t, "edge-1", time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)

	// no certificate: rejected although tokens are not required
	if _, err := e.svc.AuthenticateAgent(httptest.NewRequest("GET", "/v1/opamp", nil)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("requireClientCert without certificate: %v", err)
	}
	// even with a valid token the certificate stays mandatory
	tok, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	if err := e.db.Identity().CreateEnrollmentToken(context.Background(), domain.EnrollmentToken{ID: uuid.New(), Name: "t", Hash: domain.SHA256([]byte(tok)), CreatedBy: "x"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/opamp", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	if _, err := e.svc.AuthenticateAgent(r); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("token alone must not satisfy requireClientCert: %v", err)
	}
	// certificate terminated by myriarchd itself (TLS handshake)
	r = httptest.NewRequest("GET", "/v1/opamp", nil)
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert, ca.cert}}}
	cred, err := e.svc.AuthenticateAgent(r)
	if err != nil || cred.ClientCertSubject != "edge-1" {
		t.Fatalf("mTLS at myriarchd: %v %+v", err, cred)
	}
	// certificate + token: both recorded
	r.Header.Set("Authorization", "Bearer "+tok)
	cred, err = e.svc.AuthenticateAgent(r)
	if err != nil || cred.ClientCertSubject != "edge-1" || cred.EnrollmentTokenID == nil {
		t.Fatalf("cert + token: %v %+v", err, cred)
	}
	// forwarded certificate via trusted proxy
	r = httptest.NewRequest("GET", "/v1/opamp", nil)
	r.RemoteAddr = "10.0.0.5:1"
	r.Header.Set("X-Client-Cert", url.QueryEscape(certPEM))
	if _, err := e.svc.AuthenticateAgent(r); err != nil {
		t.Fatalf("forwarded cert: %v", err)
	}
}

func TestNewFailsOnBrokenClientCA(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(f, []byte("not pem"), 0o600)
	e := newEnv(t, envOpts{})
	_ = e
	_, err := authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{TLS: config.TLSServerConfig{ClientCAFile: f}}, config.OpAMPConfig{},
		authn.Deps{Store: e.db})
	if err == nil {
		t.Fatal("a client CA file without certificates must fail at startup")
	}
	_, err = authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{TLS: config.TLSServerConfig{ClientCAFile: f + ".missing"}}, config.OpAMPConfig{},
		authn.Deps{Store: e.db})
	if err == nil {
		t.Fatal("missing client CA file")
	}
	if _, err := authn.New(context.Background(), config.AuthConfig{}, config.ServerConfig{}, config.OpAMPConfig{}, authn.Deps{}); err == nil {
		t.Fatal("store is required")
	}
}

// requireEnrollmentToken means a token even when a verified certificate is
// presented: both set means token AND certificate.
func TestEnrollmentTokenRequiredEvenWithCertificate(t *testing.T) {
	ca := newCA(t, "fleet-agents-ca")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, envOpts{
		server: func(c *config.ServerConfig) { c.TrustedProxies = []string{"10.0.0.0/8"}; c.TLS.ClientCAFile = caFile },
		opamp:  func(c *config.OpAMPConfig) { c.ClientCertHeader = "X-Client-Cert"; c.RequireEnrollmentToken = true },
	})
	_, good := ca.issue(t, "edge-1001", time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)
	r := httptest.NewRequest("GET", "/v1/opamp", nil)
	r.RemoteAddr = "10.1.1.1:4000"
	r.Header.Set("X-Client-Cert", url.QueryEscape(good))
	if _, err := e.svc.AuthenticateAgent(r); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("certificate without token must be rejected when the token is required: %v", err)
	}
	// with a valid token and the certificate the agent is accepted and carries both
	secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	if err := e.db.Identity().CreateEnrollmentToken(context.Background(), domain.EnrollmentToken{
		ID: uuid.New(), Name: "t", DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+secret)
	cred, err := e.svc.AuthenticateAgent(r)
	if err != nil || cred.EnrollmentTokenID == nil || cred.ClientCertSubject != "edge-1001" {
		t.Fatalf("token and certificate: %v %+v", err, cred)
	}
}
