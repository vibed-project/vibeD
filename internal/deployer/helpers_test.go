package deployer

import (
	"strings"
	"testing"
)

// TestScanLogLinesLongLine is the proof for #78: a log line longer than bufio's
// 64 KB default token size is returned intact instead of tripping
// bufio.ErrTooLong and failing the whole fetch (losing every line).
func TestScanLogLinesLongLine(t *testing.T) {
	long := strings.Repeat("x", 200*1024) // 200 KB single line, > 64 KB default
	input := "short line 1\n" + long + "\nshort line 3\n"

	lines, err := scanLogLines(strings.NewReader(input))
	if err != nil {
		t.Fatalf("scanLogLines errored on a long line (would lose ALL logs): %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0] != "short line 1" || lines[2] != "short line 3" {
		t.Errorf("surrounding lines mangled: %q / %q", lines[0], lines[2])
	}
	if len(lines[1]) != 200*1024 {
		t.Errorf("long line truncated: got %d bytes, want %d", len(lines[1]), 200*1024)
	}
}

func TestScanLogLinesEmpty(t *testing.T) {
	lines, err := scanLogLines(strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty input errored: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("empty input should yield no lines, got %v", lines)
	}
}
