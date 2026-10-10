// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package realip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func run(t *testing.T, trusted []string, remote string, hdr map[string]string) (client, peer string) {
	t.Helper()
	nets, err := ParseTrusted(trusted)
	if err != nil {
		t.Fatal(err)
	}
	h := New(nets).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, peer = ClientIP(r), PeerAddr(r)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return
}

func TestClientIP(t *testing.T) {
	trusted := []string{"10.0.0.0/8", "192.168.1.1"}
	tests := []struct {
		name   string
		remote string
		xff    string
		real   string
		want   string
	}{
		{"direct, no proxy", "203.0.113.5:4000", "", "", "203.0.113.5"},
		{"untrusted peer cannot spoof", "203.0.113.5:4000", "1.2.3.4", "9.9.9.9", "203.0.113.5"},
		{"trusted proxy, single hop", "10.1.1.1:5000", "198.51.100.7", "", "198.51.100.7"},
		{"trusted proxy, chain with proxies", "10.1.1.1:5000", "198.51.100.7, 10.2.2.2, 192.168.1.1", "", "198.51.100.7"},
		{"client-supplied prefix is ignored", "10.1.1.1:5000", "6.6.6.6, 198.51.100.7", "", "198.51.100.7"},
		{"all hops trusted -> leftmost", "10.1.1.1:5000", "10.9.9.9, 10.2.2.2", "", "10.9.9.9"},
		{"garbage right of the client is not trusted", "10.1.1.1:5000", "198.51.100.7, nonsense", "", "10.1.1.1"},
		{"garbage left of the client is irrelevant", "10.1.1.1:5000", "nonsense, 198.51.100.7", "", "198.51.100.7"},
		{"x-real-ip fallback", "10.1.1.1:5000", "", "198.51.100.8", "198.51.100.8"},
		{"trusted without headers", "10.1.1.1:5000", "", "", "10.1.1.1"},
		{"ipv6 client", "10.1.1.1:5000", "2001:db8::1", "", "2001:db8::1"},
		{"ipv6 peer", "[2001:db8::5]:80", "", "", "2001:db8::5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tc.xff != "" {
				hdr["X-Forwarded-For"] = tc.xff
			}
			if tc.real != "" {
				hdr["X-Real-Ip"] = tc.real
			}
			got, peer := run(t, trusted, tc.remote, hdr)
			if got != tc.want {
				t.Errorf("client = %q, want %q", got, tc.want)
			}
			if peer != tc.remote {
				t.Errorf("peer = %q, want %q (original must stay available)", peer, tc.remote)
			}
		})
	}
}

func TestParseTrusted(t *testing.T) {
	if _, err := ParseTrusted([]string{"nonsense"}); err == nil {
		t.Error("expected error")
	}
	if n, err := ParseTrusted([]string{"", " 10.0.0.1 ", "fd00::/8"}); err != nil || len(n) != 2 {
		t.Errorf("%v %v", n, err)
	}
}

func TestLimiterKeyGroupsIPv6ByPrefixAndUnmapsIPv4(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.10":                       "192.0.2.10",
		"::ffff:192.0.2.10":                "192.0.2.10",
		"2001:db8:1:2:aaaa:bbbb:cccc:dddd": "2001:db8:1:2::/64",
		"2001:db8:1:2::1":                  "2001:db8:1:2::/64",
		"2001:db8:1:3::1":                  "2001:db8:1:3::/64",
		"not an address":                   "not an address",
	} {
		if got := LimiterKey(in); got != want {
			t.Errorf("LimiterKey(%q) = %q, want %q", in, got, want)
		}
	}
}
