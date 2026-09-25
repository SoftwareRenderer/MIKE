import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock storage functions before importing helpers
vi.mock('$lib/storage', () => ({
    saveCollapsedTasks: vi.fn(async () => {}),
    pruneCollapsedTasks: vi.fn(async () => new Set<string>()),
    loadAppSetting: vi.fn(async () => false),
    saveAppSetting: vi.fn(async () => {}),
}));

import { persistCollapsedTasks, initExpandedTasks, updateTaskInBoard, removeTaskFromBoard, appendTaskToBoard, applyResolvedTasksToBoard } from '$lib/stores/app.svelte';
import { appStore } from '$lib/stores';
import * as storage from '$lib/storage';
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

describe('persistCollapsedTasks', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('saves parent IDs that are not in the expanded set', async () => {
        const expanded = new Set(['t1', 't3']);
        const parents = new Set(['t1', 't2', 't3', 't4']);
        await persistCollapsedTasks(expanded, parents);
        expect(storage.saveCollapsedTasks).toHaveBeenCalledWith(
            expect.objectContaining({ size: 2 }),
        );
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0];
        expect(saved.has('t2')).toBe(true);
        expect(saved.has('t4')).toBe(true);
        expect(saved.has('t1')).toBe(false);
    });

    it('saves empty set when all parents are expanded', async () => {
        const expanded = new Set(['t1', 't2']);
        const parents = new Set(['t1', 't2']);
        await persistCollapsedTasks(expanded, parents);
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0];
        expect(saved.size).toBe(0);
    });

    it('saves all parent IDs when none are expanded', async () => {
        const expanded = new Set<string>();
        const parents = new Set(['t1', 't2', 't3']);
        await persistCollapsedTasks(expanded, parents);
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0];
        expect(saved.size).toBe(3);
        expect(saved.has('t1')).toBe(true);
        expect(saved.has('t2')).toBe(true);
        expect(saved.has('t3')).toBe(true);
    });
});

describe('initExpandedTasks', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('returns all parent IDs minus collapsed ones', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t2']));
        const parents = new Set(['t1', 't2', 't3']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t2')).toBe(false);
        expect(expanded.has('t3')).toBe(true);
    });

    it('prunes against its own parent set, so a stale entry cannot survive a reload', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t2']));
        const parents = new Set(['t1', 't2', 't3']);
        const expanded = await initExpandedTasks(parents);
        expect(storage.pruneCollapsedTasks).toHaveBeenCalledWith(parents);
        expect(expanded.size).toBe(2);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t3')).toBe(true);
    });

    it('returns all parents when none are collapsed', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set<string>());
        const parents = new Set(['t1', 't2']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t2')).toBe(true);
    });

    it('returns empty set when all parents are collapsed', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t1', 't2']));
        const parents = new Set(['t1', 't2']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.size).toBe(0);
    });
});
