<script lang="ts">
    /**
     * The progress of the build or pull this project's image is running, and the button that stops it.
     *
     * Both the Dockerfile section and the locked view draw this. The build a person most often wants to read
     * was started by a task, and while a task runs the screen shows the locked view. The lines come from the
     * server, and only from the moment this screen started reading: what the build printed before that is kept
     * nowhere.
     */
    import { t } from '$lib/i18n';
    import type { Project } from '$lib/types';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, project }: {
        draft: SandboxDraftOwner;
        project: Project;
    } = $props();

    let buildLogEl = $state<HTMLDivElement | null>(null);
    let buildLogAtBottom = $state(true);

    // A new line follows the bottom only when the reader has not scrolled away from it.
    $effect(() => {
        if (draft.buildLog.length === 0) return;
        scrollBuildLog();
    });

    function scrollBuildLog() {
        if (!buildLogEl || !buildLogAtBottom) return;
        requestAnimationFrame(() => {
            if (!buildLogEl) return;
            buildLogEl.scrollTop = buildLogEl.scrollHeight;
            requestAnimationFrame(() => {
                if (buildLogEl) buildLogEl.scrollTop = buildLogEl.scrollHeight;
            });
        });
    }

    function onBuildLogScroll() {
        if (!buildLogEl) return;
        const gap = buildLogEl.scrollHeight - buildLogEl.scrollTop - buildLogEl.clientHeight;
        buildLogAtBottom = gap < 50;
    }
</script>

{#if draft.building}
    <div class="build-progress">
        <div class="build-notice">{t('sandbox.buildNotice')}</div>
        <button
            type="button"
            class="btn-cancel-build"
            onclick={() => draft.cancelBuild(project)}
            disabled={draft.cancelling}
            title={t('sandbox.cancelBuildTitle')}
        >{draft.cancelling ? t('sandbox.cancelling') : t('sandbox.cancelBuild')}</button>
    </div>
{/if}

{#if draft.building || draft.buildLog.length > 0}
    <div class="build-log" bind:this={buildLogEl} role="log" aria-label={t('sandbox.buildOutput')} onscroll={onBuildLogScroll}>
        {#each draft.buildLog as line}
            <div class="build-log-line">{line}</div>
        {/each}
        {#if draft.buildLog.length === 0}
            <div class="build-log-line build-log-pending">{t('sandbox.building')}</div>
        {/if}
    </div>
{/if}

{#if draft.buildCancelled}
    <div class="build-cancelled">{t('sandbox.buildCancelled')}</div>
{/if}

{#if draft.buildError}
    <div class="build-error">{draft.buildError}</div>
{/if}

<style>
    .build-progress {
        display: flex;
        align-items: center;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 8px;
    }

    .build-notice {
        flex: 1;
        padding: 6px 10px;
        background: rgb(from var(--color-status-blue) r g b / 0.08);
        border: 1px solid rgb(from var(--color-status-blue) r g b / 0.2);
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
    }

    .btn-cancel-build {
        flex-shrink: 0;
        padding: 3px 8px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: var(--text-xs);
        white-space: nowrap;
        transition: background var(--dur-base), border-color var(--dur-base);
    }

    .btn-cancel-build:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-status-red);
        color: var(--color-status-red);
    }

    .build-log {
        max-height: 180px;
        overflow-y: auto;
        padding: 8px 10px;
        background: var(--color-bg-elevated);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        font-family: var(--font-mono);
        font-size: var(--text-xs);
        line-height: 1.5;
        color: var(--color-text-secondary);
    }

    .build-log-line {
        white-space: pre-wrap;
        word-break: break-all;
    }

    .build-log-pending {
        color: var(--color-text-tertiary);
        font-style: italic;
    }

    .build-cancelled {
        padding: 6px 10px;
        background: rgb(from var(--color-warning) r g b / 0.1);
        border: 1px solid rgb(from var(--color-warning) r g b / 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-warning);
        font-size: var(--text-xs);
    }

    .build-error {
        padding: 6px 10px;
        background: rgb(from var(--color-status-red) r g b / 0.1);
        border: 1px solid rgb(from var(--color-status-red) r g b / 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-status-red);
        font-size: var(--text-xs);
    }
</style>
