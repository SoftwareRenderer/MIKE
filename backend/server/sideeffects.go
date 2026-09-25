package server

// Decision 6, written down as data instead of prose in three places.
//
// Cancellation is decided by **side effect, not entry point**. The question is "does this leave server
// state behind?", not "who called it?" — because the answer that matters when a browser tab closes is
// whether the next person to open the board sees a whole result or half of one.
//
// Three sets, and a path in none of them is **unclassified**, which is a bug rather than an absence of
// work: TestSideEffectClassification covers what the route table contains, so a new state-changing route
// has to appear here before it can be merged.
//
//   - detachedOperations persist a result. They run on Server.Detached() and finish, whatever the browser
//     does. An abort mid-way is the intermittent wrong-screen bug this repo already documents.
//   - requestScopedReads serve a response and nothing else. Their context is the request's; a cancel
//     leaves no trace, which is why it is silent (Decision 8: silent only when the user actually left).
//   - idempotentWritesOnReads is the third clause F19 forced: a read that writes is safe on a cancellable
//     path only while **every** write on it is one idempotent statement. Add a second write to one of
//     these handlers — a summary row beside the snapshot — and cancellation starts producing half-written
//     state again. That is what archgate's writeselect rule watches for; this list is the reason it
//     watches.

// sideEffectEntry is one classified path: the route as it is registered (or the middleware, for a path
// that is not a route), and why it sits where it does.
type sideEffectEntry struct {
	Path string
	Why  string
}

// detachedOperations change server state, so they outlive the request that started them.
var detachedOperations = []sideEffectEntry{
	{"POST /api/projects/{projectID}/tasks/{taskID}/start", "starts a task; its turns write messages, status and worktree contents"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/start-subtask", "same, and the parent's started-but-pending state depends on the child finishing"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/resume", "restarts a turn on the existing conversation tree; a half-applied restart is a task stuck in running with no executor"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/stop", "the transition itself is the result: a stopped task must not be left reading as running"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/commit", "commits, merges and marks done; nothing may be half-merged"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/threads/fork", "creates a branch and a checkpoint set"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/threads/switch", "restores the target thread's checkpoint into the worktree"},
	{"DELETE /api/projects/{projectID}/tasks/{taskID}/threads", "deletes the thread's checkpoints and its branch"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/diff/stage", "writes the worktree's git index"},
	{"POST /api/projects/{projectID}/git/stage", "writes the git index"},
	{"POST /api/projects/{projectID}/git/commit", "creates a commit"},
	{
		"POST /api/projects/{projectID}/git/discard",
		"reverts one file to HEAD, or removes it: a stop between unstaging and removing leaves the file on " +
			"disk with a cleared index, and a discarded file is not recoverable",
	},
	{"POST /api/projects/{projectID}/git/checkout", "moves HEAD and rewrites the working tree"},
	{"POST /api/projects/{projectID}/git/pull", "fetches and merges; a partial fetch is what the stash bracket exists to survive"},
	{"POST /api/projects/{projectID}/git/push", "publishes commits to the remote, which no later cancel can undo"},
	{"POST /api/extensions/install", "checks out, builds an image and writes the row; install is atomic by design"},
	{"POST /api/extensions/install-local", "materializes the folder, then the row — in that order, so a folder with no row is listed as installable"},
	{"POST /api/extensions/{id}/update", "moves the checkout to a new commit and may rebuild the image"},
	{"POST /api/fs/write", "writes a file"},
	{"POST /api/fs/upload", "writes a file"},
	{"POST /api/fs/mkdir", "creates a directory"},
	{"POST /api/fs/init-git", "creates a repository"},
	{
		"POST /api/fs/delete",
		"removes files from disk. A cancel between two paths of a selection leaves a half-deleted batch " +
			"that the panel no longer lists",
	},
	{"POST /api/fs/copy", "writes files into the destination folder"},
	{
		"POST /api/fs/move",
		"writes the destination and removes the source: half a move is a file in neither place",
	},
	{"POST /api/fs/rename", "renames a path an editor may have open on it"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/init", "creates the worktree and the task branch"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/messages", "persists the message, then the turn's answer arrives beside it"},
	{
		"POST /api/projects/{projectID}/tasks/{taskID}/uploads",
		"writes into the task's environment; the guest is not cancellable, so the write completes " +
			"whatever the tab does",
	},
	{"POST /api/projects/{projectID}/tasks/{taskID}/send", "a turn: messages, tool calls and tool results are written as they happen"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/compact", "rewrites the conversation with a summary; half a compaction loses turns"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/summarize", "the title and summary are stored on the task and shown on the board"},
	{"POST /api/projects/{projectID}/tasks/{taskID}/move", "status and parent are the board's content"},
	{
		"POST /api/projects/{projectID}/tasks/{taskID}/memory-alert-ack",
		"the acknowledgement is a column on the row: every device reads the alert's visibility off it, so a " +
			"dismissal that stopped half-way is a bar back up on the other device",
	},
	{
		"DELETE /api/projects/{projectID}/tasks/{taskID}/messages",
		"deletes messages and resets the worktree to the stored commit hash — a half-run leaves a tree that does not match the thread",
	},
	{
		"PATCH /api/projects/{projectID}/tasks/{taskID}/messages/{messageID}",
		"rewrites a message and may store the commit hash the deletion path later restores to",
	},
	{"PUT /api/extensions/{id}", "persists the manifest edit, and validates its sandbox against the policy before storing"},
	{"PATCH /api/extensions/{id}/enabled", "the enabled flag decides whether the extension runs at all"},
	{"PUT /api/extensions/{id}/dependency-cache", "grants or revokes a host directory and stops the sandbox so the mount set is right"},
	{"PUT /api/extensions/{id}/agent-mount", "sets or clears the host folders that reach the containers of the tasks running this agent"},
	{"PUT /api/extensions/{id}/storage", "writes the namespaced KV store an extension reads back"},
	{"DELETE /api/extensions/{id}/storage", "clears it"},
	{
		"DELETE /api/extensions/{id}",
		"deletes the row, then the directory. A half-run is the bug this repo records twice: a row with files still " +
			"present is a deleted extension that keeps executing, a directory with no row is listed as installable",
	},
	{
		"POST /api/tokens",
		"writes the token row. The plaintext is in the response only, so an aborted request must still be decided " +
			"one way: a row exists and the user regenerates, or no row exists — never a token whose caller cannot tell",
	},
	{
		"POST /api/updates/prepare",
		"downloads and verifies the release into this install's own directory. It is a download a person may " +
			"be waiting on while the tab that started it navigates away, so it finishes and leaves one archive " +
			"or none",
	},
	{
		"POST /api/updates/apply",
		"replaces the files in this install directory. Half a swap is a server that does not start, so the " +
			"copy pass and the renames run on the detached context and finish whatever the tab does",
	},
	{
		"POST /api/updates/restart",
		"ends this process to apply a staged release. There is no half of a restart to be left holding",
	},
	{
		"DELETE /api/tokens/{id}",
		"revocation is the row's absence: a cancel that left it standing leaves a credential live that the user " +
			"believes is gone",
	},
	{
		"POST /api/auth/login",
		"creates the session row. A tab closing halfway through a login must not decide whether the user is " +
			"signed in, and the answer the response gives has to match the row that exists",
	},
	{
		"POST /api/auth/logout",
		"deletes the session row: a delete that does not land leaves the session live, which is the opposite " +
			"of what was asked for",
	},
}

// requestScopedReads serve a response and leave nothing behind. Their cancellation is expected — a
// navigated-away panel, a closed editor — and must never be reported as a fault.
var requestScopedReads = []sideEffectEntry{
	{"GET /api/auth/session", "reads one session row; a cancel leaves nothing"},
	{"GET /api/fs", "a directory listing; the panel refetches"},
	{"GET /api/fs/read", "one file's content, capped before it is read"},
	{"GET /api/fs/search", "a search result set"},
	{"GET /api/projects/{projectID}/git/status", "a snapshot of the working tree"},
	{"GET /api/projects/{projectID}/git/log", "commits already made"},
	{"GET /api/projects/{projectID}/git/branches", "refs already made"},
	{"GET /api/projects/{projectID}/git/diff", "a diff against HEAD"},
	{"GET /api/projects/{projectID}/git/diff/file", "one file's diff, size-capped"},
	{"GET /api/projects/{projectID}/tasks/{taskID}/commits", "the task's own commit list"},
	{"GET /api/projects/{projectID}/tasks/{taskID}/diff/file", "one file of the task diff, re-read from the object database"},
	{"GET /api/preview/{token}", "the preview document itself"},
	{"GET /api/preview/{token}/{path...}", "a preview asset, served from the resolved worktree"},
	{"GET /api/preview/{token}/nav", "the preview's navigation record"},
	{
		"POST /api/updates/check",
		"a fetch of the release manifest that caches its answer as one config row. The verb looks like a " +
			"mutation and is one: the caller asked whether anything is newer, and a cancel leaves the previous " +
			"row standing, which is the same answer as no check at all",
	},
	{
		"POST /api/extensions/{id}/proxy",
		"a fetch through the manifest's domain allowlist. A verb that looks like a mutation and is one: the " +
			"caller asked for bytes, nothing is persisted, and a cancel leaves nothing to explain",
	},
	{
		"GET /api/updates/status",
		"the cached check result and this install's shape; it performs no egress",
	},
	{
		"POST /api/extensions/{id}/test-url",
		"one probe of an address a person is about to save. It sends a request and writes nothing: the " +
			"result belongs to the dialog that asked, and a cancel mid-flight leaves no answer behind, which is " +
			"the same as no answer",
	},
}

// idempotentWritesOnReads is the exception F19 found, and the reason it is an exception rather than a
// contradiction. Every entry is one statement that SQLite commits whole or not at all.
var idempotentWritesOnReads = []sideEffectEntry{
	{
		Path: "GET /api/projects/{projectID}/tasks/{taskID}/diff",
		Why: "materialises the done-task diff snapshot: one INSERT … ON CONFLICT(task_id) DO UPDATE, plus one " +
			"sync.Map store for the in-memory copy. A cancel leaves the old row or the new one, never a body " +
			"without its parsed_files — which is the shape that renders \"No changes since last commit\" over a " +
			"diff the server still holds",
	},
	{
		// Not a route: session middleware, so it runs on every authenticated request including a GET.
		Path: "middleware: session last_seen",
		Why: "one throttled UPDATE of a single column (60s), which is why the throttle exists: an unthrottled " +
			"write would make every request a write on a path this table calls cancellable",
	},
}
