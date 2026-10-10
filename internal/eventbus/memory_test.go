// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package eventbus

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

func TestMemoryBus(t *testing.T) {
	b := NewMemory("r1", 4)
	all, cancelAll := b.Subscribe()
	defer cancelAll()
	only, cancelOnly := b.Subscribe(AgentCommand)
	defer cancelOnly()

	id := domain.AgentID{1}
	if err := b.Publish(context.Background(), AgentCommand, AgentCommandPayload{AgentID: id, Command: CommandRestart}); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(context.Background(), GitOpsSynced, GitOpsSyncedPayload{Commit: "abc"}); err != nil {
		t.Fatal(err)
	}
	ev := recv(t, only)
	var p AgentCommandPayload
	if err := ev.Decode(&p); err != nil || p.AgentID != id || ev.Origin != "r1" {
		t.Fatalf("unexpected event %+v (%v)", ev, err)
	}
	if recv(t, all).Type != AgentCommand || recv(t, all).Type != GitOpsSynced {
		t.Fatal("unexpected order/types on unfiltered subscription")
	}
	select {
	case ev := <-only:
		t.Fatalf("filtered subscriber got %s", ev.Type)
	default:
	}

	big := strings.Repeat("x", MaxEventSize)
	if err := b.Publish(context.Background(), AgentCommand, map[string]string{"x": big}); err == nil {
		t.Fatal("expected size error")
	}
	if got := len(ChunkAgentIDs(make([]domain.AgentID, 250))); got != 3 {
		t.Fatalf("chunks = %d", got)
	}

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-all; ok {
		t.Fatal("channel must be closed after Close")
	}
	if err := b.Publish(context.Background(), AgentCommand, nil); err == nil {
		t.Fatal("publish after close must fail")
	}
}

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	return Event{}
}
