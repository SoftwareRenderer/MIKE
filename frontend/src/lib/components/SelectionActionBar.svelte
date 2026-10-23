<script lang="ts">
    import type { Snippet } from 'svelte';
    import { t } from '$lib/i18n';
    import Icon from './Icon.svelte';
    import closeIcon from '$lib/assets/icons/close.svg?raw';

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
    ><Icon src={closeIcon} width={14} height={14} /></button>
</div>

<style>

    /* Placed against the host's box, so the host has to be positioned. An accent edge over a tinted
       surface, with the deeper shadow, keeps the bar from reading as one more row of the listing. */
    .file-selection-bar {
        position: absolute;
        left: 8px;
        right: 8px;
        bottom: 8px;
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 8px;
        background: color-mix(in srgb, var(--color-accent) 12%, var(--color-surface-1));
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-md);
        box-shadow: var(--shadow-modal);
    }

    .selection-count {
        flex: 1;
        padding-left: 4px;
        border-right: 1px solid rgb(from var(--color-accent) r g b / 0.35);
        font-size: var(--text-sm);
        color: var(--color-text-primary);
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
        color: var(--color-status-red);
    }
    @media (max-width: 768px) {
        :global(.file-selection-bar .selection-btn) {
            width: 36px;
            height: 36px;
        }
    }
</style>
