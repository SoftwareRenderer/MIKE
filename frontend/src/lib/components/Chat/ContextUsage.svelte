<script lang="ts">
    interface Props {
        used?: number;
        limit?: number;
    }

    let { used = 0, limit = 0 }: Props = $props();

    let percentage = $derived(limit > 0 ? Math.round((used / limit) * 100) : 0);
    let isWarning = $derived(percentage >= 60 && percentage < 80);
    let isDanger = $derived(percentage >= 80);
    let show = $derived(used > 0);

    function formatTokens(n: number): string {
        if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
        if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
        return n.toString();
    }

    function getBarColor(): string {
        if (isDanger) return 'var(--color-status-red)';
        if (isWarning) return 'var(--color-status-amber, #f59e0b)';
        return 'var(--color-status-green, #3fb950)';
    }
</script>

{#if show}
    <div class="context-usage" title="{used} tokens used{limit > 0 ? ' / ' + limit + ' limit' : ''}">
        {#if limit > 0}
            <div class="usage-bar">
                <div class="usage-fill" style="width:{percentage}%;background:{getBarColor()}"></div>
            </div>
        {/if}
        <span class="usage-text" class:warning={isWarning} class:danger={isDanger}>
            {#if limit > 0}
                {formatTokens(used)} / {formatTokens(limit)} · {percentage}%
            {:else}
                {formatTokens(used)} tokens
            {/if}
        </span>
    </div>
{/if}

<style>
    .context-usage {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 2px 8px;
        border-radius: 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        font-size: 0.7rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
        flex-shrink: 0;
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
        color: var(--color-status-amber, #f59e0b);
    }
    .usage-text.danger {
        color: var(--color-status-red);
    }
</style>
