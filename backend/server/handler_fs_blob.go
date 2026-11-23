package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"mike/backend/git"
	"mike/backend/types"
	"mike/pkg/logging"
)

// repoRelativePath turns a repository-relative path into a filesystem one. An absolute path is refused:
// every file route jails a name relative to a root the server chose, never one the request chose.
func repoRelativePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if strings.HasPrefix(p, "/") || looksLikeDrivePath(p) {
		return "", errors.New("path must be relative to the project root")
	}
	clean := path.Clean(filepath.ToSlash(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path must stay inside the project")
	}
	return filepath.FromSlash(clean), nil
}

// looksLikeDrivePath detects a Windows-style absolute path on a non-Windows platform, where no other
// check would notice it.
func looksLikeDrivePath(p string) bool {
	return len(p) > 2 && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// refWhere answers ref as the object the named tree means by it. HEAD belongs to a worktree, so a request
// naming a task resolves it there: the checkout's HEAD is another commit, and the object under it is a
// different file or none. An empty tree, or a ref the tree will not name, keeps the ref as it came.
func refWhere(ctx context.Context, tree, ref string) string {
	if tree == "" {
		return ref
	}
	out, err := gitInDir(ctx, tree)("rev-parse", "--verify", "--end-of-options", ref)
	if err != nil {
		logging.For(logging.HTTP).Debugf("ref %q not resolved in %s: %v", ref, tree, err)
		return ref
	}
	object := strings.TrimSpace(out)
	if err := types.ValidateCommitHash(object); err != nil {
		logging.For(logging.HTTP).Debugf("ref %q named %q in %s, which is no object id", ref, object, tree)
		return ref
	}
	return object
}

// gitInDir runs git in dir and returns a function capturing its stdout.
func gitInDir(ctx context.Context, dir string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		cmd := git.CommandContext(ctx, dir, args...)
		var out, errOut cappedBuffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%v: %s", err, errOut.String())
		}
		return out.String(), nil
	}
}

// fsBlobSize asks the object database in dir how long the object spec names. ok is false when git cannot
// answer, and a caller that has no length then has to hold the bytes to refuse them.
func fsBlobSize(ctx context.Context, dir, spec string) (int64, bool) {
	return gitBlobSize(gitInDir(ctx, dir), spec)
}

// blobHint turns a size probe's answer into what readSized wants: a length to size the buffer by, or -1
// when git could not name one.
func blobHint(size int64, ok bool) int64 {
	if !ok {
		return -1
	}
	return size
}

// gitBlobSpool reads an object into memory under the caller's ceiling: truncated is true when the blob ran
// past it, and the process is stopped rather than drained — draining is the read the answer just refused.
// sizeHint sizes the buffer; see blobHint. A caller that can answer with the bytes themselves streams
// through gitBlobStream instead.
func gitBlobSpool(ctx context.Context, dir, spec string, limit, sizeHint int64) ([]byte, bool, error) {
	body, wait, err := gitBlobStream(ctx, dir, spec)
	if err != nil {
		return nil, false, err
	}
	data, readErr := readSized(body, sizeHint, limit)
	truncated := int64(len(data)) > limit
	waitErr := wait()
	if truncated {
		return nil, true, nil
	}
	// git's own refusal is what names an object that is not there, so it is the caller's 404 rather than
	// an empty body answered as a file.
	if waitErr != nil {
		return nil, false, waitErr
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, readErr
	}
	return data, false, nil
}

// gitBlobStream starts `git cat-file blob` on one object and hands back its stdout. The returned wait must
// be called on every path; it is also what stops a stream nobody drained, since an undrained pipe keeps the
// process alive.
func gitBlobStream(ctx context.Context, dir, spec string) (io.ReadCloser, func() error, error) {
	// --end-of-options (git >= 2.24) is a second line of defence: everything after it is a revision,
	// never an option, and spec carries a ref the request named.
	cmd := git.CommandContext(ctx, dir, "cat-file", "blob", "--end-of-options", spec)
	var errOut cappedBuffer
	cmd.Stderr = &errOut
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	wait := func() error {
		// Closing the read end is what stops a stream nobody drained: git takes the EPIPE and exits, which
		// is the only way Wait returns for a caller that read past (or never reached) the object's length.
		_ = pipe.Close()
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("%v: %s", err, errOut.String())
		}
		return nil
	}
	return pipe, wait, nil
}

// cappedBuffer captures only the first few kilobytes of output to prevent memory
// exhaustion from failing subprocesses.
type cappedBuffer struct {
	buf bytes.Buffer
}

const cappedKeepBytes = 4 << 10

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := cappedKeepBytes - c.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		_, _ = c.buf.Write(p)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
