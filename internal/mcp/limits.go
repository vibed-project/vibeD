package mcp

import (
	"fmt"

	"github.com/vibed-project/vibeD/internal/config"
)

// validateFileLimits checks that the file map is within configured limits.
func validateFileLimits(files map[string]string, limits config.LimitsConfig) error {
	if limits.MaxFileCount > 0 && len(files) > limits.MaxFileCount {
		return fmt.Errorf("too many files: %d exceeds maximum of %d", len(files), limits.MaxFileCount)
	}

	if limits.MaxTotalFileSize > 0 {
		total := 0
		for _, content := range files {
			total += len(content)
			if total > limits.MaxTotalFileSize {
				return fmt.Errorf("total file size exceeds maximum of %d bytes (%d MB)",
					limits.MaxTotalFileSize, limits.MaxTotalFileSize/(1024*1024))
			}
		}
	}

	return nil
}

// clampLogLines ensures the requested log lines stay within configured limits.
func clampLogLines(requested int, limits config.LimitsConfig) int {
	if requested <= 0 {
		return 50 // default
	}
	if limits.MaxLogLines > 0 && requested > limits.MaxLogLines {
		return limits.MaxLogLines
	}
	return requested
}
