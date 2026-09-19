// Package server is the HTTP boundary: routing, session/CSRF middleware, host pinning, the
// WebSocket hub, static serving of the built frontend, preview and file endpoints.
// Handlers stay thin and delegate to backend/api, backend/exec and backend/extensions.
package server

import (
	"context"
	stdtls "crypto/tls"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mike/backend/agent"
	"mike/backend/api"
	"mike/backend/auth"
	"mike/backend/config"
	kanbanexec "mike/backend/exec"
	"mike/backend/extensions"
	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/llmadapter"
	"mike/backend/lsp"
	"mike/backend/prompts"
	"mike/backend/sandbox"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/term"
	"mike/backend/tls"
	"mike/backend/types"
	"mike/backend/updates"
	"mike/pkg/keylock"
	"mike/pkg/logging"

	"mike/backend/startup"

	"mike/pkg/sandboxruntime"
)

type Server struct {
	// base is the parent of every detached operation: work the user started that leaves server state
	// behind and must not die because a browser tab closed. It is cancelled by the first shutdown stage
	// and by nothing else — a request that goes away cancels its own context, never this one.
	base context.Context
	// baseCancel is what the detached-work stage calls; kept beside the context so the pair is never split.
	baseCancel context.CancelFunc

	// shutdown holds the step registered for each stage, in the order Shutdown walks them. A component
	// registers where it is built (lifecycle.go), so a step that was never registered simply never runs.
	shutdown [stageCount]func()

	store          *store.Store
	configStore    *config.ConfigStore
	sessionMgr     *auth.SessionManager
	apiTokens      *auth.APITokenManager
	projectHandler *api.ProjectHandler
	taskHandler    *api.TaskHandler
	boardHandler   *api.BoardHandler
	appStreamMgr   *stream.AppStreamManager
	gitHandler     *api.GitHandler
	executor       *kanbanexec.TaskExecutor
	terminalMgr    *term.TerminalManager
	sandboxMgr     *sandbox.SandboxManager
	updatesService *updates.Service
	restart             chan struct{}
	sandboxStatsSampler *stream.SandboxStatsSampler
	agentRegistry       *agent.AgentRegistry
	extStore            *extensions.ExtensionStore
	extensionsDir       string              // where extension files live on disk (prompt overlay, llm mappings)
	llmAdapter          *llmadapter.Adapter // provider request adaptation (llm extensions)
	extSandboxMgr       *extensions.ExtensionSandboxManager
	extHandler          *extensions.ExtensionHandler
	extLSPMgr           *lsp.ExtensionLSPManager
	// Read per turn and per config request, so it holds nothing that can fall behind the store.
	extToolRegistry *extensions.ToolRegistry
	lspHandler      *lsp.LSPHandler
	loginLimiter    *RateLimiter
	previewStore    *previewStore
	mux             *http.ServeMux
	// router is what every request enters: the mux, then the SPA behind it.
	router http.Handler
	addr   string
	// assets is the built SPA, rooted at its document directory. The static routes read nothing else.
	assets    fs.FS
	tlsConfig *tls.Config
	// settings is read instead of the environment: no request can observe a different process than
	// the one that started.
	settings startup.Config

	// hostRoot jails the no-project file endpoints (picker, mkdir, git init, add-project path).
	// Built once and shared with the project handler so the two cannot disagree.
	hostRoot *fsutil.HostRoot

	// Two more git lock domains, for the git work this process starts from a browser tab rather than the
	// exec loop. Separate tables behind named types: a task ID and a project ID come from the same
	// alphabet, so only the type can tell the two domains apart.
	//
	// gitTask serializes stash/checkout/branch/reset on one task, which corrupts its worktree when they
	// interleave (fork+switch, delete+switch).
	gitTask gitTaskLocks

	// gitProject serializes project-level git operations (stash, checkout, pull, push): every task in
	// the project shares .git/refs/stash, so concurrent push/rev-parse/drop clobbers the hashes.
	gitProject gitProjectLocks
}

type (
	gitTaskLocks    struct{ keylock.Table }
	gitProjectLocks struct{ keylock.Table }
)

// requestLockSlowWait is the threshold for a wait a person is watching. It is not a deadline: a push to
// a slow remote holds the project lock for as long as that push takes, and answering "busy" then would
// be worse than waiting.
const requestLockSlowWait = 5 * time.Second

func requestWaitReporter(kind, idAttr string) keylock.WaitReport {
	return func(id string, waited time.Duration) {
		logging.For(logging.Git).With(idAttr, id).Warnf(
			"waited %s for the %s's git operations - another one is still running", waited.Round(time.Second), kind)
	}
}

// meminfoMB parses the host's total and available RAM from /proc/meminfo, in MB. ok is false when a
// line is missing or unparsable, so a caller never receives a 0 and reads it as an empty host.
func meminfoMB(contents string) (totalMB, availableMB int64, ok bool) {
	wanted := map[string]*int64{"MemTotal:": &totalMB, "MemAvailable:": &availableMB}
	found := map[string]bool{}
	for _, line := range strings.Split(contents, "\n") {
		for prefix, dst := range wanted {
			if found[prefix] || !strings.HasPrefix(line, prefix) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				*dst = kb / 1024 // the file reports kB
				found[prefix] = true
			}
		}
	}
	if len(found) != len(wanted) {
		return 0, 0, false
	}
	return totalMB, availableMB, true
}

// readMeminfoMB is the only reader of /proc/meminfo in this process.
func readMeminfoMB() (totalMB, availableMB int64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		logging.For(logging.Startup).Warnf("reading /proc/meminfo: %v", err)
		return 0, 0, false
	}
	return meminfoMB(string(data))
}

// readTotalMemMB returns the host's total RAM in MB, or 0 when /proc/meminfo cannot be read.
func readTotalMemMB() int64 {
	totalMB, _, ok := readMeminfoMB()
	if !ok {
		logging.For(logging.Startup).Warn("reading /proc/meminfo: MemTotal unavailable — memory gate disabled")
		return 0
	}
	return totalMB
}

// selfRSSMB lifts VmRSS out of a /proc/self/status body. The parse is separate from the read for the
// same reason meminfoMB is: the numbers the budget decides with are testable without a procfs.
func selfRSSMB(contents string) (int64, bool) {
	for _, line := range strings.Split(contents, "\n") {
		rest, ok := strings.CutPrefix(line, "VmRSS:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb / 1024, true
	}
	return 0, false
}

// readSelfRSSMB is this process's resident set in MB, and the only /proc/self read in the tree. A
// value that cannot be read answers false rather than zero, because "no reading" and "MIKE holds
// nothing" are different claims.
func readSelfRSSMB() (int64, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	return selfRSSMB(string(data))
}

// selfProcessAccountant is MIKE's own process inside the memory budget: the server's resident set is
// RAM MIKE started and holds, with no cgroup and no limit, so held and charge are the same number.
// Host-side agent CLIs are not here — they book their own reservation through the gate.
type selfProcessAccountant struct {
	read func() (int64, bool)
	warn sync.Once
}

func (a *selfProcessAccountant) MemoryAccounting() (heldMB, chargeMB int64) {
	read := a.read
	if read == nil {
		read = readSelfRSSMB
	}
	rss, ok := read()
	if !ok {
		a.warn.Do(func() {
			logging.For(logging.Exec).Warn("reading /proc/self/status: VmRSS unavailable — MIKE's own process is left out of the memory budget")
		})
		return 0, 0
	}
	return rss, rss
}

// memoryBudget answers the exec memory budget from a single instant: how much of the host's RAM is
// in use and how much is free, and how much MIKE's containers hold and could still take. Every
// container belongs to exactly one of the two managers, so none of them is counted twice; the third
// accountant is the server process, which is no one's container.
type memoryBudget struct {
	accountants []memoryAccountant
	// meminfo overrides the host memory read; nil reads the real /proc/meminfo.
	meminfo func() (totalMB, availableMB int64, ok bool)
}

// memoryAccountant is the part of a container manager that the memory budget calls.
type memoryAccountant interface {
	MemoryAccounting() (heldMB, chargeMB int64)
}

// Measure implements stream.BudgetMeasure: what MIKE costs the ceiling, what it actually holds, and the
// two host figures the pause floor needs. What the rest of the host holds is in no sum the budget
// decides with, so it is not computed here.
func (b *memoryBudget) Measure() (mikeMB, heldMB, availableMB, totalMB int64, ok bool) {
	read := b.meminfo
	if read == nil {
		read = readMeminfoMB
	}
	total, available, memOK := read()
	if !memOK {
		return 0, 0, 0, 0, false
	}
	for _, a := range b.accountants {
		held, charge := a.MemoryAccounting()
		heldMB += held
		mikeMB += charge
	}
	return mikeMB, heldMB, available, total, true
}

// The task row is the only thing that outlives the cgroup these numbers were read from.
func sandboxMemoryPeakSink(st *store.Store) func(taskID string, peakMB int64, oomKills int64) {
	return func(taskID string, peakMB int64, oomKills int64) {
		if err := st.RecordTaskSandboxMemory(taskID, peakMB, oomKills); err != nil {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("recording peak sandbox memory %d MB with %d OOM kills: %v", peakMB, oomKills, err)
		}
	}
}

// reg returns the registration for one shutdown stage: it attaches the step to s and hands back a
// function reporting a registration failure. A step that silently went unregistered would surface months
// later as a shutdown that never returns.
func (s *Server) reg(st stage, name string, run func()) func() error {
	if st < 0 || st >= stageCount {
		return func() error { return fmt.Errorf("server: shutdown stage %d is outside the declared sequence", int(st)) }
	}
	if run == nil {
		// A method value read off a component that is not assigned yet is a nil func: the panic would
		// land inside Shutdown, long after the construction that caused it.
		return func() error {
			return fmt.Errorf("server: shutdown stage %s (%d) was registered with no step", stageNames[st], int(st))
		}
	}
	if s.shutdown[st] != nil {
		return func() error {
			return fmt.Errorf("server: shutdown stage %s (%d) registered twice", stageNames[st], int(st))
		}
	}
	s.shutdown[st] = func() {
		logging.For(logging.HTTP).Infof("shutdown: %s", name)
		run()
	}
	return func() error { return nil }
}

// NewServer opens the database, wires every component, and registers a teardown step beside each
// component that needs one.
func NewServer(settings startup.Config, tlsCfg *tls.Config, assets fs.FS) (*Server, error) {
	if assets == nil {
		return nil, fmt.Errorf("create server: no frontend assets")
	}
	dbPath, addr := settings.DBPath, settings.Addr
	worktreesDir := settings.WorktreesOverride
	store, err := store.NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	cfgStore := config.NewConfigStore(store, dbPath, Version)
	cfgStore.SetRegistryURL(settings.RegistryURL)
	if err := cfgStore.MigrateProviderIDs(); err != nil {
		return nil, fmt.Errorf("migrate provider IDs: %w", err)
	}
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	if worktreesDir == "" {
		worktreesDir = config.DefaultWorkspaceDir
	}
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg.WorkspaceDir = worktreesDir

	// The CA download is offered only when a local CA exists: a user-supplied cert
	// (CAFile empty) or plain HTTP has nothing to advertise.
	cfgStore.SetTLSCAAvailable(tlsCfg != nil && tlsCfg.CAFile != "")

	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("create worktrees dir: %w", err)
	}

	s := &Server{
		store:        store,
		configStore:  cfgStore,
		settings:     settings,
		addr:         addr,
		assets:       assets,
		tlsConfig:    tlsCfg,
		loginLimiter: newLoginRateLimiter(settings.LoginRateLimit),
	}
	// Built before any step can reference it: a registration reads the method value now, so a component
	// assigned later would register a nil step, which reg refuses.
	s.base, s.baseCancel = context.WithCancel(context.Background())
	defer s.reg(stageLoginRateLimiter, "login rate limiter", s.loginLimiter.Stop)()
	// Cancelled before any resource a detached operation might still be writing through is taken away.
	defer s.reg(stageDetachedWork, "detached work", s.baseCancel)()
	// First thing built, last thing closed: every step above reads or writes through it. The pool's wait
	// count is logged on the way out: a zero is the evidence the single connection is not the constraint.
	defer s.reg(stageDatabase, "database", func() {
		stats := store.DBStats()
		logging.For(logging.HTTP).Infof("database pool over this run: %d connection waits totalling %s (open=%d, inUse=%d, maxOpen=%d)", stats.WaitCount, stats.WaitDuration.Round(time.Millisecond), stats.OpenConnections, stats.InUse, stats.MaxOpenConnections)
		if err := store.Close(); err != nil {
			logging.For(logging.HTTP).Infof("shutdown: closing database: %v", err)
		}
	})()
	s.gitTask.Configure(requestLockSlowWait, requestWaitReporter("task", logging.TaskID))
	s.gitProject.Configure(requestLockSlowWait, requestWaitReporter("project", logging.ProjectID))
	s.previewStore = newPreviewStore()
	s.previewStore.startSweeper()
	defer s.reg(stagePreviewSweeper, "preview sweeper", s.previewStore.stopSweeper)()

	sessionMgr, err := auth.NewSessionManager(store, settings.MasterPassword)
	if err != nil {
		return nil, fmt.Errorf("create session manager: %w\nSet MIKE_LOGIN_PASSWORD environment variable to enable authentication", err)
	}
	s.sessionMgr = sessionMgr
	// Bearer tokens for headless clients. Issuance and revocation live behind the session-authed
	// /api/tokens routes; this manager is what authenticates them (token_auth.go).
	s.apiTokens = auth.NewAPITokenManager(store)

	worktreeMgr := git.NewWorktreeManager(store, cfgStore)
	s.appStreamMgr = stream.NewAppStreamManager(store, worktreeMgr)
	// First to close: a watcher runs git and container work on its own goroutines, and everything it
	// can reach is torn down below it.
	defer s.reg(stageWebsocketWatchers, "websocket watchers", s.appStreamMgr.Close)()
	// The boot decisions both sandbox managers share: whether this process claimed the host's
	// container namespace, and where the bridge binary lives.
	boot := sandboxruntime.Boot{
		SweepStaleContainersAtBoot: settings.SweepStaleContainersAtBoot,
		AgentPath:                  settings.AgentPath,
	}
	s.restart = make(chan struct{}, 1)
	// The one place the update decisions are assembled: the URL and override from the resolved startup
	// config, the preference from the store, and the two paths only this server knows.
	updatesService, err := updates.New(updates.Options{
		ManifestURL:    settings.UpdateManifestURL,
		Version:        Version,
		Store:          updateStateStore{store},
		Bridge:         sandboxruntime.NewRuntimeWithBoot(boot).GetAgentBinaryPath,
		DisableUpdates: settings.DisableUpdates,
		Restart: func() error {
			select {
			case s.restart <- struct{}{}:
				return nil
			default:
				// A request is already waiting to restart; asking twice changes nothing.
				return nil
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create update service: %w", err)
	}
	s.updatesService = updatesService
	// The posture is announced once per boot so the operator's log, not the UI, is the record of which
	// switch this process booted on.
	if settings.DisableUpdates {
		logging.For(logging.Updates).Infof("release updates are disabled by %s: this server will not check for, download, install or restart onto a release", updates.EnvDisableUpdates)
	} else {
		manifestURL := settings.UpdateManifestURL
		if manifestURL == "" {
			manifestURL = updates.DefaultManifestURL
		}
		logging.For(logging.Updates).Infof("release updates are enabled against %s: the daily check and the download of a newer release run once this deployment opts in under Settings \u2192 About", manifestURL)
	}

	s.sandboxMgr = sandbox.NewSandboxManager(boot)
	defer s.reg(stageSandboxContainers, "sandbox containers", s.sandboxMgr.Cleanup)()
	// Emits a sandbox_stats task event only when usage changes.
	s.sandboxStatsSampler = stream.NewSandboxStatsSampler(s.sandboxMgr, store, s.appStreamMgr,
		stream.DefaultSandboxSampleInterval, stream.DefaultSandboxStatsInterval)
	s.appStreamMgr.SetSandboxStatsProvider(s.sandboxStatsSampler.LatestStatsProvider())
	s.sandboxStatsSampler.Start()
	defer s.reg(stageSandboxStatsSampler, "sandbox stats sampler", s.sandboxStatsSampler.Stop)()
	// The sampler only sees a live container; this sink is what outlives it.
	s.sandboxMgr.SetMemoryPeakSink(sandboxMemoryPeakSink(store))
	s.boardHandler = api.NewBoardHandler(store, cfgStore, s.sandboxMgr)
	s.terminalMgr = term.NewTerminalManager(store, cfgStore, s.sandboxMgr, nil, sandbox.DangerZone(settings.DangerZone)) // agentRegistry wired after creation
	// Before the sandbox containers: killing a session's shell after its container is gone leaves the
	// shell running.
	defer s.reg(stageTerminalSessions, "terminal sessions", s.terminalMgr.Close)()
	s.terminalMgr.SetOnPortsAssigned(func(taskID, projectID string, ports []types.AssignedPort) {
		// Column-only write: a full-row write from a row read a moment ago could
		// undo a concurrent status or execution_state change.
		if err := store.SetTaskAssignedPorts(taskID, projectID, ports); err != nil {
			logging.For(logging.Terminal).Warnf("failed to store port assignment for task %s: %v", taskID, err)
			return
		}
		task, err := store.GetTaskByID(taskID)
		if err != nil {
			logging.For(logging.Terminal).Warnf("failed to get task %s for port assignment: %v", taskID, err)
			return
		}
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	})
	s.terminalMgr.SetOnProjectPortsAssigned(func(projectID string, ports []types.AssignedPort) {
		s.appStreamMgr.BroadcastProjectPorts(projectID, ports)
	})
	commitQueue := kanbanexec.NewCommitQueue()

	s.extStore = extensions.NewExtensionStore(store)
	if err := s.extStore.InitSchema(); err != nil {
		return nil, fmt.Errorf("init extension schema: %w", err)
	}

	// agent:// URLs are providers; the registry maps them to enabled agent extensions, and configStore
	// merges them into the /api/config response.
	agentRegistry := agent.NewAgentRegistry(s.extStore, cfgStore)
	s.agentRegistry = agentRegistry
	cfgStore.SetAgentRegistry(agentRegistry)

	extRegistry := extensions.NewRegistryClient()
	s.extSandboxMgr = extensions.NewExtensionSandboxManager(extRegistry, s.extStore, boot)
	defer s.reg(stageExtensionSandboxManager, "extension sandbox manager", s.extSandboxMgr.Close)()
	s.extSandboxMgr.SetExtraMountRoots(settings.ExtMountRoots)
	s.extLSPMgr = lsp.NewExtensionLSPManager(s.extStore, s.extSandboxMgr, store)
	s.extToolRegistry = extensions.NewToolRegistry(s.extStore)
	cfgStore.SetServerToolLister(s.extToolRegistry)
	defer s.reg(stageExtensionLSPManager, "extension lsp manager", s.extLSPMgr.Close)()

	// Enabled `llm` extensions rewrite MIKE's provider requests; their mappings files are
	// re-collected on every extension change below.
	extCfg := &extensions.ExtensionConfig{RegistryURL: cfg.RegistryURL}
	s.extensionsDir = extCfg.ExtensionsDirOrDefault()
	// The no-project jail: home plus the directories declared at startup, which no request can
	// carry. The data directory is deliberately absent — it holds kanban.db and the TLS keys.
	s.hostRoot = fsutil.NewHostRoot(settings.AllowRootFS, cfg.WorkspaceDir, extCfg.ExtensionsDirOrDefault())
	s.extSandboxMgr.SetWorkspaceDir(cfg.WorkspaceDir)
	s.extSandboxMgr.SetExtensionsDir(extCfg.ExtensionsDirOrDefault())
	s.llmAdapter = llmadapter.NewAdapter()
	refreshLLMAdapters := func() {
		ms, err := extensions.CollectLLMMappings(s.extStore, extCfg.ExtensionsDirOrDefault())
		if err != nil {
			logging.For(logging.HTTP).Infof("collect llm mappings: %v", err)
			return
		}
		s.llmAdapter.SetMappings(ms)
	}
	refreshLLMAdapters()
	cfgStore.SetLLMAdapter(s.llmAdapter)

	s.refreshPromptLocale()

	s.executor = kanbanexec.NewTaskExecutor(kanbanexec.Deps{
		Store:          store,
		ConfigStore:    cfgStore,
		AppStreamMgr:   s.appStreamMgr,
		WorktreeMgr:    worktreeMgr,
		SandboxMgr:     s.sandboxMgr,
		AgentRegistry:  agentRegistry,
		LSPMgr:         s.extLSPMgr,
		CommitQueue:    commitQueue,
		ExtensionTools: s.extToolRegistry,
	})
	s.executor.SetLLMAdapter(s.llmAdapter)
	// The budget is measured on the same tick that samples the containers it counts, and reports
	// straight to the gate: MIKE's own cost, what it holds, and the host figures its pause floor needs.
	s.sandboxStatsSampler.SetBudget(
		&memoryBudget{accountants: []memoryAccountant{s.sandboxMgr, s.extSandboxMgr, &selfProcessAccountant{}}},
		s.executor.MemGate().SetMeasurements)
	s.executor.RecoverStaleTasks()
	// Wire the sandbox memory budget: MIKE_MEMORY_BUDGET_PCT of total physical memory; 0 total
	// disables the gate.
	if totalMB := readTotalMemMB(); totalMB > 0 {
		memPct := settings.MemoryBudgetPct
		capacityMB := totalMB * int64(memPct) / 100
		s.executor.MemGate().SetCapacity(capacityMB)
		// About states a ceiling only where one was resolved. A hand-built startup.Config leaves the
		// percentage at 0, and publishing "0 % of this machine's RAM" would print a figure no operator
		// chose — and one that differs on every host.
		if memPct > 0 {
			cfgStore.SetMemoryBudget(capacityMB, totalMB, memPct)
		}
		memSup := kanbanexec.NewMemorySupervisor(store, s.executor.MemGate(), s.sandboxMgr, s.executor,
			kanbanexec.DefaultMemSupervisorInterval)
		memSup.Start()
		defer s.reg(stageMemorySupervisor, "memory supervisor", memSup.Stop)()
		logging.For(logging.Startup).Infof("memory supervisor: gate capacity %d MB (%d%% of %d MB total, %s)",
			capacityMB, memPct, totalMB, startup.EnvMemoryBudgetPct)
	}
	s.terminalMgr.SetAgentRegistry(agentRegistry)

	s.taskHandler = api.NewTaskHandler(store, s.appStreamMgr, cfgStore, s.terminalMgr, s.executor, worktreeMgr, commitQueue, s.sandboxMgr)
	s.projectHandler = api.NewProjectHandler(store, cfgStore, worktreeMgr, s.terminalMgr, s.sandboxMgr, s.executor, s.hostRoot, sandbox.DangerZone(settings.DangerZone), s.taskHandler)
	s.gitHandler = api.NewGitHandler(store, s.executor.WorktreeMgr())
	s.gitHandler.SetWorktreeMutex(s.lockWorktree)

	// Extensions are entirely registry-driven — there is no built-in bootstrap step.
	s.extHandler = extensions.NewExtensionHandler(s.extStore, s.extSandboxMgr, extRegistry, extCfg)
	// What Settings → Extensions shows when a language server cannot resolve the project it
	// is serving: the state behind diagnostics core deliberately withheld rather than
	// forwarded as fact.
	s.extHandler.SetEnvironmentStatus(func(extID string) *extensions.EnvironmentStatus {
		healthy, note := s.extLSPMgr.EnvironmentSummary(extID)
		if healthy || note == "" {
			return nil
		}
		return &extensions.EnvironmentStatus{Healthy: false, Note: note}
	})
	extLSPRegistry := lsp.NewExtensionRegistry(s.extStore)
	s.lspHandler = lsp.NewLSPHandler(extLSPRegistry, s.extLSPMgr)
	defer s.reg(stageLSPHandler, "lsp handler", s.lspHandler.Close)()
	// The LSP workspace becomes the bind-mount source of the language server's container, so it
	// answers to the same jail as every other host path the server opens. See lspWorkspaceAllowed.
	s.lspHandler.SetWorkspaceValidator(s.lspWorkspaceAllowed)
	s.extHandler.SetOnExtensionChanged(func() {
		agentRegistry.Reload()
		refreshLLMAdapters()
		// Enabling or disabling a pack is a language change; the prompts follow it live,
		// the same as the UI's catalog does on the same broadcast.
		s.refreshPromptLocale()
		s.appStreamMgr.BroadcastExtensionChange()
	})
	s.extHandler.SetOnImageUpdated(func(extID string) {
		s.lspHandler.BroadcastLSPReset()
	})
	s.gitHandler.SetProjectMutex(s.lockProject)
	// On-demand file fetches read through the watcher's diff cache.
	s.appStreamMgr.SetDiffCacheUpdate(s.gitHandler.UpdateDiffCache)

	dryRun := settings.GCDryRun
	if removed, err := s.executor.WorktreeMgr().GarbageCollect(dryRun); err != nil {
		logging.For(logging.Worktree).Warnf("startup error: %v", err)
	} else if removed > 0 {
		logging.For(logging.Worktree).Infof("startup: %d orphaned worktrees cleaned", removed)
	}

	if err := store.ClearStaleTitleGenerating(); err != nil {
		logging.For(logging.Generate).Warnf("startup: failed to clear stale flags: %v", err)
	}
	if err := store.ClearStaleSummarizing(); err != nil {
		logging.For(logging.Summarize).Warnf("startup: failed to clear stale flags: %v", err)
	}

	s.setupRoutes()

	return s, nil
}

// refreshPromptLocale derives the prompt language from the enabled i18n pack and installs it as the
// template overlay, at startup and on every extension change (docs/i18n.md). A list that cannot be read
// leaves the current language in place rather than resetting to English.
func (s *Server) refreshPromptLocale() {
	ov, err := extensions.ActivePromptOverlay(s.extStore, s.extensionsDir)
	if err != nil {
		logging.For(logging.Prompts).Infof("cannot read the active pack: %v — keeping the current prompt language", err)
		return
	}
	prompts.SetOverlay(ov.Dir, ov.Locale)
	switch {
	case ov.Locale == "":
		logging.For(logging.Prompts).Infof("locale=en (no translation pack enabled)")
	case ov.Dir == "":
		logging.For(logging.Prompts).Infof("locale=%s (pack ships no prompt templates — core's English prompt renders)", ov.Locale)
	default:
		logging.For(logging.Prompts).Infof("locale=%s templates=%s", ov.Locale, ov.Dir)
	}
}

func (s *Server) handleFetchModels(w http.ResponseWriter, r *http.Request) {

	var req config.FetchModelsRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.LlamaServerURL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "llama_server_url is required")
		return
	}

	if types.IsAgentURL(req.LlamaServerURL) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		agentName, ok := types.ParseAgentURL(req.LlamaServerURL)
		if !ok {
			httputil.JSONError(w, http.StatusBadRequest, "agent URL must name exactly one extension: "+req.LlamaServerURL)
			return
		}
		ext := s.agentRegistry.LookupByName(agentName)
		if ext == nil {
			httputil.JSONError(w, http.StatusNotFound, fmt.Sprintf("Agent %q not found", agentName))
			return
		}

		adapter, err := agent.LoadAdapter(ext)
		if err != nil {
			httputil.Fault(w, "load agent adapter", err)
			return
		}
		defer adapter.Close()

		// Only enough config for the adapter's SubprocessConfig — no worktree, no sandbox.
		initCfg, _ := json.Marshal(map[string]any{
			"extension_name": ext.Name,
			"session_name":   "fetch-models-probe",
			"worktree_path":  os.TempDir(),
			"sandbox":        false,
			"config":         ext.Config,
		})
		if err := adapter.Init(ctx, initCfg); err != nil {
			httputil.Fault(w, "initialize agent adapter", err)
			return
		}

		agentModels := adapter.ListModels(ctx)
		var fetchedModels []config.FetchedModel
		for _, m := range agentModels {
			fetchedModels = append(fetchedModels, config.FetchedModel{
				Name:          m.Name,
				ContextWindow: m.ContextWindow,
			})
		}

		httputil.JSONResponse(w, http.StatusOK, config.FetchModelsResponse{Models: fetchedModels})
		return
	}

	// A masked or empty key means "use the stored key for this provider".
	req.APIKey = s.configStore.ResolveAPIKey(req.LlamaServerURL, req.APIKey)
	result := s.configStore.FetchModels(req)
	httputil.JSONResponse(w, http.StatusOK, result)
}

func (s *Server) RedirectHTTPSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.tlsConfig == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Only trust X-Forwarded-Proto when explicitly behind a trusted proxy.
		// Otherwise an attacker can set it to "https" to bypass the redirect.
		if requestIsHTTPS(r, s.settings.TrustProxy) {
			next.ServeHTTP(w, r)
			return
		}
		target := fmt.Sprintf("https://%s%s", r.Host, r.URL.RequestURI())
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// withNonce serves index.html for "/" with a request-specific CSP nonce; everything else
// passes through to the file server.
func (s *Server) withNonce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			content, err := fs.ReadFile(s.assets, "index.html")
			if err != nil {
				http.Error(w, "failed to read index.html", http.StatusInternalServerError)
				return
			}

			nonce, ok := r.Context().Value(nonceKey).(string)
			if !ok || nonce == "" {
				// A constant nonce is a published CSP bypass.
				logging.For(logging.HTTP).Errorf("serving / refused: no CSP nonce (entropy source failed)")
				http.Error(w, "unable to serve application", http.StatusInternalServerError)
				return
			}

			updatedContent := strings.Replace(string(content), "nonce=\"{{.Nonce}}\"", fmt.Sprintf("nonce=\"%s\"", nonce), 1)

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(updatedContent))
			return
		}

		next.ServeHTTP(w, r)
	})
}

type Middleware func(http.Handler) http.Handler

// defaultSniffTimeout is how long a connection gets to send the byte that identifies
// it. Real clients send it immediately; it only bounds a peer that connected and went
// silent (lost first packet, cancelled preconnect, health check, scan).
const defaultSniffTimeout = 10 * time.Second

// readyQueue paces sniffed connections against net/http's accept loop. No client
// waits behind it: a connection lands in it only once it can be served.
const readyQueue = 8

// tlsRecordTypeHandshake is the first byte of a TLS record, which is how TLS is told
// apart from plain HTTP before a request line exists.
const tlsRecordTypeHandshake = 0x16

// hybridListener handles both TLS and plain HTTP on the same port.
type hybridListener struct {
	net.Listener
	stdTLSConfig *stdtls.Config
	// Zero means defaultSniffTimeout.
	sniffTimeout time.Duration
	drops        *sniffDropLogger

	ready chan net.Conn // connections whose protocol is known
	done  chan struct{} // closed once the listener is finished
	once  sync.Once
	mu    sync.Mutex
	last  error // why the accept loop stopped, if a real failure caused it
}

// Accept hands net/http a connection whose protocol is already known. The first byte decides TLS vs
// plain HTTP and net/http only inspects the type Accept returns, so that read cannot be deferred - but
// it must not happen inside Accept, because net/http accepts one connection at a time: a peer that
// connects and sends nothing would stall every handshake queued behind it and read as a dead server.
func (l *hybridListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.ready:
		return conn, nil
	case <-l.done:
		if err := l.stopErr(); err != nil {
			return nil, err
		}
		// net/http turns this into http.ErrServerClosed while shutting down.
		return nil, net.ErrClosed
	}
}

func (l *hybridListener) Close() error {
	err := l.Listener.Close()
	l.shutdown(err)
	return err
}

// acceptLoop is the only caller of the underlying Accept, and it never reads from a
// connection, so no client can block it.
func (l *hybridListener) acceptLoop() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			// Listener gone (Close on shutdown, or a real failure): release Accept
			// with the reason, since nothing more can be accepted.
			l.shutdown(err)
			return
		}
		go l.identify(conn)
	}
}

// identify reads the first byte under a deadline and queues the connection wrapped for
// the protocol it announced. A peer that stalled, hung up or reset is logged and closed
// here: it says nothing about the listener and must not reach net/http.
func (l *hybridListener) identify(raw net.Conn) {
	timeout := l.sniffTimeout
	if timeout <= 0 {
		timeout = defaultSniffTimeout
	}
	conn := raw
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		l.drops.note(conn.RemoteAddr(), err)
		conn.Close()
		return
	}

	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		l.drops.note(conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	// The deadline was for the sniff alone. net/http sets its own (ReadHeaderTimeout),
	// and a tls.Conn wrapping this socket would inherit the stale one.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		l.drops.note(conn.RemoteAddr(), err)
		conn.Close()
		return
	}

	if buf[0] == tlsRecordTypeHandshake {
		conn = stdtls.Server(newPeekConn(conn, buf), l.stdTLSConfig)
	} else {
		conn = newPeekConn(conn, buf)
	}

	select {
	case l.ready <- conn:
	case <-l.done:
		conn.Close()
	}
}

// shutdown records why the listener stopped, releases Accept, and closes any queued
// connection nobody will serve. The first reason wins, so Close cannot mask a failure.
func (l *hybridListener) shutdown(reason error) {
	l.mu.Lock()
	if l.last == nil {
		l.last = reason
	}
	l.mu.Unlock()

	l.once.Do(func() {
		close(l.done)
		for {
			select {
			case conn := <-l.ready:
				conn.Close()
			default:
				return
			}
		}
	})
}

// stopErr returns why the accept loop stopped, if a real failure caused it.
func (l *hybridListener) stopErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// sniffLogInterval is how often one host may be reported for dropped connections;
// a scanner or a device retrying on a dead network drops one per attempt.
const sniffLogInterval = time.Minute

// maxSniffHosts bounds the rate-limit map. Past it, new hosts are simply suppressed.
const maxSniffHosts = 256

// sniffDropLogger rate-limits drop logging per remote host.
type sniffDropLogger struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *sniffDropLogger) note(addr net.Addr, cause error) {
	if l == nil || addr == nil || !l.allowed(addr) {
		return
	}
	logging.For(logging.Startup).Infof("dropped connection from %s before its first byte: %v (further drops from this host are suppressed for %s)", addr, cause, sniffLogInterval)
}

func (l *sniffDropLogger) allowed(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if at, ok := l.last[host]; ok && time.Since(at) < sniffLogInterval {
		return false
	}
	if _, ok := l.last[host]; !ok && len(l.last) >= maxSniffHosts {
		return false
	}
	l.last[host] = time.Now()
	return true
}

type peekConn struct {
	net.Conn
	buf []byte
	pos int
}

func newPeekConn(conn net.Conn, buf []byte) *peekConn {
	return &peekConn{Conn: conn, buf: buf}
}

func (p *peekConn) Read(b []byte) (n int, err error) {
	if p.pos < len(p.buf) {
		n = copy(b, p.buf[p.pos:])
		p.pos += n
		return n, nil
	}
	return p.Conn.Read(b)
}

func AddMiddleware(h http.Handler, middleware ...Middleware) http.Handler {

	for _, m := range slices.Backward(middleware) {
		h = m(h)
	}

	return h
}

// middlewareChain is the middleware Serve installs, in order. It is a method so a test
// can drive the chain that actually runs — an entry missing here protects nothing.
func (s *Server) middlewareChain(h http.Handler) http.Handler {
	return AddMiddleware(h,
		// Outermost: every request — including the /api/stream, /api/lsp and
		// terminal WebSocket upgrades and the HTTPS redirect — is origin-checked
		// before it reaches a route.
		s.HostPinMiddleware,
		s.RedirectHTTPSMiddleware,
		PanicRecoveryMiddleware,
		// Before anything that reads a body.
		MaxBodyMiddleware(httputil.MaxRequestBodyBytes),
		NonceMiddleware,
		s.CORSMiddleware,
		// Before CSRF and the session: a bearer credential carries no cookie to forge, and its
		// absence must leave the cookie path untouched.
		s.APITokenMiddleware,
		s.CSRFMiddleware,
		s.SessionMiddleware,
		CompressMiddleware,
	)
}

func (s *Server) tlsConfigToStd() *stdtls.Config {
	cert, err := stdtls.LoadX509KeyPair(s.tlsConfig.CertFile, s.tlsConfig.KeyFile)
	if err != nil {
		logging.For(logging.HTTP).Warnf("failed to load key pair: %v", err)
		return nil
	}
	return &stdtls.Config{Certificates: []stdtls.Certificate{cert}}
}

// RepairExtensionFiles restores installed extensions whose files are gone, each from the commit its own
// record pins. A missing directory is not a disabled extension: it is an enabled one that does nothing,
// invisibly, while the API still reports it installed.
func (s *Server) RepairExtensionFiles(ctx context.Context) {
	if _, _, err := s.extHandler.RepairMissingFiles(ctx); err != nil {
		logging.For(logging.Extensions).Warnf("repair: could not check installed extension files: %v", err)
	}
}

func (s *Server) Serve() error {
	caFile := ""
	if s.tlsConfig != nil {
		caFile = s.tlsConfig.CAFile
	}

	srv := &http.Server{
		Addr: s.addr,
		// Silences repeated "client does not trust the local CA" handshake noise;
		// see tls_error_log.go.
		ErrorLog:          httpErrorLog(caFile),
		Handler:           s.middlewareChain(s.router),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Echo the Host allowlist extras: a 421 through a reverse proxy is almost
	// always a missing entry here, so make the effective value visible at startup.
	if extra := s.effectiveAllowedHosts(); extra != "" {
		logging.For(logging.Host).Infof("accepting extra Host header values: %s", extra)
	}
	s.logHostRootPolicy()

	if s.tlsConfig != nil {
		logging.For(logging.HTTP).Infof("Server starting on https://%s (TLS: %s, %s)", s.addr, s.tlsConfig.CertFile, s.tlsConfig.KeyFile)
		if s.tlsConfig.CAFile != "" {
			logging.For(logging.HTTP).Infof("TLS: import %s into a device's trust store to trust this certificate", s.tlsConfig.CAFile)
		}
		l, err := net.Listen("tcp", s.addr)
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		return srv.Serve(newHybridListener(l, s.tlsConfigToStd(), 0))
	}

	logging.For(logging.HTTP).Infof("Server starting on http://%s", s.addr)
	return srv.ListenAndServe()
}

// logHostRootPolicy echoes the host-directory jail, otherwise invisible until it refuses something.
// A set-but-not-1 variable is already reported at boot by startup's warnings.
func (s *Server) logHostRootPolicy() {
	if s.hostRoot.Unrestricted() {
		logging.For(logging.Host).Warnf("%s=1: the file endpoints, project creation and clone may reach any host path",
			fsutil.AllowRootFSEnv)
		return
	}
	if s.hostRoot.HasUniversalRoot() {
		logging.For(logging.Host).Warnf("the host directory jail includes the filesystem root (roots: %s), which constrains nothing: "+
			"every host path is reachable without the widening variable", strings.Join(s.hostRoot.Roots(), ", "))
	}
}

// lspWorkspaceAllowed answers "may an LSP client open a language server at this directory?". The
// workspace string arrives in a browser message and becomes the `-v` source of the container, so it
// goes through fsutil.Contained rather than a prefix comparison: a symlink planted in a worktree a task
// can write to resolves against the host, and a lexical check reads straight through it.
func (s *Server) lspWorkspaceAllowed(workspace string) bool {
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		return false
	}
	// Projects may live anywhere on the host; the workspace dir holds every project's task
	// worktrees, which is where an open file usually is.
	roots := []string{filepath.Clean(cfg.WorkspaceDir)}
	projects, err := s.store.ListProjects()
	if err != nil {
		return false
	}
	for _, p := range projects {
		roots = append(roots, filepath.Clean(p.Path))
	}
	_, err = fsutil.Contained(workspace, roots)
	return err == nil
}

func newHybridListener(l net.Listener, stdCfg *stdtls.Config, sniffTimeout time.Duration) *hybridListener {
	h := &hybridListener{
		Listener:     l,
		stdTLSConfig: stdCfg,
		sniffTimeout: sniffTimeout, // zero means defaultSniffTimeout
		drops:        &sniffDropLogger{},
		ready:        make(chan net.Conn, readyQueue),
		done:         make(chan struct{}),
	}
	// A sniff still running when the listener closes lingers for at most sniffTimeout
	// and costs one parked goroutine: closing done has already released Accept, Serve
	// and every queued connection, so there is nothing to wait for.
	go h.acceptLoop()
	return h
}

// Shutdown runs every teardown step. Each is best-effort and none can be interrupted, so the
// order below is the dependency order, not a preference.
