/**
 * The four bulk task actions the multi-select bar offers, written once and driven by injected
 * dependencies. Each screen hands over what only it knows — the task list it can see, the project id,
 * the column table behind its labels, how it reaches the api, how a moved task lands — so the board and
 * the task view share the counting, the confirmations and the toasts without sharing a chunk.
 */
import { t } from '$lib/i18n';
import { filterTopLevelTaskIds } from './task-utils';
import type { Task } from '$lib/types';

/**
 * The writes a bulk action performs, one function per endpoint. A lazily-loaded screen keeps its
 * `await import('$lib/api')` inside the wrapper it passes here, which is what keeps chunk membership
 * a decision each screen makes rather than one this module makes for it.
 */
interface BulkTaskApis {
    deleteTask: (projectId: string, taskId: string) => Promise<unknown>;
    startTask: (projectId: string, taskId: string) => Promise<Task>;
    startSubtask: (projectId: string, taskId: string) => Promise<Task>;
    updateTask: (projectId: string, taskId: string, update: Partial<Task>) => Promise<Task>;
}

/** The board writes a bulk action makes: land one task row as answered, drop one that is gone. */
interface BulkTaskBoard {
    update: (task: Task) => void;
    remove: (taskId: string) => void;
}

/** The confirmation the bar's destructive action waits on, and the toast each batch reports through. */
interface BulkTaskDialog {
    showConfirm: (options: {
        title: string;
        message: string;
        items?: string[];
        confirmText?: string;
        cancelText?: string;
    }) => Promise<boolean>;
    showToast: (message: string, type?: 'error' | 'success') => void;
}

export interface BulkTaskActionDeps {
    /** Every task this screen can see — the board's list, or the task view's board overview. */
    tasks: () => Task[];
    projectId: () => string;
    /** Visible label for a status id, from the column table this screen shows. */
    statusLabel: (status: string) => string;
    api: BulkTaskApis;
    board: BulkTaskBoard;
    dialog: BulkTaskDialog;
    /**
     * Send one move and land its result; false when the move did not happen. The move stays the
     * caller's: the two screens land the reply differently, and which of the two is right is undecided.
     */
    moveTask: (task: Task, targetStatus: string) => Promise<boolean>;
    onRefreshBoard?: () => void;
}

/** The callbacks `useMultiSelectActions` forwards the floating bar's events to. */
export interface BulkTaskActions {
    onDelete: (taskIds: string[], titles: string[]) => Promise<void>;
    onStart: (taskIds: string[]) => Promise<void>;
    onMove: (taskIds: string[], status: string) => Promise<void>;
    onModelChange: (
        taskIds: string[],
        providerUrl: string,
        modelName: string | undefined,
        reasoningEffort: string | undefined,
    ) => Promise<void>;
}

/**
 * Build the four bulk actions. One failure reports itself and the batch continues; the success toast
 * counts what landed and stays silent when nothing did.
 */
export function createBulkTaskActions(deps: BulkTaskActionDeps): BulkTaskActions {
    const { tasks, projectId, statusLabel, api, board, dialog, moveTask } = deps;

    async function onDelete(taskIds: string[], titles: string[]): Promise<void> {
        const confirmed = await dialog.showConfirm({
            title: t('board.deleteTasks.title'),
            message: t('board.deleteTasks.message', { count: taskIds.length }),
            items: titles,
            confirmText: t('board.confirm.delete'),
            cancelText: t('board.confirm.cancel'),
        });
        if (!confirmed) return;
        // Only send DELETE for top-level tasks — the server recursively deletes descendants
        for (const id of filterTopLevelTaskIds(taskIds, tasks())) {
            try {
                await api.deleteTask(projectId(), id);
                board.remove(id);
            } catch (err) {
                dialog.showToast(t('board.toast.deleteFailed', { error: String(err) }), 'error');
            }
        }
        deps.onRefreshBoard?.();
    }

    async function onStart(taskIds: string[]): Promise<void> {
        let started = 0;
        for (const id of taskIds) {
            try {
                const task = tasks().find(candidate => candidate.id === id);
                if (!task) continue;
                // A subtask starts through its own endpoint, which answers with the parent's status.
                const updated = task.parent_task_id
                    ? await api.startSubtask(projectId(), id)
                    : await api.startTask(projectId(), id);
                board.update(updated);
                started++;
            } catch (err) {
                dialog.showToast(t('board.toast.startFailed', { error: String(err) }), 'error');
            }
        }
        if (started > 0) {
            dialog.showToast(t('board.toast.started', { count: started }), 'success');
        }
    }

    async function onMove(taskIds: string[], status: string): Promise<void> {
        let moved = 0;
        for (const id of taskIds) {
            const task = tasks().find(candidate => candidate.id === id);
            if (!task) continue;
            if (await moveTask(task, status)) moved++;
        }
        if (moved > 0) {
            dialog.showToast(t('board.toast.movedCount', { count: moved, status: statusLabel(status) }), 'success');
        }
    }

    async function onModelChange(
        taskIds: string[],
        providerUrl: string,
        modelName: string | undefined,
        reasoningEffort: string | undefined,
    ): Promise<void> {
        let updated = 0;
        for (const id of taskIds) {
            try {
                const taskUpdate: Partial<Task> = { llama_server_url: providerUrl };
                if (modelName) taskUpdate.model_name = modelName;
                else taskUpdate.model_name = undefined;
                if (reasoningEffort) taskUpdate.reasoning_effort = reasoningEffort;
                else taskUpdate.reasoning_effort = undefined;
                board.update(await api.updateTask(projectId(), id, taskUpdate));
                updated++;
            } catch (err) {
                dialog.showToast(t('board.toast.modelUpdateFailed', { error: String(err) }), 'error');
            }
        }
        if (updated > 0) {
            dialog.showToast(t('board.toast.modelUpdated', { count: updated }), 'success');
        }
    }

    return { onDelete, onStart, onMove, onModelChange };
}
