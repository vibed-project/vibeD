package server

import (
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
)

func TestIsLoopbackBind(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", false},        // all interfaces
		{"0.0.0.0:8080", false}, // all interfaces
		{"[::]:8080", false},    // all interfaces
		{"10.0.0.5:8080", false},
		{"example.com:8080", false}, // unresolvable hostname → fail safe
	}
	for _, c := range cases {
		if got := isLoopbackBind(c.addr); got != c.want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// TestCheckNoAuthBindSafety covers the #55 startup guard: no-auth on a
// non-loopback bind is refused unless devInsecure is set; every other
// combination is allowed to start.
func TestCheckNoAuthBindSafety(t *testing.T) {
	cfg := func(enabled, devInsecure bool, addr string) *config.Config {
		c := &config.Config{}
		c.Auth.Enabled = enabled
		c.Auth.DevInsecure = devInsecure
		c.Server.HTTPAddr = addr
		return c
	}

	cases := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{"auth on, public bind — ok", cfg(true, false, ":8080"), false},
		{"auth off, loopback — ok", cfg(false, false, "127.0.0.1:8080"), false},
		{"auth off, public bind, no opt-in — refuse", cfg(false, false, ":8080"), true},
		{"auth off, public bind, devInsecure — ok", cfg(false, true, ":8080"), false},
		{"auth off, wildcard IP, no opt-in — refuse", cfg(false, false, "0.0.0.0:8080"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkNoAuthBindSafety(c.cfg)
			if (err != nil) != c.wantErr {
				t.Fatalf("checkNoAuthBindSafety = %v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}
