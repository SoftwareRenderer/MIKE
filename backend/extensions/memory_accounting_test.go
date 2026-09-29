package extensions

import (
	"testing"

	"mike/pkg/sandboxruntime"
)

// fixedMemory stands in for the memory tracker, so the accounting can be tested without a
// container.
type fixedMemory map[string]sandboxruntime.MemoryUsageMB

func (f fixedMemory) Usage(name string) sandboxruntime.MemoryUsageMB { return f[name] }
func (f fixedMemory) Forget(string)                                  {}

// The shared language-server and agent sandboxes are real memory and are counted like any other
// container.
func TestExtensionMemoryAccounting(t *testing.T) {
	t.Parallel()
	sm := &ExtensionSandboxManager{
		states: map[string]*extensionSandboxState{
			"ext-capped":   {containerName: "c-capped", memLimitMB: 2048},
			"ext-uncapped": {containerName: "c-uncapped", memLimitMB: 0},
		},
		mem: fixedMemory{
			"c-capped":   {Known: true, Current: 700, Limit: 2048},
			"c-uncapped": {Known: true, Current: 140},
		},
	}

	held, charge := sm.MemoryAccounting()
	if held != 840 {
		t.Errorf("held = %d, want 840 (700 + 140)", held)
	}
	if charge != 2188 {
		t.Errorf("charge = %d, want 2188 (max(700,2048) + max(140,0))", charge)
	}
}
