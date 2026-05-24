// Package egressauthz decides whether a sandbox may reach a destination host,
// per the owning VibedApp's egress allow-list. It is the authorization brain
// the egress proxy (Squid) consults for every outbound connection — default
// deny, with vibeD's own system hosts (e.g. the source store) always allowed.
package egressauthz

import (
	"strconv"
	"strings"
)

// normalizeHost lowercases, trims, and strips a trailing numeric :port.
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		if _, err := strconv.Atoi(h[i+1:]); err == nil { // numeric port → drop it
			h = h[:i]
		}
	}
	return strings.TrimSuffix(h, ".") // drop a fully-qualified trailing dot
}

// matchOne reports whether host matches a single allow-list pattern. A bare
// "example.com" matches only exactly; a "*.example.com" wildcard matches any
// subdomain (a.example.com, a.b.example.com) but NOT the apex example.com.
func matchOne(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		dotted := "." + suffix
		return strings.HasSuffix(host, dotted) && len(host) > len(dotted)
	}
	return host == pattern
}

// Match reports whether host is permitted by the allow-list (case-insensitive,
// port-insensitive). Empty list ⇒ deny.
func Match(allowed []string, host string) bool {
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	for _, p := range allowed {
		if matchOne(p, h) {
			return true
		}
	}
	return false
}

// Authorize is the full decision: a host is allowed if it's a vibeD system
// host (always permitted, e.g. the source store) OR on the app's allow-list.
func Authorize(systemHosts, allowedHosts []string, host string) bool {
	return Match(systemHosts, host) || Match(allowedHosts, host)
}
