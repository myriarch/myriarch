// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package live_test

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/live"
)

// revocable is the authenticator the hub re-runs for open streams.
type revocable struct {
	revoked atomic.Bool
	id      atomic.Value
	calls   atomic.Int64
}

func (r *revocable) Authenticate(*http.Request) (*authz.Principal, error) {
	r.calls.Add(1)
	if r.revoked.Load() {
		return nil, domain.ErrUnauthorized
	}
	id := "t"
	if v, ok := r.id.Load().(string); ok && v != "" {
		id = v
	}
	return &authz.Principal{Type: authz.PrincipalToken, ID: id, Name: "t", Role: domain.RoleViewer, AuthMethod: "api_token"}, nil
}

func waitClosed(t *testing.T, s *stream, d time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, ok := s.next(100 * time.Millisecond); !ok {
			select {
			case _, open := <-s.lines:
				if !open {
					return
				}
			default:
			}
		}
	}
	t.Fatalf("stream was not closed: %s", what)
}

// AUTH-SSE-1 / AC-20: a stream ends when its credential is no longer valid.
func TestStreamEndsWhenTheCredentialIsRevoked(t *testing.T) {
	h := newHarness(t)
	auth := &revocable{}
	h.hub.WithAuth(auth)
	s := h.connect("viewer")
	if _, ok := s.next(2 * time.Second); !ok {
		t.Fatal("stream did not start")
	}
	// valid: the stream keeps running through several ping intervals (and is re-validated each time)
	time.Sleep(900 * time.Millisecond)
	if auth.calls.Load() < 2 {
		t.Fatalf("the credential must be re-validated on every ping: %d checks", auth.calls.Load())
	}
	h.publish(eventbus.AgentRemoved, eventbus.AgentIDsPayload{AgentIDs: []domain.AgentID{newID()}})
	if _, ok := s.nextOf("agent.removed", 2*time.Second); !ok {
		t.Fatal("a valid stream must keep receiving events")
	}
	auth.revoked.Store(true)
	waitClosed(t, s, 2*time.Second, "revoked credential")
}

func TestStreamEndsImmediatelyOnAnInvalidationEvent(t *testing.T) {
	old := live.PingInterval
	live.PingInterval = time.Hour // only the event can close the stream
	defer func() { live.PingInterval = old }()
	h := newHarness(t)
	live.PingInterval = time.Hour
	auth := &revocable{}
	h.hub.WithAuth(auth)
	s := h.connect("viewer")
	if _, ok := s.next(2 * time.Second); !ok {
		t.Fatal("stream did not start")
	}
	// an invalidation of something that is still valid does not end the stream
	h.publish(eventbus.AuthInvalidate, eventbus.AuthInvalidatePayload{Kind: "user", ID: "x"})
	time.Sleep(600 * time.Millisecond)
	h.publish(eventbus.AgentRemoved, eventbus.AgentIDsPayload{AgentIDs: []domain.AgentID{newID()}})
	if _, ok := s.nextOf("agent.removed", 2*time.Second); !ok {
		t.Fatal("stream must survive an invalidation of a still valid credential")
	}
	auth.revoked.Store(true)
	h.publish(eventbus.AuthInvalidate, eventbus.AuthInvalidatePayload{Kind: "session", ID: "abc"})
	waitClosed(t, s, 3*time.Second, "invalidated credential")
}

func TestStreamEndsWhenThePrincipalChanges(t *testing.T) {
	h := newHarness(t)
	auth := &revocable{}
	auth.id.Store("t")
	h.hub.WithAuth(auth)
	s := h.connect("viewer")
	if _, ok := s.next(2 * time.Second); !ok {
		t.Fatal("stream did not start")
	}
	auth.id.Store("somebody-else") // the credential now belongs to another principal
	waitClosed(t, s, 2*time.Second, "principal change")
}

// AC-21: streams per principal are limited.
func TestStreamsPerPrincipalAreLimited(t *testing.T) {
	h := newHarness(t)
	var streams []*stream
	for i := 0; i < live.MaxStreamsPerPrincipal; i++ {
		s := h.connect("viewer")
		if _, ok := s.next(2 * time.Second); !ok {
			t.Fatalf("stream %d did not start", i)
		}
		streams = append(streams, s)
	}
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/v1/events/stream", nil)
	req.Header.Set("Authorization", "Bearer viewer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("the stream above the limit: %d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// closing one frees a slot
	streams[0].close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.DefaultClient.Do(req.Clone(req.Context()))
		if err != nil {
			t.Fatal(err)
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not released: %d", code)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStreamLifetimeIsLimited(t *testing.T) {
	old := live.MaxStreamLifetime
	live.MaxStreamLifetime = 400 * time.Millisecond
	defer func() { live.MaxStreamLifetime = old }()
	h := newHarness(t)
	s := h.connect("viewer")
	if _, ok := s.next(2 * time.Second); !ok {
		t.Fatal("stream did not start")
	}
	waitClosed(t, s, 3*time.Second, "lifetime")
}
