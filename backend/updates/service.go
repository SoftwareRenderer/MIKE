package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mike/backend/errs"
	"mike/backend/httputil"
	"mike/pkg/logging"
)

// stateKey is the config row the last check result lives in, so a page load costs no outbound request.
const stateKey = "update_check_result"

// Store is the persistence this package needs: one row of its own.
type Store interface {
	GetConfigValue(key string) (string, error)
	SetConfigValue(key, value string) error
}

// Settings is the caller's decision about whether checks may run. This package never reads the preference
// or the environment: the answer arrives as a value, so "who decided" is not re-derived in three places.
type Settings struct {
	// Enabled is the effective answer: off when the organization switch forbids it, the stored
	// preference otherwise.
	Enabled bool
	// Answered is false while nobody has ever answered, which owes the first-run prompt. It is not
	// the same as Enabled=false.
	Answered bool
}

// State is what the last check learned and what is still owed.
type State struct {
	// CurrentVersion is this binary's version, as /api/version answers it.
	CurrentVersion string `json:"current_version"`
	// Latest is the manifest's answer, empty when no check has produced one.
	Latest string `json:"latest,omitempty"`
	// Published and NotesURL come from the same manifest as Latest and are shown beside it.
	Published string `json:"published,omitempty"`
	NotesURL  string `json:"notes_url,omitempty"`
	// Asset is this machine's archive, empty when the newest manifest names none for it.
	Asset Asset `json:"asset,omitempty"`
	// Available is the whole point: a newer release with an archive for this machine.
	Available bool `json:"available"`
	// CheckedAt is when the last check finished, in fixed-width UTC.
	CheckedAt string `json:"checked_at,omitempty"`
	// LastError is the last check's failure in MIKE's own words; the next success clears it.
	LastError string `json:"last_error,omitempty"`
	// Note is what a check learned that is neither an error nor a version.
	Note string `json:"note,omitempty"`
	// StagedVersion is the release sitting on disk awaiting a restart, empty when nothing does.
	StagedVersion string `json:"staged_version,omitempty"`
	// BackupPath names the previous release's files, kept by the swap that put StagedVersion in place.
	BackupPath string `json:"backup_path,omitempty"`
	// PreparedVersion is a downloaded, verified release waiting for the install click. PreparedSHA256
	// and PreparedBytes are what the archive is re-checked against when it is installed; PreparedFiles
	// is what the answer says installing would replace.
	PreparedVersion string `json:"prepared_version,omitempty"`
	PreparedSHA256  string `json:"prepared_sha256,omitempty"`
	PreparedBytes   int64  `json:"prepared_bytes,omitempty"`
	PreparedFiles   int    `json:"prepared_files,omitempty"`
	PreparedAt      string `json:"prepared_at,omitempty"`
}

// Status is the status answer: the last known state plus what is true of this install right now.
type Status struct {
	State
	Enabled bool `json:"enabled"`
	// Disabled is the organization switch: nothing here checks, downloads, installs or restarts, and
	// the client says so instead of offering the click.
	Disabled bool `json:"disabled"`
	Answered bool `json:"answered"`
	// Install is what this process could do about an update.
	Install Install `json:"install"`
	// ManifestURL is where the check looks, so a mirror is visible in the answer.
	ManifestURL string `json:"manifest_url"`
	// Working names the operation running now — "prepare" or "apply" — empty when none is. A second one
	// is refused, so a row can show progress rather than start another.
	Working string `json:"working,omitempty"`
}

// Service answers the update questions for one server: one install directory per process, so nothing
// is keyed and its mutex is the whole locking story.
type Service struct {
	mu sync.Mutex

	manifestURL string
	version     string
	goos        string
	goarch      string
	store       Store
	fetcher     Fetcher
	bridge      BridgePath
	restart     func() error
	exe         string
	install     Install
	now         func() time.Time
	disabled    bool

	state State
	// workMu guards which operation this process is running. A download takes minutes and a swap takes
	// seconds; neither may overlap the other, and a status request during either must still answer, so
	// neither takes the state mutex.
	workMu  sync.Mutex
	working string
}

// Options is what a Service needs and nothing else. The zero value is not usable; New refuses it.
type Options struct {
	// ManifestURL is the resolved decision from startup.Config; empty means DefaultManifestURL.
	ManifestURL string
	// Version is this binary's version (server.Version).
	Version string
	// Store may be nil, which keeps the last result in memory only.
	Store Store
	// Fetcher may be nil, which uses the real one.
	Fetcher Fetcher
	// Bridge names the sandbox-bridge file this server runs, so a staged release replaces the file in
	// use rather than the one the archive is named after. Nil leaves the bridge alone.
	Bridge BridgePath
	// Restart hands the process back to whatever started it; main.go owns it. Nil means this process
	// has no restart path, which the restart route reports rather than swallowing.
	Restart func() error
	// InstallDir overrides the install directory the shape is decided from. Test-only.
	InstallDir string
	// Executable overrides the path of the running binary. Test-only: without it a test that staged
	// anything would replace the test binary.
	Executable string
	// ContainerMarkers overrides the files that say "this is a container". Test-only: nil means the
	// defaults, and an empty list means "not a container".
	ContainerMarkers []string
	// Now overrides the clock. Test-only.
	Now func() time.Time
	// DisableUpdates is the resolved MIKE_DISABLE_UPDATES decision. Nothing in this package reads the
	// environment; the answer arrives here.
	DisableUpdates bool
}

// New builds the service and reads back whatever the last run learned. A stored result that no longer
// decodes is reported and dropped: it is a cache, and a bad row must not take the server down.
func New(o Options) (*Service, error) {
	url := o.ManifestURL
	if url == "" {
		url = DefaultManifestURL
	}
	if err := checkManifestURL(url); err != nil {
		return nil, err
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	s := &Service{
		manifestURL: url,
		version:     o.Version,
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		store:       o.Store,
		fetcher:     o.Fetcher,
		bridge:      o.Bridge,
		restart:     o.Restart,
		now:         now,
		disabled:    o.DisableUpdates,
		state:       State{CurrentVersion: o.Version},
	}
	if s.fetcher == nil {
		s.fetcher = httpFetcher{}
	}
	s.exe = o.Executable
	if s.exe == "" {
		s.exe = executablePath()
	}
	s.install = classifyInstall(o.InstallDir, o.Version, o.ContainerMarkers)
	raw, err := s.load()
	if err != nil {
		logging.For(logging.Updates).Warnf("stored update result ignored: %v", err)
	} else if raw != "" {
		var stored State
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			logging.For(logging.Updates).Warnf("stored update result ignored: %v", err)
		} else {
			stored.CurrentVersion = o.Version
			s.state = stored
		}
	}
	// The stored answer is older than this process: the newest release may be the one running now, and
	// so may the one that was staged. Left alone both keep asking for a click — the dot stays green on
	// the release already in the seat — until the next check, at most once a day.
	if KnownVersion(o.Version) {
		settled := false
		if s.state.Latest != "" {
			if newer, err := CompareVersions(s.state.Latest, o.Version); err == nil && newer <= 0 {
				s.state.Available = false
				s.state.Asset = Asset{}
				s.state.Note = ""
				settled = true
			}
		}
		if s.state.StagedVersion != "" {
			if same, err := CompareVersions(s.state.StagedVersion, o.Version); err == nil && same <= 0 {
				s.state.StagedVersion = ""
				s.state.BackupPath = ""
				settled = true
			}
		}
		if s.state.PreparedVersion != "" {
			if same, err := CompareVersions(s.state.PreparedVersion, o.Version); err == nil && same <= 0 {
				s.state.PreparedVersion = ""
				s.state.PreparedSHA256 = ""
				s.state.PreparedBytes = 0
				s.state.PreparedFiles = 0
				s.state.PreparedAt = ""
				settled = true
			}
		}
		if settled {
			s.persist()
		}
	}
	// What the last process left on disk is this one's to settle: an interrupted attempt is reaped by
	// name, and a downloaded release is kept only if the answer above still names it.
	if s.install.CanStage {
		s.reapWorkDirs()
		s.reconcilePrepared()
	}
	return s, nil
}

// reapWorkDirs removes the directories this package writes that nothing can still be reading: a
// throwaway staging tree, an archive still being written, an abandoned verification tree.
func (s *Service) reapWorkDirs() {
	for _, pattern := range []string{stagingPrefix + "*", filepath.Join(preparedDirName, "*"+partSuffix), filepath.Join(preparedDirName, "verify-*")} {
		found, err := filepath.Glob(filepath.Join(s.install.Dir, pattern))
		if err != nil {
			continue
		}
		for _, old := range found {
			os.RemoveAll(old)
		}
		if len(found) > 0 {
			logging.For(logging.Updates).Infof("removed %d leftover(s) matching %s from %s", len(found), pattern, s.install.Dir)
		}
	}
}

// beginWork takes the one operation slot and returns the function that gives it back. The name it records
// is what the status answer reports, so a second request is refused with a reason rather than a hang.
func (s *Service) beginWork(doing string) (func(), error) {
	s.workMu.Lock()
	if s.working != "" {
		running := s.working
		s.workMu.Unlock()
		return nil, refusal("an update %s is already running; wait for it to finish", running)
	}
	s.working = doing
	s.workMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.workMu.Lock()
			s.working = ""
			s.workMu.Unlock()
		})
	}, nil
}

// ErrNothingStaged says no release is waiting for a restart.
var ErrNothingStaged = errs.Conflict("no release is staged; download one first")

// Restart asks for the process restart that applies a staged release. It answers before the process ends:
// the shutdown that follows is the one a signal drives.
func (s *Service) Restart() error {
	if err := s.forbidden(); err != nil {
		return err
	}
	s.mu.Lock()
	staged, backup := s.state.StagedVersion, s.state.BackupPath
	s.mu.Unlock()
	if staged == "" {
		return ErrNothingStaged
	}
	if s.restart == nil {
		return refusal("this process has no restart path; restart it the way it was started")
	}
	s.workMu.Lock()
	working := s.working
	s.workMu.Unlock()
	if working != "" {
		return refusal("an update %s is running; wait for it to finish", working)
	}
	logging.For(logging.Updates).Infof("restarting to apply release %s; the previous files are in %s", staged, backup)
	return s.restart()
}

// forbidden is the organization switch speaking. It is checked before anything that could reach the
// network or the install directory, so a release the last process learned about cannot be installed by a
// deployment that has since turned updates off.
func (s *Service) forbidden() error {
	if s.disabled {
		return refusal("updates are disabled by %s, so this server will not check for, download or replace anything", EnvDisableUpdates)
	}
	return nil
}

// Status answers from the last known state. It never touches the network.
func (s *Service) Status(set Settings) Status {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
	s.workMu.Lock()
	working := s.working
	s.workMu.Unlock()

	return Status{
		State: st,
		// Disabled wins here rather than at every caller: "the switch forbids it" and "this deployment
		// said yes" cannot both be true of one process.
		Enabled:     set.Enabled && !s.disabled,
		Disabled:    s.disabled,
		Answered:    set.Answered,
		Install:     s.install,
		ManifestURL: s.manifestURL,
		Working:     working,
	}
}

// CheckAutomatic is the daily pass on a browser's behalf. The day belongs to the install, not to whichever
// browser asked first: the first one today gets the fetch, and every later one is answered from what that
// fetch learned. A check that ran and failed spent the day like one that succeeded, because `record`
// timestamps every check that runs.
func (s *Service) CheckAutomatic(ctx context.Context, set Settings) (Status, error) {
	// The refusal is answered before the day is consulted, and no day is spent on a check that will not
	// run: a person's later yes gets its first answer on the next boot rather than tomorrow.
	if err := s.checkRefusal(set); err != nil {
		return s.Status(set), err
	}
	if s.checkedToday() {
		return s.Status(set), nil
	}
	return s.Check(ctx, set)
}

// checkRefusal is why a check may not dial the manifest, or nil. Both entry points ask it before dialling,
// so the daily pass cannot answer a spent day over the sentence that names the switch which forbids it.
func (s *Service) checkRefusal(set Settings) error {
	if err := s.forbidden(); err != nil {
		return err
	}
	if !set.Enabled {
		return errDisabled()
	}
	return nil
}

// checkedToday reports whether a check has run since the UTC day turned. A stored instant that will not
// parse reads as unspent: an unknown is a reason to ask, not a reason to stay quiet.
func (s *Service) checkedToday() bool {
	s.mu.Lock()
	raw := s.state.CheckedAt
	s.mu.Unlock()
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	const day = "2006-01-02"
	return at.UTC().Format(day) == s.now().UTC().Format(day)
}

// Check fetches the manifest and records what it says. A refusal is returned as (status, err): a check
// that failed must still tell a person what the last good answer was.
func (s *Service) Check(ctx context.Context, set Settings) (Status, error) {
	if err := s.checkRefusal(set); err != nil {
		return s.Status(set), err
	}
	body, err := s.fetcher.Get(ctx, s.manifestURL)
	if err != nil {
		s.recordFailure(err)
		return s.Status(set), fault("check for updates", err)
	}
	manifest, err := ParseManifest(body, s.manifestURL)
	if err != nil {
		s.recordFailure(err)
		return s.Status(set), fault("read the release manifest", err)
	}

	asset, hasAsset := manifest.AssetFor(s.goos, s.goarch)
	newer, err := CompareVersions(manifest.Latest, s.version)
	if err != nil {
		// The manifest was checked when it was parsed, so this is the *local* version refusing to
		// compare: a development or image build. What it cannot say is a note, never "up to date".
		s.record(func(st *State) {
			st.Latest = manifest.Latest
			st.Published = manifest.Published
			st.NotesURL = manifest.NotesURL
			st.Asset = Asset{}
			st.Available = false
			st.LastError = ""
			st.Note = "this build reports its version as " + quoteOrEmpty(s.version) + ", so there is no release to compare it with"
		})
		return s.Status(set), nil
	}

	note := ""
	if _, has := manifest.AssetFor(s.goos, s.goarch); !has && newer > 0 {
		note = fmt.Sprintf("release %s names no archive for %s/%s", manifest.Latest, s.goos, s.goarch)
	}
	s.mu.Lock()
	s.state.Latest = manifest.Latest
	s.state.Published = manifest.Published
	s.state.NotesURL = manifest.NotesURL
	s.state.Available = newer > 0 && hasAsset
	if newer > 0 {
		s.state.Asset = asset
	} else {
		s.state.Asset = Asset{}
	}
	s.state.Note = note
	s.state.CheckedAt = s.now().UTC().Format(time.RFC3339)
	s.state.LastError = ""
	s.mu.Unlock()

	s.persist()
	return s.Status(set), nil
}

// record changes the state under the lock and writes it back, so the dot and the next page load agree.
func (s *Service) record(change func(*State)) {
	s.mu.Lock()
	s.state.Note = ""
	change(&s.state)
	s.state.CheckedAt = s.now().UTC().Format(time.RFC3339)
	s.mu.Unlock()
	s.persist()
}

// recordFailure keeps the last good answer and notes the failure beside it: a server that could not reach
// the manifest today is still running an older release than the one it saw yesterday.
func (s *Service) recordFailure(cause error) {
	logging.For(logging.Updates).Warnf("update check failed: %v", cause)
	s.record(func(st *State) { st.LastError = cause.Error() })
}

func (s *Service) persist() {
	if s.store == nil {
		return
	}
	s.mu.Lock()
	raw, err := json.Marshal(s.state)
	s.mu.Unlock()
	if err != nil {
		logging.For(logging.Updates).Warnf("update result not saved: %v", err)
		return
	}
	if err := s.store.SetConfigValue(stateKey, string(raw)); err != nil {
		logging.For(logging.Updates).Warnf("update result not saved: %v", err)
	}
}

func (s *Service) load() (string, error) {
	if s.store == nil {
		return "", nil
	}
	return s.store.GetConfigValue(stateKey)
}

// refusal is the state a person can change; the sentence reaches the browser unchanged.
func refusal(format string, args ...any) error { return errs.Conflict(fmt.Sprintf(format, args...)) }

// fault is MIKE's own problem: the cause goes to the log, the browser gets the operation's name.
func fault(operation string, cause error) error { return errs.Operation(operation, cause) }

// errDisabled says the preference has not been turned on, which is the state a person can change here.
func errDisabled() error {
	return refusal("update checks are off; turn them on in Settings → About")
}

// checkManifestURL is the rule startup applies to the variable, applied here to the value: a caller that
// built a URL from a fixture meets the same bar as the operator who typed one.
func checkManifestURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("update manifest URL %q is not a URL", raw)
	}
	if !httputil.FetchSchemeAllowed(u) {
		return fmt.Errorf("update manifest URL %q must be https (http is accepted only on loopback)", raw)
	}
	_, err = manifestHost(raw)
	return err
}
