import type { Task } from '$lib/types';
import { get } from 'svelte/store';
import { moveTask, updateTask, stopTask, resumeTask, deleteTask, startTask, commitAndDone, getQueuedTaskIds } from '$lib/api';
import { selectedTask, board as boardStore, showEditTask, editTaskData as editTaskDataStore, selectedProject } from '$lib/stores/app';
import { showCardDetail } from '$lib/stores/ui';
import { showConfirm, showToast } from '$lib/stores/dialogs';
import { commitDialog as commitDialogStore } from '$lib/stores/dialogs';
import { getTaskDiffStatsContext } from '$lib/context';

/** Dependencies the component must provide. */
export interface CardActionsDeps {
    task: Task;
    columnId: string;
    projectId: string;
    hasChildren: boolean;
    hasChanges: boolean;
    hasRunningDescendant: boolean;
    liveExecutionState: string;
    childrenMap?: Map<string, Task[]>;
    expandedTasks?: Set<string>;
    /** Callback to refresh the board. */
    onRefreshBoard?: () => void;
    /** Callback to toggle expanded state. */
    onToggleExpanded?: (taskId: string) => void;
    /** Callback to create a child task. */
    onCreateChildTask?: (parentId: string) => void;
    /** Callback to select a task (for inline subtasks). */
    onSelect?: (task: Task) => void;
}

const statuses = ['backlog', 'in_progress', 'review', 'done'] as const;
const statusLabels: Record<string, string> = {
    backlog: 'Backlog',
    in_progress: 'In Progress',
    review: 'Review',
    done: 'Done',
};
const STOPPED_STATES = ['stopped', 'failed'];

/**
 * Manages all card action handlers.
 *
 * Handles: start, pause, resume, delete, move/cycle status, edit,
 * create child, keyboard navigation, click handling, and drag-and-drop.
 */
export function useCardActions(deps: CardActionsDeps) {
    let mouseDownPos = $state<{ x: number; y: number } | null>(null);

    function handleMouseDown(e: MouseEvent) {
        mouseDownPos = { x: e.clientX, y: e.clientY };
    }

    function handleDragStart(e: DragEvent) {
        e.stopPropagation();
        if (!e.dataTransfer) return;
        e.dataTransfer.setData('text/plain', deps.task.id);
        e.dataTransfer.effectAllowed = 'move';
    }

    function handleCardClick(e: MouseEvent) {
        // If the mouse moved significantly between mousedown and click,
        // it was a drag gesture — suppress the click to avoid opening CardDetail
        if (mouseDownPos) {
            const dx = e.clientX - mouseDownPos.x;
            const dy = e.clientY - mouseDownPos.y;
            if (Math.sqrt(dx * dx + dy * dy) > 5) {
                mouseDownPos = null;
                return;
            }
        }
        mouseDownPos = null;
        // Skip if the click originated from a child button
        const target = e.target as HTMLElement;
        if (target.closest('button, input, a, select, .card-edit-panel')) return;
        if (deps.onSelect) {
            deps.onSelect(deps.task);
        } else {
            selectedTask.set(deps.task);
            showCardDetail.set(true);
        }
    }

    function handleCardKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            selectedTask.set(deps.task);
            showCardDetail.set(true);
        }
        // Alt+ArrowLeft/ArrowRight: move task to previous/next column
        if (e.altKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
            e.preventDefault();
            e.stopPropagation();
            if (!deps.projectId) return;
            const currentIdx = statuses.indexOf(deps.task.status as typeof statuses[number]);
            const targetIdx = e.key === 'ArrowLeft'
                ? (currentIdx - 1 + statuses.length) % statuses.length
                : (currentIdx + 1) % statuses.length;
            const targetStatus = statuses[targetIdx];
            (async () => {
                try {
                    const updated = await moveTask(deps.projectId, deps.task.id, targetStatus);
                    Object.assign(deps.task, (updated as any).queued ? { ...deps.task, ...updated } : updated);
                    boardStore.update(b => b ? { ...b, tasks: b.tasks.map(t => t.id === deps.task.id ? deps.task : t) } : b);
                } catch (err) {
                    console.error('Failed to move task:', err);
                }
            })();
        }
    }

    async function handlePause(e: MouseEvent) {
        e.stopPropagation();
        if (!deps.projectId) return;
        try {
            const updated = await stopTask(deps.projectId, deps.task.id);
            Object.assign(deps.task, (updated as any).queued ? { ...deps.task, ...updated } : updated);
            boardStore.update(b => b ? { ...b, tasks: b.tasks.map(t => t.id === deps.task.id ? deps.task : t) } : b);
        } catch (err) {
            console.error('Failed to pause task:', err);
        }
    }

    async function handleResume(e: MouseEvent) {
        e.stopPropagation();
        if (!deps.projectId) return;
        try {
            const updated = await resumeTask(deps.projectId, deps.task.id);
            Object.assign(deps.task, (updated as any).queued ? { ...deps.task, ...updated } : updated);
            boardStore.update(b => b ? { ...b, tasks: b.tasks.map(t => t.id === deps.task.id ? deps.task : t) } : b);
        } catch (err) {
            console.error('Failed to resume task:', err);
        }
    }

    async function cycleStatus(e: MouseEvent) {
        e.stopPropagation();
        if (!deps.projectId) return;
        const currentIdx = statuses.indexOf(deps.task.status as typeof statuses[number]);
        const nextIdx = (currentIdx + 1) % statuses.length;
        const nextStatus = statuses[nextIdx];
        try {
            let updated: Task;
            if (nextStatus === 'done' && deps.hasChanges) {
                const confirmedMessage = await new Promise<string | null>((resolve) => {
                    (commitDialogStore as any).set({
                        streaming: true,
                        message: '',
                        confirmText: 'Commit and move to Done',
                        cancelText: 'Cancel',
                        projectId: deps.projectId!,
                        taskId: deps.task.id,
                        _resolve: resolve,
                    });
                });
                if (!confirmedMessage) return;
                updated = await commitAndDone(deps.projectId, deps.task.id, confirmedMessage);
                // Clear stale diff stats immediately
                const taskDiffStatsStore = getTaskDiffStatsContext();
                taskDiffStatsStore.update(map => { delete map[deps.task.id]; return { ...map }; });
            } else {
                updated = await moveTask(deps.projectId, deps.task.id, nextStatus);
            }
            Object.assign(deps.task, (updated as any).queued ? { ...deps.task, ...updated } : updated);
            boardStore.update(b => b ? { ...b, tasks: b.tasks.map(t => t.id === deps.task.id ? deps.task : t) } : b);
        } catch (err) {
            console.error('Failed to move task:', err);
        }
    }

    async function handleDelete(e: MouseEvent) {
        e.stopPropagation();
        if (!deps.projectId) return;
        const confirmed = await showConfirm({
            title: 'Delete Task',
            message: 'Delete this task? This cannot be undone.',
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        try {
            await deleteTask(deps.projectId, deps.task.id);
            deps.onRefreshBoard?.();
        } catch (err) {
            if (getQueuedTaskIds().has(deps.task.id)) {
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.filter(t => t.id !== deps.task.id) };
                });
                deps.onRefreshBoard?.();
            } else {
                showToast('Failed to delete task: ' + String(err), 'error');
            }
        }
    }

    function handleCreateChild(e: MouseEvent) {
        e.stopPropagation();
        deps.onCreateChildTask?.(deps.task.id);
    }

    async function handleStart(e: MouseEvent) {
        e.stopPropagation();
        if (!deps.projectId) return;
        try {
            if (deps.task.parent_task_id) {
                const { startSubtask } = await import('$lib/api');
                const updated = await startSubtask(deps.projectId, deps.task.id);
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.map(t => t.id === updated.id ? updated : t) };
                });
            } else {
                const updated = await startTask(deps.projectId, deps.task.id);
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.map(t => t.id === updated.id ? updated : t) };
                });
            }
        } catch (err) {
            console.error('Failed to start task:', err);
            showToast('Failed to start task: ' + String(err), 'error');
        }
    }

    function toggleExpanded(e: MouseEvent) {
        e.stopPropagation();
        deps.onToggleExpanded?.(deps.task.id);
    }

    function startEdit(e: MouseEvent) {
        e.stopPropagation();
        const cfg = (boardStore as any).get?.() ?? null;
        const providers = cfg?.providers || [];
        const selProject = get(selectedProject);
        editTaskDataStore.set({
            taskId: deps.task.id,
            projectId: deps.projectId!,
            taskTitle: deps.task.title,
            taskPrompt: deps.task.prompt,
            editProviders: providers,
            editModel: deps.task.model_name || '',
            editUrl: deps.task.llama_server_url || '',
            editReasoningEffort: deps.task.reasoning_effort || '',
            editOperatingMode: deps.task.operating_mode ?? 'act',
            editAutoMerge: deps.task.auto_merge ?? true,
            editSubtasksAutoMerge: deps.task.subtasks_auto_merge ?? true,
            taskHasParent: !!deps.task.parent_task_id,
            targetBranch: selProject?.origin_branch || 'main',
            isAgentSelected: () => {
                const url = deps.task.llama_server_url;
                if (!url) return false;
                if (url === 'pi://agent') return true;
                const p = providers.find((p: any) => p.url === url);
                return p ? (p as any).type === 'agent' : false;
            },
        });
        showEditTask.set(true);
    }

    function handleEditSaved(title: string, model: string, url: string, reasoningEffort: string, operatingMode: string, autoMerge: boolean, subtasksAutoMerge: boolean) {
        const trimmed = title.trim();
        if (!trimmed || !deps.projectId) {
            showEditTask.set(false);
            editTaskDataStore.set(null);
            return;
        }
        const updates: Record<string, unknown> = { title: trimmed };
        updates.operating_mode = operatingMode;
        updates.auto_commit = autoMerge;
        updates.auto_merge = autoMerge;
        updates.subtasks_auto_merge = subtasksAutoMerge;
        if (model) updates.model_name = model;
        if (url) updates.llama_server_url = url;
        if (reasoningEffort) updates.reasoning_effort = reasoningEffort;
        deps.task.title = trimmed;
        deps.task.operating_mode = operatingMode;
        deps.task.auto_commit = autoMerge;
        deps.task.auto_merge = autoMerge;
        deps.task.subtasks_auto_merge = subtasksAutoMerge;
        if (model) deps.task.model_name = model;
        if (url) deps.task.llama_server_url = url;
        if (reasoningEffort) deps.task.reasoning_effort = reasoningEffort;
        boardStore.update(b => {
            if (!b) return b;
            return { ...b, tasks: b.tasks.map(t => t.id === deps.task.id ? deps.task : t) };
        });
        updateTask(deps.projectId, deps.task.id, updates).catch(err => {
            console.error('Failed to update task:', err);
        });
        showEditTask.set(false);
        editTaskDataStore.set(null);
    }

    function handleEditCancelled() {
        showEditTask.set(false);
        editTaskDataStore.set(null);
    }

    function truncate(s: string, max: number): string {
        if (!s) return '';
        return s.length > max ? s.slice(0, max) + '...' : s;
    }

    function getNextStatus(): string {
        const currentIdx = statuses.indexOf(deps.task.status as typeof statuses[number]);
        const nextIdx = (currentIdx + 1) % statuses.length;
        return statuses[nextIdx];
    }

    return {
        mouseDownPos,
        handleMouseDown,
        handleDragStart,
        handleCardClick,
        handleCardKeydown,
        handlePause,
        handleResume,
        cycleStatus,
        handleDelete,
        handleCreateChild,
        handleStart,
        toggleExpanded,
        startEdit,
        handleEditSaved,
        handleEditCancelled,
        truncate,
        getNextStatus,
        statusLabels,
    };
}
