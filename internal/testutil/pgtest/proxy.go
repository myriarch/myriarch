// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pgtest

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// Proxy is a TCP proxy between a client and a PostgreSQL server that can stop
// forwarding without closing any connection. Both sides then see a peer that
// has gone silent without a FIN or RST, as after the hard failure of a node or
// a network partition. It listens on a free port of 127.0.0.1.
//
// The server side does not notice such a failure: the proxy's kernel still
// answers its keepalives. A test that needs the server to drop the session must
// end it itself (pg_terminate_backend) or use Close.
type Proxy struct {
	ln     net.Listener
	target string
	frozen atomic.Bool

	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	conns     []net.Conn
	wg        sync.WaitGroup
}

// StartProxy starts a proxy to target ("host:port") that is closed when the
// test ends.
func StartProxy(t testing.TB, target string) *Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pgtest: proxy listen: %v", err)
	}
	p := &Proxy{ln: ln, target: target, done: make(chan struct{})}
	p.wg.Add(1)
	go p.accept()
	t.Cleanup(p.Close)
	return p
}

// Addr returns the address clients connect to.
func (p *Proxy) Addr() *net.TCPAddr { return p.ln.Addr().(*net.TCPAddr) }

// Freeze stops forwarding in both directions of all current and future
// connections. Nothing is closed.
func (p *Proxy) Freeze() { p.frozen.Store(true) }

// Close closes the listener and all connections.
func (p *Proxy) Close() {
	p.closeOnce.Do(func() { close(p.done) })
	_ = p.ln.Close()
	p.mu.Lock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Proxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, up)
		p.mu.Unlock()
		p.wg.Add(2)
		go p.pipe(up, c)
		go p.pipe(c, up)
	}
}

// pipe copies src to dst. Once frozen it reads nothing more and forwards
// nothing; the connection stays open until Close.
func (p *Proxy) pipe(dst, src net.Conn) {
	defer p.wg.Done()
	buf := make([]byte, 32*1024)
	for {
		if p.frozen.Load() {
			<-p.done
			return
		}
		n, err := src.Read(buf)
		if n > 0 && !p.frozen.Load() { // what was read just before the freeze is swallowed
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
