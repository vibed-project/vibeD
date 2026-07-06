// Package netguard classifies IP addresses that an egress path facing
// untrusted input must never dial directly — the cloud metadata endpoint
// (169.254.169.254), loopback, link-local, and private/internal ranges — and
// helps enforce that at resolve/connect time. Name-based allow-lists cannot,
// because a permitted hostname can resolve (or be rebound) to a blocked
// address; checking the resolved IP closes that gap.
package netguard

import (
	"context"
	"net"
	"net/netip"
)

// IsBlocked reports whether addr is in a range that must not be dialed from an
// untrusted-facing egress path:
//
//   - loopback (127.0.0.0/8, ::1)
//   - link-local unicast (169.254.0.0/16 — including the 169.254.169.254 cloud
//     metadata endpoint — and IPv6 fe80::/10)
//   - link-local / interface-local / any multicast
//   - private / RFC1918 and IPv6 ULA (fc00::/7, via netip.Addr.IsPrivate)
//   - the unspecified address (0.0.0.0, ::)
//
// IPv4-mapped IPv6 addresses are normalized first, so ::ffff:169.254.169.254 is
// classified the same as 169.254.169.254. An invalid address is treated as
// blocked — it is never safe to dial something we could not parse.
func IsBlocked(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsPrivate() ||
		addr.IsUnspecified()
}

// IsLinkLocalOrLoopback reports whether addr is loopback, link-local (including
// the 169.254.169.254 metadata endpoint and IPv6 fe80::/10), the unspecified
// address, or multicast — ranges that are NEVER a legitimate egress
// destination. Unlike IsBlocked it deliberately does NOT include private/RFC1918
// or IPv6 ULA, which are valid in-cluster service addresses (e.g. an in-cluster
// source store the egress authz must keep allowing). Use this where blanket
// private-range denial would break legitimate internal traffic; use IsBlocked
// for an untrusted-facing dial where nothing internal is a valid target.
func IsLinkLocalOrLoopback(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}

// hostResolvesTo reports whether host resolves to at least one address that
// blocked() classifies as off-limits. A literal IP is checked directly; a
// hostname is resolved with res (nil → net.DefaultResolver). One matching answer
// is enough — an attacker's resolver can return a mix of a good and a bad IP
// hoping the connector picks the latter. A resolution error is returned so the
// caller can choose its own failure mode.
func hostResolvesTo(ctx context.Context, res *net.Resolver, host string, blocked func(netip.Addr) bool) (bool, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return blocked(addr), nil
	}
	if res == nil {
		res = net.DefaultResolver
	}
	addrs, err := res.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return false, err
	}
	for _, a := range addrs {
		if blocked(a) {
			return true, nil
		}
	}
	return false, nil
}

// HostResolvesToBlocked reports whether host resolves to any IsBlocked address
// (the strict set, including private/RFC1918). For an untrusted-facing dial.
func HostResolvesToBlocked(ctx context.Context, res *net.Resolver, host string) (bool, error) {
	return hostResolvesTo(ctx, res, host, IsBlocked)
}

// HostResolvesToLinkLocalOrLoopback reports whether host resolves to any
// IsLinkLocalOrLoopback address — the metadata/loopback ranges that are never a
// valid egress target — WITHOUT denying legitimate private/in-cluster
// addresses. Used by the egress authz, which must keep allowing in-cluster
// system hosts (their ClusterIPs are RFC1918) while still refusing a rebind to
// the cloud metadata endpoint.
func HostResolvesToLinkLocalOrLoopback(ctx context.Context, res *net.Resolver, host string) (bool, error) {
	return hostResolvesTo(ctx, res, host, IsLinkLocalOrLoopback)
}
