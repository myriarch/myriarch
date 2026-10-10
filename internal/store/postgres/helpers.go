// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/myriarch/myriarch/internal/domain"
)

// dbErr maps driver errors to domain sentinels. The message of the result is safe for API clients (it carries
// neither the operation, nor constraint names, nor driver text); those are in the Internal text of the
// *domain.ClientError, which the HTTP layer logs with the request ID. Unclassified errors keep the operation name
// (they become 500 responses with a generic text).
func dbErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return &domain.ClientError{Msg: "not found", Kind: domain.ErrNotFound, Cause: err, Internal: op + ": not found"}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return &domain.ClientError{Msg: "already exists", Kind: domain.ErrConflict, Cause: err,
				Internal: fmt.Sprintf("%s: duplicate %s", op, pgErr.ConstraintName)}
		case "23503": // foreign_key_violation
			return &domain.ClientError{Msg: "a referenced object does not exist", Kind: domain.ErrNotFound, Cause: err,
				Internal: fmt.Sprintf("%s: referenced entity does not exist (%s)", op, pgErr.ConstraintName)}
		case "23514", "22P02", "22007", "22008": // check violation, invalid text/datetime
			return &domain.ClientError{Msg: "invalid value", Kind: domain.ErrInvalidArgument, Cause: err,
				Internal: fmt.Sprintf("%s: %s", op, pgErr.Message)}
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// timeArg converts a timestamp to a query argument (zero -> NULL).
func timeArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// timePtrArg converts an optional timestamp (nil/zero -> NULL).
func timePtrArg(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC()
}

// hashArg converts a hash to a bytea argument (empty -> NULL).
func hashArg(h domain.Hash) any {
	if len(h) == 0 {
		return nil
	}
	return []byte(h)
}

// textArg converts "" to NULL.
func textArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// uuidPtrArg converts an optional uuid to an argument.
func uuidPtrArg(id *uuid.UUID) any {
	if id == nil || *id == uuid.Nil {
		return nil
	}
	return *id
}

// uuidArg converts the zero uuid to NULL.
func uuidArg(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// jsonArg marshals v for a jsonb parameter. A nil map/slice/pointer (JSON
// "null") becomes def ("{}" or "[]"), or SQL NULL when def is empty.
func jsonArg(v any, def string) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	if string(b) == "null" {
		if def == "" {
			return nil, nil
		}
		return []byte(def), nil
	}
	return stripJSONNUL(b), nil
}

// stripJSONNUL removes JSON escapes of the NUL character ("\u0000"), which
// PostgreSQL jsonb rejects. Escaped backslashes are respected.
func stripJSONNUL(b []byte) []byte {
	if !bytes.Contains(b, []byte(`\u0000`)) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' {
			if i+1 < len(b) && b[i+1] == '\\' { // escaped backslash
				out = append(out, '\\', '\\')
				i++
				continue
			}
			if i+5 < len(b) && string(b[i:i+6]) == `\u0000` {
				i += 5
				continue
			}
		}
		out = append(out, b[i])
	}
	return out
}

// decodeJSON decodes jsonb bytes with integer preservation. Empty input
// leaves v unchanged.
func decodeJSON(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return domain.UnmarshalJSONPreservingInts(b, v)
}

func stringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// likeEscape escapes LIKE/ILIKE metacharacters (escape char is backslash).
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// argList builds positional placeholders for dynamic queries.
type argList struct{ args []any }

func (a *argList) add(v any) string {
	a.args = append(a.args, v)
	return fmt.Sprintf("$%d", len(a.args))
}

// terminalRolloutStatuses are the statuses for which RolloutStatus.IsTerminal
// is true (kept in SQL as a literal list; asserted by a test).
const terminalRolloutStatusesSQL = `('completed','aborted','rolled_back','superseded')`
