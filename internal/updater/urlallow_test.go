// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestURLAllowed(t *testing.T) {
	prefixes := []string{"https://fleet.example.com/api/v1/artifacts/", "http://localhost:8080/api/"}
	ok := []string{
		"https://fleet.example.com/api/v1/artifacts/abc/content/otelcol-contrib_0.163.0_linux_amd64.tar.gz",
		"https://FLEET.example.com:443/api/v1/artifacts/abc",
		"http://localhost:8080/api/x",
	}
	for _, u := range ok {
		assert.NoError(t, urlAllowed(u, prefixes), u)
	}
	bad := []string{
		"https://fleet.example.com.evil.net/api/v1/artifacts/abc",
		"https://evil.net/api/v1/artifacts/abc",
		"http://fleet.example.com/api/v1/artifacts/abc",           // scheme downgrade
		"https://fleet.example.com:8443/api/v1/artifacts/abc",     // other port
		"https://fleet.example.com/api/v1/artifacts",              // not below the prefix
		"https://fleet.example.com/api/v1/artifactsX/abc",         // segment boundary
		"https://fleet.example.com/api/v1/artifacts/../../secret", // traversal
		"https://user:pw@fleet.example.com/api/v1/artifacts/abc",  // credentials
		"https://fleet.example.com@evil.net/api/v1/artifacts/abc", // userinfo trick
		"ftp://fleet.example.com/api/v1/artifacts/abc",            // scheme
		"file:///etc/passwd",
		"/api/v1/artifacts/abc",
		"",
		"https://fleet.example.com/other",
	}
	for _, u := range bad {
		assert.Error(t, urlAllowed(u, prefixes), u)
	}
	assert.Error(t, urlAllowed("https://fleet.example.com/api/v1/artifacts/abc", nil), "empty allowlist denies everything")
}

func TestURLAllowedPrefixWithoutTrailingSlash(t *testing.T) {
	p := []string{"https://fleet.example.com/api/v1/artifacts"}
	assert.NoError(t, urlAllowed("https://fleet.example.com/api/v1/artifacts/x", p))
	assert.NoError(t, urlAllowed("https://fleet.example.com/api/v1/artifacts", p))
	assert.Error(t, urlAllowed("https://fleet.example.com/api/v1/artifacts-evil/x", p))
}

func TestDownloadClientRefusesRedirectOffAllowlist(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("evil")) }))
	defer evil.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/artifacts/redirect-evil":
			http.Redirect(w, r, evil.URL+"/x", http.StatusFound)
		case "/api/v1/artifacts/redirect-same":
			http.Redirect(w, r, "/api/v1/artifacts/ok", http.StatusFound)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer good.Close()
	cfg := defaultConfigFor("linux")
	cfg.Update.AllowedURLPrefixes = []string{good.URL + "/api/v1/artifacts/"}
	cfg.Update.DownloadIdleTimeout = Duration(time.Second)
	c, err := newDownloadClient(cfg)
	require.NoError(t, err)

	resp, err := c.Get(good.URL + "/api/v1/artifacts/redirect-same")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	_, err = c.Get(good.URL + "/api/v1/artifacts/redirect-evil")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not below an allowed URL prefix")
}

func TestIdleConnTimesOutStalledReads(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(2 * time.Second) // never sends anything
			c.Close()
		}
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer raw.Close()
	ic := &idleConn{Conn: raw, timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err = ic.Read(make([]byte, 10))
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second)
}

func TestTLSConfigCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer srv.Close()
	_, err := tlsConfig("/nonexistent")
	require.Error(t, err)
	bad := writeArchive(t, []byte("not pem"))
	_, err = tlsConfig(bad)
	require.Error(t, err)

	// without the CA the self-signed server is rejected, with it accepted
	cfg := defaultConfigFor("linux")
	cfg.Update.DownloadIdleTimeout = Duration(time.Second)
	c, err := newDownloadClient(cfg)
	require.NoError(t, err)
	_, err = c.Get(srv.URL)
	require.Error(t, err)

	pem := tlsCertPEM(srv)
	cfg.Server.CAFile = writeArchive(t, pem)
	c, err = newDownloadClient(cfg)
	require.NoError(t, err)
	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

func tlsCertPEM(srv *httptest.Server) []byte {
	var out []byte
	for _, c := range srv.TLS.Certificates {
		for _, der := range c.Certificate {
			out = append(out, pemEncode(der)...)
		}
	}
	return out
}

var _ = tls.VersionTLS12
