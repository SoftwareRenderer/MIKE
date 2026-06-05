<script lang="ts">
    import { marked } from 'marked';

    interface Props {
        role: string;
        content: string;
        isStreaming?: boolean;
        timestamp?: string;
        stats?: { prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number };
        onCopy?: () => void;
        onDelete?: () => void;
        onEdit?: (newContent: string) => void;
        onRegenerate?: () => void;
    }

    let { role, content, isStreaming = false, timestamp, stats, onCopy, onDelete, onEdit, onRegenerate }: Props = $props();

    function formatTime(isoStr: string): string {
        const date = new Date(isoStr);
        if (isNaN(date.getTime())) return '';
        return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    }

    let isEditing = $state(false);
    let editContent = $state(content);
    let textareaEl: HTMLTextAreaElement | null = $state(null);
    let statsView = $state<'reading' | 'generation'>('generation');

    // Derived stats for generation view
    let genTokens = $derived(stats?.predicted_tokens ?? 0);
    let genTime = $derived(stats ? (stats.predicted_ms / 1000) : 0);
    let genSpeed = $derived(genTime > 0 ? Math.round(genTokens / genTime) : 0);

    // Derived stats for reading view
    let readTokens = $derived(stats?.prompt_tokens ?? 0);
    let readTime = $derived(stats ? (stats.prompt_ms / 1000) : 0);
    let readSpeed = $derived(readTime > 0 ? Math.round(readTokens / readTime) : 0);

    let renderedHtml = $derived(
        (role === 'assistant' || role === 'system')
            ? (marked.parse(content || '', { async: false }) as string)
            : ''
    );

    let userHtml = $derived(
        role === 'user'
            ? content.replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>')
            : ''
    );

    function handleCopy() {
        navigator.clipboard.writeText(content);
        onCopy?.();
    }

    function handleDelete() {
        onDelete?.();
    }

    function startEdit() {
        editContent = content;
        isEditing = true;
        setTimeout(() => {
            if (textareaEl) {
                textareaEl.focus();
                textareaEl.select();
            }
        }, 50);
    }

    function cancelEdit() {
        isEditing = false;
        editContent = content;
    }

    function submitEdit() {
        if (editContent.trim()) {
            onEdit?.(editContent.trim());
            isEditing = false;
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            submitEdit();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            cancelEdit();
        }
    }
</script>

<div 
    class="chat-message" 
    class:user={role === 'user'} 
    class:assistant={role === 'assistant'} 
    class:system={role === 'system'}
>
    {#if role === 'user'}
        <div class="message-wrapper user-wrapper">
            {#if isEditing}
                <div class="edit-form">
                    <textarea
                        bind:this={textareaEl}
                        bind:value={editContent}
                        onkeydown={handleKeydown}
                        rows="3"
                    ></textarea>
                    <div class="edit-actions">
                        <button class="btn-edit-cancel" onclick={cancelEdit}>Cancel</button>
                        <button class="btn-edit-submit" onclick={submitEdit}>Send</button>
                    </div>
                </div>
            {:else}
                <div class="message-bubble">
                    <div class="message-text">{@html userHtml}</div>
                </div>
                <div class="message-actions user-actions">
                    {#if timestamp}
                        <span class="message-timestamp">{formatTime(timestamp)}</span>
                    {/if}
                    <button class="action-btn" onclick={handleCopy} title="Copy">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                        </svg>
                    </button>
                    {#if onEdit}
                        <button class="action-btn" onclick={startEdit} title="Edit">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                                <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                            </svg>
                        </button>
                    {/if}
                    {#if onDelete}
                        <button class="action-btn" onclick={handleDelete} title="Delete">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <polyline points="3 6 5 6 21 6"/>
                                <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
                            </svg>
                        </button>
                    {/if}
                </div>
            {/if}
        </div>
    {:else if role === 'assistant'}
        <div class="message-wrapper assistant-wrapper">
            <div class="message-text" class:streaming={isStreaming}>
                {@html renderedHtml}
                {#if isStreaming}
                    <span class="cursor-blink"></span>
                {/if}
            </div>
            {#if stats}
                <div class="message-stats">
                    <span class="stats-model-name">{stats.model_name || 'model'}</span>
                    <div class="stats-row">
                        <div class="stats-toggle">
                            <button
                                type="button"
                                class="toggle-btn {statsView === 'reading' ? 'active' : ''}"
                                data-view="reading"
                                onclick={() => statsView = 'reading'}
                                title="Reading (prompt processing)"
                            >
                                <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                    <path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H20v20H6.5a2.5 2.5 0 0 1 0-5H20"/>
                                </svg>
                            </button>
                            <button
                                type="button"
                                class="toggle-btn {statsView === 'generation' ? 'active' : ''}"
                                data-view="generation"
                                onclick={() => statsView = 'generation'}
                                title="Generation (token output)"
                            >
                                <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                    <path d="M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5L12 3z"/>
                                    <path d="M19 15l.5 1.5L21 17l-1.5.5L19 19l-.5-1.5L17 17l1.5-.5L19 15z"/>
                                    <path d="M5 19l.5 1.5L7 21l-1.5.5L5 23l-.5-1.5L3 21l1.5-.5L5 19z"/>
                                </svg>
                            </button>
                        </div>
                        <div class="stat-values">
                            {#if statsView === 'generation'}
                                <span class="stat-item">{genTokens} tokens</span>
                                <span class="stat-separator">·</span>
                                <span class="stat-item">{genTime.toFixed(1)}s</span>
                                <span class="stat-separator">·</span>
                                <span class="stat-item">{genSpeed} tok/s</span>
                            {:else}
                                <span class="stat-item">{readTokens} tokens</span>
                                <span class="stat-separator">·</span>
                                <span class="stat-item">{readTime.toFixed(1)}s</span>
                                <span class="stat-separator">·</span>
                                <span class="stat-item">{readSpeed} tok/s</span>
                            {/if}
                        </div>
                    </div>
                </div>
            {/if}
            <div class="message-actions assistant-actions">
                    {#if timestamp}
                        <span class="message-timestamp">{formatTime(timestamp)}</span>
                    {/if}
                    <button class="action-btn" onclick={handleCopy} title="Copy">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                        </svg>
                    </button>
                    {#if onRegenerate && !isStreaming}
                        <button class="action-btn" onclick={onRegenerate} title="Regenerate">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <path d="M21 2v6h-6"/>
                                <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                                <path d="M3 22v-6h6"/>
                                <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                            </svg>
                        </button>
                    {/if}
                    {#if onDelete}
                        <button class="action-btn" onclick={handleDelete} title="Delete">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <polyline points="3 6 5 6 21 6"/>
                                <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
                            </svg>
                        </button>
                    {/if}
                </div>
        </div>
    {:else}
        <div class="message-wrapper system-wrapper">
            <div class="message-text system-text">{content}</div>
        </div>
    {/if}
</div>

<style>
    .chat-message {
        padding: 8px 0;
    }
    .message-wrapper {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .user-wrapper {
        align-items: flex-end;
    }
    .assistant-wrapper {
        align-items: flex-start;
    }
    .system-wrapper {
        align-items: flex-start;
    }
    .message-bubble {
        max-width: 80%;
        background: color-mix(in srgb, var(--color-accent) 8%, transparent);
        border-radius: 1.125rem;
        padding: 8px 14px;
    }
    .message-text {
        font-size: 0.875rem;
        line-height: 1.875rem;
        word-break: break-word;
        white-space: pre-wrap;
    }
    .user .message-text {
        color: var(--color-text-primary);
    }
    .assistant .message-text {
        color: var(--color-text-primary);
        white-space: normal;
    }
    .assistant .message-text.streaming {
        border-left: 2px solid var(--color-accent);
        padding-left: 12px;
    }
    .system-text {
        color: var(--color-text-tertiary);
        font-style: italic;
        font-size: 0.8125rem;
        line-height: 1.5rem;
    }
    .assistant .message-text :global(pre) {
        background: var(--color-surface-0);
        padding: 10px 14px;
        border-radius: var(--radius-md);
        overflow-x: auto;
        margin: 8px 0;
        font-size: 0.8125rem;
        line-height: 1.5rem;
    }
    .assistant .message-text :global(code) {
        background: var(--color-surface-0);
        padding: 2px 6px;
        border-radius: 4px;
        font-size: 0.8125rem;
        line-height: 1.25rem;
        font-family: var(--font-mono);
    }
    .assistant .message-text :global(pre code) {
        background: none;
        padding: 0;
    }
    .assistant .message-text :global(p) {
        margin: 0;
        line-height: 1.25;
    }
    .assistant .message-text :global(p:last-child) {
        margin-bottom: 0;
    }
    .assistant .message-text :global(ul),
    .assistant .message-text :global(ol) {
        margin: 0.5rem 0;
        padding-left: 1.5rem;
        white-space: normal;
        line-height: 1.25;
    }
    .assistant .message-text :global(li) {
        margin: 0;
        line-height: 1.25;
        white-space: normal;
    }
    .assistant .message-text :global(li > p) {
        margin: 0;
    }
    .assistant .message-text :global(blockquote) {
        border-left: 3px solid var(--color-border);
        padding-left: 12px;
        margin: 8px 0;
        color: var(--color-text-secondary);
    }
    .message-stats {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 2px 0 4px;
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
    }
    .stats-model-name {
        font-size: 0.625rem;
        font-weight: 600;
        color: var(--color-text-tertiary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
        opacity: 0.7;
    }
    .stats-row {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .stats-toggle {
        display: inline-flex;
        align-items: center;
        border-radius: 3px;
        background: var(--color-text-tertiary);
        background: color-mix(in srgb, var(--color-text-tertiary) 15%, transparent);
        padding: 2px;
        gap: 0;
    }
    .toggle-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 16px;
        height: 16px;
        border: none;
        background: none;
        border-radius: 2px;
        color: var(--color-text-tertiary);
        cursor: pointer;
        transition: all 0.15s ease;
        padding: 0;
    }
    .toggle-btn.active {
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        box-shadow: 0 1px 2px rgba(0, 0, 0, 0.15);
    }
    .toggle-btn:hover:not(.active) {
        color: var(--color-text-primary);
    }
    .stat-values {
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .stat-separator {
        opacity: 0.5;
    }
    .message-actions {
        display: flex;
        gap: 2px;
        padding: 2px 4px;
    }
    .user-actions {
        justify-content: flex-end;
    }
    .assistant-actions {
        justify-content: flex-start;
    }
    .message-timestamp {
        font-size: 0.625rem;
        color: var(--color-text-tertiary);
        opacity: 0.6;
        white-space: nowrap;
        margin: 0 4px;
    }
    .user-actions .message-timestamp {
        margin-left: 0;
    }
    .assistant-actions .message-timestamp {
        margin-right: 0;
    }
    .action-btn {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 4px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        transition: all 0.15s ease;
    }
    .action-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .edit-form {
        display: flex;
        flex-direction: column;
        gap: 6px;
        max-width: 80%;
        width: 100%;
    }
    .edit-form textarea {
        width: 100%;
        min-height: 60px;
        padding: 8px 12px;
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        line-height: 1.5rem;
        font-family: inherit;
        resize: vertical;
        outline: none;
    }
    .edit-form textarea:focus {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 2px var(--color-accent)22;
    }
    .edit-actions {
        display: flex;
        gap: 6px;
        justify-content: flex-end;
    }
    .btn-edit-cancel {
        padding: 4px 12px;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        cursor: pointer;
    }
    .btn-edit-cancel:hover {
        background: var(--color-surface-4);
    }
    .btn-edit-submit {
        padding: 4px 12px;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        background: var(--color-accent);
        color: white;
        cursor: pointer;
    }
    .btn-edit-submit:hover {
        opacity: 0.85;
    }
    .cursor-blink {
        display: inline-block;
        width: 2px;
        height: 14px;
        background: var(--color-accent);
        margin-left: 2px;
        vertical-align: text-bottom;
        animation: blink 0.8s step-end infinite;
    }
    @keyframes blink {
        0%, 100% { opacity: 1; }
        50% { opacity: 0; }
    }
</style>
