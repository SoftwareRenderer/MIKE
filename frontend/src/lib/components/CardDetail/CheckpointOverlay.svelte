<script lang="ts">
    import type { DiffFile, DiffImageSource } from '$lib/types/diff';
    import { t } from '$lib/i18n';
    import Spinner from '../Spinner.svelte';
    import { useLazyComponent } from '$lib/composables/use-lazy-component.svelte';

    // A static import here would pull DiffViewer back into this component's own chunk.
    const diffViewer = useLazyComponent(() => import('../DiffViewer.svelte'));

    $effect(() => {
        diffViewer.load();
    });

    let { files, untracked, loading, onClose, onExpandFile, onLineClick, imageSource }: {
        files: DiffFile[];
        untracked: { path: string; fileSize?: number; file: DiffFile; truncated?: boolean; origAdded?: number }[];
        loading: boolean;
        onClose: () => void;
        onExpandFile: (path?: string) => void;
        onLineClick: (path: string, line: number) => void;
        /** Which commits this checkpoint's diff was taken between. */
        imageSource?: DiffImageSource;
    } = $props();
</script>

<div class="checkpoint-diff-overlay">
    <div class="checkpoint-diff-header">
        <div class="checkpoint-diff-title">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                <path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/>
            </svg>
            <span>{t('task.checkpoint.title')}</span>
        </div>
        <button class="btn-close-checkpoint" onclick={onClose} title={t('task.closeEscAria')} aria-label={t('app.close')}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                <path d="M18 6L6 18M6 6l12 12"/>
            </svg>
        </button>
    </div>
    {#if loading}
        <div class="checkpoint-diff-loading checkpoint-diff-loading-root">
            <Spinner size={16} label={t('diff.loadingChanges')} />
        </div>
    {:else}
        <div class="checkpoint-diff-body">
            {#if diffViewer.component}
                {@const DiffViewer = diffViewer.component}
                <DiffViewer stagedFiles={[]} unstagedFiles={files} stagedUntracked={[]} unstagedUntracked={untracked} onExpandFile={onExpandFile} onExpandUntracked={onExpandFile} onLineClick={onLineClick} imageSource={imageSource} />
            {:else}
                <div class="checkpoint-diff-loading">
                    <Spinner size={16} label={t('diff.loadingChanges')} />
                </div>
            {/if}
        </div>
    {/if}
</div>

<style>
    .checkpoint-diff-overlay {
        display: flex;
        flex-direction: column;
        height: 100%;
        width: 100%;
        overflow: hidden;
    }
    .checkpoint-diff-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 12px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-2);
        flex-shrink: 0;
    }
    .checkpoint-diff-title {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--text-sm);
        font-weight: 600;
        color: white;
    }
    .checkpoint-diff-title svg {
        opacity: 0.7;
    }
    .btn-close-checkpoint {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        border-radius: var(--radius-sm);
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-close-checkpoint:hover {
        opacity: 1;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .checkpoint-diff-body {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-height: 0;
        overflow: hidden;
    }
    .checkpoint-diff-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--color-text-tertiary);
    }
    .checkpoint-diff-loading-root {
        flex: 1;
        min-height: 0;
    }
    .checkpoint-diff-body .checkpoint-diff-loading {
        height: 100%;
    }
</style>
