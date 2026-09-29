package server

import (
	"context"
	"strings"
	"testing"

	"mike/pkg/logging"
)

// TestShutdownOrderIsWhatWeDeclared: the declared sequence is [stageCount] names, and Shutdown must run
// the registered steps in exactly that order whatever order they were registered in. Construction order
// cannot express this sequence (the LSP handler is built after the sandbox manager but must close before
// it), so the order is a declaration and this test is what keeps it one.
func TestShutdownOrderIsWhatWeDeclared(t *testing.T) {
	var ran []string
	record := func(name string) func() { return func() { ran = append(ran, name) } }
	logged, restore := logging.TestSink()
	defer restore()

	s := &Server{}
	// Registered back to front, so an implementation that walked registration order fails here.
	for st := stageCount - 1; st >= 0; st-- {
		if err := s.reg(st, stageNames[st], record(stageNames[st]))(); err != nil {
			t.Fatalf("register %s: %v", stageNames[st], err)
		}
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	want := strings.Join(stageNames[:], ",")
	got := strings.Join(ran, ",")
	if got != want {
		t.Errorf("shutdown order =\n  %s\nwant (the declared order)\n  %s", got, want)
	}

	// Every step names itself before it runs. When the shutdown deadline fires, the last line in the log
	// is the only thing that names what wedged, so a step that logs nothing is a wedge with no owner.
	out := logged.String()
	last := -1
	for st := stage(0); st < stageCount; st++ {
		at := strings.Index(out, "shutdown: "+stageNames[st])
		if at < 0 {
			t.Errorf("no log line for the %q step", stageNames[st])
			continue
		}
		if at < last {
			t.Errorf("the log line for %q came out of order", stageNames[st])
		}
		last = at
	}
}

// The sequence is only declared if every stage has a name: an unlisted stage would render as the empty
// string and a wedged shutdown would log "shutdown: " with nothing to name.
func TestShutdownStagesAreDeclaredEntirely(t *testing.T) {
	for st := stage(0); st < stageCount; st++ {
		if stageNames[st] == "" {
			t.Errorf("stage %d has no name; add it to stageNames", int(st))
		}
	}
	if stageNames[stageCount-1] != "database" {
		t.Errorf("the last stage must be the database, got %q", stageNames[stageCount-1])
	}
	if stageNames[stageDetachedWork] != "detached work" {
		t.Errorf("the first stage must be the detached-work cancellation, got %q", stageNames[stageDetachedWork])
	}
	if stageNames[stagePreviewSweeper] != "preview sweeper" {
		t.Errorf("the second stage must be the preview sweeper, got %q", stageNames[stagePreviewSweeper])
	}
}

// A component a test never built has no step, so the sequence must skip it rather than fail — this is the
// property that replaced eleven nil checks.
func TestShutdownRunsOnlyWhatWasRegistered(t *testing.T) {
	var ran []string
	s := &Server{}
	if err := s.reg(stageTerminalSessions, "terminal sessions", func() { ran = append(ran, "terminal sessions") })(); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if len(ran) != 1 || ran[0] != "terminal sessions" {
		t.Errorf("ran %v, want only the one registered step", ran)
	}
}

// A second registration of one stage would silently drop a step, which is a leaked goroutine discovered
// later as a shutdown that never returns.
func TestRegistrationRefusesToOverwriteAStage(t *testing.T) {
	s := &Server{}
	if err := s.reg(stageDatabase, "database", func() {})(); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := s.reg(stageDatabase, "database", func() {})()
	if err == nil {
		t.Fatal("a second registration of the same stage was accepted")
	}
	if !strings.Contains(err.Error(), "registered twice") {
		t.Errorf("error = %v, want it to name the double registration", err)
	}
}

// NewServer registers the real steps; a server built by the production constructor must walk the whole
// declared sequence. This is the check that a component cannot be added without its step.
func TestNewServerRegistersEveryStage(t *testing.T) {
	s := testServer(t)
	var missing []string
	for st := stage(0); st < stageCount; st++ {
		if s.shutdown[st] == nil {
			missing = append(missing, stageNames[st])
		}
	}
	if len(missing) != 0 {
		t.Errorf("stages with no registered step: %v", missing)
	}

	// No Shutdown here: testServer registers one as a cleanup, and a step run twice (a closed channel,
	// a closed database) is a different failure than the one this test is about.
}

// A detached operation outlives the request that started it and dies with the process. Both halves matter:
// cancelling one operation must not cancel another (they share the base only for the shutdown signal), and
// the base has no deadline, because the bound on a merge is the work finishing, not a timer.
func TestDetachedContextsShareTheBaseAndNothingElse(t *testing.T) {
	s := testServer(t)

	a, cancelA := s.Detached()
	b, _ := s.Detached()
	defer cancelA()

	cancelA()
	if a.Err() == nil {
		t.Error("the cancelled detached context is still live")
	}
	if b.Err() != nil {
		t.Errorf("cancelling one detached operation cancelled another: %v", b.Err())
	}
	if _, ok := a.Deadline(); ok {
		t.Error("a detached context has a deadline; the bound is the process stopping, not a timer")
	}
}

// The first shutdown stage is the base cancellation, so every step that runs afterwards can still be
// written to. A task that unwinds into a closed database is how a shutdown starts reporting phantom
// failures the operator cannot reproduce.
func TestShutdownCancelsTheBaseForEverythingStillRunning(t *testing.T) {
	s := &Server{}
	base, cancel := context.WithCancel(context.Background())
	s.base, s.baseCancel = base, cancel
	s.reg(stageDetachedWork, "detached work", cancel)

	detached, _ := s.Detached()
	if detached.Err() != nil {
		t.Fatalf("a detached context is dead before shutdown: %v", detached.Err())
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if detached.Err() == nil {
		t.Error("shutdown left a detached operation running — nothing will ever cancel it")
	}
}

// A step registered as nil — the method value of a component that is not assigned yet — is a nil-func call
// inside Shutdown, discovered as a panic during an exit. It is a boot failure instead.
func TestRegistrationRefusesANilStep(t *testing.T) {
	s := &Server{}
	err := s.reg(stageTerminalSessions, "terminal sessions", nil)()
	if err == nil {
		t.Fatal("a nil step was accepted")
	}
	if !strings.Contains(err.Error(), "no step") {
		t.Errorf("error = %v, want it to name the missing step", err)
	}
	if s.shutdown[stageTerminalSessions] != nil {
		t.Error("a refused registration still occupied its stage")
	}
}
