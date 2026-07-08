<script lang="ts">
    interface Props {
        stats: {
            model_name: string;
            prompt_tokens: number;
            predicted_tokens: number;
            prompt_ms: number;
            predicted_ms: number;
            duration: number;
        };
    }

    let { stats }: Props = $props();

    let genTokens = $derived(stats.predicted_tokens ?? 0);
</script>

<div class="message-stats">
    <span class="stats-model-name">{stats.model_name || 'model'}</span>
    {#if genTokens > 0}
        <span class="stat-separator" aria-hidden="true">·</span>
        <span class="stat-item">{genTokens} tokens</span>
    {/if}
    {#if stats.duration > 0}
        <span class="stat-separator" aria-hidden="true">·</span>
        <span class="stat-item">{(stats.duration / 1000).toFixed(1)}s</span>
    {/if}
</div>

<style>
    .message-stats {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 2px 4px 4px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
    }
    .stats-model-name {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .stat-separator {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
    }
    .stat-item {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
</style>
