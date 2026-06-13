<script lang="ts">
    import type { Task } from '$lib/types';
    import Column from './Column.svelte';
    import { moveTask, reorderTasks, clearDoneTasks, getBoard } from '$lib/api';
    import { board as boardStore, showConfirm, showToast } from '$lib/store';

    let { tasks, projectId, onAddTask, onCreateChildTask }: { tasks: Task[]; projectId: string; onAddTask?: () => void; onCreateChildTask?: (parentId: string) => void } = $props();

    let draggedTaskId: string | null = $state(null);

    const columns = [
        { id: 'backlog', title: 'Backlog', color: 'var(--color-column-backlog)' },
        { id: 'in_progress', title: 'In Progress', color: 'var(--color-column-in-progress)' },
        { id: 'review', title: 'Review', color: 'var(--color-column-review)' },
        { id: 'done', title: 'Done', color: 'var(--color-column-done)' },
    ];

    /**
     * Get tasks for a column, ordered with parents first and children nested underneath.
     * Returns tasks with a `depth` property for indentation.
     */
    function getTasksForColumn(status: string): (Task & { depth: number })[] {
        const columnTasks = tasks.filter(t => t.status === status);
        const taskMap = new Map<string, Task>();
        for (const t of columnTasks) {
            taskMap.set(t.id, t);
        }

        // Build children map
        const childrenMap = new Map<string, Task[]>();
        const rootTasks: Task[] = [];
        for (const t of columnTasks) {
            if (t.parent_task_id && taskMap.has(t.parent_task_id)) {
                if (!childrenMap.has(t.parent_task_id)) {
                    childrenMap.set(t.parent_task_id, []);
                }
                childrenMap.get(t.parent_task_id)!.push(t);
            } else {
                rootTasks.push(t);
            }
        }

        // Sort root tasks by column_order
        rootTasks.sort((a, b) => a.column_order - b.column_order);
        // Sort children within each parent by column_order
        for (const [, children] of childrenMap) {
            children.sort((a, b) => a.column_order - b.column_order);
        }

        // Flatten: parent, then children (depth 1)
        const result: (Task & { depth: number })[] = [];
        for (const t of rootTasks) {
            result.push({ ...t, depth: 0 });
            const children = childrenMap.get(t.id);
            if (children) {
                for (const child of children) {
                    result.push({ ...child, depth: 1 });
                }
            }
        }
        return result;
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
            // Update boardStore with new task; WebSocket broadcast will refresh the full board
            boardStore.update(b => {
                if (!b) return b;
                return { ...b, tasks: b.tasks.map(t => t.id === taskId ? updated : t) };
            });
        } catch (e) {
            console.error('Failed to move task:', e);
        }
        draggedTaskId = null;
    }

    async function handleClearDone() {
        const doneTasks = getTasksForColumn('done');
        if (doneTasks.length === 0) return;
        const confirmed = await showConfirm({
            title: 'Clear Done Tasks',
            message: `Permanently delete ${doneTasks.length} done task${doneTasks.length > 1 ? 's' : ''}? This cannot be undone.`,
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        try {
            await clearDoneTasks(projectId);
            const board = await getBoard(projectId);
            boardStore.set(board);
        } catch (e) {
            showToast('Failed to clear done tasks: ' + String(e), 'error');
        }
    }
</script>

<div class="board" role="region" aria-label="Task board">
    {#each columns as col}
        <Column
            columnId={col.id}
            title={col.title}
            color={col.color}
            tasks={getTasksForColumn(col.id)}
            onDragStart={handleDragStart}
            onDrop={(taskId: string) => handleDrop(taskId, col.id)}
            onAddTask={col.id === 'backlog' ? onAddTask : undefined}
            onCreateChildTask={col.id === 'backlog' ? onCreateChildTask : undefined}
            onClearAll={col.id === 'done' ? handleClearDone : undefined}
            {projectId}
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