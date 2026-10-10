// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

func TestHandshakeAuthentication(t *testing.T) {
	r := newRig(t, rigOpts{requireTk: true})
	r.auth.tokens["myre_ok"] = &authn.AgentCredential{Labels: map[string]string{}}

	_, resp, err := r.dialRaw("", newAgentID(), capsCollector)
	if err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("missing token: err=%v resp=%v", err, resp)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("401 must carry WWW-Authenticate")
	}
	_, resp, err = r.dialRaw("myre_bad", newAgentID(), capsCollector)
	if err == nil || resp.StatusCode != 401 {
		t.Fatalf("bad token: %v %v", err, resp)
	}
	a, _, err := r.dialRaw("myre_ok", newAgentID(), capsCollector)
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	a.close()

	// infrastructure failure of the authenticator: 503 + Retry-After (agents back off, no 401 storm)
	r.auth.err = fmt.Errorf("%w: db down", domain.ErrUnavailable)
	_, resp, err = r.dialRaw("myre_ok", newAgentID(), capsCollector)
	if err == nil || resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("auth backend down: %v %v", err, resp)
	}
	if got := counter(t, r, "myriarch_opamp_connect_rejected_total", "reason", "unauthorized"); got != 2 {
		t.Fatalf("rejected{unauthorized} = %v", got)
	}

	// the replica-wide bound for enrollment token lookups: 429 + Retry-After
	r.auth.err = &domain.RateLimitError{Msg: "too many enrollment token lookups", After: time.Second}
	_, resp, err = r.dialRaw("myre_ok", newAgentID(), capsCollector)
	if err == nil || resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("lookup bound: %v %v", err, resp)
	}
	if got := counter(t, r, "myriarch_opamp_connect_rejected_total", "reason", "auth_rate_limited"); got != 1 {
		t.Fatalf("rejected{auth_rate_limited} = %v", got)
	}
}

func TestConnectRateLimitReturns429WithRetryAfter(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.ConnectRateLimit = 1 }}) // burst 2
	var accepted, limited int
	var retry string
	for i := 0; i < 8; i++ {
		a, resp, err := r.dialRaw("", newAgentID(), capsCollector)
		switch {
		case err == nil:
			accepted++
			a.close()
		case resp != nil && resp.StatusCode == 429:
			limited++
			retry = resp.Header.Get("Retry-After")
		default:
			t.Fatalf("unexpected: %v %v", err, resp)
		}
	}
	if accepted < 2 || accepted > 4 || limited < 4 || retry != "5" {
		t.Fatalf("accepted=%d limited=%d retry-after=%q (token bucket 1/s, burst 2)", accepted, limited, retry)
	}
	if counter(t, r, "myriarch_opamp_connect_rejected_total", "reason", "rate_limited") < 4 {
		t.Fatal("rate limit rejections must be counted")
	}
}

func TestMaxConnectionsPerReplicaReturns503(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.MaxConnectionsPerReplica = 2 }})
	var conns []*rawAgent
	for i := 0; i < 2; i++ {
		id := newAgentID()
		a := r.connect("", id, capsCollector)
		a.send(firstMessage(id, descOpts{host: fmt.Sprint("h", i), version: "0.162.0"}))
		a.mustRecv()
		conns = append(conns, a)
	}
	eventually(t, "two connections", 5*time.Second, func() bool { return r.srv.Connections() == 2 })
	_, resp, err := r.dialRaw("", newAgentID(), capsCollector)
	if err == nil || resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "30" {
		t.Fatalf("over the connection limit: %v %v", err, resp)
	}
	conns[0].close()
	eventually(t, "slot freed", 5*time.Second, func() bool { return r.srv.Connections() == 1 })
	a, _, err := r.dialRaw("", newAgentID(), capsCollector)
	if err != nil {
		t.Fatalf("connection after a slot was freed: %v", err)
	}
	a.close()
}

func counter(t *testing.T, r *rig, name, label, value string) float64 {
	t.Helper()
	mfs, err := r.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := label == ""
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					match = true
				}
			}
			if !match {
				continue
			}
			if m.Counter != nil {
				sum += m.GetCounter().GetValue()
			} else if m.Gauge != nil {
				sum += m.GetGauge().GetValue()
			} else if m.Histogram != nil {
				sum += float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return sum
}

func TestMetrics(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()
	a.send(&protobufs.AgentToServer{})
	a.mustRecv()
	eventually(t, "connection gauge", 5*time.Second, func() bool { return counter(t, r, "myriarch_opamp_connections", "transport", "websocket") == 1 })
	if in := counter(t, r, "myriarch_opamp_messages_total", "direction", "in"); in != 2 {
		t.Fatalf("messages in = %v", in)
	}
	if out := counter(t, r, "myriarch_opamp_messages_total", "direction", "out"); out != 2 {
		t.Fatalf("messages out = %v", out)
	}
	if counter(t, r, "myriarch_opamp_message_duration_seconds", "", "") != 2 {
		t.Fatal("duration histogram sample count")
	}
	a.close()
	eventually(t, "connection gauge back to 0", 5*time.Second, func() bool { return counter(t, r, "myriarch_opamp_connections", "transport", "websocket") == 0 })
}

// ----------------------------------------------- duplicate instance uids ---

func TestSameAgentReconnectingClosesTheStaleConnection(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	old := r.connect("", id, capsCollector)
	old.send(firstMessage(id, descOpts{host: "same-host", version: "0.162.0"}))
	old.mustRecv()

	// the agent reconnects (e.g. after a network change) before the server noticed the old connection died
	fresh := r.connect("", id, capsCollector)
	fresh.send(firstMessage(id, descOpts{host: "same-host", version: "0.162.0"}))
	m := fresh.mustRecv()
	if m.AgentIdentification != nil {
		t.Fatal("same host: this is a reconnect, not a clone")
	}
	old.expectClosed(5 * time.Second)
	r.waitEvent(id, domain.AgentEventIdentityConflict)
	eventually(t, "single live connection", 5*time.Second, func() bool { return r.srv.Connections() == 1 })
	time.Sleep(300 * time.Millisecond)
	if ag := r.agent(id); !ag.IsOnline() {
		t.Fatalf("closing the replaced connection must not mark the agent offline: %+v", ag.Connection)
	}
	// the new connection keeps working
	fresh.send(&protobufs.AgentToServer{})
	fresh.mustRecv()
}

func TestClonedAgentGetsNewInstanceUID(t *testing.T) {
	r := newRig(t, rigOpts{})
	id := newAgentID()
	original := r.connect("", id, capsCollector)
	original.send(firstMessage(id, descOpts{host: "edge-1001", version: "0.162.0"}))
	original.mustRecv()

	// a cloned image: same persisted instance uid, other host, connected at the same time
	clone := r.connect("", id, capsCollector)
	clone.send(firstMessage(id, descOpts{host: "edge-1002", version: "0.162.0"}))
	m := clone.mustRecv()
	if m.AgentIdentification == nil || len(m.AgentIdentification.NewInstanceUid) != 16 {
		t.Fatalf("the newer connection must receive a new instance uid: %v", m)
	}
	newUID, err := uuid.FromBytes(m.AgentIdentification.NewInstanceUid)
	if err != nil || newUID == uuid.UUID(id) || newUID.Version() != 7 {
		t.Fatalf("new uid must be a fresh UUIDv7: %v %v", newUID, err)
	}
	if m.Flags&flagFullState == 0 {
		t.Fatal("the clone must resend its full state under the new uid")
	}
	// the legitimate agent is untouched
	ag := r.agent(id)
	if ag.Hostname != "edge-1001" || !ag.IsOnline() {
		t.Fatalf("the original agent must not be overwritten by the clone: %+v", ag)
	}
	r.waitEvent(id, domain.AgentEventIdentityConflict)
	original.send(&protobufs.AgentToServer{})
	original.mustRecv()

	// the clone continues with its new uid on the same connection
	clone.id = domain.AgentID(newUID)
	clone.seq = 0
	clone.send(firstMessage(clone.id, descOpts{host: "edge-1002", version: "0.162.0"}))
	if m := clone.mustRecv(); m.ErrorResponse != nil || m.AgentIdentification != nil {
		t.Fatalf("%v", m)
	}
	cl := r.agent(clone.id)
	if cl.Hostname != "edge-1002" || !cl.IsOnline() {
		t.Fatalf("clone registered as separate agent: %+v", cl)
	}
	if !r.agent(id).IsOnline() || r.agent(id).Hostname != "edge-1001" {
		t.Fatal("original still intact")
	}
	// when the original is OFFLINE a different host is accepted as the same agent (host renamed/replaced)
	original.close()
	clone.close()
	eventually(t, "both offline", 5*time.Second, func() bool { return !r.agent(id).IsOnline() })
	moved := r.connect("", id, capsCollector)
	moved.send(firstMessage(id, descOpts{host: "edge-1001-new", version: "0.162.0"}))
	if m := moved.mustRecv(); m.AgentIdentification != nil {
		t.Fatal("an offline agent re-appearing under another host name is the same agent")
	}
	if r.agent(id).Hostname != "edge-1001-new" {
		t.Fatal("hostname must follow")
	}
}

// ---------------------------------------------------------- disconnects ---

func TestConnectionCloseAndAgentDisconnectMarkOffline(t *testing.T) {
	r := newRig(t, rigOpts{})
	id1, id2 := newAgentID(), newAgentID()
	a1 := r.connect("", id1, capsCollector)
	a1.send(firstMessage(id1, descOpts{host: "h1", version: "0.162.0"}))
	a1.mustRecv()
	a2 := r.connect("", id2, capsCollector)
	a2.send(firstMessage(id2, descOpts{host: "h2", version: "0.162.0"}))
	a2.mustRecv()

	// an explicit agent_disconnect message ends the session immediately
	a1.send(&protobufs.AgentToServer{AgentDisconnect: &protobufs.AgentDisconnect{}})
	eventually(t, "offline after agent_disconnect", 5*time.Second, func() bool { return !r.agent(id1).IsOnline() })
	a1.expectSilence(300 * time.Millisecond) // nil reply
	a1.close()
	r.waitEvent(id1, domain.AgentEventDisconnected)
	time.Sleep(300 * time.Millisecond)
	if r.countEvents(id1, domain.AgentEventDisconnected) != 1 {
		t.Fatalf("exactly one disconnected event: %v", r.eventTypes(id1))
	}

	// a dropped TCP connection
	a2.conn.UnderlyingConn().Close()
	eventually(t, "offline after drop", 5*time.Second, func() bool { return !r.agent(id2).IsOnline() })
	r.waitEvent(id2, domain.AgentEventDisconnected)
	if ag := r.agent(id2); ag.Connection.DisconnectedAt == nil || ag.Connection.LastSeenAt == nil {
		t.Fatalf("%+v", ag.Connection)
	}
}

// ------------------------------------------------------- HTTP transport ---

func (r *rig) httpPoll(t *testing.T, msg *protobufs.AgentToServer, token string) *protobufs.ServerToAgent {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", r.ts.URL, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/x-protobuf")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("http poll: %d %s", resp.StatusCode, body)
	}
	var out protobufs.ServerToAgent
	if err := proto.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestHTTPTransportWorksAndNeverGoesOfflineOnClose(t *testing.T) {
	// the ConnContext of Attach is installed on the http.Server by the rig: without it the HTTP transport panics
	r := newRig(t, rigOpts{})
	id := newAgentID()
	first := firstMessage(id, descOpts{host: "http-agent", version: "0.162.0"})
	first.Capabilities = capsCollector
	m := r.httpPoll(t, first, "")
	hash, _ := remoteBody(t, m)
	ag := r.agent(id)
	if !ag.IsOnline() || ag.Connection.Transport != "http" || ag.Connection.ReplicaID != "" {
		t.Fatalf("http agent connection: %+v", ag.Connection)
	}
	// status poll with sequence continuing: no flags, no re-offer (pooled session keeps the state)
	m = r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 1, RemoteConfigStatus: &protobufs.RemoteConfigStatus{LastRemoteConfigHash: hash, Status: protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED}}, "")
	if m.RemoteConfig != nil || m.Flags != 0 {
		t.Fatalf("poll 2: %v", m)
	}
	m = r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 2}, "")
	if m.RemoteConfig != nil || m.Flags != 0 || m.Capabilities == 0 {
		t.Fatalf("heartbeat poll: %v", m)
	}
	// every request closes its "connection": that must not mark the agent offline
	time.Sleep(300 * time.Millisecond)
	if !r.agent(id).IsOnline() {
		t.Fatal("HTTP polls must not cause offline transitions")
	}
	if r.srv.Connections() != 0 {
		t.Fatal("polls are not counted as websocket connections")
	}

	// a change is delivered with the next poll response
	newHash := newDesired(t, r, id, "service: {}\n# http\n")
	time.Sleep(300 * time.Millisecond)
	m = r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 3}, "")
	got, _ := remoteBody(t, m)
	if string(got) != string(newHash) {
		t.Fatal("HTTP agents get changes with the next poll")
	}
	// restart is delivered as the only content of a poll response
	if err := r.ctl.Restart(context.Background(), id, "user:1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	m = r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 4}, "")
	if m.Command == nil || m.RemoteConfig != nil {
		t.Fatalf("restart over HTTP: %v", m)
	}
	// unknown agent over HTTP: full state requested
	other := newAgentID()
	m = r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: other[:], SequenceNum: 0}, "")
	if m.Flags&flagFullState == 0 {
		t.Fatal("unknown http agent must be asked for its full state")
	}
	if r.agentOrNil(other) != nil {
		t.Fatal("nothing stored")
	}
	// the sweeper marks silent polling agents offline
	time.Sleep(2500 * time.Millisecond)
	r.srv.SweepOffline(context.Background())
	if r.agent(id).IsOnline() {
		t.Fatal("silent polling agent must be marked offline by the sweeper")
	}
	// ... and the next poll brings it back
	m = r.httpPoll(t, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 5}, "")
	eventually(t, "online again", 5*time.Second, func() bool { return r.agent(id).IsOnline() })
	_ = strings.TrimSpace
}

// ---------------------------------------------------------- slow clients ---

func TestSlowAgentIsDisconnectedAfterSendTimeout(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.SendTimeout = 500 * time.Millisecond; c.MaxMessageSize = 64 << 20 }})
	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "slow", version: "0.162.0"}))
	hash, _ := remoteBody(t, a.mustRecv())
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""))
	a.mustRecv()

	// the agent stops reading its socket (hung process): shrink the receive window and push big configs until the kernel buffers are full
	if tc, ok := a.conn.UnderlyingConn().(interface{ SetReadBuffer(int) error }); ok {
		_ = tc.SetReadBuffer(1024)
	}
	// freeze the reader goroutine of the test agent by filling its message channel is not enough; instead stop reading at the TCP level
	// by pausing the read loop through a blocked channel send: fill the channel first
	for i := 0; i < cap(a.msgs); i++ {
		a.msgs <- &protobufs.ServerToAgent{}
	}
	big := make([]byte, 0, 3<<20)
	for len(big) < 3<<20 {
		big = append(big, "receivers:\n  otlp: {}\n# padding padding padding padding padding padding padding\n"...)
	}
	before := counter(t, r, "myriarch_opamp_push_errors_total", "", "")
	start := time.Now()
	offline := func() bool { return !r.agent(id).IsOnline() }
	for i := 0; i < 40 && !offline(); i++ {
		newDesired(t, r, id, fmt.Sprintf("# %d\n%s", i, big))
		time.Sleep(150 * time.Millisecond)
	}
	// the server gives up on the agent that does not accept data within SendTimeout
	eventually(t, "slow agent disconnected and marked offline", 20*time.Second, offline)
	t.Logf("slow agent was disconnected after %v", time.Since(start))
	if counter(t, r, "myriarch_opamp_push_errors_total", "", "") <= before {
		t.Fatal("a timed out push must be counted")
	}
}

// ------------------------------------------------------------- shutdown ---

func TestShutdownClosesConnectionsMarksOfflineAndRefusesNewOnes(t *testing.T) {
	r := newRig(t, rigOpts{})
	var agents []*rawAgent
	var ids []domain.AgentID
	for i := 0; i < 5; i++ {
		id := newAgentID()
		a := r.connect("", id, capsCollector)
		a.send(firstMessage(id, descOpts{host: fmt.Sprint("h", i), version: "0.162.0"}))
		a.mustRecv()
		agents = append(agents, a)
		ids = append(ids, id)
	}
	eventually(t, "5 connections", 5*time.Second, func() bool { return r.srv.Connections() == 5 })

	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t0 := time.Now()
	if err := r.srv.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("shutdown of 5 connections took %v", d)
	}
	for _, a := range agents {
		a.expectClosed(5 * time.Second)
	}
	for _, id := range ids {
		ag := r.agent(id)
		if ag.IsOnline() || ag.Connection.ReplicaID != "" {
			t.Fatalf("agent %s still online after shutdown: %+v", id, ag.Connection)
		}
		r.waitEvent(id, domain.AgentEventDisconnected)
	}
	_, resp, err := r.dialRaw("", newAgentID(), capsCollector)
	if err == nil || resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "10" {
		t.Fatalf("new connections during shutdown: %v %v", err, resp)
	}
	if counter(t, r, "myriarch_opamp_connect_rejected_total", "reason", "shutdown") != 1 {
		t.Fatal("shutdown rejection must be counted")
	}
}

func TestEnrollmentTokenRequiredInHandshakeWithRealAuthn(t *testing.T) {
	r := newRig(t, rigOpts{realAuth: true, requireTk: true})
	ctx := context.Background()
	secret, err := authn.NewToken(domain.EnrollmentTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	tokID := uuid.New()
	if err := r.db.Identity().CreateEnrollmentToken(ctx, domain.EnrollmentToken{ID: tokID, Name: "sites", DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)),
		Labels: map[string]string{"channel": "edge-be"}, CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}

	if _, resp, err := r.dialRaw("", newAgentID(), capsCollector); err == nil || resp.StatusCode != 401 {
		t.Fatalf("no token: %v %v", err, resp)
	}
	if _, resp, err := r.dialRaw("myre_"+strings.Repeat("x", 32), newAgentID(), capsCollector); err == nil || resp.StatusCode != 401 {
		t.Fatalf("unknown token: %v %v", err, resp)
	}
	id := newAgentID()
	a := r.connect(secret, id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	a.mustRecv()
	ag := r.agent(id)
	if ag.Labels["channel"] != "edge-be" || ag.EnrollmentTokenID == nil || *ag.EnrollmentTokenID != tokID {
		t.Fatalf("labels and token id of the enrollment token must be applied to the new agent: %+v", ag)
	}
	r.realAuth.FlushEnrollmentUsage(ctx)
	tok, _ := r.db.Identity().GetEnrollmentTokenByHash(ctx, domain.SHA256([]byte(secret)))
	if tok.UseCount < 1 || tok.LastUsedAt == nil {
		t.Fatalf("token usage: %+v", tok)
	}
	// revoked tokens stop working for new connections (after the cache TTL; purge by creating a fresh service)
	if err := r.db.Identity().RevokeEnrollmentToken(ctx, tokID, time.Now()); err != nil {
		t.Fatal(err)
	}
}
