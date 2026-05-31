package egressauthz

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// debug is enabled when VIBED_SQUID_HELPER_DEBUG=1. When on, every raw stdin
// line from Squid is dumped to stderr so kubectl logs reveals exactly what
// format Squid is producing — invaluable when an external_acl_type format
// token silently substitutes "-" and the helper's downstream parsing looks
// wrong. Off by default so production logs stay quiet.
var debug = os.Getenv("VIBED_SQUID_HELPER_DEBUG") == "1"

// RunSquidHelper runs the Squid external_acl helper loop. Squid (configured
// `external_acl_type ... %SRC %>ru .../vibed-egress-authz squid-helper`) writes
// one request per line — "<SRC> <URI>", or "<channel-id> <SRC> <URI>" with
// concurrency — and this asks the authz service whether SRC may reach URI's
// host, replying "OK" / "ERR" (prefixed with the channel id when present). It
// fails closed: any authz error denies. Blocks until in reaches EOF.
func RunSquidHelper(ctx context.Context, in io.Reader, out io.Writer, authzURL string) error {
	authzURL = strings.TrimRight(authzURL, "/")
	hc := &http.Client{Timeout: 5 * time.Second}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	w := bufio.NewWriter(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if debug {
			fmt.Fprintf(os.Stderr, "squid-helper: input %q\n", line)
		}
		id, src, uri := parseHelperLine(line)
		host := hostFromURI(uri)
		if debug {
			fmt.Fprintf(os.Stderr, "squid-helper: parsed id=%q src=%q uri=%q host=%q\n", id, src, uri, host)
		}
		decision := "ERR"
		if allowEgress(ctx, hc, authzURL, src, host) {
			decision = "OK"
		}
		if id != "" {
			fmt.Fprintf(w, "%s %s\n", id, decision)
		} else {
			fmt.Fprintf(w, "%s\n", decision)
		}
		_ = w.Flush()
	}
	return sc.Err()
}

// parseHelperLine handles both the no-concurrency ("SRC URI") and concurrency
// ("ID SRC URI") forms of Squid's helper protocol.
func parseHelperLine(line string) (id, src, uri string) {
	f := strings.Fields(line)
	switch len(f) {
	case 0:
		return "", "", ""
	case 1:
		return "", f[0], ""
	case 2:
		return "", f[0], f[1]
	default:
		return f[0], f[1], f[2]
	}
}

// hostFromURI extracts the hostname from a Squid %>ru value: "host:443" (a
// CONNECT target) or "http://host/path" (a forward-proxied HTTP request).
func hostFromURI(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	if strings.Contains(uri, "://") {
		if u, err := url.Parse(uri); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	host := uri
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip :port from the CONNECT target
	}
	return host
}

func allowEgress(ctx context.Context, hc *http.Client, authzURL, src, host string) bool {
	if host == "" {
		return false
	}
	q := url.Values{"src": {src}, "host": {host}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authzURL+"/authz?"+q.Encode(), nil)
	if err != nil {
		return false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false // fail closed
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}
