package server

// Shutdown walks declared stages in order; a component registers its step where it is built.

import "context"

// stage is one step of the shutdown sequence. Its zero value is the first stage on purpose: a step that
// forgets to name its stage lands at the front of the sequence, where a missing "close the containers"
// step is noticed as a wedged run rather than at the end, where it looks like a clean exit.
type stage int

const (
	// stageDetachedWork first: cancelling the base context is the only step that asks work to stop
	// itself, and the only one that cannot wedge (a cancel does not wait). Everything the cancelled work
	// might still touch — the database, the container it is streaming from — is alive for the rest of the
	// sequence, so asking first is the one order that gives an in-flight write a chance to land before the
	// machinery under it goes away.
	stageDetachedWork stage = iota
	// stagePreviewSweeper next: the sweeper owns goroutines nothing else can reap.
	stagePreviewSweeper
	// stageWebsocketWatchers before everything they touch: a watcher runs git and container work on
	// its own goroutines, and everything it can reach is torn down below it.
	stageWebsocketWatchers
	// stageTerminalSessions before the sandbox containers: a shell whose container is already gone is
	// a shell left running.
	stageTerminalSessions
	stageLSPHandler
	stageExtensionLSPManager
	stageExtensionSandboxManager
	// stageMemorySupervisor before the containers it can start.
	stageMemorySupervisor
	// stageSandboxStatsSampler before the containers it samples, or it samples a container that is
	// already gone and reports a sandbox that no longer exists.
	stageSandboxStatsSampler
	stageSandboxContainers
	stageLoginRateLimiter
	// stageDatabase last: every step above reads or writes through it.
	stageDatabase
	stageCount
)

// stageNames is indexed by stage. TestShutdownStagesAreDeclaredEntirely fails if the two ever disagree,
// which is what makes adding a stage a deliberate act instead of a silent off-by-one.
var stageNames = [stageCount]string{
	stageDetachedWork:            "detached work",
	stagePreviewSweeper:          "preview sweeper",
	stageWebsocketWatchers:       "websocket watchers",
	stageTerminalSessions:        "terminal sessions",
	stageLSPHandler:              "lsp handler",
	stageExtensionLSPManager:     "extension lsp manager",
	stageExtensionSandboxManager: "extension sandbox manager",
	stageMemorySupervisor:        "memory supervisor",
	stageSandboxStatsSampler:     "sandbox stats sampler",
	stageSandboxContainers:       "sandbox containers",
	stageLoginRateLimiter:        "login rate limiter",
	stageDatabase:                "database",
}

// Shutdown runs every registered step in the declared order. Each step logs its name *before* running:
// none of them can be interrupted, so when the shutdown deadline fires, the last line in the log is the
// only thing that names what wedged. The deadline itself is server.RunShutdownWithDeadline's, and it is
// enforced by leaving rather than by cancelling — every step waits on something that cannot be
// interrupted (a container not answering podman, a bridge whose peer died).
func (s *Server) Shutdown(ctx context.Context) error {
	for st := stage(0); st < stageCount; st++ {
		run := s.shutdown[st]
		if run == nil {
			continue
		}
		run()
	}
	return nil
}

// Detached is work that outlives its request, dies with the process and is never joined to shutdown.
func (s *Server) Detached() (context.Context, context.CancelFunc) {
	return context.WithCancel(s.base)
}
