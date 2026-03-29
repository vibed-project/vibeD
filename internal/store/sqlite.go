package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
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
	status       TEXT NOT NULL,
	target       TEXT NOT NULL DEFAULT '',
	image_ref    TEXT NOT NULL DEFAULT '',
	url          TEXT NOT NULL DEFAULT '',
	port         INTEGER NOT NULL DEFAULT 0,
	env_vars     TEXT NOT NULL DEFAULT '{}',
	secret_refs  TEXT NOT NULL DEFAULT '{}',
	language     TEXT NOT NULL DEFAULT '',
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
CREATE INDEX IF NOT EXISTS idx_artifact_versions_artifact_id ON artifact_versions(artifact_id);
CREATE INDEX IF NOT EXISTS idx_share_links_artifact_id ON share_links(artifact_id);

CREATE TABLE IF NOT EXISTS departments (
	id         TEXT PRIMARY KEY,
	name       TEXT UNIQUE NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`

// SQLiteStore is a persistent ArtifactStore backed by SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at the given path
// and initializes the schema. Uses WAL mode for concurrent read performance.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving sqlite path: %w", err)
	}

	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}

	// Enable WAL mode for concurrent reads and busy timeout for contention.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("setting pragma %q: %w", pragma, err)
		}
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
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

	return &SQLiteStore{db: db}, nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
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

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO artifacts (id, name, owner_id, status, target, image_ref, url, port,
			env_vars, secret_refs, language, static_files, error, created_at, updated_at, storage_ref,
			version, version_id, shared_with)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifact.ID, artifact.Name, artifact.OwnerID, string(artifact.Status),
		string(artifact.Target), artifact.ImageRef, artifact.URL, artifact.Port,
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
	return nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*api.Artifact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, owner_id, status, target, image_ref, url, port,
			env_vars, secret_refs, language, static_files, error, created_at, updated_at,
			storage_ref, version, version_id, shared_with
		FROM artifacts WHERE id = ?`, id)

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
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, owner_id, status, target, image_ref, url, port,
			env_vars, secret_refs, language, static_files, error, created_at, updated_at,
			storage_ref, version, version_id, shared_with
		FROM artifacts WHERE name = ?`, name)

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
	whereClause := ""
	var args []interface{}

	if opts.StatusFilter != "" && opts.StatusFilter != "all" {
		whereClause = " WHERE status = ?"
		args = append(args, opts.StatusFilter)
	}

	// Get total count first
	countQuery := "SELECT COUNT(*) FROM artifacts" + whereClause
	var totalRaw int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalRaw); err != nil {
		return nil, fmt.Errorf("counting artifacts: %w", err)
	}

	// Fetch rows with ordering and optional pagination
	query := `SELECT id, name, owner_id, status, target, url, created_at, updated_at, version, shared_with FROM artifacts` + whereClause + ` ORDER BY created_at DESC`
	queryArgs := append([]interface{}{}, args...)

	if opts.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		queryArgs = append(queryArgs, opts.Limit+opts.Offset+200, 0) // fetch enough for ownership filter
	}

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	defer rows.Close()

	// Scan all matching rows, apply ownership filter in Go
	var all []api.ArtifactSummary
	for rows.Next() {
		var (
			summary                api.ArtifactSummary
			status, target         string
			createdAt, updatedAt   string
			sharedWithJSON         string
		)
		if err := rows.Scan(
			&summary.ID, &summary.Name, &summary.OwnerID,
			&status, &target, &summary.URL,
			&createdAt, &updatedAt, &summary.Version, &sharedWithJSON,
		); err != nil {
			return nil, fmt.Errorf("scanning artifact summary: %w", err)
		}

		summary.Status = api.ArtifactStatus(status)
		summary.Target = api.DeploymentTarget(target)
		summary.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		summary.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		_ = json.Unmarshal([]byte(sharedWithJSON), &summary.SharedWith)

		if !opts.AdminView && opts.OwnerID != "" {
			isOwner := summary.OwnerID == opts.OwnerID
			isShared := slices.Contains(summary.SharedWith, opts.OwnerID)
			if !isOwner && !isShared {
				continue
			}
		}

		all = append(all, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	total := len(all)

	// Apply offset/limit
	if opts.Offset > 0 && opts.Offset < len(all) {
		all = all[opts.Offset:]
	} else if opts.Offset >= len(all) {
		all = nil
	}

	if opts.Limit > 0 && opts.Limit < len(all) {
		all = all[:opts.Limit]
	}

	return &ListResult{Artifacts: all, Total: total}, nil
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

	res, err := s.db.ExecContext(ctx,
		`UPDATE artifacts SET name=?, owner_id=?, status=?, target=?, image_ref=?, url=?, port=?,
			env_vars=?, secret_refs=?, language=?, static_files=?, error=?, created_at=?, updated_at=?,
			storage_ref=?, version=?, version_id=?, shared_with=?
		WHERE id=?`,
		artifact.Name, artifact.OwnerID, string(artifact.Status),
		string(artifact.Target), artifact.ImageRef, artifact.URL, artifact.Port,
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
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, "DELETE FROM artifacts WHERE id = ?", id)
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

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO artifact_versions (version_id, artifact_id, version, image_ref, storage_ref,
			env_vars, secret_refs, status, url, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
	rows, err := s.db.QueryContext(ctx,
		`SELECT version_id, artifact_id, version, image_ref, storage_ref,
			env_vars, secret_refs, status, url, created_at, created_by
		FROM artifact_versions WHERE artifact_id = ? ORDER BY version ASC`, artifactID)
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
		a               api.Artifact
		status, target  string
		envVarsJSON     string
		secretRefsJSON  string
		sharedWithJSON  string
		createdAt       string
		updatedAt       string
	)

	err := row.Scan(
		&a.ID, &a.Name, &a.OwnerID, &status, &target,
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
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	if envVarsJSON != "" && envVarsJSON != "{}" {
		_ = json.Unmarshal([]byte(envVarsJSON), &a.EnvVars)
	}
	if secretRefsJSON != "" && secretRefsJSON != "{}" {
		_ = json.Unmarshal([]byte(secretRefsJSON), &a.SecretRefs)
	}
	if sharedWithJSON != "" && sharedWithJSON != "[]" {
		_ = json.Unmarshal([]byte(sharedWithJSON), &a.SharedWith)
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
		_ = json.Unmarshal([]byte(envVarsJSON), &v.EnvVars)
	}
	if secretRefsJSON != "" && secretRefsJSON != "{}" {
		_ = json.Unmarshal([]byte(secretRefsJSON), &v.SecretRefs)
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
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, role, status, provider, department_id, created_at, updated_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &u.Provider, &u.DepartmentID, &createdAt, &updatedAt)
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
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, role, status, provider, department_id, created_at, updated_at FROM users WHERE name = ?`, name,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &u.Provider, &u.DepartmentID, &createdAt, &updatedAt)
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

func (s *SQLiteStore) ListUsers(ctx context.Context, departmentID string) ([]api.User, error) {
	query := `SELECT id, name, email, role, status, provider, department_id, created_at, updated_at FROM users`
	var args []interface{}
	if departmentID != "" {
		query += ` WHERE department_id = ?`
		args = append(args, departmentID)
	}
	query += ` ORDER BY name`

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

func (s *SQLiteStore) ListShareLinks(ctx context.Context, artifactID string) ([]api.ShareLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token, artifact_id, created_by, password, expires_at, created_at, revoked FROM share_links WHERE artifact_id=? ORDER BY created_at DESC`, artifactID)
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
		`INSERT INTO departments (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		dept.ID, dept.Name,
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
		`SELECT id, name, created_at, updated_at FROM departments WHERE id = ?`, id,
	).Scan(&d.ID, &d.Name, &createdAt, &updatedAt)
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
		`SELECT id, name, created_at, updated_at FROM departments WHERE name = ?`, name,
	).Scan(&d.ID, &d.Name, &createdAt, &updatedAt)
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

func (s *SQLiteStore) ListDepartments(ctx context.Context) ([]api.Department, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, created_at, updated_at FROM departments ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing departments: %w", err)
	}
	defer rows.Close()

	var depts []api.Department
	for rows.Next() {
		var d api.Department
		var createdAt, updatedAt string
		if err := rows.Scan(&d.ID, &d.Name, &createdAt, &updatedAt); err != nil {
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
		`UPDATE departments SET name=?, updated_at=? WHERE id=?`,
		dept.Name, dept.UpdatedAt.Format(time.RFC3339Nano), dept.ID,
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
