<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { StreamEvent } from '$lib/types';
    import { createTaskStream, getTaskMessages, deleteTaskMessagesAfter, updateTaskMessage, stopTask, startTask } from '$lib/api';
    import ChatForm from './ChatForm.svelte';
    import ChatMessage from './ChatMessage.svelte';

    interface ChatEntry {
        id: string;
        role: string;
        content: string;
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
        stats?: {
            model_name: string;
            prompt_tokens: number;
            predicted_tokens: number;
            prompt_ms: number;
            predicted_ms: number;
            duration: number;
        };
    }

    let { projectId, taskId, taskPrompt = '', disabled = false }: { projectId: string; taskId: string; taskPrompt?: string; disabled?: boolean } = $props();

    let entries: ChatEntry[] = $state([]);
    let isLoading = $state(false);
    let connected = $state(false);
    let connecting = $state(true);
    let ws: ReturnType<typeof createTaskStream> | null = null;
    let scrollEl: HTMLElement | null = null;
    let atBottom = $state(true);
    let inputValue = $state('');

    let currentAssistantId: string | null = $state(null);
    let activeToolCalls = $state<Map<string, ChatEntry>>(new Map());
    let deleteConfirm = $state<{ entryId: string; count: number } | null>(null);
    let lastSentMessage: string | null = $state(null);
    let isStarting = $state(false);

    // A task is "new" when it has no assistant messages (only user messages or empty)
    let hasAssistantResponse = $derived(entries.some(e => e.role === 'assistant'));
    let isNewTask = $derived(!hasAssistantResponse && taskPrompt && !isLoading);

    function genId(): string {
        return Math.random().toString(36).slice(2, 10);
    }

    function ensureAssistantEntry(): ChatEntry {
        if (currentAssistantId) {
            const entry = entries.find(e => e.id === currentAssistantId);
            if (entry) return entry;
        }
        const id = genId();
        currentAssistantId = id;
        const entry: ChatEntry = { id, role: 'assistant', content: '', isStreaming: true };
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

    function handleEvent(event: StreamEvent) {
        switch (event.type) {
            case 'token': {
                const data = event.data as { text: string };
                const entry = ensureAssistantEntry();
                entry.content += data.text;
                break;
            }
            case 'reasoning': {
                const data = event.data as { text: string };
                const entry = ensureAssistantEntry();
                entry.content += data.text;
                break;
            }
            case 'tool_call': {
                const data = event.data as { tool_id: string; tool_name: string; args: Record<string, unknown> };
                closeCurrentAssistant();
                const id = genId();
                const entry: ChatEntry = {
                    id, role: 'assistant', content: '', isStreaming: true,
                    toolCall: { toolName: data.tool_name, args: data.args, raw_args: data.raw_args, isPending: true }
                };
                entries.push(entry);
                activeToolCalls.set(data.tool_id, entry);
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number };
                const entry = activeToolCalls.get(data.tool_id);
                if (entry && entry.toolCall) {
                    entry.toolCall.result = data.output;
                    entry.toolCall.success = data.success;
                    entry.toolCall.duration = data.duration;
                    entry.toolCall.isPending = false;
                    entry.isStreaming = false;
                }
                activeToolCalls.delete(data.tool_id);
                break;
            }
            case 'user_message': {
                const data = event.data as { role: string; content: string };
                // Skip if this is an echo of a message we just sent (optimistic UI already added it)
                if (lastSentMessage === data.content) {
                    lastSentMessage = null;
                    break;
                }
                closeCurrentAssistant();
                entries.push({ id: genId(), role: data.role, content: data.content, isStreaming: false });
                break;
            }
            case 'complete': {
                const data = event.data as { state: string; model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number };
                closeCurrentAssistant();
                // Attach stats to the last assistant entry
                const lastAssistant = [...entries].reverse().find(e => e.role === 'assistant' && !e.toolCall);
                if (lastAssistant && data.predicted_tokens !== undefined) {
                    lastAssistant.stats = { model_name: data.model_name, prompt_tokens: data.prompt_tokens, predicted_tokens: data.predicted_tokens, prompt_ms: data.prompt_ms, predicted_ms: data.predicted_ms, duration: data.duration };
                }
                connected = false;
                isLoading = false;
                break;
            }
            case 'error': {
                closeCurrentAssistant();
                connected = false;
                isLoading = false;
                break;
            }
            case 'status': {
                const data = event.data as { state: string };
                if (data.state === 'running') {
                    isLoading = true;
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
        }
        scroll();
    }

    async function loadHistory() {
        try {
            const messages = await getTaskMessages(projectId, taskId);
            entries = messages.map(m => ({
                id: m.id, role: m.role, content: m.content, isStreaming: false,
                stats: m.model_name ? {
                    model_name: m.model_name,
                    prompt_tokens: m.prompt_tokens ?? 0,
                    predicted_tokens: m.predicted_tokens ?? 0,
                    prompt_ms: m.prompt_ms ?? 0,
                    predicted_ms: m.predicted_ms ?? 0,
                    duration: m.duration ?? 0,
                } : undefined,
            }));
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
        // Add message optimistically
        entries.push({ id: genId(), role: 'user', content: msg, isStreaming: false });
        lastSentMessage = msg;
        if (ws) ws.send(msg);
        isLoading = true;
        scroll();
    }

    async function handleRegenerate(entryId: string) {
        if (isLoading || disabled) return;
        const idx = entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        // Find the last user message before this assistant message
        const beforeEntries = entries.slice(0, idx);
        const lastUser = [...beforeEntries].reverse().find(e => e.role === 'user');
        if (!lastUser) return;
        // Delete messages from backend starting after the last user message
        try {
            await deleteTaskMessagesAfter(projectId, taskId, lastUser.id);
        } catch (e) {
            console.error('Failed to delete messages for regenerate:', e);
        }
        // Trim local entries: keep everything up to and including the last user message
        const userIdx = entries.findIndex(e => e.id === lastUser.id);
        entries = entries.slice(0, userIdx + 1);
        // Re-send the user message to trigger a new LLM response
        lastSentMessage = lastUser.content;
        if (ws) ws.send(lastUser.content);
        isLoading = true;
        scroll();
    }

    async function handleStartTask() {
        if (isStarting || disabled) return;
        isStarting = true;
        isLoading = true;
        // Add the prompt as the first user message
        entries.push({ id: genId(), role: 'user', content: taskPrompt, isStreaming: false });
        lastSentMessage = taskPrompt;
        scroll();
        try {
            await startTask(projectId, taskId);
        } catch (e) {
            console.error('Failed to start task:', e);
            isLoading = false;
            isStarting = false;
        }
    }

    async function handleStop() {
        if (ws) {
            ws.close();
            ws = null;
            connected = false;
        }
        isLoading = false;
        closeCurrentAssistant();
        try {
            await stopTask(projectId, taskId);
        } catch (e) {
            console.error('Failed to stop task:', e);
        }
    }

    function connect() {
        if (ws) return;
        connecting = true;
        ws = createTaskStream(projectId, taskId, (event) => handleEvent(event));
        ws.ws.onopen = () => { connecting = false; connected = true; };
        ws.ws.onclose = () => {
            connected = false;
            closeCurrentAssistant();
            isLoading = false;
            isStarting = false;
            lastSentMessage = null;
            ws = null;
            setTimeout(() => { if (!ws && !destroyed) connect(); }, 3000);
        };
        ws.ws.onerror = () => { connecting = false; };
    }

    let destroyed = $state(false);

    onMount(async () => {
        await loadHistory();
        connect();
    });

    onDestroy(() => {
        destroyed = true;
        if (ws) ws.close();
    });
</script>

<div class="task-chat">
    <div class="chat-messages" bind:this={scrollEl} onscroll={checkScroll}>
        {#if isNewTask && taskPrompt}
            <!-- New task: show prompt as first message -->
            <ChatMessage role="user" content={taskPrompt} isStreaming={false} />
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
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                    <path d="M6 1h4l1 2h3l-2 3h3l-3 4h2l-4 4H7l-1-2H3l2-3H3l3-4H6l1-2z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                                </svg>
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
                            <pre class="tool-args">{JSON.stringify(entry.toolCall.args, null, 2).trim() === '{}' ? entry.toolCall.raw_args || '(no arguments)' : JSON.stringify(entry.toolCall.args, null, 2)}</pre>
                            {#if entry.toolCall.result !== undefined}
                                <pre class="tool-result" class:success={entry.toolCall.success} class:failure={!entry.toolCall.success}>
                                    {entry.toolCall.result}
                                </pre>
                            {/if}
                        </div>
                    </div>
                {:else}
                    <ChatMessage role={entry.role} content={entry.content} isStreaming={entry.isStreaming || false} stats={entry.stats} onCopy={() => navigator.clipboard.writeText(entry.content)} onDelete={() => { const idx = entries.findIndex(e => e.id === entry.id); deleteConfirm = { entryId: entry.id, count: entries.length - idx }; }} onEdit={(newContent) => { const idx = entries.findIndex(e => e.id === entry.id); const messageAfterIdx = idx + 1; if (messageAfterIdx < entries.length) { deleteTaskMessagesAfter(projectId, taskId, entries[messageAfterIdx].id).catch(console.error); } updateTaskMessage(projectId, taskId, entry.id, newContent).catch(console.error); lastSentMessage = newContent; entries[idx].content = newContent; entries = entries.slice(0, idx + 1); if (ws) ws.send(newContent); isLoading = true; }} onRegenerate={() => handleRegenerate(entry.id)} />
                {/if}
            {/each}
        {/if}
    </div>

    {#if deleteConfirm}
        <div class="delete-overlay">
            <div class="delete-dialog">
                <h3>Delete Message</h3>
                <p>This will delete {deleteConfirm.count} message{deleteConfirm.count > 1 ? 's' : ''}. This action cannot be undone.</p>
                <div class="delete-actions">
                    <button class="btn-cancel" onclick={() => deleteConfirm = null}>Cancel</button>
                    <button class="btn-delete" onclick={() => { const idx = entries.findIndex(e => e.id === deleteConfirm!.entryId); entries = entries.filter((_, i) => i < idx); deleteTaskMessagesAfter(projectId, taskId, deleteConfirm!.entryId).catch(console.error); deleteConfirm = null; }}>Delete</button>
                </div>
            </div>
        </div>
    {/if}
    {#if isNewTask && !isLoading}
        <div class="chat-start">
            <button class="btn-start-task" onclick={handleStartTask} disabled={isStarting || disabled}>
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
            onSubmit={(msg) => handleSend(msg)}
            onStop={handleStop}
        />
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
    .chat-empty p { font-size: 13px; margin: 0; font-weight: 500; }
    .chat-empty .hint { font-size: 11px; }
    @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
    .tool-call-entry { padding: 4px 0; }
    .tool-call-card {
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        border-left: 3px solid var(--color-accent);
        padding: 8px 12px;
    }
    .tool-call-header { display: flex; align-items: center; gap: 6px; margin-bottom: 4px; }
    .tool-name { font-weight: 600; font-size: 12px; color: var(--color-accent); }
    .tool-status { font-size: 11px; font-weight: 500; }
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
    .tool-duration { font-size: 10px; color: var(--color-text-tertiary); margin-left: auto; }
    .tool-args {
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        font-size: 11px;
        font-family: var(--font-mono);
        overflow-x: auto;
        margin: 0;
        color: var(--color-text-secondary);
    }
    .tool-result {
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        font-size: 11px;
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
        align-items: center;
        justify-content: center;
        padding: 16px 12px;
        border-top: 1px solid var(--color-border);
        flex-shrink: 0;
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
