package egressauthz

import (
	"context"
	"net"
)

// blockedCIDRs are the private, link-local, and loopback ranges (including the
// 169.254.169.254 instance-metadata address) that a sandbox must never reach
// via an allow-listed hostname. Even when a per-app allow-list authorizes a
// hostname, we resolve it and deny if it lands in one of these ranges. This is
// defense-in-depth against a DNS-rebinding attack: an attacker-controlled
// domain (allow-listed, or matching a wildcard) can point its A/AAAA record at
// 169.254.169.254 (the link-local instance-metadata endpoint) or an internal
// service IP, so authorizing the name alone is not enough — the resolved
// address must be checked too.
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

// isBlockedIP reports whether ip falls in any private/link-local/loopback/
// metadata range. An IPv4 address is also matched against its IPv6-mapped
// form so callers don't need to normalize.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range blockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// hostResolver resolves a hostname to its IP addresses. Abstracted so tests can
// inject deterministic answers (and so a literal IP host doesn't hit DNS).
type hostResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// netResolver is the production resolver backed by the system stub resolver.
type netResolver struct{ r *net.Resolver }

func (n netResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r := n.r
	if r == nil {
		r = net.DefaultResolver
	}
	return r.LookupIPAddr(ctx, host)
}

// resolvesToBlocked resolves host and reports whether ANY of its addresses is
// in a blocked range. A host that is already a literal IP is checked directly
// without a DNS lookup. found reports whether resolution produced any usable
// address; when a lookup fails (found=false) the caller decides the policy
// (this package fails closed on the SSRF path).
func resolvesToBlocked(ctx context.Context, resolver hostResolver, host string) (blocked, found bool) {
	if host == "" {
		return false, false
	}
	// Literal IP: no DNS needed.
	if ip := net.ParseIP(host); ip != nil {
		return isBlockedIP(ip), true
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false, false
	}
	for _, a := range addrs {
		if isBlockedIP(a.IP) {
			return true, true
		}
	}
	return false, true
}
