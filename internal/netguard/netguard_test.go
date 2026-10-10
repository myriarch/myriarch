// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestCheckAddrDeniesReservedRanges(t *testing.T) {
	p, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	denied := []string{
		"127.0.0.1", "127.255.255.254", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1",
		"169.254.169.254", "0.0.0.0", "224.0.0.1", "255.255.255.255", "::1", "::", "fc00::1", "fd12:3456::1", "fe80::1",
		"ff02::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1", "64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe",
		"2002:7f00:1::1",
	}
	for _, s := range denied {
		if err := p.CheckAddr(netip.MustParseAddr(s)); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: want denied, got %v", s, err)
		}
	}
	allowed := []string{"8.8.8.8", "140.82.112.5", "172.32.0.1", "2606:4700::1111", "64:ff9b::808:808"}
	for _, s := range allowed {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: want allowed, got %v", s, err)
		}
	}
}

func TestAllowedCIDRsOverrideDeny(t *testing.T) {
	p, err := New([]string{"10.20.0.0/16", "192.168.5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckAddr(netip.MustParseAddr("10.20.3.4")); err != nil {
		t.Errorf("allowed CIDR denied: %v", err)
	}
	if err := p.CheckAddr(netip.MustParseAddr("192.168.5.5")); err != nil {
		t.Errorf("allowed IP denied: %v", err)
	}
	if err := p.CheckAddr(netip.MustParseAddr("10.21.0.1")); err == nil {
		t.Error("10.21.0.1 must stay denied")
	}
	if _, err := New([]string{"not-a-cidr"}); err == nil {
		t.Error("invalid CIDR must be rejected")
	}
}

func TestGuardedTransportBlocksLoopbackAndRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()

	p, _ := New(nil)
	c := p.Client(0)
	if _, err := c.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "not allowed by the outbound policy") {
		t.Fatalf("loopback request must be blocked, got %v", err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	h, _, _ := net.SplitHostPort(host)
	p2, _ := New([]string{h + "/32"})
	resp, err := p2.Client(0).Get(srv.URL)
	if err != nil {
		t.Fatalf("allowed range: %v", err)
	}
	resp.Body.Close()
	if _, err := Permissive().Client(0).Get(srv.URL); err != nil {
		t.Fatalf("permissive policy: %v", err)
	}

	// a redirect to a forbidden address is stopped by the dial check even if
	// the first hop is allowed
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redir.Close()
	rh, _, _ := net.SplitHostPort(strings.TrimPrefix(redir.URL, "http://"))
	p3, _ := New([]string{rh + "/32"})
	cl := p3.Client(0)
	cl.CheckRedirect = nil // isolate the dial check
	if _, err := cl.Get(redir.URL); err == nil || !strings.Contains(err.Error(), "not allowed by the outbound policy") {
		t.Fatalf("redirect to metadata address must be blocked, got %v", err)
	}
}

func TestExemptProxyHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("proxied")) }))
	defer srv.Close()
	pu, _ := url.Parse(srv.URL)
	p, _ := New(nil)
	tr := p.TransportWithProxy(pu)
	c := &http.Client{Transport: tr}
	resp, err := c.Get("http://example.invalid/x")
	if err != nil {
		t.Fatalf("proxy host must be exempt: %v", err)
	}
	resp.Body.Close()
	// without the exemption the proxy dial is refused
	p2, _ := New(nil)
	tr2 := p2.transport(http.ProxyURL(pu), nil)
	if _, err := (&http.Client{Transport: tr2}).Get("http://example.invalid/x"); err == nil {
		t.Fatal("proxy on loopback must be refused without exemption")
	}
	_ = context.Background()
}

func TestRedirectPolicy(t *testing.T) {
	check := RedirectPolicy(3, []string{"github.com", "*.githubusercontent.com"})
	mk := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	if err := check(mk("https://objects.githubusercontent.com/x"), nil); err != nil {
		t.Errorf("listed wildcard host: %v", err)
	}
	if err := check(mk("https://evil.example.com/x"), nil); err == nil {
		t.Error("unlisted host must be refused")
	}
	if err := check(mk("http://github.com/x"), nil); err == nil {
		t.Error("http target must be refused")
	}
	if err := check(mk("https://github.com/x"), make([]*http.Request, 3)); err == nil {
		t.Error("hop limit must apply")
	}
	if err := RedirectPolicy(3, nil)(mk("https://anything.example/x"), nil); err != nil {
		t.Errorf("no host list: %v", err)
	}
}

// The proxy exemption covers the proxy connection of one transport only: the exact host and port.
func TestProxyExemptionIsNarrow(t *testing.T) {
	var p2Hits int32
	var mu sync.Mutex
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("proxied")) }))
	defer proxy.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		p2Hits++
		mu.Unlock()
		w.Write([]byte("direct"))
	}))
	defer other.Close()
	pu, _ := url.Parse(proxy.URL)

	p, _ := New(nil)
	tr := p.TransportWithProxy(pu)
	// the transport with the proxy reaches the proxy ...
	resp, err := (&http.Client{Transport: tr}).Get("http://example.invalid/x")
	if err != nil {
		t.Fatalf("proxy connection: %v", err)
	}
	resp.Body.Close()
	// ... but another client of the same policy does not get a direct connection to that host and port,
	if _, err := p.Client(0).Get(proxy.URL); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct request to the proxy address through another client: %v", err)
	}
	// and the exemption does not cover another port of the proxy host
	if _, err := p.Client(0).Get(other.URL); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct request to another port of the proxy host: %v", err)
	}
	if p2Hits != 0 {
		t.Fatalf("the other listener received %d requests", p2Hits)
	}
}

func TestAllProxyIsNotExempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("direct")) }))
	defer srv.Close()
	old := getenv
	getenv = func(k string) string {
		if k == "ALL_PROXY" || k == "all_proxy" {
			return srv.URL
		}
		return ""
	}
	defer func() { getenv = old }()
	p, _ := New(nil)
	if _, err := p.Client(0).Get(srv.URL); !errors.Is(err, ErrDenied) {
		t.Fatalf("ALL_PROXY host must not be exempt: %v", err)
	}
}

func TestEnvironmentProxyHostsAreExemptForThatTransportOnly(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("proxied")) }))
	defer proxy.Close()
	old := getenv
	getenv = func(k string) string {
		if k == "HTTPS_PROXY" {
			return proxy.URL
		}
		return ""
	}
	defer func() { getenv = old }()
	addrs := proxyAddrsFromEnv()
	if len(addrs) != 1 || addrs[0] != strings.TrimPrefix(proxy.URL, "http://") {
		t.Fatalf("proxy addresses from the environment: %v", addrs)
	}
}

func TestDenyListCoversSpecialPurposeRanges(t *testing.T) {
	p, _ := New(nil)
	for _, s := range []string{
		"::a00:1", "::7f00:1", "::808:808", // IPv4-compatible IPv6
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", // Teredo
		"100::1",                               // discard prefix
		"192.88.99.1", "192.0.2.1", "198.51.100.7", "203.0.113.9",
	} {
		if err := p.CheckAddr(netip.MustParseAddr(s)); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: want denied, got %v", s, err)
		}
	}
	// operators can still open a documentation range for a lab
	lab, _ := New([]string{"192.0.2.0/24"})
	if err := lab.CheckAddr(netip.MustParseAddr("192.0.2.1")); err != nil {
		t.Errorf("allowedCidrs must open the range: %v", err)
	}
}

// With a forward proxy only the host name is left to the proxy: address literals and localhost are
// refused locally, before the request reaches the proxy.
func TestProxiedRequestsToDeniedLiteralsAreRefusedLocally(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.Host)
		mu.Unlock()
		w.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	p, _ := New(nil)
	c := &http.Client{Transport: p.TransportWithProxy(pu)}
	for _, target := range []string{"http://169.254.169.254/latest/", "http://[fd00::1]/", "http://10.1.2.3:8080/", "http://localhost:9999/", "http://127.0.0.1:1/",
		// spellings that netip does not parse: trailing dot, numeric hosts that are not canonical
		"http://localhost.:9999/", "http://app.localhost.:9999/", "http://2130706433/", "http://0x7f.0.0.1/", "http://127.1/", "http://0177.0.0.1/", "http://0xa9fea9fe/"} {
		if _, err := c.Get(target); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: want ErrDenied, got %v", target, err)
		}
	}
	mu.Lock()
	n := len(seen)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("the proxy saw %d requests: %v", n, seen)
	}
	// host names and public literals still go to the proxy
	resp, err := c.Get("http://example.invalid/x")
	if err != nil {
		t.Fatalf("host name through the proxy: %v", err)
	}
	resp.Body.Close()
	resp, err = c.Get("http://v1.example.invalid./x")
	if err != nil {
		t.Fatalf("host name with a trailing dot through the proxy: %v", err)
	}
	resp.Body.Close()
	resp, err = c.Get("http://8.8.8.8/x")
	if err != nil {
		t.Fatalf("public literal through the proxy: %v", err)
	}
	resp.Body.Close()
}
