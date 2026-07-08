import type { Task } from '$lib/types';
import { board as boardStore } from '$lib/stores/app';
import { showToast } from '$lib/stores/dialogs';
import { moveTask } from '$lib/api';

/** Reactive state for board section drag-and-drop. */
export interface SectionDragState {
    isOver: boolean;
    isRejected: boolean;
}

/** Composable for board overview drag-and-drop.
 * Handles drag over/leave/drop on status sections with validation. */
export function useBoardDnd(allTasks: Task[], projectId: string, onRefreshBoard?: () => void) {
    let sectionDragState = $state<Map<string, SectionDragState>>(new Map());

    /** Walk up the ancestor chain to check if any ancestor is In Progress. */
    function hasAncestorInProgress(task: Task): boolean {
        const taskMap = new Map(allTasks.map(t => [t.id, t]));
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
        const task = allTasks.find(t => t.id === taskId);
        if (!task) return false;
        if (targetStatus === 'backlog' && task.parent_task_id && hasAncestorInProgress(task)) return false;
        return true;
    }

    function handleSectionDragOver(e: DragEvent, sectionId: string) {
        e.preventDefault();
        if (!e.dataTransfer) return;
        const taskId = e.dataTransfer.getData('text/plain');
        const state = sectionDragState.get(sectionId) || { isOver: false, isRejected: false };
        if (taskId && !canDrop(taskId, sectionId)) {
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
        const taskId = e.dataTransfer?.getData('text/plain');
        if (!taskId) return;
        const task = allTasks.find(t => t.id === taskId);
        if (!task || task.status === targetStatus) return;
        if (!canDrop(taskId, targetStatus)) {
            showToast('Cannot move to Backlog: parent task is In Progress', 'error');
            return;
        }
        try {
            const updated = await moveTask(projectId, taskId, targetStatus);
            const willConsolidate = task.parent_task_id &&
                allTasks.some(t => t.id === task.parent_task_id && t.status === targetStatus);
            if (!willConsolidate) {
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.map(t => t.id === taskId ? updated : t) };
                });
            }
        } catch (err) {
            console.error('Failed to move task:', err);
            showToast('Failed to move task: ' + String(err), 'error');
        }
    }

    return {
        sectionDragState,
        handleSectionDragOver,
        handleSectionDragLeave,
        handleSectionDrop,
    };
}
