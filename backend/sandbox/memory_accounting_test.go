package sandbox

import (
	"testing"

	"mike/pkg/sandboxruntime"
)

// fixedMemory stands in for the memory tracker, so the accounting can be tested without a
// container.
type fixedMemory map[string]sandboxruntime.MemoryUsageMB

func (f fixedMemory) Usage(name string) sandboxruntime.MemoryUsageMB { return f[name] }
func (f fixedMemory) Forget(string)                                  {}

// TestMemoryAccountingChargesEachContainerItsCapOrItsHolding: the budget is asked one number per
// container — what it holds, or its cap when it has one, whichever is larger. A capped container is
// charged its cap and not cap + usage, a capped container whose cgroup cannot be read still owes its
// cap, and an uncapped one with nothing readable is charged nothing rather than a guess.
func TestMemoryAccountingChargesEachContainerItsCapOrItsHolding(t *testing.T) {
	for _, tc := range []struct {
		name       string
		active     map[string]string
		caps       map[string]int64
		usage      map[string]sandboxruntime.MemoryUsageMB
		wantHeld   int64
		wantCharge int64
	}{
		{
			name:   "a capped container beside an uncapped one",
			active: map[string]string{"task-a": "c-a", "proj:p": "c-p"},
			caps:   map[string]int64{"task-a": 512, "proj:p": 0},
			usage: map[string]sandboxruntime.MemoryUsageMB{
				"c-a": {Known: true, Current: 300, Limit: 512},
				"c-p": {Known: true, Current: 90},
			},
			wantHeld: 390, wantCharge: 602, // 300 + 90 held; max(300,512) + max(90,0) charged
		},
		{
			name:     "nothing readable yet",
			active:   map[string]string{"task-a": "c-a", "task-b": "c-b"},
			caps:     map[string]int64{"task-a": 512, "task-b": 0},
			usage:    nil, // no cgroup read yet, or it went away mid-run
			wantHeld: 0, wantCharge: 512,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := &SandboxManager{active: tc.active, capsMB: tc.caps, mem: fixedMemory(tc.usage)}

			held, charge := sm.MemoryAccounting()
			if held != tc.wantHeld {
				t.Errorf("held = %d, want %d", held, tc.wantHeld)
			}
			if charge != tc.wantCharge {
				t.Errorf("charge = %d, want %d", charge, tc.wantCharge)
			}
		})
	}
}
