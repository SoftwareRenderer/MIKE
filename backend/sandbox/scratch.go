package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"mike/backend/fsutil"
	"mike/pkg/logging"
	"mike/pkg/sandboxruntime"
)

// Every build gets one scratch directory, the only host path a build may write, so cleanup is one RemoveAll. It
// stays out of the data directory, where the database and TLS key live, because a Dockerfile decides how much disk
// a build needs.

// buildScratchRootName is the directory MIKE makes inside the worktrees root — a tree that is outside
// every checkout and already understood as MIKE-owned scratch.
const buildScratchRootName = "build-scratch"

// buildScratchRoot is injected at startup. Left unset it refuses every build rather than picking a host
// directory of its own; main sets it beside the worktrees root, which is outside every checkout and already
// MIKE-owned.
var buildScratchRoot string

// buildScratchSeq keeps two builds of the same project from naming the same directory.
var buildScratchSeq atomic.Uint64

// SetScratchRoot says where MIKE's per-build scratch directories live. Until it is called the build path stays
// disabled, which is how a test binary that never means to build keeps out of the filesystem.
func SetScratchRoot(root string) { buildScratchRoot = strings.TrimSpace(root) }

// buildScratch is one build's directory.
type buildScratch struct {
	// Dir is the directory itself, named by the build id.
	Dir string
	// ID names the directory and the container, so a leftover says which build left it.
	ID string
	// Output is the subdirectory mounted read-write into the build container.
	Output string

	peak atomic.Int64
	stop chan struct{}
	done chan struct{}
}

// newBuildScratch makes the per-build directory. Its name is not a secret: the id exists so that a leftover
// directory or container can be traced back to the build that made it.
func newBuildScratch(projectID, projectName string) (*buildScratch, error) {
	if buildScratchRoot == "" {
		return nil, fmt.Errorf("no scratch root is configured, so an image build has nowhere to write")
	}
	// The id is what ties a leftover to a build in the log; a caller that has no id (a project built from a
	// bare record) still has a name to file it under.
	base := ""
	for _, candidate := range []string{projectID, projectName} {
		if err := sandboxruntime.ValidateProjectName(strings.TrimSpace(candidate)); err == nil {
			base = strings.TrimSpace(candidate)
			break
		}
	}
	if base == "" {
		return nil, fmt.Errorf("the project has no id or name to file its build scratch under")
	}
	id := fmt.Sprintf("%s-%d-%d", base, time.Now().Unix(), buildScratchSeq.Add(1))
	if err := sandboxruntime.ValidateProjectName(id); err != nil {
		return nil, err
	}
	root := filepath.Join(buildScratchRoot, buildScratchRootName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create the scratch root: %w", err)
	}
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "context"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "base"), 0o755); err != nil {
		return nil, err
	}
	return &buildScratch{Dir: dir, ID: id, Output: filepath.Join(dir, "out")}, nil
}

// BaseArchiveAt is where base image i is written, one per directory: a read-only mount takes a directory, and the
// guest is given the exact name. The 0755 mode suits buildah, which is root in the guest though the bind keeps
// host ownership.
func (s *buildScratch) BaseArchiveAt(i int) string {
	return filepath.Join(s.Dir, "base", fmt.Sprintf("base-%d.tar", i))
}

// ContextDir is the staged tree, mounted read-only.
func (s *buildScratch) ContextDir() string { return filepath.Join(s.Dir, "context") }

// Archive is the one file the build container writes: the finished image as a docker-archive. One file is what
// makes its size watchable while the build runs, and the host reads it once, when loading the image.
func (s *buildScratch) Archive() string { return filepath.Join(s.Output, "image.tar") }

// Remove deletes the whole directory, reporting rather than failing: a leftover is a Warn naming the path,
// never a build that reports failure after its image loaded.
func (s *buildScratch) Remove() {
	if s == nil {
		return
	}
	s.StopWatch()
	if err := os.RemoveAll(s.Dir); err != nil {
		logging.For(logging.Sandbox).With(logging.BuildID, s.ID).Warnf("build scratch directory left behind at %s: %v", s.Dir, err)
		return
	}
	logging.For(logging.Sandbox).With(logging.BuildID, s.ID).Debugf("removed build scratch %s (peak %s)", s.Dir, humanBytes(s.peak.Load()))
}

// Watch samples the output directory's size, the one build artifact visible from the host: a container's own
// writable layer is not (docs/adr/0002). Past the cap the stop channel closes and the caller's cancelled context
// kills the container.
func (s *buildScratch) Watch(maxBytes int64, onCancel func()) {
	if s == nil || maxBytes <= 0 {
		return
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		tick := time.NewTicker(buildOutputSampleEvery)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				// Counted from the start every sample rather than kept as a running total: the directory
				// is small next to the walk's cost, and a running total is one more thing to get wrong.
				usage, err := fsutil.TreeUsageFor(s.Output, fsutil.WalkBudget{})
				size := usage.Bytes
				if err != nil {
					continue
				}
				if size > s.peak.Load() {
					s.peak.Store(size)
				}
				if size > maxBytes {
					logging.For(logging.Sandbox).With(logging.BuildID, s.ID).Errorf(
						"build output reached %s, over the %s limit; stopping the build", humanBytes(size), humanBytes(maxBytes))
					onCancel()
					return
				}
			}
		}
	}()
}

// StopWatch ends the sampler. Safe to call twice.
func (s *buildScratch) StopWatch() {
	if s == nil || s.stop == nil {
		return
	}
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	if s.done != nil {
		<-s.done
	}
	s.stop = nil
}

const (
	buildOutputSampleEvery = 2 * time.Second
	buildOutputMaxBytes    = int64(4) << 30
)

// SweepScratch removes every entry left under the scratch root. Nothing is running at boot, so every entry was
// left by a build that was killed, and a leftover build directory is the largest thing MIKE ever writes.
func SweepScratch() {
	if buildScratchRoot == "" {
		return
	}
	root := filepath.Join(buildScratchRoot, buildScratchRootName)
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.For(logging.Sandbox).Warnf("build scratch sweep could not read %s: %v", root, err)
		}
		return
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, name)
		usage, _ := fsutil.TreeUsageFor(path, fsutil.WalkBudget{})
		if err := os.RemoveAll(path); err != nil {
			logging.For(logging.Sandbox).Warnf("leftover build scratch %s (%s) could not be removed: %v", path, humanBytes(usage.Bytes), err)
			continue
		}
		logging.For(logging.Sandbox).Infof("removed leftover build scratch %s (%s)", path, humanBytes(usage.Bytes))
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
