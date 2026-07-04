package tenant

import "sync"

// Deps is what a resolver Factory receives. Default is the single-tenant scope
// the core would otherwise use (its configured namespace); Options is a generic
// bag an out-of-tree factory reads its own settings from, so enabling
// multi-tenancy needs no new core config field.
type Deps struct {
	Default Tenant
	Options map[string]string
}

// Factory builds the process Resolver. There is at most one.
type Factory func(Deps) (Resolver, error)

var (
	mu      sync.Mutex
	factory Factory
)

// Register installs the resolver factory. Called from an out-of-tree module's
// init(); it panics if a factory is already registered.
func Register(f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if factory != nil {
		panic("tenant: a resolver factory is already registered")
	}
	factory = f
}

// Build returns the registered resolver, or a single-tenant resolver over
// deps.Default when none is registered.
func Build(deps Deps) (Resolver, error) {
	mu.Lock()
	f := factory
	mu.Unlock()
	if f != nil {
		return f(deps)
	}
	return Single(deps.Default), nil
}
