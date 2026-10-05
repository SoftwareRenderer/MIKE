<script lang="ts">
    import { appStore } from '$lib/stores';
    import { effectiveOpenTerminalKey, formatShortcut, handleLinkClick, handleLinkKeydown } from '$lib/utils/keyboard';
    import { t } from '$lib/i18n';
    import Icon from './Icon.svelte';
    import commitPushIcon from '$lib/assets/icons/commit-push.svg?raw';
    import closeIcon from '$lib/assets/icons/close.svg?raw';

    /**
     * The explorer header both screens draw: close, path, the branch, and the terminal.
     * `branches` is what makes a screen branch-flavoured — without it the branch is a static badge.
     */
    let { pathLabel, pathClamped = false, closeAria, branch, statusColor = '', branches,
        branchesLoading = false, dropdownOpen = $bindable(false), checkoutLoading = false,
        onSelectBranch, changesOpen, onChangesOpen, showReview = false, onCloseRequest, onOpenTerminal,
        terminalHref }: {
        pathLabel: string;
        /** A workspace-rooted path is long by construction and gets two lines. */
        pathClamped?: boolean;
        closeAria: string;
        branch: string;
        /** Set when the badge is painted from a task status; it arrives as an inline style. */
        statusColor?: string;
        branches?: string[];
        branchesLoading?: boolean;
        /** Bound so the owning screen can fold the dropdown into its Escape order. */
        dropdownOpen?: boolean;
        checkoutLoading?: boolean;
        onSelectBranch?: (branch: string) => void;
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

    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let dropdownEl: HTMLDivElement | null = null;

    $effect(() => {
        function handleClick(e: MouseEvent) {
            if (dropdownEl && !dropdownEl.contains(e.target as Node)) {
                dropdownOpen = false;
            }
        }
        if (dropdownOpen) {
            document.addEventListener('click', handleClick);
            return () => document.removeEventListener('click', handleClick);
        }
    });
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
                onclick={() => dropdownOpen = !dropdownOpen}
                title={t('explorer.view.switchBranch')}
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
                <div class="explorer-dropdown-list" role="listbox">
                    {#if branchesLoading}
                        <div class="explorer-dropdown-item loading">{t('explorer.shared.loading')}</div>
                    {:else}
                        {#each branches as branchOption}
                            <button
                                class="explorer-dropdown-item"
                                class:active={branchOption === branch}
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
                        {#if branches.length === 0}
                            <div class="explorer-dropdown-item empty">{t('explorer.view.noBranches')}</div>
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
