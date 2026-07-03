// Package entitlements gates paid features behind the running edition.
//
// The OSS core ships the "community" edition, under which no paid feature is
// licensed. A closed enterprise binary installs a license-backed implementation
// via Set (through pkg/plugin.SetEntitlements) after verifying its signed
// license key; paid feature gate points then call Require before activating.
// Because the OSS core never calls Set, an OSS build stays community and every
// Require for a paid feature fails — but the OSS code never gates itself, so
// there is no behavior change for community users.
package entitlements

import (
	"fmt"
	"sync"
)

// Entitlements reports the running edition and which paid features it licenses.
type Entitlements interface {
	// Edition names the running edition, e.g. "community" or "enterprise".
	Edition() string
	// Licensed reports whether the named paid feature is licensed.
	Licensed(feature string) bool
}

// community is the OSS default: it licenses no paid feature.
type community struct{}

func (community) Edition() string      { return "community" }
func (community) Licensed(string) bool { return false }

// current holds the process-wide Entitlements, guarded by mu (Entitlements has
// varying concrete types, so a mutex — not atomic.Value — is used).
var (
	mu      sync.RWMutex
	current Entitlements = community{}
)

// Set installs the process-wide entitlements. The enterprise binary calls it
// once, after verifying its license; the OSS core never does.
func Set(e Entitlements) {
	if e == nil {
		e = community{}
	}
	mu.Lock()
	current = e
	mu.Unlock()
}

// Get returns the current entitlements (never nil).
func Get() Entitlements {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Require returns nil if feature is licensed in the current edition, else a
// descriptive error. It is the gate a paid feature calls before activating.
func Require(feature string) error {
	e := Get()
	if e.Licensed(feature) {
		return nil
	}
	return fmt.Errorf("feature %q is not licensed in the %q edition; a valid vibeD Enterprise license is required", feature, e.Edition())
}
