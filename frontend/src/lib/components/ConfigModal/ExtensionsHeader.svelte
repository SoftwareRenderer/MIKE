<script lang="ts">
    import { t } from '$lib/i18n';
    import { typeBadge } from '$lib/utils/extension-manifest';
    import type { ExtensionType } from '$lib/types';

    /**
     * The sticky header of Settings → Extensions: which of the two lists is on screen, the text query
     * both lists narrow by, the category badges the query left, and the check that switches back to the
     * Installed list to show its answer.
     *
     * Counts are measured over the list the query left, never over the category-narrowed one — see
     * `$lib/utils/extension-categories`.
     */
    let {
        activeView,
        installedCount,
        loadingUpdates,
        searchQuery,
        categories,
        categoryCounts,
        selectedCategories,
        onloadupdates,
        onview,
        onsearch,
        ontogglecategory,
        onclearcategories,
    }: {
        activeView: 'installed' | 'browse';
        installedCount: number;
        loadingUpdates: boolean;
        searchQuery: string;
        categories: ExtensionType[];
        categoryCounts: Map<ExtensionType, number>;
        selectedCategories: Set<ExtensionType>;
        onloadupdates: () => void;
        onview: (view: 'installed' | 'browse') => void;
        onsearch: (query: string) => void;
        ontogglecategory: (category: ExtensionType) => void;
        onclearcategories: () => void;
    } = $props();
</script>

<div class="sticky-header">
    <!-- View Tabs -->
    <div class="view-tabs">
        <div class="view-tabs-left">
            <button
                class="view-tab"
                class:active={activeView === 'installed'}
                onclick={() => onview('installed')}
            >
                {t('settings.extensions.installedCount', { count: installedCount })}
            </button>
            <button
                class="view-tab"
                class:active={activeView === 'browse'}
                onclick={() => onview('browse')}
            >
                {t('settings.extensions.browse')}
            </button>
        </div>
        <div class="view-tabs-right">
            <button class="btn btn-secondary" onclick={onloadupdates} disabled={loadingUpdates}>
                {loadingUpdates ? t('settings.extensions.checking') : t('settings.extensions.checkUpdates')}
            </button>
        </div>
    </div>

    <!-- Search -->
    <div class="search-row">
        <input
            type="search"
            placeholder={t('settings.extensions.searchPlaceholder')}
            value={searchQuery}
            oninput={(e) => onsearch((e.target as HTMLInputElement).value)}
            class="search-input"
        />
    </div>

    <!-- Category quick-search: one badge per category the query left in the current view -->
    {#if categories.length > 0 || selectedCategories.size > 0}
        <div class="category-row" role="group" aria-label={t('settings.extensions.categoryGroup')}>
            {#each categories as category}
                {@const count = categoryCounts.get(category) ?? 0}
                {@const label = typeBadge(category)}
                <button
                    class="category-badge"
                    class:active={selectedCategories.has(category)}
                    aria-pressed={selectedCategories.has(category)}
                    aria-label={t('settings.extensions.categoryAria', { label, count })}
                    onclick={() => ontogglecategory(category)}
                >{label}</button>
            {/each}
            {#if selectedCategories.size > 0}
                <button class="category-clear" onclick={onclearcategories}>
                    {t('settings.extensions.clearCategories')}
                </button>
            {/if}
        </div>
    {/if}
</div>

<style>
    /* Sticky wrapper: view-tabs + search-row + category-row, flush with modal-header */
    .sticky-header {
        position: sticky;
        top: 0;
        z-index: var(--z-sticky);
        background: var(--color-surface-1);
    }

    /* View tabs */
    .view-tabs {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 0.5rem;
        border-bottom: 1px solid var(--color-border-subtle);
        padding: 0.5rem 0;
    }

    .view-tabs-left {
        display: flex;
        gap: 0.5rem;
    }

    .view-tabs-right {
        display: flex;
        gap: 0.5rem;
        flex-shrink: 0;
    }

    .view-tab {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        font-size: var(--text-base);
        padding: 0.5rem 1rem;
        border-radius: var(--radius-md);
        transition: all 0.15s;
    }

    .view-tab:hover {
        background: rgba(139, 148, 158, 0.1);
        color: var(--color-text-primary);
    }

    .view-tab.active {
        background: rgba(88, 166, 255, 0.15);
        color: var(--color-accent);
    }

    /* Search */
    .search-row {
        display: flex;
        gap: 0.5rem;
        padding: 0.25rem 0;
    }

    .search-input {
        flex: 1;
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-md);
        padding: 0.5rem 0.75rem;
        color: var(--color-text-primary);
        font-size: var(--text-base);
    }

    .search-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    /* Category quick-search badges under the search input */
    .category-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 0.4rem;
        padding: 0 0 0.5rem;
    }

    .category-badge {
        display: inline-flex;
        align-items: center;
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-pill);
        padding: 0.2rem 0.6rem;
        color: var(--color-text-secondary);
        font-size: var(--text-2xs);
        font-weight: 700;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        cursor: pointer;
        transition: all 0.15s;
    }

    .category-badge:hover {
        color: var(--color-text-primary);
        border-color: var(--color-accent);
    }

    .category-badge.active {
        background: rgba(88, 166, 255, 0.15);
        border-color: var(--color-accent);
        color: var(--color-accent);
    }

    /* Not a category: the way out of the selection, so it reads as an action, not a fourth badge. */
    .category-clear {
        background: none;
        border: none;
        padding: 0.2rem 0.25rem;
        color: var(--color-text-tertiary);
        font-size: var(--text-xs);
        text-decoration: underline;
        cursor: pointer;
    }

    .category-clear:hover {
        color: var(--color-text-primary);
    }

    .btn {
        padding: 0.4rem 0.75rem;
        border-radius: var(--radius-md);
        font-size: var(--text-sm);
        cursor: pointer;
        border: 1px solid transparent;
        transition: all 0.15s;
    }

    .btn-secondary {
        background: var(--color-btn-secondary-bg);
        border-color: var(--color-border-subtle);
        color: var(--color-text-primary);
    }

    .btn-secondary:hover:not(:disabled) {
        background: var(--color-border-subtle);
    }
</style>
