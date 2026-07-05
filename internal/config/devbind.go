package config

import (
	"fmt"
	"net"
)

// ResolveDevBind enforces the no-auth exposure policy (#55). When authentication
// is DISABLED, an unauthenticated control plane must not be reachable off-host
// by accident. Given the requested bind address and the --dev-insecure
// acknowledgement, it returns the address to actually listen on plus an optional
// operator warning, or an error the caller should treat as fatal.
//
// Rules (only apply when authEnabled is false):
//   - Loopback / explicit-localhost binds are always fine.
//   - A non-loopback bind WITHOUT devInsecure is rewritten to loopback and a
//     warning is returned (fail safe rather than fail closed, so local dev on
//     the default ":8080" keeps working but only on 127.0.0.1).
//   - A non-loopback bind WITH devInsecure is honored, with a prominent warning
//     that the control plane is exposed without authentication.
//
// When authEnabled is true the requested address is returned unchanged.
func ResolveDevBind(addr string, authEnabled, devInsecure bool) (effective, warning string, err error) {
	if authEnabled {
		return addr, "", nil
	}

	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		// Accept a bare ":8080" (empty host = all interfaces) or a bare port.
		if addr != "" && addr[0] == ':' {
			host, port = "", addr[1:]
		} else {
			return "", "", fmt.Errorf("invalid server.httpAddr %q: %w", addr, splitErr)
		}
	}

	if isLoopbackBind(host) {
		return addr, "", nil
	}

	if !devInsecure {
		// Force loopback. Preserve the requested port.
		forced := net.JoinHostPort("127.0.0.1", port)
		return forced, fmt.Sprintf(
			"authentication is DISABLED and server.httpAddr %q is not loopback; binding to %s instead. "+
				"Enable auth, or pass --dev-insecure (VIBED_DEV_INSECURE=1) to bind the requested address anyway.",
			addr, forced), nil
	}

	return addr, fmt.Sprintf(
		"WARNING: authentication is DISABLED and the control plane is bound to a non-loopback address %q "+
			"because --dev-insecure was set. Anyone who can reach this address has full, unauthenticated admin access. "+
			"Do not use this outside a trusted, isolated environment.", addr), nil
}

// isLoopbackBind reports whether host names the loopback interface (or the
// common localhost spellings). An empty host means "all interfaces" and is NOT
// loopback.
func isLoopbackBind(host string) bool {
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
