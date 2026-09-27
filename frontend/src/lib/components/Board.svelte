<script lang="ts">
    import type { Task } from '$lib/types';
    import Column from './Column.svelte';
    import MultiSelectBar from './MultiSelectBar.svelte';
    import { tick, onMount } from 'svelte';
    import { moveTask, clearDoneTasks, deleteTask, startTask } from '$lib/api';
    import { sortByLastMoved } from '$lib/utils/sort-tasks';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard } from '$lib/stores';
    import { gc } from '$lib/storage';
    import { mergeQueuedUpdate, filterTopLevelTaskIds, ancestorInProgress, willConsolidateIntoParent } from '$lib/utils/task-utils';
    import { createStatusLabel } from '$lib/utils/subtask-status';
    import { columnInViewport, type Span } from '$lib/utils/review-notify';
    import { useMultiSelectActions } from '$lib/composables/use-multi-select-actions.svelte';
    import { useSubtaskHierarchy } from '$lib/composables/use-subtask-hierarchy.svelte';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    import { t, type MessageKey } from '$lib/i18n';
    // UI extension injection point — re-render when extensions change
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.PROJECT_BOARD);
        uiInjector.refreshSlot(INJECTION_POINTS.PROJECT_BOARD, { component: 'Board', projectId });
    });

    let { tasks, projectId, projectPath, onAddTask, onCreateChildTask, onRefreshBoard, onSelectTask, scrollToColumn, boardFocus }: { tasks: Task[]; projectId: string; projectPath?: string; onAddTask?: () => void; onCreateChildTask?: (parentId: string) => void; onRefreshBoard?: () => void; onSelectTask?: (task: Task) => void; scrollToColumn?: string | null; boardFocus?: { projectId: string; column: string } | null } = $props();

    // Multi-select actions (wires up floating bar events)
    let multiSelectActions = useMultiSelectActions({
        async onDelete(taskIds, titles) {
            const confirmed = await dialogStore.showConfirm({
                title: t('board.deleteTasks.title'),
                message: t('board.deleteTasks.message', { count: taskIds.length }),
                items: titles,
                confirmText: t('board.confirm.delete'),
                cancelText: t('board.confirm.cancel'),
            });
            if (!confirmed) return;
            // Only send DELETE for top-level tasks, server recursively deletes descendants
            const topLevelIds = filterTopLevelTaskIds(taskIds, tasks);
            for (const id of topLevelIds) {
                try {
                    await deleteTask(projectId, id);
                    removeTaskFromBoard(appStore, id);
                } catch (err) {
                    dialogStore.showToast(t('board.toast.deleteFailed', { error: String(err) }), 'error');
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
                    dialogStore.showToast(t('board.toast.startFailed', { error: String(err) }), 'error');
                }
            }
            if (started > 0) {
                dialogStore.showToast(t('board.toast.started', { count: started }), 'success');
            }
        },
        async onMove(taskIds, status) {
            let moved = 0;
            for (const id of taskIds) {
                const task = tasks.find(t => t.id === id);
                if (!task) continue;
                if (await applyMove(task, status)) moved++;
            }
            if (moved > 0) {
                dialogStore.showToast(
                    t('board.toast.movedCount', { count: moved, status: statusLabel(status) }),
                    'success',
                );
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
                    dialogStore.showToast(t('board.toast.modelUpdateFailed', { error: String(err) }), 'error');
                }
            }
            if (updated > 0) {
                dialogStore.showToast(t('board.toast.modelUpdated', { count: updated }), 'success');
            }
        },
    });
    let multiSelect = multiSelectActions.multiSelect;

    onMount(() => {
        multiSelectActions.setup();
    });

    // Subtask hierarchy management (expanded state, consolidation, auto-expand)
    let hierarchy = useSubtaskHierarchy(() => tasks, () => undefined);

    // Garbage-collect stale IndexedDB entries for deleted tasks (Board-specific)
    let gcInitialized = $state(false);
    $effect(() => {
        if (gcInitialized) return;
        if (hierarchy.parentTaskIds.size > 0) {
            gcInitialized = true;
            (async () => {
                const allIds = new Set(tasks.map(t => t.id));
                const projectPaths = projectPath ? new Set([projectPath]) : undefined;
                await gc(allIds, projectPaths, undefined, projectId);
            })().catch(() => {});
        }
    });

    function handleSelectTask(t: Task) {
        onSelectTask?.(t);
        // Push state first so the URL is updated before the subscription fires
        const url = new URL(location.href);
        url.searchParams.set('task', t.id);
        const newUrl = location.pathname + (url.search || '');
        history.pushState(null, '', newUrl);
        // Then set stores (subscription will see updated URL and skip updateUrl)
        appStore.selectedTask = t;
        uiStore.showCardDetail = true;
    }

    /**
     * A column is two things at once: `id` is **data** (compared, persisted, sent to the
     * API — never translated, and what {@link getColumnEl} finds a column by), and
     * `labelKey` is both its visible label and the word in its accessible name.
     */
    const columns: { id: string; labelKey: MessageKey; color: string }[] = [
        { id: 'backlog', labelKey: 'board.column.backlog', color: 'var(--color-column-backlog)' },
        { id: 'in_progress', labelKey: 'board.column.inProgress', color: 'var(--color-column-in-progress)' },
        { id: 'review', labelKey: 'board.column.review', color: 'var(--color-column-review)' },
        { id: 'done', labelKey: 'board.column.done', color: 'var(--color-column-done)' },
    ];

    /** Visible label for a status id (ids themselves are data — see {@link columns}). */
    const statusLabel = createStatusLabel(columns);

    /** Card direction from Settings → Preferences: true = least recently moved on top. */
    const sortOldestFirst = $derived(appStore.config?.sort_oldest_first === true);

    /** Pre-computed column tasks (memoized per column status). */
    const columnTasks = $derived.by(() => {
        const cols: Record<string, Task[]> = { backlog: [], in_progress: [], review: [], done: [] };
        for (const t of tasks) {
            if (hierarchy.consolidatedSubtaskIds.has(t.id)) continue;
            if (!cols[t.status]) cols[t.status] = [];
            cols[t.status].push(t);
        }
        // Newest move first (top) unless the preference asks for oldest first; stable,
        // so unrelated edits never re-rank a card
        for (const status of Object.keys(cols)) {
            cols[status] = sortByLastMoved(cols[status], sortOldestFirst);
        }
        return cols;
    });

    /** Get tasks for a column (memoized via columnTasks). */
    function getTasksForColumn(status: string): Task[] {
        return columnTasks[status] || [];
    }

    /** Send one move and land the result on the board. False when the server refused it. */
    async function applyMove(task: Task, targetStatus: string): Promise<boolean> {
        try {
            const updated = await moveTask(projectId, task.id, targetStatus);
            // The WebSocket broadcast is what lands a consolidating subtask.
            const willConsolidate = willConsolidateIntoParent(task, tasks, targetStatus);
            if (!willConsolidate) {
                // When offline, updated is a partial optimistic result — merge with existing task
                updateTaskInBoard(appStore, task.id, mergeQueuedUpdate(task, updated));
            }
            return true;
        } catch (e) {
            console.error('Failed to move task:', e);
            dialogStore.showToast(t('board.toast.moveFailed', { error: String(e) }), 'error');
            return false;
        }
    }

    async function handleDrop(taskId: string, targetStatus: string) {
        if (!taskId || !targetStatus) return;
        const task = tasks.find(t => t.id === taskId);
        if (!task || task.status === targetStatus) return;
        // Block subtasks from moving to Backlog when an ancestor is In Progress.
        if (targetStatus === 'backlog' && task.parent_task_id && ancestorInProgress(tasks, task.id)) {
            dialogStore.showToast(
                t('board.toast.cannotMoveToStatus', {
                    targetStatus: statusLabel(targetStatus),
                    parentStatus: statusLabel('in_progress'),
                }),
                'error',
            );
            return;
        }
        if (await applyMove(task, targetStatus)) {
            announce(t('board.toast.moved', { status: statusLabel(targetStatus) }));
        }
    }

    async function handleClearDone() {
        const doneTasks = getTasksForColumn('done');
        if (doneTasks.length === 0) return;
        const confirmed = await dialogStore.showConfirm({
            title: t('board.clearDone.title'),
            message: t('board.clearDone.message', { count: doneTasks.length }),
            confirmText: t('board.confirm.delete'),
            cancelText: t('board.confirm.cancel'),
        });
        if (!confirmed) return;
        try {
            await clearDoneTasks(projectId);
            // Board stream WebSocket will push the update
            liveAnnouncement = t('board.clearDone.deleted', { count: doneTasks.length });
        } catch (e) {
            dialogStore.showToast(t('board.clearDone.failed', { error: String(e) }), 'error');
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

    /** Column element for a status id, e.g. 'review' -> the Review column. */
    function getColumnEl(status: string): HTMLElement | null {
        if (!boardEl || !columns.some(c => c.id === status)) return null;
        const el = boardEl.querySelector(`.column[data-column-id="${status}"]`);
        return el instanceof HTMLElement ? el : null;
    }

    /** Whether the Review column of this board is the part of it the user is looking at. */
    function measureReviewColumn() {
        const review = getColumnEl('review');
        if (!boardEl || !review) {
            clearReviewColumnInView();
            return;
        }
        // Both rects are client coordinates, so scrolling moves the column out of the board's box.
        const span = (el: HTMLElement): Span => {
            const box = el.getBoundingClientRect();
            return { start: box.left, end: box.right };
        };
        uiStore.reviewColumnInViewOf = columnInViewport(span(boardEl), span(review)) ? projectId : null;
    }

    function clearReviewColumnInView() {
        if (uiStore.reviewColumnInViewOf === projectId) uiStore.reviewColumnInViewOf = null;
    }

    $effect(() => {
        const board = boardEl;
        if (!board) return;
        measureReviewColumn();
        board.addEventListener('scroll', measureReviewColumn, { passive: true });
        window.addEventListener('resize', measureReviewColumn);
        return () => {
            board.removeEventListener('scroll', measureReviewColumn);
            window.removeEventListener('resize', measureReviewColumn);
            // An unmounted board is nobody's line of sight.
            clearReviewColumnInView();
        };
    });

    $effect(() => {
        // A card landing or leaving moves the Review column.
        const _reviewColumn = columnTasks.review;
        measureReviewColumn();
    });

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
            const col = getColumnEl(target);
            if (col) {
                col.scrollIntoView({ behavior: 'smooth', inline: 'center', block: 'nearest' });
            }
        })();
    });

    // Jump to the landing column after switching project (mobile). Used once per project,
    // and skipped when the board is not on screen (a task detail is open).
    let lastFocusedProject: string | null = null;
    $effect(() => {
        const req = boardFocus;
        if (!req || req.projectId !== projectId || req.projectId === lastFocusedProject) return;
        lastFocusedProject = req.projectId;
        (async () => {
            await tick();
            getColumnEl(req.column)?.scrollIntoView({ behavior: 'auto', inline: 'center', block: 'nearest' });
        })();
    });
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="board" bind:this={boardEl} role="region" aria-label={t('board.regionAria')} onclick={(e) => { if (e.target === e.currentTarget) multiSelect.clear(); }} onkeydown={(e) => { if (e.key === 'Escape' && multiSelect.hasSelection) multiSelect.clear(); }}>
    <div class="sr-only" aria-live="polite" aria-atomic="true">{liveAnnouncement}</div>
    {#each columns as col}
        <Column
            columnId={col.id}
            label={t(col.labelKey)}
            color={col.color}
            tasks={getTasksForColumn(col.id)}
            childrenMap={hierarchy.childrenMap}
            expandedTasks={hierarchy.expandedTasks}
            onToggleExpanded={hierarchy.toggleTaskExpanded}
            onDrop={(taskId: string) => handleDrop(taskId, col.id)}
            canDrop={(taskId: string, colId: string) => {
                const task = tasks.find(t => t.id === taskId);
                if (!task || !task.parent_task_id || colId !== 'backlog') return true;
                return !ancestorInProgress(tasks, task.id);
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