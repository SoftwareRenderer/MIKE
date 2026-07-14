<script lang="ts">
    import type { Task } from '$lib/types';
    import Column from './Column.svelte';
    import { tick } from 'svelte';
    import { moveTask, reorderTasks, clearDoneTasks } from '$lib/api';
import { sortByActivity } from '$lib/utils/sort-tasks';
    import { board as boardStore, selectedTask, persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks } from '$lib/stores/app';
import { showCardDetail } from '$lib/stores/ui';
import { showConfirm, showToast } from '$lib/stores/dialogs';
    import { getExpandedTasksContext } from '$lib/context';
    import { gc } from '$lib/storage';

    let { tasks, projectId, projectPath, onAddTask, onCreateChildTask, onRefreshBoard, scrollToColumn }: { tasks: Task[]; projectId: string; projectPath?: string; onAddTask?: () => void; onCreateChildTask?: (parentId: string) => void; onRefreshBoard?: () => void; scrollToColumn?: string | null } = $props();
    let expandedTasksContext = getExpandedTasksContext();

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
        // Sort children chronologically by last activity
        for (const [, children] of map) {
            children.sort((a, b) => {
                if (a.updated_at < b.updated_at) return -1;
                if (a.updated_at > b.updated_at) return 1;
                return a.column_order - b.column_order;
            });
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
            let parentId: string | undefined = t.parent_task_id;
            while (parentId) {
                const ancestor = taskMap.get(parentId);
                if (!ancestor) break;
                if (ancestor.status === t.status) {
                    consolidated.add(t.id);
                    break;
                }
                parentId = ancestor.parent_task_id ?? undefined;
            }
        }
        return consolidated;
    });

    let draggedTaskId: string | null = $state(null);

    // IDs of tasks that have children (potential parents)
    const parentTaskIds = $derived(new Set(tasks.filter(t => childrenMap.has(t.id)).map(t => t.id)));

    // Initialize expanded tasks from IndexedDB (default: all expanded) — run once
    let expandedInitialized = $state(false);
    $effect(() => {
        if (expandedInitialized) return;
        if (parentTaskIds.size > 0) {
            expandedInitialized = true;
            (async () => {
                const allIds = new Set(tasks.map(t => t.id));
                await cleanupCollapsedTasks(allIds);
                expandedTasksContext.set(await initExpandedTasks(parentTaskIds));
                // Garbage-collect stale IndexedDB entries for deleted tasks
                const projectPaths = projectPath ? new Set([projectPath]) : undefined;
                await gc(allIds, projectPaths);
            })().catch(() => {});
        }
    });

    // Auto-expand parent cards when new subtasks appear
    let lastExpandedParents = $state<Set<string>>(new Set());
    let expandedTasks = $state<Set<string>>(new Set());
    $effect(() => {
        expandedTasksContext.subscribe(v => expandedTasks = v);
    });
    $effect(() => {
        const currentParents = new Set(childrenMap.keys());
        for (const parentId of currentParents) {
            if (!lastExpandedParents.has(parentId)) {
                const current = expandedTasks;
                if (!current.has(parentId)) {
                    const next = new Set(current);
                    next.add(parentId);
                    expandedTasksContext.set(next);
                }
                lastExpandedParents.add(parentId);
            }
        }
    });

    // Track which task cards are expanded (shared store for Board + CardDetail sync)
    function toggleTaskExpanded(taskId: string) {
        const next = new Set(expandedTasks);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        expandedTasksContext.set(next);
        // Persist collapsed state and clean stale entries
        const allIds = new Set(tasks.map(t => t.id));
        persistCollapsedTasks(next, parentTaskIds).catch(() => {});
        cleanupCollapsedTasks(allIds).catch(() => {});
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

    /** Pre-built task lookup map (shared by hasAncestorInProgress and other functions). */
    const taskMap = $derived(new Map(tasks.map(t => [t.id, t])));

    /** Pre-computed column tasks (memoized per column status). */
    const columnTasks = $derived.by(() => {
        const cols: Record<string, Task[]> = { backlog: [], in_progress: [], review: [], done: [] };
        for (const t of tasks) {
            if (consolidatedSubtaskIds.has(t.id)) continue;
            if (!cols[t.status]) cols[t.status] = [];
            cols[t.status].push(t);
        }
        // Sort each column chronologically by last activity (oldest first)
        for (const status of Object.keys(cols)) {
            cols[status] = sortByActivity(cols[status]);
        }
        return cols;
    });

    /** Get tasks for a column (memoized via columnTasks). */
    function getTasksForColumn(status: string): Task[] {
        return columnTasks[status] || [];
    }

    function handleDragStart(taskId: string) {
        draggedTaskId = taskId;
    }

    /** Walk up the ancestor chain to check if any ancestor is In Progress. Uses shared taskMap. */
    function hasAncestorInProgress(task: Task): boolean {
        let current = task;
        while (current.parent_task_id) {
            const parent = taskMap.get(current.parent_task_id);
            if (!parent) break;
            if (parent.status === 'in_progress') return true;
            current = parent;
        }
        return false;
    }

    async function handleDrop(taskId: string, targetStatus: string) {
        if (!taskId || !targetStatus) return;
        const task = tasks.find(t => t.id === taskId);
        if (!task || task.status === targetStatus) return;
        // Block subtasks from moving to Backlog when an ancestor is In Progress.
        if (targetStatus === 'backlog' && task.parent_task_id && hasAncestorInProgress(task)) {
            showToast('Cannot move to Backlog: parent task is In Progress', 'error');
            draggedTaskId = null;
            return;
        }
        try {
            const updated = await moveTask(projectId, taskId, targetStatus);
            const statusLabel = targetStatus === 'backlog' ? 'Backlog' : targetStatus === 'in_progress' ? 'In Progress' : targetStatus === 'review' ? 'Review' : 'Done';
            announce(`Task moved to ${statusLabel}`);
            // Skip optimistic update if this subtask will be consolidated into its parent.
            // The immediate status change triggers consolidatedSubtaskIds to recalculate,
            // which hides the card mid-drop and makes the drag appear to fail.
            // Let the WebSocket broadcast handle the full board refresh instead.
            const willConsolidate = task.parent_task_id &&
                tasks.some(t => t.id === task.parent_task_id && t.status === targetStatus);
            if (!willConsolidate) {
                // When offline, updated is a partial optimistic result — merge with existing task
                const merged = (updated as any).queued
                    ? { ...task, ...updated }
                    : updated;
                // Update tasks array directly (Board renders from this prop, not boardStore)
                const idx = tasks.findIndex(t => t.id === taskId);
                if (idx !== -1) {
                    tasks[idx] = merged;
                }
                // Also sync boardStore
                boardStore.update(b => b ? { ...b, tasks: [...tasks] } : b);
            }
        } catch (e) {
            console.error('Failed to move task:', e);
            showToast('Failed to move task: ' + String(e), 'error');
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
            liveAnnouncement = `${doneTasks.length} done task${doneTasks.length > 1 ? 's' : ''} deleted`;
        } catch (e) {
            showToast('Failed to clear done tasks: ' + String(e), 'error');
        }
    }

    // Live region for screen reader announcements
    let liveAnnouncement = $state('');
    function announce(message: string) {
        // Reset then set to ensure announcement is re-triggered
        liveAnnouncement = '';
        queueMicrotask(() => { liveAnnouncement = message; });
    }

    // Scroll to a specific column on mobile (e.g. after task delete)
    let boardEl: HTMLDivElement | null = null;
    let hasScrolled = $state(false);
    $effect(() => {
        const target = scrollToColumn;
        if (!target || !boardEl || hasScrolled) return;
        hasScrolled = true;
        (async () => {
            await tick();
            const col = boardEl.querySelector(`.column[aria-label="${target === 'in_progress' ? 'In Progress' : target === 'backlog' ? 'Backlog' : target === 'review' ? 'Review' : 'Done'} column"]`);
            if (col instanceof HTMLElement) {
                col.scrollIntoView({ behavior: 'smooth', inline: 'center', block: 'nearest' });
            }
        })();
    });
</script>

<div class="board" bind:this={boardEl} role="region" aria-label="Task board">
    <div class="sr-only" aria-live="polite" aria-atomic="true">{liveAnnouncement}</div>
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
            canDrop={(taskId: string, colId: string) => {
                const task = tasks.find(t => t.id === taskId);
                if (!task || !task.parent_task_id || colId !== 'backlog') return true;
                return !hasAncestorInProgress(task);
            }}
            onAddTask={col.id === 'backlog' ? onAddTask : undefined}
            onCreateChildTask={onCreateChildTask}
            onClearAll={col.id === 'done' ? async () => { await handleClearDone(); onRefreshBoard?.(); } : undefined}
            onSelect={handleSelectTask}
            {projectId}
            onRefreshBoard={onRefreshBoard}
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