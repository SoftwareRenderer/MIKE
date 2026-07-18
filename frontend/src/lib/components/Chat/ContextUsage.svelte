<script lang="ts">
    interface Props {
        used?: number;
        limit?: number;
        onSummarize?: () => void;
        onSummarizeCancel?: () => void;
        isSummarizing?: boolean;
        disabled?: boolean;
    }

    let { used = 0, limit = 0, onSummarize, onSummarizeCancel, isSummarizing = false, disabled = false }: Props = $props();

    let percentage = $derived(limit > 0 ? Math.round((used / limit) * 100) : 0);
    let isWarning = $derived(percentage > 70 && percentage <= 80);
    let isOrange = $derived(percentage > 80 && percentage < 90);
    let isDanger = $derived(percentage >= 90);
    let show = $derived(used > 0);

    function formatTokens(n: number): string {
        if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
        if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
        return n.toString();
    }

    function getBarColor(): string {
        if (isDanger) return 'var(--color-status-red)';
        if (isOrange) return '#DB5400';
        if (isWarning) return 'var(--color-status-amber, #f59e0b)';
        return 'var(--color-status-green, #3fb950)';
    }

    async function handleClick() {
        if (!onSummarize) return;
        if (isSummarizing) {
            onSummarizeCancel?.();
            return;
        }
        await onSummarize();
    }

    function handleKeyDown(e: KeyboardEvent) {
        if (isSummarizing && onSummarizeCancel && (e.key === 'Enter' || e.key === ' ')) {
            e.preventDefault();
            onSummarizeCancel();
        } else if (!isSummarizing && !disabled && onSummarize && (e.key === 'Enter' || e.key === ' ')) {
            e.preventDefault();
            onSummarize();
        }
    }
</script>

{#if show}
    <button
        type="button"
        class="context-usage"
        class:clickable={!!onSummarize && !disabled}
        class:summarizing={isSummarizing}
        disabled={disabled}
        title="{used} tokens used{limit > 0 ? ' / ' + limit + ' limit' : ''}{onSummarize && !disabled ? (isSummarizing ? '\nClick to cancel' : '\nClick to summarize context') : ''}"
        onclick={handleClick}
        onkeydown={handleKeyDown}
    >
        {#if isSummarizing}
            <span class="spinner"></span>
        {:else if limit > 0}
            <div class="usage-bar">
                <div class="usage-fill" style="width:{percentage}%;background:{getBarColor()}"></div>
            </div>
        {/if}
        <span class="usage-text" class:warning={isWarning || isOrange} class:danger={isDanger}>
            {#if isSummarizing}
                Summarizing...
                <span class="cancel-icon" aria-hidden="true">✕</span>
            {:else if limit > 0}
                {formatTokens(used)} / {formatTokens(limit)} · {percentage}%
            {:else}
                {formatTokens(used)} tokens
            {/if}
        </span>
    </button>
{/if}

<style>
    .context-usage {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 3px 8px;
        border-radius: 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
        flex-shrink: 0;
        cursor: default;
        transition: background 0.15s ease, border-color 0.15s ease;
    }
    .context-usage.clickable {
        cursor: pointer;
    }
    .context-usage.clickable:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }
    .context-usage.summarizing {
        opacity: 0.7;
        cursor: pointer;
    }
    .usage-bar {
        width: 48px;
        height: 4px;
        border-radius: 2px;
        background: var(--color-surface-3);
        overflow: hidden;
        flex-shrink: 0;
    }
    .usage-fill {
        height: 100%;
        border-radius: 2px;
        transition: width 0.3s ease, background 0.3s ease;
    }
    .usage-text {
        transition: color 0.3s ease;
    }
    .usage-text.warning {
        color: #DB5400;
    }
    .usage-text.danger {
        color: var(--color-status-red);
    }
    .cancel-icon {
        margin-left: 4px;
        color: var(--color-status-red);
        font-size: 0.85rem;
        font-weight: bold;
        line-height: 1;
    }
    .spinner {
        width: 10px;
        height: 10px;
        border: 1.5px solid var(--color-border);
        border-top-color: var(--color-accent);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
        flex-shrink: 0;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
