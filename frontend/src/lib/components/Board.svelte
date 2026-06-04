<script lang="ts">
    import type { Task } from '$lib/types';
    import Column from './Column.svelte';
    import { moveTask, reorderTasks } from '$lib/api';
    import { board as boardStore } from '$lib/store';

    let { tasks, projectId, onAddTask }: { tasks: Task[]; projectId: string; onAddTask?: () => void } = $props();

    let draggedTaskId: string | null = $state(null);

    const columns = [
        { id: 'backlog', title: 'Backlog', color: 'var(--color-column-backlog)' },
        { id: 'in_progress', title: 'In Progress', color: 'var(--color-column-in-progress)' },
        { id: 'review', title: 'Review', color: 'var(--color-column-review)' },
        { id: 'done', title: 'Done', color: 'var(--color-column-done)' },
    ];

    function getTasksForColumn(status: string): Task[] {
        return tasks
            .filter(t => t.status === status)
            .sort((a, b) => a.column_order - b.column_order);
    }

    function handleDragStart(taskId: string) {
        draggedTaskId = taskId;
    }

    async function handleDrop(taskId: string, targetStatus: string) {
        if (!taskId || !targetStatus) return;
        const task = tasks.find(t => t.id === taskId);
        if (!task || task.status === targetStatus) return;
        try {
            const updated = await moveTask(projectId, taskId, targetStatus);
            const idx = tasks.findIndex(t => t.id === taskId);
            if (idx !== -1) tasks[idx] = updated;
            boardStore.update(b => b ? { ...b, tasks: [...tasks] } : b);
        } catch (e) {
            console.error('Failed to move task:', e);
        }
        draggedTaskId = null;
    }
</script>

<div class="board">
    {#each columns as col}
        <Column
            columnId={col.id}
            title={col.title}
            color={col.color}
            tasks={getTasksForColumn(col.id)}
            onDragStart={handleDragStart}
            onDrop={(taskId: string) => handleDrop(taskId, col.id)}
            onAddTask={col.id === 'backlog' ? onAddTask : undefined}
        />
    {/each}
</div>

<style>
    .board {
        display: flex;
        gap: 12px;
        padding: 16px;
        height: 100%;
        overflow-x: auto;
    }

    @media (max-width: 768px) {
        .board {
            overflow-x: auto;
            overflow-y: hidden;
            scroll-snap-type: x mandatory;
            -webkit-overflow-scrolling: touch;
            padding-left: 4vw;
            padding-right: 4vw;
            gap: 8px;
        }
    }
</style>