// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// A host can use every address of its /64 prefix, so all failure limiters
// count IPv6 clients per prefix (the bootstrap sign-in, the invalid bearer
// tokens and the dev login). IPv4 clients are counted per address.

// authenticateFrom authenticates a bearer token for a request that comes from
// the client address addr (host only).
func authenticateFrom(svc *authn.Service, addr, bearer string) error {
	r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.RemoteAddr = net.JoinHostPort(addr, "1234")
	r.Header.Set("Authorization", "Bearer "+bearer)
	_, err := svc.Authenticate(r)
	return err
}

// AUTH-BRUTE-1: invalid bearer tokens are delayed per IPv6 /64 prefix.
func TestInvalidBearerTokensAreCountedPerIPv6Prefix(t *testing.T) {
	e := newEnv(t, envOpts{})
	guess := func(i int) string { return "myra_" + strings.Repeat("x", 30) + fmt.Sprintf("%02d", i) }

	// six failures, each from another address of 2001:db8:1:2::/64: the grace count of five is used up
	var last string
	for i := 0; i < 6; i++ {
		last = fmt.Sprintf("2001:db8:1:2:aaaa:bbbb:cccc:%x", i+1)
		if err := authenticateFrom(e.svc, last, guess(i)); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("failure %d: %v", i, err)
		}
	}
	// the next address of the prefix is delayed, whatever its token
	err := authenticateFrom(e.svc, "2001:db8:1:2:1111:2222:3333:4444", guess(10))
	if !errors.Is(err, domain.ErrTooManyRequests) {
		t.Fatalf("another address of the same prefix: %v", err)
	}
	// other prefixes and IPv4 addresses are counted on their own
	for _, addr := range []string{"2001:db8:1:3::1", "192.0.2.77", "192.0.2.78"} {
		if err := authenticateFrom(e.svc, addr, guess(20)); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s: %v", addr, err)
		}
	}
	// the audit entry names the address of the request, not the prefix
	evs := e.findAudit("auth.token_failed")
	if len(evs) != 1 || evs[0].SourceIP != last {
		t.Fatalf("audit entries: %+v", evs)
	}
}

// AUTH-BRUTE-1: dev login attempts are limited per IPv6 /64 prefix.
func TestDevLoginIsRateLimitedPerIPv6Prefix(t *testing.T) {
	e := newEnv(t, envOpts{server: func(c *config.ServerConfig) { c.TrustedProxies = []string{"127.0.0.0/8"} }})
	from := func(addr string, user int) result {
		body := map[string]string{"username": fmt.Sprintf("guess%02d", user), "password": "wrong"} // a user name limit of its own each time
		return e.newClient().req("POST", "/api/v1/auth/dev-login", body, false, "X-Forwarded-For", addr)
	}

	// the burst of ten attempts, each from another address of 2001:db8:1:2::/64
	for i := 0; i < 10; i++ {
		if r := from(fmt.Sprintf("2001:db8:1:2:%x::1", i+1), i); r.Code != 401 {
			t.Fatalf("attempt %d: %d %s", i, r.Code, r.Body)
		}
	}
	r := from("2001:db8:1:2:ffff:ffff:ffff:ffff", 10)
	if r.Code != 429 || r.Header.Get("Retry-After") == "" || !strings.Contains(r.Problem().Detail, "client address") {
		t.Fatalf("the eleventh attempt from the prefix: %d %s", r.Code, r.Body)
	}
	// other prefixes and IPv4 addresses have their own limit
	for _, addr := range []string{"2001:db8:1:3::1", "192.0.2.77"} {
		if r := from(addr, 11); r.Code != 401 {
			t.Fatalf("%s: %d %s", addr, r.Code, r.Body)
		}
	}
}
