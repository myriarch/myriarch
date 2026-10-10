// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package actorname_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/actorname"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

func TestNameResolvesReferences(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	named, mailOnly, token := uuid.New(), uuid.New(), uuid.New()
	st.PutUser(domain.User{ID: named, Name: "Erika Mustermann", Email: "erika@example.com"})
	st.PutUser(domain.User{ID: mailOnly, Email: "ops@example.com"})
	if err := st.Identity().CreateAPIToken(ctx, domain.APIToken{ID: token, Name: "ci-deploy"}); err != nil {
		t.Fatal(err)
	}
	r := actorname.New(ctx, st.Identity())
	for ref, want := range map[string]string{
		"":                          "",
		"gitops":                    "gitops",
		"system":                    "system",
		"user:" + named.String():    "Erika Mustermann",
		"user:" + mailOnly.String(): "ops@example.com",
		"token:" + token.String():   "token ci-deploy",
		"user:" + uuid.NewString():  "", // unknown user: the reference itself (checked below)
		"user:not-a-uuid":           "user:not-a-uuid",
		"token:" + uuid.NewString(): "",
		"service:abc":               "service:abc",
	} {
		got := r.Name(ref)
		if want == "" && ref != "" {
			want = ref
		}
		if got != want {
			t.Errorf("Name(%q) = %q, want %q", ref, got, want)
		}
		if again := r.Name(ref); again != got {
			t.Errorf("Name(%q) is not stable: %q then %q", ref, got, again)
		}
	}
	// without an identity store the reference is returned as it is
	if got := actorname.New(ctx, nil).Name("user:" + named.String()); got != "user:"+named.String() {
		t.Errorf("without identity store: %q", got)
	}
}

func TestActor(t *testing.T) {
	for ref, want := range map[string][2]string{
		"user:5f1c": {string(domain.ActorUser), "5f1c"}, "token:t1": {string(domain.ActorToken), "t1"},
		"system": {string(domain.ActorSystem), "system"}, "gitops": {string(domain.ActorSystem), "system"}, "": {string(domain.ActorSystem), "system"},
	} {
		typ, id := actorname.Actor(ref)
		if string(typ) != want[0] || id != want[1] {
			t.Errorf("Actor(%q) = %s %s, want %v", ref, typ, id, want)
		}
	}
}
