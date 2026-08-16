package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vibed-project/vibeD/internal/audit"
	"github.com/vibed-project/vibeD/internal/meter"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// versionsAnnotation stores an app's deploy history (JSON []VersionRecord,
// oldest-first) on the VibedApp CR. The controller never touches annotations,
// so the deploy service owns this field. Bounded to maxVersions; evicting a
// record also deletes its retained source tarball unless another record (e.g. a
// rollback that reuses an older source) still references the same blob.
const versionsAnnotation = "vibed.dev/versions"

// maxVersions caps retained deploy history — and retained source tarballs — per
// app.
const maxVersions = 20

// VersionRecord is one entry in an app's deploy history. TarballKey/TarballRef
// point at the retained source so a rollback can re-deploy it without a
// re-upload.
type VersionRecord struct {
	Version        int    `json:"version"`
	Timestamp      string `json:"timestamp"` // RFC3339
	Template       string `json:"template"`
	Lane           string `json:"lane"`
	SourceHash     string `json:"sourceHash"`
	TarballKey     string `json:"tarballKey"`
	TarballRef     string `json:"tarballRef"`
	RolledBackFrom int    `json:"rolledBackFrom,omitempty"`
}

// newVersionKey mints the store key for a version's source tarball. App names
// are DNS labels (no dots), so ".vN" can't collide with another app's name.
//
// The key is a CAPABILITY, not just a name. The served blob backend publishes
// it at /internal/sources/<key>.tar.gz; that endpoint authenticates callers but
// cannot authorize them per app, because the in-sandbox agent fetches with one
// shared token and so carries no per-user identity to check. While the key was
// derived purely from name and version ("shop.v1"), any authenticated client
// could guess another user's key and read their source. The random suffix makes
// the reference unguessable, so holding it *is* the authorization.
//
// Randomness is free at lookup time: the key is always persisted (in the
// version history and spec.source.tarballRef) and never re-derived.
func newVersionKey(name string, v int) (string, error) {
	buf := make([]byte, 16) // 128 bits
	if _, err := rand.Read(buf); err != nil {
		// Fail closed. Falling back to a predictable key would silently
		// re-expose every subsequent deploy's source.
		return "", fmt.Errorf("generating source key: %w", err)
	}
	return fmt.Sprintf("%s.v%d.%s", name, v, hex.EncodeToString(buf)), nil
}

// loadVersions parses the deploy history from the app's annotation (oldest
// first). A missing or malformed annotation yields an empty history.
func loadVersions(app *vibedv1.VibedApp) []VersionRecord {
	raw := app.GetAnnotations()[versionsAnnotation]
	if raw == "" {
		return nil
	}
	var recs []VersionRecord
	if err := json.Unmarshal([]byte(raw), &recs); err != nil {
		return nil
	}
	return recs
}

// nextVersion returns one past the highest version in history (1 when empty).
// Eviction drops the oldest (lowest) records, so the highest always survives and
// the counter stays monotonic.
func nextVersion(history []VersionRecord) int {
	n := 0
	for _, r := range history {
		if r.Version > n {
			n = r.Version
		}
	}
	return n + 1
}

// appendCapped appends rec, trims to the newest max records, and returns the
// tarball keys of dropped records that no surviving record still references.
func appendCapped(history []VersionRecord, rec VersionRecord, max int) (kept []VersionRecord, evictedKeys []string) {
	all := append(append([]VersionRecord{}, history...), rec)
	if len(all) <= max {
		return all, nil
	}
	dropped := all[:len(all)-max]
	kept = all[len(all)-max:]
	keptKeys := make(map[string]bool, len(kept))
	for _, r := range kept {
		keptKeys[r.TarballKey] = true
	}
	for _, r := range dropped {
		if r.TarballKey != "" && !keptKeys[r.TarballKey] {
			evictedKeys = append(evictedKeys, r.TarballKey)
		}
	}
	return kept, evictedKeys
}

func marshalVersions(recs []VersionRecord) string {
	b, _ := json.Marshal(recs)
	return string(b)
}

// Versions returns the app's deploy history, newest first, if owner owns it.
func (s *Service) Versions(ctx context.Context, owner, id string) ([]VersionRecord, error) {
	app, err := s.Get(ctx, owner, id) // ownership-checked
	if err != nil {
		return nil, err
	}
	history := loadVersions(app)
	out := make([]VersionRecord, len(history))
	for i, r := range history { // reverse to newest-first for display
		out[len(history)-1-i] = r
	}
	return out, nil
}

// Rollback re-deploys the source retained for version, recording it as a new
// version. Fails if that version's source is no longer retained.
func (s *Service) Rollback(ctx context.Context, owner, id string, version int) (*Result, error) {
	s.defaults()
	t, _ := s.tenant(ctx)
	ctx = audit.WithFields(ctx, audit.Fields{TenantID: t.ID})

	app, err := s.Get(ctx, owner, id) // ownership-checked
	if err != nil {
		return nil, err
	}
	history := loadVersions(app)
	var target *VersionRecord
	for i := range history {
		if history[i].Version == version {
			target = &history[i]
		}
	}
	if target == nil || target.TarballRef == "" {
		return nil, fmt.Errorf("version %d is not available for rollback", version)
	}

	rec := VersionRecord{
		Version:        nextVersion(history),
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		Template:       target.Template,
		Lane:           target.Lane,
		SourceHash:     target.SourceHash,
		TarballKey:     target.TarballKey,
		TarballRef:     target.TarballRef,
		RolledBackFrom: version,
	}
	kept, evicted := appendCapped(history, rec, maxVersions)

	app.Spec.Source.TarballRef = target.TarballRef
	app.Spec.Runtime.Template = target.Template
	app.Spec.Runtime.Lane = vibedv1.Lane(target.Lane)
	anns := app.GetAnnotations()
	if anns == nil {
		anns = map[string]string{}
	}
	anns[versionsAnnotation] = marshalVersions(kept)
	app.SetAnnotations(anns)

	if uerr := s.Client.Update(ctx, app); uerr != nil {
		_ = s.record(ctx, "rollback", id, "error", uerr.Error())
		return nil, fmt.Errorf("rollback VibedApp: %w", uerr)
	}
	for _, k := range evicted {
		_ = s.Store.Delete(ctx, k)
	}
	if err := s.record(ctx, "rollback", id, "ok", fmt.Sprintf("to v%d", version)); err != nil {
		return nil, fmt.Errorf("rollback succeeded but audit failed: %w", err)
	}
	s.meter(ctx, meter.Event{Kind: "rollback", Tenant: t.ID, Owner: owner, App: id, Namespace: app.Namespace})
	return s.waitReady(ctx, app.Namespace, id)
}
