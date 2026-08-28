<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { StreamEvent, StreamEventType, Provider, Thread, ChatEntry, DiffData } from '$lib/types';
    import { AGENT_URL_PREFIX, isAgentProvider } from '$lib/types';
    import { sendTaskMessage, getTaskMessages, createTaskMessage, deleteTaskMessagesAfter, deleteTaskMessage, regenerateTaskMessages, updateTaskMessage, stopTask, updateTask, moveTask, getTaskThreads, switchTaskThread, forkTaskThread, checkMessageDelete, commitAndDone, summarizeTask, compactTask, getOfflineStatus, onQueueChanged, getQueuedTaskIds, getTask } from '$lib/api';
    import { appStore, uiStore, dialogStore, updateTaskInBoard } from '$lib/stores';
    import { getStreamingContext, getTaskDiffStatsContext, getCommitQueueContext, getTaskEventContext } from '$lib/context';
    import { StreamState } from '$lib/services/stream-state.svelte';
    import { createStreamHandler } from '$lib/services/stream-handler';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    // UI extension injection point — re-render when extensions change
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.TASK_CHAT);
        uiInjector.refreshSlot(INJECTION_POINTS.TASK_CHAT, { component: 'TaskChat', taskId, projectId });
    });
    import { DRAFT_SAVE_DEBOUNCE_MS } from '$lib/constants';
    import { mergeQueuedUpdate } from '$lib/utils/task-utils';
    import { effectiveStartTaskKey, matchShortcut } from '$lib/utils/keyboard';
    import { resolveWorker, getModelName } from '$lib/utils/providers';
    import { mergeToolCallsIntoAssistant, getCheckpointThreads as getCheckpointThreadsUtil, buildEntriesFromMessages, buildCheckpointStateMap, computeLastUserMessageIds, findLastUserMessageBefore, trimEntriesForRegenerate, type EntryMeta } from '$lib/utils/chat-entries';
    import { getAppStreamRef } from '$lib/stream-ref';
    import { wsConnection } from '$lib/stores/connection';
    import { getDraftText, setDraftText, clearDraftText, loadDraftAttachments, saveDraftAttachments, clearDraftAttachments } from '$lib/storage';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';
    import ChatForm from './ChatForm.svelte';
    import ChatEntries from './ChatEntries.svelte';
    import ReviewBar from './ReviewBar.svelte';
    import StartPanel from './StartPanel.svelte';

    let { projectId, taskId, taskPrompt = '', taskStatus = 'backlog', taskExecutionState = '', disabled = false, taskTitle = '', taskSummarizing = false, hasStagedChanges: hasStagedChangesProp, onRefreshDiff, onMoveTask, onThreadSwitch, onViewChanges, onInvalidateDiff, onToggleFullscreen, isChatFullscreen }: { projectId: string; taskId: string; taskPrompt?: string; taskStatus?: string; taskExecutionState?: string; disabled?: boolean; taskTitle?: string; taskSummarizing?: boolean; hasStagedChanges?: boolean; onRefreshDiff?: (data: DiffData) => void; onMoveTask?: (status: string) => void; onThreadSwitch?: () => void; onViewChanges?: (commitHash: string) => void; onInvalidateDiff?: () => void; onToggleFullscreen?: () => void; isChatFullscreen?: boolean } = $props();

    let connected = $state(false);
    let connecting = $state(true);
    // Owns everything about "follow the latest message" (scroll container,
    // atBottom, pinning). Shared with ChatEntries, which renders both.
    const follow = createChatFollow();
    // True while the initial conversation history is being fetched
    let historyLoading = $state(false);
    let inputValue = $state('');
    let draftAttachments = $state<Array<{ file: File; preview: string; mimeType: string; base64: string }>>([]);

    // Load draft text from IndexedDB on init
    $effect(() => {
        getDraftText(taskId).then(savedDraft => {
            if (savedDraft !== null) {
                inputValue = savedDraft;
            }
        }).catch(() => {});
    });

    // Load draft attachments from IndexedDB on init
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

    // Save draft text to IndexedDB (debounced to avoid writes on every keystroke)
    let draftSaveTimer: ReturnType<typeof setTimeout> | null = null;
    function handleInputSave(val: string) {
        if (draftSaveTimer) clearTimeout(draftSaveTimer);
        if (val) {
            draftSaveTimer = setTimeout(() => {
                setDraftText(taskId, val).catch(() => {});
                draftSaveTimer = null;
            }, DRAFT_SAVE_DEBOUNCE_MS);
        }
    }

    // Clear draft on send (called synchronously from ChatForm)
    function handleClearDraft() {
        clearDraftText(taskId).catch(() => {});
        clearDraftAttachments(taskId).catch(() => {});
        draftAttachments = [];
    }

    // Save draft attachments; skip empty to avoid racing with load effect on mount
    $effect(() => {
        const atts = draftAttachments;
        if (atts.length > 0) {
            saveDraftAttachments(taskId, atts.map(a => ({ mimeType: a.mimeType, data: a.base64 })));
        }
    });

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');
    let operatingMode = $state('act');
    // Tracks when local updates to task config are in-flight, so the board store
    // $effect doesn't overwrite the user's selection with stale DB values.
    let isUpdatingTask = $state(false);
    let isAgentSelected = $derived(() => {
        if (!selectedUrl) return false;
        if (selectedUrl.startsWith(AGENT_URL_PREFIX)) return true;
        const p = providers.find(p => p.url === selectedUrl);
        return p ? isAgentProvider(p) : false;
    });

    // Stream state — managed by StreamState service (reactive via $state())
    let stream = new StreamState();
    // Shorthand accessors for template use
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
    let retryCount = $derived(stream.state.retryCount);
    let retryError = $derived(stream.state.retryError);
    let contextState = $derived(stream.state.contextState);
    let streamTokenCount = $derived(stream.state.streamTokenCount);
    let lastPromptTokens = $derived(stream.state.lastPromptTokens);

    let lastSentMessage: string | null = $state(null);
    let lastSentEntryId: string | null = $state(null); // temp ID of last optimistic entry
    let isStarting = $state(false);
    let isRegenerating = $state(false); // true between regenerate delete and echo arrival
    let firstConnectDone = $state(false); // track if initial onopen has fired
    let isCommitting = $state(false);

    let threadSwitching = $state(false); // true while a thread switch is in-flight
    // Suppress buffer replay for non-running tasks to avoid flicker from stale entries.
    // For in_progress tasks, buffer events are legitimate and must be replayed.
    let suppressEvents = $state(true);
    $effect(() => { suppressEvents = taskStatus !== 'in_progress'; });
    let commitQueueStatus = getCommitQueueContext();
    let taskEventContext = getTaskEventContext();
    let queueStatus = $derived($commitQueueStatus[projectId]?.taskID === taskId ? $commitQueueStatus[projectId].status : null);

    // Thread state
    let threads: Thread[] = $state([]);
    let activeThreadId = $state('');

    let stopping = $state(false);
    let isSummarizing = $derived(taskSummarizing);
    // isLoading: true when LLM is actively streaming tokens OR task is in running state.
    // After page refresh, streamingIds is empty but execution_state preserves running state.
    let streamingIds = getStreamingContext();
    // streamingIds is from context (not appStore), accessed via $derived with context auto-subscription
    let isLoading = $derived(!stopping && ($streamingIds.has(taskId) || taskExecutionState === 'running'));

    // Last entry index for regenerate button gating
    let lastEntryIdx = $derived(entries.length - 1);

    // Show "Start Task" button when task is in backlog (not yet started).
    let showStartButton = $derived(taskStatus === 'backlog' && !isLoading);

    // Last entry ignoring client-side error notices (connection lost / queued).
    // Error entries are appended after the message they relate to, so the
    // Resume/Restart conditions must look past them.
    let lastNonErrorEntry = $derived([...entries].reverse().find(e => e.role !== 'error'));

    // Paused: the task was stopped while in progress (stays in the In
    // Progress column). The ReviewBar shows a Resume button over the input.
    let isPaused = $derived(taskExecutionState === 'paused');

    // Show "Restart Task" button when task is in_progress, stalled (no active LLM, not queued),
    // has at least one assistant response (was actually started before),
    // and the last message is from the user (agent hasn't responded yet).
    // Hidden while paused (the Resume bar replaces it) and during summarization
    // so the ChatForm (with spinner) stays visible.
    let showRestartButton = $derived(
        taskStatus === 'in_progress' &&
        !isLoading &&
        !isPaused &&
        !isSummarizing &&
        queuePosition === 0 &&
        lastNonErrorEntry !== undefined &&
        lastNonErrorEntry.role === 'user' &&
        entries.some(e => e.role === 'assistant')
    );

    // Show "Resume" button in review bar when the last message is from the user
    // (agent hasn't responded yet — user sent a follow-up before moving to review).
    // Also shows when a follow-up message was queued due to a network failure:
    // the outgoing message stays the last entry, so "Resume" is the recovery
    // action once the connection is restored.
    // Hidden during summarization so the ChatForm (with spinner) stays visible.
    let showResumeButton = $derived(
        taskStatus === 'review' &&
        !isLoading &&
        !isSummarizing &&
        lastNonErrorEntry !== undefined &&
        lastNonErrorEntry.role === 'user'
    );

    // Set of user message IDs with no assistant response after them.
    let lastUserMessageIds = $derived(computeLastUserMessageIds(entries));

    // Precomputed per-entry metadata — O(n) single-pass via buildCheckpointStateMap.
    let entryMetas = $derived(((() => {
        const metas: EntryMeta[] = new Array(entries.length);
        const streaming = isLoading;
        const cpStateMap = buildCheckpointStateMap(entries, pendingCheckpoints, checkpointLabels, checkpointCommitHashes);
        const activeThread = threads.find(t => t.is_active);
        // Count all checkpoints (oldest first) for stash index mapping.
        // Even checkpoints without commitHash get an index — the backend resolves
        // to the matching stash by index when headRef equals baseRef.
        let cpIdx = 0;
        for (let i = 0; i < entries.length; i++) {
            const entry = entries[i];
            const cpState = entry.role === 'user' ? (cpStateMap.get(i) ?? null) : null;
            const idx = cpState ? cpIdx++ : -1;
            metas[i] = entry.role === 'user'
                ? {
                    cpState,
                    cpThread: threads.length > 0 ? getCheckpointThreadsUtil(entry, threads, activeThread, i === 0) : null,
                    checkpointIndex: idx,
                    isLastUserMessage: lastUserMessageIds.has(entry.id) && !streaming,
                }
                : { cpState: null, cpThread: null, checkpointIndex: -1, isLastUserMessage: false };
        }
        // Force tracking of checkpointHashesVersion so meta rebuilds on checkpoint changes
        void checkpointHashesVersion;
        return metas;
    })()));

    function genId(): string {
        return Math.random().toString(36).slice(2, 10);
    }

    // Stable callback handlers — prevents unnecessary ChatMessage re-renders.
    function handleMsgDelete(entry: ChatEntry) {
        const idx = entries.findIndex(e => e.id === entry.id);
        if (idx === 0) return; // first message cannot be deleted
        handleDeleteRequest(entry.id, entries.length - idx);
    }
    async function handleMsgFork(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        await handleEditFork(msgId, origContent, newContent, atts);
    }
    async function handleMsgSend(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        await handleEditSend(msgId, origContent, newContent, atts);
    }
    async function handleMsgEdit(msgId: string, newContent: string) {
        await updateTaskMessage(projectId, taskId, msgId, newContent);
        const entry = entries.find(e => e.id === msgId);
        if (entry) entry.content = newContent;
    }
    function handleMsgNavigateTask(targetTaskId: string) {
        const task = appStore.board?.tasks.find(t => t.id === targetTaskId);
        if (task) {
            const url = new URL(location.href);
            url.searchParams.set('task', targetTaskId);
            history.pushState(null, '', url);
            appStore.selectedTask = task;
        }
    }

    // Watch for task config changes from the app store (e.g., inline edit)
    // Updates model selection when the task's model/provider is changed externally.
    // Skips model fields when a local updateTask is in-flight to avoid overwriting
    // the user's selection with stale DB values.
    $effect(() => {
        const boardTask = appStore.board?.tasks.find(t => t.id === taskId);
        if (boardTask) {
            let changed = false;
            if (!isUpdatingTask && boardTask.llama_server_url && boardTask.llama_server_url !== selectedUrl) {
                selectedUrl = boardTask.llama_server_url;
                changed = true;
            }
            if (!isUpdatingTask && boardTask.model_name && boardTask.model_name !== selectedModel) {
                selectedModel = boardTask.model_name;
                changed = true;
            }
            if (!isUpdatingTask && boardTask.reasoning_effort) selectedReasoningEffort = boardTask.reasoning_effort;
            if (!isUpdatingTask) operatingMode = boardTask.operating_mode ?? 'act';
            if (changed) updateContextLimit();
        }
    });

    /** Read the `thread` URL param from the current URL. */
    function readThreadFromUrl(): string | null {
        return new URLSearchParams(location.search).get('thread');
    }

    /** Update the URL to include `&thread=<threadId>`, pushing to browser history. */
    function updateThreadUrl(threadId: string) {
        const url = new URL(location.href);
        url.searchParams.set('thread', threadId);
        history.pushState({ task: taskId, thread: threadId }, '', url.toString());
    }

    /** Remove the `thread` param from the URL. */
    function clearThreadUrl() {
        const url = new URL(location.href);
        url.searchParams.delete('thread');
        history.pushState({ task: taskId }, '', url.toString());
    }

    /** Reset stream state before loading a new thread's messages. */
    function resetStreamState() {
        stream.closeCurrentAssistant();
        stream.state.activeToolCalls = new Map();
        stream.state.processedToolIds = new Set();
        stream.state.entries = [];
        stream.state.currentAssistantId = null;
        stream.state.streamTokenCount = 0;
        stream.state.awaitingResponse = false;
    }

    /** Merge an updated task into the app store. */
    function mergeTaskIntoBoard(updated: any) {
        const existing = appStore.board?.tasks.find(t => t.id === taskId);
        const merged = existing ? mergeQueuedUpdate(existing, updated) : updated;
        updateTaskInBoard(appStore, taskId, merged);
    }

    /** Result of a send: the created/optimistic message, or null on failure. */
    type SendResult = { id: string; role: string; content: string; thread_id?: string; queued?: boolean; actionId?: string };

    /** Send a message via the API with tracking state. Returns the message result, or null on failure. */
    async function sendWithTracking(msg: string, attachments?: Array<{ mimeType: string; data: string }>, regenerate = false, entryId?: string): Promise<SendResult | null> {
        lastSentMessage = msg;
        lastSentEntryId = entryId ?? null;
        stream.state.awaitingResponse = true;
        try {
            return await sendTaskMessage(projectId, taskId, msg, selectedModel, selectedUrl, attachments, selectedReasoningEffort || undefined, regenerate);
        } catch {
            stream.state.awaitingResponse = false;
            return null;
        }
    }

    /** Error codes that are client-side only — never persisted to the DB. */
    const LOCAL_ERROR_CODES = new Set(['send_queued', 'ws_disconnected']);

    /** Append a client-side error entry (visible but never persisted to the DB). */
    function appendErrorEntry(code: string, message: string) {
        stream.state.entries = [...stream.state.entries, {
            id: genId(),
            role: 'error',
            content: message,
            isStreaming: false,
            error: { code, message },
            createdAt: new Date().toISOString(),
        }];
    }

    /**
     * A send was queued due to a network failure. Mark the optimistic entry as
     * queued and append a visible error notice. The notice lives only in the UI
     * (never persisted to the DB) and is cleared when the queue flushes.
     */
    function handleQueuedSend(content: string, entryId?: string) {
        const entry = entryId
            ? stream.state.entries.find(e => e.id === entryId)
            : [...stream.state.entries].reverse().find(e => e.role === 'user' && e.content === content);
        if (entry) entry.queued = true;
        appendErrorEntry('send_queued', 'Connection lost — message queued and will be sent when the connection is restored');
    }

    function markStreaming() {
        streamingIds.update(s => { const n = new Set(s); n.add(taskId); return n; });
    }

    function markNotStreaming() {
        streamingIds.update(s => { const n = new Set(s); n.delete(taskId); return n; });
        stream.state.queuePosition = 0;
        stream.state.queueEstimatedWait = 0;
        stream.state.streamTokenCount = 0;
        stream.state.contextState = { ...stream.state.contextState, used: stream.state.lastPromptTokens };
        stream.state.promptProgress = 0;
    }

    // Stream event handler — delegated to service
    const _handleStreamEvent = createStreamHandler(stream, {
        genId,
        scroll,
        markStreaming,
        markNotStreaming,
        onRefreshDiff: (data) => onRefreshDiff?.(data),
        onThreadSwitch: () => onThreadSwitch?.(),
        loadHistory,
        updateActiveThread,
        updateThreadUrl,
        createTaskMessage: (p, t, r, c) => createTaskMessage(p, t, r, c).then(() => {}),
    });

    /** Wrapper around stream handler with component-specific echo logic. */
    function handleEvent(event: StreamEvent) {
        // Handle user_message echo before delegating to stream handler
        if (event.type === 'user_message') {
            const data = event.data as { id?: string; role: string; content: string; thread_id?: string; attachments?: Array<{ mimeType: string; data: string }> };
            if (lastSentMessage === data.content) {
                lastSentMessage = null;
                lastSentEntryId = null;
                isRegenerating = false;
                if (data.id) {
                    // Match by optimistic entry ID when available (reliable),
                    // fall back to content match for backward compat (e.g. regenerate).
                    if (lastSentEntryId) {
                        const entry = stream.state.entries.find(e => e.id === lastSentEntryId);
                        if (entry) {
                            entry.id = data.id;
                            if (data.thread_id) entry.threadId = data.thread_id;
                            if (data.attachments) entry.attachments = data.attachments;
                        }
                    } else {
                        for (let i = stream.state.entries.length - 1; i >= 0; i--) {
                            if (stream.state.entries[i].role === 'user' && stream.state.entries[i].content === data.content) {
                                stream.state.entries[i].id = data.id;
                                if (data.thread_id) stream.state.entries[i].threadId = data.thread_id;
                                if (data.attachments) stream.state.entries[i].attachments = data.attachments;
                                break;
                            }
                        }
                    }
                }
                return;
            }
        }
        // Handle error event side effects (isRegenerating, connected)
        if (event.type === 'error') {
            isRegenerating = false;
            connected = false;
        }
        _handleStreamEvent(event, suppressEvents, projectId, taskId);
    }

    /** Update providers from the app store (reacts to settings changes). */
    function updateProvidersFromStore() {
        const cfg = appStore.config;
        if (cfg && cfg.providers) {
            providers = cfg.providers;
            // Initialize model selection from the configured Worker provider/model pair
            if (providers.length > 0 && (!selectedModel || !providers.some(p => p.url === selectedUrl))) {
                const worker = resolveWorker(cfg);
                if (worker.provider && worker.modelName) {
                    selectedUrl = worker.url;
                    selectedModel = worker.modelName;
                } else if ((providers[0].models?.length ?? 0) > 0) {
                    selectedUrl = providers[0].url;
                    selectedModel = getModelName(providers[0].models[0]);
                }
            }
            updateContextLimit();
        }
    }

    /** Look up the context window for the currently selected model. */
    function updateContextLimit() {
        let provider = providers.find(p => p.url === selectedUrl);
        // Agent: selectedUrl starts with AGENT_URL_PREFIX, look up the agent provider
        if (!provider && selectedUrl.startsWith(AGENT_URL_PREFIX)) {
            provider = providers.find(p => isAgentProvider(p));
        }
        if (!provider) {
            stream.state.contextState = { ...stream.state.contextState, limit: 0 };
            return;
        }
        const model = provider.models.find((m: { name: string }) => m.name === selectedModel);
        stream.state.contextState = { ...stream.state.contextState, limit: (model as { context_window?: number })?.context_window || 0 };
    }

    async function loadHistory(scrollAfter = false) {
        try {
            const messages = await getTaskMessages(projectId, taskId);
            // Build entries from DB messages using shared utility
            const { entries: mergedDbEntries, checkpointHashes } = buildEntriesFromMessages(messages);
            stream.state.checkpointCommitHashes = checkpointHashes;

            // Merge with existing entries (e.g., from buffer replay).
            // DB entries have backend UUIDs; buffer entries have random genId() IDs.
            // Preserve buffer entries whose IDs are NOT in the DB (not yet persisted).
            const dbIds = new Set(messages.map(m => m.id));
            const existingNonDb = stream.state.entries.filter(e => !dbIds.has(e.id));
            // Collect tool_ids from DB entries to deduplicate buffer entries.
            const dbToolIds = new Set<string>();
            for (const e of mergedDbEntries) {
                if (e.toolCalls) {
                    for (const tc of e.toolCalls) {
                        dbToolIds.add(tc.toolId);
                    }
                }
            }
            // Remove buffer entries whose tool calls are already in the DB.
            // Also remove buffer assistant entries that are duplicates of DB entries:
            // - Buffer entry has tool calls with IDs matching DB → removed by tool_id check
            // - Buffer entry has reasoning but no tool calls (tool_call events evicted from buffer)
            //   and DB has assistant with tool calls → removed as reasoning-only duplicate
            // - Buffer entry has both reasoning AND tool calls but same tool IDs as DB → removed
            const dbHasToolCalls = dbToolIds.size > 0;
            const dedupedNonDb = existingNonDb.filter(e => {
                if (e.toolCalls && e.toolCalls.length > 0) {
                    return !e.toolCalls.some(tc => dbToolIds.has(tc.toolId));
                }
                // Remove buffer assistant with reasoning (and optionally empty toolCalls)
                // if DB has assistant entries with tool calls for the same turn.
                if (dbHasToolCalls && e.role === 'assistant' && e.reasoningContent) {
                    return false;
                }
                return true;
            });
            stream.state.entries = [...mergedDbEntries, ...dedupedNonDb];
            // Deduplicate by ID — keeps first occurrence (DB entries come first and are authoritative).
            // Catches edge cases where a streaming entry's temp ID was swapped to a backend ID
            // that already exists in the DB (e.g., complete event arrives after loadHistory).
            const seenIds = new Set<string>();
            stream.state.entries = stream.state.entries.filter(e => {
                if (seenIds.has(e.id)) return false;
                seenIds.add(e.id);
                return true;
            });
            // Remove entries with pending tool calls from non-running tasks.
            // For running tasks (in_progress), pending tool calls are in-flight and should be kept.
            // For stopped/completed tasks, pending calls are stale leftovers.
            if (taskStatus !== 'in_progress') {
                stream.state.entries = stream.state.entries.filter(e => !e.toolCalls?.some(tc => tc.isPending));
            }
            // These are new entry objects, so refs captured while streaming are now
            // detached; re-point them before more events for this turn arrive.
            stream.reconcileActiveToolCalls();
            // Restore context usage from the last assistant message.
            // Use total context (prompt + predicted) to reflect full window consumed.
            const lastAssistant = [...stream.state.entries].reverse().find(e => e.role === 'assistant' && e.stats);
            if (lastAssistant?.stats) {
                stream.state.lastPromptTokens = (lastAssistant.stats.prompt_tokens || 0) + (lastAssistant.stats.predicted_tokens || 0);
                stream.state.contextState = { ...stream.state.contextState, used: stream.state.lastPromptTokens };
            }
            // The DOM re-renders after the entries are replaced, so pin on the
            // next frame.
            if (scrollAfter) {
                await new Promise(resolve => requestAnimationFrame(resolve));
                scroll();
            }
        } catch (e) {
            // Task may have been deleted — clear stale draft
            if (e instanceof Error && (e.message.includes('Not found') || e.message.includes('404'))) {
                clearDraftText(taskId).catch(() => {});
                draftAttachments = [];
            }
            // Otherwise: no history yet, ignore
        }
    }

    async function loadThreads() {
        try {
            threads = await getTaskThreads(projectId, taskId);
            const active = threads.find(t => t.is_active);
            if (active) {
                activeThreadId = active.id;
            }
        } catch {
            threads = [];
        }
    }

    /** Update local thread state to reflect a new active thread (avoids refetching all threads). */
    function updateActiveThread(threadId: string) {
        activeThreadId = threadId;
        threads = threads.map(t => ({ ...t, is_active: t.id === threadId }));
    }

    async function handleThreadSwitch(threadId: string) {
        if (threadId === activeThreadId) return;
        // Prevent concurrent thread switches — wait for the in-flight one to settle.
        if (threadSwitching) return;
        threadSwitching = true;
        try {
            const switched = await switchTaskThread(projectId, taskId, threadId);
            // Refresh threads list from server to pick up any changes (deletions, etc.)
            threads = await getTaskThreads(projectId, taskId);
            activeThreadId = switched.id;
            // Clear streaming state before loading new thread's messages.
            resetStreamState();
            // Load messages for the new thread immediately (don't wait for WS event)
            await loadHistory(true);
            // Notify parent to refresh the diff panel for the new thread
            onThreadSwitch?.();
            // Update URL with new thread param
            updateThreadUrl(threadId);
            // No need to reconnect WebSocket — broadcaster is per-task, not per-thread.
            // Server sends thread_switch + diff_changed events over existing connection.
        } catch (e) {
            dialogStore.showToast(String(e));
        } finally {
            threadSwitching = false;
        }
    }

    async function handleThreadFork(messageId: string, fromThreadId: string) {
        if (isLoading || disabled) return;
        try {
            const forked = await forkTaskThread(projectId, taskId, fromThreadId, messageId);
            // Refresh threads list from server to ensure consistency
            threads = await getTaskThreads(projectId, taskId);
            activeThreadId = forked.id;
            // Clear streaming state before loading new thread's messages.
            resetStreamState();
            // Load messages for the new thread immediately
            await loadHistory(true);
            // Notify parent to refresh the diff panel for the new forked thread
            onThreadSwitch?.();
            // Update URL with the new fork's thread ID
            updateThreadUrl(forked.id);
            // No need to reconnect WebSocket — broadcaster is per-task, not per-thread.
        } catch (e) {
            dialogStore.showToast(String(e));
        }
    }

    /**
     * Follow the latest content. Forced only for user-initiated actions (send,
     * edit, regenerate); never for stream events, so a reader who scrolled up
     * keeps their position.
     */
    function scroll(force = false) {
        follow.pin(force);
    }

    async function handleSend(msg: string, attachments?: Array<{ mimeType: string; data: string }>) {
        if ((!msg && (!attachments || attachments.length === 0)) || disabled) return;
        // If LLM is streaming, cancel the current response by clearing partial assistant entry.
        if (isLoading) {
            stream.state.entries = stream.state.entries.filter(e => !e.isStreaming);
            if (currentAssistantId) {
                stream.state.currentAssistantId = null;
            }
            stream.state.activeToolCalls = new Map();
                    stream.state.processedToolIds = new Set();
            markNotStreaming();
        }
        // If task is in Done, move it back to In Progress before sending
        if (taskStatus === 'done') {
            try {
                const updated = await moveTask(projectId, taskId, 'in_progress');
                mergeTaskIntoBoard(updated);
            } catch (e) {
                dialogStore.showToast(String(e));
                return;
            }
        }
        // Remove all error entries (cleared by next user action)
        stream.state.entries = stream.state.entries.filter(e => e.role !== 'error');
        // Add message optimistically with current thread context
        const entryId = genId();
        const isOfflineNow = getOfflineStatus();
        const isQueuedForSummarize = isSummarizing;
        stream.state.entries = [...stream.state.entries, { id: entryId, role: 'user', content: msg, attachments: attachments || undefined, threadId: activeThreadId, isStreaming: false, createdAt: new Date().toISOString(), queued: isOfflineNow || isQueuedForSummarize }];
        scroll(true);
        // If queued for summarization, don't show awaiting spinner — executor hasn't started yet.
        // The executor will be started after summarization completes and process this message.
        if (isQueuedForSummarize) {
            stream.state.awaitingResponse = false;
        }
        // Send with model info for lazy-start
        const result = await sendWithTracking(msg, attachments, false, entryId);
        if (result === null) {
            // Permanent failure (e.g. server error) — remove the optimistic message and show an error.
            stream.state.entries = stream.state.entries.filter(e => !(e.role === 'user' && e.content === msg));
            appendErrorEntry('send_failed', 'Failed to send message');
            return;
        }
        if (result.queued) {
            // Network failure — the message was queued (not lost). Keep it as an
            // outgoing message and surface a visible, non-persisted notice.
            handleQueuedSend(msg, entryId);
        }
        scroll(true);
        chatFormComp?.focus();
    }

    async function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        selectedReasoningEffort = reasoningEffort || '';
        updateContextLimit();
        // Recalculate context usage from conversation history when switching providers.
        // This ensures the context bar reflects the correct usage for the new provider.
        const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && e.stats);
        if (lastAssistant?.stats) {
            stream.state.lastPromptTokens = (lastAssistant.stats.prompt_tokens || 0) + (lastAssistant.stats.predicted_tokens || 0);
            stream.state.contextState = { ...stream.state.contextState, used: stream.state.lastPromptTokens };
        }
        // Agent providers always use explore mode; reset to act when switching away
        const isAgent = isAgentSelected();
        const updates: Record<string, string | undefined> = {
            model_name: modelName,
            llama_server_url: providerUrl,
            reasoning_effort: reasoningEffort || undefined,
        };
        if (isAgent) {
            operatingMode = 'explore';
            updates.operating_mode = 'explore';
        } else if (operatingMode === 'explore') {
            operatingMode = 'act';
            updates.operating_mode = 'act';
        }
        // Update task with new model/provider
        isUpdatingTask = true;
        try {
            await updateTask(projectId, taskId, updates);
        } catch (e) {
            console.error('Failed to update task model:', e);
        } finally {
            isUpdatingTask = false;
        }
    }

    async function handleOperatingModeToggle() {
        const modes = ['act', 'plan', 'explore'];
        const prevMode = operatingMode;
        const nextIdx = (modes.indexOf(operatingMode) + 1) % modes.length;
        const newMode = modes[nextIdx];
        operatingMode = newMode;
        isUpdatingTask = true;
        try {
            await updateTask(projectId, taskId, { operating_mode: newMode });
        } catch (e) {
            console.error('Failed to update operating mode:', e);
            operatingMode = prevMode;
        } finally {
            isUpdatingTask = false;
        }
    }

    let summarizeController: AbortController | null = null;

    async function handleSummarize() {
        // Pass live getters so the dialog's donut + stats update in real time
        // (e.g. while a task keeps streaming in the background).
        const confirmed = await dialogStore.showCompressContextDialog({
            used: () => stream.state.contextState.used,
            limit: () => stream.state.contextState.limit,
            sessionCost: () => stream.state.sessionCost,
            confirmText: 'Compress',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        try {
            if (isAgentSelected()) {
                // Pi mode: trigger compaction via RPC (only works while task is running)
                const result = await compactTask(projectId, taskId);
                // Add the compaction summary as a chat entry
                stream.state.entries = [...stream.state.entries, {
                    id: genId(),
                    role: 'summarization',
                    content: result.summary || 'Context was summarized to free space',
                    isStreaming: false,
                    createdAt: new Date().toISOString(),
                    stats: { model_name: selectedModel || 'Pi', prompt_tokens: 0, predicted_tokens: 0, prompt_ms: 0, predicted_ms: 0, duration: 0 },
                }];
            } else {
                // LLM mode: summarize via backend (runs in background, result arrives via WebSocket).
                summarizeController = new AbortController();
                await summarizeTask(projectId, taskId, summarizeController.signal);
            }
        } catch (e: any) {
            if (e?.name === 'AbortError') return;
            console.error('Failed to compress context:', e);
            dialogStore.showToast(e?.message || 'Failed to compress context', 'error');
        } finally {
            summarizeController = null;
        }
    }

    // Clear queued flags when summarization finishes (task.summarizing transitions to false).
    let wasSummarizing = $state(false);
    $effect(() => {
        if (wasSummarizing && !isSummarizing) {
            stream.state.entries = stream.state.entries.map(e => e.queued ? { ...e, queued: false } : e);
        }
        wasSummarizing = isSummarizing;
    });

    function handleSummarizeCancel() {
        // Call the summarize endpoint again — backend treats it as a cancel.
        summarizeTask(projectId, taskId).catch(() => {});
    }

    async function handleRegenerate(entryId: string) {
        if (isLoading || disabled) return;
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        // Find the last user message before this assistant message
        const lastUserIdx = findLastUserMessageBefore(entries, idx);
        if (lastUserIdx === -1) return;
        const lastUser = entries[lastUserIdx];
        // Regenerate from backend starting after the last user message
        try {
            await regenerateTaskMessages(projectId, taskId, lastUser.id, true);
        } catch (e) {
            console.error('Failed to regenerate messages:', e);
        }
        // Trim local entries: keep everything up to and including the last user message
        stream.state.entries = trimEntriesForRegenerate(stream.state.entries, idx);
        isRegenerating = true;
        const result = await sendWithTracking(lastUser.content, undefined, true);
        if (result === null) {
            dialogStore.showToast('Failed to regenerate');
            isRegenerating = false;
            return;
        }
        if (result.queued) {
            // Network failure — regeneration is queued; keep the last user message
            // visible as outgoing and surface a non-persisted notice.
            handleQueuedSend(lastUser.content);
        }
        // Regenerate is a user-initiated action — jump to the latest message.
        scroll(true);
    }

    async function handleStartTask() {
        if (isStarting || disabled) return;
        // Ensure model is set on the task before starting
        if (selectedModel || selectedUrl) {
            try {
                await updateTask(projectId, taskId, {
                    model_name: selectedModel,
                    llama_server_url: selectedUrl,
                });
            } catch (e) {
                console.error('Failed to update task model before start:', e);
            }
        }
        // If the last message is already from the LLM, just update status without re-triggering
        // (use the last non-error entry — client-side error notices don't count).
        const lastEntry = lastNonErrorEntry;
        if (lastEntry && lastEntry.role === 'assistant') {
            try {
                const updated = await moveTask(projectId, taskId, 'in_progress');
                mergeTaskIntoBoard(updated);
            } catch (e) {
                dialogStore.showToast(String(e), 'error');
            }
            return;
        }
        isStarting = true;
        // Prompt is already in entries (loaded from DB at task creation)
        // Don't pre-create assistant entry — let the first token/tool_call event create it
        lastSentMessage = taskPrompt;
        stream.state.awaitingResponse = true;
        scroll();
        try {
            const updated = await moveTask(projectId, taskId, 'in_progress');
            mergeTaskIntoBoard(updated);
        } catch (e) {
            dialogStore.showToast(String(e), 'error');
            isStarting = false;
            stream.state.awaitingResponse = false;
            return;
        }
        isStarting = false;
    }

    // Start Task shortcut
    $effect(() => {
        function handleKeydown(e: KeyboardEvent) {
            if (e.defaultPrevented || e.repeat) return;
            if (!showStartButton || disabled) return;
            if (uiStore.showCreateTask || uiStore.showConfig || uiStore.showTerminal ||
                uiStore.showStats || uiStore.showSandboxConfig ||
                appStore.showEditTask || dialogStore.confirmation ||
                dialogStore.commitDialog || dialogStore.compressContextDialog) return;
            const key = effectiveStartTaskKey(appStore.config);
            if (key && matchShortcut(key, e)) {
                e.preventDefault();
                void handleStartTask();
            }
        }
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
    });

    async function handleStop() {
        stopping = true;
        markNotStreaming();
        stream.state.awaitingResponse = false;
        // Save the streaming assistant ID before clearing
        const streamingId = currentAssistantId;
        stream.closeCurrentAssistant();
        // Remove the streaming assistant entry and entries with pending tool calls.
        stream.state.entries = stream.state.entries.filter(e => e.id !== streamingId && !(e.toolCalls?.some(tc => tc.isPending)));
        // Clear active tool calls (immediate UI update)
        stream.state.activeToolCalls = new Map();
        stream.state.processedToolIds = new Set();
        try {
            await stopTask(projectId, taskId);
        } catch (e) {
            console.error('Failed to stop task:', e);
        }
        // Reload history from DB to discard any unpersisted (streaming) entries
        // so they don't appear as duplicates when the task is restarted
        await loadHistory();
    }

    $effect(() => {
        if (!($streamingIds.has(taskId) || taskExecutionState === 'running')) {
            stopping = false;
        }
    });

    let taskDiffStatsStore = getTaskDiffStatsContext();
    let diffStats = $derived($taskDiffStatsStore[taskId] || null);
    // Prefer the prop from CardDetail (computed from DiffCache, the source of truth).
    // Falls back to WebSocket store for backward compat (e.g., Card.svelte usage).
    // Includes both staged index changes and committed-but-unmerged changes.
    // When commits are already merged into the target branch, committed changes
    // don't count — only staged (index) changes matter.
    let hasStagedChanges = $derived(
        hasStagedChangesProp ?? ((diffStats?.staged_paths || []).length > 0 || (!!diffStats?.has_committed_changes && !diffStats?.already_merged))
    );

    async function handleCommitAndDone() {
        if (isCommitting || disabled) return;
        isCommitting = true;
        try {
            // Open dialog — it will stream the commit message from the backend
            const confirmedMessage = await new Promise<string | null>((resolve) => {
                dialogStore.commitDialog = {
                    streaming: true,
                    message: '',
                    confirmText: 'Commit and move to Done',
                    cancelText: 'Cancel',
                    projectId,
                    taskId,
                    _resolve: resolve,
                };
            });
            if (!confirmedMessage) {
                return; // User cancelled
            }
            const updated = await commitAndDone(projectId, taskId, confirmedMessage);
            mergeTaskIntoBoard(updated);

            if (updated?.status === 'in_progress') {
                stream.state.awaitingResponse = true;
            }
            // Reload history to show any system messages from merge conflict resolution
            await loadHistory();
        } catch (e) {
            dialogStore.showToast(String(e));
        } finally {
            isCommitting = false;
            // Clear queue status for this project
            commitQueueStatus.update(map => {
                const copy = { ...map };
                delete copy[projectId];
                return copy;
            });
        }
    }

    async function handleMoveToDone() {
        if (isCommitting || disabled) return;
        isCommitting = true;
        try {
            const updated = await moveTask(projectId, taskId, 'done');
            mergeTaskIntoBoard(updated);
            await loadHistory();
        } catch (e) {
            dialogStore.showToast(String(e));
        } finally {
            isCommitting = false;
        }
    }


    /** Called when the user clicks the delete button on a message.
     * Checks if deleting this message would also delete a fork branch, then shows the dialog. */
    async function handleDeleteRequest(entryId: string, count: number) {
        let willDeleteBranch = false;
        let branchName = '';
        try {
            const info = await checkMessageDelete(projectId, taskId, entryId);
            willDeleteBranch = info.will_delete_branch;
            branchName = info.branch_name || '';
        } catch {
            // If check fails, proceed without branch warning
        }

        let message = `This will delete ${count} message${count > 1 ? 's' : ''}. This action cannot be undone.`;
        if (willDeleteBranch && branchName) {
            message += `\n\nThis will also delete the git branch "${branchName}".`;
        }

        const confirmed = await dialogStore.showConfirm({
            title: 'Delete Message',
            message,
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;

        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        try {
            await deleteTaskMessagesAfter(projectId, taskId, entryId);

            // Discard deleted entries so loadHistory doesn't merge them back as non-DB entries.
            stream.state.entries = stream.state.entries.filter((_, i) => i < idx);

            // Reload from DB — restores pre-summary messages if summary was deleted.
            await loadHistory(true);

            // Refresh threads.
            threads = await getTaskThreads(projectId, taskId);
            const active = threads.find(t => t.is_active);
            if (active && active.id !== activeThreadId) {
                activeThreadId = active.id;
                // Update URL to reflect the new active thread
                updateThreadUrl(active.id);
            }
            // Invalidate diff cache — the backend reset worktree to a different commit,
            // changing the diff baseline. The worktree watcher doesn't detect soft resets
            // (working tree/index unchanged), so staged status in cache becomes stale.
            // Force a full HTTP reload to get fresh staged status.
            onInvalidateDiff?.();
        } catch (e) {
            dialogStore.showToast(String(e));
        }
    }

    async function handleEditFork(entryId: string, _originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) {
        const entry = entries.find(e => e.id === entryId);
        if (!entry) return;
        // Soft edit for backlog tasks: update message + task.prompt without triggering the LLM
        if (taskStatus === 'backlog') {
            const idx = entries.findIndex(e => e.id === entryId);
            if (idx !== -1) { entries[idx].content = newContent; entries[idx].createdAt = new Date().toISOString(); }
            updateTaskMessage(projectId, taskId, entryId, newContent).catch(e => dialogStore.showToast(String(e)));
            updateTask(projectId, taskId, { prompt: newContent }).catch(e => dialogStore.showToast(String(e)));
            return;
        }
        if (isLoading || disabled) return;
        try {
            const fromThreadId = entry.threadId || activeThreadId;
            const forked = await forkTaskThread(projectId, taskId, fromThreadId, entryId, newContent);
            threads = await getTaskThreads(projectId, taskId);
            activeThreadId = forked.id;
            // Clear streaming state before loading new thread's messages.
            resetStreamState();
            // Load messages for the new thread
            await loadHistory(true);
            updateThreadUrl(forked.id);
            onThreadSwitch?.();
            const result = await sendWithTracking(newContent, attachments);
            if (result?.queued) handleQueuedSend(newContent);
        } catch (e) {
            dialogStore.showToast(String(e));
        }
    }

    async function handleEditSend(entryId: string, _originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) {
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        if (!newContent.trim() && (!attachments || attachments.length === 0)) return;

        // Backlog: persist edit only (soft edit, no LLM trigger)
        if (taskStatus === 'backlog') {
            stream.state.entries = stream.state.entries.map((e, i) => i === idx ? { ...e, content: newContent, createdAt: new Date().toISOString() } : e);
            updateTaskMessage(projectId, taskId, entryId, newContent).catch(e => dialogStore.showToast(String(e)));
            updateTask(projectId, taskId, { prompt: newContent }).catch(e => dialogStore.showToast(String(e)));
            return;
        }

        if (isLoading || disabled) return;
        stream.state.entries = stream.state.entries.slice(0, idx);
        stream.closeCurrentAssistant();
        try {
            await deleteTaskMessagesAfter(projectId, taskId, entryId);
            threads = await getTaskThreads(projectId, taskId);
            const active = threads.find(t => t.is_active);
            if (active) {
                activeThreadId = active.id;
                updateThreadUrl(active.id);
            }
            // Invalidate diff cache (same reason as handleDeleteRequest)
            onInvalidateDiff?.();
        } catch (e) {
            dialogStore.showToast(String(e));
            return;
        }
        const newMsg = await createTaskMessage(projectId, taskId, 'user', newContent);
        stream.state.entries = [...stream.state.entries, { id: newMsg.id, role: 'user', content: newContent, attachments, threadId: activeThreadId, isStreaming: false, createdAt: newMsg.created_at || new Date().toISOString() }];
        const result = await sendWithTracking(newContent, attachments);
        if (result?.queued) handleQueuedSend(newContent);
        // Edit-and-send is a user-initiated send — jump to the latest message.
        scroll(true);
    }

    function connect(skipSync = false) {
        if (connected) return;
        connecting = true;
        connected = true;
        connecting = false;
        if (!firstConnectDone) {
            firstConnectDone = true;
            stream.state.entries = entries.filter(e => !(e.role === 'error' && e.error?.code === 'ws_disconnected'));
        }
    }

    /** Dismiss a single error message without affecting other messages. */
    async function handleDismissError(messageId: string) {
        const entry = entries.find(e => e.id === messageId);
        // Client-side only errors (queued / connection-lost) are not in the DB —
        // remove them locally instead of attempting a delete that would 404.
        if (entry?.error?.code && LOCAL_ERROR_CODES.has(entry.error.code)) {
            stream.state.entries = entries.filter(e => e.id !== messageId);
            return;
        }
        try {
            await deleteTaskMessage(projectId, taskId, messageId);
            stream.state.entries = entries.filter(e => e.id !== messageId);
        } catch (e) {
            console.error('Failed to dismiss error:', e);
        }
    }

    /** Force immediate reconnection. */
    function handleRetryConnect() {
        connected = false;
        // Remove existing connection lost errors
        stream.state.entries = entries.filter(e => !(e.role === 'error' && e.error?.code === 'ws_disconnected'));
        connect();
    }

    let destroyed = $state(false);

    // On mobile, selecting a task must not auto-focus the chat input (pops the keyboard)
    const { isMobile } = useIsMobile();

    // Mobile keyboard: track visualViewport shrink and add padding to keep input visible
    let keyboardOffset = $state(0);
    let chatFormRef: HTMLElement | null = $state(null);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let chatFormComp: { focus: () => void } | null = null;

    // svelte-ignore non_reactive_update — DOM ref via bind:this
    function setupViewportListener() {
        const vv = window.visualViewport;
        if (!vv) return;
        const vp = vv;
        function onResize() {
            const diff = Math.max(0, window.innerHeight - vp.height);
            keyboardOffset = diff;
        }
        vv.addEventListener('resize', onResize);
        // Also listen for orientation changes
        window.addEventListener('orientationchange', onResize);
        return () => {
            vv.removeEventListener('resize', onResize);
            window.removeEventListener('orientationchange', onResize);
        };
    }

    /** Re-read thread param from URL and switch if needed (for popstate/Back navigation). */
    async function syncThreadFromUrl() {
        const urlThread = readThreadFromUrl();
        if (!urlThread) return;
        // If URL specifies a thread and it differs from current active thread, switch to it
        if (urlThread !== activeThreadId && threads.some(t => t.id === urlThread)) {
            await handleThreadSwitch(urlThread);
        }
    }

    onMount(() => {
        // Run async init in a promise chain; cleanup is returned synchronously.
        let unsubTaskEvent: (() => void) | null = null;
        let unsubViewport: (() => void) | null | undefined = null;
        const handleUrlChange = () => syncThreadFromUrl();
        window.addEventListener('urlchanged', handleUrlChange);
        // Config changes are now tracked via $effect on appStore.config
        updateProvidersFromStore();
        unsubViewport = setupViewportListener();

        (async () => {
            await loadThreads();
            // If URL has a thread param, switch to that thread before loading history
            const urlThread = readThreadFromUrl();
            if (urlThread && threads.some(t => t.id === urlThread)) {
                try {
                    const switched = await switchTaskThread(projectId, taskId, urlThread);
                    updateActiveThread(switched.id);
                } catch (e) {
                    dialogStore.showToast(String(e));
                }
            } else if (!urlThread) {
                // No thread param — use the last active thread (is_active flag).
                // Only default to "Main" if no thread is marked active (first open).
                if (!activeThreadId) {
                    const mainThread = threads.find(t => t.name === 'Main');
                    if (mainThread) {
                        try {
                            const switched = await switchTaskThread(projectId, taskId, mainThread.id);
                            updateActiveThread(switched.id);
                        } catch (e) {
                            dialogStore.showToast(String(e));
                        }
                    }
                }
            }
            // 1. Set up task event handler FIRST (before subscribing to task).
            unsubTaskEvent = taskEventContext.subscribe((event) => {
                if (event && event.task_id === taskId) {
                    handleEvent({ type: event.type as StreamEventType, data: event.data });
                }
            });

            // 2. Clear stale streaming state for non-running tasks.
            // The global streamingTaskIds store may have a leftover entry from a previous
            // visit if the user navigated away while the task was streaming.
            if (taskStatus === 'review' || taskStatus === 'done' || taskStatus === 'backlog') {
                markNotStreaming();
            }

            // 3. Subscribe to this task directly (triggers buffer replay from server).
            // Handler is already set up above, so buffer events won't be missed.
            // App's $effect also subscribes, but the buffer is NOT cleared on re-subscribe.
            const appStream = getAppStreamRef();
            if (appStream) {
                appStream.subscribeTask(projectId, taskId);
            }

            // 4. Load history from DB (merges with buffer entries via ID-based dedup).
            historyLoading = true;
            try {
                await loadHistory();
            } finally {
                historyLoading = false;
            }

            // 5. Allow live events — buffer replay is done, DB state is loaded.
            suppressEvents = false;

            // 6. Initial focus (skipped on mobile — auto-focus pops the virtual keyboard)
            if (!destroyed && !isMobile) chatFormComp?.focus();

            // Override model selection with task-specific values if set
            try {
                const task = await (await import('$lib/api')).getTask(projectId, taskId);
                if (task.llama_server_url) {
                    selectedUrl = task.llama_server_url;
                }
                if (task.model_name) {
                    selectedModel = task.model_name;
                }
                if (task.reasoning_effort) {
                    selectedReasoningEffort = task.reasoning_effort;
                }
                // Restore session cost from the persisted task value so the badge
                // appears for non-streaming tasks.
                stream.state.sessionCost = task.session_cost || 0;
                // Recalculate context limit now that task-specific model is set
                updateContextLimit();
            } catch {
                // Task may not exist yet
            }
            connect();
            // Scroll to bottom after history is loaded and rendered.
            // pin() re-pins for a few frames, so the list need not be final yet.
            scroll();
            // If task was started externally (e.g., from board) and is running with no
            // assistant response yet, show the "awaiting LLM" spinner until first token/tool_call.
            // Only for in_progress — review tasks have no active executor.
            if (taskStatus === 'in_progress') {
                const lastEntry = entries[entries.length - 1];
                if (lastEntry?.role === 'user') {
                    stream.state.awaitingResponse = true;
                }
            }
        })();

        // Reconcile stale streaming state on tab return.
        // Mobile browsers suspend JS while hidden; the 'complete' event may be missed
        // (per-task buffer is cleared on the server when the turn ends).
        const handleTabVisibility = async () => {
            if (!document.hidden && isLoading) {
                try {
                    const task = await getTask(projectId, taskId);
                    if (task.status !== 'in_progress') {
                        // The turn finished while the tab was hidden — drop
                        // stale streaming state and reload from the DB.
                        dropStaleStreamingState();
                        await loadHistory();
                    }
                } catch {
                    // task deleted — ignore
                }
            }
        };
        window.addEventListener('visibilitychange', handleTabVisibility);

        return () => {
            window.removeEventListener('urlchanged', handleUrlChange);
            unsubViewport?.();
            unsubTaskEvent?.();
            window.removeEventListener('visibilitychange', handleTabVisibility);
        };
    });

    onDestroy(() => {
        destroyed = true;
        follow.destroy();
        // Clear the global streaming flag so the card's "Running" badge
        // doesn't persist after the detail is closed while the task streams.
        // The card falls back to execution_state === 'running' for the authoritative state.
        markNotStreaming();
        unsubQueue?.();
        unsubWsConn?.();
        window.removeEventListener('beforeunload', handleBeforeUnload);
        // Flush pending draft save on navigation/component destroy
        if (draftSaveTimer) {
            clearTimeout(draftSaveTimer);
            draftSaveTimer = null;
        }
        if (inputValue) {
            setDraftText(taskId, inputValue).catch(() => {});
        }
    });

    // Save draft on page unload (navigation, reload) as a safety net for onDestroy
    function handleBeforeUnload() {
        if (draftSaveTimer) {
            clearTimeout(draftSaveTimer);
            draftSaveTimer = null;
        }
        if (inputValue) {
            // Sync write for beforeunload — use navigator.sendBeacon-style approach
            // Since IndexedDB is async, best-effort only
            setDraftText(taskId, inputValue).catch(() => {});
        }
    }
    window.addEventListener('beforeunload', handleBeforeUnload);

    // Clear queued flags when queue changes (actions flushed)
    let unsubQueue: (() => void) | null = null;
    onMount(() => {
        unsubQueue = onQueueChanged(() => {
            const queuedIds = getQueuedTaskIds();
            // Only when nothing is queued for this task anymore was the queued
            // message actually flushed — clear its queued indicator and drop the
            // client-side notice (the notice is local-only, never persisted).
            if (!queuedIds.has(taskId)) {
                stream.state.entries = stream.state.entries
                    .map(e => e.queued ? { ...e, queued: false } : e)
                    .filter(e => !(e.role === 'error' && e.error?.code === 'send_queued'));
            }
        });
    });

    // Reconcile chat state on WebSocket reconnect. Turns completed while
    // offline aren't replayed, so we reload history. Running tasks replay
    // their in-flight turn; loadHistory merges them without clobbering.
    let unsubWsConn: (() => void) | null = null;
    onMount(() => {
        let wasDisconnected = false;
        unsubWsConn = wsConnection.subscribe((connected) => {
            if (connected && wasDisconnected) {
                void handleWsReconnect();
            }
            wasDisconnected = !connected;
        });
    });

    // Drop stale streaming entries and reset streaming UI. Turns finished
    // while offline aren't replayed by the server, so the DB is the source
    // of truth. Streaming entries (temp IDs) would duplicate DB entries.
    function dropStaleStreamingState() {
        stream.state.entries = stream.state.entries.filter(
            e => !e.isStreaming && !(e.toolCalls?.some(tc => tc.isPending))
        );
        // resetStreaming already drops every ref — no reconcile needed here.
        stream.resetStreaming();
        markNotStreaming();
    }

    async function handleWsReconnect() {
        try {
            const task = await getTask(projectId, taskId);
            if (task.status !== 'in_progress') {
                dropStaleStreamingState();
            }
            await loadHistory();
        } catch {
            // Task may have been deleted — ignore (same as handleTabVisibility)
        }
    }
</script>

<div class="task-chat">
    <!-- Live region for screen reader announcements -->
    <div class="sr-only" aria-live="polite" aria-atomic="true">
        {#if isLoading && !connected}Connecting...{:else if isLoading}Agent is responding...{:else if connected}Connected, waiting for messages{/if}
    </div>
    <ChatEntries
        {entries}
        {entryMetas}
        {lastEntryIdx}
        {activeThreadId}
        {threads}
        {awaitingResponse}
        {connected}
        {queuePosition}
        {queueEstimatedWait}
        {promptProgress}
        {retryCount}
        {retryError}
        {isAgentSelected}
        {taskStatus}
        {taskExecutionState}
        {projectId}
        {taskId}
        {follow}
        onDismissError={handleDismissError}
        onRetryConnect={handleRetryConnect}
        onThreadSwitch={handleThreadSwitch}
        isSwitching={threadSwitching}
        onMsgDelete={handleMsgDelete}
        onMsgFork={handleMsgFork}
        onMsgSend={handleMsgSend}
        onMsgEdit={handleMsgEdit}
        onRegenerate={handleRegenerate}
        onNavigateTask={handleMsgNavigateTask}
        {onViewChanges}
        {onToggleFullscreen}
        {isChatFullscreen}
        {historyLoading}
    />
    {#if showStartButton}
        <StartPanel
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            {operatingMode}
            {isStarting}
            {disabled}
            {isAgentSelected}
            onModelSelect={handleModelSelect}
            onOperatingModeToggle={handleOperatingModeToggle}
            onStartTask={handleStartTask}
        />
    {:else}
        <div class="chat-form-wrapper" style="padding-bottom: {keyboardOffset}px;" bind:this={chatFormRef}>
        <ReviewBar
            {taskStatus}
            {taskExecutionState}
            {showResumeButton}
            {showRestartButton}
            {isStarting}
            {isCommitting}
            {disabled}
            hasStagedChanges={hasStagedChanges}
            {queueStatus}
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            {onMoveTask}
            onStartTask={handleStartTask}
            onCommitAndDone={handleCommitAndDone}
            onMoveToDone={handleMoveToDone}
            onModelSelect={handleModelSelect}
        />
        {#if !showRestartButton}
        <ChatForm
            bind:this={chatFormComp}
            bind:value={inputValue}
            bind:attachments={draftAttachments}
            disabled={disabled}
            isLoading={isLoading}
            placeholder="Send a message..."
            providers={providers}
            selectedModel={selectedModel}
            selectedUrl={selectedUrl}
            selectedReasoningEffort={selectedReasoningEffort}
            contextUsed={stream.state.contextState.used}
            contextLimit={stream.state.contextState.limit}
            sessionCost={stream.state.sessionCost}
            operatingMode={operatingMode}
            projectId={projectId}
            taskId={taskId}
            onInput={handleInputSave}
            onClearDraft={handleClearDraft}
            onSubmit={handleSend}
            onStop={handleStop}
            onModelSelect={handleModelSelect}
            onOperatingModeToggle={handleOperatingModeToggle}
            onSummarize={handleSummarize}
            onSummarizeCancel={handleSummarizeCancel}
            isSummarizing={isSummarizing}
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
