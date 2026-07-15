<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { get } from 'svelte/store';
    import type { Provider, DiffData } from '$lib/types';
    import { PI_SENTINEL_URL, isAgentProvider } from '$lib/types';
    import { updateTask, moveTask, deleteTaskMessage, getTaskThreads, getTask, onQueueChanged } from '$lib/api';
    import { board as boardStore, config as configStore } from '$lib/stores/app';
    import { getStreamingContext, getTaskDiffStatsContext, getCommitQueueContext, getTaskEventContext } from '$lib/context';
    import { StreamState } from '$lib/services/stream-state.svelte';
    import { useCheckpointTracker } from '$lib/composables/use-checkpoint-tracker.svelte';
    import { useChatConnection } from '$lib/composables/use-chat-connection.svelte';
    import { useChatHistory } from '$lib/composables/use-chat-history.svelte';
    import { useChatActions } from '$lib/composables/use-chat-actions.svelte';
    import { computeLastUserMessageIds, type EntryMeta } from '$lib/utils/chat-entries';
    import { getDraftText, setDraftText, clearDraftText, loadDraftAttachments, saveDraftAttachments, clearDraftAttachments } from '$lib/storage';
    import ChatForm from './ChatForm.svelte';
    import ChatEntries from './ChatEntries.svelte';
    import ReviewBar from './ReviewBar.svelte';
    import StartPanel from './StartPanel.svelte';

    let { projectId, taskId, taskPrompt = '', taskStatus = 'backlog', taskExecutionState = '', disabled = false, taskTitle = '', hasStagedChanges: hasStagedChangesProp, onRefreshDiff, onMoveTask, onThreadSwitch, onViewChanges, onInvalidateDiff }: {
        projectId: string;
        taskId: string;
        taskPrompt?: string;
        taskStatus?: string;
        taskExecutionState?: string;
        disabled?: boolean;
        taskTitle?: string;
        hasStagedChanges?: boolean;
        onRefreshDiff?: (data: DiffData) => void;
        onMoveTask?: (status: string) => void;
        onThreadSwitch?: () => void;
        onViewChanges?: (commitHash: string) => void;
        onInvalidateDiff?: () => void;
    } = $props();

    // ── Stream state ──
    let stream = new StreamState();
    let entries = $derived(stream.state.entries);
    let currentAssistantId = $derived(stream.state.currentAssistantId);
    let activeToolCalls = $derived(stream.state.activeToolCalls);
    let processedToolIds = $derived(stream.state.processedToolIds);
    let pendingCheckpoints = $derived(stream.state.pendingCheckpoints);
    let checkpointLabels = $derived(stream.state.checkpointLabels);
    let checkpointCommitHashes = $derived(stream.state.checkpointCommitHashes);
    let checkpointHashesVersion = $derived(stream.state.checkpointHashesVersion);
    let awaitingResponse = $derived(stream.state.awaitingResponse);
    let queuePosition = $derived(stream.state.queuePosition);
    let queueEstimatedWait = $derived(stream.state.queueEstimatedWait);
    let promptProgress = $derived(stream.state.promptProgress);
    let contextState = $derived(stream.state.contextState);
    let streamTokenCount = $derived(stream.state.streamTokenCount);
    let lastPromptTokens = $derived(stream.state.lastPromptTokens);

    // ── Scroll state ──
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let scrollEl: HTMLElement | null = null;
    let atBottom = $state(true);
    let prevScrollHeight = 0;
    let userInputPending = false;
    let inputTimeout: ReturnType<typeof setTimeout> | null = null;
    let stickyUserMessage = $state('');
    let showHeader = $state(false);
    let scrollRafPending = false;

    // ── Input / draft state ──
    let inputValue = $state('');
    let draftAttachments = $state<Array<{ file: File; preview: string; mimeType: string; base64: string }>>([]);
    let draftSaveTimer: ReturnType<typeof setTimeout> | null = null;
    const DRAFT_SAVE_DEBOUNCE = 100;
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let chatFormComp: { focus: () => void } | null = null;

    // ── Model selection state ──
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');
    let operatingMode = $state('act');

    // ── Context stores (must be retrieved at top level) ──
    let streamingIdsContext = getStreamingContext();
    let commitQueueStatus = getCommitQueueContext();
    let taskEventContext = getTaskEventContext();
    let taskDiffStatsStore = getTaskDiffStatsContext();

    // ── Derived state ──
    let isLoading = $derived($streamingIdsContext.has(taskId));
    let lastEntryIdx = $derived(entries.length - 1);
    let diffStats = $derived($taskDiffStatsStore[taskId] || null);
    let hasStagedChanges = $derived(
        hasStagedChangesProp ?? (diffStats?.staged_paths || []).length > 0
    );
    let queueStatus = $derived($commitQueueStatus[projectId]?.taskID === taskId ? $commitQueueStatus[projectId].status : null);

    let showStartButton = $derived(taskStatus === 'backlog' && !isLoading);
    let showRestartButton = $derived(
        taskStatus === 'in_progress' &&
        !isLoading &&
        queuePosition === 0 &&
        entries.length > 0 &&
        entries[entries.length - 1].role === 'user'
    );
    let showResumeButton = $derived(
        taskStatus === 'review' &&
        !isLoading &&
        entries.length > 0 &&
        entries[entries.length - 1].role === 'user'
    );
    let lastUserMessageIds = $derived(computeLastUserMessageIds(entries));

    // ── Composables ──
    // History must be created first (provides loadHistory, threads, etc.)
    let history = useChatHistory({ projectId, taskId, stream, taskStatus, onThreadSwitch });

    // Connection uses history's loadHistory and thread state
    let conn = useChatConnection({
        projectId,
        taskId,
        stream,
        taskStatus,
        loadHistory: history.loadHistory,
        updateActiveThread: history.updateActiveThread,
        updateThreadUrl: history.updateThreadUrl,
        scroll,
        markUserInput,
        threads: history.threads,
        activeThreadId: history.activeThreadId,
        switchTaskThread: (pid, tid, threadId) => import('$lib/api').then(m => m.switchTaskThread(pid, tid, threadId)),
        getTask: (pid, tid) => getTask(pid, tid),
        taskEventContext,
    });

    // Actions uses both history and connection state
    let actions = useChatActions({
        projectId,
        taskId,
        stream,
        taskStatus,
        taskPrompt,
        disabled,
        selectedModel,
        selectedUrl,
        selectedReasoningEffort,
        operatingMode,
        providers,
        modelUpdatePending: false,
        threads: history.threads,
        activeThreadId: history.activeThreadId,
        isLoading,
        genId: conn.genId,
        scroll,
        markStreaming: conn.markStreaming,
        markNotStreaming: conn.markNotStreaming,
        loadHistory: history.loadHistory,
        loadThreads: history.loadThreads,
        updateActiveThread: history.updateActiveThread,
        updateThreadUrl: history.updateThreadUrl,
        resetStreamState: history.resetStreamState,
        mergeTaskIntoBoard: history.mergeTaskIntoBoard,
        onRefreshDiff,
        onThreadSwitch,
        onInvalidateDiff,
        onMoveTask,
        commitQueueStatus,
    });

    let isAgentSelected = $derived(() => {
        if (!selectedUrl) return false;
        if (selectedUrl === PI_SENTINEL_URL) return true;
        const p = providers.find(p => p.url === selectedUrl);
        return p ? isAgentProvider(p) : false;
    });

    let entryMetas = $derived(((() => {
        const metas: EntryMeta[] = new Array(entries.length);
        const streaming = isLoading;
        const cpStateMap = history.computeEntryMetas(
            entries, isLoading, pendingCheckpoints, checkpointLabels, checkpointCommitHashes
        );
        for (let i = 0; i < entries.length; i++) {
            metas[i] = cpStateMap[i];
        }
        void checkpointHashesVersion;
        return metas;
    })()));

    // ── Scroll functions ──
    function scroll(force = false) {
        if (!scrollEl) return;
        if (!force && !atBottom) return;
        requestAnimationFrame(() => {
            if (!scrollEl) return;
            scrollEl.scrollTop = scrollEl.scrollHeight;
            requestAnimationFrame(() => {
                if (scrollEl) {
                    scrollEl.scrollTop = scrollEl.scrollHeight;
                }
            });
        });
    }

    function onScroll() {
        markUserInput();
        if (scrollRafPending) return;
        scrollRafPending = true;
        requestAnimationFrame(doCheckScroll);
    }
    function doCheckScroll() {
        scrollRafPending = false;
        if (!scrollEl) return;
        const userMsgEls = scrollEl.querySelectorAll<HTMLElement>('.sticky-user-message');
        let latestScrolled: HTMLElement | null = null;
        const containerRect = scrollEl.getBoundingClientRect();
        for (const el of userMsgEls) {
            const firstLine = el.getAttribute('data-user-msg-first-line');
            if (!firstLine) continue;
            const rect = el.getBoundingClientRect();
            const elTop = rect.top - containerRect.top;
            if (elTop <= 0) {
                latestScrolled = el;
            }
        }
        stickyUserMessage = latestScrolled?.getAttribute('data-user-msg-first-line') || '';
        showHeader = !!stickyUserMessage;
        if (!userInputPending) return;
        if (inputTimeout) clearTimeout(inputTimeout);
        inputTimeout = setTimeout(() => { userInputPending = false; inputTimeout = null; }, 150);
        const gap = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight;
        if (scrollEl.scrollHeight < (prevScrollHeight || scrollEl.scrollHeight)) {
            const shrinkAmount = (prevScrollHeight || scrollEl.scrollHeight) - scrollEl.scrollHeight;
            atBottom = gap < shrinkAmount + 32;
        } else {
            atBottom = gap < 32;
        }
        prevScrollHeight = scrollEl.scrollHeight;
    }

    function markUserInput() {
        userInputPending = true;
    }

    function handleStickyClick() {
        if (!scrollEl || !stickyUserMessage) return;
        const userMsgEls = scrollEl.querySelectorAll<HTMLElement>('.sticky-user-message');
        for (let i = userMsgEls.length - 1; i >= 0; i--) {
            const firstLine = userMsgEls[i].getAttribute('data-user-msg-first-line');
            if (firstLine === stickyUserMessage) {
                userMsgEls[i].scrollIntoView({ behavior: 'smooth', block: 'start' });
                return;
            }
        }
    }

    // ── Draft persistence ──
    $effect(() => {
        getDraftText(taskId).then(savedDraft => {
            if (savedDraft !== null) inputValue = savedDraft;
        }).catch(() => {});
    });

    $effect(() => {
        loadDraftAttachments(taskId).then(atts => {
            if (atts.length > 0) {
                draftAttachments = atts.filter(a => a && a.mimeType && a.data).map(a => ({
                    file: new File([], '', { type: a.mimeType }),
                    preview: `data:${a.mimeType};base64,${a.data}`,
                    mimeType: a.mimeType,
                    base64: a.data,
                }));
            }
        }).catch(() => {});
    });

    function handleInputSave(val: string) {
        if (draftSaveTimer) clearTimeout(draftSaveTimer);
        if (val) {
            draftSaveTimer = setTimeout(() => {
                setDraftText(taskId, val).catch(() => {});
                draftSaveTimer = null;
            }, DRAFT_SAVE_DEBOUNCE);
        }
    }

    function handleClearDraft() {
        clearDraftText(taskId).catch(() => {});
        clearDraftAttachments(taskId).catch(() => {});
        draftAttachments = [];
    }

    $effect(() => {
        const atts = draftAttachments;
        if (atts.length > 0) {
            saveDraftAttachments(taskId, atts.map(a => ({ mimeType: a.mimeType, data: a.base64 })));
        }
    });

    // ── Model / provider management ──
    function updateProvidersFromStore() {
        const cfg = get(configStore);
        if (cfg && cfg.providers) {
            providers = cfg.providers;
            if (providers.length > 0 && (!selectedModel || !providers.some(p => p.url === selectedUrl))) {
                const firstProvider = providers[0];
                const _getModelName = (m: string | { name: string; reasoning_effort?: string }) => typeof m === 'string' ? m : m.name;
                if (firstProvider.default_model && firstProvider.models.some(m => _getModelName(m) === firstProvider.default_model)) {
                    selectedUrl = firstProvider.url;
                    selectedModel = firstProvider.default_model;
                } else if (firstProvider.models.length > 0) {
                    selectedUrl = firstProvider.url;
                    selectedModel = _getModelName(firstProvider.models[0]);
                }
            }
            updateContextLimit();
        }
    }

    function updateContextLimit() {
        let provider = providers.find(p => p.url === selectedUrl);
        if (!provider && selectedUrl === PI_SENTINEL_URL) {
            provider = providers.find(p => isAgentProvider(p));
        }
        if (!provider) {
            stream.state.contextState.limit = 0;
            return;
        }
        const model = provider.models.find((m: { name: string }) => m.name === selectedModel);
        stream.state.contextState.limit = (model as { context_window?: number })?.context_window || 0;
    }

    async function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        selectedReasoningEffort = reasoningEffort || '';
        updateContextLimit();
        const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && e.stats);
        if (lastAssistant?.stats) {
            stream.state.lastPromptTokens = (lastAssistant.stats.prompt_tokens || 0) + (lastAssistant.stats.predicted_tokens || 0);
            stream.state.contextState.used = stream.state.lastPromptTokens;
        }
        await actions.handleModelSelect(providerName, modelName, providerUrl, reasoningEffort);
    }

    function handleOperatingModeToggle() {
        actions.handleOperatingModeToggle((mode: string) => { operatingMode = mode; });
    }

    // ── Watch for task config changes from the board store ──
    $effect(() => {
        const boardTask = $boardStore?.tasks.find(t => t.id === taskId);
        if (boardTask) {
            if (!actions.modelUpdatePending && boardTask.llama_server_url) selectedUrl = boardTask.llama_server_url;
            if (!actions.modelUpdatePending && boardTask.model_name) selectedModel = boardTask.model_name;
            if (!actions.modelUpdatePending && boardTask.reasoning_effort) selectedReasoningEffort = boardTask.reasoning_effort;
            operatingMode = boardTask.operating_mode ?? 'act';
        }
    });

    // ── Stable callback handlers ──
    function handleMsgDelete(entry: any) {
        const idx = entries.findIndex(e => e.id === entry.id);
        if (idx === 0) return;
        actions.handleDeleteRequest(entry.id, entries.length - idx);
    }
    async function handleMsgFork(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        await actions.handleEditFork(msgId, origContent, newContent, atts);
    }
    async function handleMsgSend(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        await actions.handleEditSend(msgId, origContent, newContent, atts);
    }
    async function handleMsgEdit(msgId: string, newContent: string) {
        await updateTaskMessage(projectId, taskId, msgId, newContent);
        const entry = entries.find(e => e.id === msgId);
        if (entry) entry.content = newContent;
    }
    function handleMsgNavigateTask(targetTaskId: string) {
        const task = $boardStore?.tasks.find(t => t.id === targetTaskId);
        if (task) {
            const url = new URL(location.href);
            url.searchParams.set('task', targetTaskId);
            history.pushState(null, '', url);
            import('$lib/stores/app').then(({ selectedTask }) => selectedTask.set(task));
        }
    }

    // ── Mobile keyboard ──
    let keyboardOffset = $state(0);
    let chatFormRef: HTMLElement | null = $state(null);

    function setupViewportListener() {
        const vv = window.visualViewport;
        if (!vv) return;
        function onResize() {
            const diff = Math.max(0, window.innerHeight - vv.height);
            keyboardOffset = diff;
        }
        vv.addEventListener('resize', onResize);
        window.addEventListener('orientationchange', onResize);
        return () => {
            vv.removeEventListener('resize', onResize);
            window.removeEventListener('orientationchange', onResize);
        };
    }

    // ── Thread URL sync ──
    async function syncThreadFromUrl() {
        const urlThread = history.readThreadFromUrl();
        if (!urlThread) return;
        if (urlThread !== history.activeThreadId && history.threads.some(t => t.id === urlThread)) {
            await actions.handleThreadSwitch(urlThread);
        }
    }

    // ── Lifecycle ──
    let unsubQueue: (() => void) | null = null;

    onMount(() => {
        let unsubConfig: (() => void) | null = null;
        let unsubViewport: (() => void) | null | undefined = null;
        const handleUrlChange = () => syncThreadFromUrl();
        window.addEventListener('urlchanged', handleUrlChange);
        unsubConfig = configStore.subscribe(() => updateProvidersFromStore());
        unsubViewport = setupViewportListener();

        (async () => {
            updateProvidersFromStore();
            await history.loadThreads();
            // If URL has a thread param, switch to that thread before loading history
            const urlThread = history.readThreadFromUrl();
            if (urlThread && history.threads.some(t => t.id === urlThread)) {
                try {
                    const { switchTaskThread } = await import('$lib/api');
                    const switched = await switchTaskThread(projectId, taskId, urlThread);
                    history.updateActiveThread(switched.id);
                } catch (e) {
                    import('$lib/stores/dialogs').then(({ showToast }) => showToast(String(e)));
                }
            } else if (!urlThread) {
                if (!history.activeThreadId) {
                    const mainThread = history.threads.find(t => t.name === 'Main');
                    if (mainThread) {
                        try {
                            const { switchTaskThread } = await import('$lib/api');
                            const switched = await switchTaskThread(projectId, taskId, mainThread.id);
                            history.updateActiveThread(switched.id);
                        } catch (e) {
                            import('$lib/stores/dialogs').then(({ showToast }) => showToast(String(e)));
                        }
                    }
                }
            }
            // Load history from DB before connection setup
            await history.loadHistory(false);
            // Override model selection with task-specific values if set
            try {
                const task = await getTask(projectId, taskId);
                if (task.llama_server_url) selectedUrl = task.llama_server_url;
                if (task.model_name) selectedModel = task.model_name;
                if (task.reasoning_effort) selectedReasoningEffort = task.reasoning_effort;
            } catch { /* Task may not exist yet */ }
            // Initialize connection (subscribes to task events, connects)
            conn.setup();
            // Scroll to bottom after history is loaded and DOM has rendered
            scroll();
        })();

        unsubQueue = onQueueChanged(() => {
            for (const entry of entries) {
                if (entry.queued) entry.queued = false;
            }
        });

        return () => {
            window.removeEventListener('urlchanged', handleUrlChange);
            unsubConfig?.();
            unsubViewport?.();
        };
    });

    onDestroy(() => {
        if (inputTimeout) clearTimeout(inputTimeout);
        unsubQueue?.();
        if (draftSaveTimer) {
            clearTimeout(draftSaveTimer);
            draftSaveTimer = null;
        }
        if (inputValue) {
            setDraftText(taskId, inputValue).catch(() => {});
        }
    });

    // Save draft on page unload
    function handleBeforeUnload() {
        if (draftSaveTimer) {
            clearTimeout(draftSaveTimer);
            draftSaveTimer = null;
        }
        if (inputValue) {
            setDraftText(taskId, inputValue).catch(() => {});
        }
    }
    window.addEventListener('beforeunload', handleBeforeUnload);

    // Import used in handleMsgEdit
    async function updateTaskMessage(projectId: string, taskId: string, msgId: string, content: string) {
        const { updateTaskMessage: apiUpdate } = await import('$lib/api');
        await apiUpdate(projectId, taskId, msgId, content);
    }
</script>

<div class="task-chat">
    <!-- Live region for screen reader announcements -->
    <div class="sr-only" aria-live="polite" aria-atomic="true">
        {#if isLoading && !conn.connected}Connecting...{:else if isLoading}Agent is responding...{:else if conn.connected}Connected, waiting for messages{/if}
    </div>
    <ChatEntries
        {entries}
        {entryMetas}
        {lastEntryIdx}
        activeThreadId={history.activeThreadId}
        threads={history.threads}
        {awaitingResponse}
        connected={conn.connected}
        {queuePosition}
        {queueEstimatedWait}
        {promptProgress}
        {isAgentSelected}
        {showHeader}
        {stickyUserMessage}
        {taskStatus}
        {projectId}
        {taskId}
        onScroll={onScroll}
        onWheel={markUserInput}
        onTouchStart={markUserInput}
        onDismissError={(msgId) => conn.handleDismissError(msgId, deleteTaskMessage)}
        onRetryConnect={conn.handleRetryConnect}
        onThreadSwitch={actions.handleThreadSwitch}
        isSwitching={false}
        onMsgDelete={handleMsgDelete}
        onMsgFork={handleMsgFork}
        onMsgSend={handleMsgSend}
        onMsgEdit={handleMsgEdit}
        onRegenerate={actions.handleRegenerate}
        onNavigateTask={handleMsgNavigateTask}
        {onViewChanges}
        {scrollEl}
        onScrollElChange={(el) => scrollEl = el}
        onStickyClick={handleStickyClick}
    />
    {#if showStartButton}
        <StartPanel
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            {operatingMode}
            isStarting={actions.isStarting}
            {disabled}
            {isAgentSelected}
            onModelSelect={handleModelSelect}
            onOperatingModeToggle={handleOperatingModeToggle}
            onStartTask={actions.handleStartTask}
        />
    {:else}
        <div class="chat-form-wrapper" style="padding-bottom: {keyboardOffset}px;" bind:this={chatFormRef}>
        <ReviewBar
            {taskStatus}
            {showResumeButton}
            {showRestartButton}
            isStarting={actions.isStarting}
            isCommitting={actions.isCommitting}
            {disabled}
            hasStagedChanges={hasStagedChanges}
            {queueStatus}
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            {onMoveTask}
            onStartTask={actions.handleStartTask}
            onCommitAndDone={actions.handleCommitAndDone}
            onMoveToDone={actions.handleMoveToDone}
            onModelSelect={handleModelSelect}
        />
        {#if !showRestartButton}
        <ChatForm
            bind:this={chatFormComp}
            bind:value={inputValue}
            bind:attachments={draftAttachments}
            {disabled}
            {isLoading}
            placeholder="Send a message to the agent..."
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            contextUsed={stream.state.contextState.used}
            contextLimit={stream.state.contextState.limit}
            {operatingMode}
            {projectId}
            {taskId}
            onInput={handleInputSave}
            onClearDraft={handleClearDraft}
            onSubmit={actions.handleSend}
            onStop={actions.handleStop}
            onModelSelect={handleModelSelect}
            onOperatingModeToggle={handleOperatingModeToggle}
            onSummarize={actions.handleSummarize}
            onSummarizeCancel={actions.handleSummarizeCancel}
            isSummarizing={actions.isSummarizing}
        />
        {/if}
        </div>
    {/if}
</div>

<style>
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        padding: 0;
        margin: -1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
        border: 0;
    }

    .task-chat {
        display: flex;
        flex-direction: column;
        height: 100%;
        min-height: 0;
        overflow: hidden;
    }

    .chat-form-wrapper {
        flex-shrink: 0;
    }
</style>
