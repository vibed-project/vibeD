package config

import (
	"os"
	"strings"
)

// ResolveSecret resolves a secret value that may reference an external source.
// Supported formats:
//   - "env:VAR_NAME" — reads from environment variable VAR_NAME
//   - "file:/path/to/file" — reads file content (trimmed of whitespace)
//   - anything else — returned as-is (literal value)
func ResolveSecret(value string) string {
	if strings.HasPrefix(value, "env:") {
		envName := strings.TrimPrefix(value, "env:")
		if v := os.Getenv(envName); v != "" {
			return v
		}
		return "" // Return empty if env var not set
	}

	if strings.HasPrefix(value, "file:") {
		filePath := strings.TrimPrefix(value, "file:")
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "" // Return empty if file can't be read
		}
		return strings.TrimSpace(string(data))
	}

	return value
}
