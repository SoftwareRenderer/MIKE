<script lang="ts">
    import type { Task } from '$lib/types';
    import Card from './Card.svelte';

    let { columnId, title, color, columnColor, tasks, childrenMap, expandedTasks, onToggleExpanded, onDragStart, onDrop, onAddTask, projectId, onClearAll, onCreateChildTask }: {
        columnId: string;
        title: string;
        color: string;
        columnColor: string;
        tasks: Task[];
        childrenMap: Map<string, Task[]>;
        expandedTasks: Set<string>;
        onToggleExpanded: (taskId: string) => void;
        onDragStart: (taskId: string) => void;
        onDrop: (taskId: string) => void;
        onAddTask?: () => void;
        projectId?: string;
        onClearAll?: () => void;
        onCreateChildTask?: (parentId: string) => void;
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
        <h3>
            {#if columnId === 'backlog'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
            {:else if columnId === 'in_progress'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
            {:else if columnId === 'review'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
            {:else if columnId === 'done'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
            {/if}
            {title}
        </h3>
        <div class="header-right">
            <span class="count">{tasks.length}</span>
            {#if onClearAll && tasks.length > 0}
                <button class="clear-btn" onclick={(e) => { e.stopPropagation(); onClearAll(); }} aria-label={`Clear all ${tasks.length} done tasks`} title={`Clear all ${tasks.length} done tasks`}>
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
            {/if}
        </div>
    </div>
    <div class="card-list">
        {#if onAddTask}
            <button class="add-task-btn" onclick={onAddTask} title="Add a new task">+ Add Task</button>
        {/if}
        {#each tasks as task (task.id)}
            <Card {task} {columnId} {projectId} {childrenMap} {expandedTasks} {onToggleExpanded} {onCreateChildTask} columnColor={color} />
        {/each}
    </div>
</div>

<style>
    .column {
        flex: 1;
        min-width: 260px;
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
        display: flex;
        align-items: center;
        gap: 5px;
        font-size: 0.875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-text-secondary);
    }
    .col-icon {
        flex-shrink: 0;
        color: var(--col-color);
    }
    .count {
        background: var(--color-surface-3);
        border-radius: 10px;
        padding: 2px 8px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .header-right {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .clear-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        border: none;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        transition: background 0.15s ease, color 0.15s ease;
        padding: 0;
        flex-shrink: 0;
    }
    .clear-btn:hover {
        background: var(--color-status-red)22;
        color: var(--color-status-red);
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