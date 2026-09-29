package updates

import (
	"context"
	"strings"
	"testing"

	"mike/backend/errs"
)

// The switch forbids the whole path, not just the offer. A deployment that turns updates off after a
// release was fetched must still refuse to install it: the refusal belongs to the service, so no caller
// and no cached answer can walk around it.
func TestADisabledDeploymentRefusesEveryStep(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.prepare(t); err != nil {
		t.Fatalf("prepare with updates on: %v", err)
	}

	restarts := 0
	svc, err := New(Options{
		Version:          "1.4.0",
		Store:            f.service.store,
		Fetcher:          f.fetcher,
		InstallDir:       f.dir,
		Executable:       f.exe,
		ContainerMarkers: []string{},
		Now:              fixedClock(),
		Bridge:           func() (string, error) { return f.bridge, nil },
		DisableUpdates:   true,
		Restart:          func() error { restarts++; return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !svc.Status(Settings{}).Disabled {
		t.Errorf("the answer does not say updates are disabled")
	}

	fetches := f.fetcher.getCalls
	ctx := context.Background()
	steps := map[string]func() error{
		"check":   func() error { _, err := svc.Check(ctx, Settings{Enabled: true, Answered: true}); return err },
		"prepare": func() error { _, err := svc.Prepare(ctx); return err },
		"apply":   func() error { _, err := svc.Apply(ctx); return err },
		"restart": func() error { return svc.Restart() },
	}
	for _, name := range []string{"check", "prepare", "apply", "restart"} {
		err := steps[name]()
		if err == nil {
			t.Fatalf("%s ran with updates disabled", name)
		}
		if !errs.IsConflict(err) {
			t.Errorf("%s error %q is not a conflict", name, err)
		}
		if !strings.Contains(err.Error(), EnvDisableUpdates) {
			t.Errorf("%s error %q does not name %s", name, err, EnvDisableUpdates)
		}
	}
	if f.fetcher.getCalls != fetches {
		t.Errorf("a disabled check made %d requests", f.fetcher.getCalls-fetches)
	}
	if got := read(t, f.exe); got != "OLD-BINARY" {
		t.Errorf("the binary holds %q: an install ran while disabled", got)
	}
	if restarts != 0 {
		t.Errorf("a disabled process asked for a restart %d time(s)", restarts)
	}
	// The status answer still reads: About has to say what forbids the click.
	if st := svc.Status(Settings{}); st.State.PreparedVersion != "1.4.2" {
		t.Errorf("the prepared release was forgotten rather than refused: %+v", st.State)
	}
}

// A disabled service must not touch the network at construction either — the switch decides after the
// manifest URL is checked, so a refused boot and a disabled boot both stay quiet.
func TestADisabledServiceStillReportsItsInstallShape(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "1.4.0", InstallDir: t.TempDir(), DisableUpdates: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := s.Status(Settings{Enabled: true})
	if !st.Disabled || st.Enabled {
		t.Errorf("disabled=%v enabled=%v, want disabled and not enabled", st.Disabled, st.Enabled)
	}
	if st.Install.CanStage {
		t.Errorf("the answer offers staging while disabled: %+v", st.Install)
	}
}
