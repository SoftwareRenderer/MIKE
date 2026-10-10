package exec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mike/backend/errs"
	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/sandbox"
)

// A pasted or dropped file that is neither an image nor text lands in the environment where this task's
// actions run, and the message names that path. That environment is the task's worktree: a sandboxed task
// reaches it through the bind mount at `/home/sandbox/<project.Name>`, an unsandboxed one walks it directly.
//
// The directory is excluded through the repository's local `.git/info/exclude`, and that line is what makes
// the placement safe: without it the upload is untracked in git's eyes, and the next `git add -A`,
// `git stash -u` or the `git clean -fd` inside a checkpoint restore decides what happens to it.
//
// It goes away with the worktree, which goes away with the task and with nothing else. Stopping, pausing,
// marking done, switching model or mode and restarting the server all leave the file where the message says
// it is.
//
// An upload keeps the name it was dropped under; a second upload of that name gets `-1`, then `-2`. The
// conflict is settled by looking at the directory, because that is where the bytes land.

// uploadsDirName is where a pasted or dropped file lands inside the task's own worktree. The name is
// MIKE's own namespace so the line in the repo's local `.git/info/exclude` that hides it cannot also hide a
// file the agent or the user made, and it is deleted with the worktree — which is deleted with the task, and
// with nothing else.
const uploadsDirName = ".mike-uploads"

// uploadsExcludeHeader labels the exclude line this package writes, the way the worktrees line is labeled.
const uploadsExcludeHeader = "# mike uploads (kept out of task status)"

// noUploadEnvironmentMessage is answered when the task has no worktree yet. Preparing an environment creates
// the worktree, its branch and the task-row writes that go with them, and that is not this call's side
// effect.
const noUploadEnvironmentMessage = "the task's environment is not prepared — start the task, then attach the file again"

// MaxUploadBytes bounds one upload, and is the one number for it: server.MaxUploadSize is defined from
// here, because the HTTP layer imports this package and not the reverse, so the handler cannot own the size
// this write enforces.
const MaxUploadBytes = 20 << 20

// maxUploadSuffixTries bounds the `-N` search: one stat per try, and a directory holding this many files of
// one name is a mistake rather than a queue.
const maxUploadSuffixTries = 1000

// UploadName is the rule the file-upload endpoint already enforces (fsutil.ValidateRelPath) plus
// one segment, so a drop cannot build a directory tree inside the task's uploads.
func UploadName(raw string) (string, error) {
	if err := fsutil.ValidateRelPath(raw); err != nil {
		return "", err
	}
	if strings.ContainsRune(raw, '/') {
		return "", fmt.Errorf("upload must be one file name, not a path")
	}
	return raw, nil
}

// uploadsDir is the uploads directory of one task's worktree.
func uploadsDir(worktreePath string) string { return filepath.Join(worktreePath, uploadsDirName) }

// ensureUploadsDir makes a worktree's uploads directory. 0700: it holds one person's files inside a
// checkout other people may read.
func ensureUploadsDir(worktreePath string) (string, error) {
	dir := uploadsDir(worktreePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the task's uploads directory: %w", err)
	}
	return dir, nil
}

// writeNewUpload writes an upload to the name firstFreeUploadPath() chose because nothing stood there.
// O_EXCL is the point: the task owns the directory, and a plain open would follow a link planted in the
// gap between the scan and this write.
func writeNewUpload(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// uploadStemExt splits a name for the `-N` suffix. filepath.Ext answers a dotfile's whole name as its
// extension, which would leave no stem to carry the suffix, so such a name keeps all of it as the stem:
// `.env` becomes `.env-1`.
func uploadStemExt(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	if ext == name {
		return name, ""
	}
	return strings.TrimSuffix(name, ext), ext
}

// firstFreeUploadPath picks the name one upload gets in dir: `report.pdf`, then `report-1.pdf`, … The
// conflict is settled against the directory the bytes land in. name must have come from UploadName.
func firstFreeUploadPath(dir, name string) (string, error) {
	stem, ext := uploadStemExt(name)
	for try := range maxUploadSuffixTries {
		candidate := name
		if try > 0 {
			candidate = stem + "-" + strconv.Itoa(try) + ext
		}
		path := filepath.Join(dir, candidate)
		// Lstat, not Stat: a symlink holding the name is the name being taken, and following it would
		// write through the jail to whatever it points at.
		switch _, err := os.Lstat(path); {
		case errors.Is(err, os.ErrNotExist):
			return path, nil
		case err != nil:
			// A directory this cannot search is not a directory with a free name.
			return "", fmt.Errorf("check %s: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("no free name for %q in %d tries", name, maxUploadSuffixTries)
}

// WriteTaskFile writes one pasted or dropped file into the environment where this task's actions run, and
// answers with the path the agent opens it at.
//
// The write is host-side even for a sandboxed task: the bytes land in the worktree and the guest reaches them
// through the bind mount. So no container has to be running for a file to be attached — a paused or idle
// task accepts one — and a plan-mode task's read-only mount does not refuse it. What must exist is the
// worktree; see noUploadEnvironmentMessage for what happens when it does not.
//
// A sandboxed task is answered with the path its own environment reads, not the host's.
func (te *TaskExecutor) WriteTaskFile(projectID, taskID, rawName string, data []byte) (string, error) {
	name, err := UploadName(rawName)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("uploaded file is empty")
	}
	if len(data) > MaxUploadBytes {
		return "", fmt.Errorf("file is %d bytes, the limit is %d", len(data), MaxUploadBytes)
	}

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return "", err
	}
	project, err := te.store.GetProject(projectID)
	if err != nil {
		return "", err
	}

	worktreePath, err := te.taskWorktreePath(task.ID)
	if err != nil {
		return "", err
	}

	// The exclude line goes in before the bytes: an upload that lands unprotected can be discarded by a
	// checkpoint restore, which is worse than refusing the attachment.
	if err := git.EnsureExcluded(project.Path, uploadsDirName+"/", uploadsExcludeHeader); err != nil {
		return "", errs.Operation("keep the task's uploads out of git", err)
	}

	dir, err := ensureUploadsDir(worktreePath)
	if err != nil {
		return "", errs.Operation("create the task's uploads directory", err)
	}
	path, err := firstFreeUploadPath(dir, name)
	if err != nil {
		return "", errs.Operation("name the uploaded file", err)
	}
	// The jail runs before the write. The task's own actions can replace `.mike-uploads` with a symlink,
	// and the bytes must not follow it out of the worktree.
	safe, err := fsutil.Contained(path, []string{worktreePath})
	if err != nil {
		return "", errs.Operation("keep the uploaded file inside the task's worktree", err)
	}
	if err := writeNewUpload(safe, data); err != nil {
		return "", errs.Operation("write the uploaded file", err)
	}

	if !project.SandboxEnabled {
		return safe, nil
	}
	return filepath.Join(sandbox.MountPath(project.Name), uploadsDirName, filepath.Base(safe)), nil
}

// taskWorktreePath answers where one task's actions run, refusing when the environment is not prepared:
// WorktreePath builds the path from the store and the workspace dir, so the directory itself is the answer
// to whether it exists.
func (te *TaskExecutor) taskWorktreePath(taskID string) (string, error) {
	if te.worktreeMgr == nil {
		return "", errs.Conflict(noUploadEnvironmentMessage)
	}
	worktreePath, err := te.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		if errs.IsNotFound(err) {
			return "", err // a task that is gone is the caller's answer, not a server fault
		}
		return "", errs.Operation("find the task's worktree", err)
	}
	if info, statErr := os.Stat(worktreePath); statErr != nil || !info.IsDir() {
		return "", errs.Conflict(noUploadEnvironmentMessage)
	}
	return worktreePath, nil
}
