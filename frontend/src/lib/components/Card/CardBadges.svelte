<script lang="ts">
    let {
        statusBadge,
        modelBadge,
        diffStats,
    }: {
        statusBadge: { text: string; color: string } | null;
        modelBadge: { text: string | null; provider: string | null; model: string | null; color: string; isAgent: boolean } | null;
        diffStats: { hash: string; files: number; additions: number; deletions: number } | null;
    } = $props();
</script>

{#if statusBadge || modelBadge?.text || diffStats}
    <div class="badges-container">
        {#if statusBadge || modelBadge?.text}
            <div class="badges-row">
                {#if statusBadge}
                    <div class="badge" style="background: {statusBadge.color}11; color: {statusBadge.color}">
                        {statusBadge.text}
                    </div>
                {/if}
                {#if modelBadge?.text}
                    {#if modelBadge.isAgent}
                        <div class="badge model-badge pi-badge" style="background: {modelBadge.color}11; color: {modelBadge.color}">
                            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" style="margin-right: 3px">
                                <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
                                <circle cx="6" cy="9" r="1" fill="currentColor"/>
                                <circle cx="10" cy="9" r="1" fill="currentColor"/>
                            </svg>
                            Pi Coding Agent
                        </div>
                    {:else}
                        <div class="badge model-badge" style="background: {modelBadge.color}11; color: {modelBadge.color}">
                            {#if modelBadge.provider}<span class="badge-provider">{modelBadge.provider}</span><span class="badge-sep"> · </span>{/if}<span class="badge-model">{modelBadge.model}</span>
                        </div>
                    {/if}
                {/if}
            </div>
        {/if}
        {#if diffStats}
            <div class="diff-stats-row">
                <span class="diff-stats">
                    {#if diffStats.files > 0}<span class="diff-files">{diffStats.files} file{diffStats.files !== 1 ? 's' : ''}</span>{/if}
                    {#if diffStats.files > 0 && (diffStats.additions > 0 || diffStats.deletions > 0)}<span class="diff-sep"> · </span>{/if}
                    {#if diffStats.additions > 0}<span class="diff-add">+{diffStats.additions}</span>{/if}
                    {#if diffStats.deletions > 0}<span class="diff-del">-{diffStats.deletions}</span>{/if}
                </span>
            </div>
        {/if}
    </div>
{/if}

<style>
    .badges-container {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .badges-row {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
        margin-top: 8px;
    }
    .badge {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 0.75rem;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        font-weight: 500;
        font-family: inherit;
    }
    .model-badge {
        max-width: 100%;
        overflow: hidden;
    }
    .badge-provider {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        max-width: 100%;
    }
    .badge-model {
        flex-shrink: 0;
    }
    .badge-sep {
        flex-shrink: 0;
    }
    .diff-stats-row {
        display: flex;
        align-items: center;
        gap: 3px;
        padding: 2px 0;
    }
    .diff-stats {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-variant-numeric: tabular-nums;
    }
    .diff-files {
        color: var(--color-text-secondary);
    }
    .diff-add {
        color: var(--color-status-green, #3FB950);
        margin-left: -1px;
    }
    .diff-del {
        color: var(--color-status-red, #F85149);
        margin-left: -1px;
    }
    .diff-sep {
        color: var(--color-text-tertiary);
        opacity: 0.5;
        margin: 0 2px;
    }
</style>
