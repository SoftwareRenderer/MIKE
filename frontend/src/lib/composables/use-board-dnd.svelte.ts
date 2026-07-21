import type { Task } from '$lib/types';
import { appStore, dialogStore, updateTaskInBoard } from '$lib/stores';
import { moveTask } from '$lib/api';

/** Reactive state for board section drag-and-drop. */
export interface SectionDragState {
    isOver: boolean;
    isRejected: boolean;
}

/** Composable for board overview drag-and-drop.
 * Handles drag over/leave/drop on status sections with validation.
 * Accepts getter functions for reactive props so $derived/$effect see live values. */
export function useBoardDnd(getAllTasks: () => Task[], getProjectId: () => string) {
    let sectionDragState = $state<Map<string, SectionDragState>>(new Map());

    /** Walk up the ancestor chain to check if any ancestor is In Progress. */
    function hasAncestorInProgress(task: Task): boolean {
        const taskMap = new Map(getAllTasks().map(t => [t.id, t]));
        let current = task;
        while (current.parent_task_id) {
            const parent = taskMap.get(current.parent_task_id);
            if (!parent) break;
            if (parent.status === 'in_progress') return true;
            current = parent;
        }
        return false;
    }

    function canDrop(taskId: string, targetStatus: string): boolean {
        const task = getAllTasks().find(t => t.id === taskId);
        if (!task) return false;
        if (targetStatus === 'backlog' && task.parent_task_id && hasAncestorInProgress(task)) return false;
        return true;
    }

    function handleSectionDragOver(e: DragEvent, sectionId: string) {
        e.preventDefault();
        if (!e.dataTransfer) return;
        const taskIds = e.dataTransfer.getData('text/plain').split(',');
        // Reject if any task in the selection cannot be dropped
        let rejected = false;
        for (const taskId of taskIds) {
            if (taskId && !canDrop(taskId, sectionId)) {
                rejected = true;
                break;
            }
        }
        const state = sectionDragState.get(sectionId) || { isOver: false, isRejected: false };
        if (rejected) {
            e.dataTransfer.dropEffect = 'none';
            sectionDragState = new Map(sectionDragState).set(sectionId, { isRejected: true, isOver: false });
        } else {
            sectionDragState = new Map(sectionDragState).set(sectionId, { isRejected: false, isOver: true });
        }
    }

    function handleSectionDragLeave(sectionId: string) {
        sectionDragState = new Map(sectionDragState).set(sectionId, { isOver: false, isRejected: false });
    }

    async function handleSectionDrop(e: DragEvent, targetStatus: string) {
        const sectionId = targetStatus;
        sectionDragState = new Map(sectionDragState).set(sectionId, { isOver: false, isRejected: false });
        const taskIds = (e.dataTransfer?.getData('text/plain') || '').split(',');
        for (const taskId of taskIds) {
            if (!taskId) continue;
            const task = getAllTasks().find(t => t.id === taskId);
            if (!task || task.status === targetStatus) continue;
            if (!canDrop(taskId, targetStatus)) {
                dialogStore.showToast('Cannot move to Backlog: parent task is In Progress', 'error');
                continue;
            }
            try {
                const updated = await moveTask(getProjectId(), taskId, targetStatus);
                const willConsolidate = task.parent_task_id &&
                    getAllTasks().some(t => t.id === task.parent_task_id && t.status === targetStatus);
                if (!willConsolidate) {
                    updateTaskInBoard(appStore, taskId, updated);
                }
            } catch (err) {
                console.error('Failed to move task:', err);
                dialogStore.showToast('Failed to move task: ' + String(err), 'error');
            }
        }
    }

    return {
        get sectionDragState() { return sectionDragState; },
        handleSectionDragOver,
        handleSectionDragLeave,
        handleSectionDrop,
    };
}
