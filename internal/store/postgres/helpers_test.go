// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/domain"
)

// The problem detail of a 4xx response carries no operation name, constraint name or driver text.
func TestDBErrorsAreClientSafe(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		kind   error
		leak   []string
	}{
		"not found":  {pgx.ErrNoRows, 404, domain.ErrNotFound, []string{"agents.get"}},
		"duplicate":  {&pgconn.PgError{Code: "23505", ConstraintName: "agents_uid_key", Message: "duplicate key value violates unique constraint"}, 409, domain.ErrConflict, []string{"agents.get", "agents_uid_key", "violates"}},
		"foreign":    {&pgconn.PgError{Code: "23503", ConstraintName: "agents_fleet_fkey", Message: "insert or update violates foreign key constraint"}, 404, domain.ErrNotFound, []string{"agents.get", "agents_fleet_fkey", "violates"}},
		"check":      {&pgconn.PgError{Code: "23514", ConstraintName: "agents_name_check", Message: "new row violates check constraint \"agents_name_check\""}, 400, domain.ErrInvalidArgument, []string{"agents.get", "agents_name_check", "violates"}},
		"bad format": {&pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid: \"zzz\""}, 400, domain.ErrInvalidArgument, []string{"agents.get", "syntax", "uuid"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := dbErr("agents.get", tc.err)
			if !errors.Is(err, tc.kind) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.kind)
			}
			// a caller may add context; it stays safe
			rec := httptest.NewRecorder()
			api.WriteError(rec, httptest.NewRequest("GET", "/api/v1/agents/x", nil), err)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			var p api.Problem
			if e := json.Unmarshal(rec.Body.Bytes(), &p); e != nil {
				t.Fatal(e)
			}
			for _, leak := range tc.leak {
				if strings.Contains(p.Detail, leak) {
					t.Errorf("detail %q contains %q", p.Detail, leak)
				}
			}
			if p.Detail == "" {
				t.Error("empty detail")
			}
		})
	}
	// other errors keep the operation (they end as a generic 500 for the client)
	if err := dbErr("agents.get", errors.New("boom")); !strings.Contains(err.Error(), "agents.get") {
		t.Errorf("unclassified error: %v", err)
	}
}
