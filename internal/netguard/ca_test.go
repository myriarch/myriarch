// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package netguard

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tlsServer starts an HTTPS server whose certificate is signed by nobody the
// system trusts (the internal CA of the test) and returns it with a PEM file
// of that certificate.
func tlsServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)
	file := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(file, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, file
}

func get(c *http.Client, u string) error {
	resp, err := c.Get(u)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// FX-5: outbound.caFile is the trust store of every client of the policy.
func TestPolicyClientTrustsTheOutboundCA(t *testing.T) {
	srv, caFile := tlsServer(t)
	allow := []string{"127.0.0.1"}

	// without the CA file the certificate is unknown
	p, err := New(allow)
	if err != nil {
		t.Fatal(err)
	}
	err = get(p.Client(5*time.Second), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a certificate of an unknown CA must be refused, got %v", err)
	}

	// with it the connection works, through every kind of transport the policy makes
	pool, err := RootPool(caFile)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = New(allow)
	p.SetRootCAs(pool)
	if err := get(p.Client(5*time.Second), srv.URL); err != nil {
		t.Fatalf("Client: %v", err)
	}
	if err := get(&http.Client{Transport: p.Transport(), Timeout: 5 * time.Second}, srv.URL); err != nil {
		t.Fatalf("Transport: %v", err)
	}
	proxy, _ := url.Parse("http://proxy.example.com:3128")
	tr := p.TransportWithProxy(proxy)
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs != pool {
		t.Fatal("the transport with an explicit proxy must carry the trust store")
	}
	if !tr.ForceAttemptHTTP2 || tr.TLSClientConfig.MinVersion != 0x0303 {
		t.Fatalf("HTTP/2 stays on and TLS 1.2 is the minimum: %v %x", tr.ForceAttemptHTTP2, tr.TLSClientConfig.MinVersion)
	}
	// a clone (used e.g. for executors with their own CA) keeps the trust store
	if c := p.Transport().Clone(); c.TLSClientConfig == nil || c.TLSClientConfig.RootCAs != pool {
		t.Fatal("Clone must keep the trust store")
	}

	// the policy of dev mode (permissive) takes it too
	pp := Permissive()
	pp.SetRootCAs(pool)
	if err := get(pp.Client(5*time.Second), srv.URL); err != nil {
		t.Fatalf("permissive policy: %v", err)
	}
}

func TestRootPool(t *testing.T) {
	if p, err := RootPool(""); p != nil || err != nil {
		t.Fatalf("no file means system roots: %v %v", p, err)
	}
	if _, err := RootPool(filepath.Join(t.TempDir(), "missing.crt")); err == nil {
		t.Fatalf("a missing file must be an error: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.crt")
	_ = os.WriteFile(empty, []byte("not a certificate\n"), 0o600)
	if _, err := RootPool(empty); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Fatalf("a file without certificate must be an error: %v", err)
	}
	// the system roots stay trusted in addition to the file
	_, caFile := tlsServer(t)
	pool, err := RootPool(caFile)
	if err != nil {
		t.Fatal(err)
	}
	sys, _ := x509.SystemCertPool()
	if sys != nil && pool.Equal(sys) {
		t.Fatal("the pool must contain the CA of the file")
	}
	if p, err := AppendRoots(pool, []byte("nope")); err == nil || p != nil {
		t.Fatal("AppendRoots must fail without a certificate")
	}
}
