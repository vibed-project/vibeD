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
	"fmt"
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

// Fields are optional enrichment attached to the request context via WithFields
// and merged into every audit event recorded under that context. Empty fields
// are ignored. This lets a caller enrich events (tenant, source hash, policy
// verdict, before/after) without changing the Auditor interface.
type Fields struct {
	TenantID       string
	SessionID      string
	SourceHash     string
	PolicyDecision string
	Before         string
	After          string
}

type fieldsKey struct{}

// WithFields returns a context carrying f; audit events recorded under it are
// enriched with f's non-empty values.
func WithFields(ctx context.Context, f Fields) context.Context {
	return context.WithValue(ctx, fieldsKey{}, f)
}

func fieldsFromContext(ctx context.Context) Fields {
	f, _ := ctx.Value(fieldsKey{}).(Fields)
	return f
}

// Recorder writes governance audit events.
type Recorder struct {
	store      store.AuditStore
	logger     *slog.Logger
	failClosed bool
}

// New builds a Recorder over the given audit store. A nil store yields a
// Recorder that only logs + counts (no persistence). When failClosed is true,
// a Record call that cannot reach the persistent store returns an error so
// callers can reject the caller's action — preferring availability loss over
// an untraceable mutation.
func New(s store.AuditStore, logger *slog.Logger, failClosed bool) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{store: s, logger: logger, failClosed: failClosed}
}

// Record persists one audit event. action is deploy|delete|rollback; outcome
// is ok|denied|error. The actor is read from ctx. Returns an error only when
// the persistent store rejected the append AND the Recorder is configured
// fail-closed; the in-memory log + Prometheus counter are always updated.
func (r *Recorder) Record(ctx context.Context, action, target, outcome, detail string) error {
	if r == nil {
		return nil
	}
	actor := vibedauth.UserIDFromContext(ctx)
	f := fieldsFromContext(ctx)
	e := &api.AuditEvent{
		ID:             newID(),
		Time:           time.Now().UTC(),
		Actor:          actor,
		Action:         action,
		Target:         target,
		Outcome:        outcome,
		Detail:         detail,
		TenantID:       f.TenantID,
		SessionID:      f.SessionID,
		SourceHash:     f.SourceHash,
		PolicyDecision: f.PolicyDecision,
		Before:         f.Before,
		After:          f.After,
	}
	events.WithLabelValues(action, outcome).Inc()
	r.logger.Info("audit", "actor", actor, "action", action, "target", target, "outcome", outcome)
	if r.store != nil {
		if err := r.store.AppendAudit(ctx, e); err != nil {
			r.logger.Warn("audit: persist failed", "error", err, "action", action, "target", target, "failClosed", r.failClosed)
			if r.failClosed {
				return fmt.Errorf("audit: persist failed: %w", err)
			}
		}
	}
	return nil
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
