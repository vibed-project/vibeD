---
sidebar_position: 2
---

# Store backends

The store is where vibeD keeps **artifact metadata and control-plane state** — the `VibedApp` records, their version history, and (optionally) users, audit events, and share links. The control plane itself is stateless; everything durable lives behind the store interface, so swapping the backend is the supported way to move that state onto your own database.

A backend is selected by the `store.backend` config key and built at startup from a factory you register. The core ships three built-ins; an out-of-tree module registers more the same way, with no change to the core.

## The interfaces

A backend implements one required interface and up to three optional ones, all in `github.com/vibed-project/vibeD/pkg/plugin` (aliases of the internal `store` types):

| Interface | Required? | Persists |
| --- | --- | --- |
| `ArtifactStore` | **Yes** | Artifacts, their state, and version snapshots |
| `UserStore` | Optional | User identities and departments |
| `AuditStore` | Optional | The append-only governance audit log |
| `ShareLinkStore` | Optional | Public share links to artifacts |

Only `ArtifactStore` is mandatory. The other three are **feature-detected via type assertion**: your factory returns an `ArtifactStore`, and the core checks at startup whether that same value also satisfies `UserStore`, `AuditStore`, or `ShareLinkStore`. Implement only the capabilities you need — a value that implements all four is returned as one object.

```go
// ArtifactStore is the required interface.
type ArtifactStore interface {
	Create(ctx context.Context, artifact *api.Artifact) error
	Get(ctx context.Context, id string) (*api.Artifact, error)
	GetByName(ctx context.Context, name string) (*api.Artifact, error)
	List(ctx context.Context, opts ListOptions) (*ListResult, error)
	Update(ctx context.Context, artifact *api.Artifact) error
	Delete(ctx context.Context, id string) error

	CreateVersion(ctx context.Context, version *api.ArtifactVersion) error
	ListVersions(ctx context.Context, artifactID string) ([]api.ArtifactVersion, error)
	GetVersion(ctx context.Context, artifactID string, version int) (*api.ArtifactVersion, error)
}
```

Return the sentinel errors from [`pkg/api`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/api) so the HTTP layer maps them to the right status codes:

| Sentinel | Returned by | Maps to |
| --- | --- | --- |
| `api.ErrNotFound` | `Get`, `GetByName`, `Update`, `Delete` | 404 |
| `api.ErrAlreadyExists` | `Create` (name taken) | 409 |
| `api.ErrVersionNotFound` | `GetVersion` | 404 |

If your backend holds a handle that must be released (a DB pool, a file), also implement `io.Closer`. The core calls `Close()` on shutdown when the returned store satisfies it; the built-in `memory` and `configmap` backends don't, so it's a no-op for them.

### Optional capabilities

`UserStore`, `AuditStore`, and `ShareLinkStore` extend the same value:

```go
type UserStore interface {
	CreateUser(ctx context.Context, user *api.User) error
	GetUser(ctx context.Context, id string) (*api.User, error)
	GetUserByName(ctx context.Context, name string) (*api.User, error)
	ListUsers(ctx context.Context, departmentID string) ([]api.User, error)
	GetUserByAPIKeyHash(ctx context.Context, hash string) (*api.User, error)
	UpdateUser(ctx context.Context, user *api.User) error
	// ... plus the Department methods (Create/Get/List/Update/Delete)
}

type AuditStore interface {
	AppendAudit(ctx context.Context, e *api.AuditEvent) error
	ListAudit(ctx context.Context, q AuditQuery) ([]api.AuditEvent, error) // newest first
}

type ShareLinkStore interface {
	CreateShareLink(ctx context.Context, link *api.ShareLink, passwordHash string) error
	GetShareLink(ctx context.Context, token string) (*api.ShareLink, string, error) // link + password hash
	ListShareLinks(ctx context.Context, artifactID string) ([]api.ShareLink, error)
	RevokeShareLink(ctx context.Context, token string) error
}
```

If a backend does **not** implement `AuditStore`, the core falls back to an in-memory audit log (which does not survive a restart) and logs a warning. To persist the governance trail across restarts, implement `AuditStore`.

## `StoreDeps` — what the factory receives

Your factory is a `StoreBackendFactory`:

```go
type StoreBackendFactory = func(StoreDeps) (ArtifactStore, error)
```

`StoreDeps` carries everything a backend might need. The built-ins read only the fields they care about; your backend reads its own settings from the generic `Options` bag:

| Field | Type | Populated from |
| --- | --- | --- |
| `Backend` | `string` | `store.backend` (the selected name) |
| `SQLitePath` | `string` | `store.sqlite.path` |
| `ConfigMapName` | `string` | `store.configmap.name` |
| `ConfigMapNamespace` | `string` | `store.configmap.namespace` |
| `K8sClient` | `kubernetes.Interface` | The in-cluster / kubeconfig client |
| `Options` | `map[string]string` | `store.options` (verbatim) |
| `Logger` | `*slog.Logger` | The server logger |

`Options` is the escape hatch: a DSN, a pool size, TLS settings — anything your backend needs — flows through it, so **adding a backend never requires a new field on the core config struct**.

## Selecting and configuring a backend

Two config keys drive backend selection, both under `store` in `vibed.yaml`:

- `store.backend` — the registered name to build (`sqlite`, `memory`, `configmap`, or one you registered).
- `store.options` — a free-form `map[string]string` passed straight through to your factory as `StoreDeps.Options`.

```yaml
store:
  backend: "postgres"          # your registered name
  options:                     # arrives verbatim as StoreDeps.Options
    dsn: "postgres://vibed:secret@db:5432/vibed?sslmode=require"
    maxConns: "20"
```

If `store.backend` names a backend that was never registered, `store.New` returns an "unknown store backend" error listing the registered names, and the server exits at startup. Registering your backend is what makes the name valid — see below.

## Built-in backends

| Name | Backing store | Implements | Use for |
| --- | --- | --- | --- |
| `sqlite` | A SQLite file at `store.sqlite.path` | `ArtifactStore` + `UserStore` + `AuditStore` + `ShareLinkStore` + `io.Closer` | The default; single-replica durable state |
| `memory` | Process memory | `ArtifactStore` | Tests and ephemeral local runs — lost on restart |
| `configmap` | A Kubernetes ConfigMap (`store.configmap.name`) | `ArtifactStore` | Small, no-database clusters; **no** audit persistence |

`sqlite` is the only built-in that implements every capability. `configmap` implements just `ArtifactStore`, so audit events fall back to the in-memory log there. Both `memory` and `configmap` are fine for small or dev setups but store no user or share-link data. An out-of-tree backend (e.g. a shared Postgres) is the path to durable, multi-replica state.

## Registering a backend

Register from your provider package's `init()` with `RegisterStoreBackend(name, factory)`. It's keyed by name and **panics on a nil factory or a duplicate name** — both are programmer errors caught at startup. You may register several backends.

```go
package vibedpostgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/pkg/plugin"
)

// PostgresStore implements ArtifactStore and, on the same value, the optional
// UserStore / AuditStore / ShareLinkStore capabilities the core feature-detects.
type PostgresStore struct {
	db *sql.DB
}

func init() {
	plugin.RegisterStoreBackend("postgres", func(d plugin.StoreDeps) (plugin.ArtifactStore, error) {
		dsn := d.Options["dsn"]
		if dsn == "" {
			return nil, fmt.Errorf("postgres store: store.options.dsn is required")
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, fmt.Errorf("postgres store: open: %w", err)
		}
		return &PostgresStore{db: db}, nil
	})
}

// --- ArtifactStore (required) ---

func (s *PostgresStore) Create(ctx context.Context, a *api.Artifact) error {
	// INSERT ...; on a duplicate name return &api.ErrAlreadyExists{Name: a.Name}.
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, id string) (*api.Artifact, error) {
	// SELECT ...; on no rows return &api.ErrNotFound{ID: id}.
	return nil, nil
}

// GetByName, List, Update, Delete, CreateVersion, ListVersions, GetVersion ...

// --- io.Closer (optional; called on shutdown) ---

func (s *PostgresStore) Close() error { return s.db.Close() }

// --- Optionally also implement UserStore / AuditStore / ShareLinkStore
//     on *PostgresStore to light up those capabilities. ---
```

Then a one-file binary blank-imports the package (running its `init()`) and defers to vibeD's wiring:

```go
package main

import (
	"github.com/vibed-project/vibeD/pkg/server"

	_ "github.com/example/vibed-postgres" // runs init() → RegisterStoreBackend("postgres", …)
)

func main() {
	server.Main()
}
```

Build and run it exactly like the stock binary, then set `store.backend: "postgres"` in `vibed.yaml`:

```bash
GOTOOLCHAIN=auto GO111MODULE=on go build -o bin/vibed ./cmd/vibed
```

## Audit enrichment fields

If your backend implements `AuditStore`, persist the full [`api.AuditEvent`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/api#AuditEvent), including the optional enrichment fields — dropping them silently loses governance context. The first block (`ID`, `Time`, `Actor`, `Action`, `Target`, `Outcome`, `Detail`) is always populated; the enrichment fields are set only when a caller supplies them:

| Field | JSON key | Meaning |
| --- | --- | --- |
| `TenantID` | `tenant_id` | Tenant the action belonged to (core fills this on deploys) |
| `SessionID` | `session_id` | Auth session identifier |
| `SourceHash` | `source_hash` | `sha256` of the deployed source (core fills this on deploys) |
| `PolicyDecision` | `policy_decision` | Deploy-time policy verdict |
| `Before` | `before` | Prior state, for a governance diff |
| `After` | `after` | New state, for a governance diff |

`TenantID` and `SourceHash` are filled by the core on deploys; the remaining fields are populated when an out-of-tree recorder or policy gate supplies them. Model these as columns (not an opaque JSON blob) if you want to query the audit log by tenant, session, or policy decision.

`ListAudit` must return events **newest first** and honor the `AuditQuery` filter — `Actor`, `Action`, `Target` (empty string means "don't filter"), and `Limit` (`0` means the implementation's default cap).

## See also

- [Extending vibeD](overview.md) — the registration pattern shared by every seam.
- [Configuration reference](../configuration/config-reference.md) — the `store.*` keys your backend is selected and configured by.
- [`pkg/plugin` API docs](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/plugin) — the exact aliased types.
