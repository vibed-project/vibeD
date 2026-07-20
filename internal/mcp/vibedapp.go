package mcp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// When MCP runs over stdio there's no authenticated user; deploys still need
// an owner so the VibedApp lifecycle (list/status/delete) can scope to it.
// "local" is the stable fallback so all four tools agree on which apps to show.
const localOwner = "local"

func ownerFromContext(ctx context.Context) string {
	if o := vibedauth.UserIDFromContext(ctx); o != "" {
		return o
	}
	return localOwner
}

// tarballFromFiles packs an MCP file map into a gzipped tar, the form the
// deploy service (and the classifier) consume. Entries are sorted so the
// archive is byte-deterministic for a given input.
func tarballFromFiles(files map[string]string) (io.Reader, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// appToArtifact maps a VibedApp CR to the api.Artifact shape MCP returns for
// get_artifact_status.
func appToArtifact(app *vibedv1.VibedApp) *api.Artifact {
	a := &api.Artifact{
		ID:       app.Name,
		Name:     app.Name,
		OwnerID:  app.Spec.Owner,
		Status:   vibedv1.StatusFromPhase(app.Status.Phase),
		Target:   api.TargetSandbox,
		URL:      app.Status.URL,
		Language: app.Spec.Runtime.Template,
	}
	if app.Status.LastDeployedAt != nil {
		a.UpdatedAt = app.Status.LastDeployedAt.Time
	}
	return a
}

// versionRecordToArtifactVersion maps one deploy-history record onto the
// api.ArtifactVersion shape MCP returns for list_versions (field names are an
// MCP client contract). Warm-pool apps have no per-version image, so image_ref
// stays empty and storage_ref carries the retained source instead. Every
// recorded version was a successful deploy — matching the legacy
// snapshot-at-success semantics — hence status "running"; URL and creator come
// from the app (both are per-app, not per-version, on this path).
func versionRecordToArtifactVersion(app *vibedv1.VibedApp, rec deploy.VersionRecord) api.ArtifactVersion {
	v := api.ArtifactVersion{
		VersionID:  fmt.Sprintf("%s.v%d", app.Name, rec.Version),
		ArtifactID: app.Name,
		Version:    rec.Version,
		StorageRef: rec.TarballRef,
		Status:     api.StatusRunning,
		URL:        app.Status.URL,
		CreatedBy:  app.Spec.Owner,
	}
	if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		v.CreatedAt = t
	}
	return v
}

// appToSummary maps a VibedApp CR to the api.ArtifactSummary shape MCP
// returns for list_artifacts.
func appToSummary(app *vibedv1.VibedApp) api.ArtifactSummary {
	s := api.ArtifactSummary{
		ID:        app.Name,
		Name:      app.Name,
		OwnerID:   app.Spec.Owner,
		Namespace: app.Namespace,
		Status:    vibedv1.StatusFromPhase(app.Status.Phase),
		Target:    api.TargetSandbox,
		URL:       app.Status.URL,
	}
	if app.Status.LastDeployedAt != nil {
		s.UpdatedAt = app.Status.LastDeployedAt.Time
	}
	return s
}
