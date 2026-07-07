<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { get } from 'svelte/store';
    import type { StreamEvent, StreamEventType, Provider, Thread } from '$lib/types';
    import { PI_SENTINEL_URL, isAgentProvider } from '$lib/types';
    import { sendTaskMessage, getTaskMessages, createTaskMessage, deleteTaskMessagesAfter, deleteTaskMessage, regenerateTaskMessages, updateTaskMessage, stopTask, startTask, updateTask, moveTask, getTaskThreads, switchTaskThread, forkTaskThread, checkMessageDelete, commitAndDone, getCommitPreview, resetWorktree, summarizeTask, getOfflineStatus, onQueueChanged } from '$lib/api';
import { board as boardStore, config as configStore, showToast, showConfirm, commitDialog as commitDialogStore, selectedTask } from '$lib/store';
import { getStreamingContext, getTaskDiffStatsContext, getCommitQueueContext, getTaskEventContext } from '$lib/context';
    import { getAppStreamRef } from '$lib/stream-ref';
    import { formatToolArgs } from '$lib/utils/toolArgs';
    import { getDraftText, setDraftText, clearDraftText, loadDraftAttachments, saveDraftAttachments, clearDraftAttachments } from '$lib/storage';
    import ChatForm from './ChatForm.svelte';
    import ChatEntries from './ChatEntries.svelte';
    import ReviewBar from './ReviewBar.svelte';
    import StartPanel from './StartPanel.svelte';

    export interface ChatEntry {
        id: string;
        role: string;
        content: string;
        mergeLabel?: string; // label for merge_resolution cards
        isError?: boolean; // true for merge error cards
        reasoningContent?: string;
        hasReasoning?: boolean; // true if message has reasoning (even if content is suppressed)
        isStreaming?: boolean;
        threadId?: string;
        attachments?: Array<{ mimeType: string; data: string }>;
        toolCalls?: Array<{
            toolId: string;
            toolName: string;
            displayName?: string;
            args: Record<string, unknown>;
            raw_args?: string;
            sandbox?: boolean;
            path?: string;
            result?: string;
            resultTruncated?: boolean; // true if backend truncated the result
            originalLines?: number; // original line count before truncation
            originalBytes?: number; // original byte count before truncation
            messageId?: string; // tool_call message ID for fetching full result
            success?: boolean;
            duration?: number;
            isPending?: boolean;
            bytesWritten?: number;
        }>;
        toolCall?: {
            toolName: string;
            displayName?: string;
            args: Record<string, unknown>;
            raw_args?: string;
            result?: string;
            success?: boolean;
            duration?: number;
            isPending?: boolean;
        };
        error?: {
            code: string;
            message: string;
        };
        stats?: {
            model_name: string;
            prompt_tokens: number;
            predicted_tokens: number;
            prompt_ms: number;
            predicted_ms: number;
            duration: number;
        };
        createdAt?: string;
        queued?: boolean; // true if message is queued (sent while offline)
    }

    type DiffData = { diff: string; untracked: Array<{ path: string; lines?: string[]; line_count?: number; file_size?: number; is_binary?: boolean; truncated?: boolean; orig_added?: number }>; truncated_files?: string[]; original_file_stats?: Record<string, { added: number; removed: number }> };

    let { projectId, taskId, taskPrompt = '', taskStatus = 'backlog', disabled = false, taskTitle = '', onRefreshDiff, onMoveTask, onThreadSwitch, onViewChanges }: { projectId: string; taskId: string; taskPrompt?: string; taskStatus?: string; disabled?: boolean; taskTitle?: string; onRefreshDiff?: (data: DiffData) => void; onMoveTask?: (status: string) => void; onThreadSwitch?: () => void; onViewChanges?: (commitHash: string) => void } = $props();

    let entries: ChatEntry[] = $state([]);
    let connected = $state(false);
    let connecting = $state(true);
    let scrollEl: HTMLElement | null = null;

    let atBottom = $state(true);
    let prevScrollHeight = 0;
    let userInputPending = false;
    let inputTimeout: ReturnType<typeof setTimeout> | null = null;
    let stickyUserMessage = $state('');
    let showHeader = $state(false);
    let inputValue = $state('');
    let draftAttachments = $state<Array<{ file: File; preview: string; mimeType: string; base64: string }>>([]);

    // Load draft text from IndexedDB on init
    getDraftText(taskId).then(savedDraft => {
        if (savedDraft !== null) {
            inputValue = savedDraft;
        }
    }).catch(() => {});

    // Load draft attachments from IndexedDB on init
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

    // Save draft text to IndexedDB (debounced to avoid writes on every keystroke)
    let draftSaveTimer: ReturnType<typeof setTimeout> | null = null;
    const DRAFT_SAVE_DEBOUNCE = 100; // ms
    function handleInputSave(val: string) {
        if (draftSaveTimer) clearTimeout(draftSaveTimer);
        if (val) {
            draftSaveTimer = setTimeout(() => {
                setDraftText(taskId, val).catch(() => {});
                draftSaveTimer = null;
            }, DRAFT_SAVE_DEBOUNCE);
        }
    }

    // Clear draft text and attachments on send (called synchronously from ChatForm)
    function handleClearDraft() {
        clearDraftText(taskId).catch(() => {});
        draftAttachments = [];
    }

    // Save draft attachments to IndexedDB on every change
    $effect(() => {
        const atts = draftAttachments;
        saveDraftAttachments(taskId, atts.map(a => ({ mimeType: a.mimeType, data: a.base64 })));
    });

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');
    let planMode = $state(false);
    // Tracks when handleModelSelect's updateTask is in-flight, so the board store
    // $effect doesn't overwrite the user's selection with stale DB values.
    let modelUpdatePending = $state(false);
    let isAgentSelected = $derived(() => {
        if (!selectedUrl) return false;
        if (selectedUrl === PI_SENTINEL_URL) return true;
        const p = providers.find(p => p.url === selectedUrl);
        return p ? isAgentProvider(p) : false;
    });

    let currentAssistantId: string | null = $state(null);
    let activeToolCalls = $state<Map<string, { entryId: string }>>(new Map());
    // Track ALL processed tool IDs (including completed ones) to prevent
    // agent_end from creating duplicate tool calls in different entries.
    // Use a plain variable (not $state) to avoid reactivity resetting the Set.
    let processedToolIds = new Set<string>();

    let lastSentMessage: string | null = $state(null);
    let isStarting = $state(false);
    let isRegenerating = $state(false); // true between regenerate delete and echo arrival
    let firstConnectDone = $state(false); // track if initial onopen has fired
    let isCommitting = $state(false);
    // Suppress buffer replay for non-running tasks to avoid flicker from stale entries.
    // For in_progress tasks, buffer events are legitimate and must be replayed.
    let suppressEvents = $state(taskStatus !== 'in_progress');
    let commitQueueStatus = getCommitQueueContext();
    let taskEventContext = getTaskEventContext();
    let queueStatus = $derived($commitQueueStatus[projectId]?.taskID === taskId ? $commitQueueStatus[projectId].status : null);

    // Thread state
    let threads: Thread[] = $state([]);
    let activeThreadId = $state('');

    // Pending file checkpoints — set of assistant message IDs awaiting git commit
    let pendingCheckpoints = $state(new Set<string>());
    // Completed checkpoint labels — message_id -> label
    let checkpointLabels = $state(new Map<string, string>());
    // Completed checkpoint commit hashes — message_id -> commit_hash
    let checkpointCommitHashes = $state(new Map<string, string>());
    let checkpointHashesVersion = $state(0); // incremented when hashes change for reactivity

    // isLoading: only true when LLM is actively streaming tokens.
    // Shows stop button only during token generation, not during tool execution.
    let streamingIds = getStreamingContext();
    let isLoading = $derived($streamingIds.has(taskId));

    // Last entry index for regenerate button gating
    let lastEntryIdx = $derived(entries.length - 1);

    // awaitingResponse: true between sending a message and receiving the first token/tool_call.
    // Shows a spinner so the user knows the LLM is being contacted.
    let awaitingResponse = $state(false);

    // Queue state — set when a task is waiting for a concurrency gate slot.
    let queuePosition = $state(0);
    let queueEstimatedWait = $state(0);

    // Prompt processing progress (0-100) from llama.cpp's return_progress.
    // Shown in the "Awaiting LLM provider" spinner while prompt is being processed.
    let promptProgress = $state(0);

    // Context usage tracking
    let contextState = $state({ used: 0, limit: 0 });

    // Hybrid token counting: frontend counts per-token for smooth display,
    // backend stats events correct accuracy, complete event provides final count.
    let streamTokenCount = $state(0);
    let lastPromptTokens = $state(0);

    // Show "Start Task" button when task is in backlog (not yet started).
    let showStartButton = $derived(taskStatus === 'backlog' && !isLoading);

    // Show "Restart Task" button when task is in_progress, stalled (no active LLM, not queued),
    // and the last message is from the user (agent hasn't responded yet).
    let showRestartButton = $derived(
        taskStatus === 'in_progress' &&
        !isLoading &&
        queuePosition === 0 &&
        entries.length > 0 &&
        entries[entries.length - 1].role === 'user'
    );

    // Show "Resume" button in review bar when the last message is from the user
    // (agent hasn't responded yet — user sent a follow-up before moving to review).
    let showResumeButton = $derived(
        taskStatus === 'review' &&
        !isLoading &&
        entries.length > 0 &&
        entries[entries.length - 1].role === 'user'
    );

    // Set of user message IDs that have no assistant/tool_call entry after them.
    // Used to determine whether to show "Send" or "New Thread" in the edit form.
    let lastUserMessageIds = $derived(new Set((() => {
        const ids = new Set<string>();
        let lastUserId: string | null = null;
        for (const e of entries) {
            if (e.role === 'user') {
                lastUserId = e.id;
            } else if (lastUserId && (e.toolCalls || e.role === 'assistant')) {
                // An assistant/tool_call entry exists — the user message has a response (or one is in progress)
                lastUserId = null;
            }
        }
        // If there's a trailing user message with no assistant after it, add it
        if (lastUserId) ids.add(lastUserId);
        return ids;
    })()));

    // Precomputed per-entry metadata — avoids O(n²) lookups inside {#each}.
    interface EntryMeta {
        cpState: { isPending: boolean; commitLabel?: string; commitHash?: string } | null;
        cpThread: Thread | null;
        isLastUserMessage: boolean;
    }
    let entryMetas = $derived(((() => {
        const metas: EntryMeta[] = new Array(entries.length);
        const streaming = isLoading;
        for (let i = 0; i < entries.length; i++) {
            const entry = entries[i];
            metas[i] = entry.role === 'user'
                ? {
                    cpState: getCheckpointEntryCheckpointState(i),
                    cpThread: threads.length > 0 ? getCheckpointThreads(entry, i === 0) : null,
                    isLastUserMessage: lastUserMessageIds.has(entry.id) && !streaming,
                }
                : { cpState: null, cpThread: null, isLastUserMessage: false };
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
    function handleMsgFork(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        handleEditFork(msgId, origContent, newContent, atts);
    }
    function handleMsgSend(msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) {
        handleEditSend(msgId, origContent, newContent, atts);
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
            selectedTask.set(task);
        }
    }

    // Watch for task config changes from the board store (e.g., inline edit)
    // Updates model selection when the task's model/provider is changed externally.
    // Skips model fields when a local updateTask is in-flight to avoid overwriting
    // the user's selection with stale DB values.
    $effect(() => {
        const boardTask = $boardStore?.tasks.find(t => t.id === taskId);
        if (boardTask) {
            if (!modelUpdatePending && boardTask.llama_server_url) selectedUrl = boardTask.llama_server_url;
            if (!modelUpdatePending && boardTask.model_name) selectedModel = boardTask.model_name;
            if (!modelUpdatePending && boardTask.reasoning_effort) selectedReasoningEffort = boardTask.reasoning_effort;
            planMode = boardTask.plan_mode ?? false;
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

    function ensureAssistantEntry(): ChatEntry {
        if (currentAssistantId) {
            const entry = entries.find(e => e.id === currentAssistantId);
            if (entry) return entry;
        }
        const id = genId();
        currentAssistantId = id;
        const entry: ChatEntry = { id, role: 'assistant', content: '', isStreaming: true, createdAt: new Date().toISOString() };
        entries = [...entries, entry];
        return entry;
    }

    function closeCurrentAssistant() {
        if (currentAssistantId) {
            const entry = entries.find(e => e.id === currentAssistantId);
            if (entry) entry.isStreaming = false;
            currentAssistantId = null;
        }
    }

    /** Mark this task as actively streaming tokens. */
    function markStreaming() {
        streamingIds.update(s => { const n = new Set(s); n.add(taskId); return n; });
    }

    /** Mark this task as no longer streaming. */
    function markNotStreaming() {
        streamingIds.update(s => { const n = new Set(s); n.delete(taskId); return n; });
        queuePosition = 0;
        queueEstimatedWait = 0;
        streamTokenCount = 0;
        contextState.used = lastPromptTokens;
        promptProgress = 0;
    }

    /** Clear the "awaiting LLM" spinner — first response from provider has arrived. */
    function clearAwaiting() {
        if (awaitingResponse) awaitingResponse = false;
        promptProgress = 0;
    }

    function handleEvent(event: StreamEvent) {
        switch (event.type) {
            case 'token': {
                if (suppressEvents) break;
                clearAwaiting();
                markStreaming();
                // First token of a new turn — reset counter
                if (!currentAssistantId) {
                    streamTokenCount = 0;
                }
                streamTokenCount++;
                contextState.used = lastPromptTokens + streamTokenCount;
                const data = event.data as { text: string };
                const entry = ensureAssistantEntry();
                if (!currentAssistantId) currentAssistantId = entry.id;
                entry.content += data.text;
                scroll();
                break;
            }
            case 'reasoning': {
                if (suppressEvents) break;
                clearAwaiting();
                // First reasoning chunk of a new turn — reset counter
                if (!currentAssistantId) {
                    streamTokenCount = 0;
                }
                streamTokenCount++;
                contextState.used = lastPromptTokens + streamTokenCount;
                const data = event.data as { text: string };
                const entry = ensureAssistantEntry();
                entry.reasoningContent = (entry.reasoningContent || '') + data.text;
                scroll();
                break;
            }
            case 'tool_call': {
                if (suppressEvents) break;
                clearAwaiting();
                const data = event.data as { tool_id: string; tool_name: string; tool_display_name?: string; args: Record<string, unknown>; raw_args?: string; sandbox?: boolean; path?: string };
                // Skip incomplete tool call deltas (empty tool_id from streaming fragments)
                if (!data.tool_id || data.tool_id === '') {
                    break;
                }
                // Note: we do NOT block on processedToolIds here — streaming updates
                // for the same tool_id must pass through so raw_args can grow.
                // processedToolIds is only checked when creating a NEW tool call below.
                // Attach tool call to the current assistant entry, or fall back to
                // the last existing assistant entry (e.g., agent_end sends tool_call
                // events after the streaming assistant was already closed).
                let entry = currentAssistantId
                    ? entries.find(e => e.id === currentAssistantId) || null
                    : null;
                if (!entry) {
                    // Find last assistant entry to merge tool calls into
                    for (let i = entries.length - 1; i >= 0; i--) {
                        if (entries[i].role === 'assistant') {
                            entry = entries[i];
                            break;
                        }
                    }
                }
                if (!entry) {
                    entry = ensureAssistantEntry();
                }
                // If the LLM sent the tool name as text content before the tool call,
                // clear it in-place to avoid a blank bubble
                if (entry.content.trim() === data.tool_name && !entry.reasoningContent) {
                    entry.content = '';
                }
                if (!entry.toolCalls) entry.toolCalls = [];
                // Dedup: if a tool_call with this tool_id already exists (from buffer replay
                // of an earlier streaming delta), replace it with a new object so the
                // keyed $each in ChatMessage detects the change and re-renders ToolCall.
                const existing = entry.toolCalls.find(tc => tc.toolId === data.tool_id);
                if (existing) {
                    // Skip updates for completed tool calls (agent_end duplicates after tool_result).
                    if (!existing.isPending && existing.result !== undefined) {
                        break;
                    }
                    const updated = { ...existing,
                        toolName: data.tool_name,
                        displayName: data.tool_display_name,
                        args: data.args || {},
                        raw_args: data.raw_args,
                        sandbox: data.sandbox,
                        path: data.path,
                    };
                    // Don't re-enable isStreaming for deduped tool calls (e.g., from agent_end).
                    // The entry may have already been closed by the tool_result handler.
                    entry.toolCalls = entry.toolCalls.map(tc =>
                        tc.toolId === data.tool_id ? updated : tc
                    );
                } else if (processedToolIds.has(data.tool_id)) {
                    // Tool ID was already processed (agent_end duplicate) — skip.
                    break;
                } else {
                    // Mark as processed so agent_end duplicates are caught above.
                    processedToolIds.add(data.tool_id);
                    const ci = entry.content.length;
                    const ri = (entry.reasoningContent || '').length;
                    // Create new array reference so $derived in ChatMessage recomputes.
                    // Svelte 5 tracks array references, not in-place mutations.
                    entry.toolCalls = [...(entry.toolCalls || []), {
                        toolId: data.tool_id,
                        toolName: data.tool_name,
                        displayName: data.tool_display_name,
                        args: data.args || {},
                        raw_args: data.raw_args,
                        sandbox: data.sandbox,
                        path: data.path,
                        isPending: true,
                        contentIndex: ci,
                        reasoningIndex: ri
                    }];
                    // Only set isStreaming for NEW tool calls (during active streaming).
                    // Deduped tool calls from agent_end should not re-enable the cursor.
                    entry.isStreaming = true;
                    activeToolCalls.set(data.tool_id, { entryId: entry.id });
                }
                scroll();
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number; args?: Record<string, unknown>; raw_args?: string; sandbox?: boolean; path?: string };
                const ref = activeToolCalls.get(data.tool_id);
                activeToolCalls.delete(data.tool_id);
                let updated = false;
                if (ref) {
                    const entry = entries.find(e => e.id === ref.entryId);
                    if (entry && entry.toolCalls) {
                        const tc = entry.toolCalls.find(t => t.toolId === data.tool_id);
                        if (tc) {
                            tc.result = data.output;
                            tc.success = data.success;
                            tc.duration = data.duration;
                            tc.isPending = false;
                            if (data.args) tc.args = data.args;
                            if (data.raw_args) tc.raw_args = data.raw_args;
                            if (data.sandbox !== undefined) tc.sandbox = data.sandbox;
                            if (data.path) tc.path = data.path;
                            updated = true;
                        }
                    }
                }
                // Fallback: search all entries for a pending tool call with this tool_id.
                // This handles the case where the tool call was loaded from DB (after page refresh)
                // and is not yet in activeToolCalls.
                if (!updated) {
                    for (const entry of entries) {
                        if (entry.toolCalls) {
                            const tc = entry.toolCalls.find(t => t.toolId === data.tool_id && t.isPending);
                            if (tc) {
                                tc.result = data.output;
                                tc.success = data.success;
                                tc.duration = data.duration;
                                tc.isPending = false;
                                if (data.args) tc.args = data.args;
                                if (data.raw_args) tc.raw_args = data.raw_args;
                                if (data.sandbox !== undefined) tc.sandbox = data.sandbox;
                                if (data.path) tc.path = data.path;
                                updated = true;
                                break;
                            }
                        }
                    }
                }
                // All tool results for this turn are done — close the assistant entry
                // so the next turn's reasoning/tool calls go into a new entry.
                // The server will now make another LLM call — show the awaiting spinner.
                if (activeToolCalls.size === 0) {
                    closeCurrentAssistant();
                    awaitingResponse = true;
                }
                scroll();
                break;
            }
            case 'tool_progress': {
                const data = event.data as { tool_id?: string; bytes_written: number; path?: string };
                // Update the matching pending tool call with bytes written and path
                for (const entry of entries) {
                    if (entry.toolCalls) {
                        for (const tc of entry.toolCalls) {
                            if (tc.isPending && (!data.tool_id || tc.toolId === data.tool_id)) {
                                tc.bytesWritten = data.bytes_written;
                                if (data.path) tc.path = data.path;
                            }
                        }
                    }
                }
                break;
            }
            case 'tool_output': {
                // Streaming output from a running tool (e.g., bash stdout/stderr).
                // Append to the pending tool call's result so the UI updates in real-time.
                const data = event.data as { tool_id: string; text: string };
                for (const entry of entries) {
                    if (entry.toolCalls) {
                        for (const tc of entry.toolCalls) {
                            if (tc.isPending && tc.toolId === data.tool_id) {
                                tc.result = (tc.result || '') + data.text;
                            }
                        }
                    }
                }
                scroll();
                break;
            }
            case 'user_message': {
                const data = event.data as { id?: string; role: string; content: string; thread_id?: string; attachments?: Array<{ mimeType: string; data: string }> };
                // If this is an echo of a message we just sent, update the frontend entry's ID with the backend ID
                if (lastSentMessage === data.content) {
                    lastSentMessage = null;
                    isRegenerating = false;
                    if (data.id) {
                        // Find the last user entry (our optimistic message) and update its ID
                        for (let i = entries.length - 1; i >= 0; i--) {
                            if (entries[i].role === 'user' && entries[i].content === data.content) {
                                entries[i].id = data.id;
                                if (data.thread_id) entries[i].threadId = data.thread_id;
                                if (data.attachments) entries[i].attachments = data.attachments;
                                break;
                            }
                        }
                    }
                    break;
                }
                // Dedup: if this message has a backend ID that already exists in entries
                // (e.g., from sync buffer replaying a message already loaded via loadHistory), skip
                if (data.id && entries.some(e => e.id === data.id)) {
                    break;
                }
                closeCurrentAssistant();
                entries = [...entries, { id: data.id || genId(), role: data.role, content: data.content, threadId: data.thread_id, isStreaming: false, createdAt: new Date().toISOString() }];
                break;
            }
            case 'complete': {
                markNotStreaming();
                awaitingResponse = false;
                const data = event.data as { state: string; model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number; assistant_message_id?: string };
                // Update streaming assistant entry ID with the real backend ID (for checkpoint hash matching)
                // Must happen BEFORE closeCurrentAssistant() which clears currentAssistantId
                if (data.assistant_message_id && currentAssistantId) {
                    const entry = entries.find(e => e.id === currentAssistantId);
                    if (entry && entry.id !== data.assistant_message_id) {
                        // Only update if no other entry already has this ID (avoids duplicate keys)
                        if (!entries.some(e => e.id === data.assistant_message_id)) {
                            entry.id = data.assistant_message_id;
                        }
                    }
                }
                closeCurrentAssistant();
                // Safety net: clear isStreaming on ALL assistant entries.
                // For Pi, the tool_result handler may have already closed the current entry,
                // leaving currentAssistantId=null. But a stale entry (e.g., from a split
                // caused by closeCurrentAssistant during tool results) might still have
                // isStreaming=true. Clear all to ensure the blinking cursor disappears.
                for (const e of entries) {
                    if (e.role === 'assistant' && e.isStreaming) {
                        e.isStreaming = false;
                    }
                }
                // Store total context (prompt + predicted) for context usage display.
                // prompt_tokens = input sent to LLM, predicted_tokens = output generated.
                // Total context consumed = both, since output becomes part of history for next turn.
                if (data.prompt_tokens !== undefined) {
                    lastPromptTokens = data.prompt_tokens + (data.predicted_tokens || 0);
                    contextState.used = lastPromptTokens;
                }
                // Attach stats to the last assistant entry
                const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && !e.toolCalls);
                if (lastAssistant && data.predicted_tokens !== undefined) {
                    lastAssistant.stats = { model_name: data.model_name, prompt_tokens: data.prompt_tokens, predicted_tokens: data.predicted_tokens, prompt_ms: data.prompt_ms, predicted_ms: data.predicted_ms, duration: data.duration };
                }
                // Force scroll to bottom after all state changes settle.
                // Multiple re-renders happen (cursor disappears, stats appear, tool results finalize).
                // Double requestAnimationFrame in scroll() ensures the final DOM is measured.
                scroll(true);
                break;
            }
            case 'error': {
                clearAwaiting();
                markNotStreaming();
                closeCurrentAssistant();
                isRegenerating = false;
                connected = false;
                const data = event.data as { message: string; code?: string };
                const code = data.code || 'error';
                // Skip ws_error from createTaskStream's onerror — the onclose handler
                // will show a more descriptive "Connection lost" message instead
                if (code === 'ws_error') {
                    break;
                }
                // Skip "Task stopped" — the status badge already reflects the change
                if (code === 'stopped') {
                    break;
                }
                const message = data.message || 'An error occurred';
                const entry: ChatEntry = {
                    id: genId(),
                    role: 'error',
                    content: message,
                    isStreaming: false,
                    error: { code, message },
                    createdAt: new Date().toISOString(),
                };
                entries = [...entries, entry];
                // Persist to backend (except ws_disconnected which is ephemeral)
                if (code !== 'ws_disconnected') {
                    createTaskMessage(projectId, taskId, 'error', `[${code}] ${message}`).catch(() => {});
                }
                break;
            }
            case 'status': {
                const data = event.data as { state: string; activity?: string };
                if (data.state === 'pulling_image') {
                    // Image is being pulled — show ephemeral system message (not persisted)
                    const imgName = data.activity?.replace('Pulling ', '') || 'sandbox image';
                    entries = [...entries, { id: genId(), role: 'system', content: `Pulling ${imgName}...`, isStreaming: false, createdAt: new Date().toISOString() }];
                } else if (data.state === 'building_image') {
                    // Image is being built — show ephemeral system message (not persisted)
                    entries = [...entries, { id: genId(), role: 'system', content: 'Building sandbox image from Dockerfile...', isStreaming: false, createdAt: new Date().toISOString() }];
                } else if (data.state === 'starting_container') {
                    // Container is being created/started — show ephemeral system message (not persisted)
                    entries = [...entries, { id: genId(), role: 'system', content: 'Starting sandbox container...', isStreaming: false, createdAt: new Date().toISOString() }];
                } else if (data.state === 'running') {
                    // A new executor is starting (e.g., lazy-start after user follow-up).
                    // Clear pending tool calls and streaming state from any previous executor
                    // to avoid mixing tool call deltas from concurrent executors.
                    // Only close current assistant if it's empty (stale entry), not if it has content (active streaming).
                    if (currentAssistantId) {
                        const entry = entries.find(e => e.id === currentAssistantId);
                        if (!entry || (!entry.content && !entry.reasoningContent)) {
                            closeCurrentAssistant();
                        }
                    }
                    activeToolCalls.clear();
                    // Don't clear processedToolIds here — Pi can send multiple
                    // status:running events (e.g., extension_ui_request) within a turn.
                    // Clearing here would lose tool IDs and cause agent_end duplicates.
                    // Remove assistant entries with only pending tool calls (no text AND no reasoning).
                    // Entries with reasoning content should be kept even if they have no text content.
                    entries = entries.filter(e => !(e.toolCalls && e.toolCalls.some(tc => tc.isPending) && !e.content && !e.reasoningContent));
                    // Mark task as streaming so badge updates on board view
                    markStreaming();
                    // Gate acquired — task is no longer queued. Clear stale queue position.
                    queuePosition = 0;
                    queueEstimatedWait = 0;
                    // Keep awaitingResponse until first token/tool_call (clearAwaiting handles it).
                } else if (data.state === 'cancelled') {
                    // Response was cancelled by new user message — clear streaming state.
                    closeCurrentAssistant();
                    activeToolCalls.clear();
                    processedToolIds.clear(); // Cancel means turn is over, safe to clear
                    entries = entries.filter(e => !e.isStreaming);
                    markNotStreaming();
                    awaitingResponse = false;
                } else if (['stopped', 'failed', 'idle', 'completed', 'awaiting_review'].includes(data.state)) {
                    // Executor finished — clear streaming state.
                    // Do NOT call closeCurrentAssistant() here — Pi sends status "idle"
                    // BEFORE the tool_call/tool_result events from agent_end. Calling
                    // closeCurrentAssistant() would clear currentAssistantId, causing
                    // the subsequent tool_call events to create a new empty assistant
                    // entry (with isStreaming=true / blue cursor) instead of merging
                    // into the existing entry. Let the 'complete' event handle that.
                    markNotStreaming();
                    awaitingResponse = false;
                }
                break;
            }
            case 'build_progress': {
                const data = event.data as { line: string };
                // Append build/pull progress line as ephemeral system message (not persisted)
                if (data.line) {
                    entries = [...entries, { id: genId(), role: 'system', content: data.line, isStreaming: false, createdAt: new Date().toISOString() }];
                }
                break;
            }
            case 'hint': {
                const data = event.data as { content: string; created_at: string };
                // Loop-detection hint — show as a system message in real-time
                if (data.content) {
                    entries = [...entries, { id: genId(), role: 'system', content: data.content, isStreaming: false, createdAt: data.created_at || new Date().toISOString() }];
                }
                break;
            }
            case 'stats': {
                const data = event.data as { model_name: string; prompt_tokens: number; predicted_tokens: number; predicted_ms: number };
                // Sync frontend counter with backend count for accuracy.
                // Update lastPromptTokens from the backend's prompt_tokens (accurate per-turn baseline).
                // This ensures context usage is correct across tool call turns (no complete event between them).
                if (data.prompt_tokens !== undefined && data.prompt_tokens > 0) {
                    lastPromptTokens = data.prompt_tokens;
                }
                if (data.predicted_tokens !== streamTokenCount) {
                    streamTokenCount = data.predicted_tokens;
                    contextState.used = lastPromptTokens + streamTokenCount;
                }
                // Update real-time stats on the current streaming assistant entry
                if (currentAssistantId) {
                    const entry = entries.find(e => e.id === currentAssistantId);
                    if (entry) {
                        entry.stats = {
                            model_name: data.model_name,
                            prompt_tokens: data.prompt_tokens ?? entry.stats?.prompt_tokens ?? 0,
                            predicted_tokens: data.predicted_tokens,
                            prompt_ms: entry.stats?.prompt_ms ?? 0,
                            predicted_ms: data.predicted_ms,
                            duration: entry.stats?.duration ?? 0,
                        };
                    }
                }
                break;
            }
            case 'diff_changed': {
                const data = event.data as DiffData;
                onRefreshDiff?.(data);
                break;
            }
            case 'thread_switch': {
                // Thread was switched (possibly from another tab or API call)
                const data = event.data as { thread_id: string; task_id: string };
                updateActiveThread(data.thread_id);
                onThreadSwitch?.();
                // Skip loadHistory during regenerate to avoid clobbering the
                // optimistic user message entry before the echo arrives.
                // loadHistory will be called after the LLM response completes.
                if (!isRegenerating) {
                    // Clear streaming state to prevent old thread's entries from leaking
                    closeCurrentAssistant();
                    activeToolCalls.clear();
                    processedToolIds.clear();
                    entries = [];
                    currentAssistantId = null;
                    streamTokenCount = 0;
                    awaitingResponse = false;
                    loadHistory(true);
                }
                break;
            }
            case 'queue': {
                const data = event.data as { position: number; estimated_wait_seconds: number };
                queuePosition = data.position || 0;
                queueEstimatedWait = data.estimated_wait_seconds || 0;
                // Keep awaitingResponse visible while queued
                if (queuePosition > 0) {
                    awaitingResponse = true;
                }
                break;
            }
            case 'prompt_progress': {
                // Prompt processing progress from llama.cpp (0-100%).
                // Shown in the "Awaiting LLM provider" message while the prompt is being processed.
                const data = event.data as { total: number; cache: number; processed: number; time_ms: number };
                if (data.total > 0) {
                    // Actual progress = tokens processed / total tokens
                    // (cache tokens are instant, so processed includes cached + newly processed)
                    promptProgress = Math.round((data.processed / data.total) * 100);
                }
                break;
            }
            case 'summarization': {
                const data = event.data as { id?: string; summary: string; created_at?: string; prompt_tokens?: number; predicted_tokens?: number; prompt_ms?: number; predicted_ms?: number; model_name?: string };
                // Dedup: skip if this message ID already exists
                if (data.id && entries.some(e => e.id === data.id)) {
                    break;
                }
                entries = [...entries, {
                    id: data.id || genId(),
                    role: 'summarization',
                    content: data.summary || 'Context was summarized to free space',
                    isStreaming: false,
                    createdAt: data.created_at || new Date().toISOString(),
                    stats: data.model_name ? {
                        model_name: data.model_name,
                        prompt_tokens: data.prompt_tokens ?? 0,
                        predicted_tokens: data.predicted_tokens ?? 0,
                        prompt_ms: data.prompt_ms ?? 0,
                        predicted_ms: data.predicted_ms ?? 0,
                        duration: (data.prompt_ms ?? 0) + (data.predicted_ms ?? 0),
                    } : undefined,
                }];
                break;
            }
            case 'merge_resolution': {
                const data = event.data as { id: string; conflicted_files?: string[]; summary?: string; content: string; created_at: string; label?: string; is_error?: boolean };
                const content = data.content || 'Merged';
                const label = data.label || (content.includes('conflict') ? 'Merge conflicts resolved' : 'Merged');
                entries = [...entries, {
                    id: data.id || genId(),
                    role: 'merge_resolution',
                    content,
                    mergeLabel: label,
                    isError: data.is_error || false,
                    isStreaming: false,
                    createdAt: data.created_at || new Date().toISOString(),
                }];
                break;
            }
            case 'git_checkpoint_pending': {
                const data = event.data as { message_id: string };
                const pending = new Set(pendingCheckpoints);
                pending.add(data.message_id);
                pendingCheckpoints = pending;
                break;
            }
            case 'git_checkpoint_complete': {
                const data = event.data as { message_id: string; commit_hash: string; label: string };
                const pending = new Set(pendingCheckpoints);
                pending.delete(data.message_id);
                pendingCheckpoints = pending;
                if (data.label) {
                    const labels = new Map(checkpointLabels);
                    labels.set(data.message_id, data.label);
                    checkpointLabels = labels;
                }
                if (data.commit_hash) {
                    const hashes = new Map(checkpointCommitHashes);
                    hashes.set(data.message_id, data.commit_hash);
                    // Also store by entry ID so lookup works regardless of timing
                    // Use currentAssistantId if available, otherwise find last assistant entry with tool calls
                    let entryId = currentAssistantId;
                    if (!entryId) {
                        for (let i = entries.length - 1; i >= 0; i--) {
                            const entry = entries[i];
                            if (entry && entry.role === 'assistant' && entry.toolCalls && entry.toolCalls.length > 0) {
                                entryId = entry.id;
                                break;
                            }
                        }
                    }
                    if (entryId) {
                        hashes.set(entryId, data.commit_hash);
                    }
                    checkpointCommitHashes = hashes;
                    checkpointHashesVersion++; // trigger reactive update
                }
                break;
            }
        }
        scroll();
    }

    /** Update providers from the config store (reacts to settings changes). */
    function updateProvidersFromStore() {
        const cfg = get(configStore);
        if (cfg && cfg.providers) {
            providers = cfg.providers;
            // Initialize model selection from highest-priority provider's default model
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

    /** Look up the context window for the currently selected model. */
    function updateContextLimit() {
        const provider = providers.find(p => p.url === selectedUrl);
        if (!provider) {
            contextState.limit = 0;
            return;
        }
        const model = provider.models.find((m: { name: string }) => m.name === selectedModel);
        contextState.limit = (model as { context_window?: number })?.context_window || 0;
    }

    async function loadHistory(scrollAfter = false) {
        try {
            const messages = await getTaskMessages(projectId, taskId);
            // Restore checkpoint commit hashes from persisted messages
            const hashes = new Map<string, string>();
            for (const m of messages) {
                if (m.git_commit_hash) {
                    hashes.set(m.id, m.git_commit_hash);
                }
            }
            checkpointCommitHashes = hashes;

            // Build entries from DB messages
            const dbEntries = messages.filter(m => m.role !== 'title').map(m => {
                const base = { threadId: m.thread_id || undefined };
                // Restore error entries from persisted data
                if (m.role === 'error') {
                    const content = m.content || '';
                    const match = content.match(/^\[([^\]]+)\]\s*(.*)/);
                    const code = match ? match[1] : 'error';
                    const message = match ? match[2] : content;
                    // Skip "Task stopped" errors — the status badge reflects the change
                    if (code === 'stopped') return null;
                    return {
                        ...base, id: m.id, role: 'error' as const, content: message, isStreaming: false, createdAt: m.created_at,
                        error: { code, message },
                    };
                }
                // Restore tool call entries from persisted data — merged into preceding assistant entry
                if (m.role === 'tool_call' && m.tool_calls) {
                    const tc = JSON.parse(m.tool_calls);
                    const result = m.tool_result ? JSON.parse(m.tool_result) : null;
                    const isPending = !result;
                    return {
                        ...base, id: m.id, role: 'assistant' as const, content: '', isStreaming: false, createdAt: m.created_at,
                        toolCalls: [{
                            toolId: tc.tool_id || m.id,
                            toolName: tc.tool_name,
                            displayName: tc.tool_display_name,
                            args: tc.args ?? {},
                            raw_args: tc.raw_args,
                            sandbox: tc.sandbox,
                            result: result?.output,
                            resultTruncated: m.result_truncated,
                            originalLines: result?.original_lines,
                            originalBytes: result?.original_bytes,
                            messageId: m.id, // tool_call message ID for fetching full result
                            success: result?.success ?? true,
                            duration: result?.duration,
                            isPending,
                        }],
                    };
                }
                // Parse attachments if present
                let parsedAttachments: Array<{ mimeType: string; data: string }> | undefined;
                if (m.attachments) {
                    try {
                        const atts = typeof m.attachments === 'string' ? JSON.parse(m.attachments) : m.attachments;
                        if (Array.isArray(atts)) parsedAttachments = atts;
                    } catch { /* ignore parse errors */ }
                }
                const entry: Partial<ChatEntry> = {
                    ...base, id: m.id, role: m.role, content: m.content, reasoningContent: m.reasoning_content, hasReasoning: m.has_reasoning, attachments: parsedAttachments, isStreaming: false, createdAt: m.created_at,
                };
                if (m.role === 'merge_resolution') {
                    const isErr = m.content?.includes('Merge failed');
                    (entry as ChatEntry).mergeLabel = isErr ? 'Merge failed' : (m.content?.includes('conflict') ? 'Merge conflicts resolved' : 'Merged');
                    (entry as ChatEntry).isError = isErr || false;
                }
                return {
                    ...entry,
                    stats: m.model_name ? {
                        model_name: m.model_name,
                        prompt_tokens: m.prompt_tokens ?? 0,
                        predicted_tokens: m.predicted_tokens ?? 0,
                        prompt_ms: m.prompt_ms ?? 0,
                        predicted_ms: m.predicted_ms ?? 0,
                        duration: m.duration ?? 0,
                    } : undefined,
                };
            });
            // Filter out null entries (e.g., skipped "stopped" errors)
            const validDbEntries = dbEntries.filter((e: ChatEntry | null): e is ChatEntry => e !== null);
            // Merge toolCalls entries into the preceding assistant entry
            const mergedDbEntries = mergeToolCallsIntoAssistant(validDbEntries as ChatEntry[]);
            // Merge with existing entries (e.g., from buffer replay).
            // DB entries have backend UUIDs; buffer entries have random genId() IDs.
            // Preserve buffer entries whose IDs are NOT in the DB (not yet persisted).
            const dbIds = new Set(messages.map(m => m.id));
            const existingNonDb = entries.filter(e => !dbIds.has(e.id));
            // Collect tool_ids from DB entries to deduplicate buffer entries.
            // Buffer replay may create duplicate tool call entries (random IDs vs DB UUIDs).
            const dbToolIds = new Set<string>();
            for (const e of mergedDbEntries) {
                if (e.toolCalls) {
                    for (const tc of e.toolCalls) {
                        dbToolIds.add(tc.toolId);
                    }
                }
            }
            // Remove buffer entries whose tool calls are already in the DB.
            const dedupedNonDb = existingNonDb.filter(e => {
                if (e.toolCalls) {
                    return !e.toolCalls.some(tc => dbToolIds.has(tc.toolId));
                }
                return true;
            });
            entries = [...mergedDbEntries, ...dedupedNonDb];
            // Remove entries with pending tool calls from non-running tasks.
            // For running tasks (in_progress), pending tool calls are in-flight and should be kept.
            // For stopped/completed tasks, pending calls are stale leftovers.
            if (taskStatus !== 'in_progress') {
                entries = entries.filter(e => !e.toolCalls?.some(tc => tc.isPending));
            }
            // Restore context usage from the last assistant message.
            // Use total context (prompt + predicted) to reflect full window consumed.
            const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && e.stats);
            if (lastAssistant?.stats) {
                lastPromptTokens = (lastAssistant.stats.prompt_tokens || 0) + (lastAssistant.stats.predicted_tokens || 0);
                contextState.used = lastPromptTokens;
            }
            // After replacing entries, the DOM re-renders and scroll state may be lost.
            // Scroll to bottom on the next frame.
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

    function mergeToolCallsIntoAssistant(allEntries: ChatEntry[]): ChatEntry[] {
        // DB ordering: assistant(text+reasoning) → tool_call → assistant(response to tool) → tool_call...
        // Merge each tool-call-only entry into the preceding assistant entry
        // so tool calls appear below the assistant's reasoning/text in the UI.
        const merged: ChatEntry[] = [];
        for (const entry of allEntries) {
            if (entry.toolCalls && entry.toolCalls.length > 0 && !entry.content) {
                // This is a tool-call-only entry — merge into preceding assistant
                const prev = merged[merged.length - 1];
                if (prev && prev.role === 'assistant') {
                    if (!prev.toolCalls) prev.toolCalls = [];
                    prev.toolCalls.push(...entry.toolCalls);
                } else {
                    // No preceding assistant entry, keep as-is
                    merged.push(entry);
                }
            } else {
                merged.push(entry);
            }
        }
        return merged;
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

    /** Get the thread whose label should be shown at a given checkpoint.
     * Finds the most specific (deepest) ancestor of the active thread whose
     * fork point is at or before the message time.
     * When requireOptions is true, only returns a thread if 2+ threads are
     * relevant at this checkpoint (used for the first message to avoid
     * showing a divider with no switch options). */
    function getCheckpointThreads(entry: ChatEntry, requireOptions = false): Thread | null {
        if (!threads.length) return null;
        const entryTime = entry.createdAt;
        if (!entryTime) return null;

        const activeThread = threads.find(t => t.is_active);
        if (!activeThread) return null;

        // Walk from active thread up to root. Return first (most specific) ancestor
        // whose checkpoint is at or before the entry time.
        const threadMap = new Map(threads.map(t => [t.id, t]));
        let current: Thread = activeThread;
        let checkpointThread: Thread | null = null;

        while (true) {
            const ct = current.checkpoint_message_time;
            // No checkpoint (root/Main), zero/epoch, or checkpoint <= entry → qualifies
            if (!ct || ct.startsWith('0001-') || ct.startsWith('1970-') || ct <= entryTime) {
                checkpointThread = current;
                break;
            }
            // This thread's fork point is after the entry; try parent
            const parent = threadMap.get(current.parent_thread_id || '');
            if (!parent) return null; // fallback to null
            current = parent;
        }

        // For the first message, only show checkpoint if there are actual switch options
        if (requireOptions) {
            let relevantCount = 0;
            for (const t of threads) {
                const ct = t.checkpoint_message_time;
                if (!ct || ct.startsWith('0001-') || ct.startsWith('1970-') || ct <= entryTime) {
                    relevantCount++;
                }
            }
            if (relevantCount < 2) return null;
        }

        return checkpointThread;
    }

    /** Get the checkpoint pending/label state for the checkpoint divider before a user message.
     * Looks at the next assistant entry (with tool calls or hash) after the given entry index,
     * since the checkpoint is created after tool calls in the same turn.
     * Falls back to walking backward if no forward entry has a checkpoint.
     * Returns { isPending, commitLabel, commitHash } or null if no checkpoint state to show. */
    function getCheckpointEntryCheckpointState(entryIdx: number): { isPending: boolean; commitLabel?: string; commitHash?: string } | null {
        // First: look forward to find the next assistant entry with tool calls or a checkpoint hash
        // (checkpoint is created after tool calls, so hash is on the assistant msg in the same turn)
        for (let i = entryIdx + 1; i < entries.length; i++) {
            const next = entries[i];
            if (next.role === 'assistant') {
                const hasToolCalls = next.toolCalls && next.toolCalls.length > 0;
                const hasHash = checkpointCommitHashes.has(next.id);
                if (hasToolCalls || hasHash) {
                    return {
                        isPending: pendingCheckpoints.has(next.id),
                        commitLabel: checkpointLabels.get(next.id),
                        commitHash: checkpointCommitHashes.get(next.id) || undefined,
                    };
                }
                break; // found the turn's assistant entry, no checkpoint
            }
        }
        // Fallback: walk backward to find the last assistant entry with tool calls or a checkpoint hash
        for (let i = entryIdx - 1; i >= 0; i--) {
            const prev = entries[i];
            if (prev.role === 'assistant') {
                const hasToolCalls = prev.toolCalls && prev.toolCalls.length > 0;
                const hasHash = checkpointCommitHashes.has(prev.id);
                if (hasToolCalls || hasHash) {
                    return {
                        isPending: pendingCheckpoints.has(prev.id),
                        commitLabel: checkpointLabels.get(prev.id),
                        commitHash: checkpointCommitHashes.get(prev.id) || undefined,
                    };
                }
            }
            if (prev.role === 'user') break; // stop at previous user message
        }
        return null;
    }


    /** Update local thread state to reflect a new active thread (avoids refetching all threads). */
    function updateActiveThread(threadId: string) {
        activeThreadId = threadId;
        threads = threads.map(t => ({ ...t, is_active: t.id === threadId }));
    }

    async function handleThreadSwitch(threadId: string) {
        if (threadId === activeThreadId) return;
        try {
            const switched = await switchTaskThread(projectId, taskId, threadId);
            // Refresh threads list from server to pick up any changes (deletions, etc.)
            threads = await getTaskThreads(projectId, taskId);
            activeThreadId = switched.id;
            // Clear streaming state before loading new thread's messages.
            // loadHistory() merges DB entries with existing non-DB entries,
            // so old thread's streaming entries would leak into the new thread.
            closeCurrentAssistant();
            activeToolCalls.clear();
                    processedToolIds.clear();
            entries = [];
            currentAssistantId = null;
            streamTokenCount = 0;
            awaitingResponse = false;
            // Load messages for the new thread immediately (don't wait for WS event)
            await loadHistory(true);
            // Notify parent to refresh the diff panel for the new thread
            onThreadSwitch?.();
            // Update URL with new thread param
            updateThreadUrl(threadId);
            // No need to reconnect WebSocket — broadcaster is per-task, not per-thread.
            // Server sends thread_switch + diff_changed events over existing connection.
        } catch (e) {
            showToast(String(e));
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
            closeCurrentAssistant();
            activeToolCalls.clear();
                    processedToolIds.clear();
            entries = [];
            currentAssistantId = null;
            streamTokenCount = 0;
            awaitingResponse = false;
            // Load messages for the new thread immediately
            await loadHistory(true);
            // Update URL with the new fork's thread ID
            updateThreadUrl(forked.id);
            // No need to reconnect WebSocket — broadcaster is per-task, not per-thread.
        } catch (e) {
            showToast(String(e));
        }
    }

    function scroll(force = false) {
        if (!scrollEl) return;
        if (!force && !atBottom) return;
        requestAnimationFrame(() => {
            if (!scrollEl) return;
            scrollEl.scrollTop = scrollEl.scrollHeight;
            // Second frame to correct for DOM shrink (e.g., streaming cursor disappearing)
            // that happens after the first frame sets scrollTop.
            requestAnimationFrame(() => {
                if (scrollEl) {
                    scrollEl.scrollTop = scrollEl.scrollHeight;
                }
            });
        });
    }

    // rAF-throttled scroll handler — at most once per frame.
    let scrollRafPending = false;
    function onScroll() {
        markUserInput();
        if (scrollRafPending) return;
        scrollRafPending = true;
        requestAnimationFrame(doCheckScroll);
    }
    function doCheckScroll() {
        scrollRafPending = false;
        if (!scrollEl) return;
        // Find the latest user message whose top has scrolled into/above the header zone.
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
        // Don't clear flag here — keep it active during inertial scrolling.
        // Reset a timeout instead; flag clears when scrolling settles.
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

    async function handleSend(msg: string, attachments?: Array<{ mimeType: string; data: string }>) {
        if ((!msg && (!attachments || attachments.length === 0)) || disabled) return;
        // If LLM is streaming, cancel the current response by clearing partial assistant entry.
        if (isLoading) {
            entries = entries.filter(e => !e.isStreaming);
            if (currentAssistantId) {
                currentAssistantId = null;
            }
            activeToolCalls.clear();
                    processedToolIds.clear();
            markNotStreaming();
        }
        // If task is in Done, move it back to In Progress before sending
        if (taskStatus === 'done') {
            try {
                const updated = await moveTask(projectId, taskId, 'in_progress');
                boardStore.update(b => {
                    if (!b) return b;
                    const existing = b.tasks.find(t => t.id === taskId);
                    const merged = (updated as any).queued ? { ...existing, ...updated } : updated;
                    return { ...b, tasks: b.tasks.map(t => t.id === taskId ? merged : t) };
                });
            } catch (e) {
                showToast(String(e));
                return;
            }
        }
        // Remove all error entries (cleared by next user action)
        entries = entries.filter(e => e.role !== 'error');
        // Add message optimistically with current thread context
        const entryId = genId();
        const isOfflineNow = getOfflineStatus();
        entries = [...entries, { id: entryId, role: 'user', content: msg, attachments: attachments || undefined, threadId: activeThreadId, isStreaming: false, createdAt: new Date().toISOString(), queued: isOfflineNow }];
        // Don't pre-create — let the first token/tool_call event create the assistant entry
        lastSentMessage = msg;
        // Send with model info for lazy-start
        awaitingResponse = true;
        try {
            await sendTaskMessage(projectId, taskId, msg, selectedModel, selectedUrl, attachments, selectedReasoningEffort || undefined);
        } catch (e) {
            // Send failed — show error, remove optimistic message
            const errorMsg = e instanceof Error ? e.message : 'Failed to send message';
            entries = entries.filter(e => !(e.role === 'user' && e.content === msg));
            entries = [...entries, { id: genId(), role: 'error', content: errorMsg, isStreaming: false, error: { code: 'send_failed', message: errorMsg }, createdAt: new Date().toISOString() }];
            awaitingResponse = false;
            return;
        }
        scroll();
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
            lastPromptTokens = (lastAssistant.stats.prompt_tokens || 0) + (lastAssistant.stats.predicted_tokens || 0);
            contextState.used = lastPromptTokens;
        }
        // Update task with new model/provider
        modelUpdatePending = true;
        try {
            await updateTask(projectId, taskId, {
                model_name: modelName,
                llama_server_url: providerUrl,
                reasoning_effort: reasoningEffort || undefined,
            });
        } catch (e) {
            console.error('Failed to update task model:', e);
        } finally {
            modelUpdatePending = false;
        }
    }

    async function handlePlanModeToggle(newPlanMode: boolean) {
        planMode = newPlanMode;
        try {
            await updateTask(projectId, taskId, { plan_mode: newPlanMode });
        } catch (e) {
            console.error('Failed to update plan mode:', e);
            planMode = !newPlanMode; // revert on error
        }
    }

    async function handleSummarize() {
        const confirmed = await showConfirm({
            title: 'Compress Context',
            message: 'This will summarize and remove older messages from the context window to reduce token usage. The summary will be inserted as a new message. Continue?',
            confirmText: 'Compress',
            cancelText: 'Cancel',
            variant: 'default',
        });
        if (!confirmed) return;
        try {
            const result = await summarizeTask(projectId, taskId);
            // Update context usage after summarization
            // The WebSocket 'summarization' event will add the checkpoint entry
            // and the next token event will refresh the usage count
            console.log('[summarize] context summarized:', result.old_count, '->', result.new_count, 'messages');
        } catch (e: any) {
            console.error('Failed to summarize:', e);
            showToast(e?.message || 'Failed to summarize context', 'error');
        }
    }

    async function handleRegenerate(entryId: string) {
        if (isLoading || disabled) return;
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        // Find the last user message before this assistant message
        const beforeEntries = entries.slice(0, idx);
        const lastUser = [...beforeEntries].reverse().find(e => e.role === 'user');
        if (!lastUser) return;
        // Regenerate from backend starting after the last user message
        try {
            await regenerateTaskMessages(projectId, taskId, lastUser.id, true);
        } catch (e) {
            console.error('Failed to regenerate messages:', e);
        }
        // Trim local entries: keep everything up to and including the last user message
        const userIdx = entries.findIndex(e => e.id === lastUser.id);
        entries = entries.slice(0, userIdx + 1);
        // Don't pre-create — let the first token/tool_call event create the assistant entry
        // Re-send the user message to trigger a new LLM response
        lastSentMessage = lastUser.content;
        isRegenerating = true;
        awaitingResponse = true;
        try {
            await sendTaskMessage(projectId, taskId, lastUser.content, selectedModel, selectedUrl, undefined, selectedReasoningEffort || undefined);
        } catch (e) {
            const errorMsg = e instanceof Error ? e.message : 'Failed to regenerate';
            showToast(errorMsg);
            isRegenerating = false;
            awaitingResponse = false;
            return;
        }
        scroll();
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
        const lastEntry = entries[entries.length - 1];
        if (lastEntry && lastEntry.role === 'assistant') {
            try {
                const updated = await startTask(projectId, taskId);
                boardStore.update(b => {
                    if (!b) return b;
                    const merged = (updated as any).queued ? { ...$boardStore?.tasks.find(t => t.id === taskId)!, ...updated } : updated;
                    return { ...b, tasks: b.tasks.map(t => t.id === taskId ? merged : t) };
                });
            } catch (e) {
                showToast(String(e), 'error');
            }
            return;
        }
        isStarting = true;
        // Prompt is already in entries (loaded from DB at task creation)
        // Don't pre-create assistant entry — let the first token/tool_call event create it
        lastSentMessage = taskPrompt;
        awaitingResponse = true;
        scroll();
        try {
            const updated = await startTask(projectId, taskId);
            boardStore.update(b => {
                if (!b) return b;
                const merged = (updated as any).queued ? { ...$boardStore?.tasks.find(t => t.id === taskId)!, ...updated } : updated;
                return { ...b, tasks: b.tasks.map(t => t.id === taskId ? merged : t) };
            });
        } catch (e) {
            showToast(String(e), 'error');
            isStarting = false;
            awaitingResponse = false;
            return;
        }
        isStarting = false;
    }

    async function handleStop() {
        markNotStreaming();
        awaitingResponse = false;
        // Save the streaming assistant ID before closeCurrentAssistant() clears it
        const streamingId = currentAssistantId;
        closeCurrentAssistant();
        // Remove the streaming assistant entry (incomplete response) and entries with
        // pending tool calls so they don't leak through loadHistory's merge.
        // loadHistory preserves non-DB entries, and streaming entries have random
        // genId() IDs that aren't in the DB.
        entries = entries.filter(e => e.id !== streamingId && !(e.toolCalls?.some(tc => tc.isPending)));
        // Clear active tool calls (immediate UI update)
        activeToolCalls.clear();
                    processedToolIds.clear();
        try {
            await stopTask(projectId, taskId);
        } catch (e) {
            console.error('Failed to stop task:', e);
        }
        // Reload history from DB to discard any unpersisted (streaming) entries
        // so they don't appear as duplicates when the task is restarted
        await loadHistory();
    }

    let taskDiffStatsStore = getTaskDiffStatsContext();
    let diffStats = $derived($taskDiffStatsStore[taskId] || null);
    let hasChanges = $derived(!!diffStats?.hash && (diffStats.files || 0) > 0);

    async function handleCommitAndDone() {
        if (isCommitting || disabled) return;
        isCommitting = true;
        try {
            let updated;
            if (hasChanges) {
                // Fetch commit message preview (dialog shows loading spinner)
                const confirmedMessage = await new Promise<string | null>((resolve) => {
                    // Open dialog in loading state
                    commitDialogStore.set({
                        loading: true,
                        message: '',
                        confirmText: 'Commit and move to Done',
                        cancelText: 'Cancel',
                        _resolve: resolve,
                    });
                    // Fetch preview in background
                    (async () => {
                        let commitMessage = '';
                        let authorName = '';
                        let authorEmail = '';
                        let mergeTarget = '';
                        try {
                            const preview = await getCommitPreview(projectId, taskId);
                            commitMessage = preview.commit_message;
                            authorName = preview.author_name || '';
                            authorEmail = preview.author_email || '';
                            mergeTarget = preview.merge_target || '';
                        } catch (e) {
                            commitMessage = taskTitle;
                        }
                        // Update dialog with actual message
                        commitDialogStore.update(d => d ? { ...d, loading: false, message: commitMessage, authorName, authorEmail, mergeTarget } : null);
                    })();
                });
                if (!confirmedMessage) {
                    return; // User cancelled
                }
                updated = await commitAndDone(projectId, taskId, confirmedMessage);
                // Clear stale diff stats immediately so the button updates right away
                taskDiffStatsStore.update(map => { delete map[taskId]; return { ...map }; });
            } else {
                updated = await moveTask(projectId, taskId, 'done');
            }
            boardStore.update(b => {
                if (!b) return b;
                const merged = (updated as any).queued ? { ...$boardStore?.tasks.find(t => t.id === taskId)!, ...updated } : updated;
                return { ...b, tasks: b.tasks.map(t => t.id === taskId ? merged : t) };
            });
            // Reload history to show any system messages from merge conflict resolution
            await loadHistory();
        } catch (e) {
            showToast(String(e));
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

        const confirmed = await showConfirm({
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
            entries = entries.filter((_, i) => i < idx);

            // Roll back worktree to the last checkpoint before the deleted messages
            const lastCheckpointHash = (() => {
                for (let i = entries.length - 1; i >= 0; i--) {
                    const hash = checkpointCommitHashes.get(entries[i].id);
                    if (hash) return hash;
                }
                return null;
            })();
            if (lastCheckpointHash) {
                try {
                    await resetWorktree(projectId, taskId, lastCheckpointHash);
                } catch {
                    // Best-effort: worktree may not exist if task is done
                }
            }

            // Refresh threads list and sync active thread after deletion
            threads = await getTaskThreads(projectId, taskId);
            const active = threads.find(t => t.is_active);
            if (active) {
                activeThreadId = active.id;
                // Update URL to reflect the new active thread
                updateThreadUrl(active.id);
            }
        } catch (e) {
            showToast(String(e));
        }
    }

    async function handleEditFork(entryId: string, _originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) {
        const entry = entries.find(e => e.id === entryId);
        if (!entry) return;
        // Soft edit for backlog tasks: update message + task.prompt without triggering the LLM
        if (taskStatus === 'backlog') {
            const idx = entries.findIndex(e => e.id === entryId);
            if (idx !== -1) { entries[idx].content = newContent; entries[idx].createdAt = new Date().toISOString(); }
            updateTaskMessage(projectId, taskId, entryId, newContent).catch(e => showToast(String(e)));
            updateTask(projectId, taskId, { prompt: newContent }).catch(e => showToast(String(e)));
            return;
        }
        if (isLoading || disabled) return;
        try {
            const fromThreadId = entry.threadId || activeThreadId;
            const forked = await forkTaskThread(projectId, taskId, fromThreadId, entryId, newContent);
            threads = await getTaskThreads(projectId, taskId);
            activeThreadId = forked.id;
            // Clear streaming state before loading new thread's messages.
            closeCurrentAssistant();
            activeToolCalls.clear();
                    processedToolIds.clear();
            entries = [];
            currentAssistantId = null;
            streamTokenCount = 0;
            // Load messages for the new thread
            await loadHistory(true);
            updateThreadUrl(forked.id);
            onThreadSwitch?.();
            lastSentMessage = newContent;
            awaitingResponse = true;
            await sendTaskMessage(projectId, taskId, newContent, selectedModel, selectedUrl, attachments, selectedReasoningEffort || undefined);
        } catch (e) {
            showToast(String(e));
        }
    }

    async function handleEditSend(entryId: string, _originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) {
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;

        // Backlog: persist edit only (soft edit, no LLM trigger)
        if (taskStatus === 'backlog') {
            entries = entries.map((e, i) => i === idx ? { ...e, content: newContent, createdAt: new Date().toISOString() } : e);
            updateTaskMessage(projectId, taskId, entryId, newContent).catch(e => showToast(String(e)));
            updateTask(projectId, taskId, { prompt: newContent }).catch(e => showToast(String(e)));
            return;
        }

        if (isLoading || disabled) return;
        entries = entries.slice(0, idx);
        closeCurrentAssistant();
        try {
            await deleteTaskMessagesAfter(projectId, taskId, entryId);
            threads = await getTaskThreads(projectId, taskId);
            const active = threads.find(t => t.is_active);
            if (active) {
                activeThreadId = active.id;
                updateThreadUrl(active.id);
            }
        } catch (e) {
            showToast(String(e));
            return;
        }
        const newMsg = await createTaskMessage(projectId, taskId, 'user', newContent);
        entries = [...entries, { id: newMsg.id, role: 'user', content: newContent, attachments, threadId: activeThreadId, isStreaming: false, createdAt: newMsg.created_at || new Date().toISOString() }];
        lastSentMessage = newContent;
        awaitingResponse = true;
        await sendTaskMessage(projectId, taskId, newContent, selectedModel, selectedUrl, attachments, selectedReasoningEffort || undefined);
        scroll();
    }

    function connect(skipSync = false) {
        if (connected) return;
        connecting = true;
        connected = true;
        connecting = false;
        if (!firstConnectDone) {
            firstConnectDone = true;
            entries = entries.filter(e => !(e.role === 'error' && e.error?.code === 'ws_disconnected'));
        }
    }

    /** Dismiss a single error message without affecting other messages. */
    async function handleDismissError(messageId: string) {
        try {
            await deleteTaskMessage(projectId, taskId, messageId);
            entries = entries.filter(e => e.id !== messageId);
        } catch (e) {
            console.error('Failed to dismiss error:', e);
        }
    }

    /** Force immediate reconnection. */
    function handleRetryConnect() {
        connected = false;
        // Remove existing connection lost errors
        entries = entries.filter(e => !(e.role === 'error' && e.error?.code === 'ws_disconnected'));
        connect();
    }

    let destroyed = $state(false);

    // Mobile keyboard: track visualViewport shrink and add padding to keep input visible
    let keyboardOffset = $state(0);
    let chatFormRef: HTMLElement | null = $state(null);
    let chatFormComp: { focus: () => void } | null = null;

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
            try {
                const switched = await switchTaskThread(projectId, taskId, urlThread);
                updateActiveThread(switched.id);
                await loadHistory();
                // No reconnect needed — broadcaster is per-task
            } catch (e) {
                showToast(String(e));
            }
        }
    }

    onMount(() => {
        // Run async init in a promise chain; cleanup is returned synchronously.
        let unsubTaskEvent: (() => void) | null = null;
        let unsubConfig: (() => void) | null = null;
        let unsubViewport: (() => void) | null | undefined = null;
        const handleUrlChange = () => syncThreadFromUrl();
        window.addEventListener('urlchanged', handleUrlChange);
        unsubConfig = configStore.subscribe(() => updateProvidersFromStore());
        unsubViewport = setupViewportListener();
        const handleKeydown = (e: KeyboardEvent) => {
            if (['ArrowUp','ArrowDown','PageUp','PageDown','Home','End'].includes(e.key)) {
                markUserInput();
            }
        };
        window.addEventListener('keydown', handleKeydown);

        (async () => {
            // Providers loaded via configStore subscription (no separate fetch needed)
            updateProvidersFromStore();
            await loadThreads();
            // If URL has a thread param, switch to that thread before loading history
            const urlThread = readThreadFromUrl();
            if (urlThread && threads.some(t => t.id === urlThread)) {
                try {
                    const switched = await switchTaskThread(projectId, taskId, urlThread);
                    updateActiveThread(switched.id);
                } catch (e) {
                    showToast(String(e));
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
                            showToast(String(e));
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
            const stream = getAppStreamRef();
            if (stream) {
                stream.subscribeTask(projectId, taskId);
            }

            // 4. Load history from DB (merges with buffer entries via ID-based dedup).
            await loadHistory();

            // 5. Allow live events — buffer replay is done, DB state is loaded.
            suppressEvents = false;

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
            } catch {
                // Task may not exist yet
            }
            connect();
            // Scroll to bottom after history is loaded and DOM has rendered.
            // scroll() uses requestAnimationFrame internally so the DOM update is ready.
            scroll();
            // If task was started externally (e.g., from board) and is running with no
            // assistant response yet, show the "awaiting LLM" spinner until first token/tool_call.
            // Only for in_progress — review tasks have no active executor.
            if (taskStatus === 'in_progress') {
                const lastEntry = entries[entries.length - 1];
                if (lastEntry?.role === 'user') {
                    awaitingResponse = true;
                }
            }
        })();

        return () => {
            window.removeEventListener('urlchanged', handleUrlChange);
            unsubConfig?.();
            unsubViewport?.();
            window.removeEventListener('keydown', handleKeydown);
            unsubTaskEvent?.();
        };
    });

    onDestroy(() => {
        destroyed = true;
        if (inputTimeout) clearTimeout(inputTimeout);
        unsubQueue?.();
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
            // When queue changes, check if any queued messages have been flushed
            // Clear queued flag for messages that are no longer in the queue
            for (const entry of entries) {
                if (entry.queued) {
                    entry.queued = false;
                }
            }
        });
    });
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
        {isAgentSelected}
        {showHeader}
        {stickyUserMessage}
        {taskStatus}
        {projectId}
        {taskId}
        onScroll={onScroll}
        onWheel={markUserInput}
        onTouchStart={markUserInput}
        onDismissError={handleDismissError}
        onRetryConnect={handleRetryConnect}
        onThreadSwitch={handleThreadSwitch}
        onMsgDelete={handleMsgDelete}
        onMsgFork={handleMsgFork}
        onMsgSend={handleMsgSend}
        onMsgEdit={handleMsgEdit}
        onRegenerate={handleRegenerate}
        onNavigateTask={handleMsgNavigateTask}
        {onViewChanges}
        {scrollEl}
        onScrollElChange={(el) => scrollEl = el}
    />


    {#if showStartButton}
        <StartPanel
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            {planMode}
            {isStarting}
            {disabled}
            {isAgentSelected}
            onModelSelect={handleModelSelect}
            onPlanModeToggle={handlePlanModeToggle}
            onStartTask={handleStartTask}
        />
    {:else}
        <div class="chat-form-wrapper" style="padding-bottom: {keyboardOffset}px;" bind:this={chatFormRef}>
        <ReviewBar
            {taskStatus}
            {showResumeButton}
            {showRestartButton}
            {isStarting}
            {isCommitting}
            {disabled}
            {hasChanges}
            {queueStatus}
            {providers}
            {selectedModel}
            {selectedUrl}
            {selectedReasoningEffort}
            {onMoveTask}
            onStartTask={handleStartTask}
            onCommitAndDone={handleCommitAndDone}
            onModelSelect={handleModelSelect}
        />
        {#if !showRestartButton}
        <ChatForm
            bind:this={chatFormComp}
            bind:value={inputValue}
            bind:attachments={draftAttachments}
            disabled={disabled}
            isLoading={isLoading}
            placeholder="Send a message to the agent..."
            providers={providers}
            selectedModel={selectedModel}
            selectedUrl={selectedUrl}
            selectedReasoningEffort={selectedReasoningEffort}
            contextUsed={contextState.used}
            contextLimit={contextState.limit}
            planMode={planMode}
            projectId={projectId}
            taskId={taskId}
            onInput={handleInputSave}
            onClearDraft={handleClearDraft}
            onSubmit={handleSend}
            onStop={handleStop}
            onModelSelect={handleModelSelect}
            onPlanModeToggle={handlePlanModeToggle}
            onSummarize={handleSummarize}
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
