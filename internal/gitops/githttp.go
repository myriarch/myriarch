// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/myriarch/myriarch/internal/netguard"
)

var installMu sync.Mutex

// installGitHTTP makes go-git use base for http:// and https:// remotes. The
// client carries the outbound destination policy (dial check, timeouts) and
// the trust store of outbound.caFile; here redirects are limited to 3 hops and
// https, and a response body larger than maxBytes is cut off (a hostile server
// cannot stream an unbounded pack).
//
// extraCA (PEM, optional) is gitops.auth.caFile: it extends the trust store of
// the client. It is not handed to go-git as CABundle on purpose: go-git then
// clones the transport of the client, which only works for a plain
// *http.Transport, not for the size limit below, and it would replace the
// trust store (dropping outbound.caFile).
func installGitHTTP(base *http.Client, maxBytes int64, extraCA []byte) error {
	c := *base
	c.Timeout = 0 // the fetch context bounds the operation
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	if len(extraCA) > 0 {
		tr, ok := rt.(*http.Transport)
		if !ok {
			return errors.New("gitops.auth.caFile needs an HTTP client with a standard transport")
		}
		tr = tr.Clone()
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		pool, err := netguard.AppendRoots(tr.TLSClientConfig.RootCAs, extraCA)
		if err != nil {
			return fmt.Errorf("gitops.auth.caFile: %w", err)
		}
		tr.TLSClientConfig.RootCAs = pool
		rt = tr
	}
	c.Transport = &limitTransport{rt: rt, max: maxBytes}
	if base.CheckRedirect == nil {
		c.CheckRedirect = netguard.RedirectPolicy(3, nil)
	}
	installMu.Lock()
	defer installMu.Unlock()
	h := githttp.NewClient(&c)
	client.InstallProtocol("https", h)
	client.InstallProtocol("http", h)
	return nil
}

// errResponseTooLarge is returned when a Git response exceeds the limit.
var errResponseTooLarge = errors.New("git response exceeds gitops.maxRepoBytes")

type limitTransport struct {
	rt  http.RoundTripper
	max int64
}

func (t *limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(req)
	if err != nil || t.max <= 0 || resp.Body == nil {
		return resp, err
	}
	resp.Body = &limitedBody{rc: resp.Body, left: t.max}
	return resp, nil
}

type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errResponseTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }
