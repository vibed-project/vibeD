//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestArtifact(id, name string) *api.Artifact {
	now := time.Now()
	return &api.Artifact{
		ID:        id,
		Name:      name,
		Status:    api.StatusRunning,
		Target:    api.TargetKubernetes,
		ImageRef:  "nginx:latest",
		URL:       "http://localhost:31000",
		Port:      8080,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestConfigMapStore_CreateAndGet(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	s := store.NewConfigMapStore(clients.Clientset, "test-store", ns)
	ctx := context.Background()

	artifact := newTestArtifact("art-001", "my-app")
	err := s.Create(ctx, artifact)
	require.NoError(t, err)

	got, err := s.Get(ctx, "art-001")
	require.NoError(t, err)
	assert.Equal(t, "art-001", got.ID)
	assert.Equal(t, "my-app", got.Name)
	assert.Equal(t, api.StatusRunning, got.Status)
	assert.Equal(t, api.TargetKubernetes, got.Target)
	assert.Equal(t, "nginx:latest", got.ImageRef)
}

func TestConfigMapStore_GetByName(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	s := store.NewConfigMapStore(clients.Clientset, "test-store", ns)
	ctx := context.Background()

	artifact := newTestArtifact("art-002", "named-app")
	require.NoError(t, s.Create(ctx, artifact))

	got, err := s.GetByName(ctx, "named-app")
	require.NoError(t, err)
	assert.Equal(t, "art-002", got.ID)
	assert.Equal(t, "named-app", got.Name)
}

func TestConfigMapStore_DuplicateName(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	s := store.NewConfigMapStore(clients.Clientset, "test-store", ns)
	ctx := context.Background()

	a1 := newTestArtifact("art-010", "dup-name")
	a2 := newTestArtifact("art-011", "dup-name")

	require.NoError(t, s.Create(ctx, a1))
	err := s.Create(ctx, a2)
	require.Error(t, err)
	assert.IsType(t, &api.ErrAlreadyExists{}, err)
}

func TestConfigMapStore_Update(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	s := store.NewConfigMapStore(clients.Clientset, "test-store", ns)
	ctx := context.Background()

	artifact := newTestArtifact("art-003", "update-app")
	require.NoError(t, s.Create(ctx, artifact))

	// Update status and URL
	artifact.Status = api.StatusFailed
	artifact.Error = "build timeout"
	artifact.URL = "http://new-url:9090"
	require.NoError(t, s.Update(ctx, artifact))

	got, err := s.Get(ctx, "art-003")
	require.NoError(t, err)
	assert.Equal(t, api.StatusFailed, got.Status)
	assert.Equal(t, "build timeout", got.Error)
	assert.Equal(t, "http://new-url:9090", got.URL)
}

func TestConfigMapStore_Delete(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	s := store.NewConfigMapStore(clients.Clientset, "test-store", ns)
	ctx := context.Background()

	artifact := newTestArtifact("art-004", "delete-app")
	require.NoError(t, s.Create(ctx, artifact))

	require.NoError(t, s.Delete(ctx, "art-004"))

	_, err := s.Get(ctx, "art-004")
	require.Error(t, err)
	assert.IsType(t, &api.ErrNotFound{}, err)
}

func TestConfigMapStore_List(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	s := store.NewConfigMapStore(clients.Clientset, "test-store", ns)
	ctx := context.Background()

	a1 := newTestArtifact("art-005", "list-app-1")
	a1.Status = api.StatusRunning
	a2 := newTestArtifact("art-006", "list-app-2")
	a2.Status = api.StatusFailed
	a3 := newTestArtifact("art-007", "list-app-3")
	a3.Status = api.StatusRunning

	require.NoError(t, s.Create(ctx, a1))
	require.NoError(t, s.Create(ctx, a2))
	require.NoError(t, s.Create(ctx, a3))

	// List all
	all, err := s.List(ctx, "", "")
	require.NoError(t, err)
	assert.Len(t, all, 3)

	// Filter by status
	running, err := s.List(ctx, "running", "")
	require.NoError(t, err)
	assert.Len(t, running, 2)

	failed, err := s.List(ctx, "failed", "")
	require.NoError(t, err)
	assert.Len(t, failed, 1)
	assert.Equal(t, "list-app-2", failed[0].Name)
}

func TestConfigMapStore_GetNotFound(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	s := store.NewConfigMapStore(clients.Clientset, "test-store", ns)
	ctx := context.Background()

	// First create something so the ConfigMap exists
	artifact := newTestArtifact("art-008", "exists-app")
	require.NoError(t, s.Create(ctx, artifact))

	// Now try to get a non-existent ID
	_, err := s.Get(ctx, "nonexistent-id")
	require.Error(t, err)
	assert.IsType(t, &api.ErrNotFound{}, err)
}
