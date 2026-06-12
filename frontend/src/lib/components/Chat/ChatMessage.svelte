<script lang="ts">
    import { marked } from 'marked';

    interface Props {
        role: string;
        content: string;
        reasoningContent?: string;
        isStreaming?: boolean;
        timestamp?: string;
        messageId?: string;
        stats?: { model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number };
        onCopy?: () => void;
        onDelete?: () => void;
        onFork?: (messageId: string, originalContent: string, newContent: string) => void;
        onSend?: (messageId: string, originalContent: string, newContent: string) => void;
        onRegenerate?: () => void;
        isLastUserMessage?: boolean;
        isFirstUserMessage?: boolean;
    }

    let { role, content, reasoningContent, isStreaming = false, timestamp, messageId, stats, onCopy, onDelete, onFork, onSend, onRegenerate, isLastUserMessage = false, isFirstUserMessage = false }: Props = $props();

    let reasoningExpanded = $state(false);

    function formatTime(isoStr: string): string {
        const date = new Date(isoStr);
        if (isNaN(date.getTime())) return '';
        return date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
    }

    let isEditing = $state(false);
    let editContent = $state(content);
    let textareaEl: HTMLTextAreaElement | null = $state(null);
    let statsView = $state<'reading' | 'generation'>('generation');
    let copied = $state(false);

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

    async function handleCopy() {
        try {
            await navigator.clipboard.writeText(content);
            copied = true;
            setTimeout(() => copied = false, 2000);
        } catch (err) {
            // Fallback: create a temporary textarea and copy
            try {
                const textarea = document.createElement('textarea');
                textarea.value = content;
                textarea.style.position = 'fixed';
                textarea.style.opacity = '0';
                document.body.appendChild(textarea);
                textarea.select();
                document.execCommand('copy');
                document.body.removeChild(textarea);
                copied = true;
                setTimeout(() => copied = false, 2000);
            } catch (_) {
                // Both methods failed
                console.error('Failed to copy to clipboard:', err);
            }
        }
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
        if (editContent.trim() && messageId) {
            if (isFirstUserMessage) {
                onFork?.(messageId, content, editContent.trim());
            } else if (isLastUserMessage && onSend) {
                onSend(messageId, content, editContent.trim());
            } else {
                onFork?.(messageId, content, editContent.trim());
            }
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
    data-message-id={messageId}
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
                        <button class="btn-edit-cancel" onclick={cancelEdit} title="Cancel editing">Cancel</button>
                        <button class="btn-edit-submit" onclick={submitEdit} title="Save changes">
                            {#if isFirstUserMessage}
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <polyline points="20 6 9 17 4 12"/>
                                </svg>
                                Save
                            {:else if isLastUserMessage}
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <line x1="22" y1="2" x2="11" y2="13"/>
                                    <polygon points="22 2 15 22 11 13 2 9 22 2"/>
                                </svg>
                                Send
                            {:else}
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <circle cx="18" cy="18" r="3"/>
                                    <circle cx="6" cy="6" r="3"/>
                                    <path d="M13 6h3a2 2 0 0 1 2 2v7"/>
                                    <line x1="6" y1="9" x2="6" y2="21"/>
                                </svg>
                                Fork
                            {/if}
                        </button>
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
                    <button class="action-btn" class:copied={copied} onclick={handleCopy} aria-label="Copy message" title="Copy message">
                        <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                        </svg>
                        <span class="check">✓</span>
                    </button>
                    {#if onFork}
                        <button class="action-btn" onclick={startEdit} aria-label="Fork conversation from this message" title="Edit / fork conversation">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                                <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                            </svg>
                        </button>
                    {/if}
                    {#if onDelete}
                        <button class="action-btn" onclick={handleDelete} aria-label="Delete message and all following messages" title="Delete message and all following messages">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                        </button>
                    {/if}
                </div>
            {/if}
        </div>
    {:else if role === 'assistant'}
        <div class="message-wrapper assistant-wrapper">
            {#if reasoningContent}
                <div class="reasoning-block" class:expanded={reasoningExpanded}>
                    <button class="reasoning-toggle" onclick={() => reasoningExpanded = !reasoningExpanded} aria-expanded={reasoningExpanded} title={reasoningExpanded ? 'Hide reasoning' : 'Show reasoning'}>
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <path d="M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5L12 3z"/>
                        </svg>
                        {reasoningExpanded ? 'Hide' : 'Show'} reasoning
                    </button>
                    {#if reasoningExpanded}
                        <div class="reasoning-content">{reasoningContent}</div>
                    {/if}
                </div>
            {/if}
            <div class="message-bubble assistant-bubble">
                <div class="message-text" class:streaming={isStreaming}>
                    {@html renderedHtml}
                    {#if isStreaming}
                        <span class="cursor-blink"></span>
                    {/if}
                </div>
            </div>
            {#if stats}
                <div class="message-stats">
                    <div class="stats-row">
                        <span class="stats-model-name">{stats.model_name || 'model'}</span>
                        <span class="stat-separator">·</span>
                        <div class="stats-toggle" role="group" aria-label="Statistics view">
                            <button
                                type="button"
                                class="toggle-btn {statsView === 'reading' ? 'active' : ''}"
                                data-view="reading"
                                onclick={() => statsView = 'reading'}
                                aria-label="Reading stats (prompt processing)"
                                aria-pressed={statsView === 'reading'}
                                title="Reading stats (prompt processing)"
                            >
                                <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H20v20H6.5a2.5 2.5 0 0 1 0-5H20"/>
                                </svg>
                            </button>
                            <button
                                type="button"
                                class="toggle-btn {statsView === 'generation' ? 'active' : ''}"
                                data-view="generation"
                                onclick={() => statsView = 'generation'}
                                aria-label="Generation stats (token output)"
                                aria-pressed={statsView === 'generation'}
                                title="Generation stats (token output)"
                            >
                                <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
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
                    <button class="action-btn" class:copied={copied} onclick={handleCopy} aria-label="Copy message" title="Copy message">
                        <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                        </svg>
                        <span class="check">✓</span>
                    </button>
                    {#if onRegenerate && !isStreaming}
                        <button class="action-btn" onclick={onRegenerate} aria-label="Regenerate response" title="Regenerate response">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <path d="M21 2v6h-6"/>
                                <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                                <path d="M3 22v-6h6"/>
                                <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                            </svg>
                        </button>
                    {/if}
                    {#if onDelete}
                        <button class="action-btn" onclick={handleDelete} aria-label="Delete message and all following messages" title="Delete message and all following messages">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
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
    .assistant .message-bubble {
        max-width: 100%;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        padding: 10px 14px;
    }
    .message-text {
        font-size: 1rem;
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
    .reasoning-block {
        margin-bottom: 6px;
    }
    .reasoning-toggle {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        cursor: pointer;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        transition: all 0.15s ease;
    }
    .reasoning-toggle:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .reasoning-content {
        margin-top: 4px;
        padding: 8px 12px;
        background: var(--color-surface-0);
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        line-height: 1.5rem;
        color: var(--color-text-secondary);
        white-space: pre-wrap;
        font-style: italic;
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
        margin: 0.75rem 0;
        line-height: 1.7;
    }
    .assistant .message-text :global(p:first-child) {
        margin-top: 0;
    }
    .assistant .message-text :global(p:last-child) {
        margin-bottom: 0;
    }
    .assistant .message-text :global(h1),
    .assistant .message-text :global(h2),
    .assistant .message-text :global(h3),
    .assistant .message-text :global(h4),
    .assistant .message-text :global(h5),
    .assistant .message-text :global(h6) {
        margin: 1rem 0 0.5rem;
        font-weight: 600;
        line-height: 1.4;
        color: var(--color-text-primary);
    }
    .assistant .message-text :global(h1) { font-size: 1.43rem; }
    .assistant .message-text :global(h2) { font-size: 1.29rem; }
    .assistant .message-text :global(h3) { font-size: 1.14rem; }
    .assistant .message-text :global(h4) { font-size: 1.07rem; }
    .assistant .message-text :global(h5) { font-size: 1rem; }
    .assistant .message-text :global(h6) { font-size: 1rem; color: var(--color-text-secondary); }
    .assistant .message-text :global(h1:first-child),
    .assistant .message-text :global(h2:first-child),
    .assistant .message-text :global(h3:first-child) {
        margin-top: 0;
    }
    .assistant .message-text :global(hr) {
        border: none;
        border-top: 1px solid var(--color-border);
        margin: 1.25rem 0;
    }
    .assistant .message-text :global(a) {
        color: var(--color-accent);
        text-decoration: underline;
        text-underline-offset: 2px;
    }
    .assistant .message-text :global(a:hover) {
        color: var(--color-accent-hover);
    }
    .assistant .message-text :global(ul),
    .assistant .message-text :global(ol) {
        margin: 0.75rem 0;
        padding-left: 1.5rem;
        white-space: normal;
        line-height: 1.7;
    }
    .assistant .message-text :global(ul:first-child),
    .assistant .message-text :global(ol:first-child) {
        margin-top: 0;
    }
    .assistant .message-text :global(ul:last-child),
    .assistant .message-text :global(ol:last-child) {
        margin-bottom: 0;
    }
    .assistant .message-text :global(li) {
        margin: 0.125rem 0;
        line-height: 1.7;
        white-space: normal;
    }
    .assistant .message-text :global(li > p) {
        margin: 0.25rem 0;
        line-height: 1.7;
    }
    .assistant .message-text :global(blockquote) {
        border-left: 3px solid var(--color-border);
        padding-left: 12px;
        margin: 8px 0;
        color: var(--color-text-secondary);
    }
    .message-stats {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 2px 0 4px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
    }
    .stats-model-name {
        font-size: 0.75rem;
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
        align-items: center;
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
        display: flex;
        align-items: center;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        opacity: 0.6;
        white-space: nowrap;
        margin: 0 4px;
        padding: 4px 0;
        height: 22px;
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
        position: relative;
        width: 22px;
        height: 22px;
        flex-shrink: 0;
    }
    .action-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .action-btn .icon {
        opacity: 1;
        transition: opacity 0.2s ease;
    }
    .action-btn.copied .icon {
        opacity: 0;
        pointer-events: none;
        position: absolute;
    }
    .action-btn .check {
        position: absolute;
        opacity: 0;
        font-size: 14px;
        font-weight: 700;
        line-height: 1;
        transition: opacity 0.2s ease;
        pointer-events: none;
    }
    .action-btn.copied .check {
        opacity: 1;
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
    }
    .edit-form textarea:focus-visible {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 2px var(--color-accent)22;
        outline: none;
    }
    .edit-actions {
        display: flex;
        gap: 6px;
        justify-content: flex-end;
    }
    .btn-edit-cancel {
        display: inline-flex;
        align-items: center;
        gap: 4px;
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
        display: inline-flex;
        align-items: center;
        gap: 4px;
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
