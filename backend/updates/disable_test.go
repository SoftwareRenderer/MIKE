package updates

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/errs"
)

// The switch forbids the whole path, not just the offer. A deployment that turns updates off after a
// release was fetched must still refuse to install it: the refusal belongs to the service, so no caller
// and no cached answer can walk around it.
func TestADisabledDeploymentRefusesEveryStep(t *testing.T) {
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
	if !svc.Status(Settings{}).Managed {
		t.Errorf("the answer does not say updates are managed")
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
	s, err := New(Options{Version: "1.4.0", InstallDir: t.TempDir(), DisableUpdates: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := s.Status(Settings{Enabled: true})
	if !st.Managed || st.Enabled {
		t.Errorf("managed=%v enabled=%v, want managed and not enabled", st.Managed, st.Enabled)
	}
	if st.Install.CanStage {
		t.Errorf("the answer offers staging while managed: %+v", st.Install)
	}
}

// An install that cannot replace its own files is managed, even when it knows about a newer release. The
// refusal comes before a byte is asked for, because a downloaded release sitting in a container image is the
// worst outcome of a half-applied update.
func TestAnInstallThatCannotTakeAReleaseIsManaged(t *testing.T) {
	marker := filepath.Join(t.TempDir(), ".dockerenv")
	write(t, marker, "MARKER", 0o644)

	cases := []struct {
		name  string
		opts  Options
		wants []string
	}{
		{name: "a container image", opts: Options{Version: "1.4.0", InstallDir: releaseDir(t), ContainerMarkers: []string{marker}}, wants: []string{"managed by your administrator", "container image"}},
		{name: "a development build", opts: Options{Version: "dev", ContainerMarkers: []string{}}, wants: []string{"managed by your administrator", "not a release version"}},
		{name: "a binary outside a release layout", opts: Options{Version: "1.4.0", InstallDir: t.TempDir(), ContainerMarkers: []string{}}, wants: []string{"managed by your administrator", "unpacked release"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := &fakeFetcher{body: manifestDoc(t, map[string]any{"latest": "1.4.2"}), dlBody: goodArchive(t)}
			o := tc.opts
			o.Fetcher, o.Store, o.Now = fetcher, &fakeStore{}, fixedClock()
			svc, err := New(o)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			st := svc.Status(Settings{Enabled: true, Answered: true})
			if !st.Managed || st.Enabled {
				t.Errorf("managed=%v enabled=%v, want managed and not enabled", st.Managed, st.Enabled)
			}
			if st.Install.CanStage {
				t.Errorf("the answer offers staging: %+v", st.Install)
			}

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
					t.Fatalf("%s ran on an install that cannot take a release", name)
				}
				if !errs.IsConflict(err) {
					t.Errorf("%s error %q is not a conflict", name, err)
				}
				for _, want := range tc.wants {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%s error %q does not say %q", name, err, want)
					}
				}
			}
			if fetcher.getCalls != 0 {
				t.Errorf("a managed server made %d request(s) of the manifest", fetcher.getCalls)
			}
			if fetcher.dlCalls != 0 {
				t.Errorf("a managed server downloaded %d archive(s)", fetcher.dlCalls)
			}
		})
	}
}
