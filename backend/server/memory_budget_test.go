package server

import (
	"strings"
	"testing"

	"mike/pkg/logging"
)

func TestMeminfoMB(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		total    int64
		avail    int64
		ok       bool
	}{
		{"both lines", "MemTotal:       15504384 kB\nMemFree:         1234 kB\nMemAvailable:   3245056 kB\n", 15141, 3169, true},
		// MemAvailable is the line the budget is built on; without it there is no usable answer.
		{"no MemAvailable", "MemTotal: 15504384 kB\n", 0, 0, false},
		{"no MemTotal", "MemAvailable: 3245056 kB\n", 0, 0, false},
		{"unparsable value", "MemTotal: many kB\nMemAvailable: 3245056 kB\n", 0, 0, false},
		{"empty", "", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			total, avail, ok := meminfoMB(tc.contents)
			if ok != tc.ok || total != tc.total || avail != tc.avail {
				t.Errorf("meminfoMB() = (%d, %d, %v), want (%d, %d, %v)", total, avail, ok, tc.total, tc.avail, tc.ok)
			}
		})
	}
}

// Measure is the one figure the gate, the About tab and the supervisor tick all read: the accountants
// summed (a container belongs to only one of them, so summing is correct), the server's own process
// beside them, and the host's two readings passed through for the floor rule. An unreadable host is
// reported as not measured, never as a host holding nothing.
func TestMemoryBudgetMeasure(t *testing.T) {
	cases := []struct {
		name          string
		accountants   []memoryAccountant
		meminfo       func() (int64, int64, bool)
		wantHeld      int64
		wantMike      int64
		wantAvailable int64
		wantTotal     int64
		wantOK        bool
	}{
		{
			name: "every manager and the server's own process",
			accountants: []memoryAccountant{
				fixedAccountant{held: 300, charge: 512},
				fixedAccountant{held: 90, charge: 90},
				fixedAccountant{held: 120, charge: 120},
			},
			meminfo: func() (int64, int64, bool) { return 1000, 400, true },
			// The floor rule needs free and total; it cannot derive them from the used figure.
			wantHeld: 510, wantMike: 722, wantAvailable: 400, wantTotal: 1000, wantOK: true,
		},
		{
			name:        "an unreadable host is not measured",
			accountants: []memoryAccountant{fixedAccountant{held: 300, charge: 512}},
			meminfo:     func() (int64, int64, bool) { return 0, 0, false },
			wantOK:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &memoryBudget{accountants: tc.accountants, meminfo: tc.meminfo}
			mikeMB, held, available, total, ok := b.Measure()
			if ok != tc.wantOK || held != tc.wantHeld || mikeMB != tc.wantMike ||
				available != tc.wantAvailable || total != tc.wantTotal {
				t.Errorf("Measure() = (%d, %d, %d, %d, %v), want (%d, %d, %d, %d, %v)",
					mikeMB, held, available, total, ok,
					tc.wantMike, tc.wantHeld, tc.wantAvailable, tc.wantTotal, tc.wantOK)
			}
		})
	}
}

func TestSelfRSSMB(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		want     int64
		ok       bool
	}{
		{"VmRSS line", "Name:\tkanban\nVmRSS:\t  248532 kB\nVmSize:\t 999999 kB\n", 242, true},
		// A process that never reached the line has no answer; zero would be read as "holds nothing".
		{"no VmRSS", "Name:\tkanban\n", 0, false},
		{"unparsable value", "VmRSS:\tmany kB\n", 0, false},
		{"no number", "VmRSS:\t\n", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := selfRSSMB(tc.contents)
			if ok != tc.ok || got != tc.want {
				t.Errorf("selfRSSMB() = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// MIKE's own process is in the budget: it has no cgroup and no limit, so what it holds is what it
// costs. An unreadable reading adds nothing and says so once, not once per tick.
func TestSelfProcessAccountantChargesTheServer(t *testing.T) {
	held, charge := (&selfProcessAccountant{read: func() (int64, bool) { return 242, true }}).MemoryAccounting()
	if held != 242 || charge != 242 {
		t.Errorf("held=%d charge=%d, want 242/242", held, charge)
	}

	buf, restore := logging.TestSink()
	defer restore()
	unreadable := &selfProcessAccountant{read: func() (int64, bool) { return 0, false }}
	for range 3 {
		if h, c := unreadable.MemoryAccounting(); h != 0 || c != 0 {
			t.Fatalf("an unreadable reading must add nothing, got %d/%d", h, c)
		}
	}
	out := buf.String()
	if n := strings.Count(out, "VmRSS unavailable"); n != 1 {
		t.Errorf("the missing reading was reported %d times, want 1; log:\n%s", n, out)
	}
}

type fixedAccountant struct{ held, charge int64 }

func (f fixedAccountant) MemoryAccounting() (int64, int64) { return f.held, f.charge }
