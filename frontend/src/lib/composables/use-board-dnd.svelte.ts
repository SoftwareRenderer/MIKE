import type { Task } from '$lib/types';
import { appStore, dialogStore, updateTaskInBoard } from '$lib/stores';
import { moveTask } from '$lib/api';
import { ancestorInProgress, willConsolidateIntoParent } from '$lib/utils/task-utils';
import { t } from '$lib/i18n';

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

    function canDrop(taskId: string, targetStatus: string): boolean {
        const task = getAllTasks().find(t => t.id === taskId);
        if (!task) return false;
        if (targetStatus === 'backlog' && task.parent_task_id && ancestorInProgress(getAllTasks(), task.id)) return false;
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
                dialogStore.showToast(t('boardDnd.cannotMoveToBacklog'), 'error');
                continue;
            }
            try {
                const updated = await moveTask(getProjectId(), taskId, targetStatus);
                if (!willConsolidateIntoParent(task, getAllTasks(), targetStatus)) {
                    updateTaskInBoard(appStore, taskId, updated);
                }
            } catch (err) {
                console.error('Failed to move task:', err);
                dialogStore.showToast(t('boardDnd.moveFailed', { error: String(err) }), 'error');
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
