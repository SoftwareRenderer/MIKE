<script lang="ts">
    import { t } from '$lib/i18n';
    import type { EditorHistory } from '$lib/utils/editor-history';

    /**
     * The undo/redo pair for an editor whose chrome lives elsewhere: the Explorer's tab bar and the task
     * view's diff header both sit above an editor they hold no handle on, so they render what it reported.
     * Only the narrow layout has them, where no keyboard is within reach; a direction with nothing behind
     * it is disabled, and the dim is all an icon-only button can say.
     */
    let { history, isMobile }: { history?: EditorHistory; isMobile: boolean } = $props();
</script>

{#if isMobile}
    <button
        type="button"
        class="history-btn"
        disabled={!history?.canUndo}
        onclick={() => history?.undo()}
        aria-label={t('editor.undo')}
        title={t('editor.undo')}
    >
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M6 2.7 2.7 6 6 9.3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M2.7 6h7a3.7 3.7 0 0 1 0 7.4H7.4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
    <button
        type="button"
        class="history-btn"
        disabled={!history?.canRedo}
        onclick={() => history?.redo()}
        aria-label={t('editor.redo')}
        title={t('editor.redo')}
    >
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M10 2.7 13.3 6 10 9.3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M13.3 6h-7a3.7 3.7 0 0 0 0 7.4h2.3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
{/if}

<style>
    .history-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        flex-shrink: 0;
    }
    .history-btn:hover:not(:disabled) {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .history-btn:disabled {
        opacity: 0.35;
        cursor: default;
    }
</style>
