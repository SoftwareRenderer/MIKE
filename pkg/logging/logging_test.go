package logging

import (
	"strings"
	"sync"
	"testing"
)

func TestForSetsComponent(t *testing.T) {
	buf, restore := TestSink()
	defer restore()

	For(Merge).Infof("merged into %s", "main")
	got := buf.String()
	for _, want := range []string{"component=merge", `msg="merged into main"`, "level=INFO"} {
		if !strings.Contains(got, want) {
			t.Errorf("line is missing %q; got: %s", want, got)
		}
	}
	// The old convention must not survive: the tag became an attribute, not text.
	if strings.Contains(got, "[merge]") {
		t.Errorf("the bracket tag is still in the message: %s", got)
	}
}

func TestWithLiftsIDsIntoAttributes(t *testing.T) {
	buf, restore := TestSink()
	defer restore()

	For(Merge).With(TaskID, "frost-amber-quartz").With(ProjectID, "proj-1").Infof("merged")
	got := buf.String()
	if !strings.Contains(got, "task_id=frost-amber-quartz") || !strings.Contains(got, "project_id=proj-1") {
		t.Fatalf("ids did not become attributes: %s", got)
	}
	// The whole point of the package: one filter answers "everything about this task", so the id
	// must not ALSO be baked into the message where a grep would have to know the wording.
	if strings.Contains(got, "task=frost") {
		t.Errorf("the id is duplicated as prose: %s", got)
	}
}

func TestLevelsAreThresholdsNotJustLabels(t *testing.T) {
	buf, restore := TestSink()
	defer restore()
	SetLevel("info")
	defer SetLevel("")

	For(Exec).Debugf("chatter %d", 1)
	For(Exec).Infof("state change")
	For(Exec).Warnf("kept running anyway")
	For(Exec).Errorf("user saw a failure")
	got := buf.String()
	if strings.Contains(got, "chatter") {
		t.Errorf("Debug was emitted at level info; the level is not a threshold:\n%s", got)
	}
	for _, want := range []string{"state change", "kept running anyway", "user saw a failure", "level=WARN", "level=ERROR"} {
		if !strings.Contains(got, want) {
			t.Errorf("level info must still emit %q; got:\n%s", want, got)
		}
	}

	SetLevel("debug")
	For(Exec).Debugf("now visible")
	if !strings.Contains(buf.String(), "now visible") {
		t.Errorf("Debug still suppressed after SetLevel(\"debug\"):\n%s", buf.String())
	}
}

// A typo in MIKE_LOG_LEVEL must not silence the log: startup refuses (see ParseLevel below), and if
// a level ever reaches SetLevel unvalidated it degrades to Info, the one setting that prints
// everything the old build printed.
func TestSetLevelRejectsTyposTowardInfo(t *testing.T) {
	buf, restore := TestSink()
	defer restore()

	for _, name := range []string{"", "  ", "verbos", "INFOO", "0"} {
		SetLevel(name)
		buf.Reset()
		For(Exec).Infof("state change under %q", name)
		if !strings.Contains(buf.String(), "state change") {
			t.Errorf("SetLevel(%q) hid an Info line; a typo must degrade to info, not to silence:\n%s", name, buf.String())
		}
	}
	SetLevel("")

	// And the value startup uses is validated rather than guessed at.
	for _, bad := range []string{"verbose", "DEBUG2", "notice", "10"} {
		if _, err := ParseLevel(bad); err == nil {
			t.Errorf("ParseLevel(%q) accepted a value that is not a level", bad)
		}
	}
	for _, good := range []string{"debug", "info", "warn", "warning", "error", "INFO", " Error "} {
		if _, err := ParseLevel(good); err != nil {
			t.Errorf("ParseLevel(%q) rejected a valid level: %v", good, err)
		}
	}
}

func TestSetOutputRestores(t *testing.T) {
	buf, restore := TestSink()
	For(Store).Infof("captured")
	restore()
	// After restore, writing must not panic and must not keep landing in the dead buffer.
	bufBefore := buf.String()
	For(Store).Infof("after restore")
	if strings.Contains(buf.String(), "after restore") {
		t.Error("output still redirected after restore")
	}
	if !strings.Contains(bufBefore, "captured") {
		t.Errorf("nothing was captured while redirected: %q", bufBefore)
	}
}

func TestSafeBufferToleratesConcurrentWriters(t *testing.T) {
	// The log is written from the exec loop, websocket goroutines and shutdown steps; at shutdown
	// the last two overlap. Run this under -race.
	buf, restore := TestSink()
	defer restore()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			For(Exec).With(TaskID, "t").Infof("concurrent")
		}()
	}
	wg.Wait()
	if n := len(buf.Lines()); n != 32 {
		t.Errorf("captured %d lines, want 32 (a lost line means the buffer raced)", n)
	}
}

func TestSuppressedLevelSkipsArgumentEvaluation(t *testing.T) {
	buf, restore := TestSink()
	defer restore()
	SetLevel("warn")
	defer SetLevel("")

	// A suppressed level must not evaluate its arguments...
	For(Exec).Debugf("%s", panicOnString{})
	// ...which is the one behaviour change versus log.Printf, so it is pinned: an argument with a
	// side effect at Debug may or may not run, and nothing may depend on it running.
	if buf.String() != "" {
		t.Errorf("Debug emitted at level warn: %s", buf.String())
	}
}

type panicOnString struct{}

func (panicOnString) String() string { panic("String() must not be called for a suppressed level") }
