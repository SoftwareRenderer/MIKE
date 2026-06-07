
<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { projects as projectsStore, selectedProject, board as boardStore, config as configStore, sidebarCollapsed, showCreateTask, showConfig, showTerminal, showCardDetail, selectedTask } from '$lib/store';
    import { getProjects, getBoard, getConfig, deleteProject, createBoardStream } from '$lib/api';
    import type { Project, Task, Config } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import TerminalPanel from './TerminalPanel.svelte';
    import CreateTaskModal from './CreateTaskModal.svelte';
    import ConfigModal from './ConfigModal.svelte';
    import Board from './Board.svelte';
    import FileBrowser from './FileBrowser.svelte';
    import Toast from './Toast.svelte';

    let projects: Project[] = $state([]);
    let currentProject: Project | null = $state(null);
    let tasks: Task[] = $state([]);
    let cfg: Config | null = $state(null);
    let collapsed = $state(false);
    let boardStream: ReturnType<typeof createBoardStream> | null = $state(null);

    // Local state mirroring stores
    let showCreateTaskVal = $state(false);
    let showConfigVal = $state(false);
    let showTerminalVal = $state(false);
    let showCardDetailVal = $state(false);
    let showFileBrowserVal = $state(false);
    let addProjectError = $state('');
    let selectedTaskVal: Task | null = $state(null);
    let mobileMenuOpen = $state(false);
    let isMobile = $derived(false);

    $effect(() => {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.addEventListener('resize', check);
        return () => window.removeEventListener('resize', check);
    });

    // Subscribe to Svelte 4 stores on mount
    onMount(() => {
        const unsubCreate = showCreateTask.subscribe(v => showCreateTaskVal = v);
        const unsubConfig = showConfig.subscribe(v => showConfigVal = v);
        const unsubTerminal = showTerminal.subscribe(v => showTerminalVal = v);
        const unsubCard = showCardDetail.subscribe(v => showCardDetailVal = v);
        // Sync config store changes to local cfg (e.g., after ConfigModal saves)
        const unsubCfgStore = configStore.subscribe(v => { if (v) cfg = v; });
        const unsubTask = selectedTask.subscribe(t => {
            selectedTaskVal = t;
            // Update URL when task changes
            if (t) {
                const currentUrl = new URL(location.href);
                if (currentUrl.searchParams.get('task') !== t.id) {
                    history.pushState({ task: t.id }, '', '?task=' + t.id);
                }
            }
            // Don't clear URL here - let popstate handler manage it
        });

        // Handle browser back/forward
        function handlePopState() {
            const params = new URLSearchParams(location.search);
            const taskId = params.get('task');
            if (taskId) {
                const task = tasks.find(t => t.id === taskId);
                if (task) {
                    selectedTaskVal = task;
                    showCardDetailVal = true;
                    selectedTask.set(task);
                }
            } else {
                selectedTaskVal = null;
                showCardDetailVal = false;
                selectedTask.set(null);
            }
        }
        window.addEventListener('popstate', handlePopState);

        return () => {
            unsubCreate(); unsubConfig(); unsubTerminal(); unsubCard(); unsubTask(); unsubCfgStore();
            window.removeEventListener('popstate', handlePopState);
        };
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
        } catch (e) {
            console.error('Failed to refresh board:', e);
        }
    }

    async function selectProject(p: Project) {
        disconnectBoardStream();
        currentProject = p;
        selectedProject.set(p);
        try {
            const b = await getBoard(p.id);
            tasks = b.tasks || [];
            boardStore.set(b);
            // Restore task from URL after board loads
            restoreTaskFromUrl();
            // Connect board stream for real-time updates
            boardStream = createBoardStream(p.id, () => refreshBoard());
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
    <!-- Mobile sidebar backdrop -->
    {#if isMobile && mobileMenuOpen}
        <div class="sidebar-backdrop" onclick={() => mobileMenuOpen = false}></div>
    {/if}

    <!-- Sidebar -->
    <aside class="sidebar" class:collapsed class:mobile-open={isMobile && mobileMenuOpen}>
        <div class="sidebar-header">
            <h2 class:collapsed>Kanban</h2>
            {#if isMobile}
                <button class="toggle-btn" onclick={() => mobileMenuOpen = false}>✕</button>
            {:else}
                <button class="toggle-btn" onclick={toggleSidebar}>
                    {collapsed ? '▸' : '◂'}
                </button>
            {/if}
        </div>
        <nav class="project-list">
            {#each projects as p (p.id)}
                <div class="project-item-wrapper" class:active={currentProject?.id === p.id}>
                    <button
                        class="project-item"
                        onclick={() => { selectProject(p); if (isMobile) mobileMenuOpen = false; }}
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
                {#if isMobile}
                    <button class="icon-btn hamburger" onclick={() => mobileMenuOpen = true} title="Menu">
                        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <line x1="3" y1="6" x2="21" y2="6"/>
                            <line x1="3" y1="12" x2="21" y2="12"/>
                            <line x1="3" y1="18" x2="21" y2="18"/>
                        </svg>
                    </button>
                {/if}
                <span class="project-title">{currentProject.name}</span>
                <div class="actions">
                    <button class="icon-btn" onclick={() => showConfig.set(true)} title="Settings">⚙</button>
                </div>
            </top-bar>
            <board-container>
                {#if showCardDetailVal && selectedTaskVal}
                    <CardDetail task={selectedTaskVal} tasks={tasks} onClose={() => {
                        showCardDetail.set(false);
                        selectedTask.set(null);
                        history.pushState({ task: null }, '', location.pathname);
                    }} />
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
            onTaskCreated={() => refreshBoard()}
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
    <Toast />
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
            font-size: 16px !important;
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
        top-bar {
            padding: 8px 12px;
            gap: 8px;
        }
        .project-title {
            flex: 1;
            text-align: center;
        }
    }
</style>