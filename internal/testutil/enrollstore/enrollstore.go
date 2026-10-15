// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package enrollstore wraps a store.Store so that tests of packages that use the in-memory store can create,
// read and revoke enrollment tokens by ID. The in-memory store lists the tokens it was given but cannot read
// one back; the install grants of the artifact cache and the install plan need exactly that. Everything else is
// delegated to the wrapped store.
package enrollstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// Store is a store.Store with enrollment tokens that can be created, read and revoked.
type Store struct {
	store.Store
	mu     sync.Mutex
	tokens map[uuid.UUID]domain.EnrollmentToken
	reads  int
}

// Wrap returns the wrapper around s.
func Wrap(s store.Store) *Store {
	return &Store{Store: s, tokens: map[uuid.UUID]domain.EnrollmentToken{}}
}

// Create stores a token (an empty ID gets a new one) and returns it.
func (s *Store) Create(t domain.EnrollmentToken) domain.EnrollmentToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	s.tokens[t.ID] = t
	return t
}

// Revoke marks a token revoked at the given time.
func (s *Store) Revoke(id uuid.UUID, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tokens[id]
	t.RevokedAt = &at
	s.tokens[id] = t
}

// Delete removes a token.
func (s *Store) Delete(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, id)
}

// Reads is the number of GetEnrollmentToken calls so far.
func (s *Store) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// Identity implements store.Store.
func (s *Store) Identity() store.IdentityStore {
	return &identity{IdentityStore: s.Store.Identity(), s: s}
}

type identity struct {
	store.IdentityStore
	s *Store
}

// GetEnrollmentToken implements store.IdentityStore.
func (i *identity) GetEnrollmentToken(_ context.Context, id uuid.UUID) (*domain.EnrollmentToken, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	i.s.reads++
	t, ok := i.s.tokens[id]
	if !ok {
		return nil, fmt.Errorf("enrollment token %s: %w", id, domain.ErrNotFound)
	}
	return &t, nil
}

// ListEnrollmentTokens implements store.IdentityStore.
func (i *identity) ListEnrollmentTokens(ctx context.Context) ([]domain.EnrollmentToken, error) {
	out, err := i.IdentityStore.ListEnrollmentTokens(ctx)
	if err != nil {
		return nil, err
	}
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	for _, t := range i.s.tokens {
		out = append(out, t)
	}
	return out, nil
}
