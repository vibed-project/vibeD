---
sidebar_position: 2
---

# Storage Backends

vibeD stores artifact source code and deployment manifests using pluggable storage backends.

## Local Filesystem (Default)

Stores files on disk. Best for development and single-node setups.

```yaml
storage:
  backend: "local"
  local:
    basePath: "/data/vibed/artifacts"
```

Directory structure:
```
/data/vibed/artifacts/
├── {artifact-id}/
│   ├── src/           # Source files
│   │   ├── index.html
│   │   └── style.css
│   └── manifests/     # Deployment manifests
│       └── knative-service.yaml
```

## GitHub Repository

Stores artifacts in a GitHub repository for versioning and collaboration.

```yaml
storage:
  backend: "github"
  github:
    owner: "myorg"
    repo: "vibed-artifacts"
    branch: "main"
```

Requires the `GITHUB_TOKEN` environment variable. Each artifact gets a folder in the repo:

```
vibed-artifacts/
├── artifacts/
│   ├── my-portfolio/
│   │   ├── src/
│   │   └── manifests/
│   └── chat-app/
│       ├── src/
│       └── manifests/
```

Files are committed atomically using the Git Trees API.

## GitLab Repository

Stores artifacts in a GitLab repository using the Commits API.

```yaml
storage:
  backend: "gitlab"
  gitlab:
    url: "https://gitlab.com"       # GitLab instance URL (default: https://gitlab.com)
    projectID: 12345                 # GitLab project ID (required)
    branch: "main"                   # Branch name (default: main)
    token: "env:GITLAB_TOKEN"        # Access token (supports env: and file: prefixes)
```

Requires a GitLab personal access token with `api` scope, or set the `GITLAB_TOKEN` environment variable.

Files are committed atomically using the GitLab Commits API.

## Per-User Multi-Repo Storage

When authentication is enabled, each API key user can have their own dedicated storage repository. This provides full artifact isolation between users.

Users without a per-user storage config use the global fallback backend.

```yaml
auth:
  enabled: true
  mode: "apikey"
  apiKeys:
    - key: "env:ALICE_KEY"
      name: "alice"
      storage:                           # Per-user storage override
        backend: "github"
        github:
          owner: "alice-org"
          repo: "alice-vibed-artifacts"
          token: "env:ALICE_GITHUB_TOKEN"

    - key: "env:BOB_KEY"
      name: "bob"
      storage:
        backend: "gitlab"
        gitlab:
          url: "https://gitlab.example.com"
          projectID: 42
          token: "env:BOB_GITLAB_TOKEN"

    - key: "env:SHARED_KEY"
      name: "shared"
      # No per-user storage — uses global fallback

storage:
  backend: "local"                       # Global fallback
  local:
    basePath: "/data/vibed/artifacts"
```

### How It Works

1. Each authenticated request carries a user identity (the API key `name` field).
2. When an artifact is deployed, it is stamped with the user's `owner_id`.
3. Users can only see and manage their own artifacts.
4. Storage operations are routed to the user's configured backend.
5. Users without per-user storage config use the global fallback storage.
6. Per-user backends are created lazily on first use and cached.

### Secret Resolution

Token values in per-user storage configs support the same resolution patterns:

| Format | Description |
|--------|-------------|
| `env:VAR_NAME` | Reads from environment variable |
| `file:/path/to/token` | Reads from file (whitespace trimmed) |
| Literal value | Used as-is |

### Ownership Enforcement

When auth is enabled:
- Artifacts are tagged with the deploying user's ID
- `list` only returns artifacts owned by the current user
- `status`, `update`, `delete`, and `logs` check ownership before proceeding
- Attempting to access another user's artifact returns "not found" (not "forbidden") to avoid leaking artifact existence

When auth is disabled, all ownership checks are skipped and all artifacts are visible.
