// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import "testing"

func allowed(g *guard, addrs ...string) int {
	n := 0
	for _, a := range addrs {
		if g.allowIP(a) {
			n++
		}
	}
	return n
}

// The pre-authentication connect limit bounds one client, not one address spelling.
func TestConnectLimitIsKeyedPerIPv6PrefixAndUnmapsIPv4(t *testing.T) {
	// 1/s, burst 2
	g := newGuard(1)
	if n := allowed(g, "[2001:db8:1:2::1]:1000", "[2001:db8:1:2::2]:1000", "[2001:db8:1:2:aaaa::3]:1000", "[2001:db8:1:2::4]:1000"); n != 2 {
		t.Fatalf("four addresses of one /64 passed %d times, want 2", n)
	}
	// another /64 has its own bucket
	if n := allowed(g, "[2001:db8:1:3::1]:1000"); n != 1 {
		t.Fatalf("another /64 passed %d times, want 1", n)
	}
	g2 := newGuard(1)
	if n := allowed(g2, "192.0.2.10:1000", "[::ffff:192.0.2.10]:1000", "192.0.2.10", "::ffff:192.0.2.10"); n != 2 {
		t.Fatalf("one IPv4 address in two spellings passed %d times, want 2", n)
	}
}
