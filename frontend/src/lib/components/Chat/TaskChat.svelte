<script lang="ts">
    import { onMount, onDestroy, untrack, tick } from 'svelte';
    import type { StreamEvent, StreamEventType, Provider, Thread, ChatEntry, TaskMessage, Task, ChatTurn } from '$lib/types';
    import type { DiffManifest } from '$lib/types/diff';
    import { AGENT_URL_PREFIX, isAgentProvider } from '$lib/types';
    import { sendTaskMessage, getTaskMessages, readCachedWindow, type MessageWindow, MESSAGE_JUMP_LIMIT, getTaskTurns, deleteTaskMessagesAfter, deleteTaskMessage, updateTaskMessage, stopTask, updateTask, moveTask, getTaskThreads, switchTaskThread, forkTaskThread, checkMessageDelete, commitAndDone, summarizeTask, compactTask, getOfflineStatus, onQueueChanged, onQueueFlushed, getQueuedTaskIds, getQueuedSends, getRefusedSends, getDroppedTaskIds, getTask, isFromCache, type AppStream, type SendResult } from '$lib/api';
    import { PENDING_SEND_ID_PREFIX, pendingSendEntries } from '$lib/utils/pending-sends';
    import { appStore, dialogStore, updateTaskInBoard } from '$lib/stores';
    import { streamingTaskStore } from '$lib/stores/streaming-tasks.svelte';
    import { diffStatsStore } from '$lib/stores/diff-stats.svelte';
    import { commitQueueStore } from '$lib/stores/commit-queue.svelte';
    import { StreamState } from '$lib/services/stream-state.svelte';
    import { createStreamHandler } from '$lib/services/stream-handler';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    // UI extension injection point — re-render when extensions change
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.TASK_CHAT);
        uiInjector.refreshSlot(INJECTION_POINTS.TASK_CHAT, { component: 'TaskChat', taskId, projectId });
    });
    import { DRAFT_SAVE_DEBOUNCE_MS } from '$lib/constants';
    import { draftMayPaint, draftNeedsSave, type StoredDraft } from '$lib/utils/chat-draft';
    import { mergeQueuedUpdate, taskIsInFlight } from '$lib/utils/task-utils';
    import { effectiveStartTaskKey, matchShortcut } from '$lib/utils/keyboard';
    import { isAnyModalOpen } from '$lib/utils/modal-state';
    import { trustedExecutionState, isTaskPaused } from '$lib/utils/execution-state';
    import { offersResume } from '$lib/utils/review-bar';
    import { reselectProvider, getProviderDisplayName } from '$lib/utils/providers';
    import { buildEntriesFromMessages, mergeLoadedHistory, buildEntryMetas, computeLastUserMessageIds, findLastUserMessageBefore, trimEntriesForRegenerate, pendingToolIds, endsOnReviewResult, entriesAwaitingRows, takeEchoMatch, removeFailedSend, removeDismissedError } from '$lib/utils/chat-entries';
    import { getAppStreamRef } from '$lib/stream-ref';
    import { useConnectionRestored } from '$lib/composables/use-connection-restored.svelte';
    import { useTaskEvents } from '$lib/composables/use-task-events.svelte';
    import { getDraftText, setDraftText, loadDraftAttachments, saveDraftAttachments, clearDraftAttachments, type DraftAttachment } from '$lib/storage';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { t } from '$lib/i18n';
    import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';
    import { appliedRevision, applyWindowRead, isStaleHistoryRead, newestWindowUnchanged, prependRows, replayBufferOwed, resolveAnchor, anchorScrollDelta, readerScrolledUp, noticeForWindowRead, windowIsReading, jumpFloor, pageUntilRowLands, type MeasuredEntry, type ScrollAnchor, type WindowRead, type WindowReadNotice } from '$lib/utils/chat-window';
    import { createTabReturn } from '$lib/composables/use-tab-return';
    import { fetchTaskSync } from '$lib/services/task-sync';
    import { heldFromDigest, resyncPlan, type HeldConversation } from '$lib/utils/task-sync';
    import ChatForm from './ChatForm.svelte';
    import ChatEntries from './ChatEntries.svelte';
    import ReviewBar from './ReviewBar.svelte';
    import StartPanel from './StartPanel.svelte';

    let { projectId, taskId, taskPrompt = '', taskStatus = 'backlog', taskExecutionState = '', taskSummarizing = false, hasStagedChanges: hasStagedChangesProp, onRefreshDiff, onMoveTask, onViewChanges, onReloadDiff, openCheckpointHash = '', onToggleFullscreen, isChatFullscreen, onChatInputFocus, onChatInputBlur, onMessageSent, onTaskDone }: { projectId: string; taskId: string; taskPrompt?: string; taskStatus?: string; taskExecutionState?: string; taskSummarizing?: boolean; hasStagedChanges?: boolean; onRefreshDiff?: (data: DiffManifest) => void; onMoveTask?: (status: string) => void; /** The chat moved the files under the Changes panel — a thread switch, a rewind that followed a deletion, a fork an edit retired — so reload it, never just clear it. */ onReloadDiff?: () => void; onViewChanges?: (commitHash: string, commitLabel?: string) => void; /** Which checkpoint the Changes panel is showing, so its band reads as the open one. */ openCheckpointHash?: string; onToggleFullscreen?: () => void; isChatFullscreen?: boolean; /** Focus entered / left the chat input — the owner of the maximize decides what that means. */ onChatInputFocus?: () => void; onChatInputBlur?: () => void; /** A message was sent — the auto maximize ends with the send, whatever the request did next. */ onMessageSent?: () => void; /** A Done click in the review bar succeeded — the view that owns navigation decides what happens next. Fires on a commit deferred to a conflict-resolution turn too, where the task stays in progress: the click still meant "I'm finished with it". */ onTaskDone?: () => void } = $props();

    // Owns everything about "follow the latest message" (scroll container,
    // atBottom, pinning). Shared with ChatEntries, which renders both.
    const follow = createChatFollow();
    // True while the initial conversation history is being fetched
    let historyLoading = $state(false);
    /** Set when the transcript may not be current: read from this device's mirror, or not read at all. */
    let historyNotice = $state<WindowReadNotice>(null);
    /** The stream this chat holds its task key on, set while the chat is mounted. */
    let heldStream: AppStream | null = null;
    /** Rows every window has delivered, oldest first; the transcript is rebuilt from these. */
    let historyRows: TaskMessage[] = [];
    /** Where the next-older window starts, and whether there is one. */
    let earlierCursor = $state('');
    let hasEarlier = $state(false);
    /** Strong ETag of the newest window on screen; empty when no live baseline is known. */
    let historyRevision = '';
    /** The conversation this transcript was built from, as the sync digest named it. Only an applied read
     * writes here. */
    let appliedConversation: HeldConversation = { epoch: '', revision: 0, messages: 0, thread: '' };
    /** True when this transcript threw away content no later read can redraw, so the next return must re-read. */
    let transcriptDiverged = false;
    /** Identifies the newest history read; older reads are discarded. */
    let historyReadSeq = 0;
    let loadingEarlier = $state(false);
    /** The window read in flight, so a scroll to the top edge and a jump to a prompt share one page. */
    let earlierInFlight: Promise<boolean> | null = null;
    /** The prompts the header can name, oldest first, and where the next-older page of them starts. A
     * window counted in answers does not hold the prompt that opened a long turn; this read does. */
    let chatTurns = $state<ChatTurn[]>([]);
    let turnsCursor = '';
    let turnsHasMore = $state(false);
    // The draft chat text: `utils/chat-draft` owns the rule, and `storedDraftText` is this tab's belief
    // about what the store holds. Only `writeDraftText` and the opening read move it.
    let inputValue = $state('');
    let storedDraftText: StoredDraft = null;
    let writesSinceDraftRead = 0;
    let draftReadSeq = 0;
    let draftSaveTimer: ReturnType<typeof setTimeout> | null = null;
    let draftAttachments = $state<DraftAttachment[]>([]);

    /** The only writer, so the belief stays true. `''` removes the draft. */
    function writeDraftText(text: string) {
        if (draftSaveTimer) {
            clearTimeout(draftSaveTimer);
            draftSaveTimer = null;
        }
        storedDraftText = text;
        writesSinceDraftRead++;
        setDraftText(taskId, text).catch(() => {});
    }

    $effect(() => {
        const task = taskId;
        const seq = ++draftReadSeq;
        storedDraftText = null;
        writesSinceDraftRead = 0;
        getDraftText(task).then(saved => {
            if (destroyed || seq !== draftReadSeq) return;
            const stored = saved ?? '';
            if (draftMayPaint(inputValue, writesSinceDraftRead > 0)) inputValue = stored;
            // A write of ours after the read began is the newer word about the store.
            if (writesSinceDraftRead === 0) storedDraftText = stored;
        }).catch(() => {});
    });

    // Load draft attachments from IndexedDB on init
    $effect(() => {
        loadDraftAttachments(taskId).then(atts => {
            if (atts.length > 0) {
                draftAttachments = atts.filter(a => a && a.mimeType && a.data);
            }
        }).catch(() => {});
    });

    function flushDraftText() {
        if (draftNeedsSave(storedDraftText, inputValue)) writeDraftText(inputValue);
    }

    // Save draft text to IndexedDB (debounced to avoid writes on every keystroke)
    function handleInputSave(val: string) {
        if (draftSaveTimer) {
            clearTimeout(draftSaveTimer);
            draftSaveTimer = null;
        }
        if (!draftNeedsSave(storedDraftText, val)) return;
        draftSaveTimer = setTimeout(() => {
            draftSaveTimer = null;
            writeDraftText(val);
        }, DRAFT_SAVE_DEBOUNCE_MS);
    }

    // Clear draft on send (called synchronously from ChatForm)
    function handleClearDraft() {
        writeDraftText('');
        clearDraftAttachments(taskId).catch(() => {});
        draftAttachments = [];
    }

    // Save draft attachments; skip empty to avoid racing with load effect on mount
    $effect(() => {
        const atts = draftAttachments;
        if (atts.length > 0) {
            saveDraftAttachments(taskId, atts);
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

    /**
     * The selected provider's own name — for an agent that is the `display_name` in its manifest, because core
     * names no agent. Empty when nothing is selected, so `MessageStats` answers with its own fallback.
     */
    let selectedProviderName = $derived.by(() => {
        const p = providers.find(p => p.url === selectedUrl);
        return p ? getProviderDisplayName(p, providers) : '';
    });

    // Stream state — managed by StreamState service (reactive via $state())
    let stream = new StreamState();
    // Shorthand accessors for template use
    let entries = $derived(stream.state.entries);
    let currentAssistantId = $derived(stream.state.currentAssistantId);
    let pendingCheckpoints = $derived(stream.state.pendingCheckpoints);
    let checkpointLabels = $derived(stream.state.checkpointLabels);
    let checkpointCommitHashes = $derived(stream.state.checkpointCommitHashes);
    let checkpointHashesVersion = $derived(stream.state.checkpointHashesVersion);
    let awaitingResponse = $derived(stream.state.awaitingResponse);
    let lockWait = $derived(stream.state.lockWait);
    let queuePosition = $derived(stream.state.queuePosition);
    let queueEstimatedWait = $derived(stream.state.queueEstimatedWait);
    let promptProgress = $derived(stream.state.promptProgress);
    let retryCount = $derived(stream.state.retryCount);
    let retryError = $derived(stream.state.retryError);

    let lastSentMessage: string | null = $state(null);
    let lastSentEntryId: string | null = $state(null); // temp ID of last optimistic entry
    let isStarting = $state(false);
    let isCommitting = $state(false);

    let threadSwitching = $state(false); // true while a thread switch is in-flight
    /** True from a "New Thread" click until its fork lands and its send goes out: the transcript holds a
     * question that is on its way to being answered. */
    let startingTurn = $state(false);
    // Replay the buffer only while the task is in flight, so a stale buffer cannot flicker entries that are
    // not coming. `isLoading` counts because the board's status lags the turn that frame announces.
    let suppressEvents = $state(true);
    $effect(() => { suppressEvents = taskStatus !== 'in_progress' && !isLoading; });

    /** The task's own `execution_state` and the board's copy it was read against. */
    let rowExecutionState = $state<string | null>(null);
    let rowStateBoardCopy = $state('');
    let knownExecutionState = $derived(
        trustedExecutionState(rowExecutionState, rowStateBoardCopy, taskExecutionState),
    );
    /** Record a reading of the task's own row. */
    function noteTaskRow(executionState: string | undefined) {
        rowExecutionState = executionState || null;
        rowStateBoardCopy = taskExecutionState;
    }

    let lastExecutionState: string | undefined;
    $effect(() => {
        const was = lastExecutionState;
        lastExecutionState = taskExecutionState;
        // The value at mount is not a stop: a task opened while it runs has live calls.
        if (was === undefined || taskIsInFlight(taskExecutionState) || !taskIsInFlight(was)) return;
        stream.dropUnresolvedToolCalls();
    });
    let queueStatus = $derived(commitQueueStore.statusOf(projectId, taskId));

    let threads: Thread[] = $state([]);
    let activeThreadId = $state('');

    let stopping = $state(false);
    let isSummarizing = $derived(taskSummarizing);
    // isLoading: true when LLM is actively streaming tokens OR task is in running state.
    // After page refresh, streamingTaskStore is empty but execution_state preserves running state.
    let isLoading = $derived(!stopping && (streamingTaskStore.has(taskId) || knownExecutionState === 'running'));

    // Last entry index for regenerate button gating
    let lastEntryIdx = $derived(entries.length - 1);

    // Show "Start Task" button when task is in backlog (not yet started).
    let showStartButton = $derived(taskStatus === 'backlog' && !isLoading);

    // Last entry ignoring client-side error notices (connection lost / queued).
    // Error entries are appended after the message they relate to, so the
    // Resume/Restart conditions must look past them.
    let lastNonErrorEntry = $derived([...entries].reverse().find(e => e.role !== 'error'));

    // Stopped mid-run: it stays in the In Progress column, and the ReviewBar offers Resume over the input.
    // Asked of the task's own row, not the board's copy.
    let isPaused = $derived(isTaskPaused(knownExecutionState));

    // "Restart Task" is hidden while paused (the Resume bar replaces it) and while summarizing, so the
    // ChatForm's spinner stays visible.
    let showRestartButton = $derived(
        taskStatus === 'in_progress' &&
        !isLoading &&
        !awaitingResponse &&
        !isPaused &&
        !isSummarizing &&
        queuePosition === 0 &&
        lastNonErrorEntry !== undefined &&
        lastNonErrorEntry.role === 'user' &&
        entries.some(e => e.role === 'assistant')
    );

    let showResumeButton = $derived(offersResume({
        taskStatus,
        loading: isLoading,
        summarizing: isSummarizing,
        awaitingResponse,
        startingTurn,
        lastEntry: lastNonErrorEntry,
    }));

    // Set of user message IDs with no assistant response after them. Window-safe: the window always
    // ends on the newest row.
    let lastUserMessageIds = $derived(computeLastUserMessageIds(entries));

    let entryMetas = $derived.by(() => {
        // A checkpoint hash lands in a Map, so reading the version is what re-runs this when one arrives.
        void checkpointHashesVersion;
        return buildEntryMetas({
            entries,
            pendingCheckpoints,
            checkpointLabels,
            checkpointCommitHashes,
            threads,
            hasEarlier,
            lastUserMessageIds,
            streaming: isLoading,
        });
    });

    function genId(): string {
        return Math.random().toString(36).slice(2, 10);
    }

    // Stable callback handlers — prevents unnecessary ChatMessage re-renders.
    function handleMsgDelete(entry: ChatEntry) {
        const idx = entries.findIndex(e => e.id === entry.id);
        if (idx === 0) return; // first message cannot be deleted
        handleDeleteRequest(entry.id);
    }
    async function handleMsgFork(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        await handleEditFork(msgId, origContent, newContent, atts);
    }
    async function handleMsgSend(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        await handleEditSend(msgId, origContent, newContent, atts);
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

    // Follow model and provider changes made elsewhere (inline edit, settings), but skip them while a local
    // updateTask is in flight, so a stale row cannot overwrite what the user just picked.
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

    /** Reset stream state before loading a new thread's messages. */
    function resetStreamState() {
        stream.resetForThreadSwitch();
        historyRevision = '';
    }

    /** Merge an updated task into the app store. */
    function mergeTaskIntoBoard(updated: any) {
        const existing = appStore.board?.tasks.find(t => t.id === taskId);
        const merged = existing ? mergeQueuedUpdate(existing, updated) : updated;
        updateTaskInBoard(appStore, taskId, merged);
    }

    type SendRequest = {
        attachments?: Array<{ mimeType: string; data: string }>;
        /** The thread already holds this text, so the send means "answer what the thread holds". */
        regenerate?: boolean;
        /** The optimistic entry on screen for this send, which the echo renames to the row's real id. */
        entryId?: string;
        /** Take the thread back to this row as the message lands. Server-side, so truncating and running
         * are one step: see `handleTaskSend`. */
        replaceMessageId?: string;
    };

    /** Send a message via the API with tracking state. Returns the message result, or null on failure. */
    async function sendWithTracking(msg: string, request: SendRequest = {}): Promise<SendResult | null> {
        lastSentMessage = msg;
        lastSentEntryId = request.entryId ?? null;
        stream.state.awaitingResponse = true;
        try {
            return await sendTaskMessage({
                projectId,
                taskId,
                content: msg,
                modelName: selectedModel,
                serverUrl: selectedUrl,
                attachments: request.attachments,
                reasoningEffort: selectedReasoningEffort || undefined,
                regenerate: request.regenerate,
                replaceMessageId: request.replaceMessageId,
            });
        } catch {
            stream.state.awaitingResponse = false;
            return null;
        }
    }

    /** Frames the anchor is re-read while lazily rendered rows find their height; see `settleOnRow`. */
    const ANCHOR_SETTLE_FRAMES = 20;

    /** Cards this view stopped drawing. Nothing can delete them server-side, so the dismissal is remembered
     * here or a re-derive of the queue paints them again. */
    const dismissedNotices = new Set<string>();

    /** Append a client-side error entry (visible but never persisted to the DB). */
    function appendErrorEntry(code: string, message: string) {
        stream.state.entries = [...stream.state.entries, {
            id: genId(),
            role: 'error',
            content: message,
            isStreaming: false,
            error: { code, message },
            createdAt: new Date().toISOString(),
            local: true,
        }];
    }

    /**
     * A send was queued by a network failure: mark the optimistic row queued and add a notice that lives only
     * in this UI until the queue flushes. The action id keeps a re-derive of the queue from drawing the
     * message twice.
     */
    function handleQueuedSend(content: string, entryId?: string, actionId?: string) {
        const entry = entryId
            ? stream.state.entries.find(e => e.id === entryId)
            : [...stream.state.entries].reverse().find(e => e.role === 'user' && e.content === content);
        if (entry) {
            entry.queued = true;
            if (actionId) entry.actionId = actionId;
        }
        appendErrorEntry('send_queued', t('chat.task.sendQueued'));
    }

    function markStreaming() {
        streamingTaskStore.add(taskId);
    }

    function markNotStreaming() {
        streamingTaskStore.delete(taskId);
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
        onThreadSwitch: () => onReloadDiff?.(),
        loadHistory,
        updateActiveThread,
        updateThreadUrl,
        onDiverged: () => { transcriptDiverged = true; },
    });

    /** Wrapper around stream handler with component-specific echo logic. */
    function handleEvent(event: StreamEvent) {
        // Handle user_message echo before delegating to stream handler
        if (event.type === 'user_message') {
            const data = event.data as { id?: string; role: string; content: string; thread_id?: string; attachments?: Array<{ mimeType: string; data: string }> };
            if (lastSentMessage === data.content) {
                const entryId = lastSentEntryId;
                // Read the id before clearing it, so a second echo of one send renames nothing twice.
                lastSentMessage = null;
                lastSentEntryId = null;
                const entry = takeEchoMatch(stream.state.entries, { entryId, content: data.content });
                if (entry && data.id) {
                    entry.id = data.id;
                    if (data.thread_id) entry.threadId = data.thread_id;
                    if (data.attachments) entry.attachments = data.attachments;
                }
                // An id that no longer resolves is not this echo's row; let the frame through and it appends.
                if (entry || !data.id) return;
            }
        }
        _handleStreamEvent(event, suppressEvents);
    }

    /** Update providers from the app store (reacts to settings changes). */
    function updateProvidersFromStore() {
        const cfg = appStore.config;
        if (cfg && cfg.providers) {
            providers = cfg.providers;
            // Keep a still-valid selection, otherwise follow the configured worker pair.
            const next = reselectProvider(cfg, providers, selectedUrl, selectedModel);
            if (next) {
                selectedUrl = next.url;
                selectedModel = next.model;
            }
            updateContextLimit();
        }
    }

    // The provider list can change while this chat stays open: enabling an agent extension makes the server
    // report a new one. untrack() keeps the state this sync reads and writes from making the effect re-run
    // itself.
    $effect(() => {
        const cfg = appStore.config;
        if (cfg) untrack(updateProvidersFromStore);
    });

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

    /** A newest-window read that has gone out and not yet been drawn. Its place in the read order is
     * claimed when it goes out, not when it lands: the newest read issued is the one that may paint, which
     * is what lets the open ask early and apply after a read it started in between. */
    interface WindowReadInFlight {
        seq: number;
        read: Promise<MessageWindow>;
    }

    /** Send for the newest window without waiting for it. The window route answers for whichever thread is
     * current and names none, so this needs nothing but the task. */
    function issueWindowRead(knownRevision = ''): WindowReadInFlight {
        const seq = ++historyReadSeq;
        const read = getTaskMessages(projectId, taskId, { knownRevision });
        // A read its caller never applies — a thread switch superseded it, or the chat unmounted first —
        // still has to have its failure consumed, or it surfaces as an unhandled rejection.
        read.catch(() => {});
        return { seq, read };
    }

    /** The task's own row, recorded the moment it lands. `execution_state` is what decides whether an
     * unanswered tool call is work or a leftover, and the model fields decide what the form offers. A row
     * that did not answer costs the reading, never the paint: the board's copy stands in. */
    async function readTaskRow(): Promise<Task | null> {
        try {
            const row = await getTask(projectId, taskId);
            noteTaskRow(row.execution_state);
            return row;
        } catch {
            return null;
        }
    }

    /** Draw what this browser holds of the conversation while its read of the newest window is still out —
     * the last Review result it saw, and nothing else: a window ending there holds no work that can still
     * move, so showing it a round trip early loses nothing. The answered read replaces these rows. */
    async function paintHeldWindow(): Promise<void> {
        const held = await readCachedWindow(projectId, taskId);
        if (!held || destroyed || historyRows.length > 0) return;
        if (!endsOnReviewResult(held.messages) || taskIsInFlight(knownExecutionState)) return;
        historyRows = held.messages;
        earlierCursor = held.before;
        hasEarlier = held.hasMore;
        historyNotice = noticeForWindowRead('provisional', readContext(knownExecutionState));
        renderHistory(true, knownExecutionState);
        // What is on screen came from this browser, not from a read, so the replay buffer is still owed.
        declareRows(historyNotice);
    }

    /** `executionState` decides whether an unanswered tool call is work or a leftover: pass the task row,
     * not the board's copy. The answer says how the read ended, so a caller can tell a mirror copy from
     * the server's own rows. `preRead` is a window read already in flight, applied here once what the render
     * needs has landed. */
    async function loadHistory(scrollAfter = false, executionState = knownExecutionState, knownRevision = '',
        preRead?: WindowReadInFlight): Promise<WindowRead> {
        let read: WindowRead = 'failed';
        const seq = preRead ? preRead.seq : ++historyReadSeq;
        try {
            const window = await (preRead ? preRead.read : getTaskMessages(projectId, taskId, { knownRevision }));
            if (isStaleHistoryRead(destroyed, seq, historyReadSeq)) return 'failed';
            const unchanged = newestWindowUnchanged(knownRevision, window.revision, window.unchanged);
            read = isFromCache(window) ? 'cached' : 'answered';
            historyNotice = noticeForWindowRead(read, readContext(executionState));
            declareRows(historyNotice);
            const applies = !unchanged && windowIsReading(read, historyRows.length > 0);
            if (applies) {
                const joined = applyWindowRead(historyRows, window.messages);
                if (historyRows.length > 0 && !joined.keepsEarlier) {
                    stream.state.entries = entriesAwaitingRows(stream.state.entries);
                }
                historyRows = joined.rows;

                if (!joined.keepsEarlier) {
                    earlierCursor = window.before;
                    hasEarlier = window.hasMore;
                }
                // A window that cannot be joined replaced the conversation rather than extending it, and the
                // reader's row is gone with it. Only then does a rebuild move the viewport.
                renderHistory(scrollAfter || !joined.keepsEarlier, executionState);
            }
            // The baseline names what this screen painted, never what it merely downloaded.
            historyRevision = appliedRevision(read, applies, { windowRevision: window.revision, held: historyRevision });
            // The transcript draws first: the prompts the header names arrive a moment later and relabel it.
            await readTurns(true);
        } catch (e) {
            if (isStaleHistoryRead(destroyed, seq, historyReadSeq)) return 'failed';
            // Task may have been deleted — the draft it holds goes with it
            if (e instanceof Error && (e.message.includes('Not found') || e.message.includes('404'))) {
                writeDraftText('');
                draftAttachments = [];
                return 'failed';
            }
            // Anything else: what is on screen may be the last thing read, so say so.
            transcriptDiverged = true;
            historyNotice = noticeForWindowRead('failed', readContext(executionState));
            declareRows(historyNotice);
        }
        if (isStaleHistoryRead(destroyed, seq, historyReadSeq)) return read;
        // A queued send has no row, so no read can bring it; the queue is read either way.
        await renderPendingSends();
        return read;
    }

    function readContext(executionState: string) {
        return { holdsTaskKey: heldStream !== null, taskInFlight: taskIsInFlight(executionState) };
    }

    /** Tell the hub whether this screen has a copy of the conversation the server answered: with none, or
     * with only its own cached copy drawn (`'pending'`), the replay buffer is all it has and must not be
     * trimmed. Re-declaring what the hub already knows sends nothing. */
    function declareRows(notice: WindowReadNotice) {
        heldStream?.subscribeTask(projectId, taskId, replayBufferOwed(notice));
    }

    /**
     * Put back the sends this browser wrote and the server has not answered, derived from
     * `offline-queue` on every rebuild rather than remembered in the mirror.
     */
    async function renderPendingSends(): Promise<void> {
        let additions;
        try {
            const pending = getQueuedSends(projectId, taskId);
            const refused = await getRefusedSends(projectId, taskId);
            const rendered = stream.state.entries
                .filter(e => !e.id.startsWith(PENDING_SEND_ID_PREFIX))
                .map(e => e.actionId)
                .filter((id): id is string => !!id);
            additions = pendingSendEntries({
                pending,
                refused,
                rendered,
                dismissed: dismissedNotices,
                copy: { queued: t('chat.task.sendQueued'), refused: t('chat.task.sendRefused') },
                threadId: activeThreadId,
            });
        } catch {
            return; // an unreadable queue: what is on screen is the best reading available
        }
        const kept = stream.state.entries.filter(e => !e.id.startsWith(PENDING_SEND_ID_PREFIX));
        stream.state.entries = [...kept, ...additions];
    }

    /** Draws the transcript from the rows loaded so far. Rebuilding it whole keeps a turn the window cut
     * in the middle as one entry. */
    function renderHistory(scrollAfter: boolean, executionState: string) {
        const { entries: mergedDbEntries, checkpointHashes } = buildEntriesFromMessages(historyRows);
        stream.state.checkpointCommitHashes = checkpointHashes;

        stream.state.entries = mergeLoadedHistory(mergedDbEntries, historyRows.map(m => m.id), stream.state.entries, taskIsInFlight(executionState));
        // These are new entry objects, so refs captured while streaming are now
        // detached; re-point them before more events for this turn arrive.
        stream.reconcileActiveToolCalls();
        stream.settleStreamingRef();
        // Restore context usage from the last assistant message.
        // Use total context (prompt + predicted) to reflect full window consumed.
        const lastAssistant = [...stream.state.entries].reverse().find(e => e.role === 'assistant' && e.stats);
        if (lastAssistant?.stats) {
            stream.state.lastPromptTokens = (lastAssistant.stats.prompt_tokens || 0) + (lastAssistant.stats.predicted_tokens || 0);
            stream.state.contextState = { ...stream.state.contextState, used: stream.state.lastPromptTokens };
        }
        if (!scrollAfter) return;
        // The DOM re-renders after the entries are replaced, so pin on the next frame.
        void (async () => {
            await tick();
            requestAnimationFrame(() => scroll());
        })();
    }

    /** How one read of the older rows travels. Scroll-to-top paging leaves every field alone. */
    interface OlderPage {
        from?: string;
        limit?: number;
        /** Re-pin the reader while the prepended rows find their heights. A jump owns the scroll and lands
         * once, so it declines the wait. */
        settle?: boolean;
    }

    /** Adds the next-older window above the rows held, leaving the reader on their row: the entry under
     * the top edge is found again by id afterwards. True when rows arrived. */
    async function loadEarlierMessages(page: OlderPage = {}): Promise<boolean> {
        const { from = '', limit = 0, settle = true } = page;
        // The cursor is named for exactly as long as older rows exist, so the flag is the guard.
        if (!hasEarlier) return false;
        // A reader who reaches the top edge while a jump is paging asks for the same page: waiting on the
        // read in flight is the answer, not a second one.
        if (earlierInFlight) return earlierInFlight;
        earlierInFlight = (async () => {
            loadingEarlier = true;
            // A reader who came up here is reading older rows, so growth at the bottom must not pull them back.
            // One still at the bottom did not ask for this page: it only fills the chat.
            const readingAbove = !follow.following;
            if (readingAbove) follow.stopFollowing();
            const held = historyRows.length;
            try {
                const older = await getTaskMessages(projectId, taskId, { before: earlierCursor, from, limit });
                const el = follow.el;
                // Read where the reader is now, not where the request went out: they may have kept scrolling
                // while it was in flight, and pinning them back to that is a jump.
                const anchor = readingAbove && settle && el ? resolveAnchor(measuredEntries(el), el.getBoundingClientRect().top) : null;
                historyRows = prependRows(historyRows, older.messages);
                earlierCursor = older.before;
                hasEarlier = older.hasMore;
                renderHistory(false, knownExecutionState);
                await tick();
                // A jump declines the settle and lands at its caller's hand, so it gets neither.
                if (anchor && el) await settleOnRow(el, anchor);
                else if (!readingAbove) follow.pin(true);
                return historyRows.length > held;
            } catch {
                // Nothing to undo: the rows held and the cursor are untouched.
                return false;
            } finally {
                loadingEarlier = false;
                earlierInFlight = null;
            }
        })();
        return earlierInFlight;
    }

    /**
     * Reads the prompts beside the rows: the sticky header names the prompt a window counted in answers leaves
     * out. A failed read costs the header those labels, not the conversation, so nothing here re-throws.
     */
    async function readTurns(fromTheStart: boolean): Promise<void> {
        try {
            const page = await getTaskTurns(projectId, taskId, fromTheStart ? '' : turnsCursor);

            const joined = fromTheStart
                ? applyWindowRead(chatTurns, page.turns)
                : { rows: prependRows(chatTurns, page.turns), keepsEarlier: false };
            chatTurns = joined.rows;
            if (!joined.keepsEarlier) {
                turnsCursor = page.before;
                turnsHasMore = page.hasMore;
            }
        } catch {
            // What the rows on screen name is what the header has.
        }
    }

    /** Lands the prompt the header named by asking for the rows between the oldest row held and that
     * prompt, walking when no prompt read carried a cursor for it. The caller owns the scroll. */
    async function jumpToTurn(entryId: string): Promise<boolean> {
        const landed = await pageUntilRowLands(
            entryId,
            turnRowOnScreen,
            from => loadEarlierMessages({ from, limit: MESSAGE_JUMP_LIMIT, settle: false }),
            jumpFloor(chatTurns, entryId),
        );
        if (!landed) return false;
        await keepTurnsAbove(entryId);
        return true;
    }

    function turnRowOnScreen(entryId: string): boolean {
        const el = follow.el;
        return !!el && !!el.querySelector(`[data-entry-id="${CSS.escape(entryId)}"]`);
    }

    /** A jump that landed on the oldest prompt this browser knows asks for the ones above it, so the next
     * click up has somewhere to go. */
    async function keepTurnsAbove(entryId: string): Promise<void> {
        if (!turnsHasMore || chatTurns[0]?.id !== entryId) return;
        await readTurns(false);
    }

    /** Keeps the reader's row under the top edge while the prepended rows find their real heights — entries
     * are `content-visibility: auto`, so a page that lands is placeholder-sized at first. A scroll stops
     * it: the reader is driving. */
    async function settleOnRow(el: HTMLElement, anchor: ScrollAnchor): Promise<void> {
        let written = el.scrollTop;
        let steady = 0;
        for (let frame = 0; frame < ANCHOR_SETTLE_FRAMES && steady < 2; frame++) {
            await new Promise(resolve => requestAnimationFrame(resolve));

            if (readerScrolledUp(written, el.scrollTop)) return;
            const node = el.querySelector<HTMLElement>(`[data-entry-id="${anchor.id}"]`);
            if (!node) return;
            const delta = anchorScrollDelta(anchor, node.getBoundingClientRect().top - el.getBoundingClientRect().top);
            if (Math.abs(delta) < 1) { steady++; continue; }
            steady = 0;
            el.scrollTop += delta;
            written = el.scrollTop;
        }
    }

    /** Frames to keep re-reading the anchor row while lazily rendered rows above it find their height. */

    /** Every entry box in viewport offsets, for resolveAnchor. */
    function measuredEntries(el: HTMLElement): MeasuredEntry[] {
        const top = el.getBoundingClientRect().top;
        return Array.from(el.querySelectorAll<HTMLElement>('[data-entry-id]')).map(node => {
            const box = node.getBoundingClientRect();
            return { id: node.dataset.entryId as string, top: box.top - top, bottom: box.bottom - top };
        });
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
        if (activeThreadId && threadId !== activeThreadId) resetStreamState();
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
            // The new thread's files are a different comparison.
            onReloadDiff?.();
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

    /**
     * Follow the latest content. Forced only for user-initiated actions, never for stream events, so a reader
     * who scrolled up keeps their position.
     */
    function scroll(force = false) {
        follow.pin(force);
    }

    async function handleSend(msg: string, attachments?: Array<{ mimeType: string; data: string }>) {
        if (!msg && (!attachments || attachments.length === 0)) return;

        if (isLoading) {
            stream.abandonInterruptedTurn();
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
        try {
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
            const result = await sendWithTracking(msg, { attachments, entryId });
            if (result === null) {
                // Permanent failure (e.g. server error) — the row goes by its id and the draft comes back.
                stream.state.entries = removeFailedSend(stream.state.entries, {
                    entryId,
                    copy: { failed: t('chat.task.sendFailed') },
                });
                chatFormComp?.restoreDraft(msg, attachments);
                // No answer is coming for this send; a later echo must not be read as its.
                lastSentMessage = null;
                lastSentEntryId = null;
                return;
            }
            if (result.queued) {
                // Network failure — the message was queued (not lost). Keep it as an
                // outgoing message and surface a visible, non-persisted notice.
                handleQueuedSend(msg, entryId, result.actionId);
            }
            scroll(true);
        } finally {
            // The send is the user’s part done: the view that maximized for the input gets the room
            // back, whatever the request did next.
            onMessageSent?.();
        }
    }

    async function handleModelSelect(_providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        selectedReasoningEffort = reasoningEffort || '';
        updateContextLimit();
        // Recount context against the new provider's window.
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
        // Live getters, so the dialog's donut and stats keep updating while the task streams.
        const confirmed = await dialogStore.showCompressContextDialog({
            used: () => stream.state.contextState.used,
            limit: () => stream.state.contextState.limit,
            sessionCost: () => stream.state.sessionCost,
            confirmText: t('chat.compress.confirm'),
            cancelText: t('chat.common.cancel'),
        });
        if (!confirmed) return;
        try {
            if (isAgentSelected()) {
                // An agent compacts through its own RPC, which only works while the task runs.
                const result = await compactTask(projectId, taskId);
                stream.state.entries = [...stream.state.entries, {
                    id: genId(),
                    role: 'summarization',
                    content: result.summary || t('chat.stream.summarized'),
                    isStreaming: false,
                    createdAt: new Date().toISOString(),
                    stats: { model_name: selectedModel || selectedProviderName, prompt_tokens: 0, predicted_tokens: 0, prompt_ms: 0, predicted_ms: 0, duration: 0 },
                }];
            } else {
                // A plain model summarizes in the background; the result arrives over the WebSocket.
                summarizeController = new AbortController();
                await summarizeTask(projectId, taskId, summarizeController.signal);
            }
        } catch (e: any) {
            if (e?.name === 'AbortError') return;
            console.error('Failed to compress context:', e);
            dialogStore.showToast(e?.message || t('chat.task.compressFailed'), 'error');
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
        if (isLoading) return;
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        const lastUserIdx = findLastUserMessageBefore(entries, idx);
        if (lastUserIdx === -1) return;
        const lastUser = entries[lastUserIdx];
        // A failed truncate leaves the rows the retry would answer still there, so the turn is not started.
        try {
            await deleteTaskMessagesAfter(projectId, taskId, lastUser.id, true);
        } catch (e) {
            dialogStore.showToast(String(e));
            return;
        }
        stream.state.entries = trimEntriesForRegenerate(stream.state.entries, idx);
        const result = await sendWithTracking(lastUser.content, { regenerate: true });
        if (result === null) {
            dialogStore.showToast(t('chat.task.regenerateFailed'));
            return;
        }
        if (result.queued) {
            // Network failure — regeneration is queued; keep the last user message
            // visible as outgoing and surface a non-persisted notice.
            handleQueuedSend(lastUser.content, undefined, result.actionId);
        }
        scroll(true);
    }

    async function handleStartTask() {
        if (isStarting) return;
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
        // If the last real message is the assistant's, moving the column is enough. Client-side notices
        // don't count as a real message.
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
        // The prompt is already on screen, and the first token or tool_call event creates the assistant entry.
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
            if (!showStartButton) return;
            if (isAnyModalOpen()) return;
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
        // An explicit stop answers every open call now — no waiting for the transition the effect above reads.
        stream.dropUnresolvedToolCalls();
        stream.state.entries = stream.state.entries.filter(e => e.id !== streamingId);
        stream.state.processedToolIds = new Set();
        // The row the stop returns is the freshest reading, so the rebuild needs no waiting for the board.
        let stoppedState = 'paused';
        try {
            const stopped = await stopTask(projectId, taskId);
            stoppedState = stopped?.execution_state || 'paused';
        } catch (e) {
            console.error('Failed to stop task:', e);
        }
        noteTaskRow(stoppedState);
        // Re-read the transcript so unpersisted streaming rows don't come back as duplicates.
        await loadHistory(false, stoppedState);
    }

    $effect(() => {
        if (!(streamingTaskStore.has(taskId) || knownExecutionState === 'running')) {
            stopping = false;
        }
    });

    let diffStats = $derived(diffStatsStore.get(taskId));
    // Prefer the prop from CardDetail, which reads DiffCache; the WebSocket store is the fallback for other
    // callers. Merged commits don't count - only staged changes do.
    let hasStagedChanges = $derived(
        hasStagedChangesProp ?? ((diffStats?.staged_paths || []).length > 0 || (!!diffStats?.has_committed_changes && !diffStats?.already_merged))
    );

    async function handleCommitAndDone() {
        if (isCommitting) return;

        const thisProjectId = projectId;
        const thisTaskId = taskId;
        isCommitting = true;
        try {
            // Open dialog — it will stream the commit message from the backend
            const confirmedMessage = await dialogStore.showCommitDialog({
                streaming: true,
                message: '',
                confirmText: t('chat.review.commitAndDone'),
                cancelText: t('chat.common.cancel'),
                projectId: thisProjectId,
                taskId: thisTaskId,
            });
            if (!confirmedMessage) {
                return; // User cancelled
            }
            const updated = await commitAndDone(thisProjectId, thisTaskId, confirmedMessage);
            mergeTaskIntoBoard(updated);

            if (updated?.status === 'in_progress') {
                stream.state.awaitingResponse = true;
            }
            // Reload history to show any system messages from merge conflict resolution
            await loadHistory(false, updated?.execution_state || knownExecutionState);
            if (updated) onTaskDone?.();
        } catch (e) {
            dialogStore.showToast(String(e));
        } finally {
            isCommitting = false;
            // Clear queue status for this project
            commitQueueStore.remove(thisProjectId);
        }
    }

    async function handleMoveToDone() {
        if (isCommitting) return;
        isCommitting = true;
        try {
            const updated = await moveTask(projectId, taskId, 'done');
            mergeTaskIntoBoard(updated);
            await loadHistory(false, updated?.execution_state || knownExecutionState);
            if (updated) onTaskDone?.();
        } catch (e) {
            dialogStore.showToast(String(e));
        } finally {
            isCommitting = false;
        }
    }


    /** Ask what this deletion takes with it beyond the thread — a fork branch — then show the dialog.
     *  No row count is claimed: what goes is every row after this one in the thread, which the loaded window
     *  neither counts nor bounds. */
    async function handleDeleteRequest(entryId: string) {
        let willDeleteBranch = false;
        let branchName = '';
        try {
            const info = await checkMessageDelete(projectId, taskId, entryId);
            willDeleteBranch = info.will_delete_branch;
            branchName = info.branch_name || '';
        } catch {
            // A check that did not answer promises nothing extra; the deletion itself still runs.
        }

        // One sentence per consequence the reader cannot undo, joined by a blank line so each stays whole.
        const lines = [t('chat.task.deleteConfirm')];
        if (willDeleteBranch && branchName) lines.push(t('chat.task.deleteAlsoBranch', { branch: branchName }));
        const message = lines.join('\n\n');

        const confirmed = await dialogStore.showConfirm({
            title: t('chat.task.deleteTitle'),
            message,
            confirmText: t('chat.common.delete'),
            cancelText: t('chat.common.cancel'),
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
            onReloadDiff?.();
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
        if (isLoading || startingTurn) return;
        startingTurn = true;
        try {
            const fromThreadId = entry.threadId || activeThreadId;
            const forked = await forkTaskThread(projectId, taskId, fromThreadId, entryId, newContent);
            threads = await getTaskThreads(projectId, taskId);
            activeThreadId = forked.id;
            // Clear streaming state before loading new thread's messages.
            resetStreamState();
            // The question is stored and its answer has not started, which is what the wait line means.
            stream.state.awaitingResponse = true;
            // Load messages for the new thread
            await loadHistory(true);
            updateThreadUrl(forked.id);
            onReloadDiff?.();
            // regenerate: the fork already wrote the edited question into the new thread, so this send is
            // "answer what the thread holds", not a second copy of the same message.
            const result = await sendWithTracking(newContent, { attachments, regenerate: true });
            if (result?.queued) handleQueuedSend(newContent, undefined, result.actionId);
        } catch (e) {
            stream.state.awaitingResponse = false;
            dialogStore.showToast(String(e));
        } finally {
            startingTurn = false;
        }
    }

    /** Re-run the turn from an edited message. One send carries the edit and the row it replaces: a
     * delete-then-send leaves a gap whose thread the executor reads as a duplicate submit — no answer. */
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

        if (isLoading) return;
        // The same shape the request produces: the edited row stands for everything after it.
        const optimisticId = genId();
        stream.state.entries = [...stream.state.entries.slice(0, idx), {
            id: optimisticId, role: 'user', content: newContent, attachments, threadId: activeThreadId,
            isStreaming: false, createdAt: new Date().toISOString(),
        }];
        stream.closeCurrentAssistant();
        scroll(true);
        const result = await sendWithTracking(newContent, { attachments, entryId: optimisticId, replaceMessageId: entryId });
        if (result === null) {
            // Nothing was truncated, so re-read rather than hold a transcript missing rows the server kept.
            await loadHistory(true);
            return;
        }
        if (result.queued) {
            handleQueuedSend(newContent, optimisticId, result.actionId);
        }
        // The replace can retire the fork this row opened and moves the diff baseline. A failed re-read here
        // is the panel catching up on a send that already landed.
        try {
            threads = await getTaskThreads(projectId, taskId);
            const active = threads.find(t => t.is_active);
            if (active) {
                activeThreadId = active.id;
                updateThreadUrl(active.id);
            }
        } catch { /* the transcript on screen is still the one the send produced */ }
        onReloadDiff?.();
        if (!result.queued && result.id) {
            const entry = stream.state.entries.find(e => e.id === optimisticId);
            if (entry) {
                entry.id = result.id;
                if (result.thread_id) entry.threadId = result.thread_id;
            }
        }
        // Edit-and-send is a user-initiated send — jump to the latest message.
        scroll(true);
    }

    // Rewrites the summarized context. The entry shows what the stored row says, so a refusal puts
    // back the text the server still holds rather than leaving prose on screen that no turn will read.
    async function handleSummaryEdit(entryId: string, newContent: string) {
        const entry = entries.find(e => e.id === entryId);
        if (!entry) return;
        const stored = entry.content;
        stream.state.entries = entries.map(e => (e.id === entryId ? { ...e, content: newContent } : e));
        try {
            await updateTaskMessage(projectId, taskId, entryId, newContent);
        } catch (e) {
            stream.state.entries = stream.state.entries.map(en => (en.id === entryId ? { ...en, content: stored } : en));
            dialogStore.showToast(String(e));
        }
    }

    /** Dismiss a single error message without affecting other messages. */
    async function handleDismissError(messageId: string) {
        const outcome = removeDismissedError(entries, messageId);
        // Whatever the outcome, this view stops drawing it.
        dismissedNotices.add(messageId);
        stream.state.entries = outcome.entries;
        if (outcome.deleteId === null) return;
        try {
            await deleteTaskMessage(projectId, taskId, outcome.deleteId);
            stream.state.entries = stream.state.entries.filter(e => e.id !== messageId);
        } catch (e) {
            console.error('Failed to dismiss error:', e);
        }
    }

    let destroyed = $state(false);

    // On mobile, selecting a task must not auto-focus the chat input (pops the keyboard)
    const { isMobile } = useIsMobile();

    // Mobile keyboard: track visualViewport shrink and add padding to keep input visible
    let keyboardOffset = $state(0);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let chatFormComp: { focus: () => void; restoreDraft: (text: string, images?: Array<{ mimeType: string; data: string }>) => void } | null = null;

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

    // The frames App.svelte dispatches for the selected or floating task; armed once the history
    // this screen paints has been read.
    const taskEvents = useTaskEvents(() => taskId, (event) => {
        handleEvent({ type: event.type as StreamEventType, data: event.data });
    });

    onMount(() => {
        // Run async init in a promise chain; cleanup is returned synchronously.
        let unsubViewport: (() => void) | null | undefined = null;
        let unmounted = false;
        const handleUrlChange = () => syncThreadFromUrl();
        window.addEventListener('urlchanged', handleUrlChange);
        // Providers come from the $effect on appStore.config above — it runs at mount too.
        unsubViewport = setupViewportListener();

        (async () => {
            // The open's three reads, asked in the same breath: the thread list, the task's own row, and the
            // newest window — which names no thread, so it waits on neither of the other two. What still
            // orders the *apply* is written where it is applied.
            const threadsRead = loadThreads();
            const rowRead = readTaskRow();
            let windowRead = issueWindowRead();
            // What this browser holds of the conversation goes on screen as soon as its cache gives it up,
            // which is while those reads are still out. It paints nothing unless it ends on a Review result.
            void paintHeldWindow();

            await threadsRead;
            // If URL has a thread param, switch to that thread before loading history
            const urlThread = readThreadFromUrl();
            if (urlThread && threads.some(t => t.id === urlThread)) {
                try {
                    const switched = await switchTaskThread(projectId, taskId, urlThread);
                    updateActiveThread(switched.id);
                    // The read that went out answers for the thread this screen has just left.
                    windowRead = issueWindowRead();
                } catch (e) {
                    dialogStore.showToast(String(e));
                }
            } else if (!urlThread && !activeThreadId) {
                // No thread param — use the last active thread (is_active flag).
                // Only default to "Main" if no thread is marked active (first open).
                const mainThread = threads.find(t => t.name === 'Main');
                if (mainThread) {
                    try {
                        const switched = await switchTaskThread(projectId, taskId, mainThread.id);
                        updateActiveThread(switched.id);
                        windowRead = issueWindowRead();
                    } catch (e) {
                        dialogStore.showToast(String(e));
                    }
                }
            }
            // The row is applied before the task key is taken: it opens the gate that drops frames for a
            // task this screen does not believe is running, and the replay that key earns arrives while the
            // rows are still loading (`docs/reconnection.md → The row decides, not the board's copy`).
            const taskRow = await rowRead;
            if (unmounted) return;

            // Open the task-event reader. Before this the last published frame is not this
            // screen's to act on: its history is not loaded yet, and a frame handled now would
            // land ahead of the rows it belongs between.
            taskEvents.arm();

            // Clear stale streaming state for non-running tasks.
            // A leftover id in the streaming store from a visit that ended mid-turn would otherwise
            // keep this chat showing a turn that is not running.
            if (taskStatus === 'review' || taskStatus === 'done' || taskStatus === 'backlog') {
                markNotStreaming();
            }

            // Take this task's key: that frame is what earns the server's replay buffer, which is the only
            // source for a call already in flight. Released when this chat goes.
            heldStream = getAppStreamRef();
            if (heldStream) {
                heldStream.subscribeTask(projectId, taskId);
                // The key is new, so whatever the screen is already under has to be claimed on it: a
                // provisional paint before this line happened while no key was held.
                declareRows(historyNotice);
            }

            // Draw the window the open asked for (merges with buffer entries via ID-based dedup). It waits
            // for the row above because whether an unanswered tool call is work or a leftover is that
            // reading's to say.
            historyLoading = true;
            try {
                await loadHistory(false, knownExecutionState, '', windowRead);
            } finally {
                historyLoading = false;
            }

            // Allow live events — buffer replay is done, DB state is loaded.
            suppressEvents = false;

            // Initial focus (skipped on mobile — auto-focus pops the virtual keyboard)
            if (!destroyed && !isMobile) chatFormComp?.focus();

            // Task-specific model selection, from the row already read.
            if (taskRow) {
                if (taskRow.llama_server_url) {
                    selectedUrl = taskRow.llama_server_url;
                }
                if (taskRow.model_name) {
                    selectedModel = taskRow.model_name;
                }
                if (taskRow.reasoning_effort) {
                    selectedReasoningEffort = taskRow.reasoning_effort;
                }
                // Restore session cost from the persisted task value so the badge
                // appears for non-streaming tasks.
                stream.state.sessionCost = taskRow.session_cost || 0;
                // Recalculate context limit now that task-specific model is set
                updateContextLimit();
            }
            // Scroll to bottom after history is loaded and rendered.
            // pin() re-pins for a few frames, so the list need not be final yet.
            scroll();
            // If task was started externally (e.g., from board) and is running with no
            // assistant response yet, show the "awaiting LLM" spinner until first token/tool_call.
            // Only for a task the server says is running: a stalled one answers with the Restart
            // bar, and `awaitingResponse` is what holds that bar off the chat input.
            if (taskIsInFlight(knownExecutionState)) {
                const lastEntry = entries[entries.length - 1];
                if (lastEntry?.role === 'user') {
                    stream.state.awaitingResponse = true;
                }
            }
        })();

        // A turn missed whole leaves nothing on screen that could report it, so every return asks the server.
        const returnWatcher = createTabReturn({
            revalidateOnReturn: true,
            onReturn: () => { void reconcileAfterAbsence(); },
        });

        return () => {
            window.removeEventListener('urlchanged', handleUrlChange);
            unsubViewport?.();
            returnWatcher.destroy();
            // Release on the stream that was asked to hold it, not on whatever is current now.
            heldStream?.unsubscribeTask(projectId, taskId);
            heldStream = null;
            unmounted = true;
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
        unsubFlushed?.();
        window.removeEventListener('beforeunload', handleBeforeUnload);
        flushDraftText();
    });

    /** The unload safety net for the destroy flush: an IndexedDB write started this late is best-effort. */
    function handleBeforeUnload() {
        flushDraftText();
    }
    window.addEventListener('beforeunload', handleBeforeUnload);

    // Clear queued flags when queue changes (actions flushed)
    let unsubQueue: (() => void) | null = null;
    let unsubFlushed: (() => void) | null = null;
    onMount(() => {
        unsubQueue = onQueueChanged(() => {
            // Two different reasons for an empty queue: an applied action, or one the server
            // refused. Only the first means the notice can go away.
            if (!getQueuedTaskIds().has(taskId)) {
                const cleared = stream.state.entries.map(e => (e.queued ? { ...e, queued: false } : e));
                stream.state.entries = getDroppedTaskIds().has(taskId)
                    // Its own code: a refusal keeps its notice even after the next flush clears the record.
                    ? cleared.map(e =>
                        e.role === 'error' && e.error?.code === 'send_queued'
                            ? { ...e, content: t('chat.task.sendRefused'), error: { ...e.error, code: 'send_refused', message: t('chat.task.sendRefused') } }
                            : e
                    )
                    : cleared.filter(e => !(e.role === 'error' && e.error?.code === 'send_queued'));
            }
            void renderPendingSends();
        });
        // The read that filled this transcript went out before the flush applied anything, so the end
        // of a flush is the signal that the applied row belongs on screen.
        unsubFlushed = onQueueFlushed(() => {
            void loadHistory(false, knownExecutionState);
        });
    });

    // Turns completed while offline aren't replayed, so the transcript comes back from the
    // database when the connection does. Running tasks replay their in-flight turn; loadHistory
    // merges them without clobbering.
    useConnectionRestored(() => {
        historyRevision = '';
        void reconcileAfterAbsence();
    });

    /**
     * What a window that has not been being read does about it: it asks the server, because a missed turn
     * leaves nothing here that could report it. No digest, or `force` (the reader clicking Retry), is read as
     * "everything moved".
     */
    async function reconcileAfterAbsence(force = false): Promise<void> {
        const digest = force ? null : await fetchTaskSync(projectId, taskId);
        const verdict = digest
            ? resyncPlan({
                conversation: appliedConversation,
                row: { status: taskStatus, executionState: knownExecutionState, summarizing: taskSummarizing },
            }, digest)
            : { conversation: true, diff: false, row: true };
        if (transcriptDiverged) verdict.conversation = true;
        let executionState = knownExecutionState;
        if (verdict.row) {
            try {
                executionState = (await getTask(projectId, taskId)).execution_state;
                noteTaskRow(executionState);
            } catch {
                // Deleted or unreachable: keep the reading from the last row read.
            }
        }
        if (!taskIsInFlight(executionState)) dropStaleStreamingState();
        // A held ETag can only confirm what this screen already claimed it holds, so doubt reads the rows.
        const read = await loadHistory(follow.following, executionState, verdict.conversation ? '' : historyRevision);
        if (read === 'answered') {
            stream.state.unresolvedToolIds = pendingToolIds(stream.state.entries);
            if (digest) {
                appliedConversation = heldFromDigest(digest);
                transcriptDiverged = false;
            }
        }
    }

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
</script>

<div class="task-chat">
    <!-- Only the turn itself is announced; a state that never changes is noise twice per reply. -->
    <div class="sr-only" aria-live="polite" aria-atomic="true">
        {#if isLoading}{t('chat.task.responding')}{/if}
    </div>
    <ChatEntries
        {entries}
        {entryMetas}
        {lastEntryIdx}
        {threads}
        {awaitingResponse}
        {lockWait}
        {queuePosition}
        {queueEstimatedWait}
        {promptProgress}
        {retryCount}
        {retryError}
        {isAgentSelected}
        {taskStatus}
        taskExecutionState={knownExecutionState}
        turnInFlight={isLoading}
        {startingTurn}
        {projectId}
        {taskId}
        {follow}
        onDismissError={handleDismissError}
        onThreadSwitch={handleThreadSwitch}
        isSwitching={threadSwitching}
        onMsgDelete={handleMsgDelete}
        onMsgFork={handleMsgFork}
        onMsgSend={handleMsgSend}
        onRegenerate={handleRegenerate}
        onSummaryEdit={handleSummaryEdit}
        onNavigateTask={handleMsgNavigateTask}
        {onViewChanges}
        {openCheckpointHash}
        {onToggleFullscreen}
        {isChatFullscreen}
        {historyLoading}
        historyNotice={historyNotice}
        onRetryHistory={() => { void reconcileAfterAbsence(true); }}
        hasEarlier={hasEarlier}
        loadingEarlier={loadingEarlier}
        onLoadEarlier={loadEarlierMessages}
        turns={chatTurns}
        turnsHasMore={turnsHasMore}
        onJumpToTurn={jumpToTurn}
    />
    {#if showStartButton}
        <StartPanel
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            {operatingMode}
            {isStarting}
            {isAgentSelected}
            onModelSelect={handleModelSelect}
            onOperatingModeToggle={handleOperatingModeToggle}
            onStartTask={handleStartTask}
        />
    {:else}
        <div class="chat-form-wrapper" style="padding-bottom: {keyboardOffset}px;">
        <ReviewBar
            {taskStatus}
            taskExecutionState={knownExecutionState}
            {showResumeButton}
            {startingTurn}
            {showRestartButton}
            {isStarting}
            {isCommitting}
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
            isLoading={isLoading}
            placeholder={t('chat.form.taskPlaceholder')}
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
            onInputFocus={onChatInputFocus}
            onInputBlur={onChatInputBlur}
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
