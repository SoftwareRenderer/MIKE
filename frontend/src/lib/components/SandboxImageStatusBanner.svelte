<script lang="ts">
    import { fade } from 'svelte/transition';
    import { sandboxImageStatus } from '$lib/services/sandbox-image-status.svelte';
    import { sandboxImageLabel, sandboxImageVisible } from '$lib/utils/sandbox-image-status';
    import { t } from '$lib/i18n';

    /**
     * Progress for a project's sandbox image being built (from its Dockerfile) or pulled.
     *
     * The file explorer opens a project by asking its sandbox to list files, and that request does
     * not come back until the image exists — which for a project with a Dockerfile can mean a build
     * nobody asked for and no way to see it. This banner is that visibility: the phase, the image,
     * and the tail of podman's output, which is where a failed build says why.
     */
    let state = $derived(sandboxImageStatus.state);
    let label = $derived(sandboxImageLabel(state));
    let failed = $derived(state.phase === 'failed');
</script>

{#if sandboxImageVisible(state)}
    <div
        class="sandbox-image-status"
        class:failed
        transition:fade={{ duration: 150 }}
        role={failed ? 'alert' : 'status'}
        aria-live="polite"
    >
        <div class="sandbox-image-status-head">
            {#if !failed}
                <span class="sandbox-image-status-spin" aria-hidden="true"></span>
            {/if}
            <span class="sandbox-image-status-label">{label}</span>
            {#if failed}
                <button
                    type="button"
                    class="sandbox-image-status-dismiss"
                    onclick={() => sandboxImageStatus.clear()}
                    aria-label={t('sandbox.dismissImageError')}
                >
                    {t('sandbox.dismiss')}
                </button>
            {/if}
        </div>
        {#if failed && state.error}
            <div class="sandbox-image-status-error">{state.error}</div>
        {/if}
        {#if state.lines.length > 0}
            <pre class="sandbox-image-status-log" aria-label={t('sandbox.imageBuildOutput')}>{state.lines.join('\n')}</pre>
        {/if}
    </div>
{/if}

<style>
    .sandbox-image-status {
        display: flex;
        flex-direction: column;
        gap: 6px;
        margin: 0 0 8px 0;
        padding: 8px 10px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        font-size: 0.75rem;
    }

    .sandbox-image-status.failed {
        border-color: var(--color-status-red);
        background: var(--color-status-red-bg);
    }

    .sandbox-image-status-head {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .sandbox-image-status-label {
        color: var(--color-text-primary);
        font-weight: 500;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .sandbox-image-status-spin {
        flex: 0 0 auto;
        width: 10px;
        height: 10px;
        border: 2px solid var(--color-border-bright);
        border-top-color: var(--color-accent-dark);
        border-radius: 50%;
        animation: spin 0.8s linear infinite;
    }

    .sandbox-image-status-error {
        color: var(--color-status-red);
        white-space: pre-wrap;
        word-break: break-word;
    }

    .sandbox-image-status-log {
        margin: 0;
        padding: 6px 8px;
        max-height: 8rem;
        overflow: auto;
        background: var(--color-surface-1);
        border-radius: var(--radius-sm);
        font-family: var(--font-mono, monospace);
        font-size: 0.6875rem;
        line-height: 1.4;
        color: var(--color-text-secondary);
        white-space: pre-wrap;
        word-break: break-word;
    }

    .sandbox-image-status-dismiss {
        margin-left: auto;
        padding: 2px 8px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-secondary);
        font-family: inherit;
        font-size: 0.6875rem;
        cursor: pointer;
    }

    .sandbox-image-status-dismiss:hover {
        color: var(--color-text-primary);
        border-color: var(--color-border-bright);
    }
</style>
