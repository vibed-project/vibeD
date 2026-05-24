// Package audit records mutating actions (deploy, delete, rollback) to a
// governance audit trail. The Recorder pulls the actor from the request
// context, writes to an append-only store, increments a Prometheus counter,
// and mirrors the event to structured logs. A nil Recorder is a safe no-op so
// callers needn't branch on whether auditing is configured.
package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
)

// events counts recorded audit events by action and outcome.
var events = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "vibed",
	Name:      "audit_events_total",
	Help:      "Audit events recorded, by action and outcome.",
}, []string{"action", "outcome"})

// Recorder writes governance audit events.
type Recorder struct {
	store  store.AuditStore
	logger *slog.Logger
}

// New builds a Recorder over the given audit store. A nil store yields a
// Recorder that only logs + counts (no persistence).
func New(s store.AuditStore, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{store: s, logger: logger}
}

// Record persists one audit event. action is deploy|delete|rollback; outcome
// is ok|denied|error. The actor is read from ctx.
func (r *Recorder) Record(ctx context.Context, action, target, outcome, detail string) {
	if r == nil {
		return
	}
	actor := vibedauth.UserIDFromContext(ctx)
	e := &api.AuditEvent{
		ID:      newID(),
		Time:    time.Now().UTC(),
		Actor:   actor,
		Action:  action,
		Target:  target,
		Outcome: outcome,
		Detail:  detail,
	}
	events.WithLabelValues(action, outcome).Inc()
	r.logger.Info("audit", "actor", actor, "action", action, "target", target, "outcome", outcome)
	if r.store != nil {
		if err := r.store.AppendAudit(ctx, e); err != nil {
			r.logger.Warn("audit: persist failed", "error", err, "action", action, "target", target)
		}
	}
}

// List returns recorded audit events matching q (newest first). Returns nil
// when no store is configured.
func (r *Recorder) List(ctx context.Context, q store.AuditQuery) ([]api.AuditEvent, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	return r.store.ListAudit(ctx, q)
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
