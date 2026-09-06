import { describe, it, expect, vi } from 'vitest';
import { DiffCache, type DiffCacheApis, type DiffApiResponse } from '$lib/services/diff-cache.svelte';
import type { DiffRow } from '$lib/types/diff';

function makeApis(overrides: Partial<DiffCacheApis> = {}): DiffCacheApis {
    return {
        getTaskDiff: vi.fn(async () => ({ files: {} })),
        getDiffFile: vi.fn(async () => ({ rows: [] })),
        stageFile: vi.fn(async () => {}),
        enrichRows: (rows) => rows as DiffRow[],
        ...overrides,
    };
}

describe('DiffCache', () => {
    it('starts with empty state', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        expect(cache.state.files).toEqual({});
        expect(cache.state.untracked).toEqual({});
        expect(cache.state.loading).toBe(false);
    });

    it('updateDiff populates files from manifest', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.updateDiff({
            files: {
                'src/main.ts': { hash: 'abc', type: 'tracked', added: 5, removed: 2, staged: false },
            },
        });
        expect(cache.state.files['src/main.ts'].added).toBe(5);
        expect(cache.state.files['src/main.ts'].removed).toBe(2);
        expect(cache.fileHashes['src/main.ts']).toBe('abc');
    });

    it('updateDiff clears cache on empty manifest', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.updateDiff({
            files: {
                'src/main.ts': { hash: 'abc', type: 'tracked', added: 5, removed: 2, staged: false },
            },
        });
        cache.updateDiff({});
        expect(cache.state.files).toEqual({});
        expect(cache.fileHashes).toEqual({});
    });

    it('applyStagedPaths updates staged flags', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        cache.applyStagedPaths(['a.ts']);
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['b.ts'].staged).toBe(false);
    });

    it('processStats with empty hash clears cache', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 } };
        cache.tracker.hash = 'prev-hash'; // was tracking a non-empty diff
        cache.processStats({ hash: '', files: 0, additions: 0, deletions: 0 }, false);
        expect(cache.state.files).toEqual({});
    });

    it('processStats skips when diffLoading is true', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 } };
        cache.processStats({ hash: 'new-hash', files: 1, additions: 0, deletions: 0 }, true);
        // Should not have changed
        expect(cache.state.files['a.ts']).toBeDefined();
    });

    it('processStats removes deleted files', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0 },
        };
        cache.fileHashes = { 'a.ts': 'h1', 'b.ts': 'h2' };
        cache.tracker.hash = 'old-hash';
        cache.processStats({
            hash: 'new-hash',
            files: 1,
            additions: 0,
            deletions: 0,
            removed_files: ['b.ts'],
        }, false);
        expect(cache.state.files['a.ts']).toBeDefined();
        expect(cache.state.files['b.ts']).toBeUndefined();
        expect(cache.fileHashes['b.ts']).toBeUndefined();
    });

    it('reset clears all state', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 } };
        cache.fileHashes = { 'a.ts': 'h1' };
        cache.tracker.hash = 'some-hash';
        cache.reset();
        expect(cache.state.files).toEqual({});
        expect(cache.fileHashes).toEqual({});
        expect(cache.tracker.hash).toBeNull();
    });

    it('state is reactive via $state()', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        expect(cache.state.files).toEqual({});
        cache.updateDiff({
            files: { 'a.ts': { hash: 'h1', type: 'tracked', added: 1, removed: 0 } },
        });
        expect(cache.state.files['a.ts']).toBeDefined();
        expect(cache.state.files['a.ts'].added).toBe(1);
    });

    it('stageFile does optimistic update', async () => {
        const apis = makeApis();
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        let revertCalled = false;
        await cache.stageFile('a.ts', true, () => { revertCalled = true; });
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(apis.stageFile).toHaveBeenCalledWith('proj-1', 'task-1', 'a.ts', true);
        expect(revertCalled).toBe(false);
    });

    it('stageFile calls onRevert on failure', async () => {
        const apis = makeApis({
            stageFile: vi.fn(async () => { throw new Error('fail'); }),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        const revertPaths: string[] = [];
        await expect(cache.stageFile('a.ts', true, (path) => { revertPaths.push(path); })).rejects.toThrow('fail');
        expect(revertPaths).toContain('a.ts');
    });

    it('stagedPathsKey produces sorted key', () => {
        expect(DiffCache.stagedPathsKey(['b.ts', 'a.ts'])).toBe('a.ts\nb.ts');
        expect(DiffCache.stagedPathsKey(null)).toBe('');
        expect(DiffCache.stagedPathsKey(undefined)).toBe('');
        expect(DiffCache.stagedPathsKey([])).toBe('');
    });

    it('getters provide live projectId and taskId for API calls', () => {
        const apis = makeApis();
        let currentProject = 'proj-1';
        let currentTask = 'task-1';
        const cache = new DiffCache(apis, () => currentProject, () => currentTask);
        // Verify initial IDs are used
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        cache.stageFile('a.ts', true, () => {});
        expect(apis.stageFile).toHaveBeenCalledWith('proj-1', 'task-1', 'a.ts', true);
        // Change the values the getters close over
        currentProject = 'proj-2';
        currentTask = 'task-2';
        // Verify subsequent stageFile uses new IDs
        cache.stageFile('a.ts', false, () => {});
        expect(apis.stageFile).toHaveBeenCalledWith('proj-2', 'task-2', 'a.ts', false);
    });

    it('revertStageFile replaces file via spread for Svelte reactivity', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: true },
        };
        cache.revertStageFile('a.ts', true);
        expect(cache.state.files['a.ts'].staged).toBe(false);
    });

    it('setFile replaces files record via spread for Svelte reactivity', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 },
        };
        cache.setFile('a.ts', { path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'x' }], added: 1, removed: 0 });
        expect(cache.state.files['a.ts'].rows.length).toBe(1);
    });

    it('setUntrackedMeta replaces untracked record via spread for Svelte reactivity', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.untracked = {
            'a.ts': { fileSize: 100, loading: false },
        };
        cache.setUntrackedMeta('a.ts', { fileSize: 200, loading: true });
        expect(cache.state.untracked['a.ts'].fileSize).toBe(200);
    });

    it('applyStagedPaths replaces files record via spread for Svelte reactivity', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        cache.applyStagedPaths(['a.ts']);
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['b.ts'].staged).toBe(false);
    });

    it('processStats reconciles full manifest when hash changes with no file_hashes', async () => {
        const getTaskDiff = vi.fn(async () => ({
            files: {
                'a.ts': { hash: 'h1', type: 'tracked', added: 5, removed: 2, staged: false },
            },
        }));
        const getDiffFile = vi.fn(async () => ({ rows: [] }));
        const cache = new DiffCache(makeApis({ getTaskDiff, getDiffFile }), () => 'proj-1', () => 'task-1');
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 } };
        cache.tracker.hash = 'old-hash';
        // Hash changed but file_hashes omitted/empty — must not silently ignore it.
        cache.processStats({ hash: 'new-hash', files: 1, additions: 0, deletions: 0 }, false);
        await Promise.resolve();
        await Promise.resolve();
        expect(getTaskDiff).toHaveBeenCalled();
    });

    it('processStats fetches delta files on hash change with file_hashes', async () => {
        const getDiffFile = vi.fn(async () => ({ rows: [] }));
        const cache = new DiffCache(makeApis({ getDiffFile }), () => 'proj-1', () => 'task-1');
        cache.fileHashes = { 'a.ts': 'h-old' };
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'x' }], added: 1, removed: 0 } };
        cache.tracker.hash = 'old-hash';
        cache.processStats({ hash: 'new-hash', files: 1, additions: 0, deletions: 0, file_hashes: { 'a.ts': 'h-new' } }, false);
        await Promise.resolve();
        await Promise.resolve();
        expect(getDiffFile).toHaveBeenCalledWith('proj-1', 'task-1', 'a.ts', expect.anything());
    });

    it('fetchFullManifest drops untracked entries that transitioned to tracked (staging)', async () => {
        const getTaskDiff = vi.fn(async () => ({
            files: {
                'new.txt': { hash: 'h2', type: 'tracked', added: 1, removed: 0, staged: true },
            },
        }));
        const getDiffFile = vi.fn(async () => ({ rows: [{ lineNumber: 1, variant: 'added', text: 'hi' }] }));
        const cache = new DiffCache(makeApis({ getTaskDiff, getDiffFile }), () => 'proj-1', () => 'task-1');
        // new.txt is currently cached as an untracked file.
        cache.state.files = { 'new.txt': { path: 'new.txt', rows: [{ lineNumber: 1, variant: 'added', text: 'hi' }], added: 1, removed: 0, staged: false } };
        cache.state.untracked = { 'new.txt': { loading: false } };
        cache.fileHashes = { 'new.txt': 'h1' };
        cache.tracker.hash = 'old-hash';
        cache.tracker.untrackedN = 1;
        // Broadcast: new.txt is now tracked + staged and untracked count dropped to 0.
        cache.processStats({ hash: 'new-hash', files: 1, additions: 1, deletions: 0, untracked_n: 0, file_hashes: { 'new.txt': 'h2' }, staged_paths: ['new.txt'] }, false);
        await new Promise(r => setTimeout(r, 0));
        await new Promise(r => setTimeout(r, 0));
        // The untracked entry must be gone so the panel moves the file to Staged.
        expect(cache.state.untracked['new.txt']).toBeUndefined();
        expect(cache.state.files['new.txt'].staged).toBe(true);
    });

    it('processStats removes files via spread for Svelte reactivity', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0 },
        };
        cache.state.untracked = {
            'b.ts': { fileSize: 100 },
        };
        cache.fileHashes = { 'a.ts': 'h1', 'b.ts': 'h2' };
        cache.tracker.hash = 'old-hash';
        cache.processStats({
            hash: 'new-hash',
            files: 1,
            additions: 0,
            deletions: 0,
            removed_files: ['b.ts'],
        }, false);
        expect(cache.state.files['a.ts']).toBeDefined();
        expect(cache.state.files['b.ts']).toBeUndefined();
        expect(cache.state.untracked['b.ts']).toBeUndefined();
    });

    it('loadDiff includes blank untracked files (line_count=0)', async () => {
        const apis = makeApis({
            getTaskDiff: vi.fn(async () => ({
                files: {
                    'empty.txt': { hash: 'h-blank', type: 'untracked', line_count: 0, added: 0, staged: false },
                    'has-content.txt': { hash: 'h-content', type: 'untracked', line_count: 3, added: 3, staged: false },
                },
            })),
            getDiffFile: vi.fn(async (_projectId, _taskId, path) => {
                if (path === 'empty.txt') return { lines: [] };
                return { lines: ['a', 'b', 'c'] };
            }),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        await cache.loadDiff(() => {});
        // Both files should be in the cache, including the blank one
        expect(cache.state.files['empty.txt']).toBeDefined();
        expect(cache.state.files['empty.txt'].rows).toEqual([]);
        expect(cache.state.untracked['empty.txt']).toBeDefined();
        expect(cache.state.files['has-content.txt']).toBeDefined();
        expect(cache.state.files['has-content.txt'].rows.length).toBe(3);
    });

    it('stageAll iteratively stages each unstaged file', async () => {
        const apis = makeApis();
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
            'c.ts': { path: 'c.ts', rows: [], added: 0, removed: 0, staged: true }, // already staged
        };
        await cache.stageAll();
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['b.ts'].staged).toBe(true);
        expect(cache.state.files['c.ts'].staged).toBe(true);
        expect(apis.stageFile).toHaveBeenCalledWith('proj-1', 'task-1', 'a.ts', true);
        expect(apis.stageFile).toHaveBeenCalledWith('proj-1', 'task-1', 'b.ts', true);
        expect(apis.stageFile).not.toHaveBeenCalledWith('proj-1', 'task-1', 'c.ts', true);
    });

    it('unstageAll iteratively unstages each staged file', async () => {
        const apis = makeApis();
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: true },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: true },
            'c.ts': { path: 'c.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        await cache.unstageAll();
        expect(cache.state.files['a.ts'].staged).toBe(false);
        expect(cache.state.files['b.ts'].staged).toBe(false);
        expect(cache.state.files['c.ts'].staged).toBe(false);
        expect(apis.stageFile).toHaveBeenCalledWith('proj-1', 'task-1', 'a.ts', false);
        expect(apis.stageFile).toHaveBeenCalledWith('proj-1', 'task-1', 'b.ts', false);
        expect(apis.stageFile).not.toHaveBeenCalledWith('proj-1', 'task-1', 'c.ts', false);
    });

    it('stageAll skips individual failures without breaking others', async () => {
        const stageFn = vi.fn(async (_projectId, _taskId, path, _stage) => {
            if (path === 'b.ts') throw new Error('fail');
        });
        const apis = makeApis({ stageFile: stageFn });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
            'c.ts': { path: 'c.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        await cache.stageAll();
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['b.ts'].staged).toBe(false); // reverted
        expect(cache.state.files['c.ts'].staged).toBe(true);
    });

    it('unstageAll skips individual failures without breaking others', async () => {
        const stageFn = vi.fn(async (_projectId, _taskId, path, _stage) => {
            if (path === 'b.ts') throw new Error('fail');
        });
        const apis = makeApis({ stageFile: stageFn });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: true },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: true },
            'c.ts': { path: 'c.ts', rows: [], added: 0, removed: 0, staged: true },
        };
        await cache.unstageAll();
        expect(cache.state.files['a.ts'].staged).toBe(false);
        expect(cache.state.files['b.ts'].staged).toBe(true); // reverted
        expect(cache.state.files['c.ts'].staged).toBe(false);
    });

    it('getters reflect changes mid-flight for subsequent calls', async () => {
        let resolveDiff: (value: DiffApiResponse) => void;
        let currentProject = 'proj-1';
        let currentTask = 'task-1';
        const apis = makeApis({
            getTaskDiff: vi.fn<() => Promise<DiffApiResponse>>(async () => new Promise(resolve => { resolveDiff = resolve; })),
        });
        const cache = new DiffCache(apis, () => currentProject, () => currentTask);
        const loadPromise = cache.loadDiff(() => {});
        // Switch task while loadDiff is in flight
        currentProject = 'proj-2';
        currentTask = 'task-2';
        // Resolve the original diff
        resolveDiff!({ files: { 'a.ts': { hash: 'h1', type: 'tracked', added: 1, removed: 0, staged: false } } });
        await loadPromise;
        // Original call used old IDs (captured at call time)
        expect(apis.getTaskDiff).toHaveBeenNthCalledWith(1, 'proj-1', 'task-1', undefined);
        // Subsequent calls use new IDs
        cache.state.files = {
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        cache.stageFile('b.ts', true, () => {});
        expect(apis.stageFile).toHaveBeenCalledWith('proj-2', 'task-2', 'b.ts', true);
    });

    // --- "Show full diff" must not re-derive what the per-file endpoint never answered ---

    it('patchFile merges into the cached entry instead of replacing it', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'x' }], added: 1, removed: 0, staged: true, isNewFile: true },
        };
        cache.patchFile('a.ts', { rows: [], added: 0, removed: 0, truncated: false });
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['a.ts'].isNewFile).toBe(true);
        expect(cache.state.files['a.ts'].rows).toEqual([]);
        expect(cache.state.files['a.ts'].truncated).toBe(false);
    });

    it('patchFile creates the entry when the path is not cached', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.patchFile('new.ts', { added: 2 });
        expect(cache.state.files['new.ts'].path).toBe('new.ts');
        expect(cache.state.files['new.ts'].added).toBe(2);
    });

    it('expandFile keeps a staged file staged and its status badge intact', async () => {
        const fullRows = Array.from({ length: 5 }, (_v, i) => ({ lineNumber: i + 1, variant: 'added', text: `line ${i}` }));
        const getDiffFile = vi.fn(async () => ({ rows: fullRows, truncated: false }));
        const cache = new DiffCache(makeApis({ getDiffFile }), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': {
                path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'line 0' }], added: 1, removed: 0,
                truncated: true, origAdded: 120, origRemoved: 3, staged: true, isNewFile: true,
            },
        };
        expect(await cache.expandFile('a.ts')).toBe(true);
        const file = cache.state.files['a.ts'];
        // Regression: the entry was rebuilt from the response alone, so `staged` became
        // undefined and the panel rendered the file under Unstaged (badge A -> M too).
        expect(file.staged).toBe(true);
        expect(file.isNewFile).toBe(true);
        expect(file.rows.length).toBe(5);
        expect(file.added).toBe(5);
        expect(file.removed).toBe(0);
        expect(file.truncated).toBe(false);
        expect(getDiffFile).toHaveBeenCalledWith('proj-1', 'task-1', 'a.ts', { full: true });
    });

    it('expandFile takes committedRows from the response, not from the cache', async () => {
        const getDiffFile = vi.fn(async () => ({ rows: [{ lineNumber: 1, variant: 'added', text: 'x' }], truncated: false }));
        const cache = new DiffCache(makeApis({ getDiffFile }), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, truncated: true, staged: true, committedRows: [{ lineNumber: 1, variant: 'added', text: 'old' }] },
        };
        await cache.expandFile('a.ts');
        expect(cache.state.files['a.ts'].committedRows).toEqual([]);
        expect(cache.state.files['a.ts'].staged).toBe(true);
    });

    it('expandFile is a no-op for an uncached path or a rowless response', async () => {
        const getDiffFile = vi.fn(async () => ({ rows: undefined, lines: ['a'] }));
        const cache = new DiffCache(makeApis({ getDiffFile }), () => 'proj-1', () => 'task-1');
        expect(await cache.expandFile('missing.ts')).toBe(false);
        expect(getDiffFile).not.toHaveBeenCalled();

        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: true } };
        expect(await cache.expandFile('a.ts')).toBe(false);
        expect(cache.state.files['a.ts'].staged).toBe(true);
    });
});
