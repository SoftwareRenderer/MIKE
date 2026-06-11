<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { get } from 'svelte/store';
    import type { StreamEvent, Provider } from '$lib/types';
    import { createTaskStream, getTaskMessages, createTaskMessage, deleteTaskMessagesAfter, regenerateTaskMessages, updateTaskMessage, stopTask, startTask, updateTask, getConfig } from '$lib/api';
import { board as boardStore, config as configStore, showToast, streamingTaskIds } from '$lib/store';
    import ChatForm from './ChatForm.svelte';
    import ChatMessage from './ChatMessage.svelte';
    import ModelSelector from './ModelSelector.svelte';

    interface ChatEntry {
        id: string;
        role: string;
        content: string;
        reasoningContent?: string;
        isStreaming?: boolean;
        toolCall?: {
            toolName: string;
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

    let { projectId, taskId, taskPrompt = '', disabled = false, onRefreshDiff }: { projectId: string; taskId: string; taskPrompt?: string; disabled?: boolean; onRefreshDiff?: (data: DiffData) => void } = $props();

    let entries: ChatEntry[] = $state([]);
    let connected = $state(false);
    let connecting = $state(true);
    let ws: ReturnType<typeof createTaskStream> | null = null;
    let scrollEl: HTMLElement | null = null;
    let atBottom = $state(true);
    let inputValue = $state('');

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');

    let currentAssistantId: string | null = $state(null);
    let activeToolCalls = $state<Map<string, ChatEntry>>(new Map());
    let deleteConfirm = $state<{ entryId: string; count: number } | null>(null);
    let lastSentMessage: string | null = $state(null);
    let isStarting = $state(false);
    let firstConnectDone = $state(false); // track if initial onopen has fired

    // isLoading: only true when LLM is actively streaming tokens.
    // Shows stop button only during token generation, not during tool execution.
    let isLoading = $derived($streamingTaskIds.has(taskId));

    // A task is "new" (show "Start Task" button) only when:
    // - There are no entries yet (no messages loaded from backend)
    // - The task prompt is set
    // - Not currently loading
    // Once entries are loaded (even just user messages), show the chat.
    let isNewTask = $derived(entries.length === 0 && taskPrompt && !isLoading);

    function genId(): string {
        return Math.random().toString(36).slice(2, 10);
    }

    function formatToolArgs(toolName: string, args: Record<string, unknown>): string {
        switch (toolName) {
            case 'execute_command':
                return `$ ${args.command as string}`;
            case 'read_file':
                return `📄 ${args.path as string}`;
            case 'write_file': {
                const path = args.path as string;
                const content = args.content as string;
                const lines = content ? content.split('\n').length : 0;
                return `✏️ ${path} (${lines} lines)`;
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
        entries.push(entry);
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
    }

    function handleEvent(event: StreamEvent) {
        switch (event.type) {
            case 'token': {
                markStreaming();
                const data = event.data as { text: string };
                const entry = ensureAssistantEntry();
                entry.content += data.text;
                break;
            }
            case 'reasoning': {
                const data = event.data as { text: string };
                const entry = ensureAssistantEntry();
                entry.reasoningContent = (entry.reasoningContent || '') + data.text;
                break;
            }
            case 'tool_call': {
                const data = event.data as { tool_id: string; tool_name: string; args: Record<string, unknown> };
                // If the LLM sent the tool name as text content before the tool call,
                // clear it to avoid duplicating the tool name in the UI
                if (currentAssistantId) {
                    const entry = entries.find(e => e.id === currentAssistantId);
                    if (entry && entry.content.trim() === data.tool_name) {
                        entry.content = '';
                    }
                }
                closeCurrentAssistant();
                // Skip incomplete tool call deltas (empty tool_id from streaming fragments)
                if (!data.tool_id || data.tool_id === '') {
                    break;
                }
                const id = genId();
                const entry: ChatEntry = {
                    id, role: 'assistant', content: '', isStreaming: true, createdAt: new Date().toISOString(),
                    toolCall: { toolName: data.tool_name, args: data.args, raw_args: data.raw_args, isPending: true }
                };
                entries.push(entry);
                activeToolCalls.set(data.tool_id, entry);
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number };
                const entry = activeToolCalls.get(data.tool_id);
                activeToolCalls.delete(data.tool_id);
                if (entry && entry.toolCall) {
                    // Create a completely new entry object to force Svelte 5 reactivity
                    // (keyed {#each} may skip re-render if object reference is the same)
                    const updatedEntry: ChatEntry = {
                        ...entry,
                        isStreaming: false,
                        toolCall: {
                            ...entry.toolCall,
                            result: data.output,
                            success: data.success,
                            duration: data.duration,
                            isPending: false
                        }
                    };
                    const idx = entries.findIndex(e => e.id === entry.id);
                    if (idx !== -1) {
                        entries = entries.map((e, i) => i === idx ? updatedEntry : e);
                    }
                } else {
                    entries = [...entries];
                }
                break;
            }
            case 'user_message': {
                const data = event.data as { id?: string; role: string; content: string };
                // If this is an echo of a message we just sent, update the frontend entry's ID with the backend ID
                if (lastSentMessage === data.content) {
                    lastSentMessage = null;
                    if (data.id) {
                        // Find the last user entry (our optimistic message) and update its ID
                        for (let i = entries.length - 1; i >= 0; i--) {
                            if (entries[i].role === 'user' && entries[i].content === data.content) {
                                entries[i].id = data.id;
                                break;
                            }
                        }
                    }
                    break;
                }
                closeCurrentAssistant();
                entries.push({ id: data.id || genId(), role: data.role, content: data.content, isStreaming: false, createdAt: new Date().toISOString() });
                break;
            }
            case 'complete': {
                markNotStreaming();
                const data = event.data as { state: string; model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number };
                closeCurrentAssistant();
                // Attach stats to the last assistant entry
                const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && !e.toolCall);
                if (lastAssistant && data.predicted_tokens !== undefined) {
                    lastAssistant.stats = { model_name: data.model_name, prompt_tokens: data.prompt_tokens, predicted_tokens: data.predicted_tokens, prompt_ms: data.prompt_ms, predicted_ms: data.predicted_ms, duration: data.duration };
                }
                connected = false;
                break;
            }
            case 'error': {
                markNotStreaming();
                closeCurrentAssistant();
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
                entries.push(entry);
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
                    closeCurrentAssistant();
                    activeToolCalls.clear();
                    // Remove pending tool call entries (assistant entries with toolCall and isStreaming)
                    entries = entries.filter(e => !(e.toolCall && e.toolCall.isPending));
                    // Mark task as streaming so badge updates on board view
                    markStreaming();
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

        } catch {
            providers = [];
        }
    }

    /** Update providers from the config store (reacts to settings changes). */
    function updateProvidersFromStore() {
        const cfg = get(configStore);
        if (cfg && cfg.providers) {
            providers = cfg.providers;
        }
    }

    async function loadHistory() {
        try {
            const messages = await getTaskMessages(projectId, taskId);
            entries = messages.map(m => {
                // Restore error entries from persisted data
                if (m.role === 'error') {
                    const content = m.content || '';
                    const match = content.match(/^\[([^\]]+)\]\s*(.*)/);
                    const code = match ? match[1] : 'error';
                    const message = match ? match[2] : content;
                    return {
                        id: m.id, role: 'error' as const, content: message, isStreaming: false, createdAt: m.created_at,
                        error: { code, message },
                    };
                }
                // Restore tool call entries from persisted data
                if (m.role === 'tool_call' && m.tool_calls) {
                    const tc = JSON.parse(m.tool_calls);
                    const result = m.tool_result ? JSON.parse(m.tool_result) : null;
                    return {
                        id: m.id, role: 'assistant' as const, content: '', isStreaming: false, createdAt: m.created_at,
                        toolCall: {
                            toolName: tc.tool_name,
                            args: tc.args ?? {},
                            raw_args: tc.raw_args,
                            result: result?.output,
                            success: result?.success ?? true,
                            duration: result?.duration,
                            isPending: false,
                        },
                    };
                }
                return {
                    id: m.id, role: m.role, content: m.content, reasoningContent: m.reasoning_content, isStreaming: false, createdAt: m.created_at,
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
        } catch { /* No history yet */ }
    }

    function scroll() {
        if (scrollEl && atBottom) {
            scrollEl.scrollTop = scrollEl.scrollHeight;
        }
    }

    function checkScroll() {
        if (scrollEl) {
            atBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 32;
        }
    }

    function handleSend(msg: string) {
        if (!msg || disabled) return;
        // Remove all error entries (cleared by next user action)
        entries = entries.filter(e => e.role !== 'error');
        // Add message optimistically
        entries.push({ id: genId(), role: 'user', content: msg, isStreaming: false, createdAt: new Date().toISOString() });
        // Create assistant entry immediately so streaming UI appears
        ensureAssistantEntry();
        lastSentMessage = msg;
        // Send with model info for lazy-start
        if (ws && ws.sendWithModel) {
            ws.sendWithModel(msg, selectedModel, selectedUrl);
        } else if (ws) {
            ws.send(msg);
        }
        scroll();
    }

    async function handleModelSelect(providerName: string, modelName: string, providerUrl: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        // Update task with new model/provider
        try {
            await updateTask(projectId, taskId, {
                model_name: modelName,
                llama_server_url: providerUrl,
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
            await regenerateTaskMessages(projectId, taskId, lastUser.id);
        } catch (e) {
            console.error('Failed to regenerate messages:', e);
        }
        // Trim local entries: keep everything up to and including the last user message
        const userIdx = entries.findIndex(e => e.id === lastUser.id);
        entries = entries.slice(0, userIdx + 1);
        // Create assistant entry immediately so streaming UI appears
        ensureAssistantEntry();
        // Re-send the user message to trigger a new LLM response
        lastSentMessage = lastUser.content;
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
        isStarting = true;
        // Add the prompt as the first user message
        entries.push({ id: genId(), role: 'user', content: taskPrompt, isStreaming: false, createdAt: new Date().toISOString() });
        // Create assistant entry immediately so streaming UI appears
        ensureAssistantEntry();
        lastSentMessage = taskPrompt;
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

    async function handleDelete() {
        if (!deleteConfirm) return;
        const entryId = deleteConfirm.entryId;
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) {
            deleteConfirm = null;
            return;
        }
        try {
            await deleteTaskMessagesAfter(projectId, taskId, entryId);
            entries = entries.filter((_, i) => i < idx);
        } catch (e) {
            showToast(String(e));
        }
        deleteConfirm = null;
    }

    async function handleEdit(entryId: string, newContent: string) {
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        // Find the first PERSISTED message after this one (backend IDs are UUIDs).
        // Frontend-optimistic entries (streaming assistant) use random IDs not in the DB.
        const isBackendId = (id: string) => /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id);
        const firstPersistedAfter = entries.slice(idx + 1).find(e => isBackendId(e.id));
        // Optimistic update: update local state immediately
        lastSentMessage = newContent;
        entries[idx].content = newContent;
        entries = entries.slice(0, idx + 1);
        if (ws) ws.send(newContent);
        // Fire-and-forget API calls with error handling
        if (firstPersistedAfter) {
            deleteTaskMessagesAfter(projectId, taskId, firstPersistedAfter.id).catch(e => showToast(String(e)));
        }
        updateTaskMessage(projectId, taskId, entryId, newContent).catch(e => showToast(String(e)));
    }

    function connect() {
        if (ws) return;
        connecting = true;
        ws = createTaskStream(projectId, taskId, (event) => handleEvent(event));
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
            connected = false;
            closeCurrentAssistant();
            isStarting = false;
            lastSentMessage = null;
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
                entries.push(entry);
            }
            // Auto-retry after 3 seconds
            setTimeout(() => { if (!ws && !destroyed) connect(); }, 3000);
        };
        ws.ws.onerror = () => { connecting = false; };
    }

    /** Force immediate WebSocket reconnect. */
    function handleRetryConnect() {
        if (ws) {
            ws.close();
            ws = null;
        }
        // Remove existing connection lost errors
        entries = entries.filter(e => !(e.role === 'error' && e.error?.code === 'ws_disconnected'));
        connect();
    }

    let destroyed = $state(false);

    onMount(async () => {
        await loadProviders();
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
        } catch {
            // Task may not exist yet
        }
        connect();
        // Subscribe to config store to react to settings changes
        return configStore.subscribe(() => updateProvidersFromStore());
    });

    onDestroy(() => {
        destroyed = true;
        if (ws) ws.close();
    });
</script>

<div class="task-chat">
    <!-- Live region for screen reader announcements -->
    <div class="sr-only" aria-live="polite" aria-atomic="true">
        {#if isLoading && !connected}Connecting...{:else if isLoading}Agent is responding...{:else if connected}Connected, waiting for messages{/if}
    </div>
    <div class="chat-messages" bind:this={scrollEl} onscroll={checkScroll}>
        {#if isNewTask && taskPrompt}
            <!-- New task: show prompt as first message -->
            <ChatMessage role="user" content={taskPrompt} isStreaming={false} timestamp={entries[0]?.createdAt} onEdit={async (newContent) => { await updateTask(projectId, taskId, { prompt: newContent }); }} />
        {:else if entries.length === 0}
            <div class="chat-empty">
                <svg width="32" height="32" viewBox="0 0 16 16" fill="none">
                    <path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1" stroke-linejoin="round" opacity="0.3"/>
                </svg>
                <p>No messages yet</p>
                <span class="hint">Send a message to start the conversation</span>
            </div>
        {:else}
            {#each entries as entry (entry.id)}
                {#if entry.toolCall}
                    <div class="tool-call-entry">
                        <div class="tool-call-card">
                            <div class="tool-call-header">
                                <span class="tool-icon">{#if entry.toolCall.toolName === 'execute_command'}⚡{:else if entry.toolCall.toolName === 'read_file'}📄{:else if entry.toolCall.toolName === 'write_file'}✏️{:else if entry.toolCall.toolName === 'task_complete'}✅{:else}⚡{/if}</span>
                                <span class="tool-name">{entry.toolCall.toolName}</span>
                                {#if entry.toolCall.isPending}
                                    <span class="tool-status pending"><span class="spinner"></span> Running...</span>
                                {:else if entry.toolCall.success}
                                    <span class="tool-status success">Done</span>
                                {:else}
                                    <span class="tool-status error">Failed</span>
                                {/if}
                                {#if entry.toolCall.duration !== undefined}
                                    <span class="tool-duration">{entry.toolCall.duration}ms</span>
                                {/if}
                            </div>
                            <pre class="tool-args">{formatToolArgs(entry.toolCall.toolName, entry.toolCall.args)}</pre>
                            {#if entry.toolCall.result !== undefined}
                                <pre class="tool-result" class:success={entry.toolCall.success} class:failure={!entry.toolCall.success}>
                                    {entry.toolCall.result}
                                </pre>
                            {/if}
                        </div>
                    </div>
                {:else if entry.role === 'error'}
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
                            </div>
                            <div class="error-message">{entry.content}</div>
                            {#if entry.error?.code === 'ws_disconnected'}
                                <button class="btn-retry" onclick={handleRetryConnect}>
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <polyline points="23 4 23 10 17 10"/>
                                        <path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>
                                    </svg>
                                    Retry now
                                </button>
                            {/if}
                        </div>
                    </div>
                {:else}
                    <ChatMessage role={entry.role} content={entry.content} reasoningContent={entry.reasoningContent} isStreaming={entry.isStreaming || false} timestamp={entry.createdAt} messageId={entry.id} stats={entry.stats} onDelete={() => { const idx = entries.findIndex(e => e.id === entry.id); deleteConfirm = { entryId: entry.id, count: entries.length - idx }; }} onEdit={(newContent) => handleEdit(entry.id, newContent)} onRegenerate={() => handleRegenerate(entry.id)} />
                {/if}
            {/each}
        {/if}
    </div>

    {#if deleteConfirm}
        <div class="delete-overlay" role="dialog" aria-modal="true" aria-labelledby="delete-dialog-title" aria-live="assertive">
            <div class="delete-dialog">
                <h3 id="delete-dialog-title">Delete Message</h3>
                <p>This will delete {deleteConfirm.count} message{deleteConfirm.count > 1 ? 's' : ''}. This action cannot be undone.</p>
                <div class="delete-actions">
                    <button class="btn-cancel" onclick={() => deleteConfirm = null}>Cancel</button>
                    <button class="btn-delete" onclick={handleDelete}>Delete</button>
                </div>
            </div>
        </div>
    {/if}
    {#if isNewTask && !isLoading}
        <div class="chat-start">
            {#if providers.length > 0}
                <div class="start-model-row">
                    <ModelSelector
                        providers={providers}
                        currentModel={selectedModel}
                        currentUrl={selectedUrl}
                        onSelect={handleModelSelect}
                    />
                </div>
            {/if}
            <button class="btn-start-task" onclick={handleStartTask} disabled={isStarting || disabled || (providers.length > 0 && !selectedModel)}>
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
        <ChatForm
            bind:value={inputValue}
            disabled={disabled}
            isLoading={isLoading}
            placeholder="Send a message to the agent..."
            providers={providers}
            selectedModel={selectedModel}
            selectedUrl={selectedUrl}
            onSubmit={(msg) => handleSend(msg)}
            onStop={handleStop}
            onModelSelect={handleModelSelect}
        />
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
        border-left: 3px solid var(--color-accent);
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
    .tool-result.success { border-left: 3px solid var(--color-status-green); }
    .tool-result.failure { border-left: 3px solid var(--color-status-red); }

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

    .delete-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
    }
    .delete-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px 24px;
        max-width: 360px;
        width: 90%;
    }
    .delete-dialog h3 {
        font-size: 0.875rem;
        font-weight: 600;
        margin: 0 0 8px;
        color: var(--color-text-primary);
    }
    .delete-dialog p {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        margin: 0 0 16px;
        line-height: 1.5;
    }
    .delete-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
    }
    .btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        cursor: pointer;
    }
    .btn-cancel:hover {
        background: var(--color-surface-4);
    }
    .btn-delete {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        background: var(--color-status-red);
        color: white;
        cursor: pointer;
    }
    .btn-delete:hover {
        opacity: 0.85;
    }
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
</style>
