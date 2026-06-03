
<script lang="ts">
    import { onMount } from 'svelte';
    import { projects as projectsStore, selectedProject, board as boardStore, config as configStore, sidebarCollapsed, showCreateTask, showConfig, showTerminal, showCardDetail, selectedTask } from '$lib/store';
    import { getProjects, getBoard, getConfig, deleteProject } from '$lib/api';
    import type { Project, Task, Config } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import TerminalPanel from './TerminalPanel.svelte';
    import CreateTaskModal from './CreateTaskModal.svelte';
    import ConfigModal from './ConfigModal.svelte';
    import Board from './Board.svelte';
    import FileBrowser from './FileBrowser.svelte';

    let projects: Project[] = $state([]);
    let currentProject: Project | null = $state(null);
    let tasks: Task[] = $state([]);
    let cfg: Config | null = $state(null);
    let collapsed = $state(false);

    // Local state mirroring stores
    let showCreateTaskVal = $state(false);
    let showConfigVal = $state(false);
    let showTerminalVal = $state(false);
    let showCardDetailVal = $state(false);
    let showFileBrowserVal = $state(false);
    let addProjectError = $state('');
    let selectedTaskVal: Task | null = $state(null);

    // Subscribe to Svelte 4 stores on mount
    onMount(() => {
        const unsubCreate = showCreateTask.subscribe(v => showCreateTaskVal = v);
        const unsubConfig = showConfig.subscribe(v => showConfigVal = v);
        const unsubTerminal = showTerminal.subscribe(v => showTerminalVal = v);
        const unsubCard = showCardDetail.subscribe(v => showCardDetailVal = v);
        const unsubTask = selectedTask.subscribe(t => selectedTaskVal = t);
        return () => { unsubCreate(); unsubConfig(); unsubTerminal(); unsubCard(); unsubTask(); };
    });

    onMount(async () => {
        try {
            const projs = await getProjects();
            projects = projs || [];
            projectsStore.set(projects);
            if (projects.length > 0) {
                selectProject(projects[0]);
            }
            const c = await getConfig();
            cfg = c;
            configStore.set(c);
        } catch (e) {
            console.error('Failed to load:', e);
        }
    });

    async function selectProject(p: Project) {
        currentProject = p;
        selectedProject.set(p);
        try {
            const b = await getBoard(p.id);
            tasks = b.tasks || [];
            boardStore.set(b);
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
        if (!confirm(`Remove "${p.name}" from the board?\n\nThis will remove the project and all its tasks from the Kanban board. No files will be deleted.`)) {
            return;
        }
        try {
            await deleteProject(p.id);
            const index = projects.indexOf(p);
            projects.splice(index, 1);
            projectsStore.set(projects);
            // Auto-select next project (or previous if it was the last)
            if (projects.length > 0) {
                const nextIndex = Math.min(index, projects.length - 1);
                selectProject(projects[nextIndex]);
            } else {
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
    <!-- Sidebar -->
    <aside class="sidebar">
        <div class="sidebar-header">
            <h2 class:collapsed>Kanban</h2>
            <button class="toggle-btn" onclick={toggleSidebar}>
                {collapsed ? '▸' : '◂'}
            </button>
        </div>
        <nav class="project-list">
            {#each projects as p (p.id)}
                <div class="project-item-wrapper" class:active={currentProject?.id === p.id}>
                    <button
                        class="project-item"
                        onclick={() => selectProject(p)}
                    >
                        <span class="project-name">{p.name}</span>
                    </button>
                    <button
                        class="project-remove-btn"
                        onclick={(e) => { e.stopPropagation(); removeProject(p); }}
                        title="Remove project"
                    >
                        ✕
                    </button>
                </div>
            {/each}
        </nav>
        <div class="sidebar-footer">
            <button class="add-project-btn" onclick={openAddProject} title="Add Project">+ Project</button>
        </div>
    </aside>

    <!-- Main Content -->
    <main class="main">
        {#if currentProject}
            <top-bar>
                <span class="project-title">{currentProject.name}</span>
                <div class="actions">
                    <button class="icon-btn" onclick={() => showConfig.set(true)} title="Settings">⚙</button>
                </div>
            </top-bar>
            <board-container>
                {#if showCardDetailVal && selectedTaskVal}
                    <CardDetail task={selectedTaskVal} tasks={tasks} onClose={() => showCardDetail.set(false)} />
                {:else}
                    <Board 
                        {tasks} 
                        projectId={currentProject.id}
                        onAddTask={() => showCreateTask.set(true)}
                    />
                {/if}
            </board-container>
        {:else}
            <div class="empty-state">
                <h2>No Projects</h2>
                <p>Add a project to get started. Projects must be git repositories.</p>
                <button class="btn-primary" onclick={openAddProject}>Add Project</button>
            </div>
        {/if}
    </main>

    <!-- Modals -->
    {#if showCreateTaskVal}
        <CreateTaskModal
            projectId={currentProject?.id}
            onClose={() => showCreateTask.set(false)}
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
    {#if showFileBrowserVal}
        <FileBrowser
            onClose={() => showFileBrowserVal = false}
            onSelect={handleSelectProjectPath}
        />
    {/if}
    {#if addProjectError}
        <div class="error-toast">{addProjectError}</div>
    {/if}
</div>

<svelte:head>
    <title>Kanban Board</title>
</svelte:head>

<style>
    .app {
        display: flex;
        height: 100vh;
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
        font-size: 16px;
        font-weight: 600;
    }
    .collapsed .sidebar-header h2 {
        display: none;
    }
    .toggle-btn {
        font-size: 12px;
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
        width: 100%;
        padding: 8px 12px;
        border-radius: var(--radius-md);
        text-align: left;
        font-size: 14px;
        color: var(--color-text-primary);
        background: transparent;
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
        width: 24px;
        height: 24px;
        padding: 0;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-status-red);
        font-size: 14px;
        opacity: 0;
        transition: opacity 0.15s ease;
        flex-shrink: 0;
        cursor: pointer;
    }
    .project-remove-btn:hover {
        background: var(--color-status-red);
        color: white;
    }
    .project-name {
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .collapsed .project-name {
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
        font-size: 13px;
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
    top-bar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 16px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
    }
    .project-title {
        font-size: 14px;
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
        font-size: 13px;
    }
    .icon-btn:hover {
        background: var(--color-accent);
    }
    board-container {
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
    }
    .empty-state h2 {
        font-size: 20px;
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
        font-size: 14px;
        z-index: 400;
        box-shadow: 0 4px 12px rgba(0,0,0,0.3);
    }
</style>