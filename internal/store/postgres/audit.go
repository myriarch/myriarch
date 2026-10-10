// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type auditStore struct{ db *DB }

var _ store.AuditStore = (*auditStore)(nil)

// Append implements store.AuditStore.
func (s *auditStore) Append(ctx context.Context, e domain.AuditEvent) error {
	t := e.Time
	if t.IsZero() {
		t = time.Now()
	}
	if e.Result == "" {
		e.Result = "success"
	}
	var details any
	if len(e.Details) > 0 {
		b, err := jsonArg(e.Details, "")
		if err != nil {
			return err
		}
		details = b
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO audit_events (time, actor_type, actor_id, actor_name, action, target_type, target_id, result, details, source_ip, request_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		t.UTC(), string(e.ActorType), cleanText(e.ActorID), cleanText(e.ActorName), cleanText(e.Action), cleanText(e.TargetType), cleanText(e.TargetID),
		e.Result, details, e.SourceIP, e.RequestID)
	return dbErr("audit.append", err)
}

// List implements store.AuditStore.
func (s *auditStore) List(ctx context.Context, f store.AuditFilter) ([]domain.AuditEvent, int, error) {
	var (
		args    argList
		clauses []string
	)
	if f.ActorID != "" {
		// The UI shows names, the API historically took ids: match either.
		p := args.add(f.ActorID)
		clauses = append(clauses, fmt.Sprintf("(actor_id = %[1]s OR lower(actor_name) = lower(%[1]s))", p))
	}
	if f.Action != "" {
		if strings.HasSuffix(f.Action, "*") {
			clauses = append(clauses, "action LIKE "+args.add(likeEscape(strings.TrimSuffix(f.Action, "*"))+"%"))
		} else {
			clauses = append(clauses, "action = "+args.add(f.Action))
		}
	}
	if f.TargetType != "" {
		clauses = append(clauses, "target_type = "+args.add(f.TargetType))
	}
	if f.TargetID != "" {
		clauses = append(clauses, "target_id = "+args.add(f.TargetID))
	}
	if f.From != nil {
		clauses = append(clauses, "time >= "+args.add(f.From.UTC()))
	}
	if f.To != nil {
		clauses = append(clauses, "time <= "+args.add(f.To.UTC()))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := max(f.Offset, 0)
	sql := `SELECT id, time, actor_type, actor_id, actor_name, action, target_type, target_id, result, details, source_ip, request_id, count(*) OVER ()
FROM audit_events` + where + ` ORDER BY time DESC, id DESC LIMIT ` + args.add(limit) + ` OFFSET ` + args.add(offset)
	rows, err := s.db.q.Query(ctx, sql, args.args...)
	if err != nil {
		return nil, 0, dbErr("audit.list", err)
	}
	defer rows.Close()
	out := []domain.AuditEvent{}
	total := 0
	for rows.Next() {
		var (
			e       domain.AuditEvent
			actor   string
			details []byte
		)
		if err := rows.Scan(&e.ID, &e.Time, &actor, &e.ActorID, &e.ActorName, &e.Action, &e.TargetType, &e.TargetID, &e.Result, &details, &e.SourceIP, &e.RequestID, &total); err != nil {
			return nil, 0, dbErr("audit.list scan", err)
		}
		e.ActorType, e.Time = domain.ActorType(actor), e.Time.UTC()
		if len(details) > 0 {
			if err := decodeJSON(details, &e.Details); err != nil {
				return nil, 0, fmt.Errorf("decode audit details: %w", err)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dbErr("audit.list", err)
	}
	if len(out) == 0 && offset > 0 {
		rows.Close()
		if err := s.db.q.QueryRow(ctx, `SELECT count(*) FROM audit_events`+where, args.args[:len(args.args)-2]...).Scan(&total); err != nil {
			return nil, 0, dbErr("audit.list count", err)
		}
	}
	return out, total, nil
}

// DeleteBefore implements store.AuditStore.
func (s *auditStore) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.db.execMaintenance(ctx, `DELETE FROM audit_events WHERE time < $1`, before.UTC())
	if err != nil {
		return 0, dbErr("audit.deleteBefore", err)
	}
	return n, nil
}
