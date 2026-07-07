package config

import (
	"sort"
	"strings"
)

// Out-of-tree providers registered through the pkg/plugin extension API add
// store backends and auth modes to the internal/store and internal/auth
// registries at init() time. The config validator must then accept their
// store.backend / auth.mode values — but config is a leaf package that must NOT
// import those registries (internal/auth already imports config, so importing
// it back would create a cycle). The server therefore injects small lister
// functions here before Load runs. When they are unset — bare config tests, or
// tools that never boot a server — only the built-in names are valid, so
// validation stays strict by default.
var (
	storeBackendLister func() []string
	authModeLister     func() []string
)

// SetStoreBackendLister installs a function returning every store backend known
// to the registry (built-in + out-of-tree). The server calls it once before
// LoadConfig; passing nil clears it.
func SetStoreBackendLister(f func() []string) { storeBackendLister = f }

// SetAuthModeLister installs a function returning every registered auth mode.
// The server calls it once before LoadConfig; passing nil clears it.
func SetAuthModeLister(f func() []string) { authModeLister = f }

// contains reports whether want is in the built-in allow-list.
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// listerHas reports whether the injected lister knows want. A nil lister (never
// injected) knows nothing, so only the built-in allow-list applies.
func listerHas(f func() []string, want string) bool {
	if f == nil {
		return false
	}
	for _, got := range f() {
		if got == want {
			return true
		}
	}
	return false
}

// availableNames renders "builtin ∪ registered" as a sorted, comma-separated
// list for a validation error message, so an operator sees that an
// out-of-tree-registered backend/mode is in fact available. The empty mode (the
// apikey default) is never surfaced.
func availableNames(builtin []string, lister func() []string) string {
	set := make(map[string]bool, len(builtin))
	for _, b := range builtin {
		set[b] = true
	}
	if lister != nil {
		for _, n := range lister() {
			if n != "" {
				set[n] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
