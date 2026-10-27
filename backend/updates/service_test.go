package updates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"mike/backend/errs"
	"mike/pkg/logging"
)

// fakeFetcher answers the network without any. Every fake counts its calls: a check that was refused
// must have cost no request.
type fakeFetcher struct {
	body         []byte
	getErr       error
	dlBody       []byte
	dlErr        error
	getCalls     int
	dlCalls      int
	lastGet      string
	lastDownload string
	lastLimit    int64
}

func (f *fakeFetcher) Get(_ context.Context, url string) ([]byte, error) {
	f.getCalls++
	f.lastGet = url
	return f.body, f.getErr
}

// Download writes what Get's fake would have streamed, so the staging tests exercise the same path a
// real archive does: a file on disk, hashed as it landed.
func (f *fakeFetcher) Download(_ context.Context, url, dest string, limit int64) (int64, string, error) {
	f.dlCalls++
	f.lastDownload = url
	f.lastLimit = limit
	if f.dlErr != nil {
		return 0, "", f.dlErr
	}
	if int64(len(f.dlBody)) > limit {
		return 0, "", fmt.Errorf("fake: %d bytes is over the %d byte limit", len(f.dlBody), limit)
	}
	if err := os.WriteFile(dest, f.dlBody, 0o600); err != nil {
		return 0, "", err
	}
	return int64(len(f.dlBody)), digestHex(f.dlBody), nil
}

type fakeStore struct {
	values map[string]string
	fail   error
	sets   int
}

func (f *fakeStore) GetConfigValue(key string) (string, error) {
	if f.fail != nil {
		return "", f.fail
	}
	return f.values[key], nil
}

func (f *fakeStore) SetConfigValue(key, value string) error {
	if f.fail != nil {
		return f.fail
	}
	f.sets++
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

// fixedClock makes CheckedAt an assertion instead of a timestamp nobody compares.
func fixedClock() func() time.Time {
	t := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func testService(t *testing.T, o Options) (*Service, *fakeFetcher, *fakeStore) {
	t.Helper()
	fetcher := &fakeFetcher{body: manifestDoc(t, nil)}
	if o.Fetcher == nil {
		o.Fetcher = fetcher
	}
	if o.Store == nil {
		o.Store = &fakeStore{}
	}
	if o.Version == "" {
		o.Version = "1.4.0"
	}
	if o.InstallDir == "" {
		o.InstallDir = releaseDir(t)
	}
	if o.Now == nil {
		o.Now = fixedClock()
	}
	if o.ContainerMarkers == nil {
		// Not a container, whatever the machine running the test is.
		o.ContainerMarkers = []string{}
	}
	s, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, fetcher, o.Store.(*fakeStore)
}

// An operator who never opted in must not pay for a request, and the refusal has to say who said no.
func TestCheckRefusesWhileDisabled(t *testing.T) {
	s, fetcher, store := testService(t, Options{})

	_, err := s.Check(context.Background(), Settings{Enabled: false, Answered: true})
	if err == nil {
		t.Fatalf("Check ran with the preference off")
	}
	// A switch that is off is the caller's own doing, not a server fault: the API answers 409.
	if !errs.IsConflict(err) {
		t.Errorf("error %q is not a conflict (%v)", err, err)
	}
	if fetcher.getCalls != 0 {
		t.Errorf("a refused check made %d requests", fetcher.getCalls)
	}
	if store.sets != 0 {
		t.Errorf("a refused check wrote %d rows", store.sets)
	}

	t.Run("the preference being off names where to turn it on", func(t *testing.T) {
		_, err := s.Check(context.Background(), Settings{Enabled: false, Answered: true})
		if err == nil || !strings.Contains(err.Error(), "Settings") {
			t.Errorf("error %q does not say where the switch is", err)
		}
	})
}

func TestCheckRecordsWhatIsNewer(t *testing.T) {
	cases := []struct {
		name      string
		current   string
		latest    string
		wantAvail bool
		wantAsset bool
	}{
		{name: "a newer release with an archive", current: "1.4.0", latest: "1.4.2", wantAvail: true, wantAsset: true},
		{name: "the same release", current: "1.4.2", latest: "1.4.2"},
		{name: "a newer binary than the manifest", current: "1.5.0", latest: "1.4.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, fetcher, store := testService(t, Options{Version: tc.current})
			fetcher.body = manifestDoc(t, map[string]any{"latest": tc.latest})

			before := s.Status(Settings{Enabled: true})
			if before.State.Available {
				t.Fatalf("the service began this test believing an update was available")
			}

			st, err := s.Check(context.Background(), Settings{Enabled: true, Answered: true})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if st.State.Note != "" {
				t.Errorf("Note = %q on a check that had nothing to caveat", st.State.Note)
			}
			if st.State.Available != tc.wantAvail {
				t.Errorf("Available = %v, want %v", st.State.Available, tc.wantAvail)
			}
			if gotAsset := st.State.Asset.URL != ""; gotAsset != tc.wantAsset {
				t.Errorf("asset present = %v, want %v (%+v)", gotAsset, tc.wantAsset, st.State.Asset)
			}
			if st.State.Latest != strings.TrimPrefix(tc.latest, "v") {
				t.Errorf("Latest = %q, want %q", st.State.Latest, tc.latest)
			}
			if st.State.CheckedAt != "2026-09-18T12:00:00Z" {
				t.Errorf("CheckedAt = %q, want the clock this test owns", st.State.CheckedAt)
			}
			if st.State.LastError != "" {
				t.Errorf("LastError = %q after a successful check", st.State.LastError)
			}
			if store.sets == 0 {
				t.Errorf("the result was never persisted, so the dot will not survive a restart")
			}
		})
	}
}

// A manifest with no archive for this machine is an answer about the release, not a fault: the dot stays
// dark and the error stays empty, because there is nothing for a person to do.
func TestCheckWithoutAnAssetForThisMachine(t *testing.T) {
	s, fetcher, _ := testService(t, Options{Version: "1.4.0"})
	fetcher.body = manifestDoc(t, map[string]any{
		"latest": "1.4.2",
		"assets": map[string]any{
			"plan9/m68k": map[string]any{"url": "https://getmike.dev/mike.tar.gz", "sha256": strings.Repeat("a", 64)},
		},
	})
	st, err := s.Check(context.Background(), Settings{Enabled: true})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st.State.Available {
		t.Errorf("Available = %v for a release with no archive here", st.State.Available)
	}
	if st.State.Latest != "1.4.2" {
		t.Errorf("Latest = %q, want the version the manifest named", st.State.Latest)
	}
	if !strings.Contains(st.State.Note, "no archive") {
		t.Errorf("Note = %q, want the answer to say which platform is missing", st.State.Note)
	}
}

// A failed check must not dim the dot: a server that cannot reach the manifest today is still running
// the release yesterday's answer said was old.
func TestFailedCheckKeepsTheLastGoodAnswer(t *testing.T) {
	s, fetcher, _ := testService(t, Options{Version: "1.4.0"})
	if _, err := s.Check(context.Background(), Settings{Enabled: true}); err != nil {
		t.Fatalf("first Check: %v", err)
	}
	if !s.Status(Settings{}).State.Available {
		t.Fatalf("the first check did not light the dot")
	}

	fetcher.getErr = errors.New("dial tcp: lookup getmike.dev: no such host")
	fetcher.body = nil
	st, err := s.Check(context.Background(), Settings{Enabled: true})
	if err == nil {
		t.Fatalf("Check reported success over a fetch failure")
	}
	if !st.State.Available {
		t.Errorf("the failure cleared an answer that is still true")
	}
	if st.State.Latest != "1.4.2" {
		t.Errorf("Latest = %q, want the last good answer", st.State.Latest)
	}
	if !strings.Contains(st.State.LastError, "getmike.dev") {
		t.Errorf("LastError = %q, want the cause the About tab can show", st.State.LastError)
	}

	// The next success clears the failure, so a transient DNS error does not stay on screen.
	fetcher.getErr = nil
	fetcher.body = manifestDoc(t, nil)
	st, err = s.Check(context.Background(), Settings{Enabled: true})
	if err != nil {
		t.Fatalf("recovery Check: %v", err)
	}
	if st.State.LastError != "" {
		t.Errorf("LastError = %q after a successful check", st.State.LastError)
	}
}

// The dot is answered from the stored row, so a restart does not blank the About tab and a page load
// costs no request.
func TestStateSurvivesARestart(t *testing.T) {
	store := &fakeStore{}
	dir := releaseDir(t)

	first, err := New(Options{Version: "1.4.0", Store: store, Fetcher: &fakeFetcher{body: manifestDoc(t, nil)}, InstallDir: dir, ContainerMarkers: []string{}, Now: fixedClock()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := first.Check(context.Background(), Settings{Enabled: true}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	second, err := New(Options{Version: "1.4.0", Store: store, Fetcher: &fakeFetcher{}, InstallDir: dir, ContainerMarkers: []string{}, Now: fixedClock()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := second.Status(Settings{})
	if !st.State.Available || st.State.Latest != "1.4.2" {
		t.Errorf("the rebuilt service forgot the last check: %+v", st.State)
	}
	// The stored current version belongs to the process that wrote it, not to the one reading it now.
	if st.State.CurrentVersion != "1.4.0" {
		t.Errorf("CurrentVersion = %q", st.State.CurrentVersion)
	}
}

// The row is a cache, not a record: a value that no longer decodes is reported and dropped, and never
// takes the boot down with it.
func TestAGarbledStoredResultIsReportedAndDropped(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	store := &fakeStore{values: map[string]string{stateKey: "not json at all"}}
	s, err := New(Options{Version: "1.4.0", Store: store, Fetcher: &fakeFetcher{}, InstallDir: releaseDir(t)})
	if err != nil {
		t.Fatalf("New refused the boot over a cache entry: %v", err)
	}
	if got := s.Status(Settings{}).State.Latest; got != "" {
		t.Errorf("Latest = %q, want the garbled row dropped", got)
	}
	if !strings.Contains(buf.String(), "component=updates") {
		t.Errorf("the dropped row was not logged by its component: %q", buf.String())
	}
}

// A store that cannot be read is the same story: the answer is "nothing known yet", not a fault.
func TestAnUnreadableStoreIsNotFatal(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	s, err := New(Options{Version: "1.4.0", Store: &fakeStore{fail: errors.New("database is locked")}, Fetcher: &fakeFetcher{}, InstallDir: releaseDir(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.Status(Settings{}).State.CheckedAt != "" {
		t.Errorf("a service that read nothing answered as if it had checked")
	}
	if buf.String() == "" {
		t.Errorf("an unreadable store should be reported")
	}
}

// The day belongs to the install, not to whichever browser asked first: the first automatic pass of the
// day fetches and every later one is answered from what that fetch learned. docs/auto-update.md.
func TestAutomaticCheckRunsOncePerInstallDay(t *testing.T) {
	s, fetcher, _ := testService(t, Options{})

	for _, who := range []string{"first browser of the day", "second browser of the day"} {
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true}); err != nil {
			t.Fatalf("%s: %v", who, err)
		}
	}
	if fetcher.getCalls != 1 {
		t.Errorf("two automatic passes made %d manifest requests, want 1", fetcher.getCalls)
	}
	// A declined pass answers with what is known rather than nothing: its dot is the install's dot.
	if s.Status(Settings{}).State.Latest == "" {
		t.Errorf("the install knows no release after a check that ran")
	}
}

// What spends the day, and what does not. The day is a bound on manifest requests, so only a pass that
// made one — or a person's own check — may spend it.
func TestWhatSpendsTheInstallDay(t *testing.T) {
	t.Run("a manual check spends it, because it fetched", func(t *testing.T) {
		s, fetcher, _ := testService(t, Options{})
		if _, err := s.Check(context.Background(), Settings{Enabled: true, Answered: true}); err != nil {
			t.Fatalf("manual check: %v", err)
		}
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true}); err != nil {
			t.Fatalf("automatic check: %v", err)
		}
		if fetcher.getCalls != 1 {
			t.Errorf("%d manifest requests after a manual check, want 1", fetcher.getCalls)
		}
	})

	t.Run("a check that failed spent it", func(t *testing.T) {
		fetcher := &fakeFetcher{getErr: errors.New("no route to host")}
		s, _, _ := testService(t, Options{Fetcher: fetcher})
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true}); err == nil {
			t.Fatalf("the first pass hid the failure")
		}
		// The second pass is declined rather than faulty: it dials nothing and answers with the status,
		// which carries the first pass's failure in `last_error`.
		st, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true})
		if err != nil {
			t.Fatalf("a spent day is not a fault: %v", err)
		}
		if st.State.LastError == "" {
			t.Errorf("the declined pass answered without the failure the install is carrying")
		}
		if fetcher.getCalls != 1 {
			t.Errorf("%d manifest requests after a failed check, want 1: a failure costs the day like a success", fetcher.getCalls)
		}
	})

	t.Run("checks being off spends nothing", func(t *testing.T) {
		s, fetcher, _ := testService(t, Options{})
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: false, Answered: true}); err == nil {
			t.Fatalf("the pass ran with the preference off")
		}
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true}); err != nil {
			t.Fatalf("the pass after a yes: %v", err)
		}
		if fetcher.getCalls != 1 {
			t.Errorf("%d manifest requests, want the one a yes owed", fetcher.getCalls)
		}
	})

	t.Run("the next UTC day asks again", func(t *testing.T) {
		day := time.Date(2026, 9, 18, 23, 30, 0, 0, time.UTC)
		s, fetcher, _ := testService(t, Options{Now: func() time.Time { return day }})
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true}); err != nil {
			t.Fatalf("first day: %v", err)
		}
		day = day.Add(45 * time.Minute)
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true}); err != nil {
			t.Fatalf("second day: %v", err)
		}
		if fetcher.getCalls != 2 {
			t.Errorf("%d manifest requests across a day boundary, want 2", fetcher.getCalls)
		}
	})

	t.Run("a stored instant that will not parse is not today", func(t *testing.T) {
		store := &fakeStore{values: map[string]string{stateKey: `{"checked_at":"yesterday, probably"}`}}
		s, fetcher, _ := testService(t, Options{Store: store})
		if _, err := s.CheckAutomatic(context.Background(), Settings{Enabled: true, Answered: true}); err != nil {
			t.Fatalf("automatic check: %v", err)
		}
		if fetcher.getCalls != 1 {
			t.Errorf("%d manifest requests against an unreadable stamp, want 1: an unknown is a reason to ask", fetcher.getCalls)
		}
	})
}
