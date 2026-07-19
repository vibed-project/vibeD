package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver

	"github.com/vibed-project/vibeD/pkg/api"
)

const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
        id           TEXT PRIMARY KEY,
        name         TEXT UNIQUE NOT NULL,
        owner_id     TEXT NOT NULL DEFAULT '',
        namespace    TEXT NOT NULL DEFAULT '',
        status       TEXT NOT NULL,
        target       TEXT NOT NULL DEFAULT '',
        mode         TEXT NOT NULL DEFAULT '',
        image_ref    TEXT NOT NULL DEFAULT '',
        url          TEXT NOT NULL DEFAULT '',
        port         INTEGER NOT NULL DEFAULT 0,
        env_vars     TEXT NOT NULL DEFAULT '{}',
        secret_refs  TEXT NOT NULL DEFAULT '{}',	language     TEXT NOT NULL DEFAULT '',
	static_files TEXT NOT NULL DEFAULT '',
	error        TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	storage_ref  TEXT NOT NULL DEFAULT '',
	version      INTEGER NOT NULL DEFAULT 0,
	version_id   TEXT NOT NULL DEFAULT '',
	shared_with  TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS users (
	id         TEXT PRIMARY KEY,
	name       TEXT UNIQUE NOT NULL,
	email      TEXT NOT NULL DEFAULT '',
	role       TEXT NOT NULL DEFAULT 'user',
	status     TEXT NOT NULL DEFAULT 'active',
	provider   TEXT NOT NULL DEFAULT 'local',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS share_links (
	token       TEXT PRIMARY KEY,
	artifact_id TEXT NOT NULL,
	created_by  TEXT NOT NULL,
	password    TEXT NOT NULL DEFAULT '',
	expires_at  TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	revoked     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS artifact_versions (
	version_id  TEXT PRIMARY KEY,
	artifact_id TEXT NOT NULL,
	version     INTEGER NOT NULL,
	image_ref   TEXT NOT NULL DEFAULT '',
	storage_ref TEXT NOT NULL DEFAULT '',
	env_vars    TEXT NOT NULL DEFAULT '{}',
	secret_refs TEXT NOT NULL DEFAULT '{}',
	status      TEXT NOT NULL,
	url         TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	created_by  TEXT NOT NULL DEFAULT '',
	UNIQUE(artifact_id, version)
);

CREATE INDEX IF NOT EXISTS idx_artifacts_status ON artifacts(status);
CREATE INDEX IF NOT EXISTS idx_artifacts_owner_id ON artifacts(owner_id);
-- The List hot path orders by created_at DESC, often with a status filter.
-- A plain created_at index serves the unfiltered (admin) listing; the composite
-- serves "WHERE status = ? ORDER BY created_at DESC" without a filesort.
CREATE INDEX IF NOT EXISTS idx_artifacts_created_at ON artifacts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_artifacts_status_created ON artifacts(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_artifact_versions_artifact_id ON artifact_versions(artifact_id);
CREATE INDEX IF NOT EXISTS idx_share_links_artifact_id ON share_links(artifact_id);

-- artifact_shares normalizes artifacts.shared_with (a JSON array) into an
-- indexed join table so the List "shared with me" filter is an indexed lookup
-- instead of a "shared_with LIKE '%\"owner\"%'" full-table scan (#80). The JSON
-- column stays the source of truth for an artifact's SharedWith; this table is a
-- derived query index kept in sync on every write and backfilled on open.
CREATE TABLE IF NOT EXISTS artifact_shares (
        artifact_id TEXT NOT NULL,
        owner       TEXT NOT NULL,
        PRIMARY KEY (artifact_id, owner)
);
CREATE INDEX IF NOT EXISTS idx_artifact_shares_owner ON artifact_shares(owner);

CREATE TABLE IF NOT EXISTS departments (
        id         TEXT PRIMARY KEY,
        name       TEXT UNIQUE NOT NULL,
        namespace  TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
        id              TEXT PRIMARY KEY,
        ts              TEXT NOT NULL,
        actor           TEXT NOT NULL DEFAULT '',
        action          TEXT NOT NULL DEFAULT '',
        target          TEXT NOT NULL DEFAULT '',
        outcome         TEXT NOT NULL DEFAULT '',
        detail          TEXT NOT NULL DEFAULT '',
        tenant_id       TEXT NOT NULL DEFAULT '',
        session_id      TEXT NOT NULL DEFAULT '',
        source_hash     TEXT NOT NULL DEFAULT '',
        policy_decision TEXT NOT NULL DEFAULT '',
        before_state    TEXT NOT NULL DEFAULT '',
        after_state     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_events(ts);`

// SQLiteStore is a persistent ArtifactStore backed by SQLite.
type SQLiteStore struct {
	db *sql.DB

	// Prepared statements for hot paths
	stmtGetArtifact       *sql.Stmt
	stmtGetArtifactByName *sql.Stmt
	stmtCreateArtifact    *sql.Stmt
	stmtUpdateArtifact    *sql.Stmt
	stmtDeleteArtifact    *sql.Stmt
	stmtCreateVersion     *sql.Stmt
	stmtListVersions      *sql.Stmt
	stmtGetUser           *sql.Stmt
	stmtGetUserByName     *sql.Stmt
}

func init() {
	Register("sqlite", func(d Deps) (ArtifactStore, error) { return NewSQLiteStore(d.SQLitePath) })
}

// sqlitePoolSize bounds the database/sql connection pool. SQLite is a
// single-writer file DB, so more connections only help concurrent readers
// (which WAL allows on separate connections); a small bound keeps read
// parallelism without piling up writers that would just queue on the file
// lock and burn busy_timeout.
const sqlitePoolSize = 4

// sqliteDSNPragmas are applied by the modernc.org/sqlite driver to every
// new pooled connection (format: ?_pragma=name(value), busy_timeout first).
// synchronous, busy_timeout, foreign_keys, cache_size, temp_store and
// mmap_size are all per-connection settings, so they must live in the DSN —
// a plain Exec would only configure whichever connection the pool handed
// out. journal_mode=WAL is persistent in the DB file but harmless to
// (re)apply per connection.
const sqliteDSNPragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=cache_size(-64000)" + // 64 MB page cache per connection
	"&_pragma=temp_store(MEMORY)" +
	"&_pragma=mmap_size(268435456)" // 256 MB mmap'd reads

// NewSQLiteStore opens (or creates) a SQLite database at the given path
// and initializes the schema. Uses WAL mode for concurrent read performance.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving sqlite path: %w", err)
	}

	db, err := sql.Open("sqlite", absPath+sqliteDSNPragmas)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}

	// Bound the pool (see sqlitePoolSize) and keep the connections idle
	// rather than closing them: reopening a SQLite connection is cheap but
	// re-warming its page cache is not, and connections never go stale.
	db.SetMaxOpenConns(sqlitePoolSize)
	db.SetMaxIdleConns(sqlitePoolSize)
	db.SetConnMaxLifetime(0)

	// Force a connection now so a bad path or DSN pragma fails here with a
	// clear error instead of surfacing on the first query.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to sqlite db: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
	        db.Close()
	        return nil, fmt.Errorf("initializing schema: %w", err)
	}

	// Migrations
	if _, err := db.Exec(`ALTER TABLE artifacts ADD COLUMN namespace TEXT NOT NULL DEFAULT ''`); err != nil {
	        // Ignore duplicate column error
	}
	if _, err := db.Exec(`ALTER TABLE artifacts ADD COLUMN mode TEXT NOT NULL DEFAULT ''`); err != nil {
	        // Ignore duplicate column error — added for the Instant Preview fast path
	}
	if _, err := db.Exec(`ALTER TABLE departments ADD COLUMN namespace TEXT NOT NULL DEFAULT ''`); err != nil {
	        // Ignore duplicate column error
	}

	// Migration: add department_id to users if missing
	if !columnExists(db, "users", "department_id") {
		if _, err := db.Exec(`ALTER TABLE users ADD COLUMN department_id TEXT NOT NULL DEFAULT ''`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrating users table: %w", err)
		}
	}

	// Migration: add api_key_hash to users if missing
	if !columnExists(db, "users", "api_key_hash") {
		if _, err := db.Exec(`ALTER TABLE users ADD COLUMN api_key_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrating users table (api_key_hash): %w", err)
		}
	}

	// Index the api_key_hash auth-lookup path (GetUserByAPIKeyHash runs on every
	// API-key request). Created here rather than in the base schema because the
	// column is added by the migration above. Partial index: the lookup always
	// filters api_key_hash != '', and most rows have no key, so this stays small.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_users_api_key_hash ON users(api_key_hash) WHERE api_key_hash != ''`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating api_key_hash index: %w", err)
	}

	// Migration: add the enriched audit columns if missing (older DBs).
	for _, col := range []string{"tenant_id", "session_id", "source_hash", "policy_decision", "before_state", "after_state"} {
		if !columnExists(db, "audit_events", col) {
			if _, err := db.Exec(`ALTER TABLE audit_events ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrating audit_events table (%s): %w", col, err)
			}
		}
	}

	// Backfill the artifact_shares index from the existing shared_with JSON on an
	// upgraded DB. Guarded on an empty table so it runs once; INSERT OR IGNORE +
	// the composite PK keep it idempotent regardless. json_each expands each
	// artifact's shared_with array into (artifact_id, owner) rows.
	var shareRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM artifact_shares`).Scan(&shareRows); err != nil {
		db.Close()
		return nil, fmt.Errorf("checking artifact_shares: %w", err)
	}
	if shareRows == 0 {
		if _, err := db.Exec(`INSERT OR IGNORE INTO artifact_shares (artifact_id, owner)
			SELECT a.id, je.value FROM artifacts a, json_each(a.shared_with) je
			WHERE a.shared_with != '' AND a.shared_with != '[]'`); err != nil {
			db.Close()
			return nil, fmt.Errorf("backfilling artifact_shares: %w", err)
		}
	}

	s := &SQLiteStore{db: db}

	// Prepare hot-path statements
	if err := s.prepareStatements(); err != nil {
		db.Close()
		return nil, fmt.Errorf("preparing statements: %w", err)
	}

	return s, nil
}

func (s *SQLiteStore) prepareStatements() error {
	var err error

	s.stmtGetArtifact, err = s.db.Prepare(`SELECT id, name, owner_id, namespace, status, target, mode, image_ref, url, port, env_vars, secret_refs, language, static_files, error, created_at, updated_at, storage_ref, version, version_id, shared_with FROM artifacts WHERE id = ?`)
	if err != nil {
	        return err
	}

	s.stmtGetArtifactByName, err = s.db.Prepare(`SELECT id, name, owner_id, namespace, status, target, mode, image_ref, url, port, env_vars, secret_refs, language, static_files, error, created_at, updated_at, storage_ref, version, version_id, shared_with FROM artifacts WHERE name = ?`)
	if err != nil {
	        return err
	}

	s.stmtCreateArtifact, err = s.db.Prepare(`
	        INSERT INTO artifacts (
	                id, name, owner_id, namespace, status, target, mode, image_ref, url, port, env_vars, secret_refs, language, static_files, error, created_at, updated_at, storage_ref, version, version_id, shared_with
	        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
	        return err
	}

	s.stmtUpdateArtifact, err = s.db.Prepare(`
	        UPDATE artifacts SET
	                name = ?, owner_id = ?, namespace = ?, status = ?, target = ?, mode = ?, image_ref = ?, url = ?, port = ?,
	                env_vars = ?, secret_refs = ?, language = ?, static_files = ?, error = ?, created_at = ?, updated_at = ?,
	                storage_ref = ?, version = ?, version_id = ?, shared_with = ?
	        WHERE id = ?
	`)
	if err != nil {
		return err
	}

	s.stmtDeleteArtifact, err = s.db.Prepare(`DELETE FROM artifacts WHERE id = ?`)
	if err != nil {
		return err
	}

	s.stmtCreateVersion, err = s.db.Prepare(`
		INSERT INTO artifact_versions (
			version_id, artifact_id, version, image_ref, storage_ref, env_vars, secret_refs, status, url, created_at, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}

	s.stmtListVersions, err = s.db.Prepare(`
		SELECT version_id, artifact_id, version, image_ref, storage_ref, env_vars, secret_refs, status, url, created_at, created_by 
		FROM artifact_versions 
		WHERE artifact_id = ? 
		ORDER BY version ASC
	`)
	if err != nil {
		return err
	}

	s.stmtGetUser, err = s.db.Prepare(`SELECT id, name, email, role, status, provider, department_id, api_key_hash, created_at, updated_at FROM users WHERE id = ?`)
	if err != nil {
		return err
	}

	s.stmtGetUserByName, err = s.db.Prepare(`SELECT id, name, email, role, status, provider, department_id, api_key_hash, created_at, updated_at FROM users WHERE name = ? COLLATE NOCASE`)
	if err != nil {
		return err
	}

	return nil
}

// Close closes the database and all prepared statements.
func (s *SQLiteStore) Close() error {
	if s.stmtGetArtifact != nil {
		s.stmtGetArtifact.Close()
	}
	if s.stmtGetArtifactByName != nil {
		s.stmtGetArtifactByName.Close()
	}
	if s.stmtCreateArtifact != nil {
		s.stmtCreateArtifact.Close()
	}
	if s.stmtUpdateArtifact != nil {
		s.stmtUpdateArtifact.Close()
	}
	if s.stmtDeleteArtifact != nil {
		s.stmtDeleteArtifact.Close()
	}
	if s.stmtCreateVersion != nil {
		s.stmtCreateVersion.Close()
	}
	if s.stmtListVersions != nil {
		s.stmtListVersions.Close()
	}
	if s.stmtGetUser != nil {
		s.stmtGetUser.Close()
	}
	if s.stmtGetUserByName != nil {
		s.stmtGetUserByName.Close()
	}
	return s.db.Close()
}

func (s *SQLiteStore) Create(ctx context.Context, artifact *api.Artifact) error {
	envVars, err := json.Marshal(artifact.EnvVars)
	if err != nil {
		return fmt.Errorf("marshaling env_vars: %w", err)
	}
	secretRefs, err := json.Marshal(artifact.SecretRefs)
	if err != nil {
		return fmt.Errorf("marshaling secret_refs: %w", err)
	}
	sharedWith, err := json.Marshal(artifact.SharedWith)
	if err != nil {
		return fmt.Errorf("marshaling shared_with: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Stmt(s.stmtCreateArtifact).ExecContext(ctx,
	        artifact.ID, artifact.Name, artifact.OwnerID, artifact.Namespace, string(artifact.Status),
	        string(artifact.Target), string(artifact.Mode), artifact.ImageRef, artifact.URL, artifact.Port,
	        string(envVars), string(secretRefs), artifact.Language, artifact.StaticFiles, artifact.Error,
	        artifact.CreatedAt.Format(time.RFC3339Nano), artifact.UpdatedAt.Format(time.RFC3339Nano),
	        artifact.StorageRef, artifact.Version, artifact.VersionID, string(sharedWith),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return &api.ErrAlreadyExists{Name: artifact.Name}
		}
		return fmt.Errorf("inserting artifact: %w", err)
	}
	if err := syncArtifactShares(ctx, tx, artifact.ID, artifact.SharedWith); err != nil {
		return err
	}
	return tx.Commit()
}

// syncArtifactShares makes the artifact_shares index rows for artifactID exactly
// match shared (the artifact's SharedWith). Runs inside the artifact's write
// transaction so the JSON column and its index stay consistent.
func syncArtifactShares(ctx context.Context, tx *sql.Tx, artifactID string, shared []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_shares WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("clearing artifact shares: %w", err)
	}
	for _, owner := range shared {
		if owner == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO artifact_shares (artifact_id, owner) VALUES (?, ?)`,
			artifactID, owner); err != nil {
			return fmt.Errorf("inserting artifact share: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*api.Artifact, error) {
	row := s.stmtGetArtifact.QueryRowContext(ctx, id)

	a, err := scanArtifact(row)
	if err == sql.ErrNoRows {
		return nil, &api.ErrNotFound{ArtifactID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("querying artifact: %w", err)
	}
	return a, nil
}

func (s *SQLiteStore) GetByName(ctx context.Context, name string) (*api.Artifact, error) {
	row := s.stmtGetArtifactByName.QueryRowContext(ctx, name)

	a, err := scanArtifact(row)
	if err == sql.ErrNoRows {
		return nil, &api.ErrNotFound{ArtifactID: name}
	}
	if err != nil {
		return nil, fmt.Errorf("querying artifact by name: %w", err)
	}
	return a, nil
}

func (s *SQLiteStore) List(ctx context.Context, opts ListOptions) (*ListResult, error) {
	var conditions []string
	var args []interface{}

	if opts.StatusFilter != "" && opts.StatusFilter != "all" {
		conditions = append(conditions, "status = ?")
		args = append(args, opts.StatusFilter)
	}

	if !opts.AdminView && opts.OwnerID != "" {
		// Owned directly, or shared with the caller via the indexed join table.
		// The IN-subquery hits idx_artifact_shares_owner instead of the old
		// "shared_with LIKE '%\"owner\"%'" full-table scan (#80).
		conditions = append(conditions, "(owner_id = ? OR id IN (SELECT artifact_id FROM artifact_shares WHERE owner = ?))")
		args = append(args, opts.OwnerID, opts.OwnerID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count first
	countQuery := "SELECT COUNT(*) FROM artifacts" + whereClause
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("counting artifacts: %w", err)
	}

	// Fetch rows with ordering and optional pagination
	query := `SELECT id, name, owner_id, namespace, status, target, mode, url, created_at, updated_at, version, shared_with FROM artifacts` + whereClause + ` ORDER BY created_at DESC`
	queryArgs := append([]interface{}{}, args...)
	if opts.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		queryArgs = append(queryArgs, opts.Limit, opts.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	defer rows.Close()

	var results []api.ArtifactSummary
	if results == nil {
		results = make([]api.ArtifactSummary, 0)
	}

	for rows.Next() {
		var (
			summary              api.ArtifactSummary
			status, target, mode string
			createdAt, updatedAt string
			sharedWithJSON       string
		)
		if err := rows.Scan(
		        &summary.ID, &summary.Name, &summary.OwnerID, &summary.Namespace,
		        &status, &target, &mode, &summary.URL,
		        &createdAt, &updatedAt, &summary.Version, &sharedWithJSON,
		); err != nil {			return nil, fmt.Errorf("scanning artifact summary: %w", err)
		}

		summary.Status = api.ArtifactStatus(status)
		summary.Target = api.DeploymentTarget(target)
		summary.Mode = api.DeployMode(mode)
		summary.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		summary.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if err := json.Unmarshal([]byte(sharedWithJSON), &summary.SharedWith); err != nil && sharedWithJSON != "" && sharedWithJSON != "null" {
			return nil, fmt.Errorf("unmarshaling shared_with: %w", err)
		}
		if summary.SharedWith == nil {
			summary.SharedWith = []string{}
		}

		results = append(results, summary)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &ListResult{Artifacts: results, Total: total}, nil
}

func (s *SQLiteStore) Update(ctx context.Context, artifact *api.Artifact) error {
	envVars, err := json.Marshal(artifact.EnvVars)
	if err != nil {
		return fmt.Errorf("marshaling env_vars: %w", err)
	}
	secretRefs, err := json.Marshal(artifact.SecretRefs)
	if err != nil {
		return fmt.Errorf("marshaling secret_refs: %w", err)
	}
	sharedWith, err := json.Marshal(artifact.SharedWith)
	if err != nil {
		return fmt.Errorf("marshaling shared_with: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Stmt(s.stmtUpdateArtifact).ExecContext(ctx,
	        artifact.Name, artifact.OwnerID, artifact.Namespace, string(artifact.Status),
	        string(artifact.Target), string(artifact.Mode), artifact.ImageRef, artifact.URL, artifact.Port,
	        string(envVars), string(secretRefs), artifact.Language, artifact.StaticFiles, artifact.Error,
	        artifact.CreatedAt.Format(time.RFC3339Nano), artifact.UpdatedAt.Format(time.RFC3339Nano),
	        artifact.StorageRef, artifact.Version, artifact.VersionID, string(sharedWith),
	        artifact.ID,
	)
	if err != nil {
		return fmt.Errorf("updating artifact: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &api.ErrNotFound{ArtifactID: artifact.ID}
	}
	if err := syncArtifactShares(ctx, tx, artifact.ID, artifact.SharedWith); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Stmt(s.stmtDeleteArtifact).ExecContext(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting artifact: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &api.ErrNotFound{ArtifactID: id}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM artifact_versions WHERE artifact_id = ?", id); err != nil {
		return fmt.Errorf("deleting versions: %w", err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM artifact_shares WHERE artifact_id = ?", id); err != nil {
		return fmt.Errorf("deleting shares: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) CreateVersion(ctx context.Context, version *api.ArtifactVersion) error {
	envVars, err := json.Marshal(version.EnvVars)
	if err != nil {
		return fmt.Errorf("marshaling env_vars: %w", err)
	}
	secretRefs, err := json.Marshal(version.SecretRefs)
	if err != nil {
		return fmt.Errorf("marshaling secret_refs: %w", err)
	}

	_, err = s.stmtCreateVersion.ExecContext(ctx,
		version.VersionID, version.ArtifactID, version.Version,
		version.ImageRef, version.StorageRef, string(envVars), string(secretRefs),
		string(version.Status), version.URL,
		version.CreatedAt.Format(time.RFC3339Nano), version.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("inserting version: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListVersions(ctx context.Context, artifactID string) ([]api.ArtifactVersion, error) {
	rows, err := s.stmtListVersions.QueryContext(ctx, artifactID)
	if err != nil {
		return nil, fmt.Errorf("listing versions: %w", err)
	}
	defer rows.Close()

	var versions []api.ArtifactVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, *v)
	}
	return versions, rows.Err()
}

func (s *SQLiteStore) GetVersion(ctx context.Context, artifactID string, version int) (*api.ArtifactVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version_id, artifact_id, version, image_ref, storage_ref,
			env_vars, secret_refs, status, url, created_at, created_by
		FROM artifact_versions WHERE artifact_id = ? AND version = ?`, artifactID, version)
	if err != nil {
		return nil, fmt.Errorf("querying version: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, &api.ErrVersionNotFound{ArtifactID: artifactID, Version: version}
	}
	return scanVersion(rows)
}

// --- helpers ---

// scanner is implemented by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanArtifact(row scanner) (*api.Artifact, error) {
	var (
		a              api.Artifact
		status, target string
		mode           string
		envVarsJSON    string
		secretRefsJSON string
		sharedWithJSON string
		createdAt      string
		updatedAt      string
	)

	err := row.Scan(
	        &a.ID, &a.Name, &a.OwnerID, &a.Namespace, &status, &target, &mode,
	        &a.ImageRef, &a.URL, &a.Port, &envVarsJSON, &secretRefsJSON,
	        &a.Language, &a.StaticFiles, &a.Error,
	        &createdAt, &updatedAt, &a.StorageRef,
	        &a.Version, &a.VersionID, &sharedWithJSON,
	)
	if err != nil {
		return nil, err
	}

	a.Status = api.ArtifactStatus(status)
	a.Target = api.DeploymentTarget(target)
	a.Mode = api.DeployMode(mode)
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	if envVarsJSON != "" && envVarsJSON != "{}" {
		if err := json.Unmarshal([]byte(envVarsJSON), &a.EnvVars); err != nil {
			return nil, fmt.Errorf("unmarshaling env_vars: %w", err)
		}
	}
	if secretRefsJSON != "" && secretRefsJSON != "{}" {
		if err := json.Unmarshal([]byte(secretRefsJSON), &a.SecretRefs); err != nil {
			return nil, fmt.Errorf("unmarshaling secret_refs: %w", err)
		}
	}
	if sharedWithJSON != "" && sharedWithJSON != "[]" {
		if err := json.Unmarshal([]byte(sharedWithJSON), &a.SharedWith); err != nil {
			return nil, fmt.Errorf("unmarshaling shared_with: %w", err)
		}
	}

	return &a, nil
}

func scanVersion(rows *sql.Rows) (*api.ArtifactVersion, error) {
	var (
		v              api.ArtifactVersion
		status         string
		envVarsJSON    string
		secretRefsJSON string
		createdAt      string
	)

	err := rows.Scan(
		&v.VersionID, &v.ArtifactID, &v.Version,
		&v.ImageRef, &v.StorageRef, &envVarsJSON, &secretRefsJSON,
		&status, &v.URL, &createdAt, &v.CreatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("scanning version: %w", err)
	}

	v.Status = api.ArtifactStatus(status)
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)

	if envVarsJSON != "" && envVarsJSON != "{}" {
		if err := json.Unmarshal([]byte(envVarsJSON), &v.EnvVars); err != nil {
			return nil, fmt.Errorf("unmarshaling env_vars: %w", err)
		}
	}
	if secretRefsJSON != "" && secretRefsJSON != "{}" {
		if err := json.Unmarshal([]byte(secretRefsJSON), &v.SecretRefs); err != nil {
			return nil, fmt.Errorf("unmarshaling secret_refs: %w", err)
		}
	}

	return &v, nil
}

func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// --- User CRUD ---

func (s *SQLiteStore) CreateUser(ctx context.Context, user *api.User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, name, email, role, status, provider, department_id, api_key_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.Name, user.Email, user.Role, user.Status, user.Provider, user.DepartmentID,
		user.APIKeyHash,
		user.CreatedAt.Format(time.RFC3339Nano), user.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("user %q already exists", user.Name)
		}
		return fmt.Errorf("inserting user: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetUser(ctx context.Context, id string) (*api.User, error) {
	var u api.User
	var createdAt, updatedAt string
	err := s.stmtGetUser.QueryRowContext(ctx, id).Scan(
		&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &u.Provider, &u.DepartmentID, &u.APIKeyHash, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("querying user: %w", err)
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &u, nil
}

func (s *SQLiteStore) GetUserByName(ctx context.Context, name string) (*api.User, error) {
	var u api.User
	var createdAt, updatedAt string
	err := s.stmtGetUserByName.QueryRowContext(ctx, name).Scan(
		&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &u.Provider, &u.DepartmentID, &u.APIKeyHash, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("querying user by name: %w", err)
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &u, nil
}

// appendPage adds LIMIT/OFFSET to query when limit > 0 (limit <= 0 means "all",
// matching artifact ListOptions). A negative offset is clamped to 0.
func appendPage(query string, args []interface{}, limit, offset int) (string, []interface{}) {
	if limit <= 0 {
		return query, args
	}
	if offset < 0 {
		offset = 0
	}
	return query + " LIMIT ? OFFSET ?", append(args, limit, offset)
}

func (s *SQLiteStore) ListUsers(ctx context.Context, departmentID string, limit, offset int) ([]api.User, error) {
	query := `SELECT id, name, email, role, status, provider, department_id, created_at, updated_at FROM users`
	var args []interface{}
	if departmentID != "" {
		query += ` WHERE department_id = ?`
		args = append(args, departmentID)
	}
	query += ` ORDER BY name`
	query, args = appendPage(query, args, limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	var users []api.User
	for rows.Next() {
		var u api.User
		var createdAt, updatedAt string
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &u.Provider, &u.DepartmentID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning user: %w", err)
		}
		u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *SQLiteStore) GetUserByAPIKeyHash(ctx context.Context, hash string) (*api.User, error) {
	var u api.User
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, role, status, provider, department_id, api_key_hash, created_at, updated_at
		 FROM users WHERE api_key_hash = ? AND api_key_hash != ''`,
		hash,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &u.Provider, &u.DepartmentID, &u.APIKeyHash, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("user not found by API key hash: %w", err)
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &u, nil
}

func (s *SQLiteStore) UpdateUser(ctx context.Context, user *api.User) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET name=?, email=?, role=?, status=?, department_id=?, updated_at=? WHERE id=?`,
		user.Name, user.Email, user.Role, user.Status, user.DepartmentID,
		user.UpdatedAt.Format(time.RFC3339Nano), user.ID,
	)
	if err != nil {
		return fmt.Errorf("updating user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", user.ID)
	}
	return nil
}

// --- Share Link CRUD ---

func (s *SQLiteStore) CreateShareLink(ctx context.Context, link *api.ShareLink, passwordHash string) error {
	expiresAt := ""
	if link.ExpiresAt != nil {
		expiresAt = link.ExpiresAt.Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO share_links (token, artifact_id, created_by, password, expires_at, created_at) VALUES (?,?,?,?,?,?)`,
		link.Token, link.ArtifactID, link.CreatedBy,
		passwordHash, expiresAt,
		link.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("creating share link: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetShareLink(ctx context.Context, token string) (*api.ShareLink, string, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT token, artifact_id, created_by, password, expires_at, created_at, revoked FROM share_links WHERE token=?`, token)

	var link api.ShareLink
	var passwordHash, expiresAtStr, createdAtStr string
	var revoked int
	if err := row.Scan(&link.Token, &link.ArtifactID, &link.CreatedBy, &passwordHash, &expiresAtStr, &createdAtStr, &revoked); err != nil {
		if err == sql.ErrNoRows {
			return nil, "", &api.ErrShareLinkNotFound{Token: token}
		}
		return nil, "", fmt.Errorf("getting share link: %w", err)
	}

	link.HasPassword = passwordHash != ""
	link.Revoked = revoked != 0
	link.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
	if expiresAtStr != "" {
		t, _ := time.Parse(time.RFC3339, expiresAtStr)
		link.ExpiresAt = &t
	}
	return &link, passwordHash, nil
}

func (s *SQLiteStore) ListShareLinks(ctx context.Context, artifactID string, limit, offset int) ([]api.ShareLink, error) {
	query, args := appendPage(
		`SELECT token, artifact_id, created_by, password, expires_at, created_at, revoked FROM share_links WHERE artifact_id=? ORDER BY created_at DESC`,
		[]interface{}{artifactID}, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing share links: %w", err)
	}
	defer rows.Close()

	var links []api.ShareLink
	for rows.Next() {
		var link api.ShareLink
		var passwordHash, expiresAtStr, createdAtStr string
		var revoked int
		if err := rows.Scan(&link.Token, &link.ArtifactID, &link.CreatedBy, &passwordHash, &expiresAtStr, &createdAtStr, &revoked); err != nil {
			return nil, fmt.Errorf("scanning share link: %w", err)
		}
		link.HasPassword = passwordHash != ""
		link.Revoked = revoked != 0
		link.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		if expiresAtStr != "" {
			t, _ := time.Parse(time.RFC3339, expiresAtStr)
			link.ExpiresAt = &t
		}
		links = append(links, link)
	}
	return links, nil
}

func (s *SQLiteStore) RevokeShareLink(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE share_links SET revoked=1 WHERE token=?`, token)
	if err != nil {
		return fmt.Errorf("revoking share link: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &api.ErrShareLinkNotFound{Token: token}
	}
	return nil
}

// --- Department CRUD ---

func (s *SQLiteStore) CreateDepartment(ctx context.Context, dept *api.Department) error {
	_, err := s.db.ExecContext(ctx,
	        `INSERT INTO departments (id, name, namespace, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
	        dept.ID, dept.Name, dept.Namespace,
	        dept.CreatedAt.Format(time.RFC3339Nano), dept.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("department %q already exists", dept.Name)
		}
		return fmt.Errorf("inserting department: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetDepartment(ctx context.Context, id string) (*api.Department, error) {
	var d api.Department
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx,
	        `SELECT id, name, namespace, created_at, updated_at FROM departments WHERE id = ?`, id,
	).Scan(&d.ID, &d.Name, &d.Namespace, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("department %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("querying department: %w", err)
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &d, nil
}

func (s *SQLiteStore) GetDepartmentByName(ctx context.Context, name string) (*api.Department, error) {
	var d api.Department
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx,
	        `SELECT id, name, namespace, created_at, updated_at FROM departments WHERE name = ?`, name,
	).Scan(&d.ID, &d.Name, &d.Namespace, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("department %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("querying department by name: %w", err)
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &d, nil
}

func (s *SQLiteStore) ListDepartments(ctx context.Context, limit, offset int) ([]api.Department, error) {
	query, args := appendPage(`SELECT id, name, namespace, created_at, updated_at FROM departments ORDER BY name`, nil, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
	        return nil, fmt.Errorf("listing departments: %w", err)
	}
	defer rows.Close()

	var depts []api.Department
	for rows.Next() {
	        var d api.Department
	        var createdAt, updatedAt string
	        if err := rows.Scan(&d.ID, &d.Name, &d.Namespace, &createdAt, &updatedAt); err != nil {
	                return nil, fmt.Errorf("scanning department: %w", err)
	        }
		d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		depts = append(depts, d)
	}
	return depts, rows.Err()
}

func (s *SQLiteStore) UpdateDepartment(ctx context.Context, dept *api.Department) error {
	res, err := s.db.ExecContext(ctx,
	        `UPDATE departments SET name=?, namespace=?, updated_at=? WHERE id=?`,
	        dept.Name, dept.Namespace, dept.UpdatedAt.Format(time.RFC3339Nano), dept.ID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("department %q already exists", dept.Name)
		}
		return fmt.Errorf("updating department: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("department %q not found", dept.ID)
	}
	return nil
}

func (s *SQLiteStore) DeleteDepartment(ctx context.Context, id string) error {
	// Clear department_id on all users in this department
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET department_id='' WHERE department_id=?`, id); err != nil {
		return fmt.Errorf("clearing department from users: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM departments WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("deleting department: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("department %q not found", id)
	}
	return nil
}

// columnExists checks if a column exists in a SQLite table.
func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}
