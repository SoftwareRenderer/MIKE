package git

// Submodules: a task's worktree is given its submodule working copies as linked worktrees of the person's own
// shared stores, because `git submodule update --init` in a linked worktree re-clones the `.gitmodules` URL
// once per task. A store is found by asking git, never derived from the path, and MIKE never parses
// `.gitmodules` itself. The one fetch of a repository-chosen URL is InitSubmodules, at project import only; a
// task that finds no copy stops rather than reaching the network.
//
// docs/git-operations.md → Submodules: share the person's copy, never re-fetch.

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"mike/backend/fsutil"
	"mike/pkg/logging"
)

// submoduleModulesDir is the directory a git repository keeps its submodules' gitdirs under.
const submoduleModulesDir = "modules"

// maxSubmoduleWalk bounds the nested walk and the number of submodules one task is given, so a cycle or a
// pathological `.gitmodules` cannot spin this loop or fill the container's mount list.
const maxSubmoduleWalk = 128

// submoduleFetchTimeout bounds the one fetch a stale pointer is answered with, generously: a store's first
// fetch is a real clone. Only the cancelled context bounds the child — the WaitDelay `git.CommandContext`
// carries bounds its pipes.
const submoduleFetchTimeout = 5 * time.Minute

// objectIDRe is a git object id: SHA-1, or SHA-256 in a repository created with `--object-format=sha256`.
var objectIDRe = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// submodule is one gitlink a task's commit names, resolved to the shared store its objects come from.
type submodule struct {
	// path is the submodule directory, workspace-relative (the path the superproject records).
	path string
	// name is that path as the parent's own tree records it, which the fallback derivation uses.
	name string
	// sha is the commit the superproject records for it.
	sha string
	// store is the shared gitdir the person's own checkout populated, found by resolveStore.
	store string
}

// PopulateSubmodules gives the worktree every submodule its own commit names, nested ones included, and
// returns those paths parent-first; one already at that commit is left alone. A store the person never
// populated is an error naming it, never an empty directory handed to the agent.
//
// projectPath is the checkout the person populated, whose submodule working trees name the stores this hands
// out; worktreePath is the task's directory the checkouts are built in.
func PopulateSubmodules(ctx context.Context, worktreePath, projectPath string) ([]string, error) {
	var out []string
	err := walkSubmodules(worktreePath, projectPath, func(s submodule) error {
		if err := ensureCommit(ctx, s.store, s.sha); err != nil {
			return fmt.Errorf("submodule %s: %w", s.path, err)
		}
		if err := checkoutSubmodule(s.store, s.sha, filepath.Join(worktreePath, filepath.FromSlash(s.path))); err != nil {
			return fmt.Errorf("submodule %s: %w", s.path, err)
		}
		out = append(out, s.path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SubmodulePaths lists the submodules a worktree's own commit names, in the order PopulateSubmodules walks,
// and creates nothing. Every path that builds a container derives its git-pointer pins from here, so a
// rebuild cannot forget a pin the first create remembered.
func SubmodulePaths(worktreePath, projectPath string) ([]string, error) {
	var out []string
	err := walkSubmodules(worktreePath, projectPath, func(s submodule) error {
		out = append(out, s.path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// walkSubmodules walks the gitlinks of a worktree's own commit out through each submodule's shared store,
// calling visit parents first. A missing store, or one without the commit its superproject records, is an
// error rather than a skipped branch: skipping it would under-pin the container.
func walkSubmodules(worktreePath, projectPath string, visit func(submodule) error) error {
	// The checkout's own gitdir holds the `modules` tree it populated: the common directory for an ordinary
	// checkout, the admin directory for a project that is itself a `git worktree`. A checkout that is gone
	// leaves the task's worktree to read the shared objects, and the derivation alone to find stores by.
	gitDir, err := checkoutGitDir(projectPath)
	if err != nil {
		if gitDir, err = CommonGitDir(worktreePath); err != nil {
			return err
		}
	}
	head, err := commitAt(worktreePath, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve the task worktree's commit: %w", err)
	}

	// A node is one repository's commit, the store its trees are read from, and where its checkout lives
	// relative to the task's worktree.
	type node struct {
		store  string
		sha    string
		prefix string
		depth  int
	}

	count := 0
	walk := []node{{store: gitDir, sha: head}}
	for len(walk) > 0 {
		n := walk[0]
		walk = walk[1:]

		links, err := gitlinks(n.store, n.sha, n.prefix)
		if err != nil {
			return err
		}
		modulesDir := filepath.Join(n.store, submoduleModulesDir)
		for _, link := range links {
			if count++; count > maxSubmoduleWalk {
				return fmt.Errorf("more than %d submodules in this repository", maxSubmoduleWalk)
			}
			// A project moved out from under a live task: no checkout to name, so only the derivation.
			var checkout string
			if projectPath != "" {
				checkout = filepath.Join(projectPath, filepath.FromSlash(link.path))
			}
			store := resolveStore(modulesDir, checkout, link.name)
			if store == "" {
				return fmt.Errorf("submodule %s has no local copy under %s: populate it in the project checkout at %s (git submodule update --init --recursive %s) before starting a task",
					link.path, modulesDir, projectPath, link.path)
			}
			if work := worktreeOf(store); work != "" {
				// The store is read through the person's own copy, so a missing one is not a state to read past.
				if _, err := os.Stat(work); err != nil {
					return fmt.Errorf("submodule %s's local copy at %s has no working tree — %s is gone: restore it in the project checkout at %s (git submodule update --init --recursive %s) before starting a task",
						link.path, store, work, projectPath, link.path)
				}
			}
			link.store = store
			if err := visit(link); err != nil {
				return err
			}
			if n.depth+1 >= maxSubmoduleWalk {
				logging.For(logging.Git).Warnf("stopped walking submodules at %s: depth limit %d reached", link.path, maxSubmoduleWalk)
				continue
			}
			walk = append(walk, node{store: store, sha: link.sha, prefix: link.path, depth: n.depth + 1})
		}
	}
	return nil
}

// resolveStore names the shared gitdir under modulesDir that holds the person's copy of the submodule whose
// working tree is checkout, or "" where they never populated one. git keys that directory by the
// `.gitmodules` section rather than by the path, so the checkout's own answer is asked first and the
// `<modulesDir>/<name>` derivation is the fallback for a store whose checkout is gone.
func resolveStore(modulesDir, checkout, name string) string {
	if asked := storeOfCheckout(checkout); asked != "" {
		// Only a store inside this checkout's own `modules` tree counts: that tree is what the container pins
		// read-only. An answer naming metadata elsewhere on the disk is another repository's.
		if dir, err := fsutil.Contained(asked, []string{modulesDir}); err == nil {
			return dir
		}
	}
	if derived := filepath.Join(modulesDir, filepath.FromSlash(name)); isGitDir(derived) {
		return derived
	}
	return ""
}

// storeOfCheckout is the gitdir the checkout at dir uses, or "" where there is no checkout to ask. The git
// pointer is looked for before git is, because rev-parse walks up: an unpopulated submodule, or an empty path,
// would otherwise answer with somebody else's repository.
func storeOfCheckout(dir string) string {
	if dir == "" || !HasGitDir(dir) {
		return ""
	}
	asked, err := absGitDir(dir, "--git-dir")
	if err != nil {
		return ""
	}
	return asked
}

// worktreeOf is the working tree a store serves, or "" where its configuration names none — the form a bare
// repository is left in. The store's own `config` is read by name because git changes into the working tree
// before it runs a single command, so a store whose copy is gone cannot be opened as a repository at all.
func worktreeOf(store string) string {
	out, err := Run("", "config", "--file", filepath.Join(store, "config"), "--get", "core.worktree")
	if err != nil {
		return ""
	}
	work := strings.TrimSpace(out)
	if work == "" {
		return ""
	}
	if !filepath.IsAbs(work) {
		work = filepath.Join(store, work)
	}
	return filepath.Clean(work)
}

// gitlinks lists the gitlinks of one commit as submodule rows, NUL-delimited so a path with a quote or a
// newline arrives whole. The listing comes from the commit, not the working tree, so what an agent writes
// later cannot choose which paths a container mounts.
func gitlinks(store, sha, prefix string) ([]submodule, error) {
	out, err := Run(store, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return nil, fmt.Errorf("list submodules of %s: %s", shortHash(sha), gitMessage(out, err))
	}
	var links []submodule
	for _, entry := range strings.Split(out, "\x00") {
		// "<mode> <type> <sha>\t<path>"
		fields := strings.SplitN(entry, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		meta := strings.Fields(fields[0])
		if len(meta) != 3 || meta[0] != "160000" {
			continue
		}
		if err := validateSubmodulePath(fields[1]); err != nil {
			return nil, err
		}
		links = append(links, submodule{
			path: path.Join(prefix, fields[1]),
			name: fields[1],
			sha:  meta[2],
		})
	}
	return links, nil
}

// validateSubmodulePath rejects a gitlink path that names a directory outside the worktree or would take a
// git pointer's place, which is where it becomes a filesystem path and a mount point.
func validateSubmodulePath(p string) error {
	if err := fsutil.ValidateRelPath(p); err != nil {
		return fmt.Errorf("submodule path %q: %w", p, err)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".git" {
			return fmt.Errorf("submodule path %q would replace a git pointer", p)
		}
	}
	return nil
}

// checkoutSubmodule gives dst the recorded commit as a linked worktree of the submodule's own shared
// repository. A checkout already at that commit is left alone: touching it would pull a mount out from under
// a container that may have it mounted.
func checkoutSubmodule(store, sha, dst string) error {
	if current := checkedOutCommit(dst); current == sha {
		return nil
	} else if current != "" {
		// The task's own earlier turn moved it: the directory is the task's, and the superproject cannot see
		// through the gitlink.
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("clear the stale submodule checkout: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if out, err := Run(store, "worktree", "add", "--detach", dst, sha); err != nil {
		return fmt.Errorf("create the submodule worktree: %s: %w", gitMessage(out, err), err)
	}
	return nil
}

// checkedOutCommit is the commit the checkout at dir stands on, or "" where there is no checkout.
func checkedOutCommit(dir string) string {
	if !HasGitDir(dir) {
		return ""
	}
	out, _ := Run(dir, "rev-parse", "--quiet", "HEAD")
	return strings.TrimSpace(out)
}

// ensureCommit makes the shared store hold a commit, fetching once into the store — never into a task —
// when it does not, so the next task on that pointer is local again.
func ensureCommit(ctx context.Context, store, sha string) error {
	if !objectIDRe.MatchString(sha) {
		return fmt.Errorf("submodule records %q, which is not a commit id", sha)
	}
	if _, err := Run(store, "cat-file", "-e", sha+"^{commit}"); err == nil {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, submoduleFetchTimeout)
	defer cancel()
	out, err := CommandContext(fetchCtx, store, "fetch", "--no-tags", "origin").CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch %s for commit %s: %s: %w", remoteURL(store), shortHash(sha), gitMessage(string(out), err), err)
	}
	if _, err := Run(store, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return fmt.Errorf("remote %s does not have commit %s", remoteURL(store), shortHash(sha))
	}
	logging.For(logging.Git).Infof("fetched a submodule's missing commit into the shared store at %s", store)
	return nil
}

// remoteURL names the store's origin, for a failure a person has to act on.
func remoteURL(store string) string {
	out, err := Run(store, "config", "--get", "remote.origin.url")
	if err != nil || strings.TrimSpace(out) == "" {
		return "origin (no URL configured)"
	}
	return strings.TrimSpace(out)
}

// checkoutState is what the person's own submodule checkout holds, which decides whether fetching into it
// can cost them anything.
type checkoutState int

const (
	// checkoutAbsent is the state a fresh clone and `git worktree add` leave: no working copy at all.
	checkoutAbsent checkoutState = iota
	// checkoutAtCommit holds the recorded commit and nothing of the person's own on top of it.
	checkoutAtCommit
	checkoutDirty
	checkoutElsewhere
	// checkoutNotASubmodule is content with no git pointer, so not git's copy to move.
	checkoutNotASubmodule
)

func (s checkoutState) String() string {
	switch s {
	case checkoutDirty:
		return "it holds uncommitted work of the person's own"
	case checkoutElsewhere:
		return "it stands on a commit of the person's own, not the one the superproject records"
	case checkoutNotASubmodule:
		return "its directory holds files with no git pointer in it"
	}
	return "its working copy is missing"
}

// InitSubmodules fetches, into the person's own checkout, the working copy of every submodule that checkout's
// commit names, nested ones included, and returns the paths it populated. It belongs to project import and
// nowhere else: the fetch spends the operator's own credentials (`credential.helper`, their ssh agent) on a
// destination the repository's text chose, and leaves that destination as the store's `origin`, which every
// later fetch then treats as the person's own remote.
//
// A checkout the person has content in is left alone: `git submodule update` checks the recorded commit out,
// which detaches their own submodule off their work. onStatus, which may be nil, hears every outcome worth
// reporting; a submodule that will not fetch is skipped, because a project whose upstream is unreachable is
// still a project.
//
// The fetch is git's own verb over submoduleProtocols, so a repository cannot point a submodule at a directory
// on this machine.
func InitSubmodules(ctx context.Context, projectPath string, onStatus func(string)) ([]string, error) {
	return initSubmodules(ctx, projectPath, nil, onStatus)
}

// initSubmodules takes the environment a fixture appends to reach an upstream that is a directory on this
// machine.
func initSubmodules(ctx context.Context, projectPath string, widen []string, onStatus func(string)) ([]string, error) {
	report := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if onStatus != nil {
			onStatus(line)
		}
		logging.For(logging.Git).Warnf("importing submodules of %s: %s", projectPath, line)
	}

	gitDir, err := checkoutGitDir(projectPath)
	if err != nil {
		return nil, err
	}
	head, err := commitAt(projectPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve the project's commit: %w", err)
	}
	// The listing comes from the commit, so a `.gitmodules` advertising a submodule no commit names is never
	// fetched, and every path handed to git is one validateSubmodulePath already accepted.
	links, err := gitlinks(gitDir, head, "")
	if err != nil {
		return nil, err
	}
	if len(links) > maxSubmoduleWalk {
		return nil, fmt.Errorf("more than %d submodules in this repository", maxSubmoduleWalk)
	}

	var populated []string
	for _, link := range links {
		checkout := filepath.Join(projectPath, filepath.FromSlash(link.path))
		switch state := checkoutStateOf(checkout, link.sha); state {
		case checkoutAtCommit:
			populated = append(populated, link.path)
			continue
		case checkoutAbsent:
		default:
			report("submodule %s was left alone: %s", link.path, state)
			continue
		}
		cmd := CommandContext(ctx, projectPath, submoduleInitArgs(link.path)...)
		cmd.Env = submoduleEnv(widen)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// A cancelled request is the caller's own navigation, not a repository that could not be
			// fetched; the same distinction the clone path makes.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return populated, ctxErr
			}
			report("submodule %s was not fetched: %s", link.path, gitMessage(string(out), err))
			continue
		}
		populated = append(populated, link.path)
	}
	return populated, nil
}

// submoduleInitArgs is the argv that fetches one submodule's working copy: git's own verb, asked to recurse,
// for one validated path the superproject's commit records rather than for every section `.gitmodules`
// advertises. The transports that URL may use are refused in the environment — see submoduleEnv.
func submoduleInitArgs(subPath string) []string {
	return []string{"submodule", "update", "--init", "--recursive", "--", subPath}
}

// checkoutStateOf is what the person's own working copy of a submodule holds. The directory is read before git
// is asked anything, because a never-populated submodule leaves an empty one.
func checkoutStateOf(checkout, sha string) checkoutState {
	entries, err := os.ReadDir(checkout)
	if err != nil || len(entries) == 0 {
		return checkoutAbsent
	}
	if !HasGitDir(checkout) {
		return checkoutNotASubmodule
	}
	if checkedOutCommit(checkout) != sha {
		return checkoutElsewhere
	}
	if out, err := Run(checkout, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
		return checkoutDirty
	}
	return checkoutAtCommit
}

// PruneSubmoduleWorktrees prunes every submodule store's own worktree registry under a project, the twin of the
// project's own prune. Each store is swept by git, never by removing registrations from an admin directory
// whose name git derives from a checkout that may be gone.
func PruneSubmoduleWorktrees(projectPath string) {
	gitDir, err := checkoutGitDir(projectPath)
	if err != nil {
		return
	}
	for _, store := range submoduleStores(filepath.Join(gitDir, submoduleModulesDir), 0) {
		if _, err := Run(store, "worktree", "prune"); err != nil {
			logging.For(logging.Git).Warnf("pruning submodule worktrees in %s failed", store)
		}
	}
}

// submoduleStores collects every submodule gitdir under dir. The segments of a nested path are plain
// directories on the way to the gitdir at the end of them, and a nested submodule hangs off its parent
// store's own `modules`.
func submoduleStores(dir string, depth int) []string {
	if depth >= maxSubmoduleWalk {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var stores []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if !isGitDir(child) {
			// A path segment on the way to a store, e.g. the `vendor` of `vendor/lib`.
			if !slices.Contains(gitDirInternals, entry.Name()) {
				stores = append(stores, submoduleStores(child, depth+1)...)
			}
			continue
		}
		stores = append(stores, child)
		stores = append(stores, submoduleStores(filepath.Join(child, submoduleModulesDir), depth+1)...)
	}
	return stores
}

// gitDirInternals are the directories a git repository keeps inside its own gitdir, none of which holds a
// submodule store — `worktrees` holds the registrations this sweep is about.
var gitDirInternals = []string{
	"branches", "hooks", "info", "logs", "objects", "refs", "rr-cache", "svn", "worktrees",
}

// isGitDir reports whether dir is a repository's gitdir rather than a linked worktree's admin directory:
// both carry a HEAD, and only the admin directory carries a commondir pointing back at the repository.
func isGitDir(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, "HEAD")); err != nil {
		return false
	}
	if _, err := os.Lstat(filepath.Join(dir, "commondir")); err == nil {
		return false
	}
	_, err := os.Lstat(filepath.Join(dir, "objects"))
	return err == nil
}

// commitAt resolves a ref to a commit id and returns it only in the form git itself would accept as one,
// so a ref read back from a repository is never pasted on to another command line.
func commitAt(dir, ref string) (string, error) {
	out, err := Run(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s is not a commit: %s", ref, gitMessage(out, err))
	}
	sha := strings.TrimSpace(out)
	if !objectIDRe.MatchString(sha) {
		return "", fmt.Errorf("%s resolved to %q, which is not a commit id", ref, sha)
	}
	return sha, nil
}
