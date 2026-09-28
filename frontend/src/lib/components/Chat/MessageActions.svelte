<script lang="ts">
    import { t } from '$lib/i18n';

    interface Props {
        variant: 'user' | 'assistant';
        timestampTooltip?: string;
        timestampDisplay?: string;
        hasContent?: boolean;
        isStreaming?: boolean;
        isOptimistic?: boolean;
        onCopy?: () => void;
        onDelete?: () => void;
        onRegenerate?: () => void;
        onFork?: () => void;
        /** Rewrites this message where it stands — the summarized context, never a chat turn. */
        onEdit?: () => void;
        copied?: boolean;
    }

    let { variant = 'assistant', timestampTooltip, timestampDisplay, hasContent = true, isStreaming = false, isOptimistic = false, onCopy, onDelete, onRegenerate, onFork, onEdit, copied = false }: Props = $props();
</script>

<div class="message-actions" class:user-actions={variant === 'user'} class:assistant-actions={variant === 'assistant'}>
    {#if timestampDisplay}
        <span class="message-timestamp" title={timestampTooltip || undefined}>{timestampDisplay}</span>
    {/if}

    {#if variant === 'user'}
        <button class="action-btn" class:copied={copied} onclick={onCopy} aria-label={t('chat.actions.copy')} title={t('chat.actions.copy')}>
            <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
            </svg>
            <span class="check">✓</span>
        </button>
        {#if onFork}
            <button class="action-btn" onclick={onFork} aria-label={t('chat.actions.forkAria')} title={t('chat.actions.forkTitle')}>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                    <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                </svg>
            </button>
        {/if}
        {#if onDelete}
            <button class="action-btn" onclick={onDelete} aria-label={t('chat.actions.delete')} title={t('chat.actions.delete')}>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
        {/if}
    {:else}
        {#if hasContent}
            <button class="action-btn" class:copied={copied} onclick={onCopy} aria-label={t('chat.actions.copy')} title={t('chat.actions.copy')}>
                <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
                <span class="check">✓</span>
            </button>
        {/if}
        {#if onRegenerate && !isStreaming && !isOptimistic}
            <button class="action-btn" onclick={onRegenerate} aria-label={t('chat.actions.regenerate')} title={t('chat.actions.regenerate')}>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M21 2v6h-6"/>
                    <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                    <path d="M3 22v-6h6"/>
                    <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                </svg>
            </button>
        {/if}
        {#if onEdit}
            <button class="action-btn" onclick={onEdit} aria-label={t('chat.actions.editSummary')} title={t('chat.actions.editSummary')}>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                    <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                </svg>
            </button>
        {/if}
        {#if onDelete && !isStreaming && !isOptimistic}
            <button class="action-btn" onclick={onDelete} aria-label={t('chat.actions.delete')} title={t('chat.actions.delete')}>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
        {/if}
    {/if}
</div>

<style>
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
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        white-space: nowrap;
        margin: 0 4px;
        padding: 4px 0;
        height: 22px;
    }
    .user-actions .message-timestamp {
        margin-left: 0;
    }
    .assistant-actions .message-timestamp {
        margin: 0;
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
        opacity: 1;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .action-btn .icon {
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
        font-size: var(--text-base);
        font-weight: 700;
        line-height: 1;
        transition: opacity 0.2s ease;
        pointer-events: none;
    }
    .action-btn.copied .check {
        opacity: 1;
    }
</style>
