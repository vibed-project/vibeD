package config

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// SchemeResolver resolves the reference part of a "<scheme>:<ref>" secret value
// (e.g. for "vault:secret/data/db#password" it receives "secret/data/db#password").
// The core registers env and file; an out-of-tree module can register others
// (e.g. vault, kms) via RegisterScheme.
type SchemeResolver func(ctx context.Context, ref string) (string, error)

var (
	schemeMu sync.RWMutex
	schemes  = map[string]SchemeResolver{}
)

// RegisterScheme installs a resolver for a secret scheme (the token before the
// first ':' in a value, e.g. "vault"). It panics on an empty scheme, a nil
// resolver, or a duplicate — all programmer errors surfaced at startup.
func RegisterScheme(scheme string, r SchemeResolver) {
	schemeMu.Lock()
	defer schemeMu.Unlock()
	if scheme == "" || r == nil {
		panic("config: RegisterScheme requires a non-empty scheme and a non-nil resolver")
	}
	if _, dup := schemes[scheme]; dup {
		panic("config: secret scheme already registered: " + scheme)
	}
	schemes[scheme] = r
}

func init() {
	// Built-in schemes (behavior-preserving).
	RegisterScheme("env", func(_ context.Context, ref string) (string, error) {
		if v := os.Getenv(ref); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("environment variable %q is not set", ref)
	})
	RegisterScheme("file", func(_ context.Context, ref string) (string, error) {
		data, err := os.ReadFile(ref)
		if err != nil {
			return "", fmt.Errorf("reading secret file %q: %w", ref, err)
		}
		return strings.TrimSpace(string(data)), nil
	})
}

// ResolveSecret resolves a secret value that may reference an external source.
// Supported formats:
//   - "env:VAR_NAME" — reads from environment variable VAR_NAME
//   - "file:/path/to/file" — reads file content (trimmed of whitespace)
//   - "<scheme>:<ref>" for any scheme registered via RegisterScheme
//   - anything else (no scheme, or an unregistered scheme) — returned as-is
func ResolveSecret(value string) (string, error) {
	return ResolveSecretContext(context.Background(), value)
}

// ResolveSecretContext is ResolveSecret with a caller-provided context, passed
// through to the scheme resolver (so external fetches can be bounded/cancelled).
func ResolveSecretContext(ctx context.Context, value string) (string, error) {
	scheme, ref, ok := strings.Cut(value, ":")
	if !ok {
		return value, nil // no scheme — literal value
	}
	schemeMu.RLock()
	r, found := schemes[scheme]
	schemeMu.RUnlock()
	if !found {
		return value, nil // unregistered scheme — treat as a literal value
	}
	return r(ctx, ref)
}
