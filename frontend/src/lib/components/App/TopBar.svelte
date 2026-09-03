<script lang="ts">
    import type { Project } from '$lib/types';
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import { toHomeRelative } from '$lib/utils/path';
    import { appStore } from '$lib/stores';
    import { effectiveOpenTerminalKey, formatShortcut } from '$lib/utils/keyboard';

    let {
        currentProject,
        currentBranch,
        onOpenMenu,
        onOpenSandboxConfig,
        onOpenExplorerView,
        onOpenTerminal,
        onShowStats,
        onShowConfig,
        hasReview = false,
    }: {
        currentProject: Project;
        currentBranch: string;
        onOpenMenu: () => void;
        onOpenSandboxConfig: () => void;
        onOpenExplorerView: () => void;
        onOpenTerminal: () => void;
        onShowStats: () => void;
        onShowConfig: () => void;
        hasReview?: boolean;
    } = $props();

    let isMainBranch = $derived(currentBranch === 'main' || currentBranch === 'master');

    // Terminal button tooltip — always shows the effective shortcut (tooltips
    // keep the shortcut text even when keycap hints are hidden).
    const terminalButtonTitle = $derived.by(() => {
        const key = effectiveOpenTerminalKey(appStore.config);
        return key ? `Open terminal (${formatShortcut(key)})` : 'Open terminal';
    });
</script>

<header class="top-bar">
    <!-- Hamburger menu button (hidden on desktop via CSS) -->
    <button class="icon-btn hamburger" onclick={onOpenMenu} aria-label="Open menu" title="Open menu">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <line x1="3" y1="6" x2="21" y2="6"/>
            <line x1="3" y1="12" x2="21" y2="12"/>
            <line x1="3" y1="18" x2="21" y2="18"/>
        </svg>
        {#if hasReview}
            <span class="review-dot" aria-hidden="true"></span>
        {/if}
    </button>
    <button class="icon-btn sandbox-config-btn" onclick={onOpenSandboxConfig} aria-label="Sandbox configuration" title="Sandbox configuration">
        {#if currentProject.sandbox_enabled}
            <Icon src={shieldIcon} width={14} height={14} />
        {:else}
            <Icon src={shieldOffIcon} color="var(--color-status-red)" width={14} height={14} />
        {/if}
    </button>
    <span class="project-title">
        <button
            class="icon-btn explorer-btn"
            onclick={onOpenExplorerView}
            aria-label="Open explorer" title="Open file explorer"
        >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
            </svg>
            <span class="project-path">{toHomeRelative(currentProject.path)}</span>
            {#if currentBranch}
                <span class="branch-badge" class:main={isMainBranch}>
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="6" cy="6" r="2.5"/>
                        <circle cx="10" cy="10" r="2.5"/>
                        <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                        <line x1="6" y1="3.5" x2="6" y2="2"/>
                        <line x1="10" y1="12.5" x2="10" y2="14"/>
                    </svg>
                    {currentBranch}
                </span>
            {/if}
        </button>
    </span>
    <div class="actions">
        <button class="icon-btn" onclick={onOpenTerminal} aria-label="Open terminal" title={terminalButtonTitle}>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
        <!-- Desktop-only buttons (hidden on mobile via CSS) -->
        <button class="icon-btn desktop-only-btn" onclick={onShowStats} aria-label="Token usage stats" title="Token usage stats">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="8" width="3" height="6" rx="0.5" fill="currentColor"/>
                <rect x="6.5" y="4" width="3" height="10" rx="0.5" fill="currentColor"/>
                <rect x="12" y="1" width="3" height="13" rx="0.5" fill="currentColor"/>
            </svg>
        </button>
        <button class="icon-btn desktop-only-btn" onclick={onShowConfig} aria-label="Settings" title="Settings">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <circle cx="12" cy="12" r="3"/>
                <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
            </svg>
        </button>
    </div>
</header>

<style>
    .top-bar {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 16px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
    }
    .project-title {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .icon-btn.explorer-btn {
        min-width: 0;
        gap: 6px;
        padding-left: 6px;
        font-weight: 500;
        color: var(--color-text-primary);
        justify-content: flex-start;
        position: relative;
        height: 28px;
    }
    .explorer-btn .project-path,
    .explorer-btn .branch-badge {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .branch-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 1px 8px;
        border-radius: 999px;
        font-size: 0.75rem;
        font-weight: 500;
        font-family: var(--font-mono);
        white-space: nowrap;
        border: 1px solid var(--color-branch-border);
        background: var(--color-branch-bg);
        color: var(--color-branch-text);
    }
    .branch-badge.main {
        border-color: var(--color-branch-main-border);
        background: var(--color-branch-main-bg);
        color: var(--color-branch-main-text);
    }
    .actions {
        display: flex;
        gap: 8px;
        flex-shrink: 0;
    }
    .icon-btn {
        padding: 6px 12px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        font-size: 0.875rem;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .icon-btn:hover {
        background: var(--color-accent-dark);
    }
    .icon-btn.sandbox-config-btn {
        color: var(--color-primary);
    }
    .hamburger {
        display: flex;
        position: relative;
    }
    .review-dot {
        position: absolute;
        top: 4px;
        right: 4px;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--color-status-orange);
        pointer-events: none;
    }

    /* Desktop-only buttons — hidden on mobile */
    @media (max-width: 768px) {
        .desktop-only-btn {
            display: none;
        }
    }

    @media (max-width: 768px) {
        .explorer-btn {
            flex: 1;
            min-width: 0;
        }
        .explorer-btn .branch-badge {
            position: absolute;
            top: 50%;
            right: 8px;
            transform: translateY(-50%);
            background: var(--color-branch-solid);
            border-color: var(--color-branch-solid);
            font-size: 0.6875rem;
            padding: 1px 6px;
            color: #fff;
        }
        .explorer-btn .branch-badge.main {
            background: var(--color-branch-main-solid);
            border-color: var(--color-branch-main-solid);
            color: #fff;
        }
    }

    @media (min-width: 769px) {
        .hamburger {
            display: none;
        }
        .explorer-btn {
            flex-shrink: 0;
        }
    }
</style>
