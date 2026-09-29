package types

import "testing"

// TestParseOperatingMode walks the whole input space. An unknown mode is not cosmetic: the tool-set
// switch defaults to act, so accepting one would hand write tools to a task that looks read-only.
func TestParseOperatingMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   OperatingMode
		wantOK bool
	}{
		{"act", ModeAct, true},
		{"plan", ModePlan, true},
		{"explore", ModeExplore, true},

		// Not a mode. CreateTask fills in act for old rows; a caller must ask for it.
		{"", "", false},
		// Case is part of the wire format, so these are unknown values, not aliases.
		{"Plan", "", false},
		{"ACT", "", false},
		// Former names for the same idea. Aliases would accept clients off the wire format.
		{"read-only", "", false},
		{"readonly", "", false},
		{"ask", "", false},
		{"yolo", "", false},
		// What a hand-edited config or a stray paste produces.
		{" plan", "", false},
		{"plan ", "", false},
	}

	for _, tc := range cases {
		t.Run(func() string {
			if tc.in == "" {
				return "empty"
			}
			return tc.in
		}(), func(t *testing.T) {
			t.Parallel()
			got, ok := ParseOperatingMode(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("ParseOperatingMode(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestOperatingModeRoundTrip: the constants must keep spelling the wire values, because they
// cross SQLite, JSON and the WebSocket layer. Renaming a constant is fine; changing its value is
// a migration.
func TestOperatingModeRoundTrip(t *testing.T) {
	t.Parallel()
	for _, m := range []OperatingMode{ModeAct, ModePlan, ModeExplore} {
		if _, ok := ParseOperatingMode(string(m)); !ok {
			t.Errorf("%q does not parse: a constant must always be a valid wire value", m)
		}
	}
	if ModeAct != "act" || ModePlan != "plan" || ModeExplore != "explore" {
		t.Errorf("operating mode wire values changed: %q %q %q", ModeAct, ModePlan, ModeExplore)
	}
}

// TestTaskModeHelpers: the per-mode helpers are the whole vocabulary this package offers for
// "what may this turn do". Which of them mounts the worktree read-only is decided where the
// container arguments are built, not here.
func TestTaskModeHelpers(t *testing.T) {
	t.Parallel()
	for _, m := range []OperatingMode{ModeAct, ModePlan, ModeExplore, ""} {
		task := &Task{OperatingMode: m}
		if got, want := task.IsPlanMode(), m == ModePlan; got != want {
			t.Errorf("mode %q: IsPlanMode() = %v, want %v", m, got, want)
		}
		if got, want := task.IsExploreMode(), m == ModeExplore; got != want {
			t.Errorf("mode %q: IsExploreMode() = %v, want %v", m, got, want)
		}
	}
}
