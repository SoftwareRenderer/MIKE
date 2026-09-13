package sandbox

import (
	"testing"

	"kanban/pkg/sandboxruntime"
)

// fixedMemory stands in for the memory tracker, so the accounting can be tested without a
// container.
type fixedMemory map[string]sandboxruntime.MemoryUsageMB

func (f fixedMemory) Usage(name string) sandboxruntime.MemoryUsageMB { return f[name] }
func (f fixedMemory) Forget(string)                                  {}

func TestMemoryAccountingChargesACappedContainerItsCapOnce(t *testing.T) {
	sm := &SandboxManager{
		active: map[string]string{"task-a": "c-a", "proj:p": "c-p"},
		capsMB: map[string]int64{"task-a": 512, "proj:p": 0},
		mem: fixedMemory{
			"c-a": {Known: true, Current: 300, Limit: 512},
			"c-p": {Known: true, Current: 90},
		},
	}

	held, charge := sm.MemoryAccounting()
	if held != 390 {
		t.Errorf("held = %d, want 390 (300 + 90)", held)
	}
	// The capped container is charged its cap (512), not cap + usage; the uncapped one what it holds.
	if charge != 602 {
		t.Errorf("charge = %d, want 602 (max(300,512) + max(90,0))", charge)
	}
}

func TestMemoryAccountingUnknownContainerIsChargedItsCap(t *testing.T) {
	sm := &SandboxManager{
		active: map[string]string{"task-a": "c-a", "task-b": "c-b"},
		capsMB: map[string]int64{"task-a": 512, "task-b": 0},
		mem:    fixedMemory{}, // neither answers: no cgroup read yet, or it went away mid-run
	}

	held, charge := sm.MemoryAccounting()
	if held != 0 {
		t.Errorf("held = %d, want 0 (nothing was readable)", held)
	}
	// A capped container whose usage is unreadable still owes its cap; an uncapped one has nothing
	// left to be charged by, and is charged nothing rather than a guess.
	if charge != 512 {
		t.Errorf("charge = %d, want 512", charge)
	}
}
