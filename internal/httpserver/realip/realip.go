// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package realip determines the real client address of a request behind
// trusted reverse proxies (config server.trustedProxies).
//
// Rules: forwarding headers (X-Forwarded-For, X-Forwarded-Proto) are honored
// ONLY if the direct peer is inside a trusted CIDR; X-Forwarded-For is walked
// from right to left and the first address that is not itself a trusted proxy
// is the client (so a client can not spoof an address by sending its own
// header). The direct peer address stays available through PeerIP.
package realip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type ctxKey int

const peerKey ctxKey = 1

// Resolver extracts client addresses.
type Resolver struct {
	trusted []*net.IPNet
}

// New returns a Resolver for the given trusted proxy networks.
func New(trusted []*net.IPNet) *Resolver { return &Resolver{trusted: trusted} }

// ParseTrusted parses CIDRs and bare IPs.
func ParseTrusted(cidrs []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			ip := net.ParseIP(s)
			if ip == nil {
				return nil, &net.ParseError{Type: "IP address", Text: s}
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// IsTrusted reports whether ip belongs to a trusted proxy network.
func (r *Resolver) IsTrusted(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// hostIP returns the IP of an address in "host:port" or bare form.
func hostIP(addr string) net.IP {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if i := strings.IndexByte(host, '%'); i >= 0 { // IPv6 zone
		host = host[:i]
	}
	return net.ParseIP(host)
}

// Middleware rewrites r.RemoteAddr to the client address ("ip:port" with the
// peer's port) and stores the original peer address in the context
// (PeerAddr). It also marks the request scheme in a header-independent way
// via the returned context only for trusted peers (not needed by handlers).
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		peer := req.RemoteAddr
		ctx := context.WithValue(req.Context(), peerKey, peer)
		if client := r.clientIP(req, peer); client != "" {
			port := ""
			if _, p, err := net.SplitHostPort(peer); err == nil {
				port = p
			}
			if port != "" {
				req.RemoteAddr = net.JoinHostPort(client, port)
			} else {
				req.RemoteAddr = client
			}
		}
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

// clientIP returns the forwarded client IP for trusted peers, "" otherwise.
func (r *Resolver) clientIP(req *http.Request, peer string) string {
	pip := hostIP(peer)
	if !r.IsTrusted(pip) {
		return ""
	}
	xff := req.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		if xr := strings.TrimSpace(req.Header.Get("X-Real-Ip")); xr != "" && net.ParseIP(xr) != nil {
			return xr
		}
		return ""
	}
	var chain []string
	for _, h := range xff {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				chain = append(chain, p)
			}
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		ip := hostIP(chain[i])
		if ip == nil {
			return "" // garbage in the chain: do not trust any of it
		}
		if !r.IsTrusted(ip) || i == 0 {
			return ip.String()
		}
	}
	return ""
}

// PeerAddr returns the direct peer address ("ip:port") of the connection,
// regardless of forwarding headers. Falls back to r.RemoteAddr.
func PeerAddr(r *http.Request) string {
	if v, ok := r.Context().Value(peerKey).(string); ok {
		return v
	}
	return r.RemoteAddr
}

// PeerIP returns the IP of the direct peer.
func PeerIP(r *http.Request) net.IP { return hostIP(PeerAddr(r)) }

// ClientIP returns the (already rewritten) client IP of the request as string.
func ClientIP(r *http.Request) string {
	if ip := hostIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

// IsTrustedPeer reports whether the direct peer of r is a trusted proxy.
func (r *Resolver) IsTrustedPeer(req *http.Request) bool { return r.IsTrusted(PeerIP(req)) }

// LimiterKey maps a client address (a host without port) to the key of its rate-limit bucket: IPv6
// sources share one bucket per /64 (a single host can use the whole prefix), IPv4 sources, including
// their IPv4-mapped IPv6 spelling, one per address. Text that is not an address is used unchanged.
func LimiterKey(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	ip = ip.Unmap()
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}
