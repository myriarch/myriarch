// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"encoding/pem"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

// connectProxy is a forward proxy that tunnels CONNECT requests. It hijacks the
// connection before it answers: Hijack flushes a 200 written earlier and only
// then stops the background read of net/http, so a client that starts its TLS
// handshake at once can have the first byte of its ClientHello taken by that
// read, which hands it back in the bufio.Reader of Hijack and nowhere else.
func connectProxy(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var tunnels atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		dst, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		src, buf, err := hj.Hijack()
		if err != nil {
			dst.Close()
			return
		}
		// the client starts its handshake only after it has read this, when the
		// background read of net/http is stopped
		if _, err := io.WriteString(src, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			src.Close()
			dst.Close()
			return
		}
		tunnels.Add(1)
		// buf.Reader yields the bytes net/http has read from the client already
		// before it reads src
		go func() { _, _ = io.Copy(dst, buf.Reader); src.Close(); dst.Close() }()
		go func() { _, _ = io.Copy(src, dst); src.Close(); dst.Close() }()
	}))
	t.Cleanup(srv.Close)
	return srv, &tunnels
}

// FX-5: the artifact cache trusts the internal CA of outbound.caFile also on its
// own transport for artifacts.proxyUrl (which Transport.Clone() style code
// could lose).
func TestArtifactDownloadThroughProxyTrustsTheOutboundCA(t *testing.T) {
	m := newMirror(t)
	m.release("0.162.0", fakeArchive(500))
	proxy, tunnels := connectProxy(t)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.srv.Certificate().Raw}), 0o600))

	build := func(withCA bool) *Cache {
		p, err := netguard.New([]string{"127.0.0.1"})
		require.NoError(t, err)
		if withCA {
			pool, err := netguard.RootPool(caFile)
			require.NoError(t, err)
			p.SetRootCAs(pool)
		}
		cfg := config.Default().Artifacts
		cfg.Dir = t.TempDir()
		cfg.SourceBaseURL = m.srv.URL
		cfg.ProxyURL = proxy.URL
		cfg.Verify.TrustedRootFile = filepath.Join(sigDir, "trusted_root.json")
		c, err := NewCache(cfg, Deps{Store: memstore.New(), HTTPClient: p.Client(time.Minute), Policy: p, DownloadSecret: []byte("0123456789abcdef0123456789abcdef")})
		require.NoError(t, err)
		c.verifyFn = func(_, _ []byte, _, version string) (*verified, error) {
			return &verified{Identity: "https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v" + version}, nil
		}
		return c
	}

	_, err := build(false).Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")

	a, err := build(true).Ensure(context.Background(), "otelcol-contrib", "0.162.0", "linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactReady, a.Status)
	assert.Positive(t, tunnels.Load(), "the download went through the proxy")
}
