// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"golang.org/x/net/proxy"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/netguard"
)

// go-git's SSH transport dials through golang.org/x/net/proxy when the
// endpoint carries proxy options. A private URL scheme selects a dialer that
// applies the outbound destination policy to every connection attempt (the
// resolved address, not the host name), like the HTTP client does for
// https:// remotes.

const sshGuardScheme = brand.Name + "-netguard"

var (
	sshGuards   sync.Map // id -> *netguard.Policy
	sshGuardSeq atomic.Uint64
)

func init() {
	proxy.RegisterDialerType(sshGuardScheme, func(u *url.URL, _ proxy.Dialer) (proxy.Dialer, error) {
		p, ok := sshGuards.Load(u.Host)
		if !ok {
			return nil, errors.New("no outbound policy registered for the ssh connection")
		}
		return guardedDialer{p.(*netguard.Policy)}, nil
	})
}

type guardedDialer struct{ policy *netguard.Policy }

func (d guardedDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d guardedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.policy.DialContext(ctx, network, addr)
}

// sshGuardOptions returns the proxy options that route an SSH connection
// through policy.
func sshGuardOptions(policy *netguard.Policy) transport.ProxyOptions {
	id := "p" + strconv.FormatUint(sshGuardSeq.Add(1), 10)
	sshGuards.Store(id, policy)
	return transport.ProxyOptions{URL: sshGuardScheme + "://" + id}
}
