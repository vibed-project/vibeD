package config

import (
	"fmt"
	"os"
	"strings"
)

// ResolveSecret resolves a secret value that may reference an external source.
// Supported formats:
//   - "env:VAR_NAME" — reads from environment variable VAR_NAME
//   - "file:/path/to/file" — reads file content (trimmed of whitespace)
//   - anything else — returned as-is (literal value)
func ResolveSecret(value string) (string, error) {
	if strings.HasPrefix(value, "env:") {
		envName := strings.TrimPrefix(value, "env:")
		if v := os.Getenv(envName); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("environment variable %q is not set", envName)
	}

	if strings.HasPrefix(value, "file:") {
		filePath := strings.TrimPrefix(value, "file:")
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("reading secret file %q: %w", filePath, err)
		}
		return strings.TrimSpace(string(data)), nil
	}

	return value, nil
}
