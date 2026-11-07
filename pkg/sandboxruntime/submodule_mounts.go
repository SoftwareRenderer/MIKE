package sandboxruntime

// The submodule half of a task worktree's git pointers: each populated submodule adds a `.git` file inside
// the read-write worktree mount, and whoever rewrites it decides which configuration the host's git reads
// next. A pointer is pinned read-only and every directory above it bound, so it cannot be renamed out from
// under its pin.
//
// docs/sandbox-runtime.md → A task worktree's git pointer is pinned read-only.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// maxSubmoduleMounts bounds how many submodule paths one container is configured with, so a repository of
// pathological gitlinks cannot produce more podman arguments than a create can carry. The walk that produces
// the listing (backend/git/submodules.go) holds the same bound.
const maxSubmoduleMounts = 128

// SubmoduleMounts returns the `-v` pairs that pin a task worktree's submodule git pointers, given the
// workspace-relative paths the task's commit names. Append them after the worktree's own mount, which is
// where their destinations live, and treat an error as a refusal: a skipped mount is a container that
// believes it is pinned.
func SubmoduleMounts(worktreePath, mountPath string, submodules []string, contentMode string) ([]string, error) {
	if contentMode != "rw" && contentMode != "ro" {
		return nil, fmt.Errorf("submodule content mode %q is neither \"rw\" nor \"ro\"", contentMode)
	}
	if len(submodules) > maxSubmoduleMounts {
		return nil, fmt.Errorf("%d submodules is more than %d this container can pin", len(submodules), maxSubmoduleMounts)
	}

	ancestors := map[string]bool{}
	for _, sub := range submodules {
		if err := ValidateSubmodulePath(sub); err != nil {
			return nil, err
		}
		segs := strings.Split(sub, "/")
		prefix := ""
		for _, seg := range segs[:len(segs)-1] {
			prefix = path.Join(prefix, seg)
			ancestors[prefix] = true
		}
	}

	// Shallowest first: a mount's destination has to exist as a real directory before another is layered
	// inside it, and lexical order puts every prefix before the path it prefixes.
	ordered := make([]string, 0, len(ancestors))
	for anc := range ancestors {
		ordered = append(ordered, anc)
	}
	sort.Strings(ordered)

	var args []string
	for _, anc := range ordered {
		host := filepath.Join(worktreePath, filepath.FromSlash(anc))
		dest := path.Join(mountPath, anc)
		if err := ValidateContainerPath(dest); err != nil {
			return nil, err
		}
		info, err := os.Stat(host)
		if err != nil {
			return nil, fmt.Errorf("submodule ancestor %s is missing from the worktree (%v): its mount cannot be established", anc, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("submodule ancestor %s is not a directory; only a directory can be pinned in place", anc)
		}
		args = append(args, "-v", BindMountSpec(host, dest, contentMode))
	}

	for _, sub := range submodules {
		host := filepath.Join(worktreePath, filepath.FromSlash(sub), GitPointerFile)
		dest := path.Join(mountPath, sub, GitPointerFile)
		if err := ValidateContainerPath(dest); err != nil {
			return nil, err
		}
		info, err := os.Lstat(host)
		if err != nil {
			return nil, fmt.Errorf("submodule %s has no git pointer to pin (%v): a populated submodule is a linked worktree, so its %s file must exist before a container is created around it",
				sub, err, GitPointerFile)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("submodule %s has a %s where its git pointer should be; only a regular file can be pinned read-only",
				sub, info.Mode().Type())
		}
		args = append(args, "-v", BindMountSpec(host, dest, "ro"))
	}
	return args, nil
}

// ValidateSubmodulePath accepts a workspace-relative submodule path only if it names one directory inside
// the worktree: no escaping, no empty or dotted segment, and no segment that is itself a git pointer. The
// charset is the container path's own, and the check is on the relative form because path.Join would clean a
// `..` out of the destination.
func ValidateSubmodulePath(sub string) error {
	if sub == "" || strings.HasPrefix(sub, "/") {
		return fmt.Errorf("submodule path %q is not a workspace-relative path", sub)
	}
	if err := ValidateContainerPath("/" + sub); err != nil {
		return fmt.Errorf("submodule path %q: %w", sub, err)
	}
	for _, seg := range strings.Split(sub, "/") {
		if seg == "" || seg == "." {
			return fmt.Errorf("submodule path %q has an empty segment", sub)
		}
		if seg == GitPointerFile {
			return fmt.Errorf("submodule path %q would replace a git pointer", sub)
		}
	}
	return nil
}

// VerifySubmoduleGitPointer requires a submodule's git pointer to name a gitdir under
// <projectGitDir>/modules/, the tree only the person's own checkout and MIKE's shared store write. Like its
// worktree twin it judges the cleaned path and ignores the admin directory's name.
func VerifySubmoduleGitPointer(worktreePath, subPath, projectGitDir string) error {
	if err := ValidateSubmodulePath(subPath); err != nil {
		return err
	}
	target, err := gitPointerTarget(worktreePath, subPath)
	if err != nil {
		return err
	}
	adminRoot := filepath.Join(projectGitDir, "modules")
	if !withinDir(adminRoot, target) {
		return fmt.Errorf("the git pointer of submodule %s names %q, which is outside %s: the submodule's git metadata must belong to this project",
			subPath, target, adminRoot)
	}
	return nil
}
