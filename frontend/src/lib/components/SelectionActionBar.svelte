<script lang="ts">
    import type { Snippet } from 'svelte';
    import { t } from '$lib/i18n';

    /**
     * The touch equivalent of a context menu: how many rows are held, and the way out of the selection.
     * The actions belong to the host, since only it knows what can be done to its rows.
     */
    let { count, onClear, actions }: {
        count: number;
        onClear: () => void;
        /** The host's buttons, each a `button.selection-btn.btn-ghost`. */
        actions: Snippet;
    } = $props();
</script>

<div class="file-selection-bar" role="toolbar" aria-label={t('explorer.shared.selectionAria')}>
    <span class="selection-count">{t('explorer.shared.selectedCount', { count })}</span>
    {@render actions()}
    <button
        type="button"
        class="selection-btn btn-ghost"
        title={t('explorer.shared.selectionClear')}
        aria-label={t('explorer.shared.selectionClear')}
        onclick={onClear}
    >✕</button>
</div>

<style>
    @import '$lib/styles/shared.css';

    /* Placed against the host's box, so the host has to be positioned. */
    .file-selection-bar {
        position: absolute;
        left: 8px;
        right: 8px;
        bottom: 8px;
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 4px 6px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
    }

    .selection-count {
        flex: 1;
        padding-left: 4px;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    /* The host's buttons come from its snippet, so only a global selector reaches them. This is their
       geometry — a compact square — and nothing else: `.btn-ghost` on each one owns the colour and the
       hover, and `.destructive` marks the one that removes something. */
    :global(.file-selection-bar .selection-btn) {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 26px;
        height: 26px;
        padding: 0;
        border-radius: var(--radius-sm);
        font-size: var(--text-sm);
    }
    :global(.file-selection-bar .selection-btn.destructive) {
        color: var(--color-status-red, #f85149);
    }
    @media (max-width: 768px) {
        :global(.file-selection-bar .selection-btn) {
            width: 36px;
            height: 36px;
        }
        .selection-count {
            font-size: var(--text-sm);
        }
    }
</style>
