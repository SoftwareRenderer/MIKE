package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/types"
)

// The build context is a subtree of one commit, staged with `git archive`, so tracked files in that subtree are the
// whole input: untracked files, `.git` and the worktrees directory were never in the set rather than excluded by a
// rule (docs/adr/0002).

// The staged tree is bounded from `git ls-tree` before any byte is written, so a project over the bound is refused
// without a partial tree on disk.
const (
	buildContextMaxEntries = fsutil.CopyMaxEntries
	buildContextMaxBytes   = int64(512) << 20
	// buildDockerfileMaxBytes bounds the file MIKE parses. A Dockerfile past it is not a Dockerfile.
	buildDockerfileMaxBytes = 1 << 20
)

// BuildContext is the staged input of one build.
type BuildContext struct {
	// Dir is the staged root on the host, inside this build's scratch directory.
	Dir string
	// Root is the repository-relative directory the context covers ("." is the whole repository).
	Root string
	// Commit is the pinned object id every byte in Dir came from.
	Commit string
	// Dockerfile is the Dockerfile's path relative to Dir.
	Dockerfile string
	// Files and Bytes are what was staged, for the audit line and the progress message.
	Files int
	Bytes int64
}

// BuildContextRoot decides which repository directory a build reads, defaulting to the Dockerfile's own. Both
// inputs are validated here, because the build reads the stored value with the server's privileges.
func BuildContextRoot(dockerfileRel, configured string) (string, error) {
	root := strings.TrimSpace(configured)
	if root == "" {
		dir := path.Dir(filepath.ToSlash(dockerfileRel))
		if dir == "" || dir == "." {
			return ".", nil
		}
		return dir, nil
	}
	if err := validateRepoRelative(root); err != nil {
		return "", fmt.Errorf("sandbox_build_context: %w", err)
	}
	return path.Clean(root), nil
}

// validateRepoRelative rejects anything that is not a path inside a repository: absolute paths, drive letters,
// Windows separators, empty or dot segments, `..`, and the git directory.
//
// A host path is refused rather than jailed, because this field selects a subtree of a commit.
func validateRepoRelative(rel string) error {
	if rel == "" {
		return fmt.Errorf("path is empty")
	}
	if len(rel) > fsutil.MaxRelPathLen {
		return fmt.Errorf("path is too long (max %d characters)", fsutil.MaxRelPathLen)
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("path must be inside the repository, not absolute")
	}
	if strings.ContainsRune(rel, '\\') {
		return fmt.Errorf(`path must not contain "\"`)
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return fmt.Errorf("path must not name a drive")
	}
	// Nothing expands this field: `~` and `$HOME` would be a host path arriving by another name.
	if strings.HasPrefix(rel, "~") || strings.ContainsRune(rel, '$') {
		return fmt.Errorf("path must be a path inside the repository, not a home or variable reference")
	}
	if fsutil.HasGitSegment(rel) {
		return fmt.Errorf("path must not be inside .git")
	}
	trimmed := strings.TrimSuffix(rel, "/")
	if trimmed == "" {
		return fmt.Errorf("path must not be the repository root written as %q", rel)
	}
	if cleaned := path.Clean(trimmed); cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf(`path must not contain ".."`)
	}
	for _, seg := range strings.Split(trimmed, "/") {
		switch seg {
		case "":
			return fmt.Errorf("path contains an empty segment")
		case "..":
			return fmt.Errorf(`path must not contain ".."`)
		}
		if len(seg) > fsutil.MaxRelSegmentLen {
			return fmt.Errorf("path segment is too long (max %d characters)", fsutil.MaxRelSegmentLen)
		}
	}
	return nil
}

// NormalizeBuildContextValue folds the column to one shape, so `service/` and `service` cannot be two cache keys for
// one build. A shape that changes the meaning is refused, so no caller can store it.
func NormalizeBuildContextValue(rel string) (string, error) {
	trimmed := strings.TrimSpace(rel)
	if trimmed == "" {
		return "", nil
	}
	// Refused as written, before any folding: nobody writes `a/../b` meaning `b`, and a `..` here reaches
	// outside the context directory.
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return "", fmt.Errorf(`path must not contain ".."`)
		}
	}
	cleaned := path.Clean(trimmed)
	if err := validateRepoRelative(cleaned); err != nil {
		return "", err
	}
	return cleaned, nil
}

// dockerfileInRepo turns the stored Dockerfile path into a path inside the checkout. The column holds a host path
// because that is what Settings writes, but a build reads the file from a commit.
func dockerfileInRepo(projectPath, dockerfilePath string) (string, error) {
	if projectPath == "" || dockerfilePath == "" {
		return "", fmt.Errorf("the project needs both a repository path and a Dockerfile path")
	}
	absFile, err := filepath.Abs(dockerfilePath)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil || !fsutil.RelInsideRoot(rel) || rel == "." {
		return "", fmt.Errorf("sandbox_dockerfile_path %s is outside the repository %s: a build reads the Dockerfile from a commit, so point it at a file that is committed inside the repository", dockerfilePath, projectPath)
	}
	rel = filepath.ToSlash(rel)
	if err := validateRepoRelative(rel); err != nil {
		return "", fmt.Errorf("sandbox_dockerfile_path: %w", err)
	}
	return rel, nil
}

// resolveGitRef runs one git command in the checkout and returns its trimmed stdout. It is separate so
// the staging path shares the fence and the timeout with every other git read in this repository.
func resolveGitRef(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := git.CommandContext(ctx, repo, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, lastLine(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// StageBuildContext writes the context subtree of the checkout's current commit into dst. One commit is resolved
// first and used for every later read, so nothing can come from a different one.
func StageBuildContext(ctx context.Context, project *types.Project, root, dockerfileRel, dst string) (*BuildContext, error) {
	if err := validateRepoRelative(root); err != nil {
		return nil, fmt.Errorf("sandbox_build_context: %w", err)
	}
	if err := validateRepoRelative(dockerfileRel); err != nil {
		return nil, fmt.Errorf("sandbox_dockerfile_path: %w", err)
	}

	commit, err := resolveGitRef(ctx, project.Path, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if err := validateGitObjectID(commit); err != nil {
		return nil, fmt.Errorf("repository HEAD is not an object id: %w", err)
	}

	relDockerfile, err := relativeToContext(root, dockerfileRel)
	if err != nil {
		return nil, err
	}

	entries, total, err := listTree(ctx, project.Path, commit, root)
	if err != nil {
		return nil, err
	}
	if entries > buildContextMaxEntries {
		return nil, fmt.Errorf("build context %s holds %d files, over the limit of %d: narrow the build context", root, entries, buildContextMaxEntries)
	}
	if total > buildContextMaxBytes {
		return nil, fmt.Errorf("build context %s holds %d MB, over the limit of %d MB: narrow the build context", root, total>>20, buildContextMaxBytes>>20)
	}

	archive, err := gitArchive(ctx, project.Path, commit, root)
	if err != nil {
		return nil, err
	}
	if err := extractBuildContextArchive(archive, dst, root); err != nil {
		return nil, err
	}
	return &BuildContext{
		Dir:        dst,
		Root:       root,
		Commit:     commit,
		Dockerfile: relDockerfile,
		Files:      entries,
		Bytes:      total,
	}, nil
}

// relativeToContext is the Dockerfile's path inside the staged tree, or a refusal when the context does not contain
// it. The staged tree is the only place the builder reads.
func relativeToContext(root, dockerfileRel string) (string, error) {
	if root == "." {
		return dockerfileRel, nil
	}
	rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(dockerfileRel))
	if err != nil || !fsutil.RelInsideRoot(rel) || strings.HasPrefix(filepath.ToSlash(rel), "..") {
		return "", fmt.Errorf("the build context %s does not contain the Dockerfile %s: set the build context to the Dockerfile's directory or above it", root, dockerfileRel)
	}
	return filepath.ToSlash(rel), nil
}

// listTree counts the regular files a subtree holds, without reading them: `ls-tree -r -l` reports each
// blob's size in the same pass.
func listTree(ctx context.Context, repo, commit, root string) (entries int, total int64, err error) {
	args := []string{"ls-tree", "-r", "-l", commit}
	if root != "." {
		args = append(args, "--", root)
	}
	out, err := resolveGitRef(ctx, repo, args...)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		// "<mode> <type> <sha>\t<size>\t<path>", where size is "-" for a symlink, which has no bytes.
		fields := strings.Fields(strings.Replace(line, "\t", " ", 2))
		if len(fields) < 5 || fields[1] != "blob" {
			continue
		}
		entries++
		if fields[3] == "-" {
			continue
		}
		if n, convErr := strconv.ParseInt(fields[3], 10, 64); convErr == nil {
			total += n
		}
	}
	if entries == 0 {
		return 0, 0, fmt.Errorf("the build context %s holds no committed files at HEAD", root)
	}
	return entries, total, nil
}

// gitArchive returns one commit's subtree as a tar stream.
func gitArchive(ctx context.Context, repo, commit, root string) ([]byte, error) {
	args := []string{"archive", "--format=tar", commit}
	if root != "." {
		args = append(args, "--", root)
	}
	cmd := git.CommandContext(ctx, repo, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git archive: %w: %s", err, lastLine(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// extractBuildContextArchive unpacks the archive into dst, keeping only regular files under the staged prefix. A
// symlink is why this is not `tar -x`: it would resolve to a host path the builder follows.
func extractBuildContextArchive(archive []byte, dst, root string) error {
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read build archive: %w", err)
		}
		// `git archive` opens its stream with a global pax header naming itself. It describes the stream,
		// not a file, and every rule below — including the subtree prefix — would refuse it.
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := path.Clean(filepath.ToSlash(hdr.Name))
		if root != "." {
			// `git archive` emits the ancestor directories of a pathspec so the tree can be created; they
			// carry no bytes, and the staged directory stands in for the root itself.
			if hdr.Typeflag == tar.TypeDir && (name == root || strings.HasPrefix(root+"/", name+"/")) {
				continue
			}
			if !strings.HasPrefix(name, root+"/") {
				return fmt.Errorf("build archive holds %q, which is outside the context %s", hdr.Name, root)
			}
			name = strings.TrimPrefix(name, root+"/")
		}
		if name == "." || name == "/" || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			return fmt.Errorf("build archive holds an entry outside the context: %q", hdr.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o400)
			if err != nil {
				return err
			}
			written, copyErr := io.Copy(f, io.LimitReader(tr, buildContextMaxBytes+1))
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if written > buildContextMaxBytes {
				return fmt.Errorf("build archive member %s is over the size limit", name)
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("the build context holds %s (%s), which a build cannot read: only committed regular files reach the builder", name, archiveEntryName(hdr.Typeflag))
		}
	}
}

func archiveEntryName(typeflag byte) string {
	switch typeflag {
	case tar.TypeSymlink:
		return "a symlink"
	case tar.TypeLink:
		return "a hard link"
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		return "a device"
	default:
		return fmt.Sprintf("an entry of type %q", string(typeflag))
	}
}

// CommitFile reads one file out of one commit. It is how the Dockerfile the person committed is read
// rather than the one on disk, and it bounds the read before the bytes are kept.
func CommitFile(ctx context.Context, repo, commit, rel string) ([]byte, error) {
	if err := validateRepoRelative(rel); err != nil {
		return nil, err
	}
	cmd := git.CommandContext(ctx, repo, "show", commit+":"+rel)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w: %s", commit, rel, err, lastLine(stderr.String()))
	}
	if stdout.Len() > buildDockerfileMaxBytes {
		return nil, fmt.Errorf("%s is over the %d KB limit for a Dockerfile", rel, buildDockerfileMaxBytes/1024)
	}
	return stdout.Bytes(), nil
}

// validateGitObjectID accepts the hex object id `rev-parse` prints, and refuses text that would reach
// `git archive` as an argument of its own.
func validateGitObjectID(ref string) error {
	if ref == "" {
		return fmt.Errorf("empty")
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("%q looks like an option", ref)
	}
	for _, r := range ref {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return fmt.Errorf("%q is not a hex object id", ref)
		}
	}
	switch len(ref) {
	case 40, 64:
		return nil
	default:
		return fmt.Errorf("%q is %d characters, not an object id", ref, len(ref))
	}
}
