// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"crypto/x509"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

// FX-5: the release tracker reaches an HTTPS API whose certificate was issued
// by the internal CA (outbound.caFile) and refuses it without that CA.
func TestTrackerTrustsTheOutboundCA(t *testing.T) {
	g := newGH(t)
	srv := httptest.NewUnstartedServer(g.srv.Config.Handler)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	cfg := config.Default().Releases
	cfg.FixtureDir = ""
	cfg.GitHubAPIURL = srv.URL + "/api/v3"
	cfg.RawBaseURL = srv.URL + "/raw"

	poll := func(pool *x509.CertPool) error {
		p, err := netguard.New([]string{"127.0.0.1"}) // the test server is on loopback
		require.NoError(t, err)
		p.SetRootCAs(pool)
		tr, err := NewTracker(cfg, Deps{Store: memstore.New(), HTTPClient: p.Client(10 * time.Second)})
		require.NoError(t, err)
		return tr.Poll(context.Background())
	}

	err := poll(nil)
	require.Error(t, err, "a certificate of an unknown CA must be refused")
	assert.Contains(t, err.Error(), "certificate")

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	require.NoError(t, poll(pool))
}
