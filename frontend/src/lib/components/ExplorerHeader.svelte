<script lang="ts">
    import { appStore } from '$lib/stores';
    import { effectiveOpenTerminalKey, formatShortcut, handleLinkClick, handleLinkKeydown } from '$lib/utils/keyboard';
    import { filterBranches, matchesBranch, nextBranchRow } from '$lib/utils/branch-rows';
    import { taskViewHref } from '$lib/utils/app-link';
    import { urlRouter } from '$lib/services/url-router.svelte';
    import type { HeldBranch } from '$lib/api';
    import { t } from '$lib/i18n';
    import Icon from './Icon.svelte';
    import commitPushIcon from '$lib/assets/icons/commit-push.svg?raw';
    import closeIcon from '$lib/assets/icons/close.svg?raw';

    /**
     * The explorer header both screens draw: close, path, the branch, and the terminal.
     * `branches` is what makes a screen branch-flavoured — without it the branch is a static badge.
     */
    let { pathLabel, pathClamped = false, closeAria, branch, statusColor = '', branches,
        branchesLoading = false, branchesError = false, withheld = 0, heldBranches = [], projectId,
        dropdownOpen = $bindable(false),
        checkoutLoading = false, onSelectBranch, onRetryBranches, changesOpen, onChangesOpen,
        showReview = false, onCloseRequest, onOpenTerminal, terminalHref }: {
        pathLabel: string;
        /** A workspace-rooted path is long by construction and gets two lines. */
        pathClamped?: boolean;
        closeAria: string;
        branch: string;
        /** Set when the badge is painted from a task status; it arrives as an inline style. */
        statusColor?: string;
        branches?: string[];
        branchesLoading?: boolean;
        /** The read failed, so the list says that instead of claiming there are no branches. */
        branchesError?: boolean;
        /** withheld counts the refs the list left out because another worktree holds them. */
        withheld?: number;
        /** heldBranches are the refs a task holds; each row opens that task. */
        heldBranches?: HeldBranch[];
        /** The held rows link into a task of this project. */
        projectId: string;
        /** Bound so the owning screen can fold the dropdown into its Escape order. */
        dropdownOpen?: boolean;
        checkoutLoading?: boolean;
        onSelectBranch?: (branch: string) => void;
        onRetryBranches?: () => void;
        changesOpen: boolean;
        onChangesOpen: (open: boolean) => void;
        showReview?: boolean;
        onCloseRequest: () => void;
        onOpenTerminal: () => void;
        /** Link to the terminal-only screen; the click opens the panel here. */
        terminalHref: string;
    } = $props();

    // Tooltips keep the shortcut text even when keycap hints are hidden.
    const terminalButtonTitle = $derived.by(() => {
        const key = effectiveOpenTerminalKey(appStore.config);
        return key
            ? t('explorer.shared.terminalTitle', { shortcut: formatShortcut(key) })
            : t('explorer.shared.openTerminal');
    });

    // svelte-ignore non_reactive_update — these are DOM refs, read from effects and handlers
    let dropdownEl: HTMLDivElement | null = null;
    let triggerEl: HTMLButtonElement | null = null;
    let filterEl: HTMLInputElement | null = null;
    let query = $state('');

    const visibleBranches = $derived(filterBranches(branches ?? [], query));
    // A held ref is reachable only by typing, so an empty field lists the switchable rows and says what it left out.
    const narrowing = $derived(query.trim() !== '');
    const visibleHeld = $derived(narrowing ? heldBranches.filter(b => matchesBranch(b.name, query)) : []);

    $effect(() => {
        function handleClick(e: MouseEvent) {
            const target = e.target;
            if (!(target instanceof Node)) return;
            // The click that opened the list is still bubbling here when this listener exists.
            if (triggerEl?.contains(target)) return;
            // A control that removes itself in its own click, like Retry, is detached before the event arrives.
            if (!document.contains(target)) return;
            if (dropdownEl && !dropdownEl.contains(target)) {
                dropdownOpen = false;
            }
        }
        if (dropdownOpen) {
            document.addEventListener('click', handleClick);
            return () => document.removeEventListener('click', handleClick);
        }
    });

    /** An open list focuses the filter, and closing hands focus back to the trigger: a browser drops a
     * removed focus target onto the document body. */
    $effect(() => {
        if (!dropdownOpen) return;
        query = '';
        let heldInside = false;
        function handleFocusIn(e: FocusEvent) {
            heldInside = !!dropdownEl && e.target instanceof Node && dropdownEl.contains(e.target);
        }
        // The listener goes on before the focus below, because that first focus is the case this cleanup exists for.
        document.addEventListener('focusin', handleFocusIn);
        filterEl?.focus();
        return () => {
            document.removeEventListener('focusin', handleFocusIn);
            if (heldInside && triggerEl?.isConnected) triggerEl.focus();
        };
    });

    /** Arrows and Home/End move focus between rows; a loading, failed or empty row is a message, not a target. */
    function handleListKeydown(e: KeyboardEvent) {
        const rows = dropdownEl
            ? Array.from(dropdownEl.querySelectorAll<HTMLElement>('a.explorer-dropdown-item, button.explorer-dropdown-item'))
            : [];
        const next = nextBranchRow(rows.indexOf(e.target as HTMLElement), e.key, rows.length);
        if (next < 0) return;
        e.preventDefault();
        rows[next].focus();
    }
</script>

<div class="explorer-header">
    <button class="close-btn" onclick={() => { if (changesOpen) { onChangesOpen(false); } else { onCloseRequest(); } }} aria-label={closeAria} title={t('explorer.shared.closeEscape')}>
        <Icon src={closeIcon} width={16} height={16} />
    </button>
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="explorer-view-icon">
        <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
    </svg>
    <span class="project-path" class:is-clamped={pathClamped} class:is-ellipsis={!pathClamped} title={pathLabel}>{pathLabel}</span>

    {#if changesOpen || !branches}
        <span class="explorer-branch-badge" class:is-status={!!statusColor} style={statusColor ? `background: ${statusColor}22; color: ${statusColor}; border-color: ${statusColor}44` : undefined} title={branch}>
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <circle cx="6" cy="6" r="2.5"/>
                <circle cx="10" cy="10" r="2.5"/>
                <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                <line x1="6" y1="3.5" x2="6" y2="2"/>
                <line x1="10" y1="12.5" x2="10" y2="14"/>
            </svg>
            <span class="branch-badge-label">{branch}</span>
        </span>
    {:else}
        <div class="explorer-selector" class:open={dropdownOpen}>
            <button
                class="explorer-dropdown-btn"
                bind:this={triggerEl}
                onclick={() => dropdownOpen = !dropdownOpen}
                onkeydown={(e) => { if (e.key === 'ArrowDown' && !dropdownOpen) { e.preventDefault(); dropdownOpen = true; } }}
                title={t('explorer.view.switchBranch')}
                aria-haspopup="listbox"
                aria-expanded={dropdownOpen}
                disabled={checkoutLoading}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <circle cx="6" cy="6" r="2.5"/>
                    <circle cx="10" cy="10" r="2.5"/>
                    <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                    <line x1="6" y1="3.5" x2="6" y2="2"/>
                    <line x1="10" y1="12.5" x2="10" y2="14"/>
                </svg>
                <span class="explorer-name">{branch}</span>
                <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <polyline points="2,3.5 5,6.5 8,3.5"/>
                </svg>
            </button>

            {#if dropdownOpen}
                <div class="explorer-dropdown-list" bind:this={dropdownEl} onkeydown={handleListKeydown}>
                    {#if branchesLoading}
                        <div class="explorer-dropdown-item loading">{t('explorer.shared.loading')}</div>
                    {:else if branchesError}
                        <div class="explorer-dropdown-item error">
                            <span class="explorer-dropdown-name">{t('explorer.view.errLoadBranches')}</span>
                            {#if onRetryBranches}
                                <button class="explorer-dropdown-retry" onclick={onRetryBranches}>
                                    {t('explorer.view.retry')}
                                </button>
                            {/if}
                        </div>
                    {:else}
                        <input
                            class="explorer-dropdown-filter"
                            type="text"
                            autocomplete="off"
                            bind:value={query}
                            bind:this={filterEl}
                            placeholder={t('explorer.view.filterBranches')}
                            aria-label={t('explorer.view.filterBranches')}
                        />
                        <div class="explorer-dropdown-rows" role="listbox"
                            aria-label={t('explorer.view.switchBranch')}>
                            {#each visibleBranches as branchOption (branchOption)}
                                <button
                                    class="explorer-dropdown-item"
                                    class:active={branchOption === branch}
                                    tabindex="-1"
                                    onclick={() => { dropdownOpen = false; onSelectBranch?.(branchOption); }}
                                    role="option"
                                    aria-selected={branchOption === branch}
                                >
                                    {#if branchOption === branch}
                                        <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                            <polyline points="1.5,5 4,7.5 8.5,2.5"/>
                                        </svg>
                                    {:else}
                                        <span class="explorer-check-empty"></span>
                                    {/if}
                                    <span class="explorer-dropdown-name" title={branchOption}>{branchOption}</span>
                                </button>
                            {/each}
                            {#if visibleBranches.length === 0 && visibleHeld.length === 0}
                                <div class="explorer-dropdown-item empty">
                                    {branches?.length ? t('explorer.view.noBranchMatch') : t('explorer.view.noBranches')}
                                </div>
                            {/if}
                        </div>
                        {#if visibleHeld.length > 0}
                            <!-- The row links to the task holding this ref, which this checkout cannot check out. -->
                            <div class="explorer-dropdown-held">
                                {#each visibleHeld as held (held.name)}
                                    <a class="explorer-dropdown-item is-held"
                                        href={taskViewHref(projectId, held.taskId)}
                                        onclick={(e) => handleLinkClick(e, () => {
                                            dropdownOpen = false;
                                            urlRouter.openTask(projectId, held.taskId);
                                        })}
                                    >
                                        <span class="explorer-check-empty"></span>
                                        <span class="explorer-dropdown-name" title={held.name}>{held.name}</span>
                                        <span class="explorer-dropdown-hint">{t('explorer.view.branchHeldByTask')}</span>
                                    </a>
                                {/each}
                            </div>
                        {/if}
                        {#if withheld > 0 && !narrowing}
                            <!-- Outside the listbox and the scroll, so the reason is not below the fold. -->
                            <div class="explorer-dropdown-note">
                                {t('explorer.view.branchesHeldElsewhere', { count: withheld })}
                                {#if heldBranches.length > 0}
                                    <span class="explorer-dropdown-hint">{t('explorer.view.branchesHeldHint')}</span>
                                {/if}
                            </div>
                        {/if}
                    {/if}
                </div>
            {/if}
        </div>
    {/if}
    <div class="explorer-header-spacer"></div>
    {#if showReview}
        <button class="icon-btn git-status-btn" class:active={changesOpen} onclick={() => onChangesOpen(!changesOpen)} aria-label={t('explorer.shared.reviewChanges')} title={t('explorer.shared.reviewChanges')}>
            <Icon src={commitPushIcon} color="currentColor" width={14} height={14} />
        </button>
    {/if}
    <a
        class="icon-btn terminal-btn"
        href={terminalHref}
        role="button"
        tabindex="0"
        draggable="false"
        onclick={(e) => handleLinkClick(e, onOpenTerminal)}
        onkeydown={(e) => handleLinkKeydown(e, onOpenTerminal)}
        aria-label={t('explorer.shared.openTerminal')} title={terminalButtonTitle}
    >
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
            <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    </a>
</div>

<style>
    /* Anchors get neither the buttons' inherited colour nor the `button:hover` fade. */
    .terminal-btn {
        color: inherit;
    }
    .terminal-btn:hover {
        opacity: 0.85;
    }
</style>
