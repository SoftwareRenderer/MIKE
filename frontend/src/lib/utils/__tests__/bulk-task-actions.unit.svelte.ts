import { describe, it, expect, vi } from 'vitest';
import { createBulkTaskActions, type BulkTaskActionDeps } from '$lib/utils/bulk-task-actions';
import { t } from '$lib/i18n';
import type { Task } from '$lib/types';

function makeTask(id: string, status: string, parentId?: string): Task {
    return {
        id,
        project_id: 'proj-1',
        title: `Task ${id}`,
        prompt: '',
        status,
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: false,
        auto_merge: false,
        subtasks_auto_merge: false,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        moved_at: new Date().toISOString(),
        ...(parentId ? { parent_task_id: parentId } : {}),
    };
}

/** A backlog root with a done child, plus an unrelated review task. */
function tasks(): Task[] {
    return [makeTask('root', 'backlog'), makeTask('child', 'done', 'root'), makeTask('solo', 'review')];
}

/** Deps whose api answers every call successfully; overrides replace single members. */
function harness(overrides: Partial<BulkTaskActionDeps> = {}) {
    const calls = { updated: [] as Task[], removed: [] as string[] };
    const dialog = { showConfirm: vi.fn().mockResolvedValue(true), showToast: vi.fn() };
    const api = {
        deleteTask: vi.fn().mockResolvedValue(undefined),
        startTask: vi.fn().mockImplementation((_p: string, id: string) => Promise.resolve({ ...makeTask(id, 'in_progress') })),
        startSubtask: vi.fn().mockImplementation((_p: string, id: string) => Promise.resolve({ ...makeTask(id, 'in_progress', 'root') })),
        updateTask: vi.fn().mockImplementation((_p: string, id: string, update: Partial<Task>) => Promise.resolve({ ...makeTask(id, 'review'), ...update })),
    };
    const moveTask = vi.fn().mockResolvedValue(true);
    const onRefreshBoard = vi.fn();
    const deps: BulkTaskActionDeps = {
        tasks,
        projectId: () => 'proj-1',
        statusLabel: (status) => `[${status}]`,
        api,
        board: { update: (task) => calls.updated.push(task), remove: (taskId) => calls.removed.push(taskId) },
        dialog,
        moveTask,
        onRefreshBoard,
        ...overrides,
    };
    return { actions: createBulkTaskActions(deps), api, dialog, moveTask, onRefreshBoard, calls };
}

describe('createBulkTaskActions — delete', () => {
    it('asks once, naming the count and listing the titles', async () => {
        const h = harness();
        await h.actions.onDelete(['root', 'solo'], ['Task root', 'Task solo']);

        expect(h.dialog.showConfirm).toHaveBeenCalledWith({
            title: t('board.deleteTasks.title'),
            message: t('board.deleteTasks.message', { count: 2 }),
            items: ['Task root', 'Task solo'],
            confirmText: t('board.confirm.delete'),
            cancelText: t('board.confirm.cancel'),
        });
    });

    it('sends nothing when the answer is no', async () => {
        const h = harness({ dialog: { showConfirm: vi.fn().mockResolvedValue(false), showToast: vi.fn() } });
        await h.actions.onDelete(['root'], ['Task root']);

        expect(h.api.deleteTask).not.toHaveBeenCalled();
        expect(h.onRefreshBoard).not.toHaveBeenCalled();
    });

    it('sends one delete per top-level task, because the server takes the subtree with it', async () => {
        const h = harness();
        await h.actions.onDelete(['root', 'child', 'solo'], []);

        expect(h.api.deleteTask.mock.calls.map(c => c[1])).toEqual(['root', 'solo']);
        expect(h.calls.removed).toEqual(['root', 'solo']);
        expect(h.onRefreshBoard).toHaveBeenCalledTimes(1);
    });

    it('reports each failure and still sends the rest of the batch', async () => {
        const h = harness();
        h.api.deleteTask.mockRejectedValueOnce(new Error('boom'));
        await h.actions.onDelete(['root', 'solo'], []);

        expect(h.dialog.showToast).toHaveBeenCalledWith(
            t('board.toast.deleteFailed', { error: 'Error: boom' }),
            'error',
        );
        expect(h.api.deleteTask).toHaveBeenCalledTimes(2);
    });
});

describe('createBulkTaskActions — start', () => {
    it('starts a subtask through the subtask endpoint and a top-level task through its own', async () => {
        const h = harness();
        await h.actions.onStart(['root', 'child']);

        expect(h.api.startTask).toHaveBeenCalledWith('proj-1', 'root');
        expect(h.api.startSubtask).toHaveBeenCalledWith('proj-1', 'child');
        expect(h.calls.updated.map(task => task.id)).toEqual(['root', 'child']);
        expect(h.dialog.showToast).toHaveBeenCalledWith(t('board.toast.started', { count: 2 }), 'success');
    });

    it('counts only what started, and stays silent when nothing did', async () => {
        const h = harness();
        h.api.startTask.mockRejectedValue(new Error('busy'));
        await h.actions.onStart(['root', 'solo']);

        expect(h.dialog.showToast).toHaveBeenNthCalledWith(1, t('board.toast.startFailed', { error: 'Error: busy' }), 'error');
        expect(h.dialog.showToast).toHaveBeenCalledTimes(2);
        expect(h.calls.updated).toEqual([]);

        const none = harness();
        await none.actions.onStart(['missing']);
        expect(none.api.startTask).not.toHaveBeenCalled();
        expect(none.dialog.showToast).not.toHaveBeenCalled();
    });
});

describe('createBulkTaskActions — move', () => {
    it('hands every selected task to the injected move path and counts what it accepted', async () => {
        const h = harness();
        h.moveTask.mockResolvedValueOnce(false);
        await h.actions.onMove(['root', 'solo'], 'review');

        expect(h.moveTask.mock.calls.map(([, status]) => status)).toEqual(['review', 'review']);
        expect(h.moveTask.mock.calls[0][0].id).toBe('root');
        expect(h.dialog.showToast).toHaveBeenCalledWith(
            t('board.toast.movedCount', { count: 1, status: '[review]' }),
            'success',
        );
    });

    it('says nothing when no move happened', async () => {
        const h = harness({ moveTask: vi.fn().mockResolvedValue(false) });
        await h.actions.onMove(['root'], 'done');

        expect(h.dialog.showToast).not.toHaveBeenCalled();
    });

    it('leaves a task the view does not hold unsent', async () => {
        const h = harness();
        await h.actions.onMove(['gone'], 'done');

        expect(h.moveTask).not.toHaveBeenCalled();
    });
});

describe('createBulkTaskActions — change model', () => {
    it('sends the provider with the model and effort, and counts each write', async () => {
        const h = harness();
        await h.actions.onModelChange(['root', 'solo'], 'http://server:8080', 'llama-3', 'high');

        expect(h.api.updateTask).toHaveBeenNthCalledWith(1, 'proj-1', 'root', {
            llama_server_url: 'http://server:8080',
            model_name: 'llama-3',
            reasoning_effort: 'high',
        });
        expect(h.calls.updated.map(task => task.id)).toEqual(['root', 'solo']);
        expect(h.dialog.showToast).toHaveBeenCalledWith(t('board.toast.modelUpdated', { count: 2 }), 'success');
    });

    it('clears a model or an effort the picker left empty', async () => {
        const h = harness();
        await h.actions.onModelChange(['root'], 'http://server:8080', undefined, undefined);

        const sent = h.api.updateTask.mock.calls[0][2];
        expect('model_name' in sent).toBe(true);
        expect(sent.model_name).toBeUndefined();
        expect('reasoning_effort' in sent).toBe(true);
        expect(sent.reasoning_effort).toBeUndefined();
    });

    it('reports a failed write and toasts nothing when every one failed', async () => {
        const h = harness();
        h.api.updateTask.mockRejectedValue(new Error('nope'));
        await h.actions.onModelChange(['root'], 'http://server:8080', 'llama-3', undefined);

        expect(h.dialog.showToast).toHaveBeenCalledWith(
            t('board.toast.modelUpdateFailed', { error: 'Error: nope' }),
            'error',
        );
        expect(h.dialog.showToast).toHaveBeenCalledTimes(1);
    });
});
