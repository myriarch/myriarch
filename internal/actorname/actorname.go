// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package actorname resolves stored actor references ("user:<uuid>",
// "token:<uuid>", "system", "gitops") into display names and audit actors.
//
// Stored references stay stable identifiers (rollout creators, approvers,
// version selections, audit); everything that is shown to people (REST
// responses, SSE events, notifications) presents the name instead.
package actorname

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// Resolver resolves the references of one response or event. It caches
// lookups and loads the (small) API token list on first use. Not safe for
// concurrent use: create one per request.
type Resolver struct {
	ctx      context.Context
	identity store.IdentityStore
	cache    map[string]string
	tokens   map[uuid.UUID]string
}

// New returns a resolver reading from identity (nil: references are returned as they are).
func New(ctx context.Context, identity store.IdentityStore) *Resolver {
	return &Resolver{ctx: ctx, identity: identity, cache: map[string]string{}}
}

// Name returns the display name of an actor reference ("" stays "").
// Unknown references are returned as they are.
func (r *Resolver) Name(ref string) string {
	if ref == "" {
		return ""
	}
	if n, ok := r.cache[ref]; ok {
		return n
	}
	n := r.resolve(ref)
	r.cache[ref] = n
	return n
}

func (r *Resolver) resolve(ref string) string {
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || r.identity == nil {
		return ref // "system", "gitops" or an already readable name
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return ref
	}
	switch kind {
	case "user":
		u, err := r.identity.GetUser(r.ctx, uid)
		if err != nil || u == nil {
			return ref
		}
		switch {
		case u.Name != "":
			return u.Name
		case u.Email != "":
			return u.Email
		}
	case "token":
		if r.tokens == nil {
			r.tokens = map[uuid.UUID]string{}
			if list, err := r.identity.ListAPITokens(r.ctx); err == nil {
				for _, t := range list {
					r.tokens[t.ID] = t.Name
				}
			}
		}
		if n := r.tokens[uid]; n != "" {
			return "token " + n
		}
	}
	return ref
}

// Actor splits a stored reference into the audit actor type and id.
// "user:<id>" and "token:<id>" map to their types, anything else is the system.
func Actor(ref string) (domain.ActorType, string) {
	kind, id, ok := strings.Cut(ref, ":")
	if ok {
		switch kind {
		case "user":
			return domain.ActorUser, id
		case "token":
			return domain.ActorToken, id
		}
	}
	return domain.ActorSystem, "system"
}
