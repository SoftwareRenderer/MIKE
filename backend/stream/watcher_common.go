package stream

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"

	"mike/pkg/logging"
)

// runWatcherEventLoop dispatches fsnotify events to handleEvent until the
// watcher is closed. prefix identifies the watcher in log messages.
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
// recording each path in pathCache for lookup. skipDir identifies directories
// to skip, including their subtrees.
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

// dropWatchedTree removes a directory and every key recorded below it, undoing
// addRecursiveWatcher. Callers must hold the lock guarding pathCache.
func dropWatchedTree(pathCache map[string]string, dir string) {
	prefix := dir + string(filepath.Separator)
	for p := range pathCache {
		if p == dir || strings.HasPrefix(p, prefix) {
			delete(pathCache, p)
		}
	}
}

// lookupWatchedOwner resolves the owner of path recorded by addRecursiveWatcher,
// or "" if none. Checks the path and its ancestors until an owner is found.
// Consults both bare and separator-terminated forms.
//
// Callers must hold the lock guarding pathCache; this reads the map as-is.
func lookupWatchedOwner(pathCache map[string]string, path string) string {
	dir := path
	for {
		if value, ok := pathCache[dir]; ok {
			return value
		}
		if value, ok := pathCache[dir+string(filepath.Separator)]; ok {
			return value
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
