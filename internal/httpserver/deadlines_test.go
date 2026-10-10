// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
)

func shortBudgets(t *testing.T) {
	t.Helper()
	oldBody, oldWrite := requestBodyBudget, responseWriteBudget
	requestBodyBudget, responseWriteBudget = 600*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() { requestBodyBudget, responseWriteBudget = oldBody, oldWrite })
}

// An incomplete request body must not hold a connection beyond the body budget.
func TestIncompleteRequestBodyEndsWithinTheBudget(t *testing.T) {
	shortBudgets(t)
	s, stop := startServer(t, config.ServerConfig{ShutdownTimeout: time.Second}, Deps{
		Ready: func(context.Context) error { return nil },
	})
	defer stop()
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_, _ = c.Write([]byte("POST /healthz HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\nabc"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadAll(c) // returns when the server closes the connection
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("the connection was still open after %s", time.Since(start))
	}
	if d := time.Since(start); d > requestBodyBudget+time.Second {
		t.Fatalf("connection held for %s, budget %s", d, requestBodyBudget)
	}
}

// A client that does not read a large response must not hold the handler.
func TestUnreadResponseEndsWithinTheWriteBudget(t *testing.T) {
	shortBudgets(t)
	returned := make(chan error, 1)
	s, stop := startServer(t, config.ServerConfig{ShutdownTimeout: time.Second}, Deps{
		Ready: func(context.Context) error { return nil },
		Licenses: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chunk := make([]byte, 1<<20)
			for i := 0; i < 512; i++ {
				if _, err := w.Write(chunk); err != nil {
					returned <- err
					return
				}
			}
			returned <- nil
		}),
	})
	defer stop()
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	_, _ = c.Write([]byte("GET /licenses/THIRD_PARTY_NOTICES HTTP/1.1\r\nHost: x\r\n\r\n"))
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("the handler wrote everything to a client that reads nothing")
		}
	case <-time.After(responseWriteBudget + 3*time.Second):
		t.Fatal("the handler is still blocked on the write")
	}
}

// Streams, WebSocket upgrades and slowly read downloads outlive the budgets.
func TestLongLivedResponsesOutliveTheBudgets(t *testing.T) {
	shortBudgets(t)
	upgrader := websocket.Upgrader{}
	var sseWrites, dlWrites atomic.Int32
	mods := []api.Module{api.ModuleFunc{ModuleName: "t", RoutesFunc: func() []api.Route {
		return []api.Route{
			{Method: "GET", Pattern: "/stream", OperationID: "stream", Permission: authz.PermRead, Streaming: true, Handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				rc := http.NewResponseController(w)
				for i := 0; i < 4; i++ {
					fmt.Fprint(w, "data: x\n\n")
					_ = rc.Flush()
					sseWrites.Add(1)
					time.Sleep(responseWriteBudget)
				}
			}},
			{Method: "GET", Pattern: "/download", OperationID: "download", Auth: api.AuthCustom, LongTransfer: true, Handler: func(w http.ResponseWriter, r *http.Request) {
				for i := 0; i < 6; i++ {
					if _, err := w.Write([]byte("0123456789")); err != nil {
						return
					}
					_ = http.NewResponseController(w).Flush()
					dlWrites.Add(1)
					time.Sleep(responseWriteBudget / 2)
				}
			}},
		}
	}}}
	s, stop := startServer(t, config.ServerConfig{ShutdownTimeout: time.Second}, Deps{
		Auth: newFakeAuth(), Modules: mods, OpAMPPath: "/v1/opamp",
		OpAMP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			time.Sleep(2 * responseWriteBudget)
			_ = conn.WriteMessage(websocket.TextMessage, []byte("late"))
		}),
		Ready: func(context.Context) error { return nil },
	})
	defer stop()
	base := s.Addr().String()

	// WebSocket
	wsc, _, err := websocket.DefaultDialer.Dial("ws://"+base+"/v1/opamp", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsc.Close()
	_ = wsc.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, msg, err := wsc.ReadMessage(); err != nil || string(msg) != "late" {
		t.Fatalf("websocket closed before the late message: %q %v", msg, err)
	}

	// slowly read download: the whole body arrives
	resp, err := http.Get("http://" + base + "/api/v1/download")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || len(b) != 60 {
		t.Fatalf("download: %d bytes, %v", len(b), err)
	}

	// SSE stream (authenticated)
	req, _ := http.NewRequest("GET", "http://"+base+"/api/v1/stream", nil)
	req.AddCookie(&http.Cookie{Name: "sess", Value: "v"})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	lines := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "data:") {
			lines++
		}
	}
	resp.Body.Close()
	if lines != 4 {
		t.Fatalf("stream delivered %d events, want 4", lines)
	}
}

// Concurrent readiness checks share one database round trip.
func TestReadyzIsCachedAndCoalesced(t *testing.T) {
	var calls atomic.Int32
	s, stop := startServer(t, config.ServerConfig{ShutdownTimeout: time.Second}, Deps{
		Ready: func(context.Context) error {
			calls.Add(1)
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	})
	defer stop()
	done := make(chan int, 50)
	for i := 0; i < 50; i++ {
		go func() {
			resp, err := http.Get("http://" + s.Addr().String() + "/readyz")
			if err != nil {
				done <- 0
				return
			}
			resp.Body.Close()
			done <- resp.StatusCode
		}()
	}
	for i := 0; i < 50; i++ {
		if code := <-done; code != 200 {
			t.Fatalf("readyz status %d", code)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Ready was called %d times, want 1", n)
	}
}

// Public routes that change state require a JSON Content-Type.
func TestPublicMutationRequiresJSONContentType(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s.Handler(), "POST", "/api/v1/public", nil, `{"a":1}`)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("no Content-Type: status %d, want 415 (%s)", rec.Code, rec.Body.String())
	}
	rec = do(t, s.Handler(), "POST", "/api/v1/public", map[string]string{"Content-Type": "text/plain"}, `{"a":1}`)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: status %d, want 415", rec.Code)
	}
	rec = do(t, s.Handler(), "POST", "/api/v1/public", map[string]string{"Content-Type": "application/json"}, `{"a":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("application/json: status %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// A request that is rejected before the connection is handed over (no valid credentials) keeps the
// read budget, even when it carries an Upgrade header or targets a streaming route: net/http discards
// the unread body of a rejected request, and that must not hold the connection without bound.
func TestRejectedRequestsOnUpgradeAndStreamRoutesEndWithinTheBudget(t *testing.T) {
	shortBudgets(t)
	mods := []api.Module{api.ModuleFunc{ModuleName: "t", RoutesFunc: func() []api.Route {
		return []api.Route{
			{Method: "GET", Pattern: "/stream", OperationID: "stream", Permission: authz.PermRead, Streaming: true, Handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: x\n\n")
			}},
		}
	}}}
	s, stop := startServer(t, config.ServerConfig{ShutdownTimeout: time.Second}, Deps{
		Auth: newFakeAuth(), Modules: mods, OpAMPPath: "/v1/opamp",
		OpAMP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// authentication failure before the upgrade: the body is not read
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}),
		Ready: func(context.Context) error { return nil },
	})
	defer stop()
	cases := map[string]string{
		"opamp with upgrade header":  "POST /v1/opamp HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nContent-Length: 1000\r\n\r\nabc",
		"opamp without upgrade":      "POST /v1/opamp HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\nabc",
		"stream without credentials": "GET /api/v1/stream HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\nabc",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := net.Dial("tcp", s.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			start := time.Now()
			_, _ = c.Write([]byte(raw))
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, err = io.ReadAll(c) // returns when the server closes the connection
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatalf("the connection was still open after %s", time.Since(start))
			}
			if d := time.Since(start); d > requestBodyBudget+time.Second {
				t.Fatalf("connection held for %s, budget %s", d, requestBodyBudget)
			}
		})
	}
}
