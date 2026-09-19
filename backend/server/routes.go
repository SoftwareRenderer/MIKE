package server

import (
	"io/fs"
	"mike/backend/fsutil"
	"net/http"
	"path/filepath"
	"strings"
)

func (s *Server) setupRoutes() {
	s.mux = http.NewServeMux()

	// Health & auth
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)

	// Server release updates (docs/auto-update.md): what is known, a check on demand, then the two
	// separate clicks that change anything. Session + CSRF, and out of tokenScope: a bearer token must
	// not be able to replace this binary.
	s.mux.HandleFunc("GET /api/updates/status", s.handleUpdateStatus)
	s.mux.HandleFunc("POST /api/updates/check", s.handleUpdateCheck)
	s.mux.HandleFunc("POST /api/updates/prepare", s.handleUpdatePrepare)
	s.mux.HandleFunc("POST /api/updates/apply", s.handleUpdateApply)
	s.mux.HandleFunc("POST /api/updates/restart", s.handleUpdateRestart)
	// Rate limit login attempts (5/min per IP by default; MIKE_LOGIN_RATE_LIMIT can
	// raise or lower it, but no longer turn it off).
	s.mux.Handle("POST /api/auth/login", RateLimitMiddleware(s.loginLimiter, s.settings.TrustProxy, http.HandlerFunc(s.handleAuthLogin)))
	s.mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("GET /api/auth/session", s.handleAuthSession)

	// Session management (signed-in user only — no public path, so
	// SessionMiddleware auths these and CSRFMiddleware guards the non-GET ones).
	s.mux.HandleFunc("GET /api/sessions", s.handleSessionsList)
	s.mux.HandleFunc("POST /api/sessions/revoke-others", s.handleSessionsRevokeOthers)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleSessionRevoke)

	// API tokens for headless clients (same rule as the session routes: session-authed, CSRF on the
	// writes, never public, and never on tokenScope — a token cannot mint or revoke a token).
	s.mux.HandleFunc("GET /api/tokens", s.handleAPITokensList)
	s.mux.HandleFunc("POST /api/tokens", s.handleAPITokenCreate)
	s.mux.HandleFunc("DELETE /api/tokens/{id}", s.handleAPITokenRevoke)

	// The local CA behind the server's default certificate, for Settings → About.
	s.mux.HandleFunc("GET /api/tls/ca", s.HandleTLSCA)

	s.mux.HandleFunc("POST /api/config/test-connection", s.configStore.HandleTestConnection)
	s.mux.HandleFunc("POST /api/config/fetch-models", s.handleFetchModels)
	s.mux.HandleFunc("GET /api/config", s.configStore.HandleConfig)
	s.mux.HandleFunc("PUT /api/config", s.configStore.HandleConfig)

	// WebSocket stream
	s.mux.HandleFunc("GET /api/stream", s.appStreamMgr.HandleStream)

	// LSP WebSocket
	s.mux.HandleFunc("GET /api/lsp", s.lspHandler.HandleLSP)
	// LSP sandbox memory stats (for the editor LSP status tooltip)
	s.mux.HandleFunc("GET /api/lsp/memory", s.lspHandler.HandleSandboxMemory)

	// HTML/SVG preview documents (served to sandboxed preview iframes)
	s.mux.HandleFunc("POST /api/preview", s.HandlePreviewServe)
	s.mux.HandleFunc("GET /api/preview/{token}", s.HandlePreviewGet)
	s.mux.HandleFunc("GET /api/preview/{token}/{path...}", s.HandlePreviewGet)
	// Watchdog nav state (session-authed; the exact route wins over {path...})
	s.mux.HandleFunc("GET /api/preview/{token}/nav", s.HandlePreviewNav)
	s.mux.HandleFunc("DELETE /api/preview/{token}", s.HandlePreviewDelete)

	s.mux.HandleFunc("GET /api/extensions", s.extHandler.HandleList)
	// Each registration names the one handler it reaches: the mux answers the 405, so no closure here
	// inspects r.Method.
	s.mux.HandleFunc("GET /api/extensions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleGet(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("PUT /api/extensions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleUpdate(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("DELETE /api/extensions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleDelete(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("PATCH /api/extensions/{id}/enabled", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleSetEnabled(w, r, r.PathValue("id"))
	})
	// The user's grant of a host directory to one extension's language server, for its resolved
	// dependencies. Separate from PUT /api/extensions/{id} because an empty value must mean
	// "revoke" and because applying it stops the sandbox.
	s.mux.HandleFunc("PUT /api/extensions/{id}/dependency-cache", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleSetDependencyCache(w, r, r.PathValue("id"))
	})
	// Its own endpoint because an empty list has to mean "clear the approval".
	s.mux.HandleFunc("PUT /api/extensions/{id}/agent-mount", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleSetAgentMount(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("GET /api/extensions/registry", s.extHandler.HandleRegistryIndex)
	s.mux.HandleFunc("GET /api/extensions/local", s.extHandler.HandleLocalExtensions)
	s.mux.HandleFunc("POST /api/extensions/install", s.extHandler.HandleInstall)
	s.mux.HandleFunc("POST /api/extensions/install-local", s.extHandler.HandleInstallLocal)
	s.mux.HandleFunc("GET /api/extensions/check-updates", s.extHandler.HandleCheckUpdates)
	s.mux.HandleFunc("POST /api/extensions/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleUpdateImage(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("POST /api/extensions/{id}/proxy", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleProxy(w, r, r.PathValue("id"))
	})
	// Probes the address field of an extension's configuration before it is saved. Its own route because the
	// permission is different: a typed address grants this one call, and the proxy route never gets it.
	s.mux.HandleFunc("POST /api/extensions/{id}/test-url", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleTestURL(w, r, r.PathValue("id"))
	})
	// HandleStorage keeps its own switch: the three verbs share a path and differ only in which
	// namespaced KV operation they run, which is a choice the path cannot express.
	s.mux.HandleFunc("GET /api/extensions/{id}/storage", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleStorage(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("PUT /api/extensions/{id}/storage", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleStorage(w, r, r.PathValue("id"))
	})
	s.mux.HandleFunc("DELETE /api/extensions/{id}/storage", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleStorage(w, r, r.PathValue("id"))
	})

	// Global utilities
	s.mux.HandleFunc("GET /api/home", s.handleHome)
	s.mux.HandleFunc("GET /api/fs/read", s.handleFSRead)
	s.mux.HandleFunc("GET /api/fs/raw", s.handleFSRaw)
	s.mux.HandleFunc("POST /api/fs/write", s.handleFSWrite)
	s.mux.HandleFunc("POST /api/fs/upload", s.handleFSUpload)
	s.mux.HandleFunc("POST /api/fs/init-git", s.handleFSInitGit)
	s.mux.HandleFunc("POST /api/fs/mkdir", s.handleFSMkdir)
	s.mux.HandleFunc("GET /api/fs/search", s.handleFSSearch)
	s.mux.HandleFunc("GET /api/fs", s.handleFS)

	s.mux.HandleFunc("GET /api/projects", s.projectHandler.HandleList)
	s.mux.HandleFunc("POST /api/projects", s.projectHandler.HandleCreate)
	s.mux.HandleFunc("POST /api/projects/clone", s.projectHandler.HandleClone)
	s.mux.HandleFunc("GET /api/projects/counts", s.projectHandler.HandleCounts)
	s.mux.HandleFunc("GET /api/projects/{projectID}", s.projectHandler.HandleByID)
	s.mux.HandleFunc("PUT /api/projects/{projectID}", s.projectHandler.HandleByID)
	s.mux.HandleFunc("DELETE /api/projects/{projectID}", s.projectHandler.HandleByID)
	s.mux.HandleFunc("GET /api/projects/{projectID}/board", s.boardHandler.HandleBoard)
	s.mux.HandleFunc("GET /api/projects/{projectID}/stats", s.projectHandler.HandleStats)
	s.mux.HandleFunc("DELETE /api/projects/{projectID}/stats", s.projectHandler.HandleStats)
	s.mux.HandleFunc("POST /api/projects/{projectID}/sandbox/build", s.projectHandler.HandleSandboxBuild)
	s.mux.HandleFunc("GET /api/projects/{projectID}/terminal", s.terminalMgr.HandleProjectTerminal)

	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks", s.taskHandler.HandleList)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks", s.taskHandler.HandleList)
	s.mux.HandleFunc("DELETE /api/projects/{projectID}/tasks/done", s.taskHandler.HandleClearDone)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}", s.taskHandler.HandleByID)
	s.mux.HandleFunc("PUT /api/projects/{projectID}/tasks/{taskID}", s.taskHandler.HandleByID)
	s.mux.HandleFunc("DELETE /api/projects/{projectID}/tasks/{taskID}", s.taskHandler.HandleByID)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", s.taskHandler.HandleMove)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/init", s.taskHandler.HandleInit)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start", s.taskHandler.HandleStart)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start-subtask", s.taskHandler.HandleStartSubtask)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/stop", s.taskHandler.HandleStop)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/memory-alert-ack", s.taskHandler.HandleDismissMemoryAlert)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/resume", s.executor.HandleRetry)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/send", s.executor.HandleSend)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/summarize", s.executor.HandleSummarize)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/compact", s.executor.HandleCompact)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/uploads", s.handleTaskUpload)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff", s.gitHandler.HandleTaskDiff)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", s.gitHandler.HandleTaskDiffFile)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/diff/stage", s.gitHandler.HandleStageFile)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/commits", s.gitHandler.HandleTaskCommits)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/commit-preview/stream", s.taskHandler.HandleCommitPreviewStream)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/commit", s.taskHandler.HandleCommitAndDone)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/terminal", s.terminalMgr.HandleTerminal)

	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/messages", s.handleTaskMessages)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/messages", s.handleTaskMessages)
	s.mux.HandleFunc("DELETE /api/projects/{projectID}/tasks/{taskID}/messages", s.handleTaskMessages)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/messages/{messageID}", s.handleTaskMessageByID)
	s.mux.HandleFunc("PATCH /api/projects/{projectID}/tasks/{taskID}/messages/{messageID}", s.handleTaskMessageByID)

	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/threads", s.handleThreads)
	s.mux.HandleFunc("DELETE /api/projects/{projectID}/tasks/{taskID}/threads", s.handleThreads)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/threads/fork", s.handleThreadFork)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/threads/switch", s.handleThreadSwitch)

	// Git operations
	s.mux.HandleFunc("GET /api/projects/{projectID}/git/log", s.gitHandler.HandleLog)
	s.mux.HandleFunc("GET /api/projects/{projectID}/git/branches", s.gitHandler.HandleBranches)
	s.mux.HandleFunc("GET /api/projects/{projectID}/git/diff", s.gitHandler.HandleDiff)
	s.mux.HandleFunc("GET /api/projects/{projectID}/git/diff/file", s.gitHandler.HandleProjectDiffFile)
	s.mux.HandleFunc("GET /api/projects/{projectID}/git/status", s.gitHandler.HandleStatus)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/stage", s.gitHandler.HandleProjectStageFile)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/commit", s.gitHandler.HandleProjectCommit)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/checkout", s.gitHandler.HandleCheckout)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/pull", s.gitHandler.HandlePull)
	s.mux.HandleFunc("POST /api/projects/{projectID}/git/push", s.gitHandler.HandlePush)

	// Task-specific git operations
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/git/log", s.gitHandler.HandleTaskLog)

	// Extensions static files — FILES ONLY. This prefix is public (see
	// isPublicPath: sandboxed extension iframes have an opaque origin and cannot
	// send session cookies), so it stays minimal. A raw http.FileServer would
	// render a directory index for /extensions/ and /extensions/<author>/,
	// handing the installed-extension tree to any unauthenticated client;
	// noListingFS returns 404 for those instead, and noEscapeFS refuses what http.Dir follows.
	s.mux.Handle("/extensions/", http.StripPrefix("/extensions/",
		http.FileServer(noListingFS{noEscapeFS{dir: "extensions"}})))

	// The SPA is served behind the mux rather than inside it: a catch-all in the mux outranks that
	// mux's own answer for a verb mismatch, so a GET on a POST-only route would get the document
	// instead of the 405 the route contract promises. noListingFS keeps `/assets/` from answering an
	// unauthenticated index of every chunk the build made.
	s.router = spaBehind{s.mux, s.withNonce(http.FileServer(noListingFS{noWriterToFS{http.FS(s.assets)}}))}
}

// spaBehind serves the SPA where the API mux claimed nothing. A verb mismatch and anything under the
// API prefix stay the mux's answer.
type spaBehind struct {
	api *http.ServeMux
	spa http.Handler
}

func (b spaBehind) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// An empty pattern is the mux's "nothing registered here": no route, or a route for another verb.
	if _, pattern := b.api.Handler(r); pattern == "" && !strings.HasPrefix(r.URL.Path, "/api/") {
		b.spa.ServeHTTP(w, r)
		return
	}
	b.api.ServeHTTP(w, r)
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

// noEscapeFS refuses any path whose symlink-resolved form leaves the served root. http.Dir
// follows symlinks and this prefix is served with no session (isPublicPath), so one planted
// link would make its target world-readable.
type noEscapeFS struct{ dir string }

func (n noEscapeFS) Open(name string) (http.File, error) {
	abs := filepath.Join(n.dir, filepath.FromSlash("/"+name))
	if _, err := fsutil.Contained(abs, []string{n.dir}); err != nil {
		return nil, fs.ErrNotExist
	}
	return http.Dir(n.dir).Open(name)
}
