package server

import (
	"io/fs"
	"net/http"
	"os"
	"regexp"
)

// pathRegex patterns for routing.
var (
	reProjectByID          = regexp.MustCompile(`^/api/projects/([^/]+)$`)
	reProjectBoard         = regexp.MustCompile(`^/api/projects/([^/]+)/board$`)
	reProjectBoardWS       = regexp.MustCompile(`^/api/projects/([^/]+)/board/stream$`)
	reProjectTasks         = regexp.MustCompile(`^/api/projects/([^/]+)/tasks$`)
	reProjectTaskByID      = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)$`)
	reTaskMove             = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/move$`)
	reTaskStart            = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/start$`)
	reTaskStop             = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/stop$`)
	reTaskRetry            = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/retry$`)
	reTaskResume           = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/resume$`)
	reTaskStream           = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/stream$`)
	reTaskDiff             = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/diff$`)
	reTaskMessages         = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/messages$`)
	reTaskMessageByID      = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/messages/([^/]+)$`)
	reTaskDeps             = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/dependencies$`)
	reTaskDepChild         = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/dependencies/([^/]+)$`)
	reTaskDependents       = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/dependents$`)
	reTaskReview           = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/review-comments$`)
	reTaskReviewByID       = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/review-comments/([^/]+)$`)
	reTaskReorder          = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/reorder$`)
	reTasksDone            = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/done$`)
	reProjectGitLog        = regexp.MustCompile(`^/api/projects/([^/]+)/git/log$`)
	reProjectGitBranches   = regexp.MustCompile(`^/api/projects/([^/]+)/git/branches$`)
	reProjectGitDiff       = regexp.MustCompile(`^/api/projects/([^/]+)/git/diff$`)
	reProjectGitStatus     = regexp.MustCompile(`^/api/projects/([^/]+)/git/status$`)
	reProjectGitCheckout   = regexp.MustCompile(`^/api/projects/([^/]+)/git/checkout$`)
	reProjectGitPull       = regexp.MustCompile(`^/api/projects/([^/]+)/git/pull$`)
	reProjectGitPush       = regexp.MustCompile(`^/api/projects/([^/]+)/git/push$`)
	reProjectHomeAgentMsg  = regexp.MustCompile(`^/api/projects/([^/]+)/home-agent/message$`)
	reProjectHomeAgentMsgs = regexp.MustCompile(`^/api/projects/([^/]+)/home-agent/messages$`)
	reProjectHomeAgentStream = regexp.MustCompile(`^/api/projects/([^/]+)/home-agent/stream$`)
	reProjectStats         = regexp.MustCompile(`^/api/projects/([^/]+)/stats$`)
	reProjectTerminal      = regexp.MustCompile(`^/api/projects/([^/]+)/terminal$`)
	reTaskTerminal         = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/terminal$`)
	reTaskWorktreeExists   = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/worktree-exists$`)
	reTaskThreads          = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/threads$`)
	reTaskThreadSwitch     = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/threads/switch$`)
	reTaskThreadFork       = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/threads/fork$`)
)

func (s *Server) setupRoutes() {
	s.mux = http.NewServeMux()

	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/prompts/reload", s.handleReloadPrompts)

	s.mux.HandleFunc("/api/config/test-connection", s.configStore.HandleTestConnection)
	s.mux.HandleFunc("/api/config/fetch-models", s.configStore.HandleFetchModels)
	s.mux.HandleFunc("/api/config", s.configStore.HandleConfig)
	s.mux.HandleFunc("/api/test/reset", s.handleTestReset)
	s.mux.HandleFunc("/api/test/create-orphan", s.handleCreateOrphan)
	s.mux.HandleFunc("/api/gc", s.handleGC)
	s.mux.HandleFunc("/api/fs", s.handleFS)

	s.mux.HandleFunc("/api/projects", s.handleProjects)
	s.mux.HandleFunc("/api/projects/", s.handleProjectScoped)

	frontendDist := GetEnv("FRONTEND_DIST", "./frontend/dist")
	if _, err := os.Stat(frontendDist); err == nil {
		sub, err := fs.Sub(os.DirFS(frontendDist), ".")
		if err == nil {
			s.mux.Handle("/", http.FileServer(noWriterToFS{http.FS(sub)}))
		}
	}
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path != "/api/projects" {
		s.handleProjectScoped(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.projectHandler.HandleList(w, r)
	case http.MethodPost:
		s.projectHandler.HandleCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProjectScoped(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if path == "/api/projects/counts" {
		s.projectHandler.HandleCounts(w, r)
		return
	}

	if m := reProjectByID.FindStringSubmatch(path); m != nil {
		s.projectHandler.HandleByID(w, r, m[1])
		return
	}

	if m := reProjectStats.FindStringSubmatch(path); m != nil {
		s.projectHandler.HandleStats(w, r, m[1])
		return
	}

	if m := reProjectBoardWS.FindStringSubmatch(path); m != nil {
		s.boardStreamMgr.HandleStream(w, r, m[1])
		return
	}

	if m := reProjectBoard.FindStringSubmatch(path); m != nil {
		s.boardHandler.HandleGetBoard(w, r, m[1])
		return
	}

	if m := reTaskReorder.FindStringSubmatch(path); m != nil {
		s.boardHandler.HandleReorder(w, r, m[1])
		return
	}

	if m := reTasksDone.FindStringSubmatch(path); m != nil {
		s.taskHandler.HandleClearDone(w, r, m[1])
		return
	}

	if m := reProjectTasks.FindStringSubmatch(path); m != nil {
		s.taskHandler.HandleList(w, r, m[1])
		return
	}

	if m := reTaskDeps.FindStringSubmatch(path); m != nil {
		s.depHandler.HandleDependencies(w, r, m[1], m[2])
		return
	}
	if m := reTaskDepChild.FindStringSubmatch(path); m != nil {
		s.depHandler.HandleDependency(w, r, m[1], m[2], m[3])
		return
	}

	if m := reTaskDependents.FindStringSubmatch(path); m != nil {
		s.depHandler.HandleDependents(w, r, m[1], m[2])
		return
	}

	if m := reTaskReview.FindStringSubmatch(path); m != nil {
		s.reviewHandler.HandleComments(w, r, m[1], m[2])
		return
	}
	if m := reTaskReviewByID.FindStringSubmatch(path); m != nil {
		s.reviewHandler.HandleComment(w, r, m[1], m[2], m[3])
		return
	}

	if m := reTaskStream.FindStringSubmatch(path); m != nil {
		s.executor.HandleStream(w, r, m[1], m[2])
		return
	}
	if m := reTaskDiff.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandleTaskDiff(w, r, m[1], m[2])
		return
	}

	if m := reTaskMessageByID.FindStringSubmatch(path); m != nil {
		s.handleTaskMessageByID(w, r, m[1], m[2], m[3])
		return
	}
	if m := reTaskMessages.FindStringSubmatch(path); m != nil {
		s.handleTaskMessages(w, r, m[1], m[2])
		return
	}

	if m := reTaskThreadFork.FindStringSubmatch(path); m != nil {
		s.handleThreadFork(w, r, m[1], m[2])
		return
	}
	if m := reTaskThreadSwitch.FindStringSubmatch(path); m != nil {
		s.handleThreadSwitch(w, r, m[1], m[2])
		return
	}
	if m := reTaskThreads.FindStringSubmatch(path); m != nil {
		s.handleThreads(w, r, m[1], m[2])
		return
	}

	if m := reTaskMove.FindStringSubmatch(path); m != nil {
		s.taskHandler.HandleMove(w, r, m[1], m[2])
		return
	}

	if m := reTaskStart.FindStringSubmatch(path); m != nil {
		s.executor.HandleStart(w, r, m[1], m[2])
		return
	}
	if m := reTaskStop.FindStringSubmatch(path); m != nil {
		s.executor.HandleStop(w, r, m[1], m[2])
		return
	}
	if m := reTaskRetry.FindStringSubmatch(path); m != nil {
		s.executor.HandleRetry(w, r, m[1], m[2])
		return
	}
	if m := reTaskResume.FindStringSubmatch(path); m != nil {
		s.executor.HandleResume(w, r, m[1], m[2])
		return
	}

	if m := reProjectTaskByID.FindStringSubmatch(path); m != nil {
		s.taskHandler.HandleByID(w, r, m[1], m[2])
		return
	}

	if m := reProjectGitLog.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandleLog(w, r, m[1])
		return
	}
	if m := reProjectGitBranches.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandleBranches(w, r, m[1])
		return
	}
	if m := reProjectGitDiff.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandleDiff(w, r, m[1])
		return
	}
	if m := reProjectGitStatus.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandleStatus(w, r, m[1])
		return
	}
	if m := reProjectGitCheckout.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandleCheckout(w, r, m[1])
		return
	}
	if m := reProjectGitPull.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandlePull(w, r, m[1])
		return
	}
	if m := reProjectGitPush.FindStringSubmatch(path); m != nil {
		s.gitHandler.HandlePush(w, r, m[1])
		return
	}

	if m := reProjectHomeAgentMsg.FindStringSubmatch(path); m != nil {
		s.homeAgentMgr.HandleMessage(w, r, m[1])
		return
	}
	if m := reProjectHomeAgentMsgs.FindStringSubmatch(path); m != nil {
		s.homeAgentMgr.HandleMessages(w, r, m[1])
		return
	}
	if m := reProjectHomeAgentStream.FindStringSubmatch(path); m != nil {
		s.homeAgentMgr.HandleStream(w, r, m[1])
		return
	}

	if m := reTaskWorktreeExists.FindStringSubmatch(path); m != nil {
		s.handleWorktreeExists(w, r, m[1], m[2])
		return
	}

	if m := reTaskTerminal.FindStringSubmatch(path); m != nil {
		s.terminalMgr.HandleTerminal(w, r, m[1], m[2])
		return
	}

	if m := reProjectTerminal.FindStringSubmatch(path); m != nil {
		s.terminalMgr.HandleProjectTerminal(w, r, m[1])
		return
	}

	http.NotFound(w, r)
}
