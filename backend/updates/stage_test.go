package updates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/errs"
)

// releaseArchive builds the tarball scripts/package-release.sh builds: one top-level directory named
// after the release, holding the files it ships.
func releaseArchive(t *testing.T, version, goos, goarch string, files map[string]string) []byte {
	t.Helper()
	top := fmt.Sprintf("mike-server-%s-%s-%s", version, goos, goarch)
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		header := &tar.Header{Name: top + "/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "mike-server") || strings.HasSuffix(name, "sandbox-bridge") {
			header.Mode = 0o755
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return raw.Bytes()
}

func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hostileArchive writes a tarball from raw header/value pairs, which is the only way to make the
// members a well-behaved writer refuses to write.
func hostileArchive(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		// Only a regular member carries bytes: a tar writer refuses to write a body for a link or a
		// device, which is the same reason this package refuses to unpack one.
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len("x"))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatalf("tar body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return raw.Bytes()
}

// stageFixture is an install directory as an operator unpacked it, plus a service pointed at it. The
// executable path is a fixture file: a test must never be able to replace the binary it runs from.
type stageFixture struct {
	dir      string
	exe      string
	bridge   string
	service  *Service
	fetcher  *fakeFetcher
	archive  []byte
	manifest []byte
}

func stageFixtureFor(t *testing.T, version string, archive []byte) *stageFixture {
	t.Helper()
	dir := releaseDir(t)
	exe := filepath.Join(dir, "mike-server")
	bridge := filepath.Join(dir, "sandbox", "sandbox-bridge")
	write(t, exe, "OLD-BINARY", 0o755)
	write(t, bridge, "OLD-BRIDGE", 0o755)
	write(t, filepath.Join(dir, "prompts", "old.tmpl"), "REMOVED-IN-NEW-RELEASE", 0o644)
	write(t, filepath.Join(dir, "prompts", "system.tmpl"), "OLD-PROMPT", 0o644)

	fetcher := &fakeFetcher{dlBody: archive}
	if archive != nil {
		fetcher.body = manifestDoc(t, map[string]any{
			"latest": version,
			"assets": map[string]any{
				"linux/amd64": map[string]any{
					"url":    "https://getmike.dev/download/mike-server-" + version + "-linux-amd64.tar.gz",
					"sha256": digestHex(archive),
				},
			},
		})
	}
	svc, err := New(Options{
		Version:          "1.4.0",
		Store:            &fakeStore{},
		Fetcher:          fetcher,
		InstallDir:       dir,
		Executable:       exe,
		ContainerMarkers: []string{},
		Now:              fixedClock(),
		Bridge:           func() (string, error) { return bridge, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := &stageFixture{dir: dir, exe: exe, bridge: bridge, service: svc, fetcher: fetcher, archive: archive, manifest: fetcher.body}
	if svc.install.Kind != KindPackaged {
		t.Fatalf("the fixture is not a packaged install: %+v", svc.install)
	}
	return f
}

// theService runs the staging steps with a release that was built for linux/amd64, which is the arch the
// fixture's manifest names.
func (f *stageFixture) check(t *testing.T) {
	t.Helper()
	svc, err := New(Options{
		Version:          "1.4.0",
		Store:            f.service.store,
		Fetcher:          f.fetcher,
		InstallDir:       f.dir,
		Executable:       f.exe,
		ContainerMarkers: []string{},
		Now:              fixedClock(),
		Bridge:           func() (string, error) { return f.bridge, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.service = svc
	if _, err := svc.Check(context.Background(), Settings{Enabled: true}); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// prepare runs the automatic half: download the newest release and verify it, changing nothing.
func (f *stageFixture) prepare(t *testing.T) (PreparedInfo, error) {
	t.Helper()
	return f.service.Prepare(context.Background())
}

// install runs both halves, which is what the one click does: a prepared release, then the swap.
func (f *stageFixture) install(t *testing.T) (ApplyResult, error) {
	t.Helper()
	if _, err := f.prepare(t); err != nil {
		return ApplyResult{}, err
	}
	return f.service.Apply(context.Background())
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func goodArchive(t *testing.T) []byte {
	t.Helper()
	return releaseArchive(t, "1.4.2", "linux", "amd64", map[string]string{
		"mike-server":               "NEW-BINARY",
		"prompts/system.tmpl":       "NEW-PROMPT",
		"sandbox/sandbox-bridge":    "NEW-BRIDGE",
		"sandbox/kanban.Dockerfile": "FROM scratch",
		"README.md":                 "release notes",
	})
}

// Installing moves every file the release ships onto the path *this* server uses and keeps what it
// replaced; the running process is untouched until it is restarted.
func TestInstallPutsTheReleaseInPlace(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)

	prepared, err := f.prepare(t)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.Version != "1.4.2" || prepared.Files != 4 || prepared.SHA256 != digestHex(f.archive) {
		t.Errorf("prepared = %+v, want 1.4.2 over four files at the archive's digest", prepared)
	}
	// The automatic half changes nothing the server runs; only the archive and its record appear.
	if got := read(t, f.exe); got != "OLD-BINARY" {
		t.Errorf("a download replaced the binary with %q", got)
	}
	if st := f.service.Status(Settings{}); st.State.PreparedVersion != "1.4.2" || st.State.StagedVersion != "" {
		t.Errorf("status after a download = %+v", st.State)
	}

	res, err := f.service.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := read(t, f.exe); got != "NEW-BINARY" {
		t.Errorf("the running binary's path holds %q", got)
	}
	if got := read(t, f.bridge); got != "NEW-BRIDGE" {
		t.Errorf("the bridge the server resolves holds %q", got)
	}
	if got := read(t, filepath.Join(f.dir, "prompts", "system.tmpl")); got != "NEW-PROMPT" {
		t.Errorf("the prompt template holds %q", got)
	}
	if got := read(t, filepath.Join(f.dir, "sandbox", "kanban.Dockerfile")); got != "FROM scratch" {
		t.Errorf("the Dockerfile holds %q", got)
	}

	// The binary stays executable: a release that lands mode 0644 will not start.
	info, err := os.Stat(f.exe)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the replaced binary is not executable: %v", info.Mode())
	}

	// Everything replaced is kept, one generation back.
	backup := filepath.Join(f.dir, backupDirName)
	if got := read(t, filepath.Join(backup, "mike-server")); got != "OLD-BINARY" {
		t.Errorf("backup of the binary = %q", got)
	}
	if got := read(t, filepath.Join(backup, "sandbox", "sandbox-bridge")); got != "OLD-BRIDGE" {
		t.Errorf("backup of the bridge = %q", got)
	}
	if got := read(t, filepath.Join(backup, "prompts", "system.tmpl")); got != "OLD-PROMPT" {
		t.Errorf("backup of the prompt = %q", got)
	}

	// A template this release stopped shipping is moved into the backup, not deleted.
	if _, err := os.Stat(filepath.Join(f.dir, "prompts", "old.tmpl")); !os.IsNotExist(err) {
		t.Errorf("a template the release does not ship is still in place")
	}
	if got := read(t, filepath.Join(backup, "prompts", "old.tmpl")); got != "REMOVED-IN-NEW-RELEASE" {
		t.Errorf("the dropped template was not kept: %q", got)
	}

	if res.Version != "1.4.2" || res.BackupPath != backup || len(res.Replaced) != 4 {
		t.Errorf("result = %+v, want 1.4.2 with the backup path and four replaced files", res)
	}
	if res.SHA256 != digestHex(f.archive) {
		t.Errorf("result digest = %q, want the archive's own digest", res.SHA256)
	}
	// The bytes came from the archive the manifest named, under the package-sized cap rather than the
	// small one every other outbound read uses.
	if want := "https://getmike.dev/download/mike-server-1.4.2-linux-amd64.tar.gz"; f.fetcher.lastDownload != want {
		t.Errorf("downloaded %q, want %q", f.fetcher.lastDownload, want)
	}
	if f.fetcher.lastLimit != MaxPackageBytes {
		t.Errorf("download limit = %d, want the package bound %d", f.fetcher.lastLimit, MaxPackageBytes)
	}

	// The installed release is what the status answer carries, because that is what the restart reads.
	st := f.service.Status(Settings{})
	if st.State.StagedVersion != "1.4.2" || st.State.BackupPath != backup {
		t.Errorf("status after installing = %+v", st.State)
	}
	// The downloaded release is spent: it is gone once its files are in place.
	if st.State.PreparedVersion != "" {
		t.Errorf("status still carries a downloaded release: %+v", st.State)
	}
	if _, err := os.Stat(filepath.Join(f.dir, preparedDirName)); !os.IsNotExist(err) {
		t.Errorf("the prepared directory survived its install")
	}

	// No staging directory is left behind.
	leftovers, _ := filepath.Glob(filepath.Join(f.dir, stagingPrefix+"*"))
	if len(leftovers) != 0 {
		t.Errorf("staging directories left behind: %v", leftovers)
	}
}

// One downloaded release at a time: a second download is refused while a person may be reading the
// prompt for the one already waiting.
func TestPrepareRefusesOverADownloadedRelease(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.prepare(t); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	downloads := f.fetcher.dlCalls

	_, err := f.service.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1.4.2 is already downloaded") {
		t.Fatalf("second Prepare = %v, want a refusal naming the downloaded release", err)
	}
	if f.fetcher.dlCalls != downloads {
		t.Errorf("a refused download fetched %d more archives", f.fetcher.dlCalls-downloads)
	}
}

// An install is one click, not two: the second has nothing left to swap and says so.
func TestApplyRefusesWithNothingDownloaded(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.install(t); err != nil {
		t.Fatalf("install: %v", err)
	}
	_, err := f.service.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nothing is downloaded to install") {
		t.Fatalf("second Apply = %v, want the refusal that says to check first", err)
	}
}

// The archive on disk is re-hashed before anything is renamed: the download was verified once, and the
// directory it waits in is an ordinary one since.
func TestApplyRefusesADownloadedReleaseItCanNoLongerTrust(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.prepare(t); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// Something edited the waiting archive — a restored file, a stray cleanup, a hand.
	write(t, filepath.Join(f.dir, preparedDirName, preparedArchive), "TAMPERED", 0o600)

	_, err := f.service.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no longer be trusted") {
		t.Fatalf("Apply = %v, want a refusal naming the digest check", err)
	}
	if got := read(t, f.exe); got != "OLD-BINARY" {
		t.Errorf("a distrusted release was installed anyway: binary = %q", got)
	}
	if st := f.service.Status(Settings{}); st.State.PreparedVersion != "" {
		t.Errorf("the distrusted release is still offered: %+v", st.State)
	}
	if _, err := os.Stat(filepath.Join(f.dir, preparedDirName)); !os.IsNotExist(err) {
		t.Errorf("the distrusted archive is still on disk")
	}
}

// The digest is the whole trust story this round: a mismatch means nothing on disk changed, and the
// reason says so in terms a person can act on.
func TestPrepareRefusesAnArchiveThatIsNotWhatTheManifestPromised(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	// The manifest promised one archive; the server is handed a different one with the same name.
	f.fetcher.dlBody = releaseArchive(t, "1.4.2", "linux", "amd64", map[string]string{
		"mike-server":         "SOMETHING-ELSE",
		"prompts/system.tmpl": "NEW-PROMPT",
	})

	_, err := f.prepare(t)
	// The browser gets "verify the release failed"; the digest pair it cannot use goes to the log,
	// which is what errs.Operation is for.
	if err == nil || !errs.IsOperation(err) {
		t.Fatalf("Prepare = %v, want a server fault", err)
	}
	if cause := errs.Cause(err); cause == nil || !strings.Contains(cause.Error(), "not the archive the manifest described") {
		t.Errorf("cause = %v, want the digest mismatch the operator needs", cause)
	}
	if got := read(t, f.exe); got != "OLD-BINARY" {
		t.Errorf("a rejected download changed the binary to %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, backupDirName)); !os.IsNotExist(err) {
		t.Errorf("a rejected download created a backup directory")
	}
	// Nothing is left waiting: a half-written archive is not a downloaded release.
	if st := f.service.Status(Settings{}); st.State.PreparedVersion != "" || st.State.StagedVersion != "" {
		t.Errorf("a rejected download was kept as a release to install: %+v", st.State)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(f.dir, preparedDirName, "*"+partSuffix)); len(leftovers) != 0 {
		t.Errorf("a rejected download left %v behind", leftovers)
	}
}

// A container is refused before a byte is fetched: the swap would be lost on the next start of the
// image, which is the worst possible outcome of a half-applied update.
func TestTheUpdateHalvesRefuseAnInstallThatCannotTakeThem(t *testing.T) {
	t.Parallel()
	dir := releaseDir(t)
	exe := filepath.Join(dir, "mike-server")
	write(t, exe, "OLD-BINARY", 0o755)
	// The marker is the test's own, so the refusal is decided here rather than by whether the machine
	// running the suite happens to be a container.
	marker := filepath.Join(t.TempDir(), ".containerenv")
	write(t, marker, "MARKER", 0o644)
	archive := goodArchive(t)
	fetcher := &fakeFetcher{dlBody: archive, body: manifestDoc(t, map[string]any{"latest": "1.4.2"})}

	svc, err := New(Options{
		Version:          "1.4.2",
		Store:            &fakeStore{},
		Fetcher:          fetcher,
		InstallDir:       dir,
		Executable:       exe,
		ContainerMarkers: []string{marker},
		Now:              fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := svc.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("Prepare = %v, want the container refusal", err)
	}
	if _, err := svc.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("Apply = %v, want the container refusal", err)
	}
	if fetcher.dlCalls != 0 {
		t.Errorf("a refused install downloaded %d archives", fetcher.dlCalls)
	}
}

// The check and the download are separate steps, so the download re-asks the question the check
// answered: whatever the cache claims, an old row must not talk this server into a downgrade.
func TestPrepareRefusesADowngradeFromAStaleRow(t *testing.T) {
	t.Parallel()
	stale := State{
		CurrentVersion: "1.4.0",
		Latest:         "1.0.0",
		Available:      true,
		Asset:          Asset{URL: "https://getmike.dev/download/mike-server-1.0.0-linux-amd64.tar.gz", SHA256: strings.Repeat("a", 64)},
	}

	dir := releaseDir(t)
	exe := filepath.Join(dir, "mike-server")
	write(t, exe, "OLD-BINARY", 0o755)
	svc, err := New(Options{
		Version: "1.4.0", Store: &fakeStore{}, Fetcher: &fakeFetcher{dlBody: goodArchive(t)},
		InstallDir: dir, Executable: exe, ContainerMarkers: []string{}, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Written straight into the state because a load would clear it (see
	// TestAStoredAnswerAboutTheRunningReleaseStopsOfferingIt); the refusal below must hold whatever a
	// row says.
	svc.state = stale
	if _, err := svc.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("Prepare = %v, want a refusal to downgrade", err)
	}
}

// Nothing has been checked yet: the answer is "check first", and the network stays quiet.
func TestPrepareRefusesWithoutAKnownRelease(t *testing.T) {
	t.Parallel()
	dir := releaseDir(t)
	exe := filepath.Join(dir, "mike-server")
	write(t, exe, "OLD-BINARY", 0o755)
	fetcher := &fakeFetcher{}
	svc, err := New(Options{Version: "1.4.0", Store: &fakeStore{}, Fetcher: fetcher, InstallDir: dir, Executable: exe, ContainerMarkers: []string{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "check for updates first") {
		t.Fatalf("Prepare = %v, want a refusal that says to check first", err)
	}
	if fetcher.dlCalls != 0 {
		t.Errorf("a download with nothing known fetched %d archives", fetcher.dlCalls)
	}
}

// A bridge named outside the install directory is a file this update does not own. The note is the
// point: silence here would leave a new server talking to an old bridge.
func TestAnInstallRefusesToReplaceABridgeOutsideTheInstall(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	outside := filepath.Join(t.TempDir(), "sandbox-bridge")
	write(t, outside, "OUTSIDE-BRIDGE", 0o755)
	f.service.bridge = func() (string, error) { return outside, nil }

	res, err := f.install(t)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := read(t, outside); got != "OUTSIDE-BRIDGE" {
		t.Errorf("a bridge outside the install was replaced")
	}
	var noted bool
	for _, n := range res.Notes {
		if strings.Contains(n, "outside this install") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the untouched bridge was not reported: %v", res.Notes)
	}
}

// The archive is our own file, so anything in it that is not one of ours is treated as an attack.
func TestUnpackRefusals(t *testing.T) {
	t.Parallel()
	top := "mike-server-1.4.2-linux-amd64"
	cases := []struct {
		name    string
		entries []*tar.Header
		want    string
	}{
		{
			name:    "an absolute member",
			entries: []*tar.Header{{Name: "/etc/passwd", Typeflag: tar.TypeReg}},
			want:    "absolute path",
		},
		{
			name:    "a member that escapes",
			entries: []*tar.Header{{Name: top + "/../../escaped", Typeflag: tar.TypeReg}},
			want:    "escapes the staging directory",
		},
		{
			name:    "a symlink",
			entries: []*tar.Header{{Name: top + "/mike-server", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}},
			want:    "symlink",
		},
		{
			name:    "a hard link",
			entries: []*tar.Header{{Name: top + "/mike-server", Typeflag: tar.TypeLink, Linkname: top + "/prompts/system.tmpl"}},
			want:    "hard link",
		},
		{
			name:    "a device",
			entries: []*tar.Header{{Name: top + "/mike-server", Typeflag: tar.TypeChar}},
			want:    "device",
		},
		{
			name: "two top-level directories",
			entries: []*tar.Header{
				{Name: top + "/mike-server", Typeflag: tar.TypeReg},
				{Name: "mike-server-1.0.0-linux-amd64/mike-server", Typeflag: tar.TypeReg},
			},
			want: "more than one top-level directory",
		},
		{
			name:    "a directory named for another release",
			entries: []*tar.Header{{Name: "mike-server-9.9.9-linux-amd64/mike-server", Typeflag: tar.TypeReg}},
			want:    "expected the release directory",
		},
		{
			name: "an empty archive",
			want: "holds nothing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			archive := hostileArchive(t, tc.entries...)
			_, err := unpack(bytes.NewReader(archive), filepath.Join(t.TempDir(), "unpacked"), "1.4.2", "linux", "amd64")
			if err == nil {
				t.Fatalf("unpack accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}

	t.Run("an archive with more files than a release has", func(t *testing.T) {
		t.Parallel()
		var entries []*tar.Header
		for i := 0; i <= maxArchiveMembers; i++ {
			entries = append(entries, &tar.Header{Name: fmt.Sprintf("%s/prompts/%04d.tmpl", top, i), Typeflag: tar.TypeReg})
		}
		_, err := unpack(bytes.NewReader(hostileArchive(t, entries...)), filepath.Join(t.TempDir(), "unpacked"), "1.4.2", "linux", "amd64")
		if err == nil || !strings.Contains(err.Error(), "more than") {
			t.Fatalf("unpack accepted %d members: %v", len(entries), err)
		}
	})

	t.Run("a good archive lands mode-for-mode", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "unpacked")
		release, err := unpack(bytes.NewReader(goodArchive(t)), dir, "1.4.2", "linux", "amd64")
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		if got := read(t, filepath.Join(release, "mike-server")); got != "NEW-BINARY" {
			t.Errorf("unpacked binary = %q", got)
		}
	})
}

func TestWithin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		root, target string
		want         bool
	}{
		{root: "/mike", target: "/mike/prompts/a.tmpl", want: true},
		{root: "/mike", target: "/mike", want: true},
		{root: "/mike", target: "/mike/../etc/passwd", want: false},
		{root: "/mike", target: "/mike2/a", want: false},
		{root: "/mike", target: "/etc/passwd", want: false},
	}
	for _, tc := range cases {
		if got := within(tc.root, tc.target); got != tc.want {
			t.Errorf("within(%q,%q) = %v, want %v", tc.root, tc.target, got, tc.want)
		}
	}
}
