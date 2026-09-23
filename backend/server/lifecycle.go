package server

// Shutdown walks declared stages in order; a component registers its step where it is built.

import "context"

// stage is one step of the shutdown sequence. Its zero value is the first stage on purpose: a step that
// forgets to name its stage lands at the front, where a missing teardown wedges the run instead of
// looking like a clean exit.
type stage int

const (
	// stageDetachedWork first: cancelling the base context is the only step that asks work to stop itself
	// and the only one that cannot wedge. Everything that work may still touch — the database, the
	// container it streams from — stays alive for the rest of the sequence.
	stageDetachedWork stage = iota
	// stagePreviewSweeper next: the sweeper owns goroutines nothing else can reap.
	stagePreviewSweeper
	// stageWebsocketWatchers before anything a watcher can reach: it runs git and container work on its
	// own goroutines.
	stageWebsocketWatchers
	// stageTerminalSessions before the sandbox containers: a shell whose container is already gone is
	// a shell left running.
	stageTerminalSessions
	stageLSPHandler
	stageExtensionLSPManager
	stageExtensionSandboxManager
	// stageMemorySupervisor before the containers it can start.
	stageMemorySupervisor
	// stageSandboxStatsSampler before the containers it samples, or it reports a sandbox that no longer
	// exists.
	stageSandboxStatsSampler
	stageSandboxContainers
	stageLoginRateLimiter
	// stageDatabase last: every step above reads or writes through it.
	stageDatabase
	stageCount
)

// stageNames is indexed by stage; TestShutdownStagesAreDeclaredEntirely fails if the two disagree.
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

// Shutdown runs every registered step in the declared order, logging each name before running it: no
// step can be interrupted, so the last line in the log is what names a wedged shutdown. The deadline is
// RunShutdownWithDeadline's and is enforced by leaving, not by cancelling.
func (s *Server) Shutdown() error {
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

// RestartRequests is the channel the update restart route sends on, so a restart takes the declared
// stage list rather than a second one.
func (s *Server) RestartRequests() <-chan struct{} { return s.restart }
