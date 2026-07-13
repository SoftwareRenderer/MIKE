<script lang="ts">
    import type { Project } from '$lib/types';
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';

    let {
        projects,
        currentProject,
        projectCounts,
        collapsed,
        isMobile,
        mobileMenuOpen,
        getProjectAbbr,
        onSelectProject,
        onRemoveProject,
        onToggleSidebar,
        onShowStats,
        onShowConfig,
        onToggleFullScreen,
        onAddProject,
    }: {
        projects: Project[];
        currentProject: Project | null;
        projectCounts: Record<string, { backlog: number; in_progress: number; review: number }>;
        collapsed: boolean;
        isMobile: boolean;
        mobileMenuOpen: boolean;
        getProjectAbbr: (name: string) => string;
        onSelectProject: (p: Project) => void | Promise<void>;
        onRemoveProject: (p: Project) => Promise<void>;
        onToggleSidebar: () => void;
        onShowStats: () => void;
        onShowConfig: () => void;
        onToggleFullScreen: () => void;
        onAddProject: () => void;
    } = $props();
</script>

<aside id="sidebar" class="sidebar" class:collapsed={collapsed && !isMobile} class:mobile-open={isMobile && mobileMenuOpen} role="complementary" aria-label="Sidebar">
    <div class="sidebar-header">
        <h2 class:collapsed={!isMobile && collapsed}>Projects</h2>
        <!-- Mobile close button (hidden on desktop via CSS) -->
        <button class="toggle-btn mobile-close-btn" onclick={() => mobileMenuOpen = false} aria-label="Close menu" title="Close menu">✕</button>
        <!-- Desktop collapse toggle (hidden on mobile via CSS) -->
        <button class="toggle-btn desktop-collapse-btn" class:collapsed={collapsed} onclick={onToggleSidebar} aria-expanded={collapsed ? 'false' : 'true'} aria-controls="sidebar" aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    </div>
    <nav class="project-list" aria-label="Projects">
        {#each projects as p (p.id)}
            <div class="project-item-wrapper" class:active={currentProject?.id === p.id} class:has-review={projectCounts[p.id]?.review > 0}>
                <div class="project-item-content">
                    <button
                        class="project-item"
                        onclick={() => onSelectProject(p)}
                        title={projectCounts[p.id]?.review > 0 ? `${p.name} - ${projectCounts[p.id].review} task${projectCounts[p.id].review === 1 ? '' : 's'} available for review` : `Open project: ${p.name}${p.sandbox_enabled ? ' (sandboxed)' : ''}`}
                    >
                        {#if isMobile || !collapsed}
                            {#if p.sandbox_enabled}
                                <span class="sandbox-badge" aria-label="Sandbox enabled (gVisor)" title="Sandbox enabled (gVisor)">
                                    <Icon src={shieldIcon} color="#E6EDF3" width={14} height={14} />
                                </span>
                            {:else}
                                <span class="sandbox-badge sandbox-badge-off" aria-label="Sandbox disabled" title="Sandbox disabled">
                                    <Icon src={shieldOffIcon} color="#F85149" width={14} height={14} />
                                </span>
                            {/if}
                        {/if}
                        <span class="project-name">{p.name}</span>
                        <span class="project-abbr" class:project-abbr-no-sandbox={!p.sandbox_enabled}>
                            {getProjectAbbr(p.name)}
                        </span>
                        {#if (isMobile || !collapsed) && projectCounts[p.id]}
                            <span class="project-stats">
                                <span class="stat-badge" style="color: var(--color-column-backlog)" title="Backlog">
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                                    {projectCounts[p.id].backlog}
                                </span>
                                <span class="stat-badge" style="color: var(--color-column-in-progress)" title="In Progress">
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                                    {projectCounts[p.id].in_progress}
                                </span>
                                <span class="stat-badge" style="color: var(--color-column-review)" title="Review">
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                                    {projectCounts[p.id].review}
                                </span>
                            </span>
                        {/if}
                    </button>
                </div>
                {#if isMobile || !collapsed}
                <button
                    class="project-remove-btn"
                    onclick={(e) => { e.stopPropagation(); onRemoveProject(p); }}
                    aria-label={`Remove project ${p.name}`}
                    title={`Remove project ${p.name}`}
                >
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
                {/if}
            </div>
        {/each}
        {#if isMobile || !collapsed}
            <button class="add-project-btn" onclick={onAddProject} aria-label="Add Project" title="Add Project">+ Add Project</button>
        {/if}
    </nav>
    <!-- Mobile sidebar menu (hidden on desktop via CSS) -->
    <div class="sidebar-menu">
        <button class="sidebar-menu-item" onclick={onToggleFullScreen} aria-label="Toggle full screen" title="Toggle full screen">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M8 3H5a2 2 0 00-2 2v3m18 0V5a2 2 0 00-2-2h-3m0 18h3a2 2 0 002-2v-3M3 16v3a2 2 0 002 2h3"/>
            </svg>
            Full Screen
        </button>
        <button class="sidebar-menu-item" onclick={onShowStats} aria-label="Token usage stats" title="Token usage stats">
            <svg width="20" height="20" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="8" width="3" height="6" rx="0.5" fill="currentColor"/>
                <rect x="6.5" y="4" width="3" height="10" rx="0.5" fill="currentColor"/>
                <rect x="12" y="1" width="3" height="13" rx="0.5" fill="currentColor"/>
            </svg>
            Token Usage
        </button>
        <button class="sidebar-menu-item" onclick={onShowConfig} aria-label="Settings" title="Settings">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <circle cx="12" cy="12" r="3"/>
                <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
            </svg>
            Settings
        </button>
    </div>
</aside>

<style>
    .sidebar {
        display: flex;
        flex-direction: column;
        width: 240px;
        min-width: 240px;
        background: var(--color-surface-0);
        border-right: 1px solid var(--color-border);
        transition: all 0.2s ease;
        overflow: hidden;
        z-index: 100;
    }
    .sidebar.collapsed {
        width: 56px;
        min-width: 56px;
    }
    .sidebar-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 12px 8px;
        flex-shrink: 0;
    }
    .sidebar-header h2 {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-text-tertiary);
        margin: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .sidebar-header h2.collapsed {
        visibility: hidden;
        width: 0;
    }
    .toggle-btn {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 4px;
        border-radius: 4px;
        flex-shrink: 0;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .toggle-btn svg {
        transition: transform 0.15s ease;
        transform: rotate(180deg);
    }
    .toggle-btn.collapsed svg {
        transform: rotate(0deg);
    }
    .toggle-btn:hover {
        background: var(--color-surface-2);
        color: var(--color-text-primary);
    }
    /* Mobile close button — hidden on desktop */
    .mobile-close-btn {
        display: none;
    }
    /* Desktop collapse toggle — shown by default */
    .desktop-collapse-btn {
        display: flex;
    }
    /* Sidebar menu — hidden on desktop, shown on mobile */
    .sidebar-menu {
        display: none;
    }
    .project-list {
        flex: 1;
        overflow-y: auto;
        padding: 0 8px;
    }
    .project-item-wrapper {
        display: flex;
        align-items: center;
        margin-bottom: 4px;
        overflow: hidden;
    }
    .project-item-wrapper.active .project-item {
        background: var(--color-surface-2);
    }
    .project-item-wrapper.has-review .project-item {
        border-left: 3px solid var(--color-column-review);
        border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
        padding-left: 9px;
    }
    .project-item-content {
        flex: 1;
        min-width: 100%;
        overflow: hidden;
        transition: min-width 0.25s cubic-bezier(0.4, 0, 0.2, 1);
    }
    .sidebar.collapsed .project-item {
        padding: 8px 4px;
    }
    .sidebar.collapsed .project-item-content {
        min-width: 0;
        transition: none;
    }
    .project-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 10px 12px;
        width: 100%;
        background: none;
        border: none;
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
        cursor: pointer;
        font-size: 0.9375rem;
        text-align: left;
        transition: all 0.15s ease;
        min-width: 0;
    }
    .project-item:hover {
        background: var(--color-surface-2);
    }
    .sandbox-badge {
        flex-shrink: 0;
        display: flex;
        align-items: center;
        color: var(--color-primary);
    }
    .sandbox-badge-off {
        color: var(--color-status-red, #f85149);
    }
    .project-name {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .project-abbr {
        display: none;
        font-size: 0.875rem;
        font-weight: 600;
        text-align: center;
        width: 100%;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .sidebar.collapsed .project-abbr {
        display: block;
    }
    .sidebar.collapsed .project-name {
        display: none;
    }
    .sidebar.collapsed .sandbox-badge {
        display: none;
    }
    .project-abbr-no-sandbox {
        color: var(--color-text-tertiary);
    }
    .project-stats {
        display: flex;
        gap: 4px;
        flex-shrink: 0;
    }
    .stat-badge {
        display: flex;
        align-items: center;
        gap: 2px;
        font-size: 12px;
        font-weight: 500;
        opacity: 1;
    }
    .project-remove-btn {
        flex-shrink: 0;
        width: 32px;
        height: 40px;
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 4px;
        border-radius: var(--radius-sm);
        font-size: 0.875rem;
        display: flex;
        align-items: center;
        justify-content: center;
        transition: all 0.15s ease;
    }
    .project-remove-btn:hover {
        background: var(--color-status-red-dark);
        color: white;
    }
    .sidebar-menu {
        padding: 8px;
        border-top: 1px solid var(--color-border);
    }
    .sidebar-menu-item {
        display: flex;
        align-items: center;
        gap: 10px;
        width: 100%;
        padding: 12px;
        background: none;
        border: none;
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        cursor: pointer;
        font-size: 0.9375rem;
        transition: all 0.15s ease;
    }
    .sidebar-menu-item:hover {
        background: var(--color-surface-2);
        color: var(--color-text-primary);
    }
    .add-project-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 100%;
        margin: 12px 0 2px;
        padding: 8px;
        background: none;
        border: 1px dashed var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-tertiary);
        cursor: pointer;
        font-size: 0.8125rem;
        transition: all 0.15s ease;
    }
    .add-project-btn:hover {
        border-color: var(--color-accent);
        color: var(--color-accent);
        background: color-mix(in srgb, var(--color-accent) 10%, transparent);
    }

    /* Desktop: shrink content to reveal remove button on hover */
    .project-item-wrapper:hover .project-item-content {
        min-width: 0;
    }

    @media (max-width: 768px) {
        .sidebar {
            position: fixed;
            top: 0;
            left: 0;
            bottom: 0;
            z-index: 1000;
            width: 260px;
            min-width: 260px;
            transform: translateX(-100%);
            transition: transform 0.2s ease;
        }
        .sidebar.mobile-open {
            transform: translateX(0);
        }
        /* Mobile: no shrink, remove button always visible */
        .project-item-content {
            min-width: 0;
        }
        /* Show mobile close button, hide desktop collapse toggle */
        .mobile-close-btn {
            display: flex;
        }
        .desktop-collapse-btn {
            display: none;
        }
        /* Show sidebar menu on mobile */
        .sidebar-menu {
            display: flex;
            flex-direction: column;
        }
    }
</style>
