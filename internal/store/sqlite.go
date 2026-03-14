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

func (s *SQLiteStore) List(ctx context.Context, statusFilter string, ownerID string, adminView bool) ([]api.ArtifactSummary, error) {
	query := `SELECT id, name, owner_id, status, target, url, created_at, updated_at, version, shared_with FROM artifacts`
	var args []interface{}
	var conditions []string

	if statusFilter != "" && statusFilter != "all" {
		conditions = append(conditions, "status = ?")
		args = append(args, statusFilter)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	defer rows.Close()

	var summaries []api.ArtifactSummary
	for rows.Next() {
		var (
			summary         api.ArtifactSummary
			status, target  string
			createdAt, updatedAt string
			sharedWithJSON  string
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

		// Apply ownership filter in Go (SharedWith is JSON, same approach as MemoryStore).
		if !adminView && ownerID != "" {
			isOwner := summary.OwnerID == ownerID
			isShared := slices.Contains(summary.SharedWith, ownerID)
			if !isOwner && !isShared {
				continue
			}
		}

		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
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
