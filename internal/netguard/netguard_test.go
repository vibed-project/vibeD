package netguard

import (
	"context"
	"net/netip"
	"testing"
)

func TestIsBlocked(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		// The critical target: cloud metadata (link-local).
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		// Loopback.
		{"127.0.0.1", true},
		{"::1", true},
		// Private / RFC1918.
		{"10.0.0.5", true},
		{"172.16.3.4", true},
		{"192.168.1.1", true},
		// IPv6 ULA + link-local + unspecified + multicast.
		{"fd00::1", true},
		{"fe80::1", true},
		{"::", true},
		{"0.0.0.0", true},
		{"ff02::1", true},
		{"224.0.0.1", true},
		// IPv4-mapped metadata must still be caught.
		{"::ffff:169.254.169.254", true},
		// Public addresses must pass.
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com
		{"2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		addr := netip.MustParseAddr(c.ip)
		if got := IsBlocked(addr); got != c.blocked {
			t.Errorf("IsBlocked(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

func TestIsBlocked_InvalidIsBlocked(t *testing.T) {
	if !IsBlocked(netip.Addr{}) {
		t.Error("the zero/invalid address must be treated as blocked")
	}
}

func TestHostResolvesToBlocked_Literals(t *testing.T) {
	// IP literals are checked without touching DNS.
	for _, ip := range []string{"169.254.169.254", "127.0.0.1", "10.1.2.3"} {
		blocked, err := HostResolvesToBlocked(context.Background(), nil, ip)
		if err != nil || !blocked {
			t.Errorf("HostResolvesToBlocked(%s) = (%v, %v), want (true, nil)", ip, blocked, err)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1"} {
		blocked, err := HostResolvesToBlocked(context.Background(), nil, ip)
		if err != nil || blocked {
			t.Errorf("HostResolvesToBlocked(%s) = (%v, %v), want (false, nil)", ip, blocked, err)
		}
	}
}
