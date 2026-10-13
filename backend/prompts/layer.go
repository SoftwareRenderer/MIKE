package prompts

// Core templates resolve per name: the prompts directory on disk first, then the copy compiled into the
// binary. A directory holding one edited file overrides that file alone.

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"sync/atomic"

	"mike/pkg/logging"
)

// layerFS answers from top, and from base for what top does not hold. A top error that is not "not found"
// is returned instead of fallen through, so an unreadable override fails rather than quietly rendering
// the different text compiled in beside it.
type layerFS struct {
	top  fs.FS // nil answers nothing
	base fs.FS // nil answers nothing
}

func (l layerFS) Open(name string) (fs.File, error) {
	if l.top != nil {
		file, err := l.top.Open(name)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if l.base != nil {
		return l.base.Open(name)
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadDir returns both layers' entries, each name once and sorted as fs.ReadDir requires.
func (l layerFS) ReadDir(name string) ([]fs.DirEntry, error) {
	var top, base []fs.DirEntry
	var err error
	if l.top != nil {
		if top, err = fs.ReadDir(l.top, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if l.base != nil {
		if base, err = fs.ReadDir(l.base, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	entries := make([]fs.DirEntry, 0, len(top)+len(base))
	seen := make(map[string]bool, len(top))
	for _, entry := range top {
		seen[entry.Name()] = true
		entries = append(entries, entry)
	}
	for _, entry := range base {
		if !seen[entry.Name()] {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// coreBase is the compiled-in copy, installed once at startup. A pointer to the interface keeps "unset"
// different from an FS that answers nothing.
var coreBase atomic.Pointer[fs.FS]

// SetCoreFS installs the templates compiled into this binary as the layer the disk falls through to.
func SetCoreFS(templates fs.FS) {
	coreBase.Store(&templates)
	if templates == nil {
		// A test removing its stand-in is not a boot decision.
		return
	}

	// An override that answers is otherwise invisible until a turn renders text nobody edited.
	disk, _ := fs.Glob(diskLayer(), "*.tmpl")
	inlined, _ := fs.Glob(templates, "*.tmpl")
	overrides := 0
	for _, name := range disk {
		if _, err := fs.Stat(templates, name); err == nil {
			overrides++
		}
	}
	logging.For(logging.Prompts).Infof("prompt templates: %d compiled in, %d in %s (%d overriding the binary)",
		len(inlined), len(disk), promptDir(), overrides)
}

// diskLayer is the override. It is built per call because promptDir() is the seam a test moves.
func diskLayer() fs.FS {
	return os.DirFS(promptDir())
}

func coreFS() layerFS {
	var base fs.FS
	if held := coreBase.Load(); held != nil {
		base = *held
	}
	return layerFS{top: diskLayer(), base: base}
}
