<script lang="ts">
    import type { Task } from '$lib/types';
    import Column from './Column.svelte';
    import MultiSelectBar from './MultiSelectBar.svelte';
    import { tick, onMount } from 'svelte';
    import { moveTask, reorderTasks, clearDoneTasks, deleteTask, startTask } from '$lib/api';
import { sortByActivity } from '$lib/utils/sort-tasks';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard } from '$lib/stores';
    import { getExpandedTasksContext } from '$lib/context';
    import { gc } from '$lib/storage';
    import { mergeQueuedUpdate } from '$lib/utils/task-utils';
    import { useMultiSelectActions } from '$lib/composables/use-multi-select-actions.svelte';
    import { useSubtaskHierarchy } from '$lib/composables/use-subtask-hierarchy.svelte';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    // UI extension injection point — re-render when extensions change
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.PROJECT_BOARD);
        uiInjector.refreshSlot(INJECTION_POINTS.PROJECT_BOARD, { component: 'Board', projectId });
    });

    let { tasks, projectId, projectPath, onAddTask, onCreateChildTask, onRefreshBoard, scrollToColumn }: { tasks: Task[]; projectId: string; projectPath?: string; onAddTask?: () => void; onCreateChildTask?: (parentId: string) => void; onRefreshBoard?: () => void; scrollToColumn?: string | null } = $props();
    let expandedTasksContext = getExpandedTasksContext();

    // Multi-select actions (wires up floating bar events)
    let multiSelectActions = useMultiSelectActions({
        async onDelete(taskIds, titles) {
            const confirmed = await dialogStore.showConfirm({
                title: 'Delete Tasks',
                message: `Permanently delete ${taskIds.length} task${taskIds.length > 1 ? 's' : ''}? This cannot be undone.`,
                items: titles,
                confirmText: 'Delete',
                cancelText: 'Cancel',
            });
            if (!confirmed) return;
            for (const id of taskIds) {
                try {
                    await deleteTask(projectId, id);
                    removeTaskFromBoard(appStore, id);
                } catch (err) {
                    dialogStore.showToast('Failed to delete task: ' + String(err), 'error');
                }
            }
            onRefreshBoard?.();
        },
        async onStart(taskIds) {
            let started = 0;
            for (const id of taskIds) {
                try {
                    const task = tasks.find(t => t.id === id);
                    if (!task) continue;
                    if (task.parent_task_id) {
                        const { startSubtask } = await import('$lib/api');
                        const updated = await startSubtask(projectId, id);
                        updateTaskInBoard(appStore, updated.id, updated);
                    } else {
                        const updated = await startTask(projectId, id);
                        updateTaskInBoard(appStore, updated.id, updated);
                    }
                    started++;
                } catch (err) {
                    dialogStore.showToast(`Failed to start task: ${String(err)}`, 'error');
                }
            }
            if (started > 0) {
                dialogStore.showToast(`${started} task${started > 1 ? 's' : ''} started`, 'success');
            }
        },
        async onModelChange(taskIds, providerUrl, modelName, reasoningEffort) {
            let updated = 0;
            for (const id of taskIds) {
                try {
                    const { updateTask } = await import('$lib/api');
                    const taskUpdate: Record<string, unknown> = { llama_server_url: providerUrl };
                    if (modelName) taskUpdate.model_name = modelName;
                    else taskUpdate.model_name = undefined;
                    if (reasoningEffort) taskUpdate.reasoning_effort = reasoningEffort;
                    else taskUpdate.reasoning_effort = undefined;
                    const result = await updateTask(projectId, id, taskUpdate);
                    updateTaskInBoard(appStore, result.id, result);
                    updated++;
                } catch (err) {
                    dialogStore.showToast(`Failed to update model: ${String(err)}`, 'error');
                }
            }
            if (updated > 0) {
                dialogStore.showToast(`Model updated for ${updated} task${updated > 1 ? 's' : ''}`, 'success');
            }
        },
    });
    let multiSelect = multiSelectActions.multiSelect;

    onMount(() => {
        multiSelectActions.setup();
    });

    // Subtask hierarchy management (expanded state, consolidation, auto-expand)
    let hierarchy = useSubtaskHierarchy(() => tasks, expandedTasksContext, () => undefined);

    let draggedTaskId: string | null = $state(null);

    // Garbage-collect stale IndexedDB entries for deleted tasks (Board-specific)
    let gcInitialized = $state(false);
    $effect(() => {
        if (gcInitialized) return;
        if (hierarchy.parentTaskIds.size > 0) {
            gcInitialized = true;
            (async () => {
                const allIds = new Set(tasks.map(t => t.id));
                const projectPaths = projectPath ? new Set([projectPath]) : undefined;
                await gc(allIds, projectPaths);
            })().catch(() => {});
        }
    });

    function handleSelectTask(t: Task) {
        // Push state first so the URL is updated before the subscription fires
        const url = new URL(location.href);
        url.searchParams.set('task', t.id);
        const newUrl = location.pathname + (url.search || '');
        history.pushState(null, '', newUrl);
        // Then set stores (subscription will see updated URL and skip updateUrl)
        appStore.selectedTask = t;
        uiStore.showCardDetail = true;
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
            if (hierarchy.consolidatedSubtaskIds.has(t.id)) continue;
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
            dialogStore.showToast('Cannot move to Backlog: parent task is In Progress', 'error');
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
                const merged = mergeQueuedUpdate(task, updated);
                // Update tasks array directly (Board renders from this prop, not boardStore)
                const idx = tasks.findIndex(t => t.id === taskId);
                if (idx !== -1) {
                    tasks[idx] = merged;
                }
                // Also sync boardStore
                appStore.board = { ...appStore.board!, tasks: [...tasks] };
            }
        } catch (e) {
            console.error('Failed to move task:', e);
            dialogStore.showToast('Failed to move task: ' + String(e), 'error');
        }
        draggedTaskId = null;
    }

    async function handleClearDone() {
        const doneTasks = getTasksForColumn('done');
        if (doneTasks.length === 0) return;
        const confirmed = await dialogStore.showConfirm({
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
            dialogStore.showToast('Failed to clear done tasks: ' + String(e), 'error');
        }
    }

    // Live region for screen reader announcements
    let liveAnnouncement = $state('');
    function announce(message: string) {
        // Reset then set to ensure announcement is re-triggered
        liveAnnouncement = '';
        queueMicrotask(() => { liveAnnouncement = message; });
    }

    // Scroll to a specific column on mobile (e.g. after task delete or close)
    let boardEl: HTMLDivElement | null = null;
    let hasScrolled = $state(false);
    let lastScrolledTarget = $state<string | null>(null);
    $effect(() => {
        const target = scrollToColumn;
        // Reset hasScrolled when target changes (new close event)
        if (target !== lastScrolledTarget) {
            hasScrolled = false;
            lastScrolledTarget = target ?? null;
        }
        if (!target || !boardEl || hasScrolled) return;
        // Skip scroll if target column is empty (e.g. last task in column was deleted)
        if (getTasksForColumn(target).length === 0) return;
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

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="board" bind:this={boardEl} role="region" aria-label="Task board" onclick={(e) => { if (e.target === e.currentTarget) multiSelect.clear(); }} onkeydown={(e) => { if (e.key === 'Escape' && multiSelect.hasSelection) multiSelect.clear(); }}>
    <div class="sr-only" aria-live="polite" aria-atomic="true">{liveAnnouncement}</div>
    {#each columns as col}
        <Column
            columnId={col.id}
            title={col.title}
            color={col.color}
            columnColor={col.color}
            tasks={getTasksForColumn(col.id)}
            childrenMap={hierarchy.childrenMap}
            expandedTasks={hierarchy.expandedTasks}
            onToggleExpanded={hierarchy.toggleTaskExpanded}
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
            {multiSelect}
            onRefreshBoard={onRefreshBoard}
        />
    {/each}
    <MultiSelectBar
        {multiSelect}
        {tasks}
        {projectId}
        onClear={() => multiSelect.clear()}
    />
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