package updates

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"mike/backend/fsutil"
	"mike/pkg/logging"
)

// Installing a release moves three groups of files — the binary, the bridge, the templates — and stops
// there: the running process keeps the inode it was started from, so nothing about the current server
// changes until it is restarted, which the same click asks for.
const (
	// stagingPrefix names the throwaway directory an install unpacks into — inside the install directory,
	// so the swap is a rename on one filesystem.
	stagingPrefix = ".update-staging-"
	// backupDirName holds the previous release's files. One backup, the release before this one.
	backupDirName = ".update-backup"
	// maxArchiveMembers bounds what a tarball may contain; a release directory holds a few dozen files.
	maxArchiveMembers = 256
)

// BridgePath resolves the sandbox-bridge binary this server actually runs, so a staged update replaces
// that file rather than the one the archive happens to be named after.
type BridgePath func() (string, error)

// ApplyResult is what a staged release is, for the answer that follows.
type ApplyResult struct {
	Version    string `json:"version"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	BackupPath string `json:"backup_path"`
	// Replaced names every file that moved into place.
	Replaced []string `json:"replaced"`
	// Notes carries what is not a failure: a bridge outside the install directory, a dropped template.
	Notes []string `json:"notes,omitempty"`
}

// Apply puts a prepared release in place: re-verify the archive, unpack it, keep what it replaces, rename
// the new files over the old. It never restarts anything and never downloads anything.
func (s *Service) Apply(ctx context.Context) (ApplyResult, error) {
	if err := s.forbidden(); err != nil {
		return ApplyResult{}, err
	}
	prepared := s.prepared()
	if prepared.Version == "" {
		return ApplyResult{}, refusal("nothing is downloaded to install; check for updates first")
	}
	release, err := s.beginWork("apply")
	if err != nil {
		return ApplyResult{}, err
	}
	defer release()

	// The archive was verified when it was downloaded; it is verified again here because the two are
	// separate moments and the directory is an ordinary one.
	if err := s.verifyPrepared(prepared); err != nil {
		s.clearPrepared()
		return ApplyResult{}, refusal("the downloaded release %s can no longer be trusted (%s); check for updates to fetch it again",
			prepared.Version, err)
	}

	work, err := os.MkdirTemp(s.install.Dir, stagingPrefix)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("cannot unpack the release beside the install directory: %w", err)
	}
	defer os.RemoveAll(work)

	f, err := os.Open(s.preparedArchivePath())
	if err != nil {
		return ApplyResult{}, err
	}
	defer f.Close()
	unpacked, err := unpack(f, filepath.Join(work, "unpacked"), prepared.Version, s.goos, s.goarch)
	if err != nil {
		return ApplyResult{}, err
	}

	files, stale, notes, err := s.swapPlan(unpacked)
	if err != nil {
		return ApplyResult{}, fault("plan the swap", err)
	}
	backupPath, err := s.backup(ctx, files, stale)
	if err != nil {
		return ApplyResult{}, fault("keep the previous release", err)
	}
	replaced, err := swapFiles(ctx, work, files)
	if err != nil {
		return ApplyResult{}, fault("replace the install files", fmt.Errorf("%w (the previous files are in %s)", err, backupPath))
	}

	s.mu.Lock()
	s.state.StagedVersion = prepared.Version
	s.state.BackupPath = backupPath
	s.mu.Unlock()
	s.clearPrepared()

	res := ApplyResult{Version: prepared.Version, Bytes: prepared.Bytes, SHA256: prepared.SHA256,
		BackupPath: backupPath, Replaced: replaced, Notes: notes}
	logging.For(logging.Updates).Infof("release %s installed into %s: %d file(s) replaced, previous files kept in %s",
		prepared.Version, s.install.Dir, len(replaced), backupPath)
	return res, nil
}

// swapFile is one move: a file from the unpacked release onto the path it belongs to.
type swapFile struct {
	src  string
	dst  string
	mode os.FileMode
}

// swapPlan decides which file of the release lands where. The destinations are the paths *this* server
// uses — its own executable, the bridge path it resolves — not the names the archive happens to carry.
func (s *Service) swapPlan(release string) ([]swapFile, []string, []string, error) {
	var notes []string

	exe := s.exe
	if exe == "" {
		return nil, nil, nil, errors.New("cannot tell which binary is running")
	}

	server := filepath.Join(release, "mike-server")
	if _, err := os.Stat(server); err != nil {
		return nil, nil, nil, fmt.Errorf("the release carries no mike-server to replace %s with", exe)
	}
	files := []swapFile{{src: server, dst: exe, mode: 0o755}}

	if bridge, ok := s.bridgeTarget(); !ok {
		if bridge != "" {
			notes = append(notes, fmt.Sprintf("the sandbox bridge at %s is outside this install and was left alone", bridge))
		}
	} else {
		src := filepath.Join(release, bridgeRel)
		if _, err := os.Stat(src); err != nil {
			notes = append(notes, fmt.Sprintf("the release carries no archive bridge at %s, so %s was left alone", bridgeRel, bridge))
		} else {
			files = append(files, swapFile{src: src, dst: bridge, mode: 0o755})
		}
	}

	prompts, stale, err := templateSwap(release, s.install.Dir)
	if err != nil {
		return nil, nil, nil, err
	}
	files = append(files, prompts...)
	return files, stale, notes, nil
}

// bridgeRel is where a release keeps the bridge; the packager writes it under sandbox/.
const bridgeRel = "sandbox/sandbox-bridge"

// bridgeTarget is the bridge file this update may replace: the one the server resolves, and only when it
// lives inside the install directory. An install does not reach outside the directory it belongs to.
func (s *Service) bridgeTarget() (string, bool) {
	if s.bridge == nil {
		return "", false
	}
	got, err := s.bridge()
	if err != nil || got == "" {
		return "", false
	}
	abs, err := filepath.Abs(got)
	if err != nil {
		return "", false
	}
	if !within(s.install.Dir, abs) {
		return abs, false
	}
	return abs, true
}

// templateSwap pairs every template the release ships with the file beside the binary, and lists the ones
// this release no longer ships.
func templateSwap(release, installDir string) ([]swapFile, []string, error) {
	newOnes, err := filepath.Glob(filepath.Join(release, "prompts", "*.tmpl"))
	if err != nil || len(newOnes) == 0 {
		return nil, nil, fmt.Errorf("the release carries no prompt templates")
	}
	if _, err := os.Stat(filepath.Join(installDir, "prompts")); err != nil {
		return nil, nil, fmt.Errorf("this install has no prompts directory to update: %w", err)
	}
	out := make([]swapFile, 0, len(newOnes))
	var shipped []string
	for _, src := range newOnes {
		name := filepath.Base(src)
		out = append(out, swapFile{src: src, dst: filepath.Join(installDir, "prompts", name), mode: 0o644})
		shipped = append(shipped, name)
	}
	// A template the new release stopped shipping is moved aside, not deleted: the backup keeps it.
	var stale []string
	if all, err := filepath.Glob(filepath.Join(installDir, "prompts", "*.tmpl")); err == nil {
		for _, old := range all {
			if !slices.Contains(shipped, filepath.Base(old)) {
				stale = append(stale, old)
			}
		}
	}
	return out, stale, nil
}

// backup copies what is about to be replaced into the install directory, one generation deep.
func (s *Service) backup(ctx context.Context, files []swapFile, stale []string) (string, error) {
	dir := filepath.Join(s.install.Dir, backupDirName)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("cannot clear the previous backup: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, f := range files {
		rel, err := filepath.Rel(s.install.Dir, f.dst)
		if err != nil || !fsutil.RelInsideRoot(rel) {
			rel = filepath.Base(f.dst)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		if _, err := os.Stat(f.dst); errors.Is(err, os.ErrNotExist) {
			continue // a file this install never had; there is nothing to keep
		}
		if err := copyFile(ctx, f.dst, dst); err != nil {
			return "", fmt.Errorf("cannot keep %s: %w", f.dst, err)
		}
	}
	for _, old := range stale {
		rel, err := filepath.Rel(s.install.Dir, old)
		if err != nil || !fsutil.RelInsideRoot(rel) {
			continue
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		if err := os.Rename(old, dst); err != nil {
			return "", fmt.Errorf("cannot move aside %s: %w", old, err)
		}
	}
	return dir, nil
}

// swapFiles puts the release in place in two passes: every new file is copied under the staging directory
// first, then every rename happens, so a shutdown during the long pass has moved nothing. Renaming over a
// running executable is what Linux allows — the process keeps the inode it started from and the next exec
// sees the new file — which is why nothing here stops the server first. A failure part-way through is
// reported with the backup path.
func swapFiles(ctx context.Context, work string, files []swapFile) ([]string, error) {
	next := filepath.Join(work, "next")
	staged := make([]string, 0, len(files))
	for i, f := range files {
		path := filepath.Join(next, fmt.Sprintf("%02d-%s", i, filepath.Base(f.dst)))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := copyFile(ctx, f.src, path); err != nil {
			return nil, err
		}
		if err := os.Chmod(path, f.mode); err != nil {
			return nil, err
		}
		staged = append(staged, path)
	}

	var done []string
	for i, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.dst), 0o755); err != nil {
			return done, err
		}
		if err := os.Rename(staged[i], f.dst); err != nil {
			// Renames that already landed stay landed: unwinding them is a person's call against the backup.
			return done, fmt.Errorf("cannot replace %s: %w", f.dst, err)
		}
		done = append(done, f.dst)
	}
	return done, nil
}

// unpack expands the release archive and returns its top-level directory. Anything in the archive that
// does not look like ours is treated as an attack rather than a surprise: no absolute path, no `..`, no
// link, no device, no second top-level directory, and no member named after a version other than the one
// that was downloaded.
func unpack(src io.Reader, dest, version, goos, goarch string) (string, error) {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return "", fmt.Errorf("the archive is not gzip: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	top := ""
	count := 0
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("the archive could not be read: %w", err)
		}
		clean, err := safeMember(hdr)
		if err != nil {
			return "", err
		}
		first, _ := splitFirst(clean)
		if top == "" {
			top = first
		} else if first != top {
			return "", fmt.Errorf("the archive holds more than one top-level directory (%q and %q)", top, first)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		count++
		if count > maxArchiveMembers {
			return "", fmt.Errorf("the archive holds more than %d files", maxArchiveMembers)
		}
		total += hdr.Size
		if total > MaxPackageBytes {
			return "", fmt.Errorf("the archive expands past the %d byte limit", MaxPackageBytes)
		}
		dst := filepath.Join(dest, filepath.FromSlash(clean))
		if !within(dest, dst) {
			return "", fmt.Errorf("the archive names %q, which is outside the staging directory", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", err
		}
		// The bound runs per member too: an entry lying about its size still cannot write past it.
		n, err := io.Copy(out, io.LimitReader(tr, MaxPackageBytes+1))
		out.Close()
		if err != nil {
			return "", err
		}
		if n > MaxPackageBytes {
			return "", fmt.Errorf("the archive expands past the %d byte limit", MaxPackageBytes)
		}
	}
	if top == "" {
		return "", errors.New("the archive holds nothing")
	}
	// The directory name is the release's own identity: it is what makes "we downloaded 1.4.2" mean
	// these bytes came from a 1.4.2 archive.
	want := fmt.Sprintf("mike-server-%s-%s-%s", version, goos, goarch)
	if top != want {
		return "", fmt.Errorf("the archive unpacks to %q, expected the release directory %q", top, want)
	}
	return filepath.Join(dest, top), nil
}

// safeMember rejects the members a release archive never contains.
func safeMember(hdr *tar.Header) (string, error) {
	name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
	if name == "." || name == "/" || name == "" {
		return "", fmt.Errorf("the archive names %q, which is not a file", hdr.Name)
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(hdr.Name) {
		return "", fmt.Errorf("the archive names the absolute path %q", hdr.Name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("the archive names %q, which escapes the staging directory", hdr.Name)
		}
	}
	switch hdr.Typeflag {
	case tar.TypeReg:
	case tar.TypeDir:
	default:
		return "", fmt.Errorf("the archive member %q is a %s, and only regular files are unpacked", hdr.Name, typeName(hdr.Typeflag))
	}
	return name, nil
}

func typeName(t byte) string {
	switch t {
	case tar.TypeSymlink:
		return "symlink"
	case tar.TypeLink:
		return "hard link"
	case tar.TypeChar, tar.TypeBlock:
		return "device"
	case tar.TypeFifo:
		return "fifo"
	default:
		return fmt.Sprintf("member of type %q", string(t))
	}
}

func splitFirst(clean string) (string, string) {
	if i := strings.IndexByte(clean, '/'); i >= 0 {
		return clean[:i], clean[i+1:]
	}
	return clean, ""
}

// within reports whether target sits under root, after both are cleaned.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && fsutil.RelInsideRoot(rel)
}

func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// Cancellable so a shutdown stops before a rename rather than during one: a cancelled copy leaves
	// the previous file in place.
	if _, err := io.Copy(out, fsutil.ContextReader(ctx, in)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
