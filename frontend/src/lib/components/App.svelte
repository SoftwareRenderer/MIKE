
<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { projects as projectsStore, selectedProject, board as boardStore, config as configStore, sidebarCollapsed, showCreateTask, showConfig, showTerminal, showStats, showCardDetail, selectedTask, taskDiffStats as taskDiffStatsStore, showConfirm } from '$lib/store';
    import { getProjects, getBoard, getConfig, deleteProject, createBoardStream, getProjectCounts } from '$lib/api';
import { getHomeRelativePath, getWorktreeDisplayPath } from '$lib/utils/path';
    import type { Project, Task, Config } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import TerminalPanel from './TerminalPanel.svelte';
    import CreateTaskModal from './CreateTaskModal.svelte';
    import ConfigModal from './ConfigModal.svelte';
    import StatsModal from './StatsModal.svelte';
    import Board from './Board.svelte';
    import FileBrowser from './FileBrowser.svelte';
    import Toast from './Toast.svelte';
    import ConfirmationDialog from './ConfirmationDialog.svelte';

    function getProjectAbbr(name: string): string {
        const words = name.split(/[-_\s]+/).filter(Boolean);
        if (words.length >= 2) {
            return (words[0][0] + words[1][0]).toUpperCase();
        }
        return name.slice(0, 2).toUpperCase();
    }

    /**
     * Update the URL to reflect the current navigation state.
     * Sets project/task as provided, leaves other params (e.g., `thread`) untouched.
     * Uses replaceState to avoid polluting browser history.
     * @param clearThread Also remove the `thread` param (use when leaving task detail).
     */
    function updateUrl(params: { project?: string | null; task?: string | null }, clearThread = false) {
        const url = new URL(location.href);
        url.searchParams.delete('project');
        url.searchParams.delete('task');
        if (clearThread) url.searchParams.delete('thread');
        if (params.project) url.searchParams.set('project', params.project);
        if (params.task) url.searchParams.set('task', params.task);
        const newUrl = location.pathname + (url.search || '');
        if (location.pathname + location.search !== newUrl) {
            history.replaceState(null, '', newUrl);
        }
    }

    let projects: Project[] = $state([]);
    let currentProject: Project | null = $state(null);
    let tasks: Task[] = $state([]);
    let cfg: Config | null = $state(null);
    let collapsed = $state(false);
    let boardStream: ReturnType<typeof createBoardStream> | null = $state(null);
    let projectCounts: Record<string, { backlog: number; in_progress: number; review: number }> = $state({});
    let taskDiffStats: Record<string, string> = $state({});
    let showCreateTaskVal = $state(false);
    let showConfigVal = $state(false);
    let showTerminalVal = $state(false);
    let showStatsVal = $state(false);
    let showCardDetailVal = $state(false);
    let showFileBrowserVal = $state(false);
    let addProjectError = $state('');
    let selectedTaskVal: Task | null = $state(null);
    let creatingChildOf: string | null = $state(null);
    let mobileMenuOpen = $state(false);
    let isMobile = $derived(false);
    let hasProviders = $derived(!!cfg?.providers?.length);

    $effect(() => {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.addEventListener('resize', check);
        return () => window.removeEventListener('resize', check);
    });

    // Sync Svelte 4 stores → local $state (single subscription, no onMount needed)
    $effect(() => {
        const unsub = [
            sidebarCollapsed.subscribe(v => collapsed = v),
            showCreateTask.subscribe(v => showCreateTaskVal = v),
            showConfig.subscribe(v => showConfigVal = v),
            showTerminal.subscribe(v => showTerminalVal = v),
            showStats.subscribe(v => showStatsVal = v),
            showCardDetail.subscribe(v => showCardDetailVal = v),
            configStore.subscribe(v => { if (v) cfg = v; }),
            selectedTask.subscribe(t => {
                selectedTaskVal = t;
                if (t && currentProject) {
                    updateUrl({ project: currentProject.id, task: t.id });
                }
            }),
            taskDiffStatsStore.subscribe(v => taskDiffStats = v),
        ].map(unsub => unsub);
        return () => unsub.forEach(u => u());
    });

    // Handle browser back/forward
    $effect(() => {
        function handlePopState() {
            const params = new URLSearchParams(location.search);
            const projectId = params.get('project');
            const taskId = params.get('task');

            // If URL specifies a different project, switch to it
            if (projectId && currentProject && projectId !== currentProject.id) {
                const targetProject = projects.find(p => p.id === projectId);
                if (targetProject) {
                    selectProject(targetProject);
                    // After board loads, restoreTaskFromUrl will handle the task
                    return;
                }
            }

            if (taskId) {
                const task = tasks.find(t => t.id === taskId);
                if (task) {
                    selectedTaskVal = task;
                    showCardDetailVal = true;
                    selectedTask.set(task);
                    showCardDetail.set(true);
                }
            } else {
                selectedTaskVal = null;
                showCardDetailVal = false;
                selectedTask.set(null);
                showCardDetail.set(false);
            }
            // Notify child components (e.g., TaskChat) that the URL changed
            window.dispatchEvent(new CustomEvent('urlchanged'));
        }
        window.addEventListener('popstate', handlePopState);
        return () => window.removeEventListener('popstate', handlePopState);
    });

    onMount(async () => {
        try {
            const projs = await getProjects();
            projects = projs || [];
            projectsStore.set(projects);
            if (projects.length > 0) {
                // Check URL for project param to restore previous project
                const urlParams = new URLSearchParams(location.search);
                const urlProjectId = urlParams.get('project');
                const targetProject = urlProjectId
                    ? projects.find(p => p.id === urlProjectId)
                    : null;
                selectProject(targetProject || projects[0]);
            }
            const c = await getConfig();
            cfg = c;
            configStore.set(c);
            // Fetch initial project counts
            const counts = await getProjectCounts();
            projectCounts = counts;
        } catch (e) {
            console.error('Failed to load:', e);
        }
    });

    onDestroy(() => {
        disconnectBoardStream();
    });

    // Function to restore task from URL after board loads
    function restoreTaskFromUrl() {
        const params = new URLSearchParams(location.search);
        const taskId = params.get('task');
        if (taskId) {
            const task = tasks.find(t => t.id === taskId);
            if (task) {
                selectedTaskVal = task;
                showCardDetailVal = true;
                selectedTask.set(task);
            } else {
                // Task not found (e.g., deleted) — clear task param, keep project
                const url = new URL(location.href);
                url.searchParams.delete('task');
                history.replaceState(null, '', location.pathname + url.search);
            }
        }
    }

    function disconnectBoardStream() {
        if (boardStream) {
            boardStream.close();
            boardStream = null;
        }
    }

    async function refreshBoard() {
        if (!currentProject) return;
        try {
            const b = await getBoard(currentProject.id);
            tasks = b.tasks || [];
            boardStore.set(b);
            // Update selected task reference to point to fresh object from API
            if (selectedTaskVal) {
                const fresh = tasks.find(t => t.id === selectedTaskVal.id);
                if (fresh) {
                    selectedTaskVal = fresh;
                    selectedTask.set(fresh);
                }
            }
        } catch (e) {
            console.error('Failed to refresh board:', e);
        }
    }

    function clearTaskDetail() {
        selectedTaskVal = null;
        showCardDetailVal = false;
        selectedTask.set(null);
        showCardDetail.set(false);
        updateUrl({ project: currentProject?.id }, true);
    }

    async function selectProject(p: Project) {
        disconnectBoardStream();
        currentProject = p;
        selectedProject.set(p);
        // Update URL with project param
        // Preserve task param from URL — restoreTaskFromUrl needs it after board loads
        const urlTaskId = new URLSearchParams(location.search).get('task');
        updateUrl({ project: p.id, task: selectedTaskVal?.id || urlTaskId });
        try {
            const b = await getBoard(p.id);
            tasks = b.tasks || [];
            boardStore.set(b);
            // Restore task from URL after board loads
            restoreTaskFromUrl();
            // Connect board stream for real-time updates
            boardStream = createBoardStream(p.id, () => refreshBoard(), (counts) => {
                projectCounts = counts;
            }, (task_id, short_stat, untracked_n, untracked_lines) => {
                const stat = short_stat || '';
                const fileMatch = stat.match(/(\d+) file\w* changed/);
                const insMatch = stat.match(/(\d+) insertion\w*/);
                const delMatch = stat.match(/(\d+) deletion\w*/);
                const trackedFiles = fileMatch ? parseInt(fileMatch[1]) : 0;
                const untracked = untracked_n || 0;
                const totalFiles = trackedFiles + untracked;
                const additions = (insMatch ? parseInt(insMatch[1]) : 0) + (untracked_lines || 0);
                const deletions = delMatch ? parseInt(delMatch[1]) : 0;
                if (totalFiles > 0) {
                    taskDiffStatsStore.update(map => {
                        map[task_id] = { files: totalFiles, additions, deletions };
                        return map;
                    });
                } else {
                    taskDiffStatsStore.update(map => {
                        delete map[task_id];
                        return map;
                    });
                }
            });
        } catch (e) {
            console.error('Failed to load board:', e);
        }
    }

    function openAddProject() {
        showFileBrowserVal = true;
        addProjectError = '';
    }

    async function handleSelectProjectPath(path: string) {
        showFileBrowserVal = false;
        addProjectError = '';
        try {
            const p = await fetch('/api/projects', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path }),
            });
            if (!p.ok) {
                const err = await p.json();
                throw new Error(err.error || 'Failed to create project');
            }
            const proj = await p.json();
            projects.push(proj);
            projectsStore.set(projects);
            clearTaskDetail();
            selectProject(proj);
        } catch (e) {
            addProjectError = String(e);
        }
    }

    function toggleSidebar() {
        collapsed = !collapsed;
        sidebarCollapsed.set(collapsed);
    }

    async function removeProject(p: Project) {
        const confirmed = await showConfirm({
            title: 'Remove Project',
            message: `Remove "${p.name}" from the board?\n\nThis will delete the project's Kanban board, all tasks, messages, and their worktree directories.\nThe project folder itself will NOT be deleted.`,
            confirmText: 'Remove',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        try {
            await deleteProject(p.id);
            const index = projects.indexOf(p);
            projects.splice(index, 1);
            projectsStore.set(projects);
            // Auto-select next project (or previous if it was the last)
            if (projects.length > 0) {
                const nextIndex = Math.min(index, projects.length - 1);
                clearTaskDetail();
                selectProject(projects[nextIndex]);
            } else {
                clearTaskDetail();
                currentProject = null;
                selectedProject.set(null);
                tasks = [];
                boardStore.set(null);
            }
        } catch (e) {
            addProjectError = String(e);
        }
    }
</script>

<div class="app" class:collapsed class:task-detail={showCardDetailVal && !!selectedTaskVal}>
    <!-- Skip navigation link -->
    <a href="#main-content" class="skip-link">Skip to main content</a>

    <!-- Mobile sidebar backdrop -->
    {#if isMobile && mobileMenuOpen}
        <button type="button" class="sidebar-backdrop" onclick={() => mobileMenuOpen = false} aria-label="Close menu" title="Close menu"></button>
    {/if}

    <!-- Sidebar -->
    <aside id="sidebar" class="sidebar" class:collapsed class:mobile-open={isMobile && mobileMenuOpen} aria-label="Sidebar">
        <div class="sidebar-header">
            <h2 class:collapsed>Kanban</h2>
            {#if isMobile}
                <button class="toggle-btn" onclick={() => mobileMenuOpen = false} aria-label="Close menu" title="Close menu">✕</button>
            {:else}
                <button class="toggle-btn" onclick={toggleSidebar} aria-expanded={collapsed ? 'false' : 'true'} aria-controls="sidebar" aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
                    {collapsed ? '▸' : '◂'}
                </button>
            {/if}
        </div>
        <nav class="project-list" aria-label="Projects">
            {#each projects as p (p.id)}
                <div class="project-item-wrapper" class:active={currentProject?.id === p.id}>
                    <button
                        class="project-item"
                        onclick={() => { clearTaskDetail(); selectProject(p); if (isMobile) mobileMenuOpen = false; }}
                        title={`Open project: ${p.name}`}
                    >
                        <span class="project-name">{p.name}</span>
                        <span class="project-abbr" title={p.name}>{getProjectAbbr(p.name)}</span>
                        {#if !collapsed && projectCounts[p.id]}
                            <span class="project-stats">
                                <span class="stat-badge" style="color: var(--color-column-backlog)">
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                                    {projectCounts[p.id].backlog}
                                </span>
                                <span class="stat-badge" style="color: var(--color-column-in-progress)">
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                                    {projectCounts[p.id].in_progress}
                                </span>
                                <span class="stat-badge" style="color: var(--color-column-review)">
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                                    {projectCounts[p.id].review}
                                </span>
                            </span>
                        {/if}
                    </button>
                    {#if !collapsed}
                    <button
                        class="project-remove-btn"
                        onclick={(e) => { e.stopPropagation(); removeProject(p); }}
                        aria-label={`Remove project ${p.name}`}
                        title={`Remove project ${p.name}`}
                    >
                        ✕
                    </button>
                    {/if}
                </div>
            {/each}
        </nav>
        {#if !collapsed}
        <div class="sidebar-footer">
            <button class="add-project-btn" onclick={openAddProject} aria-label="Add Project" title="Add Project">+ Project</button>
        </div>
        {/if}
    </aside>

    <!-- Main Content -->
    <main id="main-content" class="main" tabindex="-1">
        {#if currentProject}
            {#if !(showCardDetailVal && selectedTaskVal)}
            <header class="top-bar">
                {#if isMobile}
                    <button class="icon-btn hamburger" onclick={() => mobileMenuOpen = true} aria-label="Open menu" title="Open menu">
                        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <line x1="3" y1="6" x2="21" y2="6"/>
                            <line x1="3" y1="12" x2="21" y2="12"/>
                            <line x1="3" y1="18" x2="21" y2="18"/>
                        </svg>
                    </button>
                {/if}
                <span class="project-title">{getHomeRelativePath(currentProject.path)}</span>
                <div class="actions">
                    <button class="icon-btn" onclick={() => showTerminal.set(true)} aria-label="Open terminal" title="Open terminal">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                            <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                    <button class="icon-btn" onclick={() => showStats.set(true)} aria-label="Token usage stats" title="Token usage stats">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <rect x="1" y="8" width="3" height="6" rx="0.5" fill="currentColor"/>
                            <rect x="6.5" y="4" width="3" height="10" rx="0.5" fill="currentColor"/>
                            <rect x="12" y="1" width="3" height="13" rx="0.5" fill="currentColor"/>
                        </svg>
                    </button>
                    <button class="icon-btn" onclick={() => showConfig.set(true)} aria-label="Settings" title="Settings">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <circle cx="12" cy="12" r="3"/>
                            <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
                        </svg>
                    </button>
                </div>
            </header>
            {/if}
            <section class="board-container" aria-label="Kanban board">
                {#if showCardDetailVal && selectedTaskVal}
                    <CardDetail task={selectedTaskVal} tasks={tasks} projectPath={currentProject?.path} workspaceDir={cfg?.workspace_dir} onClose={() => {
                        showCardDetail.set(false);
                        selectedTask.set(null);
                        updateUrl({ project: currentProject?.id }, true);
                    }} onSelectTask={(t: Task) => {
                        selectedTaskVal = t;
                        selectedTask.set(t);
                        updateUrl({ project: currentProject?.id, task: t.id }, true);
                    }} />
                {:else}
                    <Board 
                        {tasks} 
                        projectId={currentProject.id}
                        onAddTask={() => { creatingChildOf = null; showCreateTask.set(true); }}
                        onCreateChildTask={(parentId: string) => { creatingChildOf = parentId; showCreateTask.set(true); }}
                    />
                {/if}
            </section>
        {:else}
            <div class="empty-state">
                <button class="icon-btn" onclick={() => showConfig.set(true)} aria-label="Settings" title="Settings">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="12" cy="12" r="3"/>
                        <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
                    </svg>
                </button>
                {#if !hasProviders}
                    <h2>Welcome to Kanban</h2>
                    <p>Configure an LLM provider to get started. Supports any OpenAI-compatible API.</p>
                    <button class="btn-primary" onclick={() => showConfig.set(true)} title="Configure an LLM provider">Configure Provider</button>
                {:else}
                    <h2>No Projects</h2>
                    <p>Add a project to get started. Projects must be git repositories.</p>
                    <button class="btn-primary" onclick={openAddProject} title="Add a project">Add Project</button>
                {/if}
            </div>
        {/if}
    </main>

    <!-- Modals -->
    {#if showCreateTaskVal}
        <CreateTaskModal
            projectId={currentProject?.id}
            parentId={creatingChildOf ?? undefined}
            onClose={() => { showCreateTask.set(false); creatingChildOf = null; }}
            onTaskCreated={() => { refreshBoard(); creatingChildOf = null; }}
        />
    {/if}
    {#if showConfigVal}
        <ConfigModal
            config={cfg}
            onClose={() => showConfig.set(false)}
            onSaved={() => showConfig.set(false)}
        />
    {/if}
    {#if showTerminalVal}
        <TerminalPanel />
    {/if}
    {#if showStatsVal}
        <StatsModal
            projectId={currentProject?.id}
            onClose={() => showStats.set(false)}
        />
    {/if}
    {#if showFileBrowserVal}
        <FileBrowser
            onClose={() => showFileBrowserVal = false}
            onSelect={handleSelectProjectPath}
        />
    {/if}
    {#if addProjectError}
        <div class="error-toast">{addProjectError}</div>
    {/if}
    <Toast />
    <ConfirmationDialog />
</div>

<svelte:head>
    <title>Kanban Board</title>
</svelte:head>

<style>
    /* -- Global mobile styles -- */
    @media (max-width: 768px) {
        :global(input),
        :global(textarea),
        :global(select) {
            font-size: 1rem !important;
        }

        :global(*) {
            -webkit-tap-highlight-color: transparent;
        }

        :global(button),
        :global(a),
        :global([role="button"]) {
            touch-action: manipulation;
        }

        :global(.action-btn),
        :global(.btn-icon),
        :global(.icon-btn),
        :global(.toggle-btn),
        :global(.close-btn),
        :global(.btn-back),
        :global(.btn-send),
        :global(.btn-stop),
        :global(.btn-icon-sm) {
            min-width: 44px;
            min-height: 44px;
        }
    }

    .skip-link {
        position: absolute;
        top: -100%;
        left: 16px;
        z-index: 10000;
        padding: 8px 16px;
        background: var(--color-accent);
        color: white;
        font-weight: 600;
        border-radius: var(--radius-md);
        text-decoration: none;
        white-space: nowrap;
    }
    .skip-link:focus {
        top: 16px;
    }

    .app {
        display: flex;
        height: 100dvh;
        overflow: hidden;
    }
    .sidebar {
        width: 240px;
        background: var(--color-surface-1);
        border-right: 1px solid var(--color-border);
        display: flex;
        flex-direction: column;
        transition: width 0.2s ease;
        flex-shrink: 0;
    }
    .app.collapsed .sidebar {
        width: 48px;
    }
    .app.task-detail .sidebar {
        display: none;
    }
    .sidebar-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px;
        border-bottom: 1px solid var(--color-border);
    }
    .sidebar-header h2 {
        font-size: 1rem;
        font-weight: 600;
    }
    .collapsed .sidebar-header h2 {
        display: none;
    }
    .toggle-btn {
        font-size: 0.75rem;
        padding: 4px 8px;
        color: var(--color-text-secondary);
    }
    .project-list {
        flex: 1;
        overflow-y: auto;
        padding: 8px;
    }
    .project-item-wrapper {
        display: flex;
        align-items: center;
        border-radius: var(--radius-md);
        position: relative;
    }
    .project-item-wrapper:hover .project-remove-btn {
        opacity: 1;
    }
    .project-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        width: 100%;
        padding: 8px 12px;
        border-radius: var(--radius-md);
        text-align: left;
        font-size: 0.875rem;
        color: var(--color-text-primary);
        background: transparent;
    }
    .collapsed .project-item {
        padding: 8px 4px;
    }
    .project-name {
        flex: 1 1 0;
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .project-item:hover {
        background: var(--color-surface-2);
    }
    .project-item-wrapper.active .project-item {
        background: var(--color-surface-3);
    }
    .project-remove-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 32px;
        height: 32px;
        padding: 0;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-status-red);
        font-size: 1.125rem;
        opacity: 0;
        transition: opacity 0.15s ease;
        flex-shrink: 0;
        cursor: pointer;
    }
    .project-remove-btn:hover {
        background: var(--color-status-red);
        color: white;
    }
    @media (hover: none) {
        .project-remove-btn {
            opacity: 1;
            transition: none;
        }
    }
    .project-name {
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
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
    .collapsed .project-name {
        display: none;
    }
    .collapsed .project-abbr {
        display: block;
    }
    .project-stats {
        display: flex;
        gap: 6px;
        margin-left: 8px;
        flex-shrink: 0;
    }
    .stat-badge {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 0.75rem;
        font-weight: 500;
        font-variant-numeric: tabular-nums;
        opacity: 1;
    }
    .stat-badge svg {
        flex-shrink: 0;
    }
    .collapsed .project-stats {
        display: none;
    }
    .sidebar-footer {
        padding: 8px;
        border-top: 1px solid var(--color-border);
    }
    .add-project-btn {
        width: 100%;
        padding: 8px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        font-size: 0.875rem;
        color: var(--color-text-secondary);
    }
    .add-project-btn:hover {
        background: var(--color-surface-4);
    }
    .main {
        flex: 1;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .top-bar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 16px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
    }
    .project-title {
        font-size: 0.875rem;
        font-weight: 600;
    }
    .actions {
        display: flex;
        gap: 8px;
    }
    .icon-btn {
        padding: 6px 12px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        font-size: 0.875rem;
        display: inline-flex;
        align-items: center;
        justify-content: center;
    }
    .icon-btn:hover {
        background: var(--color-accent);
    }
    .board-container {
        flex: 1;
        overflow: hidden;
    }
    .empty-state {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        height: 100%;
        gap: 12px;
        color: var(--color-text-secondary);
        text-align: center;
        position: relative;
    }
    .empty-state .icon-btn {
        position: absolute;
        top: 12px;
        right: 12px;
    }
    .empty-state h2 {
        font-size: 1.25rem;
        color: var(--color-text-primary);
    }
    .btn-primary {
        padding: 8px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent);
        color: white;
        font-weight: 500;
    }
    .btn-primary:hover {
        background: var(--color-accent-hover);
    }
    .error-toast {
        position: fixed;
        bottom: 24px;
        left: 50%;
        transform: translateX(-50%);
        background: var(--color-status-red);
        color: white;
        padding: 10px 20px;
        border-radius: var(--radius-md);
        font-size: 0.875rem;
        z-index: 400;
        box-shadow: 0 4px 12px rgba(0,0,0,0.3);
    }
    .sidebar-backdrop {
        display: none;
    }
    .hamburger {
        display: none;
    }

    @media (max-width: 768px) {
        .sidebar {
            position: fixed;
            top: 0;
            left: 0;
            bottom: 0;
            width: 280px;
            z-index: 200;
            transform: translateX(-100%);
            transition: transform 0.25s ease;
            box-shadow: none;
        }
        .sidebar.mobile-open {
            transform: translateX(0);
            box-shadow: 4px 0 24px rgba(0,0,0,0.4);
        }
        .sidebar-backdrop {
            display: block;
            position: fixed;
            inset: 0;
            background: rgba(0,0,0,0.5);
            z-index: 199;
        }
        .hamburger {
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .top-bar {
            padding: 8px 12px;
            gap: 8px;
            justify-content: flex-start;
        }
        .sidebar-header {
            padding: 10px 12px;
        }
        .project-list {
            padding: 4px;
        }
        .project-title {
            flex: 1;
        }
    }
</style>