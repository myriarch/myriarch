// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// urlAllowed reports whether rawURL may be downloaded: absolute http(s) URL
// without credentials, whose scheme and host equal those of an allowed
// prefix and whose path lies below the prefix path (on a path segment
// boundary, without ".." elements). A plain string prefix test would accept
// "https://fleet.example.com.evil.net/".
func urlAllowed(rawURL string, prefixes []string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("download URL %q: unsupported scheme", rawURL)
	}
	if u.User != nil || u.Host == "" {
		return fmt.Errorf("download URL %q must not contain credentials and needs a host", rawURL)
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." {
			return fmt.Errorf("download URL %q contains path traversal", rawURL)
		}
	}
	for _, p := range prefixes {
		pu, err := url.Parse(p)
		if err != nil {
			continue
		}
		if !strings.EqualFold(pu.Scheme, u.Scheme) || canonicalHost(pu) != canonicalHost(u) {
			continue
		}
		pp := pu.Path
		if pp == "" {
			pp = "/"
		}
		if !strings.HasPrefix(u.Path, pp) {
			continue
		}
		if strings.HasSuffix(pp, "/") || len(u.Path) == len(pp) || u.Path[len(pp)] == '/' {
			return nil
		}
	}
	return fmt.Errorf("download URL %q is not below an allowed URL prefix (update.allowedUrlPrefixes)", rawURL)
}

func canonicalHost(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Hostname()) + ":" + port
}

// idleConn refreshes a read deadline before every Read so that stalled
// downloads fail instead of hanging forever.
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(p)
}

// tlsConfig builds the TLS configuration (system roots + optional CA file).
func tlsConfig(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA file %s contains no PEM certificates", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// newDownloadClient builds the HTTP client used for artifact downloads:
// allowlisted redirects only, TLS with the configured CA, stalled-read
// timeout, no overall timeout (artifacts are ~100 MB).
func newDownloadClient(cfg Config) (*http.Client, error) {
	tlsCfg, err := tlsConfig(cfg.Server.CAFile)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	idle := cfg.Update.DownloadIdleTimeout.D()
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ForceAttemptHTTP2:     true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &idleConn{Conn: c, timeout: idle}, nil
		},
	}
	prefixes := cfg.Update.AllowedURLPrefixes
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if err := urlAllowed(req.URL.String(), prefixes); err != nil {
				return fmt.Errorf("redirect: %w", err)
			}
			if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("redirect from https to http refused")
			}
			return nil
		},
	}, nil
}
