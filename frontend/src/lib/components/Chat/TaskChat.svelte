<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { StreamEvent } from '$lib/types';
    import { createTaskStream, getTaskMessages } from '$lib/api';
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
    }

    let { projectId, taskId, disabled = false }: { projectId: string; taskId: string; disabled?: boolean } = $props();

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
                closeCurrentAssistant();
                entries.push({ id: genId(), role: data.role, content: data.content, isStreaming: false });
                break;
            }
            case 'complete':
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
        }
        scroll();
    }

    async function loadHistory() {
        try {
            const messages = await getTaskMessages(projectId, taskId);
            entries = messages.map(m => ({
                id: m.id, role: m.role, content: m.content, isStreaming: false
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
        if (ws) ws.send(msg);
        isLoading = true;
        scroll();
    }

    function handleStop() {
        if (ws) {
            ws.close();
            ws = null;
            connected = false;
            isLoading = false;
            closeCurrentAssistant();
            setTimeout(() => connect(), 500);
        }
    }

    function connect() {
        if (ws) return;
        connecting = true;
        ws = createTaskStream(projectId, taskId, (event) => handleEvent(event));
        ws.ws.onopen = () => { connecting = false; connected = true; };
        ws.ws.onclose = () => {
            connected = false;
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
        {#if entries.length === 0}
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
                    <ChatMessage role={entry.role} content={entry.content} isStreaming={entry.isStreaming || false} />
                {/if}
            {/each}
        {/if}
    </div>

    {#if connecting}
        <div class="status-bar"><span class="dot connecting"></span> Connecting...</div>
    {:else if !connected}
        <div class="status-bar error"><span class="dot disconnected"></span> Disconnected</div>
    {/if}

    <ChatForm
        bind:value={inputValue}
        disabled={disabled}
        isLoading={isLoading}
        placeholder="Send a message to the agent..."
        onSubmit={(msg) => handleSend(msg)}
        onStop={handleStop}
    />
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
    .status-bar {
        padding: 4px 12px;
        font-size: 11px;
        color: var(--color-text-tertiary);
        border-top: 1px solid var(--color-border);
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }
    .status-bar.error { color: var(--color-status-red); }
    .dot { width: 6px; height: 6px; border-radius: 50%; display: inline-block; }
    .dot.connecting { background: var(--color-status-yellow); animation: pulse 1s infinite; }
    .dot.disconnected { background: var(--color-status-red); }
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
</style>
