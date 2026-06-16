<script lang="ts">
    import type { Task } from '$lib/types';
    import Column from './Column.svelte';
    import { moveTask, reorderTasks, clearDoneTasks } from '$lib/api';
    import { board as boardStore, showConfirm, showToast, selectedTask, showCardDetail, expandedTasks as expandedTasksStore, persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks } from '$lib/store';

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

    // Subtask IDs that should be consolidated (hidden) because any ancestor is in the same column
    const consolidatedSubtaskIds = $derived.by(() => {
        const consolidated = new Set<string>();
        const taskMap = new Map<string, Task>();
        for (const t of tasks) taskMap.set(t.id, t);

        for (const t of tasks) {
            if (!t.parent_task_id) continue;
            // Walk up the ancestor chain
            let parentId = t.parent_task_id;
            while (parentId) {
                const ancestor = taskMap.get(parentId);
                if (!ancestor) break;
                if (ancestor.status === t.status) {
                    consolidated.add(t.id);
                    break;
                }
                parentId = ancestor.parent_task_id ?? null;
            }
        }
        return consolidated;
    });

    let draggedTaskId: string | null = $state(null);

    // IDs of tasks that have children (potential parents)
    const parentTaskIds = $derived(new Set(tasks.filter(t => childrenMap.has(t.id)).map(t => t.id)));

    // Initialize expanded tasks from localStorage (default: all expanded) — run once
    let expandedInitialized = $state(false);
    $effect(() => {
        if (expandedInitialized) return;
        if (parentTaskIds.size > 0) {
            expandedInitialized = true;
            const allIds = new Set(tasks.map(t => t.id));
            cleanupCollapsedTasks(allIds);
            expandedTasksStore.set(initExpandedTasks(parentTaskIds));
        }
    });

    // Track which task cards are expanded (shared store for Board + CardDetail sync)
    let expandedTasks = $derived($expandedTasksStore);
    function toggleTaskExpanded(taskId: string) {
        const next = new Set($expandedTasksStore);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        expandedTasksStore.set(next);
        // Persist collapsed state and clean stale entries
        const allIds = new Set(tasks.map(t => t.id));
        persistCollapsedTasks(next, parentTaskIds);
        cleanupCollapsedTasks(allIds);
    }

    function handleSelectTask(t: Task) {
        // Push state first so the URL is updated before the subscription fires
        const url = new URL(location.href);
        url.searchParams.set('task', t.id);
        const newUrl = location.pathname + (url.search || '');
        history.pushState(null, '', newUrl);
        // Then set stores (subscription will see updated URL and skip updateUrl)
        selectedTask.set(t);
        showCardDetail.set(true);
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
     * Consolidated subtasks (parent + all siblings in same column) are hidden.
     */
    function getTasksForColumn(status: string): Task[] {
        return tasks
            .filter(t => {
                if (t.status !== status) return false;
                // Hide consolidated subtasks (shown via parent's progress bar)
                if (consolidatedSubtaskIds.has(t.id)) return false;
                // Show root tasks always, subtasks only in review/done columns
                if (!subtaskIds.has(t.id)) return true;
                return status === 'review' || status === 'done';
            })
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
            columnColor={col.color}
            tasks={getTasksForColumn(col.id)}
            childrenMap={childrenMap}
            expandedTasks={expandedTasks}
            onToggleExpanded={toggleTaskExpanded}
            onDragStart={handleDragStart}
            onDrop={(taskId: string) => handleDrop(taskId, col.id)}
            onAddTask={col.id === 'backlog' ? onAddTask : undefined}
            onCreateChildTask={onCreateChildTask}
            onClearAll={col.id === 'done' ? handleClearDone : undefined}
            onSelect={handleSelectTask}
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