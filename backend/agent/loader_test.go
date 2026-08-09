package agent

import "kanban/pkg/agent"

// RegisterAdapterFactory registers an adapter factory for an extension name.
// Test-only: used by tests to inject mock adapters. Kept out of the
// production binary by living in a _test.go file (same package, so it can
// access the unexported adapterRegistry var).
func RegisterAdapterFactory(name string, fn agent.AdapterFactory) {
	adapterRegistry.Lock()
	defer adapterRegistry.Unlock()
	adapterRegistry.factories[name] = fn
}
