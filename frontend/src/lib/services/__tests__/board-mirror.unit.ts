import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    restoreIndexedDB = installFakeIndexedDB();
    vi.useFakeTimers();
});

afterEach(() => {
    vi.useRealTimers();
    restoreIndexedDB?.();
});

/** Both modules read the fake installed above, and `$lib/storage` opens lazily. */
async function load() {
    const mirror = await import('$lib/services/board-mirror');
    const storage = await import('$lib/storage');
    return { ...mirror, ...storage };
}

const board = (titles: string[]) => ({ tasks: titles.map(t => ({ id: t, title: t })) as never });

describe('the board mirror follows what the server answered', () => {
    it('writes once for a burst of frames, holding the newest snapshot', async () => {
        const { mirrorBoard, flushBoardMirror, getBoardCache, boardCacheKey, MIRROR_COALESCE_MS } = await load();

        mirrorBoard('proj-a', board(['first']));
        mirrorBoard('proj-a', board(['second']));
        mirrorBoard('proj-a', board(['third']));

        expect(await getBoardCache(boardCacheKey('proj-a'))).toBeNull();

        await flushBoardMirror();

        expect(await getBoardCache(boardCacheKey('proj-a'))).toEqual({ tasks: board(['third']).tasks });
        expect(MIRROR_COALESCE_MS).toBeGreaterThan(0);
    });

    it('closes the coalescing window by itself', async () => {
        const { mirrorBoard, getBoardCache, boardCacheKey, pendingMirrorProjects, MIRROR_COALESCE_MS } = await load();

        mirrorBoard('proj-a', board(['moved']));
        expect(pendingMirrorProjects()).toEqual(['proj-a']);

        await vi.advanceTimersByTimeAsync(MIRROR_COALESCE_MS);

        expect(pendingMirrorProjects()).toEqual([]);
        expect(await getBoardCache(boardCacheKey('proj-a'))).not.toBeNull();
    });

    it('keeps one key per project', async () => {
        const { mirrorBoard, flushBoardMirror, getBoardCache, boardCacheKey, pendingMirrorProjects } = await load();

        mirrorBoard('proj-a', board(['a']));
        mirrorBoard('proj-b', board(['b']));
        expect(pendingMirrorProjects().sort()).toEqual(['proj-a', 'proj-b']);

        await flushBoardMirror();

        expect(await getBoardCache(boardCacheKey('proj-a'))).not.toBeNull();
        expect(await getBoardCache(boardCacheKey('proj-b'))).not.toBeNull();
    });

    it('flush writes now and an empty flush rewrites nothing', async () => {
        const { mirrorBoard, flushBoardMirror, getBoardCacheEntry, boardCacheKey } = await load();

        mirrorBoard('proj-a', board(['a']));
        await flushBoardMirror();
        const written = await getBoardCacheEntry(boardCacheKey('proj-a'));
        expect(written).not.toBeNull();

        await vi.advanceTimersByTimeAsync(10_000);
        await flushBoardMirror();

        // A mirror read that wrote itself back would make "Data from …" say "just now" forever.
        expect((await getBoardCacheEntry(boardCacheKey('proj-a')))?.cachedAt).toBe(written!.cachedAt);
    });

    it('a later burst replaces the snapshot rather than merging into it', async () => {
        const { mirrorBoard, flushBoardMirror, getBoardCache, boardCacheKey } = await load();

        mirrorBoard('proj-a', board(['gone']));
        await flushBoardMirror();
        mirrorBoard('proj-a', board(['here']));
        await flushBoardMirror();

        expect(await getBoardCache(boardCacheKey('proj-a'))).toEqual({ tasks: board(['here']).tasks });
    });
});
