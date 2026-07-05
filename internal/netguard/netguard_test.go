package netguard

import (
	"net"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // link-local instance metadata
		{"127.0.0.1", true},
		{"0.0.0.0", true}, // unspecified → blocked
		{"::", true},      // unspecified IPv6 → blocked
		{"::1", true},
		{"fe80::1", true},
		{"fd00::1", true},
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := IsBlockedIP(ip); got != c.want {
			t.Errorf("IsBlockedIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
	// nil is blocked (fail closed).
	if !IsBlockedIP(nil) {
		t.Error("IsBlockedIP(nil) should be true (fail closed)")
	}
}

func TestIsBlockedHostIP(t *testing.T) {
	if !IsBlockedHostIP("169.254.169.254") {
		t.Error("metadata IP host should be blocked")
	}
	if IsBlockedHostIP("8.8.8.8") {
		t.Error("public IP host should not be blocked")
	}
	// A non-IP string fails closed.
	if !IsBlockedHostIP("not-an-ip") {
		t.Error("non-IP host should be blocked (fail closed)")
	}
}
