// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// queryRecorder is a pgx tracer recording all executed SQL statements.
type queryRecorder struct {
	mu   sync.Mutex
	sqls []string
}

func (q *queryRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	q.mu.Lock()
	q.sqls = append(q.sqls, d.SQL)
	q.mu.Unlock()
	return ctx
}
func (q *queryRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (q *queryRecorder) reset() {
	q.mu.Lock()
	q.sqls = nil
	q.mu.Unlock()
}

// relevant returns the statements that touch agent state, ignoring the
// background jobs (replica heartbeat, batched last_seen flush, event writer,
// bus publisher/listener).
func (q *queryRecorder) relevant() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []string
	for _, s := range q.sqls {
		switch {
		case strings.Contains(s, "replicas"), strings.Contains(s, "AS t(id)"), strings.Contains(s, "agent_events"),
			strings.Contains(s, "pg_notify"), strings.Contains(s, "LISTEN"), strings.HasPrefix(strings.TrimSpace(s), "SELECT 1"):
			continue
		}
		out = append(out, s)
	}
	return out
}

// TestHeartbeatsDoNotTouchTheDatabase protects the scalability requirement:
// 5,000 agents send ~170 heartbeats per second, which must be answered from the
// per-connection cache.
func TestHeartbeatsDoNotTouchTheDatabase(t *testing.T) {
	base := pgtest.NewPool(t)
	cfg := base.Config().Copy()
	rec := &queryRecorder{}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	r := newRig(t, rigOpts{pool: pool})

	id := newAgentID()
	a := r.connect("", id, capsCollector)
	a.send(firstMessage(id, descOpts{host: "h", version: "0.162.0"}))
	hash, _ := remoteBody(t, a.mustRecv())
	a.send(statusMsg(hash, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""))
	a.mustRecv()
	time.Sleep(1500 * time.Millisecond) // let asynchronous writers settle
	rec.reset()

	for i := 0; i < 200; i++ {
		a.send(&protobufs.AgentToServer{})
		m := a.mustRecv()
		if m.RemoteConfig != nil || m.Flags != 0 {
			t.Fatalf("heartbeat %d: %v", i, m)
		}
	}
	if q := rec.relevant(); len(q) != 0 {
		t.Fatalf("%d heartbeats executed %d agent related statements, want 0:\n%s", 200, len(q), strings.Join(q, "\n---\n"))
	}
	// unchanged health, effective config and statuses do not write either
	h := healthy()
	for i := 0; i < 20; i++ {
		a.send(&protobufs.AgentToServer{Health: h, RemoteConfigStatus: &protobufs.RemoteConfigStatus{LastRemoteConfigHash: hash, Status: protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED}})
		a.mustRecv()
	}
	if q := rec.relevant(); len(q) > 1 { // the first health report may differ in start time
		t.Fatalf("repeated identical reports executed %d statements:\n%s", len(q), strings.Join(q, "\n---\n"))
	}
	// the batched last_seen flush keeps the agent fresh without per-message writes
	eventually(t, "last_seen flushed", 5*time.Second, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for _, s := range rec.sqls {
			if strings.Contains(s, "AS t(id)") {
				return true
			}
		}
		return false
	})
}
