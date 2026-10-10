// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package live_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/httpserver"
	"github.com/myriarch/myriarch/internal/live"
	"github.com/myriarch/myriarch/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakes ---------------------------------------------------------------------

type fakeAgents struct {
	store.AgentStore
	mu       sync.Mutex
	calls    [][]domain.AgentID
	missing  map[domain.AgentID]bool
	fail     bool
	hostname func(id domain.AgentID) string
}

func (f *fakeAgents) GetMany(_ context.Context, ids []domain.AgentID) ([]domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]domain.AgentID(nil), ids...))
	if f.fail {
		return nil, fmt.Errorf("database down")
	}
	var out []domain.Agent
	for _, id := range ids {
		if f.missing[id] {
			continue
		}
		host := "host-" + id.String()[:8]
		if f.hostname != nil {
			host = f.hostname(id)
		}
		out = append(out, domain.Agent{ID: id, Kind: domain.AgentKindCollector, Hostname: host, Version: "0.161.0",
			Connection: domain.AgentConnection{Status: domain.ConnectionOnline}, Labels: map[string]string{}, IdentifyingAttributes: map[string]string{},
			NonIdentifyingAttributes: map[string]string{"site.id": "1001"}})
	}
	return out, nil
}

func (f *fakeAgents) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeReleases struct{ store.ReleaseStore }

func (fakeReleases) Latest(context.Context, string, bool) (*domain.Release, error) {
	return &domain.Release{Distribution: domain.DistributionContrib, Version: "0.162.0"}, nil
}
func (fakeReleases) Get(_ context.Context, dist, v string) (*domain.Release, error) {
	if v == "9.9.9" {
		return nil, domain.ErrNotFound
	}
	return &domain.Release{Distribution: dist, Version: v, Tag: "v" + v, BreakingChanges: []domain.ChangelogEntry{{Text: "x"}}}, nil
}

type fakeRollouts struct{ store.RolloutStore }

func (fakeRollouts) Get(_ context.Context, id uuid.UUID) (*domain.Rollout, error) {
	return &domain.Rollout{ID: id, Kind: domain.RolloutKindConfig, FleetName: "edge-nl", Status: domain.RolloutRunning, Counts: domain.RolloutCounts{Total: 3, InProgress: 1}, CreatedBy: "gitops",
		Waves: []domain.RolloutWave{{Index: 0}, {Index: 1}}}, nil
}

type fakeGitOps struct{ store.GitOpsStore }

func (fakeGitOps) GetStatus(context.Context) (*domain.GitOpsSyncStatus, error) {
	return &domain.GitOpsSyncStatus{State: "ok", AppliedCommit: &domain.GitCommit{SHA: "abc123"}, Issues: domain.Issues{{Message: "m"}}}, nil
}
func (fakeGitOps) ListFleets(context.Context) ([]domain.Fleet, error) { return nil, nil }

type fakeStore struct {
	store.Store
	agents *fakeAgents
}

func (s fakeStore) Agents() store.AgentStore     { return s.agents }
func (s fakeStore) Releases() store.ReleaseStore { return fakeReleases{} }
func (s fakeStore) Rollouts() store.RolloutStore { return fakeRollouts{} }
func (s fakeStore) GitOps() store.GitOpsStore    { return fakeGitOps{} }

// Identity: no accounts (actor references are shown as they are).
func (s fakeStore) Identity() store.IdentityStore { return nil }

func (s fakeStore) FleetVersions() store.FleetVersionStore { return fakeFleetVersions{} }

type fakeFleetVersions struct{ store.FleetVersionStore }

func (fakeFleetVersions) List(context.Context) ([]domain.FleetVersionSelection, error) {
	return nil, nil
}

// harness -------------------------------------------------------------------

type fakeAuth struct{}

func (fakeAuth) Authenticate(r *http.Request) (*authz.Principal, error) {
	if r.Header.Get("Authorization") == "Bearer viewer" {
		return &authz.Principal{Type: authz.PrincipalToken, ID: "t", Name: "t", Role: domain.RoleViewer, AuthMethod: "api_token"}, nil
	}
	return nil, fmt.Errorf("%w: no credentials", domain.ErrUnauthorized)
}
func (fakeAuth) CheckCSRF(*http.Request, *authz.Principal) error { return nil }

type harness struct {
	t      *testing.T
	hub    *live.Hub
	bus    eventbus.Bus
	agents *fakeAgents
	srv    *httptest.Server
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	oldC, oldS, oldP := live.CoalesceInterval, live.StatsInterval, live.PingInterval
	live.CoalesceInterval, live.StatsInterval, live.PingInterval = 100*time.Millisecond, 400*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { live.CoalesceInterval, live.StatsInterval, live.PingInterval = oldC, oldS, oldP })

	h := &harness{t: t, bus: eventbus.NewMemory("r1", 0), agents: &fakeAgents{missing: map[domain.AgentID]bool{}}}
	h.hub = live.New(h.bus, fakeStore{agents: h.agents}, quiet)
	hs, err := httpserver.New(config.ServerConfig{ListenAddr: ":0"}, httpserver.Deps{Auth: fakeAuth{}, Modules: []api.Module{h.hub.Module()}, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(hs.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan struct{})
	go func() { defer close(h.done); _ = h.hub.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-h.done
		h.srv.CloseClientConnections()
		h.srv.Close()
		_ = h.bus.Close()
	})
	return h
}

// stream is an SSE client.
type stream struct {
	t     *testing.T
	resp  *http.Response
	lines chan string
	close func()
}

func (h *harness) connect(auth string) *stream {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", h.srv.URL+"/api/v1/events/stream", nil)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	s := &stream{t: h.t, resp: resp, lines: make(chan string, 4096), close: func() { cancel(); resp.Body.Close() }}
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
	}()
	h.t.Cleanup(s.close)
	return s
}

type sseEvent struct {
	Event, ID, Data string
}

// next returns the next event (skipping comments); comments are returned as Event "comment".
func (s *stream) next(d time.Duration) (sseEvent, bool) {
	s.t.Helper()
	var ev sseEvent
	deadline := time.After(d)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				return ev, false
			}
			switch {
			case strings.HasPrefix(line, ":"):
				return sseEvent{Event: "comment", Data: strings.TrimSpace(strings.TrimPrefix(line, ":"))}, true
			case strings.HasPrefix(line, "event: "):
				ev.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if ev.Event != "" {
					return ev, true
				}
			}
		case <-deadline:
			return ev, false
		}
	}
}

// nextOf skips events of other types and comments.
func (s *stream) nextOf(typ string, d time.Duration) (sseEvent, bool) {
	s.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ev, ok := s.next(time.Until(deadline))
		if !ok {
			return ev, false
		}
		if ev.Event == typ {
			return ev, true
		}
	}
	return sseEvent{}, false
}

func (h *harness) publish(typ eventbus.Type, payload any) {
	h.t.Helper()
	if err := h.bus.Publish(context.Background(), typ, payload); err != nil {
		h.t.Fatal(err)
	}
}

func newID() domain.AgentID { return domain.AgentID(uuid.Must(uuid.NewV7())) }

// tests ---------------------------------------------------------------------

func TestStreamHeadersAuthAndPing(t *testing.T) {
	h := newHarness(t)
	// anonymous: 401 problem (RBAC is enforced before the stream starts)
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/v1/events/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous stream: %d", resp.StatusCode)
	}

	s := h.connect("viewer")
	if s.resp.StatusCode != 200 {
		t.Fatalf("status %d", s.resp.StatusCode)
	}
	for k, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := s.resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	ev, ok := s.next(2 * time.Second)
	if !ok || ev.Event != "comment" {
		t.Fatalf("first line must be a comment: %+v", ev)
	}
	// keep-alive pings (": ping")
	var pings int
	for i := 0; i < 2; i++ {
		ev, ok := s.next(2 * time.Second)
		if !ok || ev.Event != "comment" || ev.Data != "ping" {
			t.Fatalf("expected ': ping', got %+v ok=%v", ev, ok)
		}
		pings++
	}
	if h.hub.Clients() != 1 {
		t.Fatalf("clients = %d", h.hub.Clients())
	}
	s.close()
	deadline := time.Now().Add(3 * time.Second)
	for h.hub.Clients() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("client not removed after disconnect")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentUpdatesAreCoalescedAndWellFormed(t *testing.T) {
	h := newHarness(t)
	s := h.connect("viewer")
	s.next(time.Second) // ": connected"
	a, b := newID(), newID()

	// 20 changes of agent a and 1 of agent b within one coalescing window
	for i := 0; i < 20; i++ {
		h.publish(eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: a, Changes: []string{"health"}, Online: true})
	}
	h.publish(eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: b, Changes: []string{"connection"}})
	h.publish(eventbus.AgentDesiredChanged, eventbus.AgentIDsPayload{AgentIDs: []domain.AgentID{a}})
	h.publish(eventbus.AssignmentChanged, eventbus.AgentIDsPayload{AgentIDs: []domain.AgentID{b}})

	got := map[domain.AgentID]int{}
	var lastID uint64
	for i := 0; i < 2; i++ {
		ev, ok := s.nextOf(dto.LiveAgentUpdated, 3*time.Second)
		if !ok {
			t.Fatalf("expected 2 agent.updated events, got %d", len(got))
		}
		var sum dto.AgentSummary
		if err := json.Unmarshal([]byte(ev.Data), &sum); err != nil {
			t.Fatalf("data is not an AgentSummary: %v (%s)", err, ev.Data)
		}
		got[sum.ID]++
		if sum.Hostname == "" || sum.Version != "0.161.0" || !sum.Outdated || sum.Site["site.id"] != "1001" {
			t.Fatalf("summary: %+v", sum)
		}
		var id uint64
		fmt.Sscan(ev.ID, &id)
		if id <= lastID {
			t.Fatalf("event ids must increase monotonically: %d after %d", id, lastID)
		}
		lastID = id
	}
	if got[a] != 1 || got[b] != 1 {
		t.Fatalf("each agent exactly once per window: %v", got)
	}
	if h.agents.callCount() != 1 {
		t.Fatalf("one batched GetMany per window, got %d", h.agents.callCount())
	}
	// stats.changed hint follows (max 1 per interval)
	if _, ok := s.nextOf(dto.LiveStatsChanged, 2*time.Second); !ok {
		t.Fatal("stats.changed expected after agent changes")
	}
	if ev, ok := s.nextOf(dto.LiveAgentUpdated, 400*time.Millisecond); ok {
		t.Fatalf("no further agent.updated expected: %+v", ev)
	}
}

func TestAgentRemovedRolloutGitOpsReleaseEvents(t *testing.T) {
	h := newHarness(t)
	s := h.connect("viewer")
	s.next(time.Second)
	id := newID()

	// removal is immediate and wins over a pending update of the same agent
	h.publish(eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: id})
	h.publish(eventbus.AgentRemoved, eventbus.AgentIDsPayload{AgentIDs: []domain.AgentID{id}})
	ev, ok := s.nextOf(dto.LiveAgentRemoved, 2*time.Second)
	if !ok {
		t.Fatal("agent.removed expected")
	}
	var rem dto.LiveAgentRemovedData
	_ = json.Unmarshal([]byte(ev.Data), &rem)
	if rem.ID != id {
		t.Fatalf("removed id: %+v", rem)
	}
	if ev, ok := s.nextOf(dto.LiveAgentUpdated, 500*time.Millisecond); ok {
		t.Fatalf("a removed agent must not be announced as updated: %+v", ev)
	}

	rid := uuid.New()
	h.publish(eventbus.RolloutChanged, eventbus.RolloutChangedPayload{RolloutID: rid, FleetName: "edge-nl", Status: domain.RolloutRunning})
	ev, ok = s.nextOf(dto.LiveRolloutUpdated, 2*time.Second)
	if !ok {
		t.Fatal("rollout.updated expected")
	}
	var ro dto.RolloutSummary
	_ = json.Unmarshal([]byte(ev.Data), &ro)
	if ro.ID != rid || ro.FleetName != "edge-nl" || ro.WaveCount != 2 || ro.Counts.InProgress != 1 {
		t.Fatalf("rollout summary: %+v", ro)
	}
	if ro.Origin != domain.OriginGitOps || ro.CreatedBy != "gitops" {
		t.Fatalf("origin and creator of the summary: %+v", ro)
	}

	h.publish(eventbus.GitOpsSynced, eventbus.GitOpsSyncedPayload{Commit: "abc123", Result: "applied"})
	ev, ok = s.nextOf(dto.LiveGitOpsSynced, 2*time.Second)
	if !ok {
		t.Fatal("gitops.synced expected")
	}
	var brief dto.GitOpsBrief
	_ = json.Unmarshal([]byte(ev.Data), &brief)
	if brief.State != "ok" || brief.AppliedCommit != "abc123" || brief.IssueCount != 1 {
		t.Fatalf("gitops brief: %+v", brief)
	}

	h.publish(eventbus.ReleaseDetected, eventbus.ReleaseDetectedPayload{Distribution: domain.DistributionContrib, Version: "0.163.0"})
	ev, ok = s.nextOf(dto.LiveReleaseNew, 2*time.Second)
	if !ok {
		t.Fatal("release.new expected")
	}
	var rel dto.ReleaseSummary
	_ = json.Unmarshal([]byte(ev.Data), &rel)
	if rel.Version != "0.163.0" || rel.BreakingCount != 1 {
		t.Fatalf("release summary: %+v", rel)
	}
	// unknown releases are ignored silently
	h.publish(eventbus.ReleaseDetected, eventbus.ReleaseDetectedPayload{Distribution: domain.DistributionContrib, Version: "9.9.9"})
	// artifact changes produce no SSE event in the MVP
	h.publish(eventbus.ArtifactChanged, eventbus.ArtifactChangedPayload{ArtifactID: uuid.New()})
	if ev, ok := s.nextOf(dto.LiveReleaseNew, 500*time.Millisecond); ok {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestResyncOnBusReconnectAndLoadFailure(t *testing.T) {
	h := newHarness(t)
	s := h.connect("viewer")
	s.next(time.Second)
	h.publish(eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: newID()})
	h.agents.mu.Lock()
	h.agents.fail = true
	h.agents.mu.Unlock()
	if _, ok := s.nextOf(dto.LiveResync, 3*time.Second); !ok {
		t.Fatal("clients must be told to resync when the data cannot be loaded")
	}
	h.agents.mu.Lock()
	h.agents.fail = false
	h.agents.mu.Unlock()

	// the local-only bus.reconnected event (listener reconnected, events may be lost) -> resync
	h.publish(eventbus.BusReconnected, struct{}{})
	if _, ok := s.nextOf(dto.LiveResync, 3*time.Second); !ok {
		t.Fatal("resync expected after bus.reconnected")
	}
}

func TestSlowClientOverflowReceivesResyncAndBufferIsCleared(t *testing.T) {
	h := newHarness(t)
	h.agents.hostname = func(domain.AgentID) string { return strings.Repeat("h", 8192) } // big frames fill the TCP buffers quickly
	// a client that does not read: open the connection, then stall
	conn, err := (&net_dialer{}).dial(h.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /api/v1/events/stream HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer viewer\r\n\r\n")
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("status line %q", line)
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.hub.Clients() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("client not registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 700 distinct agents change in one window: 700 frames > buffer (256) -> overflow handling
	// (the unread socket buffers a few frames; the hub's own channel overflows first because the
	// writer goroutine blocks on a full TCP window once we stop reading)
	for i := 0; i < 20; i++ {
		ids := make([]domain.AgentID, 0, 100)
		for j := 0; j < 100; j++ {
			ids = append(ids, newID())
		}
		for _, id := range ids {
			h.publish(eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: id})
		}
		time.Sleep(150 * time.Millisecond)
	}
	// now drain what the server wrote: a resync frame must be present
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var sawResync bool
	var agentFrames int
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if l == "event: resync" {
			sawResync = true
		}
		if l == "event: agent.updated" {
			agentFrames++
		}
		if sawResync && agentFrames > 0 && strings.HasPrefix(l, "data: {}") {
			break
		}
	}
	if !sawResync {
		t.Fatalf("a client that cannot keep up must receive resync (agent frames seen: %d)", agentFrames)
	}
}

func TestStreamsEndWhenHubStops(t *testing.T) {
	h := newHarness(t)
	s := h.connect("viewer")
	s.next(time.Second)
	h.cancel()
	<-h.done
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-s.lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("SSE streams must terminate when the hub stops (graceful shutdown)")
		}
	}
}

// fleet.updated: one event per fleet and coalescing window, readable by viewers.
func TestFleetUpdatedIsCoalescedPerFleet(t *testing.T) {
	h := newHarness(t)
	s := h.connect("viewer")
	s.next(time.Second) // ": connected"
	for i := 0; i < 10; i++ {
		h.publish(eventbus.FleetVersionChanged, eventbus.FleetVersionChangedPayload{FleetName: "k8s-gateway"})
	}
	h.publish(eventbus.FleetVersionChanged, eventbus.FleetVersionChangedPayload{FleetName: "edge-nl"})
	h.publish(eventbus.FleetVersionChanged, eventbus.FleetVersionChangedPayload{}) // no fleet: ignored

	got := map[string]int{}
	for i := 0; i < 2; i++ {
		ev, ok := s.nextOf(dto.LiveFleetUpdated, 3*time.Second)
		if !ok {
			t.Fatalf("expected fleet.updated for two fleets, got %v", got)
		}
		var d dto.LiveFleetUpdatedData
		if err := json.Unmarshal([]byte(ev.Data), &d); err != nil {
			t.Fatal(err)
		}
		got[d.Name]++
	}
	if got["k8s-gateway"] != 1 || got["edge-nl"] != 1 {
		t.Fatalf("one event per fleet: %v", got)
	}
	if ev, ok := s.nextOf(dto.LiveFleetUpdated, 400*time.Millisecond); ok {
		t.Fatalf("no further fleet.updated expected: %+v", ev)
	}
}
