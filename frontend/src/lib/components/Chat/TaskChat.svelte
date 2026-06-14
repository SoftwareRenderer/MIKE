<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { get } from 'svelte/store';
    import type { StreamEvent, Provider, Thread } from '$lib/types';
    import { createTaskStream, getTaskMessages, createTaskMessage, deleteTaskMessagesAfter, deleteTaskMessage, regenerateTaskMessages, updateTaskMessage, stopTask, startTask, updateTask, moveTask, getConfig, getTaskThreads, switchTaskThread, forkTaskThread, checkMessageDelete, commitAndDone } from '$lib/api';
import { board as boardStore, config as configStore, showToast, streamingTaskIds, showConfirm } from '$lib/store';
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
        toolCalls?: Array<{
            toolId: string;
            toolName: string;
            displayName?: string;
            args: Record<string, unknown>;
            raw_args?: string;
            result?: string;
            success?: boolean;
            duration?: number;
            isPending?: boolean;
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
    }

    type DiffData = { diff: string; untracked: Array<{ path: string; diff: string; file_size?: number }> };

    let { projectId, taskId, taskPrompt = '', taskStatus = 'backlog', disabled = false, onRefreshDiff, onMoveTask }: { projectId: string; taskId: string; taskPrompt?: string; taskStatus?: string; disabled?: boolean; onRefreshDiff?: (data: DiffData) => void; onMoveTask?: (status: string) => void } = $props();

    let entries: ChatEntry[] = $state([]);
    let connected = $state(false);
    let connecting = $state(true);
    let ws: ReturnType<typeof createTaskStream> | null = null;
    let scrollEl: HTMLElement | null = null;
    let atBottom = $state(true);
    let prevScrollHeight = 0;
    let userInputPending = false;
    let inputTimeout: ReturnType<typeof setTimeout> | null = null;
    let inputValue = $state('');

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');

    let currentAssistantId: string | null = $state(null);
    let activeToolCalls = $state<Map<string, { entryId: string }>>(new Map());

    let lastSentMessage: string | null = $state(null);
    let isStarting = $state(false);
    let isRegenerating = $state(false); // true between regenerate delete and echo arrival
    let firstConnectDone = $state(false); // track if initial onopen has fired
    let isCommitting = $state(false);

    // Thread state
    let threads: Thread[] = $state([]);
    let activeThreadId = $state('');

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

    // Show "Start Task" button when task is in backlog (not yet started).
    let showStartButton = $derived(taskStatus === 'backlog' && !isLoading);

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

    function formatToolArgs(toolName: string, args: Record<string, unknown>): string {
        switch (toolName) {
            case 'execute_command':
                return `$ ${args.command as string}`;
            case 'read_file': {
                const path = args.path as string;
                return path.startsWith('./') ? path : `./${path}`;
            }
            case 'write_file': {
                const path = args.path as string;
                const content = args.content as string;
                const lines = content ? content.split('\n').length : 0;
                const prefixed = path.startsWith('./') ? path : `./${path}`;
                return `${prefixed} (${lines} lines)`;
            }
            case 'task_complete':
                return args.summary as string || '(task complete)';
            default:
                return JSON.stringify(args, null, 2);
        }
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
                const data = event.data as { text: string };
                const entry = ensureAssistantEntry();
                entry.content += data.text;
                scroll();
                break;
            }
            case 'reasoning': {
                clearAwaiting();
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
                entry.toolCalls.push({
                    toolId: data.tool_id,
                    toolName: data.tool_name,
                    displayName: data.tool_display_name,
                    args: data.args,
                    raw_args: data.raw_args,
                    isPending: true
                });
                entry.isStreaming = true;
                activeToolCalls.set(data.tool_id, { entryId: entry.id });
                scroll();
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number };
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
            case 'user_message': {
                const data = event.data as { id?: string; role: string; content: string; thread_id?: string };
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
                const data = event.data as { state: string; model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number };
                closeCurrentAssistant();
                // Track context usage from the last turn's prompt tokens
                if (data.prompt_tokens !== undefined) {
                    contextState.used = data.prompt_tokens;
                }
                // Attach stats to the last assistant entry
                const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && !e.toolCalls);
                if (lastAssistant && data.predicted_tokens !== undefined) {
                    lastAssistant.stats = { model_name: data.model_name, prompt_tokens: data.prompt_tokens, predicted_tokens: data.predicted_tokens, prompt_ms: data.prompt_ms, predicted_ms: data.predicted_ms, duration: data.duration };
                }
                connected = false;
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
                const data = event.data as { state: string };
                if (data.state === 'running') {
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
            case 'stats': {
                const data = event.data as { model_name: string; predicted_tokens: number; predicted_ms: number };
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
        }
        scroll();
    }

    async function loadProviders() {
        try {
            const cfg = await getConfig();
            providers = cfg.providers || [];
            // Initialize model selection from highest-priority provider's default model
            if (providers.length > 0) {
                const firstProvider = providers[0];
                const _getModelName = (m: string | { name: string; reasoning_effort?: string }) => typeof m === 'string' ? m : m.name;
                if (firstProvider.default_model && firstProvider.models.some(m => _getModelName(m) === firstProvider.default_model)) {
                    selectedUrl = firstProvider.url;
                    selectedModel = firstProvider.default_model;
                } else if (firstProvider.models.length > 0) {
                    // Fallback: first available model of highest-priority provider
                    selectedUrl = firstProvider.url;
                    selectedModel = _getModelName(firstProvider.models[0]);
                }
            }
            updateContextLimit();

        } catch {
            providers = [];
        }
    }

    /** Update providers from the config store (reacts to settings changes). */
    function updateProvidersFromStore() {
        const cfg = get(configStore);
        if (cfg && cfg.providers) {
            providers = cfg.providers;
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
            // Filter out title generation messages (invisible, used for stats only)
            entries = messages.filter(m => m.role !== 'title').map(m => {
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
                            result: result?.output,
                            success: result?.success ?? true,
                            duration: result?.duration,
                            isPending: false,
                        }],
                    };
                }
                return {
                    ...base, id: m.id, role: m.role, content: m.content, reasoningContent: m.reasoning_content, isStreaming: false, createdAt: m.created_at,
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
            entries = mergeToolCallsIntoAssistant(entries);
            // Restore context usage from the last assistant message
            const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && e.stats);
            if (lastAssistant?.stats?.prompt_tokens) {
                contextState.used = lastAssistant.stats.prompt_tokens;
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
            await loadHistory();
            // Update URL with new thread param
            updateThreadUrl(threadId);
            // Reconnect WebSocket — clear onclose to prevent stale handler
            // from nulling out the new ws reference after it's created.
            if (ws) {
                ws.ws.onclose = null;
                ws.close();
                ws = null;
                connected = false;
            }
            connect(true); // skip sync buffer after thread switch
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
            await loadHistory();
            // Update URL with the new fork's thread ID
            updateThreadUrl(forked.id);
            // Reconnect WebSocket — clear onclose to prevent stale handler
            // from nulling out the new ws reference after it's created.
            if (ws) {
                ws.ws.onclose = null;
                ws.close();
                ws = null;
                connected = false;
            }
            connect(true); // skip sync buffer after fork
        } catch (e) {
            showToast(String(e));
        }
    }

    function scroll() {
        if (!scrollEl || !atBottom) return;
        requestAnimationFrame(() => {
            if (scrollEl && atBottom) {
                scrollEl.scrollTop = scrollEl.scrollHeight;
            }
        });
    }

    function checkScroll() {
        if (!scrollEl || !userInputPending) return;
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

    async function handleSend(msg: string) {
        if (!msg || disabled) return;
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
                    return { ...b, tasks: b.tasks.map(t => t.id === taskId ? updated : t) };
                });
            } catch (e) {
                showToast(String(e));
                return;
            }
        }
        // Remove all error entries (cleared by next user action)
        entries = entries.filter(e => e.role !== 'error');
        // Add message optimistically with current thread context
        entries = [...entries, { id: genId(), role: 'user', content: msg, threadId: activeThreadId, isStreaming: false, createdAt: new Date().toISOString() }];
        // Don't pre-create — let the first token/tool_call event create the assistant entry
        lastSentMessage = msg;
        // Send with model info for lazy-start
        awaitingResponse = true;
        if (ws && ws.sendWithModel) {
            ws.sendWithModel(msg, selectedModel, selectedUrl);
        } else if (ws) {
            ws.send(msg);
        }
        scroll();
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
        if (ws) ws.send(lastUser.content);
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
                    return { ...b, tasks: b.tasks.map(t => t.id === taskId ? updated : t) };
                });
            } catch (e) {
                console.error('Failed to start task:', e);
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
                return { ...b, tasks: b.tasks.map(t => t.id === taskId ? updated : t) };
            });
        } catch (e) {
            console.error('Failed to start task:', e);
            isStarting = false;
            return;
        }
        isStarting = false;
    }

    async function handleStop() {
        markNotStreaming();
        awaitingResponse = false;
        if (ws) {
            ws.close();
            ws = null;
            connected = false;
        }
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

    async function handleCommitAndDone() {
        if (isCommitting || disabled) return;
        isCommitting = true;
        try {
            const updated = await commitAndDone(projectId, taskId);
            boardStore.update(b => {
                if (!b) return b;
                return { ...b, tasks: b.tasks.map(t => t.id === taskId ? updated : t) };
            });
            // Reload history to show any system messages from merge conflict resolution
            await loadHistory();
        } catch (e) {
            showToast(String(e));
        }
        isCommitting = false;
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

    async function handleEditFork(entryId: string, _originalContent: string, newContent: string) {
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
        // Fork the conversation from this message. The edited content is sent
        // via WebSocket after the fork — it becomes the first new message in the fork.
        try {
            const fromThreadId = entry.threadId || activeThreadId;
            const forked = await forkTaskThread(projectId, taskId, fromThreadId, entryId, newContent);
            // Refresh threads list from server to ensure consistency
            threads = await getTaskThreads(projectId, taskId);
            activeThreadId = forked.id;
            await loadHistory();
            // Update URL with the new fork's thread ID
            updateThreadUrl(forked.id);
            // Reconnect WebSocket — clear onclose to prevent stale handler
            // from nulling out the new ws reference after it's created.
            if (ws) {
                ws.ws.onclose = null;
                ws.close();
                ws = null;
                connected = false;
            }
            connect(true); // skip sync buffer after fork
            // Wait for WebSocket to be OPEN before sending the edited message.
            // sendWithModel silently drops messages when readyState !== OPEN.
            const wsReady = new Promise<void>((resolve, reject) => {
                const maxWait = 5000;
                const start = Date.now();
                const poll = () => {
                    if (ws && ws.ws.readyState === WebSocket.OPEN) {
                        resolve();
                    } else if (Date.now() - start > maxWait) {
                        reject(new Error('WebSocket connection timeout'));
                    } else {
                        setTimeout(poll, 50);
                    }
                };
                poll();
            });
            await wsReady;
            lastSentMessage = newContent;
            awaitingResponse = true;
            if (ws && ws.sendWithModel) {
                ws.sendWithModel(newContent, selectedModel, selectedUrl);
            } else if (ws) {
                ws.send(newContent);
            }
        } catch (e) {
            showToast(String(e));
        }
    }

    async function handleEditSend(entryId: string, _originalContent: string, newContent: string) {
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;

        // Backlog: persist edit only (soft edit, no LLM trigger)
        if (taskStatus === 'backlog') {
            entries = entries.map((e, i) => i === idx ? { ...e, content: newContent, createdAt: new Date().toISOString() } : e);
            updateTaskMessage(projectId, taskId, entryId, newContent).catch(e => showToast(String(e)));
            updateTask(projectId, taskId, { prompt: newContent }).catch(e => showToast(String(e)));
            return;
        }

        // Idle/started: delete the old message and create a new one with edited content,
        // then send via WebSocket to trigger a fresh LLM response.
        // This avoids the backend's duplicate detection which would reuse the existing ID.
        if (isLoading || disabled) return;
        // Remove the old entry and any trailing assistant entries from UI
        entries = entries.slice(0, idx);
        closeCurrentAssistant();
        // Delete old message from DB (and any messages after it)
        try {
            await deleteTaskMessagesAfter(projectId, taskId, entryId);
            // Sync active thread after deletion
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
        // Create new message in DB
        const newMsg = await createTaskMessage(projectId, taskId, 'user', newContent);
        // Add to UI
        entries = [...entries, { id: newMsg.id, role: 'user', content: newContent, threadId: activeThreadId, isStreaming: false, createdAt: newMsg.created_at || new Date().toISOString() }];
        // Don't pre-create — let the first token/tool_call event create the assistant entry
        // Send via WebSocket to trigger lazy-start executor
        lastSentMessage = newContent;
        awaitingResponse = true;
        if (ws && ws.sendWithModel) {
            ws.sendWithModel(newContent, selectedModel, selectedUrl);
        } else if (ws) {
            ws.send(newContent);
        }
        scroll();
    }

    function connect(skipSync = false) {
        if (ws) return;
        connecting = true;
        // Skip sync buffer on first connect (history already loaded from DB via loadHistory).
        // Enable sync on reconnects to catch events missed during the disconnect window.
        // After intentional reconnects (fork/thread-switch), skip sync because loadHistory
        // already loaded the correct messages and the sync buffer may contain stale events
        // from the old thread.
        const syncBuffer = firstConnectDone && !skipSync;
        ws = createTaskStream(projectId, taskId, (event) => handleEvent(event), syncBuffer);
        ws.ws.onopen = () => {
            connecting = false;
            connected = true;
            if (!firstConnectDone) {
                // First connection — just remove any connection errors
                firstConnectDone = true;
                entries = entries.filter(e => !(e.role === 'error' && e.error?.code === 'ws_disconnected'));
            } else {
                // Reconnect — reload history from DB to discard any stale
                // unpersisted entries (e.g., partial assistant message from
                // a disconnected stream) that would appear as duplicates
                loadHistory();
            }
        };
        ws.ws.onclose = (event: CloseEvent) => {
            const wasConnected = connected;
            markNotStreaming();
            awaitingResponse = false;
            connected = false;
            closeCurrentAssistant();
            isStarting = false;
            lastSentMessage = null;
            isRegenerating = false;
            ws = null;
            // Show connection lost error if this was an unexpected disconnect
            if (!event.wasClean && (wasConnected || connecting) && !destroyed) {
                const entry: ChatEntry = {
                    id: genId(),
                    role: 'error',
                    content: 'Connection lost. Reconnecting...',
                    isStreaming: false,
                    error: { code: 'ws_disconnected', message: 'Connection lost. Reconnecting...' },
                    createdAt: new Date().toISOString(),
                };
                entries = [...entries, entry];
            }
            // Auto-retry after 3 seconds
            setTimeout(() => { if (!ws && !destroyed) connect(); }, 3000);
        };
        ws.ws.onerror = () => { connecting = false; };
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

    /** Force immediate WebSocket reconnect. */
    function handleRetryConnect() {
        if (ws) {
            ws.ws.onclose = null;
            ws.close();
            ws = null;
        }
        // Remove existing connection lost errors
        entries = entries.filter(e => !(e.role === 'error' && e.error?.code === 'ws_disconnected'));
        connect();
    }

    let destroyed = $state(false);

    // Mobile keyboard: track visualViewport shrink and add padding to keep input visible
    let keyboardOffset = $state(0);
    let chatFormRef: HTMLElement | null = null;

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
                // Reconnect WebSocket — clear onclose to prevent stale handler
                if (ws) {
                    ws.ws.onclose = null;
                    ws.close();
                    ws = null;
                    connected = false;
                }
                connect(true); // skip sync buffer after thread switch
            } catch (e) {
                showToast(String(e));
            }
        }
    }

    onMount(async () => {
        await loadProviders();
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
        };
    });

    onDestroy(() => {
        destroyed = true;
        if (ws) ws.close();
        if (inputTimeout) clearTimeout(inputTimeout);
    });
</script>

<div class="task-chat">
    <!-- Live region for screen reader announcements -->
    <div class="sr-only" aria-live="polite" aria-atomic="true">
        {#if isLoading && !connected}Connecting...{:else if isLoading}Agent is responding...{:else if connected}Connected, waiting for messages{/if}
    </div>
    <div class="chat-messages" bind:this={scrollEl} onscroll={checkScroll} onwheel={markUserInput} ontouchstart={markUserInput}>
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
                {#if entry.role === 'user' && threads.length > 0}
                    {#if getCheckpointThreads(entry, entryIdx === 0)}
                        <CheckpointDivider
                            timestamp={entry.createdAt}
                            threadId={entry.threadId || activeThreadId}
                            threads={threads}
                            activeThreadId={getCheckpointThreads(entry, entryIdx === 0)!.id}
                            onSwitch={handleThreadSwitch}
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
                {:else}
                    <ChatMessage role={entry.role} content={entry.content} reasoningContent={entry.reasoningContent} isStreaming={entry.isStreaming || false} timestamp={entry.createdAt} messageId={entry.id} stats={entry.stats} toolCalls={entry.toolCalls} formatToolArgs={formatToolArgs} onDelete={entryIdx === 0 ? undefined : () => { const idx = entries.findIndex(e => e.id === entry.id); handleDeleteRequest(entry.id, entries.length - idx); }} onFork={(msgId, origContent, newContent) => handleEditFork(msgId, origContent, newContent)} onSend={(msgId, origContent, newContent) => handleEditSend(msgId, origContent, newContent)} isLastUserMessage={lastUserMessageIds.has(entry.id) && !isLoading} isFirstUserMessage={entryIdx === 0} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => handleRegenerate(entry.id) : undefined} />
                {/if}
            {/each}
        {/if}
        {#if awaitingResponse}
            <div class="awaiting-llm">
                <span class="spinner"></span>
                {#if queuePosition > 0}
                    <span>Waiting in queue — position {queuePosition}, ~{queueEstimatedWait}s</span>
                {:else}
                    <span>Awaiting LLM provider...</span>
                {/if}
            </div>
        {/if}
    </div>


    {#if showStartButton}
        <div class="chat-start">
            {#if providers.length > 0}
                <div class="start-model-row">
                    <ModelSelector
                        providers={providers}
                        currentModel={selectedModel}
                        currentUrl={selectedUrl}
                        currentReasoningEffort={selectedReasoningEffort}
                        onSelect={handleModelSelect}
                    />
                </div>
            {/if}
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
                <button class="btn-commit-done" onclick={handleCommitAndDone} disabled={isCommitting || disabled} title="Commit changes and mark task as done">
                    {#if isCommitting}
                        <span class="spinner-inline"></span> Committing...
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8l3 3 5-6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                        Commit and Mark as Done
                    {/if}
                </button>
            </div>
        {/if}
        <ChatForm
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
            onSubmit={(msg) => handleSend(msg)}
            onStop={handleStop}
            onModelSelect={handleModelSelect}
        />
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
    .chat-messages {
        flex: 1;
        overflow-y: auto;
        padding: 8px 12px;
        min-height: 0;
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
    .tool-call-entry { padding: 4px 0; }
    .tool-call-card {
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        border-left: 3px solid;
        padding: 8px 12px;
    }
    .tool-call-header { display: flex; align-items: center; gap: 6px; margin-bottom: 4px; }
    .tool-name { font-weight: 600; font-size: 0.75rem; color: var(--color-accent); }
    .tool-status { font-size: 0.75rem; font-weight: 500; }
    .tool-status.pending { color: var(--color-status-yellow); }
    .tool-status.success { color: var(--color-status-green); }
    .tool-status.error { color: var(--color-status-red); }
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
    .tool-duration { font-size: 0.75rem; color: var(--color-text-tertiary); margin-left: auto; }
    .tool-args {
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono);
        overflow-x: auto;
        margin: 0;
        color: var(--color-text-secondary);
    }
    .tool-result {
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono);
        overflow-x: auto;
        margin: 4px 0 0;
        max-height: 120px;
        overflow-y: auto;
        white-space: pre-wrap;
        word-break: break-word;
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
    .spinner-inline {
        display: inline-block;
        width: 14px;
        height: 14px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }

    /* Review bar — Commit and Mark as Done */
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
</style>
