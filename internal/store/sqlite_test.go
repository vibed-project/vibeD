package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vibed-project/vibeD/pkg/api"
)

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func testArtifact(id, name string) *api.Artifact {
	now := time.Now().Truncate(time.Microsecond)
	return &api.Artifact{
		ID:        id,
		Name:      name,
		OwnerID:   "user-1",
		Status:    api.StatusRunning,
		Target:    api.TargetKnative,
		ImageRef:  "nginx:latest",
		URL:       "https://example.com",
		Port:      8080,
		EnvVars:    map[string]string{"FOO": "bar"},
		SecretRefs: map[string]string{"DB_PASSWORD": "my-creds:password"},
		Language:   "static",
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}
}

func TestSQLiteStore_CreateAndGet(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	a := testArtifact("a1", "my-app")

	require.NoError(t, s.Create(ctx, a))

	got, err := s.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "a1", got.ID)
	assert.Equal(t, "my-app", got.Name)
	assert.Equal(t, "user-1", got.OwnerID)
	assert.Equal(t, api.StatusRunning, got.Status)
	assert.Equal(t, api.TargetKnative, got.Target)
	assert.Equal(t, "nginx:latest", got.ImageRef)
	assert.Equal(t, 8080, got.Port)
	assert.Equal(t, "bar", got.EnvVars["FOO"])
	assert.Equal(t, "my-creds:password", got.SecretRefs["DB_PASSWORD"])
}

func TestSQLiteStore_GetByName(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	a := testArtifact("a1", "my-app")

	require.NoError(t, s.Create(ctx, a))

	got, err := s.GetByName(ctx, "my-app")
	require.NoError(t, err)
	assert.Equal(t, "a1", got.ID)
	assert.Equal(t, "my-app", got.Name)
}

func TestSQLiteStore_DuplicateName(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, testArtifact("a1", "my-app")))

	err := s.Create(ctx, testArtifact("a2", "my-app"))
	assert.IsType(t, &api.ErrAlreadyExists{}, err)
}

func TestSQLiteStore_Update(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	a := testArtifact("a1", "my-app")

	require.NoError(t, s.Create(ctx, a))

	a.Status = api.StatusFailed
	a.Error = "build failed"
	a.Version = 2
	require.NoError(t, s.Update(ctx, a))

	got, err := s.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, api.StatusFailed, got.Status)
	assert.Equal(t, "build failed", got.Error)
	assert.Equal(t, 2, got.Version)
}

func TestSQLiteStore_UpdateNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	err := s.Update(ctx, testArtifact("nonexistent", "nope"))
	assert.IsType(t, &api.ErrNotFound{}, err)
}

func TestSQLiteStore_Delete(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	a := testArtifact("a1", "my-app")

	require.NoError(t, s.Create(ctx, a))

	// Also add a version to verify cascade delete.
	require.NoError(t, s.CreateVersion(ctx, &api.ArtifactVersion{
		VersionID:  "v1",
		ArtifactID: "a1",
		Version:    1,
		Status:     api.StatusRunning,
		CreatedAt:  time.Now(),
	}))

	require.NoError(t, s.Delete(ctx, "a1"))

	_, err := s.Get(ctx, "a1")
	assert.IsType(t, &api.ErrNotFound{}, err)

	versions, err := s.ListVersions(ctx, "a1")
	require.NoError(t, err)
	assert.Empty(t, versions)
}

func TestSQLiteStore_DeleteNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	err := s.Delete(ctx, "nonexistent")
	assert.IsType(t, &api.ErrNotFound{}, err)
}

func TestSQLiteStore_GetNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent")
	assert.IsType(t, &api.ErrNotFound{}, err)

	_, err = s.GetByName(ctx, "nonexistent")
	assert.IsType(t, &api.ErrNotFound{}, err)
}

func TestSQLiteStore_List(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	a1 := testArtifact("a1", "app-1")
	a1.OwnerID = "alice"
	a1.Status = api.StatusRunning
	require.NoError(t, s.Create(ctx, a1))

	a2 := testArtifact("a2", "app-2")
	a2.OwnerID = "bob"
	a2.Status = api.StatusFailed
	require.NoError(t, s.Create(ctx, a2))

	a3 := testArtifact("a3", "app-3")
	a3.OwnerID = "alice"
	a3.Status = api.StatusRunning
	require.NoError(t, s.Create(ctx, a3))

	// List all (admin view).
	all, err := s.List(ctx, "", "", true)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	// Filter by status.
	running, err := s.List(ctx, "running", "", true)
	require.NoError(t, err)
	assert.Len(t, running, 2)

	// Filter by owner (non-admin).
	aliceOnly, err := s.List(ctx, "", "alice", false)
	require.NoError(t, err)
	assert.Len(t, aliceOnly, 2)
	for _, s := range aliceOnly {
		assert.Equal(t, "alice", s.OwnerID)
	}

	// Filter by owner + status.
	aliceRunning, err := s.List(ctx, "running", "alice", false)
	require.NoError(t, err)
	assert.Len(t, aliceRunning, 2)
}

func TestSQLiteStore_SharedWith(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	a := testArtifact("a1", "shared-app")
	a.OwnerID = "alice"
	a.SharedWith = []string{"bob", "charlie"}
	require.NoError(t, s.Create(ctx, a))

	// Bob can see alice's shared artifact.
	bobList, err := s.List(ctx, "", "bob", false)
	require.NoError(t, err)
	assert.Len(t, bobList, 1)
	assert.Equal(t, "shared-app", bobList[0].Name)

	// Dave cannot see it.
	daveList, err := s.List(ctx, "", "dave", false)
	require.NoError(t, err)
	assert.Empty(t, daveList)

	// Verify SharedWith round-trips through Get.
	got, err := s.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, []string{"bob", "charlie"}, got.SharedWith)
}

func TestSQLiteStore_Versions(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Microsecond)

	require.NoError(t, s.Create(ctx, testArtifact("a1", "my-app")))

	v1 := &api.ArtifactVersion{
		VersionID:  "v1",
		ArtifactID: "a1",
		Version:    1,
		ImageRef:   "img:v1",
		Status:     api.StatusRunning,
		CreatedAt:  now,
		CreatedBy:  "alice",
		EnvVars:    map[string]string{"VER": "1"},
		SecretRefs: map[string]string{"API_KEY": "my-secret:api-key"},
	}
	v2 := &api.ArtifactVersion{
		VersionID:  "v2",
		ArtifactID: "a1",
		Version:    2,
		ImageRef:   "img:v2",
		Status:     api.StatusRunning,
		CreatedAt:  now,
		CreatedBy:  "alice",
	}

	require.NoError(t, s.CreateVersion(ctx, v1))
	require.NoError(t, s.CreateVersion(ctx, v2))

	// ListVersions returns ordered by version ASC.
	versions, err := s.ListVersions(ctx, "a1")
	require.NoError(t, err)
	assert.Len(t, versions, 2)
	assert.Equal(t, 1, versions[0].Version)
	assert.Equal(t, 2, versions[1].Version)
	assert.Equal(t, "img:v1", versions[0].ImageRef)
	assert.Equal(t, map[string]string{"VER": "1"}, versions[0].EnvVars)
	assert.Equal(t, map[string]string{"API_KEY": "my-secret:api-key"}, versions[0].SecretRefs)

	// GetVersion by number.
	got, err := s.GetVersion(ctx, "a1", 2)
	require.NoError(t, err)
	assert.Equal(t, "v2", got.VersionID)
	assert.Equal(t, "img:v2", got.ImageRef)

	// GetVersion not found.
	_, err = s.GetVersion(ctx, "a1", 99)
	assert.IsType(t, &api.ErrVersionNotFound{}, err)
}

func TestSQLiteStore_Persistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist.db")
	ctx := context.Background()

	// Create store, write data, close.
	s1, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	require.NoError(t, s1.Create(ctx, testArtifact("a1", "persist-app")))
	require.NoError(t, s1.Close())

	// Reopen and verify data survived.
	s2, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer s2.Close()

	got, err := s2.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "persist-app", got.Name)
	assert.Equal(t, api.StatusRunning, got.Status)
}
