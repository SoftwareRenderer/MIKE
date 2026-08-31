package server

import (
	"io/fs"
	"net/http"
	"os"
	"strings"
)

func (s *Server) setupRoutes() {
	s.mux = http.NewServeMux()

	// Health & auth
	s.mux.HandleFunc("/api/health", s.handleHealth)
	// Rate limit login attempts (5/min per IP by default; MIKE_LOGIN_RATE_LIMIT can
	// raise or lower it, but no longer turn it off).
	s.mux.Handle("/api/auth/login", RateLimitMiddleware(s.loginLimiter, http.HandlerFunc(s.handleAuthLogin)))
	s.mux.HandleFunc("/api/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("/api/auth/session", s.handleAuthSession)

	// Session management (signed-in user only — no public path, so
	// SessionMiddleware auths these and CSRFMiddleware guards the non-GET ones).
	s.mux.HandleFunc("GET /api/sessions", s.handleSessionsList)
	s.mux.HandleFunc("POST /api/sessions/revoke-others", s.handleSessionsRevokeOthers)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleSessionRevoke)
	s.mux.HandleFunc("/api/prompts/reload", s.handleReloadPrompts)

	// Config
	s.mux.HandleFunc("/api/config/test-connection", s.configStore.HandleTestConnection)
	s.mux.HandleFunc("/api/config/fetch-models", s.handleFetchModels)
	s.mux.HandleFunc("/api/config", s.configStore.HandleConfig)

	// WebSocket stream
	s.mux.HandleFunc("/api/stream", s.appStreamMgr.HandleStream)

	// LSP WebSocket
	s.mux.HandleFunc("/api/lsp", s.lspHandler.HandleLSP)
	// LSP sandbox memory stats (for the editor LSP status tooltip)
	s.mux.HandleFunc("/api/lsp/memory", s.lspHandler.HandleSandboxMemory)

	// HTML/SVG preview documents (served to sandboxed preview iframes)
	s.mux.HandleFunc("POST /api/preview", s.HandlePreviewServe)
	s.mux.HandleFunc("GET /api/preview/{token}", s.HandlePreviewGet)
	s.mux.HandleFunc("GET /api/preview/{token}/{path...}", s.HandlePreviewGet)
	// Watchdog nav state (session-authed; the exact route wins over {path...})
	s.mux.HandleFunc("GET /api/preview/{token}/nav", s.HandlePreviewNav)
	s.mux.HandleFunc("DELETE /api/preview/{token}", s.HandlePreviewDelete)

	// Extensions
	s.mux.HandleFunc("GET /api/extensions", s.extHandler.HandleList)
	s.mux.HandleFunc("/api/extensions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		switch r.Method {
		case http.MethodGet:
			s.extHandler.HandleGet(w, r, id)
		case http.MethodPut, http.MethodPatch:
			s.extHandler.HandleUpdate(w, r, id)
		case http.MethodDelete:
			s.extHandler.HandleDelete(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	s.mux.HandleFunc("/api/extensions/{id}/enabled", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleSetEnabled(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("GET /api/extensions/registry", s.extHandler.HandleRegistryIndex)
	s.mux.HandleFunc("GET /api/extensions/local", s.extHandler.HandleLocalExtensions)
	s.mux.HandleFunc("POST /api/extensions/install", s.extHandler.HandleInstall)
	s.mux.HandleFunc("POST /api/extensions/install-local", s.extHandler.HandleInstallLocal)
	s.mux.HandleFunc("GET /api/extensions/check-updates", s.extHandler.HandleCheckUpdates)
	s.mux.HandleFunc("GET /api/extensions/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleSandboxLogs(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("POST /api/extensions/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleUpdateImage(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("POST /api/extensions/{id}/proxy", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleProxy(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("/api/extensions/{id}/storage", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleStorage(w, r, r.PathValue("id"))
	})

	// Global utilities
	s.mux.HandleFunc("/api/home", s.handleHome)
	s.mux.HandleFunc("/api/fs/read", s.handleFSRead)
	s.mux.HandleFunc("/api/fs/write", s.handleFSWrite)
	s.mux.HandleFunc("/api/fs/init-git", s.handleFSInitGit)
	s.mux.HandleFunc("/api/fs/mkdir", s.handleFSMkdir)
	s.mux.HandleFunc("/api/fs/search", s.handleFSSearch)
	s.mux.HandleFunc("/api/fs", s.handleFS)

	// Projects
	s.mux.HandleFunc("GET /api/projects", s.projectHandler.HandleList)
	s.mux.HandleFunc("POST /api/projects", s.projectHandler.HandleCreate)
	s.mux.HandleFunc("POST /api/projects/clone", s.projectHandler.HandleClone)
	s.mux.HandleFunc("GET /api/projects/counts", s.projectHandler.HandleCounts)
	s.mux.HandleFunc("/api/projects/{projectID}", s.projectHandler.HandleByID)
	s.mux.HandleFunc("/api/projects/{projectID}/board", s.boardHandler.HandleBoard)
	s.mux.HandleFunc("/api/projects/{projectID}/stats", s.projectHandler.HandleStats)
	s.mux.HandleFunc("/api/projects/{projectID}/sandbox/build", s.projectHandler.HandleSandboxBuild)
	s.mux.HandleFunc("/api/projects/{projectID}/terminal", s.terminalMgr.HandleProjectTerminal)

	// Tasks
	s.mux.HandleFunc("/api/projects/{projectID}/tasks", s.taskHandler.HandleList)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/done", s.taskHandler.HandleClearDone)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}", s.taskHandler.HandleByID)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/move", s.taskHandler.HandleMove)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/init", s.taskHandler.HandleInit)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/start", s.taskHandler.HandleStart)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/start-subtask", s.taskHandler.HandleStartSubtask)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/stop", s.taskHandler.HandleStop)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/retry", s.executor.HandleRetry)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/resume", s.executor.HandleResume)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/send", s.executor.HandleSend)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/summarize", s.executor.HandleSummarize)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/compact", s.executor.HandleCompact)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff", s.gitHandler.HandleTaskDiff)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", s.gitHandler.HandleTaskDiffFile)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/diff/stage", s.gitHandler.HandleStageFile)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/commits", s.gitHandler.HandleTaskCommits)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/reset-worktree", s.gitHandler.HandleResetWorktree)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/commit-preview", s.taskHandler.HandleCommitPreview)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/commit-preview/stream", s.taskHandler.HandleCommitPreviewStream)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/commit", s.taskHandler.HandleCommitAndDone)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/terminal", s.terminalMgr.HandleTerminal)

	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/messages", s.handleTaskMessages)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/messages/{messageID}", s.handleTaskMessageByID)

	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/threads", s.handleThreads)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/threads/fork", s.handleThreadFork)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/threads/switch", s.handleThreadSwitch)

	// Git operations
	s.mux.HandleFunc("/api/projects/{projectID}/git/log", s.gitHandler.HandleLog)
	s.mux.HandleFunc("/api/projects/{projectID}/git/branch", s.gitHandler.HandleBranch)
	s.mux.HandleFunc("/api/projects/{projectID}/git/branches", s.gitHandler.HandleBranches)
	s.mux.HandleFunc("GET /api/projects/{projectID}/git/diff", s.gitHandler.HandleDiff)
	s.mux.HandleFunc("GET /api/projects/{projectID}/git/diff/file", s.gitHandler.HandleProjectDiffFile)
	s.mux.HandleFunc("/api/projects/{projectID}/git/status", s.gitHandler.HandleStatus)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/stage", s.gitHandler.HandleProjectStageFile)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/commit", s.gitHandler.HandleProjectCommit)
	s.mux.HandleFunc("/api/projects/{projectID}/git/checkout", s.gitHandler.HandleCheckout)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/pull", s.gitHandler.HandlePull)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/push", s.gitHandler.HandlePush)

	// Task-specific git operations
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/git/log", s.gitHandler.HandleTaskLog)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/git/branches", s.gitHandler.HandleTaskBranches)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/git/checkout", s.gitHandler.HandleTaskCheckout)

	// Frontend
	if s.frontendDist != "" {
		if _, err := os.Stat(s.frontendDist); err == nil {
			sub, err := fs.Sub(os.DirFS(s.frontendDist), ".")
			if err == nil {
				s.mux.Handle("/", s.withNonce(http.FileServer(noWriterToFS{http.FS(sub)})))
			}
		}
	}

	// Extensions static files — FILES ONLY. This prefix is public (see
	// isPublicPath: sandboxed extension iframes have an opaque origin and cannot
	// send session cookies), so it stays minimal. A raw http.FileServer would
	// render a directory index for /extensions/ and /extensions/<author>/,
	// handing the installed-extension tree to any unauthenticated client;
	// noListingFS returns 404 for those instead. http.Dir still rejects traversal.
	s.mux.Handle("/extensions/", http.StripPrefix("/extensions/", http.FileServer(noListingFS{http.Dir("extensions")})))
}

// noListingFS wraps an http.FileSystem so http.FileServer can never render a
// directory index. A directory without an index.html (the extensions root, author
// dirs, asset dirs) opens as fs.ErrNotExist, which FileServer answers as 404, so
// the tree structure stays hidden. Directories that do hold an index.html open
// normally, so the extension entry contract still works (the iframe loads
// /extensions/<author>/<name>/). Traversal protection is untouched — it lives in
// the wrapped FileSystem.
type noListingFS struct {
	http.FileSystem
}

func (n noListingFS) Open(name string) (http.File, error) {
	f, err := n.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		index, err := n.FileSystem.Open(strings.TrimSuffix(name, "/") + "/index.html")
		if err != nil {
			f.Close()
			return nil, fs.ErrNotExist
		}
		index.Close()
	}
	return f, nil
}
