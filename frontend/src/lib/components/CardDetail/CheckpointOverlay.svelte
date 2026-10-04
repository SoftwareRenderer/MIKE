<script lang="ts">
    import type { DiffFile, DiffImageSource } from '$lib/types/diff';
    import { t } from '$lib/i18n';
    import Spinner from '../Spinner.svelte';
    import { useLazyComponent } from '$lib/composables/use-lazy-component.svelte';
    // `.diff-empty` owns the centered placeholder row — the loading and fault states here are that shape,
    // and it arrives with this panel's own stylesheet rather than with the lazily loaded viewer.
    import '$lib/styles/diff-viewer.css';

    // A static import here would pull DiffViewer back into this component's own chunk.
    const diffViewer = useLazyComponent(() => import('../DiffViewer.svelte'));

    $effect(() => {
        diffViewer.load();
    });

    let { files, loading, error, label, refs, onClose, onExpandFile, onRetry, imageSource }: {
        files: DiffFile[];
        loading: boolean;
        /** What the read answered, when it answered nothing. An unreadable snapshot is not "no changes". */
        error: string;
        /** The checkpoint's own words, generated when the state was captured. */
        label?: string;
        /** The two sides this comparison ran, named so the panel is never unidentified. */
        refs?: string;
        onClose: () => void;
        onExpandFile: (path: string) => void;
        onRetry: () => void;
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
            {#if refs}
                <code class="checkpoint-diff-refs" title={refs}>{refs}</code>
            {/if}
        </div>
        <button class="btn-close-checkpoint" onclick={onClose} title={t('task.closeEscAria')} aria-label={t('app.close')}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                <path d="M18 6L6 18M6 6l12 12"/>
            </svg>
        </button>
    </div>
    {#if label}
        <p class="checkpoint-diff-subtitle">{label}</p>
    {/if}
    {#if loading}
        <div class="diff-empty pane-fill">
            <Spinner size={16} label={t('diff.loadingChanges')} />
        </div>
    {:else if error}
        <div class="diff-empty pane-fill">
            <span class="checkpoint-diff-error-text">{error}</span>
            <button class="checkpoint-diff-retry" onclick={onRetry}>{t('chat.entries.historyRetry')}</button>
        </div>
    {:else}
        <div class="pane-fill checkpoint-diff-body">
            {#if diffViewer.component}
                {@const DiffViewer = diffViewer.component}
                <DiffViewer
                    stagedFiles={[]}
                    unstagedFiles={files}
                    stagedUntracked={[]}
                    unstagedUntracked={[]}
                    unstagedTitle={t('task.checkpoint.filesSection')}
                    emptyTitle={t('task.checkpoint.noChanges')}
                    collapseScope="checkpoint"
                    onExpandFile={onExpandFile}
                    imageSource={imageSource}
                />
            {:else}
                <div class="diff-empty pane-fill">
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
        gap: 8px;
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
        color: var(--color-text-primary);
        min-width: 0;
    }
    .checkpoint-diff-title svg {
        opacity: 0.7;
        flex-shrink: 0;
    }
    .checkpoint-diff-refs {
        font-family: var(--font-mono);
        font-size: var(--text-xs);
        font-weight: 400;
        color: var(--color-text-secondary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .checkpoint-diff-subtitle {
        margin: 0;
        padding: 5px 12px;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        border-bottom: 1px solid var(--color-border);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
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
        transition: all var(--dur-base) ease;
    }
    .btn-close-checkpoint:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .checkpoint-diff-body {
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .checkpoint-diff-error-text {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
    }
    .checkpoint-diff-retry {
        font-size: var(--text-xs);
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        border: 1px solid var(--color-border);
        color: var(--color-text-primary);
        cursor: pointer;
    }
    .checkpoint-diff-retry:hover {
        opacity: 1;
        background: var(--color-surface-4);
    }
</style>
