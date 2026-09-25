import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock storage functions before importing helpers
vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async () => false),
    saveAppSetting: vi.fn(async () => {}),
}));

import { updateTaskInBoard, removeTaskFromBoard, appendTaskToBoard, applyResolvedTasksToBoard } from '$lib/stores/app.svelte';
import { appStore } from '$lib/stores';
import type { Board, Task } from '$lib/types';

function makeTask(id: string, over: Partial<Task> = {}): Task {
    return { id, project_id: 'p1', title: id, status: 'backlog', ...over } as Task;
}

describe('the one task list', () => {
    beforeEach(() => {
        appStore.board = null;
    });

    it('reads from the board, so no second list can disagree with it', () => {
        appStore.board = { project_id: 'p1', tasks: [makeTask('a'), makeTask('b')] } as Board;
        expect(appStore.tasks.map(t => t.id)).toEqual(['a', 'b']);
        appStore.board = null;
        expect(appStore.tasks).toEqual([]);
    });

    it('appends an optimistic task only while a board is on screen', () => {
        appendTaskToBoard(appStore, makeTask('a'));
        expect(appStore.board).toBeNull();
        expect(appStore.tasks).toEqual([]);

        appStore.board = { project_id: 'p1', tasks: [makeTask('a')] } as Board;
        appendTaskToBoard(appStore, makeTask('b'));
        expect(appStore.tasks.map(t => t.id)).toEqual(['a', 'b']);
    });

    it('rewrites a task by id and drops one by id', () => {
        appStore.board = { project_id: 'p1', tasks: [makeTask('a'), makeTask('b')] } as Board;
        updateTaskInBoard(appStore, 'a', makeTask('a', { status: 'review' }));
        expect(appStore.tasks.map(t => [t.id, t.status])).toEqual([['a', 'review'], ['b', 'backlog']]);

        removeTaskFromBoard(appStore, 'a');
        expect(appStore.tasks.map(t => t.id)).toEqual(['b']);
    });

    it('lands the answers of one flush onto the board: an optimistic id becomes the created task', () => {
        appStore.board = { project_id: 'p1', tasks: [makeTask('opt'), makeTask('keep')] } as Board;
        const created = makeTask('real', { status: 'in_progress' });
        const latest = makeTask('real', { status: 'review' });
        // A create answered with `created`, then a later queued action moved it: the newest wins.
        applyResolvedTasksToBoard(appStore, new Map([['opt', created], ['real', latest]]));
        expect(appStore.tasks.map(t => [t.id, t.status])).toEqual([['real', 'review'], ['keep', 'backlog']]);
    });

    it('lands a flush onto nothing when no board is on screen', () => {
        applyResolvedTasksToBoard(appStore, new Map([['opt', makeTask('real')]]));
        expect(appStore.board).toBeNull();
    });
});
