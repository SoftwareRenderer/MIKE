package server

import (
	"strings"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/startup"
	"mike/pkg/logging"
)

// widenHostRoot swaps a server's jail to the widened one. It exists because production decides the
// widening once, when the server is built: a test that pins both directions needs both
// constructions, and this file is the only place that reaches the field, so production cannot widen a
// running server from the environment.
func widenHostRoot(s *Server) { s.hostRoot = fsutil.NewHostRoot(true) }

// The jail is invisible until it refuses something, so its misconfigurations get a startup line, the
// way the login budget and the Host allowlist already do.

// TestJailFollowsTheDecisionNotTheEnvironment is the reason the widening is a configuration field: a
// variable set later — to the typo someone meant as yes, or to the exact "1" — must not change the
// jail a running server enforces. Two calls of one request are measured against one jail, and the
// roots quoted in a refusal are the roots that refused.
//
// The rule that only the exact value "1" produces the widened decision is startup's, and its table of
// truthy-looking values lives there (startup.TestOptInSwitchesAreTheExactValueOne).
func TestJailFollowsTheDecisionNotTheEnvironment(t *testing.T) {
	s, _, _, _, _ := jailedHostRootServer(t)
	if s.hostRoot.Unrestricted() {
		t.Fatal("precondition: this server was built jailed")
	}

	for _, v := range []string{"true", "yes", "0", "", "1", " 1"} {
		t.Setenv(fsutil.AllowRootFSEnv, v)
		if s.hostRoot.Unrestricted() {
			t.Fatalf("%s=%q widened the jail of a server that was built jailed", fsutil.AllowRootFSEnv, v)
		}
	}
}

// TestStartupIsQuietWhenTheJailIsClosed: a closed jail is the normal state and says nothing about the
// widening variable.
func TestStartupIsQuietWhenTheJailIsClosed(t *testing.T) {
	s, _, _, _, _ := jailedHostRootServer(t)
	buf, restore := logging.TestSink()
	defer restore()

	s.logHostRootPolicy()
	if logged := strings.ReplaceAll(buf.String(), `\"`, `"`); strings.Contains(logged, fsutil.AllowRootFSEnv) {
		t.Errorf("a closed jail is the normal state and must not warn: %s", logged)
	}
}

// TestStartupAnnouncesAWidenedJail: running without the jail is a decision the operator made, and it
// has to be visible in the log rather than discovered when something was reachable that should not
// have been.
func TestStartupAnnouncesAWidenedJail(t *testing.T) {
	s := testServer(t) // the suite's fixture is widened; its roots are every fixture's temp dir
	if !s.hostRoot.Unrestricted() {
		t.Fatal("precondition: the fixture server runs widened")
	}
	buf, restore := logging.TestSink()
	defer restore()

	s.logHostRootPolicy()
	if logged := buf.String(); !strings.Contains(logged, fsutil.AllowRootFSEnv) || !strings.Contains(logged, "any host path") {
		t.Errorf("running without the jail must be visible in the log: %s", logged)
	}
}

// TestMistypedWideningIsReportedByResolution: the classic typo ("true", "yes") is a value that does
// nothing, and the operator hears it once, at boot, from the resolution that made the decision — not
// from a handler that noticed on the request that happened to look.
func TestMistypedWideningIsReportedByResolution(t *testing.T) {
	t.Setenv(startup.EnvEncryptionKey, "a-long-enough-test-key")
	for _, v := range []string{"true", "yes", "on", "0"} {
		t.Setenv(fsutil.AllowRootFSEnv, v)
		cfg, err := startup.Resolve(startup.Flags{})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		warned := strings.Contains(strings.Join(cfg.Warnings, "\n"), fsutil.AllowRootFSEnv+"="+`"`+v+`"`)
		// Any set value that is not the exact "1" is reported, "0" included: the boot says which
		// variables did nothing, rather than the operator guessing which one they mistyped.
		if want := true; warned != want {
			t.Errorf("%s=%q warned=%v, want %v (warnings: %v)", fsutil.AllowRootFSEnv, v, warned, want, cfg.Warnings)
		}
		if cfg.AllowRootFS {
			t.Errorf("%s=%q widened the jail; only the exact 1 does", fsutil.AllowRootFSEnv, v)
		}
	}
}

// TestStartupWarnsWhenTheJailResolvesToTheRoot: a home of "/" is a jail that lets everything through.
func TestStartupWarnsWhenTheJailResolvesToTheRoot(t *testing.T) {
	t.Setenv("HOME", "/")
	s := testServerWith(t, func(c *startup.Config) { c.AllowRootFS = false })
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
