<script lang="ts">
    import type { Task } from '$lib/types';
    import Column from './Column.svelte';
    import { moveTask, reorderTasks, clearDoneTasks } from '$lib/api';
    import { board as boardStore, showConfirm, showToast } from '$lib/store';

    let { tasks, projectId, onAddTask, onCreateChildTask }: { tasks: Task[]; projectId: string; onAddTask?: () => void; onCreateChildTask?: (parentId: string) => void } = $props();

    // Build a lookup map for children of each task
    const childrenMap = $derived.by(() => {
        const map = new Map<string, Task[]>();
        for (const t of tasks) {
            if (t.parent_task_id) {
                if (!map.has(t.parent_task_id)) {
                    map.set(t.parent_task_id, []);
                }
                map.get(t.parent_task_id)!.push(t);
            }
        }
        // Sort children by column_order
        for (const [, children] of map) {
            children.sort((a, b) => a.column_order - b.column_order);
        }
        return map;
    });

    // All task IDs that are subtasks (have a parent)
    const subtaskIds = $derived(new Set(tasks.filter(t => t.parent_task_id).map(t => t.id)));

    let draggedTaskId: string | null = $state(null);

    // Track which task cards are expanded (stable across re-renders)
    let expandedTasks = $state<Set<string>>(new Set());
    function toggleTaskExpanded(taskId: string) {
        const next = new Set(expandedTasks);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        expandedTasks = next;
    }

    const columns = [
        { id: 'backlog', title: 'Backlog', color: 'var(--color-column-backlog)' },
        { id: 'in_progress', title: 'In Progress', color: 'var(--color-column-in-progress)' },
        { id: 'review', title: 'Review', color: 'var(--color-column-review)' },
        { id: 'done', title: 'Done', color: 'var(--color-column-done)' },
    ];

    /**
     * Get root tasks for a column (tasks without a parent, or whose parent is not in this column).
     * Subtasks are shown inline when the parent card is expanded.
     */
    function getTasksForColumn(status: string): Task[] {
        return tasks
            .filter(t => t.status === status && !subtaskIds.has(t.id))
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
            // Board stream WebSocket will push the update
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
            childrenMap={childrenMap}
            expandedTasks={expandedTasks}
            onToggleExpanded={toggleTaskExpanded}
            onDragStart={handleDragStart}
            onDrop={(taskId: string) => handleDrop(taskId, col.id)}
            onAddTask={col.id === 'backlog' ? onAddTask : undefined}
            onCreateChildTask={onCreateChildTask}
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