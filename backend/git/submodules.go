package git

// Submodules: a task's worktree is given its submodule working copies from a linked worktree of the
// person's own store, `<gitDir>/modules/<path>`, because `git submodule update --init` in a linked worktree
// re-clones the `.gitmodules` URL once per task. MIKE never reads a URL out of `.gitmodules` — that text is
// repository data, and this runs where `credential.helper` and `core.sshCommand` are unfenced — and fetches
// only inside a store the person created, over that store's own remote.
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
	// sha is the commit the superproject records for it.
	sha string
	// store is the shared gitdir the person's own checkout populated: <gitDir>/modules/<path>.
	store string
}

// PopulateSubmodules gives the worktree every submodule its own commit names, nested ones included, and
// returns those paths parent-first; one already at that commit is left alone. A store the person never
// populated is an error naming it, never an empty directory handed to the agent.
func PopulateSubmodules(ctx context.Context, worktreePath string) ([]string, error) {
	var out []string
	err := walkSubmodules(worktreePath, func(s submodule) error {
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
func SubmodulePaths(worktreePath string) ([]string, error) {
	var out []string
	err := walkSubmodules(worktreePath, func(s submodule) error {
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
func walkSubmodules(worktreePath string, visit func(submodule) error) error {
	// The common gitdir holds both the objects and the `modules` tree, and answers for a store that has no
	// working tree of its own.
	gitDir, err := CommonGitDir(worktreePath)
	if err != nil {
		return err
	}
	head, err := commitAt(worktreePath, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve the task worktree's commit: %w", err)
	}

	// A node is one repository's commit and where its checkout lives relative to the worktree; its store is
	// also where its trees are read from.
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
		for _, link := range links {
			if count++; count > maxSubmoduleWalk {
				return fmt.Errorf("more than %d submodules in this repository", maxSubmoduleWalk)
			}
			if _, err := os.Stat(link.store); err != nil {
				return fmt.Errorf("submodule %s has no local copy at %s: populate it in the project checkout (git submodule update --init %s) before starting a task",
					link.path, link.store, link.path)
			}
			if err := visit(link); err != nil {
				return err
			}
			if n.depth+1 >= maxSubmoduleWalk {
				logging.For(logging.Git).Warnf("stopped walking submodules at %s: depth limit %d reached", link.path, maxSubmoduleWalk)
				continue
			}
			walk = append(walk, node{store: link.store, sha: link.sha, prefix: link.path, depth: n.depth + 1})
		}
	}
	return nil
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
			path:  path.Join(prefix, fields[1]),
			sha:   meta[2],
			store: filepath.Join(store, submoduleModulesDir, filepath.FromSlash(fields[1])),
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
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
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

// remoteURL names the store's origin for a failure a person has to act on, read back from the store's own
// configuration rather than a repository's working tree.
func remoteURL(store string) string {
	out, err := Run(store, "config", "--get", "remote.origin.url")
	if err != nil || strings.TrimSpace(out) == "" {
		return "origin (no URL configured)"
	}
	return strings.TrimSpace(out)
}

// PruneSubmoduleWorktrees prunes every submodule store's own worktree registry under a project, the twin of
// the project's own prune. The admin directory's name is never used, so only git's own sweep over directories
// that are gone is safe.
func PruneSubmoduleWorktrees(projectPath string) {
	gitDir, err := CommonGitDir(projectPath)
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
