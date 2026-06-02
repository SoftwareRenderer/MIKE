"use client";

<script lang="ts">
    import { onMount } from 'svelte';
    import { projects as projectsStore, selectedProject, board as boardStore, config as configStore, sidebarCollapsed, showCreateTask, showConfig, showTerminal, showCardDetail } from '$lib/store';
    import { getProjects, getBoard, getConfig } from '$lib/api';
    import type { Project, Task, Config } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import TerminalPanel from './TerminalPanel.svelte';

    let projects: Project[] = $state([]);
    let currentProject: Project | null = $state(null);
    let tasks: Task[] = $state([]);
    let cfg: Config | null = $state(null);
    let collapsed = $state(false);

    onMount(async () => {
        try {
            const projs = await getProjects();
            projects = projs;
            projectsStore.set(projs);
            if (projs.length > 0) {
                selectProject(projs[0]);
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
            tasks = b.tasks;
            boardStore.set(b);
        } catch (e) {
            console.error('Failed to load board:', e);
        }
    }

    async function addProject() {
        const path = prompt('Enter project directory path (must be a git repo):');
        if (!path) return;
        try {
            const p = await fetch('/api/projects', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path }),
            });
            if (!p.ok) throw new Error('Failed to create project');
            const proj = await p.json();
            projects.push(proj);
            projectsStore.set(projects);
            selectProject(proj);
        } catch (e) {
            alert('Failed to create project: ' + e);
        }
    }

    function toggleSidebar() {
        collapsed = !collapsed;
        sidebarCollapsed.set(collapsed);
    }

    $effect(() => {
        showCreateTask.get();
        showConfig.get();
        showTerminal.get();
    });
</script>

<div class="app" class:collapsed>
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
                <button
                    class="project-item"
                    class:active={currentProject?.id === p.id}
                    onclick={() => selectProject(p)}
                >
                    <span class="project-name">{p.name}</span>
                </button>
            {/each}
        </nav>
        <div class="sidebar-footer">
            <button class="add-project-btn" onclick={addProject} title="Add Project">+ Project</button>
        </div>
    </aside>

    <!-- Main Content -->
    <main class="main">
        {#if currentProject}
            <top-bar>
                <span class="project-title">{currentProject.name}</span>
                <div class="actions">
                    <button class="icon-btn" onclick={() => showCreateTask.set(true)} title="Add Task">+ Task</button>
                    <button class="icon-btn" onclick={() => showConfig.set(true)} title="Settings">⚙</button>
                </div>
            </top-bar>
            <board-container>
                <Board 
                    {tasks} 
                    projectId={currentProject.id}
                />
            </board-container>
        {:else}
            <div class="empty-state">
                <h2>No Projects</h2>
                <p>Add a project to get started. Projects must be git repositories.</p>
                <button class="btn-primary" onclick={addProject}>Add Project</button>
            </div>
        {/if}
    </main>

    <!-- Modals -->
    {#if showCreateTask.get()}
        <CreateTaskModal 
            projectId={currentProject?.id} 
            on:close={() => showCreateTask.set(false)} 
        />
    {/if}
    {#if showConfig.get()}
        <ConfigModal 
            config={cfg} 
            on:close={() => showConfig.set(false)}
            on:saved={() => showConfig.set(false)}
        />
    {/if}
    {#if showTerminal.get()}
        <TerminalPanel />
    {/if}
    {#if showCardDetail.get()}
        <CardDetail />
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
    .project-item {
        display: flex;
        align-items: center;
        width: 100%;
        padding: 8px 12px;
        border-radius: var(--radius-md);
        text-align: left;
        font-size: 14px;
        color: var(--color-text-primary);
    }
    .project-item:hover {
        background: var(--color-surface-2);
    }
    .project-item.active {
        background: var(--color-surface-3);
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
</style>