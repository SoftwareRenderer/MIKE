package stream

import (
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"mike/pkg/logging"
)

// runWatcherEventLoop dispatches fsnotify events to handleEvent until the
// watcher is closed. prefix is used in log messages to identify which watcher
// the loop belongs to (e.g. "branch-watcher", "project-watcher", "watcher").
func runWatcherEventLoop(w *fsnotify.Watcher, prefix string, handleEvent func(fsnotify.Event)) {
	logging.For(logging.Stream).Infof("event loop started (%s)", prefix)
	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			handleEvent(event)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			logging.For(logging.Stream).Warnf("event loop error (%s): %v", prefix, err)
		}
	}
}

// addRecursiveWatcher adds dir and every subdirectory to the fsnotify watcher,
// recording each watched path in pathCache under value for O(1) lookup on
// events. skipDir returns true for a directory that should not be watched
// (e.g. ".git" or well-known ignored directories); such directories and their
// subtrees are skipped.
func addRecursiveWatcher(w *fsnotify.Watcher, pathCache map[string]string, dir, value string, skipDir func(name string) bool) error {
	if err := w.Add(dir); err != nil {
		return err
	}
	pathCache[dir] = value
	pathCache[dir+string(filepath.Separator)] = value
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != dir {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			_ = w.Add(path)
			pathCache[path] = value
			pathCache[path+string(filepath.Separator)] = value
		}
		return nil
	})
}

// lookupWatchedOwner resolves which watched tree owns path, returning the value
// recorded by addRecursiveWatcher. It checks path itself and then every ancestor
// directory, because fsnotify reports events for files *inside* a watched dir
// while pathCache is keyed by directory. Both the bare path and the path+separator
// forms are consulted, mirroring the two keys the setter writes.
//
// Callers must hold the lock guarding pathCache; this reads the map as-is.
func lookupWatchedOwner(pathCache map[string]string, path string) (string, bool) {
	dir := path
	for {
		if value, ok := pathCache[dir]; ok {
			return value, true
		}
		if value, ok := pathCache[dir+string(filepath.Separator)]; ok {
			return value, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
