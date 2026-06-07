<script lang="ts">
    import type { Task } from '$lib/types';
    import Card from './Card.svelte';

    let { columnId, title, color, tasks, onDragStart, onDrop, onAddTask, projectId }: {
        columnId: string;
        title: string;
        color: string;
        tasks: Task[];
        onDragStart: (taskId: string) => void;
        onDrop: (taskId: string) => void;
        onAddTask?: () => void;
        projectId?: string;
    } = $props();

    let isOver = $state(false);

    function handleDragOver(e: DragEvent) {
        e.preventDefault();
        isOver = true;
    }

    function handleDragLeave() {
        isOver = false;
    }

    function handleDrop(e: DragEvent) {
        isOver = false;
        const taskId = e.dataTransfer?.getData('text/plain');
        if (taskId) {
            onDrop(taskId);
        }
    }
</script>

<div class={`column${isOver ? ' is-over' : ''}`} ondragover={handleDragOver} ondragleave={handleDragLeave} ondrop={handleDrop} role="list" aria-label={`${title} column`}>
    <div class="column-header" style="--col-color: {color}">
        <h3>{title}</h3>
        <span class="count">{tasks.length}</span>
    </div>
    <div class="card-list">
        {#if onAddTask}
            <button class="add-task-btn" onclick={onAddTask}>+ Add Task</button>
        {/if}
        {#each tasks as task (task.id)}
            <Card {task} {columnId} {projectId} />
        {/each}
    </div>
</div>

<style>
    .column {
        flex: 1;
        min-width: 260px;
        max-width: 340px;
        background: var(--color-surface-1);
        border-radius: var(--radius-lg);
        display: flex;
        flex-direction: column;
        overflow: hidden;
        transition: background 0.15s ease;
    }
    .column.is-over {
        background: var(--color-surface-2);
    }

    @media (max-width: 768px) {
        .column {
            flex: 0 0 auto !important;
            min-width: 280px;
            max-width: none;
            width: 92vw;
            scroll-snap-align: center;
        }
    }
    .column-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        border-bottom: 2px solid var(--col-color);
    }
    .column-header h3 {
        font-size: 0.875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-text-secondary);
    }
    .count {
        background: var(--color-surface-3);
        border-radius: 10px;
        padding: 2px 8px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .card-list {
        flex: 1;
        overflow-y: auto;
        padding: 8px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .add-task-btn {
        width: 100%;
        padding: 8px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        font-size: 0.875rem;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: background 0.15s ease;
    }
    .add-task-btn:hover {
        background: var(--color-accent);
        color: white;
    }
</style>