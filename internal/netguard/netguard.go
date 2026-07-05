// Package netguard centralizes the "do not connect to this address" policy used
// across vibeD's SSRF defenses: the egress authorizer (which resolves an
// allow-listed hostname and denies private targets) and the agent/loader source
// fetchers (which dial arbitrary operator-supplied URLs). It has no third-party
// dependencies so lean sidecar binaries can import it without pulling in the
// Kubernetes client libraries.
package netguard

import "net"

// blockedCIDRs are the private, link-local, and loopback ranges (including the
// 169.254.169.254 link-local instance-metadata address) that untrusted
// workloads must never reach. Denying these is defense-in-depth against SSRF
// and DNS-rebinding: a hostname that is allow-listed (or operator-supplied) can
// still resolve to an internal service IP or the instance-metadata endpoint, so
// the resolved/dialed address must be checked, not just the name.
var blockedCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",     // RFC1918 private
		"172.16.0.0/12",  // RFC1918 private
		"192.168.0.0/16", // RFC1918 private
		"169.254.0.0/16", // link-local (incl. 169.254.169.254 instance metadata)
		"127.0.0.0/8",    // IPv4 loopback
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fd00::/8",       // IPv6 unique-local
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// IsBlockedIP reports whether ip falls in a private/link-local/loopback range.
// A nil or unspecified address is treated as blocked (fail closed): an
// unroutable/ambiguous address has no legitimate use as an egress target.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return true
	}
	for _, n := range blockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsBlockedHostIP parses host (a literal IP string, e.g. from a dialed
// "host:port" after SplitHostPort) and reports whether it is blocked. A string
// that is not a valid IP is treated as blocked (fail closed) — the dial path
// resolves names to IPs before calling this, so a non-IP here is unexpected.
func IsBlockedHostIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	return IsBlockedIP(ip)
}
