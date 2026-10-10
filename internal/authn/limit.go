// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"errors"
	"io"
	"net/http"
)

// maxIdPResponseBytes bounds every response of the identity provider (discovery document, key set, token response).
const maxIdPResponseBytes = 1 << 20

// errResponseTooLarge is returned when a response of the identity provider exceeds the limit.
var errResponseTooLarge = errors.New("identity provider response is too large (limit 1 MiB)")

// limitTransport bounds the size of the response bodies of the wrapped transport.
type limitTransport struct {
	rt  http.RoundTripper
	max int64
}

func (t *limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil || t.max <= 0 {
		return resp, err
	}
	if resp.ContentLength > t.max {
		_ = resp.Body.Close()
		return nil, errResponseTooLarge
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
		// the limit is used up: a clean end is fine, more data is an error
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			return 0, errResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }
