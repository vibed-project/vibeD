package config

import "testing"

func TestResolveDevBind(t *testing.T) {
	cases := []struct {
		name        string
		addr        string
		authEnabled bool
		devInsecure bool
		wantAddr    string
		wantWarn    bool
		wantErr     bool
	}{
		{
			name: "auth enabled leaves public bind untouched",
			addr: ":8080", authEnabled: true, wantAddr: ":8080",
		},
		{
			name: "auth enabled, explicit public addr untouched",
			addr: "0.0.0.0:8080", authEnabled: true, wantAddr: "0.0.0.0:8080",
		},
		{
			name: "no auth + loopback is fine",
			addr: "127.0.0.1:8080", authEnabled: false, wantAddr: "127.0.0.1:8080",
		},
		{
			name: "no auth + localhost is fine",
			addr: "localhost:8080", authEnabled: false, wantAddr: "localhost:8080",
		},
		{
			name: "no auth + all-interfaces bind is forced to loopback with warning",
			addr: ":8080", authEnabled: false, devInsecure: false,
			wantAddr: "127.0.0.1:8080", wantWarn: true,
		},
		{
			name: "no auth + explicit public bind forced to loopback",
			addr: "0.0.0.0:9000", authEnabled: false, devInsecure: false,
			wantAddr: "127.0.0.1:9000", wantWarn: true,
		},
		{
			name: "no auth + public bind + dev-insecure honored with prominent warning",
			addr: "0.0.0.0:8080", authEnabled: false, devInsecure: true,
			wantAddr: "0.0.0.0:8080", wantWarn: true,
		},
		{
			name: "no auth + loopback ipv6 is fine",
			addr: "[::1]:8080", authEnabled: false, wantAddr: "[::1]:8080",
		},
		{
			name: "invalid address errors",
			addr: "not-an-addr", authEnabled: false, wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, warn, err := ResolveDevBind(c.addr, c.authEnabled, c.devInsecure)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for addr %q", c.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if addr != c.wantAddr {
				t.Errorf("addr = %q, want %q", addr, c.wantAddr)
			}
			if (warn != "") != c.wantWarn {
				t.Errorf("warning presence = %v (%q), want %v", warn != "", warn, c.wantWarn)
			}
		})
	}
}
