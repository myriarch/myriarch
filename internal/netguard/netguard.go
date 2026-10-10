// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package netguard enforces the destination policy of all outbound HTTP(S)
// connections of myriarchd (executors, notifier, release tracker, artifact
// upstream, OIDC, Git over HTTPS).
//
// The policy is applied to the resolved IP address in net.Dialer.Control, which
// runs for every single connection attempt: redirects and repeated DNS
// resolution are therefore covered as well, and a host name can never be
// turned into a forbidden address between a check and the connection. Denied
// by default are loopback, private (RFC 1918), shared (CGNAT), link-local
// (incl. the cloud metadata address), unique-local, multicast, reserved and
// unspecified ranges, in IPv4 and IPv6 notation (IPv4-mapped and NAT64 forms
// are checked as the IPv4 address they carry). Operators open specific ranges
// with outbound.allowedCIDRs.
//
// Connections through a forward proxy (explicit or from the environment) are
// made to the proxy, whose exact host and port are exempt from the check in
// the transport that uses it (the proxy is trusted infrastructure that applies
// its own egress policy). Address literals and localhost in the request target
// are still refused locally; host names are left to the proxy.
package netguard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Timeouts of the hardened transport (OUT-3).
const (
	DialTimeout           = 10 * time.Second
	TLSHandshakeTimeout   = 10 * time.Second
	ResponseHeaderTimeout = 30 * time.Second
	ExpectContinueTimeout = 1 * time.Second
	IdleConnTimeout       = 90 * time.Second
	MaxResponseHeaderSize = 64 << 10
	// MaxRedirects is the default hop limit of RedirectPolicy.
	MaxRedirects = 5
)

// ErrDenied is returned (wrapped) for connections to forbidden destinations.
var ErrDenied = errors.New("destination address is not allowed by the outbound policy")

var denyRanges = mustPrefixes(
	"0.0.0.0/8",       // "this" network, unspecified
	"10.0.0.0/8",      // RFC 1918
	"100.64.0.0/10",   // shared address space (CGNAT)
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, cloud metadata
	"172.16.0.0/12",   // RFC 1918
	"192.0.0.0/24",    // IETF protocol assignments
	"192.168.0.0/16",  // RFC 1918
	"198.18.0.0/15",   // benchmarking
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, broadcast
	"::/128",          // unspecified
	"::1/128",         // loopback
	"fc00::/7",        // unique local
	"fe80::/10",       // link-local
	"ff00::/8",        // multicast
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4 (embeds an arbitrary IPv4 address)
	"64:ff9b:1::/48",  // local-use NAT64
	"::/96",           // IPv4-compatible IPv6 (deprecated, embeds an IPv4 address)
	"2001::/32",       // Teredo (embeds an IPv4 address)
	"100::/64",        // discard prefix
	"192.88.99.0/24",  // deprecated 6to4 relay anycast
	"192.0.2.0/24",    // documentation (TEST-NET-1)
	"198.51.100.0/24", // documentation (TEST-NET-2)
	"203.0.113.0/24",  // documentation (TEST-NET-3)
)

var (
	nat64Prefix  = netip.MustParsePrefix("64:ff9b::/96")
	compatPrefix = netip.MustParsePrefix("::/96")
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// Policy is the destination policy. The zero value denies all reserved
// ranges; nil is not valid (use New or Permissive).
type Policy struct {
	allowed    []netip.Prefix
	permissive bool
	mu         sync.RWMutex
	// roots is the trust store of every TLS connection made through the
	// policy (nil = the system roots of the platform).
	roots *x509.CertPool
}

// New creates a policy that denies the reserved ranges except those covered
// by allowedCIDRs (CIDRs or single IPs).
func New(allowedCIDRs []string) (*Policy, error) {
	p := &Policy{}
	for _, c := range allowedCIDRs {
		pf, err := ParsePrefix(c)
		if err != nil {
			return nil, err
		}
		p.allowed = append(p.allowed, pf)
	}
	return p, nil
}

// Permissive returns a policy that allows every destination (dev mode and
// tests only).
func Permissive() *Policy { return &Policy{permissive: true} }

// RootPool returns the trust store for outbound TLS connections: the system
// roots plus the certificates of the PEM file caFile (outbound.caFile, an
// internal CA). An empty caFile returns nil (system roots only). The file must
// be readable and contain at least one certificate.
func RootPool(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, nil
	}
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool, err := AppendRoots(nil, pemBytes)
	if err != nil {
		return nil, fmt.Errorf("CA file %s: %w", caFile, err)
	}
	return pool, nil
}

// AppendRoots returns a pool with the certificates of pemBytes added to base
// (a copy; nil = the system roots). It fails if pemBytes holds no certificate.
func AppendRoots(base *x509.CertPool, pemBytes []byte) (*x509.CertPool, error) {
	var pool *x509.CertPool
	if base != nil {
		pool = base.Clone()
	} else if sys, err := x509.SystemCertPool(); err == nil && sys != nil {
		pool = sys
	} else {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("contains no PEM certificate")
	}
	return pool, nil
}

// SetRootCAs sets the trust store of the TLS connections of every transport
// that is created by the policy afterwards (outbound.caFile): OIDC, Git over
// HTTPS, GitHub, the artifact mirror, Sigstore TUF, executors, notifications.
// nil restores the system roots.
func (p *Policy) SetRootCAs(pool *x509.CertPool) {
	p.mu.Lock()
	p.roots = pool
	p.mu.Unlock()
}

// RootCAs returns the trust store set by SetRootCAs (nil = system roots).
func (p *Policy) RootCAs() *x509.CertPool {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.roots
}

// ParsePrefix parses a CIDR or a single IP address.
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid CIDR %q", s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid IP or CIDR %q", s)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Permissive reports whether the policy allows everything.
func (p *Policy) Permissive() bool { return p == nil || p.permissive }

// CheckAddr checks one resolved address.
func (p *Policy) CheckAddr(a netip.Addr) error {
	if p.Permissive() {
		return nil
	}
	a = a.Unmap().WithZone("")
	// embedded IPv4 addresses: NAT64 (64:ff9b::/96) and IPv4-compatible IPv6 (::/96)
	if a.Is6() {
		b := a.As16()
		if nat64Prefix.Contains(a) || compatPrefix.Contains(a) {
			if err := p.CheckAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})); err != nil {
				return err
			}
		}
	}
	for _, al := range p.allowed {
		if al.Contains(a) {
			return nil
		}
	}
	for _, d := range denyRanges {
		if d.Contains(a) {
			return fmt.Errorf("%w: %s", ErrDenied, a)
		}
	}
	return nil
}

// Control is a net.Dialer.Control function checking the resolved address of
// every connection attempt.
func (p *Policy) Control(network, address string, _ syscall.RawConn) error {
	if p.Permissive() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparsable address %q", ErrDenied, address)
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: unparsable address %q", ErrDenied, address)
	}
	return p.CheckAddr(a)
}

// dialContext returns the dial function of the guarded transport. proxyAddrs are the exact "host:port"
// addresses of the forward proxies of that transport: only the connection to such an address skips the
// check (the proxy is trusted infrastructure that applies its own egress policy).
func (p *Policy) dialContext(proxyAddrs ...string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	guarded := &net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second, Control: p.Control}
	plain := &net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}
	exempt := make(map[string]bool, len(proxyAddrs))
	for _, a := range proxyAddrs {
		exempt[strings.ToLower(a)] = true
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if exempt[strings.ToLower(addr)] {
			return plain.DialContext(ctx, network, addr)
		}
		return guarded.DialContext(ctx, network, addr)
	}
}

// DialContext dials addr through the guarded dialer: the resolved address of
// every connection attempt is checked against the policy.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return p.dialContext()(ctx, network, addr)
}

// Transport returns a hardened *http.Transport: guarded dialer (every
// connection), dial/TLS/response-header timeouts and a header size limit.
// Proxy settings of the environment (HTTP_PROXY, HTTPS_PROXY, NO_PROXY) are
// honored; the connection to such a proxy (exact host and port) is exempt from
// the address check, in this transport only. ALL_PROXY is not used by net/http
// and exempts nothing.
func (p *Policy) Transport() *http.Transport {
	return p.transport(http.ProxyFromEnvironment, proxyAddrsFromEnv())
}

// TransportWithProxy is Transport with an explicit forward proxy.
func (p *Policy) TransportWithProxy(proxy *url.URL) *http.Transport {
	if proxy == nil {
		return p.Transport()
	}
	return p.transport(http.ProxyURL(proxy), []string{proxyAddr(proxy)})
}

func (p *Policy) transport(proxy func(*http.Request) (*url.URL, error), proxyAddrs []string) *http.Transport {
	return &http.Transport{
		Proxy:       p.guardProxy(proxy),
		DialContext: p.dialContext(proxyAddrs...),
		// the trust store applies to every connection of the transport (also
		// to the TLS to an HTTPS proxy); HTTP/2 stays enabled although the TLS
		// configuration is set explicitly
		TLSClientConfig:        &tls.Config{RootCAs: p.RootCAs(), MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    10,
		IdleConnTimeout:        IdleConnTimeout,
		TLSHandshakeTimeout:    TLSHandshakeTimeout,
		ResponseHeaderTimeout:  ResponseHeaderTimeout,
		ExpectContinueTimeout:  ExpectContinueTimeout,
		MaxResponseHeaderBytes: MaxResponseHeaderSize,
	}
}

// Client returns an http.Client with the guarded transport and the given
// overall timeout (0 = none; the caller sets per-request deadlines). Redirects
// follow RedirectPolicy(MaxRedirects, nil).
func (p *Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: p.Transport(), Timeout: timeout, CheckRedirect: RedirectPolicy(MaxRedirects, nil)}
}

// guardProxy wraps a Proxy function: when a proxy applies to a request, the destination is no longer
// checked by the dialer (the proxy makes that connection), so address literals in denied ranges and
// localhost are refused here, before the request is handed to the proxy. Host names are left to the
// egress policy of the proxy.
func (p *Policy) guardProxy(proxy func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(r *http.Request) (*url.URL, error) {
		u, err := proxy(r)
		if err != nil || u == nil || p.Permissive() {
			return u, err
		}
		host := strings.TrimSuffix(r.URL.Hostname(), ".")
		if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
			if cerr := p.CheckAddr(netip.MustParseAddr("127.0.0.1")); cerr != nil {
				return nil, cerr
			}
			return u, nil
		}
		if a, perr := netip.ParseAddr(host); perr == nil {
			if cerr := p.CheckAddr(a); cerr != nil {
				return nil, cerr
			}
		} else if numericHost(host) {
			// decimal, octal, hexadecimal and shortened IPv4 spellings are not canonical literals: the
			// proxy could resolve them to an address that was never checked
			return nil, fmt.Errorf("%w: non-canonical numeric host %q", ErrDenied, host)
		}
		return u, nil
	}
}

// numericHost reports whether a host name ends in a number (all digits or a 0x hexadecimal number),
// the form of the non-canonical IPv4 spellings.
func numericHost(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if last == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") || strings.HasPrefix(last, "0X") {
		last = last[2:]
		if last == "" {
			return true
		}
		for _, c := range last {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
		return true
	}
	for _, c := range last {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// proxyAddr returns the "host:port" address that net/http dials for a proxy URL.
func proxyAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// proxyAddrsFromEnv returns the proxy addresses of the proxy variables that net/http uses.
func proxyAddrsFromEnv() []string {
	var out []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		v := getenv(k)
		if v == "" {
			continue
		}
		if !strings.Contains(v, "://") {
			v = "http://" + v
		}
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			out = append(out, proxyAddr(u))
		}
	}
	return out
}

// RedirectPolicy limits redirects: at most maxHops, https targets only (an
// https request is never downgraded) and, if hosts is non-empty, only to
// listed hosts (exact match or "*.example.com" suffix). It returns the
// CheckRedirect function of an http.Client.
func RedirectPolicy(maxHops int, hosts []string) func(*http.Request, []*http.Request) error {
	if maxHops <= 0 {
		maxHops = MaxRedirects
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHops {
			return fmt.Errorf("stopped after %d redirects", maxHops)
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to %s://%s refused: only https targets are followed", req.URL.Scheme, req.URL.Host)
		}
		if len(hosts) > 0 && !HostListed(hosts, req.URL.Hostname()) {
			return fmt.Errorf("redirect to host %s refused: not in the list of allowed hosts", req.URL.Hostname())
		}
		return nil
	}
}

// HostListed matches host against exact entries and "*.suffix" entries
// (case-insensitive).
func HostListed(list []string, host string) bool {
	host = strings.ToLower(host)
	for _, a := range list {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case a == "":
		case a == host:
			return true
		case strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:]) && len(host) > len(a)-1:
			return true
		}
	}
	return false
}
