package exec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kanban/backend/errs"
	"kanban/backend/fsutil"
	"kanban/pkg/bridge/protocol"
	"kanban/pkg/logging"
)

// A pasted or dropped file that is neither an image nor text lands in the environment where this
// task's actions run, and the message names that path. Out of the worktree: a file the agent did
// not create would show up in `git status` and land in a `git add -A`.
//
// Each upload gets a directory of its own. Two gestures can carry the name `report.pdf` and the
// message that named the first must keep naming the bytes it was written for; the host cannot see
// the guest's directory, so "is that name taken?" has no answer without a second round trip.

// guestUploadsRoot is the uploads root inside a task's sandbox container.
const guestUploadsRoot = "/tmp/kanban-uploads"

// hostUploadsPrefix names the per-task uploads directory when the task runs on the host. The
// directory comes from os.MkdirTemp rather than being assembled: /tmp is shared, and a fixed name
// there can be claimed by someone else first.
const hostUploadsPrefix = "kanban-uploads"

// Read from crypto/rand rather than counted, for the same reason: a name another process can guess
// is a name it can own first.
const uploadDirIDBytes = 4

// writeTaskFileTimeout bounds one upload's write. The bridge client carries no deadline of its own
// (see bridge.lspCallCtx, the one call that does), so a guest that stopped reading would otherwise
// park this request handler for as long as its container lives.
const writeTaskFileTimeout = 60 * time.Second

// TaskUploadsMaxBytes is the bridge's own limit, so the number a user is refused for is the one
// that would have been refused anyway.
const TaskUploadsMaxBytes = protocol.MaxWriteBytes

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

func uploadDirID() (string, error) {
	var b [uploadDirIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("name the upload directory: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// WriteTaskFile writes one pasted or dropped file into the environment where this task's actions
// run, and answers with the path the agent opens it at.
//
// A sandboxed task is reached through the bridge connection its own write_file tool uses. That
// connection exists only while its container is up, and bringing one up is not a side effect this
// call may take: preparing an execution environment creates the worktree and its branch and
// rewrites the task row. The refusal when no container is running is therefore the honest answer
// rather than a fallback to the host, which would name a path the agent cannot see (see
// exec.hostGuard).
func (te *TaskExecutor) WriteTaskFile(ctx context.Context, projectID, taskID, rawName string, data []byte) (string, error) {
	name, err := UploadName(rawName)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("uploaded file is empty")
	}
	if len(data) > TaskUploadsMaxBytes {
		return "", fmt.Errorf("file is %d bytes, the limit is %d", len(data), TaskUploadsMaxBytes)
	}

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return "", err
	}
	project, err := te.store.GetProject(projectID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, writeTaskFileTimeout)
	defer cancel()

	if project.SandboxEnabled {
		return te.writeSandboxedUpload(ctx, task.ID, name, data)
	}
	return te.writeHostUpload(task.ID, name, data)
}

func (te *TaskExecutor) writeSandboxedUpload(ctx context.Context, taskID, name string, data []byte) (string, error) {
	if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
		return "", errs.Conflict("sandboxing is not available on this host")
	}
	client, err := te.sandboxMgr.GetClient(taskID)
	if err != nil {
		return "", errs.Conflict("the task's sandbox is not running — start the task, then attach the file again")
	}
	id, err := uploadDirID()
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/%s/%s/%s", guestUploadsRoot, taskID, id, name)
	if _, err := client.WriteFileBytes(ctx, path, data); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// writeHostUpload: for an unsandboxed task the host *is* the environment its actions run in.
func (te *TaskExecutor) writeHostUpload(taskID, name string, data []byte) (string, error) {
	dir, err := os.MkdirTemp(os.TempDir(), hostUploadsPrefix+"-"+taskID+"-")
	if err != nil {
		return "", fmt.Errorf("create the task's upload directory: %w", err)
	}
	target := filepath.Join(dir, name)
	// One person's clipboard, into a directory on a shared machine.
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", target, err)
	}
	return target, nil
}

// removeTaskUploads drops what a deleted task left behind. The sandboxed half needs nothing — the
// guest directory dies with its container — so this is the host's, found by the task's own prefix
// rather than by a map this process remembers: a directory written before a restart is still that
// task's.
func (te *TaskExecutor) removeTaskUploads(taskID string) {
	// Task IDs come out of the store, which makes this a check and not a jail.
	if taskID == "" || strings.ContainsAny(taskID, `*?[\`) {
		return
	}
	dirs, err := filepath.Glob(filepath.Join(os.TempDir(), hostUploadsPrefix+"-"+taskID+"-*"))
	if err != nil {
		return
	}
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			// Not worth failing a deletion over: it is 0700 and the host's tmp policy takes it.
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("upload cleanup failed: %v", err)
		}
	}
}
