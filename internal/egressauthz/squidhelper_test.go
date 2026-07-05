package egressauthz

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostFromURI(t *testing.T) {
	cases := map[string]string{
		"api.openai.com:443":           "api.openai.com", // CONNECT
		"http://example.com/x":         "example.com",    // forward-proxied HTTP
		"https://a.example.com:8443/p": "a.example.com",
		"plainhost":                    "plainhost",
		"":                             "",
		// IPv6-aware parsing (#57): brackets stripped, port removed, no mangling.
		"[2001:db8::1]:443":       "2001:db8::1", // CONNECT to bracketed IPv6 + port
		"[::1]:8080":              "::1",         // bracketed IPv6 loopback + port
		"[fe80::1]":               "fe80::1",     // bracketed IPv6, no port
		"http://[2001:db8::2]/p":  "2001:db8::2", // proxied URL with IPv6 host
		"https://[::1]:9000/path": "::1",         // proxied URL, IPv6 + port
	}
	for in, want := range cases {
		if got := hostFromURI(in); got != want {
			t.Errorf("hostFromURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHelperLine(t *testing.T) {
	cases := []struct {
		name                     string
		in                       string
		wantID, wantSrc, wantURI string
	}{
		{
			name:    "two fields",
			in:      "10.0.0.1 example.com:443",
			wantSrc: "10.0.0.1", wantURI: "example.com:443",
		},
		{
			// The Squid-5/6 wire format: %SRC %DST + a trailing "-" Squid
			// auto-appends. Earlier we wrongly treated this as concurrency
			// mode, which put the client IP in `id` and "-" in `uri`, so
			// authz saw host="-" for every request. Regression guard.
			name:    "three fields with Squid kvpair terminator",
			in:      "10.244.0.13 vibed.vibed-system.svc.cluster.local -",
			wantSrc: "10.244.0.13", wantURI: "vibed.vibed-system.svc.cluster.local",
		},
		{
			name: "empty line",
			in:   "",
		},
		{
			name:    "single field",
			in:      "only-src",
			wantSrc: "only-src",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, src, uri := parseHelperLine(tc.in)
			if id != tc.wantID || src != tc.wantSrc || uri != tc.wantURI {
				t.Errorf("parse %q = (id=%q src=%q uri=%q), want (id=%q src=%q uri=%q)",
					tc.in, id, src, uri, tc.wantID, tc.wantSrc, tc.wantURI)
			}
		})
	}
}

func TestRunSquidHelper(t *testing.T) {
	// Authz allows 10.0.0.1 -> api.openai.com only.
	res := fakeResolver{"10.0.0.1": {"api.openai.com"}}
	srv := httptest.NewServer(NewHandler(res, nil, nil))
	defer srv.Close()

	// Use the actual Squid 5/6 wire format: "%SRC %DST -" (the trailing "-"
	// is Squid's kvpair-style terminator). No concurrency channel ID since
	// we don't configure concurrency=N in the chart.
	in := strings.NewReader(strings.Join([]string{
		"10.0.0.1 api.openai.com -", // allowed
		"10.0.0.1 evil.net -",       // denied
		"10.9.9.9 api.openai.com -", // unknown source → denied
	}, "\n") + "\n")
	var out strings.Builder
	if err := RunSquidHelper(context.Background(), in, &out, srv.URL); err != nil {
		t.Fatalf("RunSquidHelper: %v", err)
	}
	got := strings.Fields(strings.ReplaceAll(out.String(), "\n", " "))
	// Non-concurrency output is the bare decision, one per request.
	want := []string{"OK", "ERR", "ERR"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("helper output = %q, want %q", got, want)
	}
}
