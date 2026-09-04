<script lang="ts">
    import { t } from '$lib/i18n';

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
    <span class="stats-model-name">{stats.model_name || t('chat.stats.modelFallback')}</span>
    {#if genTokens > 0}
        <span class="stat-separator" aria-hidden="true">·</span>
        <span class="stat-item">{t('chat.stats.tokens', { count: genTokens })}</span>
    {/if}
    {#if stats.duration > 0}
        <span class="stat-separator" aria-hidden="true">·</span>
        <span class="stat-item">{t('chat.stats.seconds', { seconds: (stats.duration / 1000).toFixed(1) })}</span>
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
