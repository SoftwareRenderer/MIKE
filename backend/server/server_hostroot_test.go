package server

import (
	"strconv"
	"strings"
	"testing"

	"kanban/backend/fsutil"
	"kanban/pkg/logging"
)

// The jail is invisible until it refuses something, so both of its silent misconfigurations
// get a startup line, the way MIKE_LOGIN_RATE_LIMIT and the Host allowlist already do.

func TestStartupEchoesAMistypedWideningVariable(t *testing.T) {
	s, _, _, _, _ := jailedHostRootServer(t)
	buf, restore := logging.TestSink()
	defer restore()

	// The classic typo: a truthy-looking value that is not the exact "1".
	for _, v := range []string{"true", "yes", "0", ""} {
		t.Setenv(fsutil.AllowRootFSEnv, v)
		s.logHostRootPolicy()
	}
	// The text handler escapes the quotes around the value, so compare against the
	// unescaped rendering rather than writing the escapes by hand.
	logged := strings.ReplaceAll(buf.String(), `\"`, `"`)
	for _, v := range []string{"true", "yes", "0"} {
		if !strings.Contains(logged, fsutil.AllowRootFSEnv+"="+strconv.Quote(v)) {
			t.Errorf("no startup line named %s=%q; logged: %s", fsutil.AllowRootFSEnv, v, logged)
		}
	}
	// The warning must also say what is still allowed, or it reads as "variable ignored".
	if !strings.Contains(logged, "jail stays in force") {
		t.Errorf("warning must say the jail stays in force: %s", logged)
	}
}

func TestStartupIsQuietWhenTheVariableIsAbsent(t *testing.T) {
	s, _, _, _, _ := jailedHostRootServer(t)
	buf, restore := logging.TestSink()
	defer restore()

	s.logHostRootPolicy()
	if logged := strings.ReplaceAll(buf.String(), `\"`, `"`); strings.Contains(logged, fsutil.AllowRootFSEnv) {
		t.Errorf("an unset variable is the normal state and must not warn: %s", logged)
	}
}

func TestStartupAnnouncesAWidenedJail(t *testing.T) {
	s, _, _, _, _ := jailedHostRootServer(t)
	t.Setenv(fsutil.AllowRootFSEnv, "1")
	buf, restore := logging.TestSink()
	defer restore()

	s.logHostRootPolicy()
	if logged := buf.String(); !strings.Contains(logged, fsutil.AllowRootFSEnv) || !strings.Contains(logged, "any host path") {
		t.Errorf("running without the jail must be visible in the log: %s", logged)
	}
}

func TestStartupWarnsWhenTheJailResolvesToTheRoot(t *testing.T) {
	// A home of "/" is a jail that lets everything through.
	t.Setenv("HOME", "/")
	t.Setenv(fsutil.AllowRootFSEnv, "")
	s := testServer(t)
	if !s.hostRoot.HasUniversalRoot() {
		t.Fatalf("expected the jail to include the filesystem root; roots = %v", s.hostRoot.Roots())
	}

	buf, restore := logging.TestSink()
	defer restore()
	s.logHostRootPolicy()
	if logged := buf.String(); !strings.Contains(logged, "filesystem root") {
		t.Errorf("no warning about a jail rooted at /: %s", logged)
	}
}
