package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"mike/backend/types"

	"mike/backend/startup"
)

// fixtureManifest answers a release index from a local server: the loopback spelling of
// MIKE_UPDATE_URL is what lets a test — and an operator running a mirror — be the update source.
// manifestFixture is a release index served from a local server, counting what it was asked for so a test
// can prove the absence of egress. The loopback spelling of MIKE_UPDATE_URL is what lets a test — and an
// operator running a mirror — be the update source.
type manifestFixture struct {
	*httptest.Server
	requests atomic.Int64
}

func (m *manifestFixture) hits() int64 { return m.requests.Load() }

func fixtureManifest(t *testing.T, version string) *manifestFixture {
	t.Helper()
	f := &manifestFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"schema":1,"latest":%q,"published":"2026-09-01T00:00:00Z","assets":{"linux/amd64":{"url":%q,"sha256":%q}}}`,
			version,
			"http://"+r.Host+"/mike-server-"+version+"-linux-amd64.tar.gz",
			strings.Repeat("a", 64))
	}))
	f.Server = srv
	t.Cleanup(srv.Close)
	return f
}

func TestUpdateStatusAnswersWithoutACheck(t *testing.T) {
	s := testServer(t)
	rec := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/updates/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	// Nothing has been checked, so the answer is "nothing known" rather than "up to date".
	if latest, ok := body["latest"]; ok && latest != "" {
		t.Errorf("latest = %v before any check", latest)
	}
	if available := body["available"]; available == true {
		t.Errorf("available = true before any check")
	}
	if body["manifest_url"] == "" || body["manifest_url"] == nil {
		t.Errorf("the answer does not say where it looks: %v", body["manifest_url"])
	}
	if _, ok := body["install"]; !ok {
		t.Errorf("the answer carries no install shape, so the UI cannot choose its refusal copy: %s", rec.Body)
	}
	// The preference has never been answered, which is the state that owes the first-run question.
	if answered := body["answered"]; answered == true {
		t.Errorf("answered = true on a server nobody has configured: %s", rec.Body)
	}
	if noStore := rec.Header().Get("Cache-Control"); noStore != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on an answer that changes", noStore)
	}
}

// The preference is what turns the feature on, and the organization switch outranks it — visibly, because
// a switch that does nothing needs a reason beside it.
func TestUpdateStatusReportsWhoDecided(t *testing.T) {
	cases := []struct {
		name         string
		disabled     bool
		preference   *bool
		wantEnabled  bool
		wantDisabled bool
		wantAnswered bool
	}{
		{name: "never answered", preference: nil},
		{name: "declined", preference: ptr(false), wantAnswered: true},
		{name: "opted in", preference: ptr(true), wantEnabled: true, wantAnswered: true},
		{name: "disabled over an opt-in", disabled: true, preference: ptr(true), wantDisabled: true, wantAnswered: true},
		{name: "disabled and never asked", disabled: true, wantDisabled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServerWith(t, func(c *startup.Config) { c.DisableUpdates = tc.disabled })
			if tc.preference != nil {
				setPreference(t, s, *tc.preference)
			}
			rec := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/updates/status", nil))
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v\n%s", err, rec.Body)
			}
			if got := body["enabled"]; got != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v (%s)", got, tc.wantEnabled, rec.Body)
			}
			if got := body["disabled"]; got != tc.wantDisabled {
				t.Errorf("disabled = %v, want %v", got, tc.wantDisabled)
			}
			if _, ok := body["forced"]; ok {
				t.Errorf("the answer still carries the override field someone used to force: %s", rec.Body)
			}
			if got := body["answered"]; got != tc.wantAnswered {
				t.Errorf("answered = %v, want %v", got, tc.wantAnswered)
			}
		})
	}
}

// Disabled is not a UI state: the routes that check, download, install and restart refuse it themselves,
// so neither a cached answer nor a hand-made request gets past the switch.
func TestDisabledDeploymentRefusesEveryUpdateRoute(t *testing.T) {
	manifest := fixtureManifest(t, "9.9.9")
	s := testServerWith(t, func(c *startup.Config) {
		c.UpdateManifestURL = manifest.URL
		c.DisableUpdates = true
	})
	setPreference(t, s, true)

	for _, path := range []string{"/api/updates/check", "/api/updates/prepare", "/api/updates/apply", "/api/updates/restart"} {
		rec := testResponseAuth(t, s, testRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusConflict {
			t.Errorf("POST %s = %d body %s, want 409", path, rec.Code, rec.Body)
			continue
		}
		if body := rec.Body.String(); !strings.Contains(body, "MIKE_DISABLE_UPDATES") {
			t.Errorf("refusal from %s does not name the switch: %s", path, body)
		}
	}
	// And it cost no egress: the manifest never heard from a server that was told not to ask.
	if got := manifest.hits(); got != 0 {
		t.Errorf("a disabled server made %d request(s) of the manifest", got)
	}
}

// A check with the preference off must not touch the network, and must say which switch said no.
func TestUpdateCheckRefusedWhileDisabled(t *testing.T) {
	manifest := fixtureManifest(t, "9.9.9")
	s := testServerWith(t, func(c *startup.Config) { c.UpdateManifestURL = manifest.URL })

	rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/check", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Settings") {
		t.Errorf("refusal %q does not name where to turn it on", body)
	}
}

// The loopback spelling of the manifest URL is a real path through the egress rule: a host the operator
// named is the only way a non-public address is dialled, and a mirror or a test fixture is exactly that.
func TestUpdateCheckAgainstAHostTheOperatorNamed(t *testing.T) {
	manifest := fixtureManifest(t, "9.9.9")
	s := testServerWith(t, func(c *startup.Config) { c.UpdateManifestURL = manifest.URL })
	setPreference(t, s, true)

	rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	if body["latest"] != "9.9.9" {
		t.Errorf("latest = %v, want the manifest's answer", body["latest"])
	}
	// Available depends on this binary's version, which is "dev" in a test build: what matters is that
	// the answer is a *note* about comparing, not a failure of the check.
	if lastErr, _ := body["last_error"].(string); lastErr != "" {
		t.Errorf("last_error = %q on a check that reached its manifest", lastErr)
	}
	if note, _ := body["note"].(string); !strings.Contains(note, "no release to compare") {
		t.Errorf("note = %q, want the sentence for a build with no release version", note)
	}
}

// The daily pass says so with `?auto=1`, and the install's day gates it: the second browser to boot today
// is answered from the fetch the first one's pass caused. The button sends no flag, because a person who
// asked is never told the day is spent. docs/auto-update.md.
func TestUpdateAutomaticCheckIsGatedByTheInstallDay(t *testing.T) {
	manifest := fixtureManifest(t, "9.9.9")
	s := testServerWith(t, func(c *startup.Config) { c.UpdateManifestURL = manifest.URL })
	setPreference(t, s, true)

	for _, who := range []string{"first", "second"} {
		rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/check?auto=1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s automatic pass: %d %s", who, rec.Code, rec.Body)
		}
	}
	if got := manifest.hits(); got != 1 {
		t.Errorf("two automatic passes made %d manifest requests, want 1", got)
	}

	if rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/check", nil)); rec.Code != http.StatusOK {
		t.Fatalf("manual check: %d %s", rec.Code, rec.Body)
	}
	if got := manifest.hits(); got != 2 {
		t.Errorf("a manual check left the manifest at %d requests, want 2", got)
	}
}

// A manifest that will not answer is a server fault and is answered as one. The About tab shows what the
// local status route still holds, so the failure costs nothing a re-read cannot put back.
func TestUpdateCheckReportsAFailureWithoutLosingTheAnswer(t *testing.T) {
	manifest := fixtureManifest(t, "9.9.9")
	s := testServerWith(t, func(c *startup.Config) { c.UpdateManifestURL = manifest.URL })
	setPreference(t, s, true)
	if rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/check", nil)); rec.Code != http.StatusOK {
		t.Fatalf("first check: %d %s", rec.Code, rec.Body)
	}

	// The fixture stops answering, which is what a dead mirror looks like from here.
	manifest.Close()
	rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/check", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body %s, want 500 for a manifest that will not answer", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "check for updates failed") {
		t.Errorf("body %q is not MIKE's own sentence", body)
	}

	// The cached answer survives the failure: the dot is answered from the store, not from this call.
	rec = testResponseAuth(t, s, testRequest(http.MethodGet, "/api/updates/status", nil))
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	if status["latest"] != "9.9.9" {
		t.Errorf("latest = %v, want the last good answer kept: %s", status["latest"], rec.Body)
	}
	if lastErr, _ := status["last_error"].(string); lastErr == "" {
		t.Errorf("last_error is empty after a failed check: %s", rec.Body)
	}
}

// Nothing is written over work that is running: the swap replaces the binary every in-flight turn is
// driving commits and containers with.
func TestUpdateApplyRefusesWhileATaskRuns(t *testing.T) {
	s := testServer(t)
	startRunningTask(t, s, "Write the changelog")

	rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/apply", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"Write the changelog", "work is in flight"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal %q does not say %q", body, want)
		}
	}
}

// Fetching a release renames nothing, so the refusal that guards the swap does not belong on it.
func TestUpdatePrepareIsNotStoppedByRunningWork(t *testing.T) {
	s := testServer(t)
	startRunningTask(t, s, "Write the changelog")

	rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/prepare", nil))
	if body := rec.Body.String(); strings.Contains(body, "work is in flight") {
		t.Errorf("a download was refused over running work: %d %s", rec.Code, body)
	}
}

// The install half answers a refusal as a refusal, whatever it is refusing about: this server's own
// install is not a release directory, and a POST must not make that a server fault.
func TestUpdateApplyRefusesAnInstallThatCannotTakeARelease(t *testing.T) {
	s := testServer(t)
	rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/apply", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "cannot replace its own files") {
		t.Errorf("refusal %q does not say what is missing", body)
	}
}

// A restart is only ever the second half of an install.
func TestUpdateRestartRefusesWhenNothingIsStaged(t *testing.T) {
	s := testServer(t)
	rec := testResponseAuth(t, s, testRequest(http.MethodPost, "/api/updates/restart", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "no release is staged") {
		t.Errorf("refusal %q does not say why", rec.Body)
	}
}

// The two routes that change anything are POSTs, and a verb they do not declare must never reach them.
func TestUpdateRouteVerbs(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		path string
		verb string
		want int
	}{
		{"/api/updates/status", http.MethodGet, http.StatusOK},
		{"/api/updates/status", http.MethodPost, http.StatusMethodNotAllowed},
		{"/api/updates/check", http.MethodGet, http.StatusMethodNotAllowed},
		{"/api/updates/prepare", http.MethodGet, http.StatusMethodNotAllowed},
		{"/api/updates/apply", http.MethodGet, http.StatusMethodNotAllowed},
		{"/api/updates/restart", http.MethodGet, http.StatusMethodNotAllowed},
	} {
		rec := testResponseAuth(t, s, testRequest(tc.verb, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.verb, tc.path, rec.Code, tc.want)
		}
	}
}

// A request with no session gets nothing: these routes name a download and end a process.
func TestUpdateRoutesAreNotPublic(t *testing.T) {
	s := testServer(t)
	for _, path := range []string{"/api/updates/status", "/api/updates/check"} {
		rec := testResponse(t, s, testRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s unauthenticated = %d, want 401", path, rec.Code)
		}
	}
	// The write half also needs the CSRF header, which the middleware checks before the handler.
	for _, path := range []string{"/api/updates/prepare", "/api/updates/apply"} {
		rec := testResponse(t, s, testRequest(http.MethodPost, path, nil))
		if rec.Code == http.StatusOK || rec.Code == http.StatusNoContent {
			t.Errorf("POST %s unauthenticated = %d, want a refusal", path, rec.Code)
		}
	}
}

// setPreference answers the update preference the way Settings does: a partial update carrying only
// that field.
func setPreference(t *testing.T, s *Server, enabled bool) {
	t.Helper()
	rec := testResponseAuth(t, s, testRequest(http.MethodPut, "/api/config", map[string]any{
		"update_check_enabled": enabled,
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("set preference: %d %s", rec.Code, rec.Body)
	}
}

func startRunningTask(t *testing.T, s *Server, title string) {
	t.Helper()
	if err := s.store.CreateProject(&types.Project{ID: "upd-proj", Name: "upd", Path: t.TempDir()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.store.CreateTask(&types.Task{
		ID: "upd-task", ProjectID: "upd-proj", Title: title, Branch: "task/upd",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// The refusal reads GetInFlightTasks, so the fixture is only a fixture if that query agrees.
	inFlight, err := s.store.GetInFlightTasks()
	if err != nil {
		t.Fatalf("in-flight tasks: %v", err)
	}
	if len(inFlight) != 1 {
		t.Fatalf("%d in-flight tasks; the refusal below would prove nothing", len(inFlight))
	}
}

func ptr(v bool) *bool { return &v }
