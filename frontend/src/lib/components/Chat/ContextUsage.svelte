<script lang="ts">
    import ContextDonut from './ContextDonut.svelte';
    import Spinner from '$lib/components/Spinner.svelte';
    import { formatTokens, formatCost } from '$lib/utils/format';
    import { dialogStore } from '$lib/stores';
    import { t } from '$lib/i18n';

    interface Props {
        used?: number;
        limit?: number;
        sessionCost?: number;
        onSummarize?: () => void;
        onSummarizeCancel?: () => void;
        isSummarizing?: boolean;
    }

    let { used = 0, limit = 0, sessionCost = 0, onSummarize, onSummarizeCancel, isSummarizing = false }: Props = $props();

    let percentage = $derived(limit > 0 ? Math.round((used / limit) * 100) : 0);
    let show = $derived(used > 0 || sessionCost > 0);

    let tooltip = $derived(
        limit > 0
            ? t('chat.context.usage', { used: formatTokens(used), limit: formatTokens(limit), percent: percentage })
            : t('chat.context.usedTokens', { used: formatTokens(used) })
    );

    // Cancelling throws away the summary being written, so the click asks first — on the keyboard
    // path too. A spinner nothing can cancel has nothing to confirm.
    async function requestCancel() {
        if (!onSummarizeCancel) return;
        const confirmed = await dialogStore.showConfirm({
            title: t('chat.compress.cancelTitle'),
            message: t('chat.compress.cancelBody'),
            confirmText: t('chat.compress.cancelConfirm'),
            cancelText: t('chat.compress.cancelKeep'),
        });
        if (confirmed) onSummarizeCancel();
    }

    async function handleClick() {
        if (!onSummarize) return;
        if (isSummarizing) {
            await requestCancel();
            return;
        }
        await onSummarize();
    }

    function handleKeyDown(e: KeyboardEvent) {
        if (isSummarizing && onSummarizeCancel && (e.key === 'Enter' || e.key === ' ')) {
            e.preventDefault();
            void requestCancel();
        } else if (!isSummarizing && onSummarize && (e.key === 'Enter' || e.key === ' ')) {
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
            class:clickable={!!onSummarize}
            class:summarizing={isSummarizing}
            title="{tooltip}{onSummarize ? (isSummarizing ? t('chat.context.clickToCancel') : t('chat.context.clickToSummarize')) : ''}"
            onclick={handleClick}
            onkeydown={handleKeyDown}
            aria-label="{tooltip}"
        >
            {#if isSummarizing}
                <Spinner size={20} decorative />
            {:else}
                <ContextDonut {used} {limit} />
                <span class="usage-text">{formatTokens(used)}</span>
            {/if}
        </button>
        {#if sessionCost > 0}
            <span class="cost-badge" title={t('chat.context.sessionCost')}>{formatCost(sessionCost)}</span>
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
        border-radius: var(--radius-lg);
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
    .context-usage.clickable:hover {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }
    .context-usage.summarizing {
        opacity: 0.7;
        cursor: pointer;
    }
    .usage-text {
        font-size: var(--text-xs);
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
        border-radius: var(--radius-lg);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        white-space: nowrap;
        flex-shrink: 0;
    }
</style>
