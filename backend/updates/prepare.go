package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mike/pkg/logging"
)

// A prepared release is the download half of an update: fetched, hashed and unpacked once to prove it is
// a release this install could take, then left in its own directory until a person clicks Install. Nothing
// outside this directory is written, which is what makes doing it without asking safe.
const (
	// preparedDirName lives inside the install directory, so the swap the install click performs is a
	// rename on one filesystem.
	preparedDirName = ".update-prepared"
	// preparedArchive is the verified archive; preparedRecord names its bytes; partSuffix marks an
	// archive still being written, which nothing reads.
	preparedArchive = "release.tar.gz"
	preparedName    = "prepared.json"
	partSuffix      = ".part"
)

// PreparedInfo describes a downloaded release.
type PreparedInfo struct {
	Version string `json:"version"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
	// Files is the number of files that would be replaced by installing.
	Files int `json:"files"`
	// Notes contains non-fatal warnings about the release.
	Notes []string `json:"notes,omitempty"`
}

// preparedRecord is what .update-prepared/prepared.json holds: enough to tell an archive on disk from a
// stranger's file when the process that wrote it is long gone.
type preparedRecord struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	Files   int    `json:"files"`
	At      string `json:"at"`
}

func (s *Service) preparedDir() string {
	return filepath.Join(s.install.Dir, preparedDirName)
}

func (s *Service) preparedArchivePath() string {
	return filepath.Join(s.preparedDir(), preparedArchive)
}

// prepared is the downloaded release this process believes it has.
func (s *Service) prepared() PreparedInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return PreparedInfo{
		Version: s.state.PreparedVersion,
		Bytes:   s.state.PreparedBytes,
		SHA256:  s.state.PreparedSHA256,
		Files:   s.state.PreparedFiles,
	}
}

// want is the release the last check found, compared again here because the check and the download are
// separate moments.
func (s *Service) want() (Asset, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.StagedVersion != "" {
		return Asset{}, "", refusal("release %s is already staged; restart to apply it", s.state.StagedVersion)
	}
	if !s.state.Available || s.state.Asset.URL == "" {
		return Asset{}, "", refusal("no newer release is known; check for updates first")
	}
	// The version is compared again rather than trusted: a check from last week must not talk this
	// server into a downgrade.
	newer, err := CompareVersions(s.state.Latest, s.version)
	if err != nil {
		return Asset{}, "", fault("compare this build with release "+s.state.Latest, err)
	}
	if newer <= 0 {
		return Asset{}, "", refusal("release %s is not newer than the running %s", s.state.Latest, s.version)
	}
	return s.state.Asset, s.state.Latest, nil
}

// Prepare downloads and verifies the newest release and stops: the archive waits in the install
// directory, and the files the server runs are untouched until the install click.
func (s *Service) Prepare(ctx context.Context) (PreparedInfo, error) {
	if err := s.forbidden(); err != nil {
		return PreparedInfo{}, err
	}
	release, err := s.beginWork("prepare")
	if err != nil {
		return PreparedInfo{}, err
	}
	defer release()

	asset, version, err := s.want()
	if err != nil {
		return PreparedInfo{}, err
	}
	// One downloaded release at a time, and never a second over the first: a person may be reading the
	// prompt for the one already waiting.
	if p := s.prepared(); p.Version != "" {
		if p.Version == version {
			return p, refusal("release %s is already downloaded; install it or restart the server to forget it", version)
		}
		return p, refusal("release %s is downloaded and waiting to install; install it before %s is fetched", p.Version, version)
	}

	dir := s.preparedDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return PreparedInfo{}, fmt.Errorf("cannot prepare a directory beside the install: %w", err)
	}
	part := filepath.Join(dir, preparedArchive+partSuffix)
	// The archive is written under a name nothing reads and renamed only once it has verified, so an
	// interrupted download cannot be mistaken for a prepared release.
	os.Remove(part)

	size, digest, err := s.fetcher.Download(ctx, asset.URL, part, MaxPackageBytes)
	if err != nil {
		os.Remove(part)
		return PreparedInfo{}, fault("download the release", err)
	}
	if !strings.EqualFold(digest, asset.SHA256) {
		os.Remove(part)
		return PreparedInfo{}, fault("verify the release", fmt.Errorf("%s is not the archive the manifest described: sha256 %s, expected %s", asset.URL, digest, asset.SHA256))
	}

	// Unpack it once, into a directory that is then thrown away: an archive that would be refused at
	// install time is refused now, before anyone is promised they can install it.
	files, notes, err := s.verifyArchive(part, version)
	if err != nil {
		os.Remove(part)
		return PreparedInfo{}, err
	}
	if err := os.Rename(part, s.preparedArchivePath()); err != nil {
		return PreparedInfo{}, fmt.Errorf("cannot keep the downloaded release: %w", err)
	}

	at := s.now().UTC().Format(time.RFC3339)
	record := preparedRecord{Version: version, SHA256: digest, Bytes: size, Files: files, At: at}
	if raw, err := json.Marshal(record); err != nil {
		os.RemoveAll(dir)
		return PreparedInfo{}, err
	} else if err := os.WriteFile(filepath.Join(dir, preparedName), raw, 0o600); err != nil {
		os.RemoveAll(dir)
		return PreparedInfo{}, fmt.Errorf("cannot record the downloaded release: %w", err)
	}

	s.mu.Lock()
	s.state.PreparedVersion = version
	s.state.PreparedSHA256 = digest
	s.state.PreparedBytes = size
	s.state.PreparedFiles = files
	s.state.PreparedAt = at
	s.mu.Unlock()
	s.persist()

	info := PreparedInfo{Version: version, Bytes: size, SHA256: digest, Files: files, Notes: notes}
	logging.For(logging.Updates).Infof("release %s downloaded and verified into %s: %d bytes, %d file(s) would be replaced",
		version, dir, size, files)
	return info, nil
}

// verifyArchive unpacks the downloaded archive into a throwaway directory, reports what installing it
// would replace, and throws the tree away, identifying potential failures before the installation is promised.
func (s *Service) verifyArchive(archive, version string) (int, []string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()

	verify, err := os.MkdirTemp(s.preparedDir(), "verify-")
	if err != nil {
		return 0, nil, err
	}
	defer os.RemoveAll(verify)

	release, err := unpack(f, filepath.Join(verify, "u"), version, s.goos, s.goarch)
	if err != nil {
		return 0, nil, err
	}
	files, _, notes, err := s.swapPlan(release)
	if err != nil {
		return 0, nil, fault("plan the swap", err)
	}
	return len(files), notes, nil
}

// verifyPrepared re-hashes the archive on disk against the digest recorded when it was fetched. It proves
// the bytes are still the bytes that were verified; it does not ask the network again.
func (s *Service) verifyPrepared(p PreparedInfo) error {
	f, err := os.Open(s.preparedArchivePath())
	if err != nil {
		return fmt.Errorf("the downloaded release is not on disk any more: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(f, MaxPackageBytes+1))
	if err != nil {
		return err
	}
	if n > MaxPackageBytes || n != p.Bytes {
		return fmt.Errorf("the downloaded release is %d bytes, not the %d that were verified", n, p.Bytes)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); !strings.EqualFold(got, p.SHA256) {
		return fmt.Errorf("its sha256 is %s, not the %s that was verified", got, p.SHA256)
	}
	return nil
}

// clearPrepared forgets a downloaded release and removes it from disk.
func (s *Service) clearPrepared() {
	os.RemoveAll(s.preparedDir())
	s.mu.Lock()
	cleared := s.state.PreparedVersion != ""
	s.state.PreparedVersion = ""
	s.state.PreparedSHA256 = ""
	s.state.PreparedBytes = 0
	s.state.PreparedFiles = 0
	s.state.PreparedAt = ""
	s.mu.Unlock()
	if cleared {
		s.persist()
	}
}

// reconcilePrepared decides at boot whether an archive left in the prepared directory is the one the
// stored answer names. A prepared release outlives the process that downloaded it; a half-written one,
// or one the answer does not name, is garbage.
func (s *Service) reconcilePrepared() {
	dir := s.preparedDir()
	raw, err := os.ReadFile(filepath.Join(dir, preparedName))
	if err != nil {
		os.RemoveAll(dir)
		return
	}
	var record preparedRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		logging.For(logging.Updates).Warnf("prepared release in %s not understood, removing it: %v", dir, err)
		os.RemoveAll(dir)
		s.clearPrepared()
		return
	}

	s.mu.Lock()
	named := s.state.PreparedVersion
	s.mu.Unlock()
	if named == "" || named != record.Version {
		os.RemoveAll(dir)
		s.clearPrepared()
		return
	}
	if _, err := os.Stat(s.preparedArchivePath()); err != nil {
		logging.For(logging.Updates).Warnf("release %s was downloaded but its archive is gone", record.Version)
		s.clearPrepared()
	}
}
