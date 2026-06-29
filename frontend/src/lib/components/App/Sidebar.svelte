<script lang="ts">
    import type { Project } from '$lib/types';
    import shieldIcon from '$lib/assets/icons/shield.svg';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg';

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
        onAddProject: () => void;
    } = $props();
</script>

<aside id="sidebar" class="sidebar" class:collapsed class:mobile-open={isMobile && mobileMenuOpen} aria-label="Sidebar">
    <div class="sidebar-header">
        <h2 class:collapsed>Projects</h2>
        {#if isMobile}
            <button class="toggle-btn" onclick={() => mobileMenuOpen = false} aria-label="Close menu" title="Close menu">✕</button>
        {:else}
            <button class="toggle-btn" onclick={onToggleSidebar} aria-expanded={collapsed ? 'false' : 'true'} aria-controls="sidebar" aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
                {collapsed ? '▸' : '◂'}
            </button>
        {/if}
    </div>
    <nav class="project-list" aria-label="Projects">
        {#each projects as p (p.id)}
            <div class="project-item-wrapper" class:active={currentProject?.id === p.id} class:has-review={projectCounts[p.id]?.review > 0}>
                <button
                    class="project-item"
                    onclick={() => onSelectProject(p)}
                    title={`Open project: ${p.name}${p.sandbox_enabled ? ' (sandboxed)' : ''}`}
                >
                    {#if !collapsed}
                        {#if p.sandbox_enabled}
                            <span class="sandbox-badge" aria-label="Sandbox enabled (gVisor)" title="Sandbox enabled (gVisor)">
                                <img src={shieldIcon} width="14" height="14" alt="" />
                            </span>
                        {:else}
                            <span class="sandbox-badge sandbox-badge-off" aria-label="Sandbox disabled" title="Sandbox disabled">
                                <img src={shieldOffIcon} width="14" height="14" alt="" />
                            </span>
                        {/if}
                    {/if}
                    <span class="project-name">{p.name}</span>
                    <span class="project-abbr" class:project-abbr-no-sandbox={!p.sandbox_enabled} title={p.name}>{getProjectAbbr(p.name)}</span>
                    {#if !collapsed && projectCounts[p.id]}
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
                {#if !collapsed}
                <button
                    class="project-remove-btn"
                    onclick={(e) => { e.stopPropagation(); onRemoveProject(p); }}
                    aria-label={`Remove project ${p.name}`}
                    title={`Remove project ${p.name}`}
                >
                    ✕
                </button>
                {/if}
            </div>
        {/each}
    </nav>
    {#if isMobile}
    <div class="sidebar-menu">
        <button class="sidebar-menu-item" onclick={onShowStats} aria-label="Token usage stats" title="Token usage stats">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="8" width="3" height="6" rx="0.5" fill="currentColor"/>
                <rect x="6.5" y="4" width="3" height="10" rx="0.5" fill="currentColor"/>
                <rect x="12" y="1" width="3" height="13" rx="0.5" fill="currentColor"/>
            </svg>
            Token Usage
        </button>
        <button class="sidebar-menu-item" onclick={onShowConfig} aria-label="Settings" title="Settings">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <circle cx="12" cy="12" r="3"/>
                <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
            </svg>
            Settings
        </button>
    </div>
    {/if}
    {#if !collapsed}
    <div class="sidebar-footer">
        <button class="add-project-btn" onclick={onAddProject} aria-label="Add Project" title="Add Project">+ Project</button>
    </div>
    {/if}
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
        font-size: 0.875rem;
        flex-shrink: 0;
    }
    .toggle-btn:hover {
        background: var(--color-surface-2);
        color: var(--color-text-primary);
    }
    .project-list {
        flex: 1;
        overflow-y: auto;
        padding: 0 8px;
    }
    .project-item-wrapper {
        position: relative;
        display: flex;
        align-items: center;
        margin-bottom: 2px;
    }
    .project-item-wrapper.active .project-item {
        background: var(--color-surface-2);
    }
    .project-item-wrapper.has-review::after {
        content: '';
        position: absolute;
        left: 0;
        top: 50%;
        transform: translateY(-50%);
        width: 3px;
        height: 20px;
        background: var(--color-column-review);
        border-radius: 0 2px 2px 0;
    }
    .project-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 8px;
        width: 100%;
        background: none;
        border: none;
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
        cursor: pointer;
        font-size: 0.8125rem;
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
        flex-shrink: 0;
        width: 24px;
        height: 24px;
        border-radius: 50%;
        background: var(--color-accent);
        color: white;
        font-size: 0.6875rem;
        font-weight: 700;
        text-transform: uppercase;
        align-items: center;
        justify-content: center;
        text-align: center;
        line-height: 24px;
    }
    .sidebar.collapsed .project-abbr {
        display: flex;
    }
    .sidebar.collapsed .project-name {
        display: none;
    }
    .sidebar.collapsed .sandbox-badge {
        display: none;
    }
    .project-abbr-no-sandbox {
        background: var(--color-surface-4);
        color: var(--color-text-secondary);
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
        font-size: 0.6875rem;
        font-weight: 500;
        opacity: 0.7;
    }
    .project-remove-btn {
        position: absolute;
        right: 4px;
        top: 50%;
        transform: translateY(-50%);
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 2px 4px;
        border-radius: 3px;
        font-size: 0.75rem;
        opacity: 0;
        transition: all 0.15s ease;
    }
    .project-item-wrapper:hover .project-remove-btn {
        opacity: 1;
    }
    .project-remove-btn:hover {
        background: var(--color-status-red);
        color: white;
    }
    .sidebar-menu {
        padding: 8px;
        border-top: 1px solid var(--color-border);
    }
    .sidebar-menu-item {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        padding: 8px;
        background: none;
        border: none;
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        cursor: pointer;
        font-size: 0.8125rem;
        transition: all 0.15s ease;
    }
    .sidebar-menu-item:hover {
        background: var(--color-surface-2);
        color: var(--color-text-primary);
    }
    .sidebar-footer {
        padding: 8px;
        border-top: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .add-project-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 4px;
        width: 100%;
        padding: 8px;
        background: var(--color-surface-2);
        border: 1px dashed var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        cursor: pointer;
        font-size: 0.8125rem;
        transition: all 0.15s ease;
    }
    .add-project-btn:hover {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
        color: var(--color-accent);
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
        .sidebar.collapsed {
            width: 260px;
            min-width: 260px;
        }
    }
</style>
