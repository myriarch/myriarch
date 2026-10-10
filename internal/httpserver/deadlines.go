// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Request deadlines (variables so that tests can shorten them).
var (
	// requestBodyBudget bounds the time a client gets to deliver the body of a request.
	requestBodyBudget = 60 * time.Second
	// responseWriteBudget is the idle write timeout: every write to the client extends the deadline.
	responseWriteBudget = 60 * time.Second
	// readyCacheTTL is the validity of a readiness result.
	readyCacheTTL = 2 * time.Second
)

// maxHeaderBytes bounds the request header of both listeners (net/http default: 1 MiB).
const maxHeaderBytes = 64 << 10

type deadlinesKey struct{}

// deadlines tracks the per-request socket deadlines. The http.Server has no global ReadTimeout or
// WriteTimeout (WebSocket and SSE connections are long lived), so each request gets a read budget
// for its body and an idle write budget; streaming routes and upgrades opt out.
type deadlines struct {
	rc  *http.ResponseController
	off atomic.Bool
}

func deadlinesFrom(ctx context.Context) *deadlines {
	d, _ := ctx.Value(deadlinesKey{}).(*deadlines)
	return d
}

// refreshWrite extends the write deadline (called before every write to the client).
func (d *deadlines) refreshWrite() {
	if d == nil || d.off.Load() {
		return
	}
	_ = d.rc.SetWriteDeadline(time.Now().Add(responseWriteBudget))
}

// disable removes the deadlines of the request once the connection is handed over (WebSocket
// upgrade) or the request is authenticated for a stream. The handler then manages the connection
// itself. It must not run before the request is accepted: a rejected request keeps its budgets.
func (d *deadlines) disable() {
	if d == nil {
		return
	}
	d.off.Store(true)
	_ = d.rc.SetReadDeadline(time.Time{})
	_ = d.rc.SetWriteDeadline(time.Time{})
}

// disableStream lifts the write budget of an accepted event stream. The read budget stays: only a
// request without a body has none, and a request that carries a body must still deliver it in time.
func (d *deadlines) disableStream() {
	if d == nil {
		return
	}
	d.off.Store(true)
	_ = d.rc.SetWriteDeadline(time.Time{})
}

// clearRead removes the read deadline once the request body is complete: a deadline that fires while
// the handler still runs would make the server abort the request context.
func (d *deadlines) clearRead() {
	_ = d.rc.SetReadDeadline(time.Time{})
}

// deadlineBody clears the read deadline when the body ends.
type deadlineBody struct {
	io.ReadCloser
	d *deadlines
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.d.clearRead()
	}
	return n, err
}

// withDeadlines applies the read budget of the body and the idle write budget to every request.
func withDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := &deadlines{rc: http.NewResponseController(w)}
		if r.Body != nil && r.Body != http.NoBody {
			_ = d.rc.SetReadDeadline(time.Now().Add(requestBodyBudget))
			r.Body = &deadlineBody{ReadCloser: r.Body, d: d}
		}
		d.refreshWrite()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deadlinesKey{}, d)))
	})
}
