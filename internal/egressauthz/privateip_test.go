package egressauthz

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false}, // just outside 172.16/12
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // link-local instance metadata
		{"127.0.0.1", true},
		{"::1", true},
		{"fe80::1", true},
		{"fd00::1", true},
		{"fdff:ffff::1", true},
		{"8.8.8.8", false},
		{"93.184.216.34", false}, // example.com
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false}, // public IPv6 resolver
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isBlockedIP(ip); got != c.want {
			t.Errorf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestResolvesToBlocked(t *testing.T) {
	dns := fakeDNS{
		"meta.example":     {"169.254.169.254"},
		"internal.example": {"10.1.2.3"},
		"mixed.example":    {"93.184.216.34", "10.0.0.1"}, // one public, one private → blocked
		"public.example":   {"93.184.216.34"},
	}
	ctx := context.Background()

	// Literal blocked IP — no DNS.
	if blocked, found := resolvesToBlocked(ctx, dns, "169.254.169.254"); !blocked || !found {
		t.Errorf("literal metadata IP: blocked=%v found=%v, want true,true", blocked, found)
	}
	// Literal public IP.
	if blocked, found := resolvesToBlocked(ctx, dns, "8.8.8.8"); blocked || !found {
		t.Errorf("literal public IP: blocked=%v found=%v, want false,true", blocked, found)
	}
	// Hostname resolving to metadata.
	if blocked, _ := resolvesToBlocked(ctx, dns, "meta.example"); !blocked {
		t.Error("meta.example should resolve to a blocked range")
	}
	// Hostname with mixed answers — any blocked answer blocks.
	if blocked, _ := resolvesToBlocked(ctx, dns, "mixed.example"); !blocked {
		t.Error("mixed.example (one private answer) should be blocked")
	}
	// Public hostname.
	if blocked, found := resolvesToBlocked(ctx, dns, "public.example"); blocked || !found {
		t.Errorf("public.example: blocked=%v found=%v, want false,true", blocked, found)
	}
	// Empty host.
	if _, found := resolvesToBlocked(ctx, dns, ""); found {
		t.Error("empty host should not resolve")
	}
}
