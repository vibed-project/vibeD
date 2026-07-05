package egressauthz

import (
	"context"
	"net"

	"github.com/vibed-project/vibeD/internal/netguard"
)

// isBlockedIP reports whether ip falls in a private/link-local/loopback range
// (incl. the 169.254.169.254 instance-metadata address). The range policy is
// shared with the agent/loader SSRF guards via internal/netguard. This is
// defense-in-depth against DNS-rebinding: an allow-listed domain can point its
// A/AAAA record at an internal service IP or the metadata endpoint, so the
// resolved address must be checked, not just the name.
//
// Unlike netguard.IsBlockedIP (which fails closed on nil/unspecified for the
// dial path), here a nil IP means "no address to check" and is not treated as
// blocked; resolvesToBlocked only calls this with parsed/resolved addresses.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return netguard.IsBlockedIP(ip)
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
