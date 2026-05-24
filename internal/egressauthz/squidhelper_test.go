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
	}
	for in, want := range cases {
		if got := hostFromURI(in); got != want {
			t.Errorf("hostFromURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHelperLine(t *testing.T) {
	id, src, uri := parseHelperLine("7 10.0.0.1 api.openai.com:443")
	if id != "7" || src != "10.0.0.1" || uri != "api.openai.com:443" {
		t.Errorf("concurrency parse: id=%q src=%q uri=%q", id, src, uri)
	}
	id, src, uri = parseHelperLine("10.0.0.1 example.com:443")
	if id != "" || src != "10.0.0.1" || uri != "example.com:443" {
		t.Errorf("no-concurrency parse: id=%q src=%q uri=%q", id, src, uri)
	}
}

func TestRunSquidHelper(t *testing.T) {
	// Authz allows 10.0.0.1 -> api.openai.com only.
	res := fakeResolver{"10.0.0.1": {"api.openai.com"}}
	srv := httptest.NewServer(NewHandler(res, nil, nil))
	defer srv.Close()

	in := strings.NewReader(strings.Join([]string{
		"1 10.0.0.1 api.openai.com:443", // allowed
		"2 10.0.0.1 evil.net:443",       // denied
		"3 10.9.9.9 api.openai.com:443", // unknown source -> denied
	}, "\n") + "\n")
	var out strings.Builder
	if err := RunSquidHelper(context.Background(), in, &out, srv.URL); err != nil {
		t.Fatalf("RunSquidHelper: %v", err)
	}
	got := strings.Fields(strings.ReplaceAll(out.String(), "\n", " "))
	// expect: "1 OK 2 ERR 3 ERR"
	want := []string{"1", "OK", "2", "ERR", "3", "ERR"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("helper output = %q, want %q", got, want)
	}
}
