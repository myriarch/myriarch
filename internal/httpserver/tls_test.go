// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/config"
)

type pki struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &pki{caCert: cert, caKey: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (p *pki) issue(t *testing.T, cn string, usage x509.ExtKeyUsage, sans ...net.IP) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: sans}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

// TestTLSWithOptionalClientCertificates: myriarchd terminates TLS, requests a client
// certificate if the client has one (mTLS for agents) but still serves
// browsers without one; the verified chain reaches the handlers.
func TestTLSWithOptionalClientCertificates(t *testing.T) {
	ca := newPKI(t)
	dir := t.TempDir()
	srvCert, srvKey := ca.issue(t, "myriarchd", x509.ExtKeyUsageServerAuth, net.ParseIP("127.0.0.1"))
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := config.ServerConfig{TLS: config.TLSServerConfig{CertFile: write("s.pem", srvCert), KeyFile: write("s.key", srvKey), ClientCAFile: write("ca.pem", ca.caPEM)}, ShutdownTimeout: time.Second}
	mod := api.ModuleFunc{ModuleName: "tls", RoutesFunc: func() []api.Route {
		return []api.Route{{Method: "GET", Pattern: "/who", OperationID: "who", Auth: api.AuthPublic, Handler: func(w http.ResponseWriter, r *http.Request) {
			cn := ""
			if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 {
				cn = r.TLS.VerifiedChains[0][0].Subject.CommonName
			}
			fmt.Fprintf(w, "tls=%v cn=%s", r.TLS != nil, cn)
		}}}
	}}
	s, stop := startServer(t, cfg, Deps{Modules: []api.Module{mod}})
	defer stop()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.caPEM)
	get := func(clientCert *tls.Certificate) string {
		t.Helper()
		tc := &tls.Config{RootCAs: pool}
		if clientCert != nil {
			tc.Certificates = []tls.Certificate{*clientCert}
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 5 * time.Second}
		resp, err := c.Get("https://" + s.Addr().String() + "/api/v1/who")
		if err != nil {
			t.Fatalf("https request: %v", err)
		}
		defer resp.Body.Close()
		b := make([]byte, 100)
		n, _ := resp.Body.Read(b)
		return string(b[:n])
	}
	if got := get(nil); got != "tls=true cn=" {
		t.Fatalf("browser without client certificate: %q", got)
	}
	cc, ck := ca.issue(t, "edge-1001", x509.ExtKeyUsageClientAuth)
	pair, err := tls.X509KeyPair(cc, ck)
	if err != nil {
		t.Fatal(err)
	}
	if got := get(&pair); got != "tls=true cn=edge-1001" {
		t.Fatalf("agent with client certificate: %q", got)
	}
	// a certificate of a foreign CA is rejected during the handshake
	other := newPKI(t)
	fc, fk := other.issue(t, "intruder", x509.ExtKeyUsageClientAuth)
	fpair, _ := tls.X509KeyPair(fc, fk)
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{fpair}}}, Timeout: 5 * time.Second}
	if resp, err := c.Get("https://" + s.Addr().String() + "/api/v1/who"); err == nil {
		resp.Body.Close()
		t.Fatalf("certificate of a foreign CA must be rejected, got %d", resp.StatusCode)
	}
	_ = context.Background
}

func TestInvalidTLSConfiguration(t *testing.T) {
	s, err := New(config.ServerConfig{ListenAddr: "127.0.0.1:0", TLS: config.TLSServerConfig{CertFile: "/nonexistent", KeyFile: "/nonexistent"}}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("missing certificate files must fail Run")
	}
	s, _ = New(config.ServerConfig{ListenAddr: "127.0.0.1:0", TLS: config.TLSServerConfig{CertFile: "c", KeyFile: "k", ClientCAFile: "/nonexistent"}}, Deps{})
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("missing client CA file must fail Run")
	}
}
