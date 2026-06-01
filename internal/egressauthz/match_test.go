package egressauthz

import "testing"

func TestMatch(t *testing.T) {
	allow := []string{"api.openai.com", "*.example.com", "EXACT.io"}
	cases := []struct {
		host string
		want bool
	}{
		{"api.openai.com", true},
		{"api.openai.com:443", true}, // port stripped
		{"API.OpenAI.com", true},     // case-insensitive
		{"api.openai.com.", true},    // trailing dot
		{"a.example.com", true},      // wildcard subdomain
		{"a.b.example.com", true},    // multi-label subdomain
		{"example.com", false},       // wildcard does NOT match apex
		{"evilexample.com", false},   // suffix without the dot boundary
		{"exact.io", true},           // case-insensitive exact
		{"notlisted.com", false},     // not on the list
		{"api.openai.com.evil.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := Match(allow, c.host); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestAuthorize(t *testing.T) {
	system := []string{"minio.vibed-system.svc"}
	allowed := []string{"api.openai.com"}

	if !Authorize(system, allowed, "minio.vibed-system.svc:9000") {
		t.Error("system host must always be allowed")
	}
	if !Authorize(system, allowed, "api.openai.com") {
		t.Error("allow-listed host must pass")
	}
	if Authorize(system, allowed, "exfil.example.net") {
		t.Error("unlisted host must be denied (default-deny)")
	}
	if Authorize(system, nil, "api.openai.com") {
		t.Error("empty allow-list ⇒ only system hosts pass")
	}
}
