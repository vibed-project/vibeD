package store

import (
	"context"
	"strings"
	"time"

	"github.com/vibed-project/vibeD/pkg/api"
)

// defaultAuditLimit caps an unbounded ListAudit query.
const defaultAuditLimit = 200

// ---- MemoryStore ----

func (s *MemoryStore) AppendAudit(_ context.Context, e *api.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, *e)
	return nil
}

func (s *MemoryStore) ListAudit(_ context.Context, q AuditQuery) ([]api.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := q.Limit
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	out := make([]api.AuditEvent, 0, limit)
	// Newest first: walk the append-only log backwards.
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.audit[i]
		if q.Actor != "" && e.Actor != q.Actor {
			continue
		}
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		if q.Target != "" && e.Target != q.Target {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// ---- SQLiteStore ----

func (s *SQLiteStore) AppendAudit(ctx context.Context, e *api.AuditEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_events (id, ts, actor, action, target, outcome, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Time.UTC().Format(time.RFC3339Nano), e.Actor, e.Action, e.Target, e.Outcome, e.Detail)
	return err
}

func (s *SQLiteStore) ListAudit(ctx context.Context, q AuditQuery) ([]api.AuditEvent, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultAuditLimit
	}

	var (
		conds []string
		args  []any
	)
	if q.Actor != "" {
		conds = append(conds, "actor = ?")
		args = append(args, q.Actor)
	}
	if q.Action != "" {
		conds = append(conds, "action = ?")
		args = append(args, q.Action)
	}
	if q.Target != "" {
		conds = append(conds, "target = ?")
		args = append(args, q.Target)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, actor, action, target, outcome, detail FROM audit_events `+where+` ORDER BY ts DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]api.AuditEvent, 0, limit)
	for rows.Next() {
		var e api.AuditEvent
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.Outcome, &e.Detail); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}
