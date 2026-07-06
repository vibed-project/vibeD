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

// HostResolvesToBlocked reports whether host resolves to at least one blocked
// address. A literal IP is checked directly; a hostname is resolved with res
// (nil → net.DefaultResolver). One blocked answer is enough to return true — an
// attacker's resolver can return a mix of a good and a metadata IP hoping the
// connector picks the latter. A resolution error is returned to the caller so
// it can choose its own failure mode.
func HostResolvesToBlocked(ctx context.Context, res *net.Resolver, host string) (bool, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return IsBlocked(addr), nil
	}
	if res == nil {
		res = net.DefaultResolver
	}
	addrs, err := res.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return false, err
	}
	for _, a := range addrs {
		if IsBlocked(a) {
			return true, nil
		}
	}
	return false, nil
}
