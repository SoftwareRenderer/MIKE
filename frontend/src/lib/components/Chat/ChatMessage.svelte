<script lang="ts">
    import { marked } from 'marked';

    interface Props {
        role: string;
        content: string;
        isStreaming?: boolean;
    }

    let { role, content, isStreaming = false }: Props = $props();

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
</script>

<div class="chat-message" class:user={role === 'user'} class:assistant={role === 'assistant'} class:system={role === 'system'}>
    <div class="message-avatar">
        {#if role === 'user'}
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                <circle cx="8" cy="5" r="2.5" fill="currentColor"/>
                <path d="M4 14c0-2.2 1.8-4 4-4s4 1.8 4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
            </svg>
        {:else}
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                <path d="M8 1l2 3.5H13l-1 3.5 1 3.5H10L8 15l-2-3.5H4l1-3.5L4 4.5h3L8 1z" fill="currentColor" opacity="0.9"/>
            </svg>
        {/if}
    </div>
    <div class="message-bubble">
        {#if role === 'user'}
            <div class="message-text user-text">{@html userHtml}</div>
        {:else if role === 'assistant'}
            <div class="message-text assistant-text" class:streaming={isStreaming}>
                {@html renderedHtml}
                {#if isStreaming}
                    <span class="cursor-blink"></span>
                {/if}
            </div>
        {:else}
            <div class="message-text system-text">{content}</div>
        {/if}
    </div>
</div>

<style>
    .chat-message {
        display: flex;
        gap: 10px;
        padding: 8px 0;
        align-items: flex-start;
    }
    .chat-message.user { flex-direction: row-reverse; }
    .message-avatar {
        flex-shrink: 0;
        width: 28px;
        height: 28px;
        border-radius: 50%;
        display: flex;
        align-items: center;
        justify-content: center;
        margin-top: 2px;
    }
    .user .message-avatar { background: var(--color-accent)22; color: var(--color-accent); }
    .assistant .message-avatar { background: var(--color-status-purple)22; color: var(--color-status-purple); }
    .system .message-avatar { background: var(--color-surface-3); color: var(--color-text-tertiary); }
    .message-bubble { max-width: 85%; min-width: 0; }
    .user .message-bubble { text-align: right; }
    .message-text {
        display: inline-block;
        padding: 8px 12px;
        border-radius: var(--radius-md);
        font-size: 13px;
        line-height: 1.55;
        word-break: break-word;
    }
    .user-text {
        background: var(--color-accent)18;
        color: var(--color-text-primary);
        border-bottom-right-radius: 4px;
    }
    .assistant-text {
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        border-bottom-left-radius: 4px;
        text-align: left;
    }
    .assistant-text.streaming { border-left: 2px solid var(--color-accent); }
    .system-text {
        background: var(--color-surface-1);
        color: var(--color-text-tertiary);
        font-style: italic;
        font-size: 12px;
    }
    .assistant-text :global(pre) {
        background: var(--color-surface-0);
        padding: 8px 12px;
        border-radius: var(--radius-sm);
        overflow-x: auto;
        margin: 6px 0;
        font-size: 12px;
    }
    .assistant-text :global(code) {
        background: var(--color-surface-0);
        padding: 1px 4px;
        border-radius: 3px;
        font-size: 12px;
        font-family: var(--font-mono);
    }
    .assistant-text :global(pre code) { background: none; padding: 0; }
    .assistant-text :global(p) { margin: 0 0 8px; }
    .assistant-text :global(p:last-child) { margin-bottom: 0; }
    .assistant-text :global(ul),
    .assistant-text :global(ol) { margin: 4px 0; padding-left: 20px; }
    .assistant-text :global(li) { margin: 2px 0; }
    .assistant-text :global(blockquote) {
        border-left: 3px solid var(--color-border);
        padding-left: 12px;
        margin: 6px 0;
        color: var(--color-text-secondary);
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
