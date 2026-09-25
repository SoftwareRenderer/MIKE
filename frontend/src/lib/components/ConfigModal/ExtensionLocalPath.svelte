<script lang="ts">
    import { t } from '$lib/i18n';

    /** Where the server laid a local extension's files down. The card decides when this is worth showing. */
    let { path }: { path?: string } = $props();
</script>

<div class="local-path">
    <span class="local-path-label">{t('settings.extensions.path')}</span>
    {#if path}
        <code class="local-path-code" title={path}>{path}</code>
    {:else}
        <span class="local-path-unknown">{t('settings.extensions.pathUnavailable')}</span>
    {/if}
</div>

<style>
    .local-path {
        display: flex;
        align-items: baseline;
        gap: 0.5rem;
        margin: 0 0 0.5rem;
        padding: 0.35rem 0.6rem;
        border-radius: 4px;
        background: color-mix(in srgb, var(--color-status-yellow) 10%, var(--color-bg-card));
        border: 1px solid color-mix(in srgb, var(--color-status-yellow) 35%, transparent);
        font-size: 0.75rem;
        overflow: hidden;
    }

    .local-path-label {
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        font-size: 0.65rem;
        color: color-mix(in srgb, var(--color-text) 70%, transparent);
        flex-shrink: 0;
    }

    .local-path-code {
        font-family: var(--font-mono, monospace);
        font-size: 0.75rem;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        color: var(--color-text);
    }

    .local-path-unknown {
        color: color-mix(in srgb, var(--color-text) 55%, transparent);
        font-style: italic;
    }
</style>
