<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { get } from 'svelte/store';
    import type { StreamEvent, Provider, Thread } from '$lib/types';
    import { sendTaskMessage, getTaskMessages, createTaskMessage, deleteTaskMessagesAfter, deleteTaskMessage, regenerateTaskMessages, updateTaskMessage, stopTask, startTask, updateTask, moveTask, getTaskThreads, switchTaskThread, forkTaskThread, checkMessageDelete, commitAndDone, resetWorktree, getOfflineStatus, onQueueChanged } from '$lib/api';
import { board as boardStore, config as configStore, showToast, streamingTaskIds, showConfirm, commitQueueStatus, selectedTask, taskEvent, taskDiffStats as taskDiffStatsStore } from '$lib/store';
    import { getAppStreamRef } from '$lib/stream-ref';
    import { formatToolArgs } from '$lib/utils/toolArgs';
    import ChatForm from './ChatForm.svelte';
    import ChatMessage from './ChatMessage.svelte';
    import ModelSelector from './ModelSelector.svelte';
    import CheckpointDivider from './CheckpointDivider.svelte';

    interface ChatEntry {
        id: string;
        role: string;
        content: string;
        reasoningContent?: string;
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

    type DiffData = { diff: string; untracked: Array<{ path: string; diff: string; file_size?: number; truncated?: boolean; orig_added?: number; orig_removed?: number }>; truncated_files?: string[]; original_file_stats?: Record<string, { added: number; removed: number }> };

    let { projectId, taskId, taskPrompt = '', taskStatus = 'backlog', disabled = false, taskTitle = '', onRefreshDiff, onMoveTask, onThreadSwitch, onViewChanges }: { projectId: string; taskId: string; taskPrompt?: string; taskStatus?: string; disabled?: boolean; taskTitle?: string; onRefreshDiff?: (data: DiffData) => void; onMoveTask?: (status: string) => void; onThreadSwitch?: () => void; onViewChanges?: (commitHash: string) => void } = $props();

    let entries: ChatEntry[] = $state([]);
    let connected = $state(false);
    let connecting = $state(true);
    let scrollEl: HTMLElement | null = null;

    let atBottom = $state(true);
    let prevScrollHeight = 0;
    let userInputPending = false;
    let showHeader = $state(false);
    let inputTimeout: ReturnType<typeof setTimeout> | null = null;
    let inputValue = $state('');

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');
    let planMode = $state(false);

    let currentAssistantId: string | null = $state(null);
    let activeToolCalls = $state<Map<string, { entryId: string }>>(new Map());

    let lastSentMessage: string | null = $state(null);
    let isStarting = $state(false);
    let isRegenerating = $state(false); // true between regenerate delete and echo arrival
    let firstConnectDone = $state(false); // track if initial onopen has fired
    let isCommitting = $state(false);
    let queueStatus = $derived(commitQueueStatus[projectId]?.taskID === taskId ? commitQueueStatus[projectId].status : null);

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
    let isLoading = $derived($streamingTaskIds.has(taskId));

    // Last entry index for regenerate button gating
    let lastEntryIdx = $derived(entries.length - 1);

    // awaitingResponse: true between sending a message and receiving the first token/tool_call.
    // Shows a spinner so the user knows the LLM is being contacted.
    let awaitingResponse = $state(false);

    // Queue state — set when a task is waiting for a concurrency gate slot.
    let queuePosition = $state(0);
    let queueEstimatedWait = $state(0);

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

    function genId(): string {
        return Math.random().toString(36).slice(2, 10);
    }

    // Watch for task config changes from the board store (e.g., inline edit)
    // Updates model selection when the task's model/provider is changed externally
    $effect(() => {
        const boardTask = $boardStore?.tasks.find(t => t.id === taskId);
        if (boardTask) {
            if (boardTask.llama_server_url) selectedUrl = boardTask.llama_server_url;
            if (boardTask.model_name) selectedModel = boardTask.model_name;
            if (boardTask.reasoning_effort) selectedReasoningEffort = boardTask.reasoning_effort;
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
        streamingTaskIds.update(s => { const n = new Set(s); n.add(taskId); return n; });
    }

    /** Mark this task as no longer streaming. */
    function markNotStreaming() {
        streamingTaskIds.update(s => { const n = new Set(s); n.delete(taskId); return n; });
        queuePosition = 0;
        queueEstimatedWait = 0;
        streamTokenCount = 0;
        contextState.used = lastPromptTokens;
    }

    /** Clear the "awaiting LLM" spinner — first response from provider has arrived. */
    function clearAwaiting() {
        if (awaitingResponse) awaitingResponse = false;
    }

    function handleEvent(event: StreamEvent) {
        switch (event.type) {
            case 'token': {
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
                clearAwaiting();
                const data = event.data as { tool_id: string; tool_name: string; args: Record<string, unknown> };
                // Skip incomplete tool call deltas (empty tool_id from streaming fragments)
                if (!data.tool_id || data.tool_id === '') {
                    break;
                }
                // Attach tool call to the current assistant entry
                const entry = ensureAssistantEntry();
                // If the LLM sent the tool name as text content before the tool call,
                // clear it in-place to avoid a blank bubble
                if (entry.content.trim() === data.tool_name && !entry.reasoningContent) {
                    entry.content = '';
                }
                if (!entry.toolCalls) entry.toolCalls = [];
                // Dedup: if a tool_call with this tool_id already exists (from buffer replay
                // of an earlier streaming delta), update it in-place instead of pushing a duplicate.
                const existing = entry.toolCalls.find(tc => tc.toolId === data.tool_id);
                if (existing) {
                    existing.toolName = data.tool_name;
                    existing.displayName = data.tool_display_name;
                    existing.args = data.args || {};
                    existing.raw_args = data.raw_args;
                    existing.sandbox = data.sandbox;
                    existing.path = data.path;
                } else {
                    entry.toolCalls.push({
                        toolId: data.tool_id,
                        toolName: data.tool_name,
                        displayName: data.tool_display_name,
                        args: data.args || {},
                        raw_args: data.raw_args,
                        sandbox: data.sandbox,
                        path: data.path,
                        isPending: true
                    });
                }
                entry.isStreaming = true;
                activeToolCalls.set(data.tool_id, { entryId: entry.id });
                scroll();
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number; args?: Record<string, unknown>; raw_args?: string; sandbox?: boolean; path?: string };
                const ref = activeToolCalls.get(data.tool_id);
                activeToolCalls.delete(data.tool_id);
                if (ref) {
                    const entry = entries.find(e => e.id === ref.entryId);
                    if (entry && entry.toolCalls) {
                        const tc = entry.toolCalls.find(t => t.toolId === data.tool_id);
                        if (tc) {
                            tc.result = data.output;
                            tc.success = data.success;
                            tc.duration = data.duration;
                            tc.isPending = false;
                            // Update with final args (streaming event had partial args)
                            if (data.args) tc.args = data.args;
                            if (data.raw_args) tc.raw_args = data.raw_args;
                            if (data.sandbox !== undefined) tc.sandbox = data.sandbox;
                            if (data.path) tc.path = data.path;
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
                // Store prompt tokens for next turn's context baseline
                if (data.prompt_tokens !== undefined) {
                    lastPromptTokens = data.prompt_tokens;
                    contextState.used = data.prompt_tokens;
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
                    // Remove assistant entries with only pending tool calls (no text content)
                    entries = entries.filter(e => !(e.toolCalls && e.toolCalls.some(tc => tc.isPending) && !e.content));
                    // Mark task as streaming so badge updates on board view
                    markStreaming();
                } else if (data.state === 'cancelled') {
                    // Response was cancelled by new user message — clear streaming state.
                    closeCurrentAssistant();
                    activeToolCalls.clear();
                    entries = entries.filter(e => !e.isStreaming);
                    markNotStreaming();
                } else if (['stopped', 'failed', 'idle', 'completed', 'awaiting_review'].includes(data.state)) {
                    // Executor finished — clear streaming state
                    markNotStreaming();
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
            case 'stats': {
                const data = event.data as { model_name: string; predicted_tokens: number; predicted_ms: number };
                // Sync frontend counter with backend count for accuracy
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
                            prompt_tokens: entry.stats?.prompt_tokens ?? 0,
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
                    loadHistory();
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
            case 'compression': {
                const data = event.data as { summary: string };
                // Add a compression checkpoint entry
                entries = [...entries, {
                    id: genId(),
                    role: 'compression',
                    content: data.summary || 'Context was summarized to free space',
                    isStreaming: false,
                    createdAt: new Date().toISOString(),
                }];
                break;
            }
            case 'merge_resolution': {
                const data = event.data as { id: string; conflicted_files: string[]; summary: string; content: string; created_at: string };
                entries = [...entries, {
                    id: data.id || genId(),
                    role: 'merge_resolution',
                    content: data.content || `Merge conflicts resolved in ${data.conflicted_files?.length || 0} file(s)`,
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
                            if (entries[i].role === 'assistant' && entries[i].toolCalls && entries[i].toolCalls.length > 0) {
                                entryId = entries[i].id;
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
                    return {
                        ...base, id: m.id, role: 'error' as const, content: message, isStreaming: false, createdAt: m.created_at,
                        error: { code, message },
                    };
                }
                // Restore tool call entries from persisted data — merged into preceding assistant entry
                if (m.role === 'tool_call' && m.tool_calls) {
                    const tc = JSON.parse(m.tool_calls);
                    const result = m.tool_result ? JSON.parse(m.tool_result) : null;
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
                            success: result?.success ?? true,
                            duration: result?.duration,
                            isPending: false,
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
                return {
                    ...base, id: m.id, role: m.role, content: m.content, reasoningContent: m.reasoning_content, attachments: parsedAttachments, isStreaming: false, createdAt: m.created_at,
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
            // Merge toolCalls entries into the preceding assistant entry
            const mergedDbEntries = mergeToolCallsIntoAssistant(dbEntries);
            // Merge with existing entries (e.g., from buffer replay).
            // DB entries have backend UUIDs; buffer entries have random genId() IDs.
            // Preserve buffer entries whose IDs are NOT in the DB (not yet persisted).
            const dbIds = new Set(messages.map(m => m.id));
            const existingNonDb = entries.filter(e => !dbIds.has(e.id));
            entries = [...mergedDbEntries, ...existingNonDb];
            // Restore context usage from the last assistant message
            const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && e.stats);
            if (lastAssistant?.stats?.prompt_tokens) {
                lastPromptTokens = lastAssistant.stats.prompt_tokens;
                contextState.used = lastPromptTokens;
            }
            // After replacing entries, the DOM re-renders and scroll state may be lost.
            // Scroll to bottom on the next frame.
            if (scrollAfter) {
                await new Promise(resolve => requestAnimationFrame(resolve));
                scroll();
            }
        } catch { /* No history yet */ }
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

    function checkScroll() {
        if (!scrollEl) return;
        // Update sticky header visibility on every scroll
        showHeader = scrollEl.scrollTop > 60;
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
            await sendTaskMessage(projectId, taskId, msg, selectedModel, selectedUrl, attachments);
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
        // Update task with new model/provider
        try {
            await updateTask(projectId, taskId, {
                model_name: modelName,
                llama_server_url: providerUrl,
                reasoning_effort: reasoningEffort || null,
            });
        } catch (e) {
            console.error('Failed to update task model:', e);
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
            await sendTaskMessage(projectId, taskId, lastUser.content, selectedModel, selectedUrl);
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
        closeCurrentAssistant();
        try {
            await stopTask(projectId, taskId);
        } catch (e) {
            console.error('Failed to stop task:', e);
        }
        // Reload history from DB to discard any unpersisted (streaming) entries
        // so they don't appear as duplicates when the task is restarted
        await loadHistory();
    }

    let diffStats = $derived($taskDiffStatsStore[taskId] || null);
    let hasChanges = $derived(!!diffStats?.hash);

    async function handleCommitAndDone() {
        if (isCommitting || disabled) return;
        isCommitting = true;
        try {
            let updated;
            if (hasChanges) {
                updated = await commitAndDone(projectId, taskId);
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
            await loadHistory();
            updateThreadUrl(forked.id);
            onThreadSwitch?.();
            lastSentMessage = newContent;
            awaitingResponse = true;
            await sendTaskMessage(projectId, taskId, newContent, selectedModel, selectedUrl, attachments);
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
        await sendTaskMessage(projectId, taskId, newContent, selectedModel, selectedUrl, attachments);
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
        function onResize() {
            const diff = Math.max(0, window.innerHeight - vv.height);
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

    onMount(async () => {
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
        const unsubTaskEvent = taskEvent.subscribe((event) => {
            if (event && event.task_id === taskId) {
                handleEvent({ type: event.type, data: event.data });
            }
        });

        // 2. Subscribe to this task directly (triggers buffer replay from server).
        // Handler is already set up above, so buffer events won't be missed.
        // App's $effect also subscribes, but the buffer is NOT cleared on re-subscribe.
        const stream = getAppStreamRef();
        if (stream) {
            stream.subscribeTask(projectId, taskId);
        }

        // 3. If task is actively streaming, pause for buffer replay to arrive.
        // Buffer events travel: server WS → onTaskEvent → taskEvent.set → handleEvent.
        // For idle/completed tasks, no buffer exists so no delay needed.
        if (taskStatus === 'in_progress' || taskStatus === 'review') {
            await new Promise(r => setTimeout(r, 200));
        }

        // 4. Load history from DB (merges with buffer entries via ID-based dedup).
        await loadHistory();

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
        if (taskStatus === 'in_progress' || taskStatus === 'review') {
            const lastEntry = entries[entries.length - 1];
            if (lastEntry?.role === 'user') {
                awaitingResponse = true;
            }
        }
        // Listen for URL changes (e.g., browser Back/Forward)
        const handleUrlChange = () => syncThreadFromUrl();
        window.addEventListener('urlchanged', handleUrlChange);
        // Subscribe to config store to react to settings changes
        const unsubConfig = configStore.subscribe(() => updateProvidersFromStore());
        // Track visualViewport for mobile keyboard awareness
        const unsubViewport = setupViewportListener();
        // Mark keyboard navigation as user input (arrow keys, page up/down, home/end)
        const handleKeydown = (e: KeyboardEvent) => {
            if (['ArrowUp','ArrowDown','PageUp','PageDown','Home','End'].includes(e.key)) {
                markUserInput();
            }
        };
        window.addEventListener('keydown', handleKeydown);
        return () => {
            window.removeEventListener('urlchanged', handleUrlChange);
            unsubConfig();
            unsubViewport?.();
            window.removeEventListener('keydown', handleKeydown);
            unsubTaskEvent();
        };
    });

    onDestroy(() => {
        destroyed = true;
        if (inputTimeout) clearTimeout(inputTimeout);
        unsubQueue?.();
    });

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
    <div class="chat-messages-wrapper">
        {#if taskTitle && entries.length > 0}
            <div class="chat-task-header" class:visible={showHeader} role="status" aria-live="polite">
                <span class="chat-task-title">{taskTitle}</span>
            </div>
        {/if}
        <div class="chat-messages" bind:this={scrollEl} onscroll={checkScroll} onwheel={markUserInput} ontouchstart={markUserInput} role="region" aria-label="Chat messages" tabindex="0">
        {#if entries.length === 0}
            <div class="chat-empty">
                <svg width="32" height="32" viewBox="0 0 16 16" fill="none">
                    <path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1" stroke-linejoin="round" opacity="0.3"/>
                </svg>
                <p>No messages yet</p>
                <span class="hint">Send a message to start the conversation</span>
            </div>
        {:else}
            {#each entries as entry, entryIdx (entry.id)}
                {#if entry.role === 'user'}
                    {@const _hv = checkpointHashesVersion}
                    {@const cpState = getCheckpointEntryCheckpointState(entryIdx)}
                    {@const cpThread = threads.length > 0 ? getCheckpointThreads(entry, entryIdx === 0) : null}
                    {#if cpThread || cpState}
                        <CheckpointDivider
                            timestamp={entry.createdAt}
                            threadId={entry.threadId || activeThreadId}
                            threads={threads}
                            activeThreadId={cpThread?.id || ''}
                            onSwitch={handleThreadSwitch}
                            isPending={cpState?.isPending || false}
                            commitLabel={cpState?.commitLabel || undefined}
                            commitHash={cpState?.commitHash || cpThread?.checkpoint_commit_hash || undefined}
                            onViewChanges={onViewChanges}
                        />
                    {/if}
                {/if}
                {#if entry.role === 'error'}
                    <div class="error-entry">
                        <div class="error-card">
                            <div class="error-header">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
                                    <line x1="12" y1="9" x2="12" y2="13"/>
                                    <line x1="12" y1="17" x2="12.01" y2="17"/>
                                </svg>
                                <span class="error-code">{entry.error?.code || 'error'}</span>
                                {#if entry.error?.code === 'ws_disconnected'}
                                    <span class="error-reconnecting"><span class="spinner"></span> Reconnecting...</span>
                                {/if}
                                <button class="btn-dismiss" onclick={() => handleDismissError(entry.id)} title="Dismiss error" aria-label="Dismiss error">
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <line x1="18" y1="6" x2="6" y2="18"/>
                                        <line x1="6" y1="6" x2="18" y2="18"/>
                                    </svg>
                                </button>
                            </div>
                            <div class="error-message">{entry.content}</div>
                            {#if entry.error?.code === 'ws_disconnected'}
                                <button class="btn-retry" onclick={handleRetryConnect} title="Retry connection">
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <polyline points="23 4 23 10 17 10"/>
                                        <path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>
                                    </svg>
                                    Retry now
                                </button>
                            {/if}
                        </div>
                    </div>
                {:else if entry.role === 'compression'}
                    <div class="compression-entry">
                        <div class="compression-divider">
                            <div class="divider-line"></div>
                            <div class="divider-content">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/>
                                </svg>
                                <span class="compression-label">Context compressed</span>
                            </div>
                            <div class="divider-line"></div>
                        </div>
                        <div class="compression-summary">{entry.content}</div>
                    </div>
                {:else if entry.role === 'merge_resolution'}
                    <div class="merge-resolution-entry">
                        <div class="merge-resolution-card">
                            <div class="merge-resolution-header">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <circle cx="18" cy="18" r="3"/>
                                    <circle cx="6" cy="6" r="3"/>
                                    <path d="M13 6h3a2 2 0 0 1 2 2v7"/>
                                    <line x1="6" y1="9" x2="6" y2="21"/>
                                </svg>
                                <span class="merge-resolution-label">Merge conflicts resolved</span>
                            </div>
                            <div class="merge-resolution-content">{entry.content}</div>
                        </div>
                    </div>
                {:else}
                    <ChatMessage role={entry.role} content={entry.content} reasoningContent={entry.reasoningContent} isStreaming={entry.isStreaming || false} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls} formatToolArgs={formatToolArgs} onDelete={entryIdx === 0 ? undefined : () => { const idx = entries.findIndex(e => e.id === entry.id); handleDeleteRequest(entry.id, entries.length - idx); }} onFork={(msgId, origContent, newContent, atts) => handleEditFork(msgId, origContent, newContent, atts)} onSend={(msgId, origContent, newContent, atts) => handleEditSend(msgId, origContent, newContent, atts)} isLastUserMessage={lastUserMessageIds.has(entry.id) && !isLoading} isFirstUserMessage={entryIdx === 0} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => handleRegenerate(entry.id) : undefined} onNavigateTask={(taskId) => { const task = $boardStore?.tasks.find(t => t.id === taskId); if (task) { const url = new URL(location.href); url.searchParams.set('task', taskId); history.pushState(null, '', url); selectedTask.set(task); } }} queued={entry.queued || false} />
                {/if}
            {/each}
        {/if}
        {#if awaitingResponse && connected && isLoading}
            <div class="awaiting-llm">
                <span class="spinner"></span>
                <span>{queuePosition > 0 ? `Awaiting LLM provider — position ${queuePosition} in queue, ~${queueEstimatedWait}s` : 'Awaiting LLM provider...'}</span>
            </div>
        {/if}
        </div>
    </div>


    {#if showStartButton}
        <div class="chat-start">
            <div class="start-model-row">
                {#if providers.length > 0}
                    <ModelSelector
                        providers={providers}
                        currentModel={selectedModel}
                        currentUrl={selectedUrl}
                        currentReasoningEffort={selectedReasoningEffort}
                        onSelect={handleModelSelect}
                    />
                {/if}
                <button
                    type="button"
                    class="btn-plan-toggle"
                    class:active={planMode}
                    onclick={() => handlePlanModeToggle(!planMode)}
                    title={planMode ? 'Plan mode: AI proposes subtasks. Click to switch to act mode.' : 'Act mode: AI makes changes directly. Click to switch to plan mode.'}
                    aria-label="Toggle plan mode"
                >
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        {#if planMode}
                            <path d="M2 4h12M2 8h12M2 12h12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        {:else}
                            <path d="M3 8l4 4L13 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        {/if}
                    </svg>
                    <span>{planMode ? 'Plan' : 'Act'}</span>
                </button>
            </div>
            <button class="btn-start-task" onclick={handleStartTask} disabled={isStarting || disabled || (providers.length > 0 && !selectedModel)} title="Start the AI task">
                {#if isStarting}
                    <span class="spinner-inline"></span> Starting...
                {:else}
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                        <path d="M4 2l10 6-10 6V2z" fill="currentColor"/>
                    </svg>
                    Start Task
                {/if}
            </button>
        </div>
    {:else}
        <div class="chat-form-wrapper" style="padding-bottom: {keyboardOffset}px;" bind:this={chatFormRef}>
        {#if taskStatus === 'done' && onMoveTask}
            <div class="chat-review-bar">
                <button class="btn-move" onclick={() => onMoveTask('review')} title="Move task to Review">
                    → Review
                </button>
                <button class="btn-move" onclick={() => onMoveTask('backlog')} title="Move task to Backlog">
                    → Backlog
                </button>
            </div>
        {/if}
        {#if taskStatus === 'review'}
            <div class="chat-review-bar">
                {#if onMoveTask}
                    <div class="move-buttons">
                        <button class="btn-move" onclick={() => onMoveTask('done')} title="Move task to Done">
                            → Done
                        </button>
                        <button class="btn-move" onclick={() => onMoveTask('backlog')} title="Move task to Backlog">
                            → Backlog
                        </button>
                    </div>
                {/if}
                <button class="btn-commit-done" onclick={handleCommitAndDone} disabled={isCommitting || disabled} title={hasChanges ? 'Commit changes and move task to Done' : 'Move task to Done'}>
                    {#if queueStatus === 'queued'}
                        <span class="spinner-inline"></span> In queue...
                    {:else if queueStatus === 'processing' || isCommitting}
                        <span class="spinner-inline"></span> {hasChanges ? 'Committing...' : 'Moving...'}
                    {:else}
                        {#if hasChanges}
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8l3 3 5-6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                            Commit and move to Done
                        {:else}
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8l3 3 5-6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                            Move to Done
                        {/if}
                    {/if}
                </button>
            </div>
        {/if}
        {#if showRestartButton}
            <div class="chat-review-bar">
                {#if providers.length > 0}
                    <div class="restart-model-row">
                        <ModelSelector
                            providers={providers}
                            currentModel={selectedModel}
                            currentUrl={selectedUrl}
                            currentReasoningEffort={selectedReasoningEffort}
                            onSelect={handleModelSelect}
                        />
                    </div>
                {/if}
                <button class="btn-restart-task" onclick={handleStartTask} disabled={isStarting || disabled || (providers.length > 0 && !selectedModel)} title="Restart the task from the last user message">
                    {#if isStarting}
                        <span class="spinner-inline"></span> Restarting...
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                            <path d="M12 1v3h-3M4 15v-3h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        Restart Task
                    {/if}
                </button>
            </div>
        {/if}
        {#if !showRestartButton}
        <ChatForm
            bind:this={chatFormComp}
            bind:value={inputValue}
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
            onSubmit={(msg, atts) => handleSend(msg, atts)}
            onStop={handleStop}
            onModelSelect={handleModelSelect}
            onPlanModeToggle={handlePlanModeToggle}
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
    .chat-messages-wrapper {
        flex: 1;
        position: relative;
        display: flex;
        flex-direction: column;
        min-height: 0;
    }
    .chat-messages {
        flex: 1;
        overflow-y: auto;
        padding: 8px 12px;
        min-height: 0;
    }
    .chat-task-header {
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
        z-index: 5;
        display: flex;
        align-items: center;
        padding: 6px 12px;
        min-height: 2.375rem;
        background: var(--color-surface-1);
        border-bottom: 1px solid var(--color-border);
        opacity: 0;
        pointer-events: none;
        transition: opacity 0.2s ease;
    }
    .chat-task-header.visible {
        opacity: 1;
    }
    .chat-task-title {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-secondary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .chat-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 48px 16px;
        color: var(--color-text-tertiary);
        text-align: center;
        height: 100%;
    }
    .chat-empty p { font-size: 0.875rem; margin: 0; font-weight: 500; }
    .chat-empty .hint { font-size: 0.75rem; }
    @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
    .spinner {
        display: inline-block;
        width: 10px;
        height: 10px;
        border: 1.5px solid var(--color-status-yellow);
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
    .awaiting-llm {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
    }

    /* Error entry styles */
    .error-entry { padding: 6px 0; }
    .error-card {
        background: color-mix(in srgb, var(--color-status-red) 6%, transparent);
        border: 1px solid color-mix(in srgb, var(--color-status-red) 30%, transparent);
        border-left: 3px solid var(--color-status-red);
        border-radius: var(--radius-md);
        padding: 10px 14px;
    }
    .error-header {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-bottom: 4px;
        color: var(--color-status-red);
    }
    .error-header svg { flex-shrink: 0; }
    .btn-dismiss {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 18px;
        height: 18px;
        padding: 0;
        margin-left: auto;
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        transition: all 0.15s ease;
    }
    .btn-dismiss:hover {
        background: color-mix(in srgb, var(--color-status-red) 15%, transparent);
        color: var(--color-status-red);
    }
    .error-code {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .error-reconnecting {
        font-size: 0.75rem;
        color: var(--color-status-yellow);
        margin-left: 4px;
        display: inline-flex;
        align-items: center;
        gap: 4px;
    }
    .error-message {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
        word-break: break-word;
    }
    .btn-retry {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        margin-top: 8px;
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 500;
        background: var(--color-status-red);
        color: white;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-retry:hover { opacity: 0.85; }


    .chat-start {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 10px;
        padding: 16px 12px;
        border-top: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .start-model-row {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .btn-plan-toggle {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        font-weight: 500;
        cursor: pointer;
        transition: all 0.15s ease;
        flex-shrink: 0;
    }
    .btn-plan-toggle:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .btn-plan-toggle.active {
        background: var(--color-accent)22;
        border-color: var(--color-accent);
        color: var(--color-accent);
    }
    .restart-model-row {
        display: flex;
        align-items: center;
        margin-right: auto;
    }
    .btn-start-task {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        padding: 10px 24px;
        border-radius: var(--radius-md);
        font-size: 0.875rem;
        font-weight: 600;
        background: var(--color-accent);
        color: white;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-start-task:hover:not(:disabled) {
        opacity: 0.85;
        transform: translateY(-1px);
    }
    .btn-start-task:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .btn-restart-task {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 8px 16px;
        border-radius: var(--radius-md);
        font-size: 0.8125rem;
        font-weight: 600;
        background: var(--color-accent);
        color: white;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-restart-task:hover:not(:disabled) {
        opacity: 0.85;
    }
    .btn-restart-task:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .spinner-inline {
        display: inline-block;
        width: 14px;
        height: 14px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }

    /* Review bar — Commit and move to Done */
    .chat-review-bar {
        display: flex;
        align-items: center;
        gap: 8px;
        justify-content: flex-end;
        padding: 8px 12px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-0);
        flex-shrink: 0;
    }
    .move-buttons {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-right: auto;
    }
    .btn-move {
        display: inline-flex;
        align-items: center;
        padding: 6px 12px;
        border-radius: var(--radius-md);
        font-size: 0.8125rem;
        font-weight: 500;
        background: var(--color-surface-2);
        color: var(--color-text);
        border: 1px solid var(--color-border);
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-move:hover {
        background: var(--color-surface-1);
        border-color: var(--color-accent);
    }
    .btn-commit-done {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 7px 16px;
        border-radius: var(--radius-md);
        font-size: 0.8125rem;
        font-weight: 600;
        background: var(--color-status-green);
        color: white;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-commit-done:hover:not(:disabled) {
        opacity: 0.85;
        transform: translateY(-1px);
    }
    .btn-commit-done:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .compression-entry {
        padding: 8px 0;
    }
    .compression-divider {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 4px 0;
    }
    .compression-divider .divider-line {
        flex: 1;
        height: 1px;
        background: var(--color-accent);
        opacity: 0.4;
    }
    .compression-divider .divider-content {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }
    .compression-label {
        font-size: 0.75rem;
        color: var(--color-accent);
        opacity: 0.8;
        white-space: nowrap;
    }
    .chat-form-wrapper {
        flex-shrink: 0;
    }
    .compression-summary {
        margin-top: 6px;
        padding: 8px 12px;
        background: color-mix(in srgb, var(--color-accent) 5%, transparent);
        border-radius: var(--radius-md);
        border-left: 3px solid var(--color-accent);
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
    }
    .merge-resolution-entry {
        padding: 8px 0;
    }
    .merge-resolution-card {
        background: color-mix(in srgb, var(--color-status-green) 6%, transparent);
        border-radius: var(--radius-md);
        border-left: 3px solid var(--color-status-green);
        overflow: hidden;
    }
    .merge-resolution-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 8px 12px 4px;
        color: var(--color-status-green);
    }
    .merge-resolution-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-status-green);
    }
    .merge-resolution-content {
        padding: 4px 12px 10px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
    }
</style>
