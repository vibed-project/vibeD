package orchestrator

import (
	"testing"
	"time"

	"github.com/vibed-project/vibeD/pkg/api"
)

// TestParseExpiresIn is the proof for #58: a non-empty but unparseable
// expires_in returns an error instead of silently yielding a zero (permanent)
// link; empty yields "no expiration"; valid values parse (incl. "7d" days).
func TestParseExpiresIn(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},                    // empty → no expiration
		{"24h", 24 * time.Hour, false},    // hours
		{"7d", 7 * 24 * time.Hour, false}, // day shorthand
		{"30m", 30 * time.Minute, false},
		{" 24h ", 24 * time.Hour, false}, // trimmed
		{"garbage", 0, true},             // unparseable → ERROR (was: permanent link)
		{"7dd", 0, true},                 // malformed day form
		{"abcd", 0, true},                // "d" suffix but bad number
		{"-5h", 0, true},                 // non-positive rejected
		{"0h", 0, true},                  // zero rejected (would be permanent)
	}
	for _, c := range cases {
		got, err := ParseExpiresIn(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseExpiresIn(%q) = %v, want error", c.in, got)
				continue
			}
			// Malformed input must map to a 400-style ErrInvalidInput.
			if _, ok := err.(*api.ErrInvalidInput); !ok {
				t.Errorf("ParseExpiresIn(%q) err type = %T, want *api.ErrInvalidInput", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseExpiresIn(%q) unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseExpiresIn(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
