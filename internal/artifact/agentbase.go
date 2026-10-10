// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/myriarch/myriarch/internal/domain"
)

// agentBase is one parsed artifacts.agentBaseUrls entry.
type agentBase struct {
	key string // normalized host:port
	url string // as configured, without trailing slash
}

// CheckAgentBaseURL validates one artifacts.agentBaseUrls entry: an absolute
// http(s) URL with a host and nothing but an optional "/" as path (no
// credentials, query or fragment). It returns the parsed URL.
func CheckAgentBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%q is not a valid URL", raw)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%q must be an absolute http(s) URL", raw)
	case u.Hostname() == "":
		return nil, fmt.Errorf("%q has no host", raw)
	case u.User != nil:
		return nil, fmt.Errorf("%q must not contain credentials", raw)
	case u.RawQuery != "" || u.ForceQuery:
		return nil, fmt.Errorf("%q must not contain a query", raw)
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return nil, fmt.Errorf("%q must not contain a fragment", raw)
	case u.Path != "" && u.Path != "/":
		return nil, fmt.Errorf("%q must not contain a path", raw)
	}
	return u, nil
}

// hostPortKey returns the normalized host:port of a URL ("" if it has no
// host): lower case, with the default port of the scheme (http and ws 80,
// https and wss 443) made explicit.
func hostPortKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "ws":
			port = "80"
		case "https", "wss":
			port = "443"
		}
	}
	if port == "" {
		return host
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	return host + ":" + port
}

func parseAgentBases(urls []string) ([]agentBase, error) {
	out := make([]agentBase, 0, len(urls))
	for i, raw := range urls {
		u, err := CheckAgentBaseURL(raw)
		if err != nil {
			return nil, fmt.Errorf("artifacts.agentBaseUrls[%d]: %w", i, err)
		}
		out = append(out, agentBase{key: hostPortKey(u), url: strings.TrimRight(raw, "/")})
	}
	return out, nil
}

// baseFor returns the download base URL for an updater: the first
// agentBaseUrls entry with the same host:port as the updater's reported
// server endpoint, else server.publicUrl.
func (c *Cache) baseFor(updater *domain.Agent) string {
	if updater == nil || len(c.agentBases) == 0 {
		return c.deps.PublicURL
	}
	ep := strings.TrimSpace(updater.NonIdentifyingAttributes[domain.AttrUpdaterServerEndpoint])
	if ep == "" {
		return c.deps.PublicURL
	}
	u, err := url.Parse(ep)
	if err != nil {
		return c.deps.PublicURL
	}
	key := hostPortKey(u)
	if key == "" {
		return c.deps.PublicURL
	}
	for _, b := range c.agentBases {
		if b.key == key {
			return b.url
		}
	}
	return c.deps.PublicURL
}
