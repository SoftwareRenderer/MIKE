import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from './fake-indexeddb';

let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    restoreIndexedDB = installFakeIndexedDB();
});

afterEach(() => restoreIndexedDB?.());

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('offline content cache keys', () => {
    it('produces namespaced keys for messages, full messages, and diffs', async () => {
        const { messagesCacheKey, messageFullCacheKey, diffCacheKey, doneDiffCacheKey, doneStatsCacheKey } = await import('$lib/storage');

        expect(messagesCacheKey('proj-a', 'task-1')).toBe('messages:proj-a:task-1');
        expect(messageFullCacheKey('proj-a', 'task-1', 'msg-9')).toBe('message-full:proj-a:task-1:msg-9');
        expect(diffCacheKey('proj-a', 'task-1')).toBe('diff:proj-a:task-1');
        expect(doneDiffCacheKey('proj-a', 'task-1', 'sha-tip')).toBe('diff:proj-a:task-1:sha-tip');
        expect(doneStatsCacheKey('proj-a', 'task-1')).toBe('done-stats:proj-a:task-1');
    });
});

describe('offline content cache GC', () => {
    it('removes stale message/diff entries for the current project, keeps valid, other projects, and board keys', async () => {
        const { setBoardCache, getBoardCache, gc } = await import('$lib/storage');

        const pid = 'proj-a';
        const validTask = 'task-valid';
        const staleTask = 'task-stale';
        const otherProject = 'proj-b';

        await setBoardCache(`messages:${pid}:${validTask}`, []);
        await setBoardCache(`messages:${pid}:${staleTask}`, []);
        await setBoardCache(`message-full:${pid}:${staleTask}:msg1`, {});
        await setBoardCache(`diff:${pid}:${staleTask}`, {});
        // Done-task caches (4-part done-diff key and done-stats key) follow the
        // same task-id-at-parts[2] sweep rule.
        await setBoardCache(`diff:${pid}:${staleTask}:sha-tip`, { diff: 'x' });
        await setBoardCache(`diff:${pid}:${validTask}:sha-tip`, { diff: 'x' });
        await setBoardCache(`done-stats:${pid}:${staleTask}`, { updated_at: 't', stats: {} });
        await setBoardCache(`done-stats:${pid}:${validTask}`, { updated_at: 't', stats: {} });
        // Same stale task id but under a different project — must be preserved
        await setBoardCache(`messages:${otherProject}:${staleTask}`, []);
        await setBoardCache(`board:${pid}`, { tasks: [] });
        await setBoardCache('projects', []);

        await gc(new Set([validTask]), undefined, undefined, pid);

        expect(await getBoardCache(`messages:${pid}:${validTask}`)).not.toBeNull();
        expect(await getBoardCache(`messages:${pid}:${staleTask}`)).toBeNull();
        expect(await getBoardCache(`message-full:${pid}:${staleTask}:msg1`)).toBeNull();
        expect(await getBoardCache(`diff:${pid}:${staleTask}`)).toBeNull();
        expect(await getBoardCache(`diff:${pid}:${staleTask}:sha-tip`)).toBeNull();
        expect(await getBoardCache(`diff:${pid}:${validTask}:sha-tip`)).not.toBeNull();
        expect(await getBoardCache(`done-stats:${pid}:${staleTask}`)).toBeNull();
        expect(await getBoardCache(`done-stats:${pid}:${validTask}`)).not.toBeNull();
        expect(await getBoardCache(`messages:${otherProject}:${staleTask}`)).not.toBeNull();
        expect(await getBoardCache(`board:${pid}`)).not.toBeNull();
        expect(await getBoardCache('projects')).not.toBeNull();
    });

    it('leaves message/diff caches untouched when no projectId is provided', async () => {
        const { setBoardCache, getBoardCache, gc } = await import('$lib/storage');

        await setBoardCache('messages:p:t', []);
        await setBoardCache('diff:p:t', {});

        await gc(new Set([]), undefined, undefined, undefined);

        expect(await getBoardCache('messages:p:t')).not.toBeNull();
        expect(await getBoardCache('diff:p:t')).not.toBeNull();
    });
});
