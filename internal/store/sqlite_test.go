package store

import (
	"context"
	"database/sql"
	"fmt"
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
		ID:         id,
		Name:       name,
		OwnerID:    "user-1",
		Status:     api.StatusRunning,
		Target:     api.TargetKubernetes,
		ImageRef:   "nginx:latest",
		URL:        "https://example.com",
		Port:       8080,
		EnvVars:    map[string]string{"FOO": "bar"},
		SecretRefs: map[string]string{"DB_PASSWORD": "my-creds:password"},
		Language:   "static",
		CreatedAt:  now,
		UpdatedAt:  now,
		Version:    1,
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
	assert.Equal(t, api.TargetKubernetes, got.Target)
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
	all, err := s.List(ctx, ListOptions{AdminView: true})
	require.NoError(t, err)
	assert.Len(t, all.Artifacts, 3)
	assert.Equal(t, 3, all.Total)

	// Filter by status.
	running, err := s.List(ctx, ListOptions{StatusFilter: "running", AdminView: true})
	require.NoError(t, err)
	assert.Len(t, running.Artifacts, 2)

	// Filter by owner (non-admin).
	aliceOnly, err := s.List(ctx, ListOptions{OwnerID: "alice"})
	require.NoError(t, err)
	assert.Len(t, aliceOnly.Artifacts, 2)
	for _, s := range aliceOnly.Artifacts {
		assert.Equal(t, "alice", s.OwnerID)
	}

	// Filter by owner + status.
	aliceRunning, err := s.List(ctx, ListOptions{StatusFilter: "running", OwnerID: "alice"})
	require.NoError(t, err)
	assert.Len(t, aliceRunning.Artifacts, 2)
}

func TestSQLiteStore_SharedWith(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	a := testArtifact("a1", "shared-app")
	a.OwnerID = "alice"
	a.SharedWith = []string{"bob", "charlie"}
	require.NoError(t, s.Create(ctx, a))

	// Bob can see alice's shared artifact.
	bobList, err := s.List(ctx, ListOptions{OwnerID: "bob"})
	require.NoError(t, err)
	assert.Len(t, bobList.Artifacts, 1)
	assert.Equal(t, "shared-app", bobList.Artifacts[0].Name)

	// Dave cannot see it.
	daveList, err := s.List(ctx, ListOptions{OwnerID: "dave"})
	require.NoError(t, err)
	assert.Empty(t, daveList.Artifacts)

	// Verify SharedWith round-trips through Get.
	got, err := s.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, []string{"bob", "charlie"}, got.SharedWith)

	// Update that revokes charlie's access re-syncs the join table: charlie can
	// no longer see it, bob still can.
	a.SharedWith = []string{"bob"}
	require.NoError(t, s.Update(ctx, a))
	charlieList, err := s.List(ctx, ListOptions{OwnerID: "charlie"})
	require.NoError(t, err)
	assert.Empty(t, charlieList.Artifacts, "revoked share must drop from the index")
	bobList, err = s.List(ctx, ListOptions{OwnerID: "bob"})
	require.NoError(t, err)
	assert.Len(t, bobList.Artifacts, 1)

	// The shared-list filter must use the join-table index, not a scan.
	var plan string
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM artifacts WHERE (owner_id = ? OR id IN (SELECT artifact_id FROM artifact_shares WHERE owner = ?))`, "x", "x")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		plan += detail + "\n"
	}
	assert.Contains(t, plan, "idx_artifact_shares_owner", "shared-list subquery should use the owner index; plan:\n"+plan)
}

// TestSQLiteStore_SharesBackfillOnOpen locks in the #80 migration: an upgraded
// DB that has shared_with JSON but an empty artifact_shares index gets the index
// backfilled on open, so the shared-list filter keeps working.
func TestSQLiteStore_SharesBackfillOnOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backfill.db")
	ctx := context.Background()

	s1, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	a := testArtifact("a1", "shared-app")
	a.OwnerID = "alice"
	a.SharedWith = []string{"bob"}
	require.NoError(t, s1.Create(ctx, a))

	// Simulate a pre-migration DB: the JSON share exists but the index doesn't.
	_, err = s1.db.Exec(`DELETE FROM artifact_shares`)
	require.NoError(t, err)
	gone, err := s1.List(ctx, ListOptions{OwnerID: "bob"})
	require.NoError(t, err)
	require.Empty(t, gone.Artifacts, "with the index cleared, bob shouldn't resolve the share")
	require.NoError(t, s1.Close())

	// Reopen: the backfill repopulates artifact_shares from shared_with JSON.
	s2, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer s2.Close()
	back, err := s2.List(ctx, ListOptions{OwnerID: "bob"})
	require.NoError(t, err)
	assert.Len(t, back.Artifacts, 1, "backfill should restore bob's shared view")
}

// TestSQLiteStore_ListPagination locks in the #80 pagination: limit<=0 returns
// all, limit>0 pages with offset, for both ListUsers and ListDepartments.
func TestSQLiteStore_ListPagination(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	for i := 0; i < 5; i++ {
		require.NoError(t, s.CreateUser(ctx, &api.User{
			ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("u%d", i),
			Role: "user", Status: "active", CreatedAt: now, UpdatedAt: now,
		}))
	}

	all, err := s.ListUsers(ctx, "", 0, 0) // 0 = all
	require.NoError(t, err)
	assert.Len(t, all, 5)

	page1, err := s.ListUsers(ctx, "", 2, 0)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, "u0", page1[0].Name)
	assert.Equal(t, "u1", page1[1].Name)

	page2, err := s.ListUsers(ctx, "", 2, 2)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.Equal(t, "u2", page2[0].Name)

	for i := 0; i < 3; i++ {
		require.NoError(t, s.CreateDepartment(ctx, &api.Department{
			ID: fmt.Sprintf("d-%d", i), Name: fmt.Sprintf("dept%d", i),
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	depAll, err := s.ListDepartments(ctx, 0, 0)
	require.NoError(t, err)
	assert.Len(t, depAll, 3)
	depPage, err := s.ListDepartments(ctx, 1, 1)
	require.NoError(t, err)
	require.Len(t, depPage, 1)
	assert.Equal(t, "dept1", depPage[0].Name)
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

// TestSQLiteStore_PerfIndexes locks in the indexes added for issue #80: the
// created_at ordering path and the api_key_hash auth lookup must be indexed so
// they don't degrade into full-table scans as the tables grow.
func TestSQLiteStore_PerfIndexes(t *testing.T) {
	s := newTestSQLiteStore(t)

	want := []string{
		"idx_artifacts_created_at",
		"idx_artifacts_status_created",
		"idx_users_api_key_hash",
	}
	for _, name := range want {
		var got string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name,
		).Scan(&got)
		require.NoErrorf(t, err, "index %q should exist", name)
		assert.Equal(t, name, got)
	}

	// The api_key_hash lookup must actually use its index, not scan the table.
	var plan string
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM users WHERE api_key_hash = ? AND api_key_hash != ''`, "x")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		plan += detail + "\n"
	}
	assert.Contains(t, plan, "idx_users_api_key_hash", "api-key lookup should use its index; plan was:\n"+plan)
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

// TestSQLiteStore_PoolPragmas guards against a regression where the
// per-connection pragmas were applied with a plain Exec: only whichever
// connection the pool handed out got busy_timeout/foreign_keys, and every
// other pooled connection ran without them. The pragmas now live in the DSN,
// so every connection must report them — verify by holding the pool's full
// complement of connections open at once and checking each one.
func TestSQLiteStore_PoolPragmas(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// The pool bound must have taken effect.
	require.Equal(t, sqlitePoolSize, s.db.Stats().MaxOpenConnections)

	// Holding sqlitePoolSize conns simultaneously forces the pool to open
	// that many distinct connections.
	conns := make([]*sql.Conn, sqlitePoolSize)
	for i := range conns {
		c, err := s.db.Conn(ctx)
		require.NoError(t, err)
		defer c.Close()
		conns[i] = c
	}
	require.Equal(t, sqlitePoolSize, s.db.Stats().OpenConnections)

	for i, c := range conns {
		var (
			journalMode string
			synchronous int
			busyTimeout int
			foreignKeys int
			cacheSize   int
			tempStore   int
			mmapSize    int64
		)
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode))
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous))
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout))
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys))
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA cache_size`).Scan(&cacheSize))
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA temp_store`).Scan(&tempStore))
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA mmap_size`).Scan(&mmapSize))

		assert.Equalf(t, "wal", journalMode, "conn %d journal_mode", i)
		assert.Equalf(t, 1, synchronous, "conn %d synchronous (1 = NORMAL)", i)
		assert.Equalf(t, 5000, busyTimeout, "conn %d busy_timeout", i)
		assert.Equalf(t, 1, foreignKeys, "conn %d foreign_keys", i)
		assert.Equalf(t, -64000, cacheSize, "conn %d cache_size", i)
		assert.Equalf(t, 2, tempStore, "conn %d temp_store (2 = MEMORY)", i)
		assert.Equalf(t, int64(268435456), mmapSize, "conn %d mmap_size", i)
	}
}
