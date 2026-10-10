// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// watchdogMaxAge is how old the main loop's heartbeat may be before /healthz
// reports a hang.
const watchdogMaxAge = 30 * time.Second

// containerStatus feeds the probe endpoints of container mode.
type containerStatus struct {
	beat atomic.Int64 // unix nanos of the last heartbeat of the main loop
	// childRunning reports whether the supervisor child runs.
	childRunning func() bool
	// updateBusy reports whether an update is between stop and health check.
	updateBusy func() bool
	now        func() time.Time
}

func (s *containerStatus) heartbeat() { s.beat.Store(s.now().UnixNano()) }

func (s *containerStatus) alive() bool {
	last := s.beat.Load()
	return last != 0 && s.now().Sub(time.Unix(0, last)) <= watchdogMaxAge
}

// ready says whether the container is ready and, if not, why.
func (s *containerStatus) ready() (bool, string) {
	if !s.childRunning() {
		return false, "supervisor child is not running"
	}
	if s.updateBusy() {
		return false, "collector update in progress"
	}
	return true, "ok"
}

// handler serves GET /healthz (liveness) and GET /readyz (readiness).
func (s *containerStatus) handler() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, ok bool, msg string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = fmt.Fprintln(w, msg)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if s.alive() {
			reply(w, true, "ok")
			return
		}
		reply(w, false, "main loop is not running")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		ok, msg := s.ready()
		reply(w, ok, msg)
	})
	// Go's mux answers other methods on known paths with 405 and unknown
	// paths with 404.
	return mux
}

// serveHealth starts the probe server; it returns the bound address and a
// stop function.
func serveHealth(listen string, h http.Handler) (addr string, stop func(), err error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return "", nil, fmt.Errorf("listen on %s: %w", listen, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() { _ = srv.Close() }, nil
}
