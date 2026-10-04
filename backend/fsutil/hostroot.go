package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AllowRootFSEnv names the variable whose resolution widens the host-directory jail. The rule
// ("only the exact value 1", and a set-but-other value warns) is startup's; this constant is the
// name the refusal quotes. It is read
// per call, not cached at startup.
const AllowRootFSEnv = "MIKE_ALLOW_ROOT_FS"

// ErrHostRoot says a path is outside every root of the jail. Handlers answer it with 403.
var ErrHostRoot = errors.New("path is outside the allowed host directories")

// escapeMarker is the text os.Root puts on a refusal caused by a symlink in the path. That
// error is neither a sentinel nor a distinct type, so matching the text is the only way to
// tell "symlink involved, try the read fallback" from "no such path".
const escapeMarker = "path escapes from parent"

// hostRoot is one allowed directory, kept as declared (absolute, cleaned) and
// symlink-resolved. Candidates are matched against both forms.
type hostRoot struct {
	raw      string
	resolved string
}

// HostRoot jails the no-project file endpoints — the directory picker, its mkdir, git init,
// and adding a project by path — to the operator's home plus the directories declared at
// startup. A request naming a project is jailed to that project instead
// (Server.validateFSPath).
//
// os.Root refuses any path involving a symlink, even one inside the root: reads of a legitimate
// link fall back to Contained, and creation walks the components itself.
type HostRoot struct {
	roots []hostRoot
	// widened is the resolved MIKE_ALLOW_ROOT_FS decision. It arrives resolved because a jail that
	// re-read the variable per call could widen between two calls of one request, and because the
	// rule "only the exact value 1 widens" then lives in two places. A root set built around a
	// project passes false: the widening is never project-scoped.
	widened bool
}

// NewHostRoot builds the jail from the operator's home plus declaredDirs — directories named
// at startup (workspace dir, extensions dir) that a request cannot carry. They are made
// absolute against the working directory: the extensions dir defaults to "extensions".
//
// allowRootFS is the widened-jail decision (startup.Config.AllowRootFS). It belongs to the
// constructor because the jail is one object with one policy: a request that could change the
// answer mid-flight would make the roots listed in a refusal not the roots that refused.
func NewHostRoot(allowRootFS bool, declaredDirs ...string) *HostRoot {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return &HostRoot{roots: normalizeHostRoots(append([]string{home}, declaredDirs...)...), widened: allowRootFS}
}

// NewContainedRoots builds the same walk over exactly the directories given, with no implicit
// home and no widening by AllowRootFSEnv.
func NewContainedRoots(dirs ...string) *HostRoot {
	return &HostRoot{roots: normalizeHostRoots(dirs...)}
}

// normalizeHostRoots drops blanks and duplicates and keeps the symlink-resolved form of each
// root beside its absolute, cleaned path.
func normalizeHostRoots(dirs ...string) []hostRoot {
	out := make([]hostRoot, 0, len(dirs))
	seen := map[string]bool{}
	for _, raw := range dirs {
		abs, resolved, ok := RootForms(raw)
		if !ok {
			continue
		}
		for _, form := range []string{abs, resolved} {
			if seen[form] {
				continue
			}
			seen[form] = true
			out = append(out, hostRoot{raw: abs, resolved: resolved})
		}
	}
	return out
}

// unrestricted reports the widening granted at construction. A nil policy is never unrestricted.
func (h *HostRoot) unrestricted() bool { return h != nil && h.widened }

// Unrestricted reports whether host-wide access was granted by the environment.
func (h *HostRoot) Unrestricted() bool { return h.unrestricted() }

// Roots returns the distinct directories the jail allows, as declared.
func (h *HostRoot) Roots() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0, len(h.roots))
	seen := map[string]bool{}
	for _, r := range h.roots {
		if seen[r.raw] {
			continue
		}
		seen[r.raw] = true
		out = append(out, r.raw)
	}
	return out
}

// HasUniversalRoot reports that a root is the filesystem root, which constrains nothing.
func (h *HostRoot) HasUniversalRoot() bool {
	if h == nil {
		return false
	}
	for _, r := range h.roots {
		if r.raw == string(filepath.Separator) || r.resolved == string(filepath.Separator) {
			return true
		}
	}
	return false
}

// Err is the refusal for path: it names the roots and the widening variable.
func (h *HostRoot) Err(path string) error {
	roots := h.Roots()
	if len(roots) == 0 {
		return fmt.Errorf("%w: %s — no home directory and no declared directories (%s=1 allows any path)",
			ErrHostRoot, path, AllowRootFSEnv)
	}
	return fmt.Errorf("%w: %s (allowed: %s; set %s=1 to allow any host path)",
		ErrHostRoot, path, strings.Join(roots, ", "), AllowRootFSEnv)
}

// locate finds the root containing an absolute path and returns the path relative to it.
func (h *HostRoot) locate(p string) (base string, rel string, err error) {
	if h == nil {
		return "", "", h.Err(p)
	}
	if !filepath.IsAbs(p) {
		return "", "", fmt.Errorf("%w: %q is not an absolute path", ErrHostRoot, p)
	}
	clean := filepath.Clean(p)
	for _, r := range h.roots {
		for _, form := range []string{r.raw, r.resolved} {
			if form == "" || (clean != form && !strings.HasPrefix(clean, form+string(filepath.Separator))) {
				continue
			}
			rel, rerr := filepath.Rel(form, clean)
			if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			return form, rel, nil
		}
	}
	return "", "", h.Err(p)
}

// IsRoot reports that p names a root itself, where the picker hides "..". Always false when
// unrestricted.
func (h *HostRoot) IsRoot(p string) bool {
	if h == nil || h.unrestricted() {
		return false
	}
	clean := filepath.Clean(p)
	for _, r := range h.roots {
		if clean == r.raw || clean == r.resolved {
			return true
		}
	}
	return false
}

// rootFor opens the jail handle for the root containing p.
func (h *HostRoot) rootFor(p string) (base string, root *os.Root, rel string, err error) {
	base, rel, err = h.locate(p)
	if err != nil {
		return "", nil, "", err
	}
	root, err = os.OpenRoot(base)
	if err != nil {
		// The root vanished: not a containment refusal, so it must not read as 403.
		return "", nil, "", fmt.Errorf("host root %s cannot be opened: %w", base, err)
	}
	return base, root, rel, nil
}

func (h *HostRoot) openRoot(p string) (*os.Root, string, error) {
	_, root, rel, err := h.rootFor(p)
	return root, rel, err
}

// Stat stats path through the jail.
func (h *HostRoot) Stat(p string) (fs.FileInfo, error) {
	if h.unrestricted() {
		return os.Stat(p)
	}
	root, rel, err := h.openRoot(p)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	fi, serr := root.Stat(rel)
	if serr != nil && isPathEscape(serr) {
		return h.statResolved(p, serr)
	}
	return fi, serr
}

// resolvedFallback resolves a path os.Root refused. Contained requires the resolved path to be inside
// the same roots, so the fallback cannot widen the jail, and the refusal's cause stays in the message
// because an escape and a broken symlink otherwise read the same to whoever is debugging one.
func (h *HostRoot) resolvedFallback(p string, cause error) (string, error) {
	resolved, err := h.resolveInside(p)
	if err != nil {
		return "", fmt.Errorf("%w (a symlink in the path is the reason: %v)", h.Err(p), cause)
	}
	return resolved, nil
}

// statResolved is the read fallback for a link os.Root refused.
func (h *HostRoot) statResolved(p string, cause error) (fs.FileInfo, error) {
	resolved, err := h.resolvedFallback(p, cause)
	if err != nil {
		return nil, err
	}
	return os.Stat(resolved)
}

// ReadDir lists path through the jail.
func (h *HostRoot) ReadDir(p string) ([]fs.DirEntry, error) {
	if h.unrestricted() {
		return os.ReadDir(p)
	}
	root, rel, err := h.openRoot(p)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, oerr := root.Open(rel)
	if oerr != nil {
		if isPathEscape(oerr) {
			return h.readDirResolved(p, oerr)
		}
		return nil, oerr
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func (h *HostRoot) readDirResolved(p string, cause error) ([]fs.DirEntry, error) {
	resolved, err := h.resolvedFallback(p, cause)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(resolved)
}

// resolveInside resolves the path and requires the answer to be inside the root set.
func (h *HostRoot) resolveInside(p string) (string, error) {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: %q is not an absolute path", ErrHostRoot, p)
	}
	roots := make([]string, 0, len(h.roots))
	for _, r := range h.roots {
		roots = append(roots, r.raw, r.resolved)
	}
	resolved, err := Contained(clean, roots)
	if err != nil {
		return "", h.Err(p)
	}
	return resolved, nil
}

// MkdirAll creates a directory chain under a root, never through a symlink.
//
// Not (*os.Root).MkdirAll: its own refusal is a *PathError nothing can classify, so a jailed
// mkdir would answer 500 where the jail means 403.
func (h *HostRoot) MkdirAll(p string, perm fs.FileMode) error {
	if h.unrestricted() {
		return os.MkdirAll(p, perm)
	}
	base, root, rel, err := h.rootFor(p)
	if err != nil {
		return err
	}
	defer root.Close()

	if rel == "." || rel == "" {
		return nil // the root itself already exists
	}
	cur := ""
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		info, lerr := root.Lstat(cur)
		switch {
		case lerr == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: %s traverses the symlink %s — a jailed mkdir never follows a link",
					ErrHostRoot, p, filepath.Join(base, cur))
			}
			if !info.IsDir() {
				return fmt.Errorf("cannot create %s: %s exists and is not a directory", p, cur)
			}
		case errors.Is(lerr, fs.ErrNotExist):
			if merr := root.Mkdir(cur, perm); merr != nil {
				if errors.Is(merr, fs.ErrExist) {
					continue // someone else created it between Lstat and Mkdir
				}
				return fmt.Errorf("create %s: %w", p, merr)
			}
		default:
			return fmt.Errorf("create %s: %w", p, lerr)
		}
	}
	return nil
}

// IsHostRootRefusal reports that err is a jail refusal (403) rather than a miss (404).
func IsHostRootRefusal(err error) bool {
	return errors.Is(err, ErrHostRoot)
}

// isPathEscape reports whether an os.Root refusal involves a symlink.
func isPathEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), escapeMarker)
}
