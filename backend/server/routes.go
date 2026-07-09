package server

import (
	"io/fs"
	"net/http"
	"os"
)

func (s *Server) setupRoutes() {
	s.mux = http.NewServeMux()

	// Health & auth
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("/api/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("/api/auth/session", s.handleAuthSession)
	s.mux.HandleFunc("/api/prompts/reload", s.handleReloadPrompts)

	// Config
	s.mux.HandleFunc("/api/config/test-connection", s.configStore.HandleTestConnection)
	s.mux.HandleFunc("/api/config/fetch-models", s.configStore.HandleFetchModels)
	s.mux.HandleFunc("/api/config/pi-available", s.configStore.HandlePiAvailability)
	s.mux.HandleFunc("/api/config/test-pi-connection", s.configStore.HandleTestPiConnection)
	s.mux.HandleFunc("/api/config", s.configStore.HandleConfig)

	// Test endpoints
	s.mux.HandleFunc("/api/test/reset", s.handleTestReset)
	s.mux.HandleFunc("/api/test/create-orphan", s.handleCreateOrphan)

	// WebSocket stream
	s.mux.HandleFunc("/api/stream", s.appStreamMgr.HandleStream)

	// Global utilities
	s.mux.HandleFunc("/api/home", s.handleHome)
	s.mux.HandleFunc("/api/gc", s.handleGC)
	s.mux.HandleFunc("/api/fs/read", s.handleFSRead)
	s.mux.HandleFunc("/api/fs/write", s.handleFSWrite)
	s.mux.HandleFunc("/api/fs/init-git", s.handleFSInitGit)
	s.mux.HandleFunc("/api/fs/mkdir", s.handleFSMkdir)
	s.mux.HandleFunc("/api/fs/search", s.handleFSSearch)
	s.mux.HandleFunc("/api/fs", s.handleFS)

	// Projects
	s.mux.HandleFunc("GET /api/projects", s.projectHandler.HandleList)
	s.mux.HandleFunc("POST /api/projects", s.projectHandler.HandleCreate)
	s.mux.HandleFunc("GET /api/projects/counts", s.projectHandler.HandleCounts)
	s.mux.HandleFunc("/api/projects/{projectID}", s.projectHandler.HandleByID)
	s.mux.HandleFunc("/api/projects/{projectID}/board", s.boardHandler.HandleGetBoard)
	s.mux.HandleFunc("/api/projects/{projectID}/board/reorder", s.boardHandler.HandleReorder)
	s.mux.HandleFunc("/api/projects/{projectID}/stats", s.projectHandler.HandleStats)
	s.mux.HandleFunc("/api/projects/{projectID}/sandbox/build", s.projectHandler.HandleSandboxBuild)
	s.mux.HandleFunc("/api/projects/{projectID}/terminal", s.terminalMgr.HandleProjectTerminal)

	// Tasks
	s.mux.HandleFunc("/api/projects/{projectID}/tasks", s.taskHandler.HandleList)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/done", s.taskHandler.HandleClearDone)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/reorder", s.boardHandler.HandleReorder)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}", s.taskHandler.HandleByID)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/move", s.taskHandler.HandleMove)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/start", s.taskHandler.HandleStartWithSubtasks)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/start-subtask", s.taskHandler.HandleStartSubtask)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/stop", s.taskHandler.HandleStopWithCascade)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/retry", s.executor.HandleRetry)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/resume", s.executor.HandleResume)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/send", s.executor.HandleSend)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/summarize", s.executor.HandleSummarize)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff", s.gitHandler.HandleTaskDiff)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", s.gitHandler.HandleTaskDiffFile)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/diff/stage", s.gitHandler.HandleStageFile)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/commits", s.gitHandler.HandleTaskCommits)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/reset-worktree", s.gitHandler.HandleResetWorktree)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/commit-preview", s.taskHandler.HandleCommitPreview)
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/commit-preview/stream", s.taskHandler.HandleCommitPreviewStream)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/commit", s.taskHandler.HandleCommitAndDone)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/terminal", s.terminalMgr.HandleTerminal)

	// Task messages
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/messages", s.handleTaskMessages)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/messages/{messageID}", s.handleTaskMessageByID)

	// Task threads
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/threads", s.handleThreads)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/threads/fork", s.handleThreadFork)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/threads/switch", s.handleThreadSwitch)

	// Task dependencies
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/dependencies", s.depHandler.HandleDependencies)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/dependencies/{childID}", s.depHandler.HandleDependency)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/dependents", s.depHandler.HandleDependents)

	// Task review comments
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/review-comments", s.reviewHandler.HandleComments)
	s.mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/review-comments/{commentID}", s.reviewHandler.HandleComment)

	// Git operations
	s.mux.HandleFunc("/api/projects/{projectID}/git/log", s.gitHandler.HandleLog)
	s.mux.HandleFunc("/api/projects/{projectID}/git/branch", s.gitHandler.HandleCurrentBranch)
	s.mux.HandleFunc("/api/projects/{projectID}/git/branches", s.gitHandler.HandleBranches)
	s.mux.HandleFunc("/api/projects/{projectID}/git/diff", s.gitHandler.HandleDiff)
	s.mux.HandleFunc("/api/projects/{projectID}/git/status", s.gitHandler.HandleStatus)
	s.mux.HandleFunc("/api/projects/{projectID}/git/checkout", s.gitHandler.HandleCheckout)
	s.mux.HandleFunc("/api/projects/{projectID}/git/pull", s.gitHandler.HandlePull)
	s.mux.HandleFunc("/api/projects/{projectID}/git/push", s.gitHandler.HandlePush)

	// Frontend
	if s.frontendDist != "" {
		if _, err := os.Stat(s.frontendDist); err == nil {
			sub, err := fs.Sub(os.DirFS(s.frontendDist), ".")
			if err == nil {
				s.mux.Handle("/", http.FileServer(noWriterToFS{http.FS(sub)}))
			}
		}
	}
}
