package exec

import (
	"fmt"
	"strings"

	"mike/backend/types"

	"mike/pkg/logging"
)

// acquireRunSlots takes the two admission gates a run holds for its whole life: the per-provider
// running-task slot, then the memory slot.
//
// The task slot is acquired BEFORE sandbox setup so a queued task doesn't hold a sandbox container
// while waiting; the memory slot follows it for the same reason — a queued task holds no container.
// Agent providers manage their own concurrency and are excluded from the task slot.
//
// It reports whether the run may proceed. A wait that was cancelled has already been reported by
// handleCancelledTask, so the caller only returns.
func (te *TaskExecutor) acquireRunSlots(setup *taskExecSetup) bool {
	if !te.configStore.IsAgentProvider(setup.serverURL) {
		if !te.acquireTaskSlot(setup, setup.serverURL) {
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return false
		}
	}
	if !te.acquireMemSlot(setup) {
		te.handleCancelledTask(setup.task.ID, setup.projectID)
		return false
	}
	return true
}

// openTaskSandbox creates or reuses the run's container, wires its tool callbacks onto the run's
// execution state, and returns the image it is running — the mid-run rebuilds need it. It reports
// whether the run may proceed; a failure is already on the task row.
func (te *TaskExecutor) openTaskSandbox(setup *taskExecSetup, state *executionState) (string, bool) {
	if !setup.sandboxEnabled {
		return "", true
	}
	// Resolve image and agent config dir early — needed for both initial creation and mid-execution recreation.
	imageToUse, agentClient, err := te.setupSandbox(setup)
	if err != nil {
		te.failTask(setup.task, setup.projectID, err.Error(), submoduleFailCode(err))
		return "", false
	}
	sb := sandboxState{operatingMode: setup.task.OperatingMode, agentConfigHash: te.agentGrantHash(setup.providerURL())}
	sb.exec, sb.execStream, sb.read, sb.write, sb.edit = makeSandboxCallbacks(setup.task.ID, agentClient)
	// Wire up bridge recovery — reconnects client or (re)creates sandbox.
	sb.recoverBridge = func() bool {
		return te.recoverBridgeForTask(te.sandboxMgr, setup, state, imageToUse, func() {
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID,
				types.TurnStatusEvent("running", "Sandbox bridge recovered"))
		})
	}
	state.setSandbox(func(dst *sandboxState) { *dst = sb })
	// The container exists now, so the budget counts it directly and the reservation made before
	// the create is no longer needed.
	te.confirmMemReservation(setup)
	return imageToUse, true
}

// newLoopContext decides the run's provider, reads the conversation the first call will be made
// from, and opens the loop state. `setup.task` ends up the row this run reads, which is why the
// conversation is read last: the first call must see the rows as they are now.
func (te *TaskExecutor) newLoopContext(setup *taskExecSetup) *loopContext {
	mc := &modelConfig{serverURL: setup.serverURL, modelName: setup.modelName}
	// A provider switch during the run changes mc, and container creation reads mc.
	setup.provider = mc
	providerName := te.configStore.GetProviderIDByURL(setup.serverURL)
	reasoningEffort := setup.task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(setup.modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(setup.serverURL)
	// One watcher for the whole run, so a provider switch mid-run keeps announcing instead of
	// leaving the last notice standing.
	silence := te.newStreamSilenceWatcher(setup.projectID, setup.task.ID)
	client := te.newLlamaClient(setup.serverURL, setup.modelName, reasoningEffort, apiKey, silence)
	tools := te.toolDefinitionsFor(setup.ctx, setup.task)

	// Context tracking: load context window from provider config, discover if unknown. Discovery is
	// a network round trip, so it runs before the conversation is read — reading first would leave
	// the rows stale by however long it took.
	var contextWindow *int
	if cw := te.configStore.GetModelContextWindow(setup.modelName, setup.serverURL); cw != nil {
		contextWindow = cw
	} else if discovered := te.configStore.DiscoverContextWindow(setup.modelName, setup.serverURL); discovered != nil {
		contextWindow = discovered
		te.configStore.SaveModelContextWindow(setup.modelName, setup.serverURL, *discovered)
		logging.For(logging.Exec).Infof("discovered context window for %s: %d", setup.modelName, *discovered)
	}

	// Read last, so the conversation is current when the first call is made.
	messages, updatedTask, storedPrompt := te.conversationFromDB(setup.task, setup.worktreePath, setup.promptScope())
	setup.task = updatedTask

	// Remove duplicate user messages that may occur during regeneration due to
	// race conditions between DELETE and POST handlers.
	if setup.regenerate {
		messages = removeDuplicateUserMessage(messages)
	}

	// Consolidate all mutable loop state into a single struct.
	lc := &loopContext{
		messages:       messages,
		prevArgLens:    make(map[string]int),
		detector:       newLoopDetector(),
		silence:        silence,
		mc:             mc,
		client:         client,
		providerName:   providerName,
		contextWindow:  contextWindow,
		sandboxEnabled: setup.sandboxEnabled,
		reportedPrompt: storedPrompt,
		tools:          tools,
	}

	// Carry over the accumulated session cost from prior runs/executors so the badge
	// reflects the full session cost across provider/model switches and executor restarts.
	lc.sessionCost = setup.task.SessionCost
	return lc
}

// refreshRunConfig applies what changed while this run was busy: a model or provider switch, an
// operating-mode change (which recreates the container with the other mount mode), and a changed
// agent sandbox grant. It reports whether the run must end — a lost slot or a container that could
// not be rebuilt, each already reported by the branch that hit it.
//
// A rebuild the options could not be read for leaves the running container alone and ends the run;
// the tool set follows the mode, so a rebuild that landed is what decides lc.tools.
func (te *TaskExecutor) refreshRunConfig(setup *taskExecSetup, lc *loopContext, state *executionState, imageToUse string) (stop bool) {
	// Check for model/provider changes before each LLM call.
	if updatedTask, err := te.store.GetTaskByID(setup.task.ID); err == nil {
		newURL := updatedTask.LlamaServerURL
		newModel := updatedTask.ModelName
		curURL, curModel := lc.mc.get()
		if newURL != "" && newURL != curURL || newModel != "" && newModel != curModel {
			// Skip model switch if target is agent — requires different executor
			if te.configStore.IsAgentProvider(newURL) {
				logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("skipping switch to agent (requires executor restart)")
			} else {
				if newURL == "" {
					newURL = curURL
				}
				if newModel == "" {
					newModel = curModel
				}
				switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, newURL, newModel, false, true, lc.silence)
				lc.client = switched.client
				lc.providerName = switched.providerName
				if switched.contextWindow != nil {
					lc.contextWindow = switched.contextWindow
				}
				lc.detector.reset() // model changed, clear stale text window
				lc.detector.clearHints()
				logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("url=%s model=%s context_window=%v", newURL, newModel, lc.contextWindow)
				// Provider (not just model) changed - move the running-task
				// slot to the new provider (may queue while it's saturated).
				if strings.TrimRight(newURL, "/") != strings.TrimRight(curURL, "/") {
					if !te.syncTaskSlot(setup, newURL) {
						te.handleCancelledTask(setup.task.ID, setup.projectID)
						return true
					}
				}
			}
		}

		// Check for operating mode change — recreate sandbox with correct mount mode
		if setup.sandboxEnabled && te.sandboxMgr != nil && te.sandboxMgr.ContainerIsRunning(setup.task.ID) {
			currentOperatingMode := state.sandbox().operatingMode
			newOperatingMode := updatedTask.OperatingMode
			if newOperatingMode != currentOperatingMode {
				logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("operating mode changed (%s → %s), recreating sandbox", currentOperatingMode, newOperatingMode)
				opts, optsErr := te.sandboxCreateOpts(setup, imageToUse, updatedTask.IsPlanMode())
				if optsErr != nil {
					logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("keeping the running sandbox through the mode change: %v", optsErr)
					return true
				}
				if err := te.recreateTaskSandbox(setup, state, sandboxRecreate{
					reason:      "mode change",
					connErrCode: "agent_connection_failed",
					opts:        opts,
					underLock:   func(sb *sandboxState) { sb.operatingMode = newOperatingMode },
				}); err != nil {
					return true
				}
				// Update setup.task reference with latest mode
				setup.task.OperatingMode = updatedTask.OperatingMode
				lc.tools = te.toolDefinitionsFor(setup.ctx, updatedTask)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", fmt.Sprintf("Sandbox recreated for mode change (%s)", newOperatingMode)))
			}
		}
	}

	// A folder the user just removed counts as a change.
	if setup.sandboxEnabled && te.sandboxMgr != nil && te.sandboxMgr.ContainerIsRunning(setup.task.ID) {
		currentHash := state.sandbox().agentConfigHash
		newHash := te.agentGrantHash(setup.providerURL())
		if newHash != currentHash {
			logging.For(logging.Sandbox).With(logging.TaskID, setup.task.ID).Infof("agent sandbox config changed, recreating sandbox")
			opts, optsErr := te.sandboxCreateOpts(setup, imageToUse, setup.task.IsPlanMode())
			if optsErr != nil {
				logging.For(logging.Sandbox).With(logging.TaskID, setup.task.ID).Warnf("keeping the running sandbox through the extension config change: %v", optsErr)
				return true
			}
			if err := te.recreateTaskSandbox(setup, state, sandboxRecreate{
				reason:      "config change",
				connErrCode: "agent_config_failed",
				opts:        opts,
				underLock:   func(sb *sandboxState) { sb.agentConfigHash = newHash },
			}); err != nil {
				return true
			}
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Sandbox recreated for extension config change"))
		}
	}
	return false
}
