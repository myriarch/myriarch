// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrClosed is returned by Publish after Close.
var ErrClosed = errors.New("eventbus: closed")

// memoryBus is a single-process Bus (tests, single replica dev mode).
type memoryBus struct {
	origin string
	buffer int

	mu     sync.Mutex
	subs   map[int]*memorySub
	nextID int
	closed bool
}

type memorySub struct {
	ch    chan Event
	types map[Type]struct{}
}

// NewMemory returns an in-process Bus. origin is used as Event.Origin;
// buffer is the per-subscriber channel size (default 256 if <= 0).
func NewMemory(origin string, buffer int) Bus {
	if buffer <= 0 {
		buffer = 256
	}
	return &memoryBus{origin: origin, buffer: buffer, subs: map[int]*memorySub{}}
}

func (b *memoryBus) Publish(_ context.Context, typ Type, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("eventbus: marshal payload: %w", err)
	}
	ev := Event{ID: uuid.New(), Type: typ, Time: time.Now().UTC(), Origin: b.origin, Payload: raw}
	enc, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("eventbus: marshal event: %w", err)
	}
	if len(enc) > MaxEventSize {
		return fmt.Errorf("eventbus: event %s too large (%d > %d bytes)", typ, len(enc), MaxEventSize)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	for _, s := range b.subs {
		if len(s.types) > 0 {
			if _, ok := s.types[typ]; !ok {
				continue
			}
		}
		select {
		case s.ch <- ev:
		default: // drop for slow subscriber
		}
	}
	return nil
}

func (b *memoryBus) Subscribe(types ...Type) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &memorySub{ch: make(chan Event, b.buffer), types: map[Type]struct{}{}}
	for _, t := range types {
		s.types[t] = struct{}{}
	}
	if b.closed {
		close(s.ch)
		return s.ch, func() {}
	}
	id := b.nextID
	b.nextID++
	b.subs[id] = s
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(s.ch)
			}
		})
	}
}

func (b *memoryBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for id, s := range b.subs {
		close(s.ch)
		delete(b.subs, id)
	}
	return nil
}
