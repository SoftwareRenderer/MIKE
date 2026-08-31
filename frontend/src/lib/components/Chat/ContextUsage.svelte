<script lang="ts">
    import ContextDonut from './ContextDonut.svelte';
    import { formatTokens, formatCost } from '$lib/utils/format';

    interface Props {
        used?: number;
        limit?: number;
        sessionCost?: number;
        onSummarize?: () => void;
        onSummarizeCancel?: () => void;
        isSummarizing?: boolean;
        disabled?: boolean;
    }

    let { used = 0, limit = 0, sessionCost = 0, onSummarize, onSummarizeCancel, isSummarizing = false, disabled = false }: Props = $props();

    let percentage = $derived(limit > 0 ? Math.round((used / limit) * 100) : 0);
    let show = $derived(used > 0 || sessionCost > 0);

    let tooltip = $derived(
        limit > 0
            ? `${formatTokens(used)} / ${formatTokens(limit)} tokens · ${percentage}%`
            : `${formatTokens(used)} tokens`
    );

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
    <div class="context-usage-group">
        <button
            type="button"
            class="context-usage"
            class:clickable={!!onSummarize && !disabled}
            class:summarizing={isSummarizing}
            disabled={disabled}
            title="{tooltip}{onSummarize && !disabled ? (isSummarizing ? ' · click to cancel' : ' · click to summarize') : ''}"
            onclick={handleClick}
            onkeydown={handleKeyDown}
            aria-label="{tooltip}"
        >
            {#if isSummarizing}
                <span class="spinner"></span>
            {:else}
                <ContextDonut {used} {limit} />
                <span class="usage-text">{formatTokens(used)}</span>
            {/if}
        </button>
        {#if sessionCost > 0}
            <span class="cost-badge" title="Session cost">{formatCost(sessionCost)}</span>
        {/if}
    </div>
{/if}

<style>
    .context-usage-group {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }
    .context-usage {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        box-sizing: border-box;
        height: 26px;
        padding: 0 6px;
        border-radius: 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
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
    .usage-text {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        font-variant-numeric: tabular-nums;
        white-space: nowrap;
        line-height: 1;
        margin-left: 4px;
    }
    .cost-badge {
        display: inline-flex;
        align-items: center;
        padding: 3px 8px;
        border-radius: 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
        flex-shrink: 0;
    }
    .spinner {
        width: 22px;
        height: 22px;
        border: 2px solid var(--color-border);
        border-top-color: var(--color-accent);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
        flex-shrink: 0;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
