// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
)

type ctxKey int

const infoKey ctxKey = 1

// reqInfo carries data produced by inner middleware to the access log.
type reqInfo struct {
	principal string
}

func infoFrom(ctx context.Context) *reqInfo {
	i, _ := ctx.Value(infoKey).(*reqInfo)
	return i
}

// statusWriter records status and size and passes Hijacker/Flusher through.
type statusWriter struct {
	http.ResponseWriter
	status   int
	bytes    int64
	wrote    bool
	hijacked bool
	dl       *deadlines
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	w.dl.refreshWrite()
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher (SSE).
func (w *statusWriter) Flush() {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		w.dl.refreshWrite()
		f.Flush()
	}
}

// Hijack implements http.Hijacker (WebSocket upgrade).
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("httpserver: underlying ResponseWriter does not support hijacking")
	}
	c, rw, err := h.Hijack()
	if err == nil {
		w.hijacked, w.wrote, w.status = true, true, http.StatusSwitchingProtocols
		// the connection is handed over (WebSocket): from here on the handler owns its deadlines
		w.dl.disable()
	}
	return c, rw, err
}

// Unwrap supports http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func validRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// metricMethod bounds the cardinality of the method label: clients can send
// arbitrary method names on routes that accept every method.
func metricMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// hstsApplies reports whether Strict-Transport-Security is sent: when TLS ends
// in myriarchd, or when the public URL is https (TLS ends at the ingress in front
// of myriarchd; browsers ignore the header on plain-http hops such as probes).
func (s *Server) hstsApplies(r *http.Request) bool {
	return r.TLS != nil || (s.publicURL != nil && s.publicURL.Scheme == "https")
}

// base wraps h with request id, real IP, access log, metrics and recovery.
func (s *Server) base(route string, level slog.Level, h http.Handler) http.Handler {
	observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, dl: deadlinesFrom(r.Context())}
		info := &reqInfo{}
		r = r.WithContext(context.WithValue(r.Context(), infoKey, info))

		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value of net/http
					panic(rec)
				}
				s.log.Error("panic in request handler", "panic", rec, "route", route, "method", r.Method, "path", r.URL.Path,
					"request_id", r.Header.Get(api.RequestIDHeader), "stack", string(debug.Stack()))
				if !sw.wrote {
					api.WriteProblem(sw, r, api.Problem{Status: http.StatusInternalServerError, Code: api.ErrorCodeInternal,
						Detail: "internal server error (request id " + r.Header.Get(api.RequestIDHeader) + ")"})
				}
			}
			status := sw.status
			if !sw.wrote {
				status = http.StatusOK
			}
			d := time.Since(start)
			method := metricMethod(r.Method)
			s.reqTotal.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
			s.reqDur.WithLabelValues(route, method).Observe(d.Seconds())
			lv := level
			if status >= 500 && lv < slog.LevelWarn {
				lv = slog.LevelWarn
			}
			if s.log.Enabled(r.Context(), lv) {
				attrs := []any{"method", r.Method, "path", r.URL.Path, "route", route, "status", status, "bytes", sw.bytes,
					"duration_ms", d.Milliseconds(), "remote", r.RemoteAddr, "request_id", r.Header.Get(api.RequestIDHeader)}
				if info.principal != "" {
					attrs = append(attrs, "principal", info.principal)
				}
				s.log.Log(r.Context(), lv, "http request", attrs...)
			}
		}()
		h.ServeHTTP(sw, r)
	})
	withID := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(api.RequestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewString()
			r.Header.Set(api.RequestIDHeader, id) // audit.FromRequest reads the request header
		}
		w.Header().Set(api.RequestIDHeader, id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if s.hstsApplies(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		observed.ServeHTTP(w, r)
	})
	return s.ip.Middleware(withID)
}
