// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

func updaterWith(endpoint string) *domain.Agent {
	a := &domain.Agent{ID: domain.AgentID(uuid.Must(uuid.NewV7())), Kind: domain.AgentKindUpdater, NonIdentifyingAttributes: map[string]string{}}
	if endpoint != "" {
		a.NonIdentifyingAttributes[domain.AttrUpdaterServerEndpoint] = endpoint
	}
	return a
}

func TestDownloadURLForPicksTheBasePerUpdater(t *testing.T) {
	s := newServeFixture(t, func(c *config.ArtifactsConfig) {
		c.AgentBaseURLs = []string{"http://myriarch:8080", "https://Fleet-Internal.example.com/"}
	})
	suffix := "/api/v1/artifacts/" + s.art.ID.String() + "/content/otelcol-contrib_0.162.0_linux_amd64.tar.gz"
	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"ws endpoint matches the http entry (same host and port)", "ws://myriarch:8080", "http://myriarch:8080"},
		{"http endpoint form", "http://myriarch:8080", "http://myriarch:8080"},
		{"case insensitive host", "ws://MyriArch:8080", "http://myriarch:8080"},
		{"endpoint with a path", "ws://myriarch:8080/v1/opamp", "http://myriarch:8080"},
		{"default port of wss equals https", "wss://fleet-internal.example.com", "https://Fleet-Internal.example.com"},
		{"explicit default port", "wss://fleet-internal.example.com:443", "https://Fleet-Internal.example.com"},
		{"other port is no match", "ws://myriarch:9090", "https://fleet.example.com"},
		{"other host is no match", "wss://fleet.example.com", "https://fleet.example.com"},
		{"missing attribute falls back to publicUrl", "", "https://fleet.example.com"},
		{"unparsable attribute falls back to publicUrl", "://%%", "https://fleet.example.com"},
		{"attribute without host falls back", "opamp", "https://fleet.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := updaterWith(tc.endpoint)
			u, hdr, err := s.cache.DownloadURLFor(s.art, up)
			require.NoError(t, err)
			assert.Equal(t, tc.want+suffix, u)
			// the token is minted for the updater agent
			cl, err := s.cache.parseToken(strings.TrimPrefix(hdr["Authorization"], "Bearer "))
			require.NoError(t, err)
			assert.Equal(t, up.ID, cl.Agent)
			assert.Equal(t, s.art.ID, cl.Artifact)
		})
	}
}

func TestDownloadURLForWithoutEntriesAndWithoutUpdater(t *testing.T) {
	s := newServeFixture(t)
	u, _, err := s.cache.DownloadURLFor(s.art, updaterWith("ws://myriarch:8080"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(u, "https://fleet.example.com/api/v1/artifacts/"), "no agentBaseUrls: always the public URL: %s", u)
	_, _, err = s.cache.DownloadURLFor(s.art, nil)
	assert.Error(t, err)
	// DownloadURL keeps using the public URL
	u2, _, err := s.cache.DownloadURL(s.art, s.agent)
	require.NoError(t, err)
	assert.Equal(t, s.url, u2)

	// without server.publicUrl an updater without a matching entry cannot be served, one with a match can
	c := s.otherCache(func(d *Deps) { d.PublicURL = "" })
	_, _, err = c.DownloadURLFor(s.art, updaterWith(""))
	assert.Error(t, err)
}

// TestServeContentAcceptsOffersWithAnInternalBase: the download token and the
// offer check do not depend on the base URL of the offer.
func TestServeContentAcceptsOffersWithAnInternalBase(t *testing.T) {
	s := newServeFixture(t, func(c *config.ArtifactsConfig) { c.AgentBaseURLs = []string{"http://myriarch:8080"} })
	up := updaterWith("ws://myriarch:8080")
	u, hdr, err := s.cache.DownloadURLFor(s.art, up)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(u, "http://myriarch:8080/"))
	s.agent = up.ID
	s.offer(u)
	// what the OpAMP layer does when it sends the offer
	hdr2, err := s.cache.DownloadHeaders(u, up.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, hdr["Authorization"])
	w := s.get(strings.TrimPrefix(u, "http://myriarch:8080"), hdr2)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, s.archive, w.Body.Bytes())
}

func TestCheckAgentBaseURL(t *testing.T) {
	for _, ok := range []string{"http://myriarch:8080", "https://fleet.example.com", "https://fleet.example.com/", "http://10.0.0.5:8080/", "http://[::1]:8080"} {
		_, err := CheckAgentBaseURL(ok)
		assert.NoError(t, err, ok)
	}
	for bad, want := range map[string]string{
		"myriarch:8080":                    "absolute http(s) URL",
		"ftp://fleet.example.com":          "absolute http(s) URL",
		"ws://fleet.example.com":           "absolute http(s) URL",
		"http://":                          "no host",
		"http://user:pw@fleet.example.com": "credentials",
		"http://fleet.example.com/base":    "path",
		"http://fleet.example.com/?x=1":    "query",
		"http://fleet.example.com/?":       "query",
		"http://fleet.example.com/#frag":   "fragment",
		"http://fleet.example.com#":        "fragment",
		"http://[::1":                      "not a valid URL",
	} {
		_, err := CheckAgentBaseURL(bad)
		if assert.Error(t, err, bad) {
			assert.Contains(t, err.Error(), want, bad)
		}
	}
	// NewCache refuses invalid entries
	f := newFixture(t)
	cfg := f.cfg
	cfg.AgentBaseURLs = []string{"http://ok:8080", "http://bad/path"}
	_, err := NewCache(cfg, f.deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifacts.agentBaseUrls[1]")
}
