import { describe, it, expect, vi } from 'vitest';
import { DiffCache } from '$lib/services/diff-cache.svelte';
import type { GitBatchResult, TaskDiff } from '$lib/api';
import type { DiffRow } from '$lib/types/diff';
import { ConnectionFailure, RequestAborted } from '$lib/api/helpers';

type DiffCacheApis = ConstructorParameters<typeof DiffCache>[0];

function makeApis(overrides: Partial<DiffCacheApis> = {}): DiffCacheApis {
    return {
        getTaskDiff: vi.fn(async () => ({ files: {} })),
        getDiffFile: vi.fn(async () => ({ rows: [] })),
        stageTaskPaths: vi.fn(async () => ({ status: 'ok', action: 'staged', applied: [] as string[], failed: [] as { path: string; error: string }[] })),
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

    it('loads a headRef diff from the server-parsed files, not the patch text', async () => {
        // The commit-to-commit branch consumes parsed_files, so the shape the server sends is what lands in
        // state, including the isNew -> isNewFile rename. The hidden-swap-file skip is updateDiffFromFiles' own,
        // not this caller's.
        const apis = makeApis({
            getTaskDiff: vi.fn(async () => ({
                diff: 'diff --git a/a.ts b/a.ts\nnew file mode 100644\n+one\n',
                parsed_files: [
                    { path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'one' }], added: 1, removed: 0, isNew: true },
                    { path: 'dir/.notes.txt.swp', rows: [{ lineNumber: 1, variant: 'added', text: 'x' }], added: 1, removed: 0 },
                    { path: 'mode-only.txt', rows: [], added: 0, removed: 0 },
                ],
            })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        await cache.loadDiff(() => {}, { headRef: 'a'.repeat(40) });

        expect(Object.keys(cache.state.files).sort()).toEqual(['a.ts']);
        expect(cache.state.files['a.ts'].added).toBe(1);
        expect(cache.state.files['a.ts'].isNewFile).toBe(true);
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

    it('applyStagedPaths replaces the files record and each entry, which is what Svelte reads', () => {
        const cache = new DiffCache(makeApis(), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        const before = cache.state.files;
        const beforeA = before['a.ts'];

        cache.applyStagedPaths(['a.ts']);

        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['b.ts'].staged).toBe(false);
        // A `$derived` over the panel's files re-runs only when the object it reads is a different
        // object, so writing the flag in place would leave the panel showing the old staging state.
        expect(cache.state.files).not.toBe(before);
        expect(cache.state.files['a.ts']).not.toBe(beforeA);
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

    it('a path moves section only when the answer says it did', async () => {
        let resolveStage: (value: GitBatchResult) => void = () => {};
        const apis = makeApis({
            stageTaskPaths: vi.fn(() => new Promise<GitBatchResult>(resolve => { resolveStage = resolve; })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
        };

        const running = cache.stagePaths(['a.ts'], true);
        // The wait is what shows; the file stays where git says it is.
        expect(cache.state.pending).toEqual(['a.ts']);
        expect(cache.state.files['a.ts'].staged).toBe(false);
        expect(apis.stageTaskPaths).toHaveBeenCalledWith('proj-1', 'task-1', ['a.ts'], true);

        resolveStage({ status: 'ok', action: 'staged', applied: ['a.ts'], failed: [] });
        await running;
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.pending).toEqual([]);
    });

    it('a refused path stays pending-free and unstaged', async () => {
        const apis = makeApis({
            stageTaskPaths: vi.fn(async () => ({
                status: 'ok', action: 'staged', applied: [] as string[], failed: [{ path: 'a.ts', error: 'git add failed' }],
            })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false } };

        const answer = await cache.stagePaths(['a.ts'], true);
        expect(answer?.failed).toEqual([{ path: 'a.ts', error: 'git add failed' }]);
        expect(cache.state.files['a.ts'].staged).toBe(false);
        expect(cache.state.pending).toEqual([]);
    });

    it('a refused request leaves every path unstaged and unpended', async () => {
        const apis = makeApis({
            stageTaskPaths: vi.fn(async () => { throw new Error('fail'); }),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false } };

        await expect(cache.stagePaths(['a.ts'], true)).rejects.toThrow('fail');
        expect(cache.state.files['a.ts'].staged).toBe(false);
        expect(cache.state.pending).toEqual([]);
    });

    it('stagedPathsKey produces sorted key', () => {
        expect(DiffCache.stagedPathsKey(['b.ts', 'a.ts'])).toBe('a.ts\nb.ts');
        expect(DiffCache.stagedPathsKey(null)).toBe('');
        expect(DiffCache.stagedPathsKey(undefined)).toBe('');
        expect(DiffCache.stagedPathsKey([])).toBe('');
    });

    it('getters provide live projectId and taskId for API calls', async () => {
        const apis = makeApis();
        let currentProject = 'proj-1';
        let currentTask = 'task-1';
        const cache = new DiffCache(apis, () => currentProject, () => currentTask);
        // Verify initial IDs are used
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        await cache.stagePaths(['a.ts'], true);
        expect(apis.stageTaskPaths).toHaveBeenCalledWith('proj-1', 'task-1', ['a.ts'], true);
        // Change the values the getters close over
        currentProject = 'proj-2';
        currentTask = 'task-2';
        await cache.stagePaths(['b.ts'], false);
        expect(apis.stageTaskPaths).toHaveBeenCalledWith('proj-2', 'task-2', ['b.ts'], false);
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

    // A file fetch the cache cancelled itself (the user moved to another file) is not a failure, and
    // logging it is how a quick switch fills the console with errors for requests nobody awaits any more.
    // The pair is tested because silencing everything would pass the same way: a connection failure for the
    // very same file has to stay visible.
    it('drops a cancelled file fetch silently and still logs a connection failure', async () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        const getDiffFile = vi.fn((_proj: string, _task: string, path: string) =>
            path === 'a.ts'
                ? Promise.reject(new RequestAborted('/api/diff/file'))
                : Promise.reject(new ConnectionFailure({ cause: new TypeError('Failed to fetch') })),
        );
        const cache = new DiffCache(makeApis({ getDiffFile }), () => 'proj-1', () => 'task-1');
        cache.fileHashes = { 'a.ts': 'h-old', 'b.ts': 'h-old' };
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'x' }], added: 1, removed: 0 },
            'b.ts': { path: 'b.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'y' }], added: 1, removed: 0 },
        };
        cache.tracker.hash = 'old-hash';

        cache.processStats(
            { hash: 'new-hash', files: 2, additions: 0, deletions: 0, file_hashes: { 'a.ts': 'h-new', 'b.ts': 'h-new' } },
            false,
        );

        await vi.waitFor(() => expect(getDiffFile).toHaveBeenCalledTimes(2));
        await vi.waitFor(() => expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('b.ts'), expect.anything()));

        const loggedForCancelled = errorSpy.mock.calls.filter((args) => String(args[0]).includes('a.ts'));
        expect(loggedForCancelled).toEqual([]);
        errorSpy.mockRestore();
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

    it('stageAll names every unstaged tracked file in one request', async () => {
        const apis = makeApis({
            stageTaskPaths: vi.fn(async (_p, _t, paths: string[]) => ({
                status: 'ok', action: 'staged', applied: paths, failed: [],
            })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
            'c.ts': { path: 'c.ts', rows: [], added: 0, removed: 0, staged: true }, // already staged
        };
        await cache.stageAll();
        expect(apis.stageTaskPaths).toHaveBeenCalledTimes(1);
        expect(apis.stageTaskPaths).toHaveBeenCalledWith('proj-1', 'task-1', ['a.ts', 'b.ts'], true);
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['b.ts'].staged).toBe(true);
        expect(cache.state.files['c.ts'].staged).toBe(true);
    });

    it('unstageAll names every staged tracked file in one request', async () => {
        const apis = makeApis({
            stageTaskPaths: vi.fn(async (_p, _t, paths: string[]) => ({
                status: 'ok', action: 'unstaged', applied: paths, failed: [],
            })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: true },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: true },
            'c.ts': { path: 'c.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        await cache.unstageAll();
        expect(apis.stageTaskPaths).toHaveBeenCalledTimes(1);
        expect(apis.stageTaskPaths).toHaveBeenCalledWith('proj-1', 'task-1', ['a.ts', 'b.ts'], false);
        expect(cache.state.files['a.ts'].staged).toBe(false);
        expect(cache.state.files['b.ts'].staged).toBe(false);
        expect(cache.state.files['c.ts'].staged).toBe(false);
    });

    it('a refused path inside a batch stays where git left it', async () => {
        const apis = makeApis({
            stageTaskPaths: vi.fn(async (_p, _t, paths: string[]) => ({
                status: 'ok', action: 'staged', applied: paths.filter(path => path !== 'b.ts'), failed: [{ path: 'b.ts', error: 'git add failed' }],
            })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        const answer = await cache.stageAll();
        expect(cache.state.files['a.ts'].staged).toBe(true);
        expect(cache.state.files['b.ts'].staged).toBe(false);
        expect(answer?.failed.map(f => f.path)).toEqual(['b.ts']);
        expect(cache.state.pending).toEqual([]);
    });

    it('nothing is pending while a second write runs on other paths', async () => {
        let resolveFirst: (value: unknown) => void = () => {};
        const apis = makeApis({
            stageTaskPaths: vi.fn()
                .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; }))
                .mockResolvedValue({ status: 'ok', action: 'staged', applied: ['b.ts'], failed: [] }),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, staged: false },
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        const first = cache.stagePaths(['a.ts'], true);
        await cache.stagePaths(['b.ts'], true);
        expect(cache.state.pending).toEqual(['a.ts']);
        expect(cache.state.files['b.ts'].staged).toBe(true);
        resolveFirst({ status: 'ok', action: 'staged', applied: ['a.ts'], failed: [] });
        await first;
        expect(cache.state.pending).toEqual([]);
    });

    it('getters reflect changes mid-flight for subsequent calls', async () => {
        let resolveDiff: (value: TaskDiff) => void;
        let currentProject = 'proj-1';
        let currentTask = 'task-1';
        const apis = makeApis({
            getTaskDiff: vi.fn<() => Promise<TaskDiff>>(async () => new Promise(resolve => { resolveDiff = resolve; })),
        });
        const cache = new DiffCache(apis, () => currentProject, () => currentTask);
        const loadPromise = cache.loadDiff(() => {});
        // Switch task while loadDiff is in flight
        currentProject = 'proj-2';
        currentTask = 'task-2';
        // Resolve the original diff
        resolveDiff!({ files: { 'a.ts': { hash: 'h1', type: 'tracked', added: 1, removed: 0, staged: false } } });
        await loadPromise;
        // Original call used old IDs (captured at call time). The third argument carries parsed:true —
        // the diff grammar lives in backend/git now, so the cache always asks for the structured form.
        expect(apis.getTaskDiff).toHaveBeenNthCalledWith(1, 'proj-1', 'task-1', { parsed: true });
        // Subsequent calls use new IDs
        cache.state.files = {
            'b.ts': { path: 'b.ts', rows: [], added: 0, removed: 0, staged: false },
        };
        void cache.stagePaths(['b.ts'], true);
        expect(apis.stageTaskPaths).toHaveBeenCalledWith('proj-2', 'task-2', ['b.ts'], true);
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
        // The response said this file has no committed half, so the stale one goes - and goes as absent.
        // An empty array says "there is a committed half, it is empty", which is the answer the committed
        // section renders as an empty file over the rows on hand.
        expect(cache.state.files['a.ts'].committedRows).toBeUndefined();
        expect(cache.state.files['a.ts'].staged).toBe(true);
    });

    // A done task shows one immutable ref comparison and the board stream keeps pushing that task's
    // working-tree stats. Reconciling them asked `/diff/file` for every path - which answers the worktree
    // against the baseline, not the pair on screen - because the parsed load has no git hashes to compare.
    it('leaves a head-ref diff alone when working-tree stats arrive', async () => {
        const getDiffFile = vi.fn(async () => ({ rows: [{ lineNumber: 1, variant: 'added', text: 'worktree line' }] }));
        const apis = makeApis({
            getDiffFile,
            getTaskDiff: vi.fn(async () => ({
                diff: 'diff --git a/a.ts b/a.ts\n-removed\n',
                parsed_files: [{ path: 'a.ts', rows: [{ lineNumber: 4, variant: 'removed', text: 'the line on screen' }], added: 0, removed: 1 }],
            })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        await cache.loadDiff(() => {}, { headRef: 'a'.repeat(40) });
        cache.processStats({
            hash: 'worktree-hash', files: 1, additions: 1, deletions: 0,
            file_hashes: { 'a.ts': 'a-worktree-hash' },
        }, false);
        await Promise.resolve();
        await Promise.resolve();
        expect(getDiffFile).not.toHaveBeenCalled();
        expect(cache.state.files['a.ts'].rows.map(r => r.text)).toEqual(['the line on screen']);
    });

    it('reconciles stats again once the view goes back to the working tree', async () => {
        const getDiffFile = vi.fn(async () => ({ rows: [{ lineNumber: 1, variant: 'added', text: 'x' }] }));
        const apis = makeApis({
            getDiffFile,
            getTaskDiff: vi.fn(async () => ({ files: {} })),
        });
        const cache = new DiffCache(apis, () => 'proj-1', () => 'task-1');
        await cache.loadDiff(() => {}, { headRef: 'a'.repeat(40) });
        await cache.loadDiff(() => {});
        cache.state.files = { 'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0 } };
        cache.fileHashes = { 'a.ts': 'h-old' };
        cache.processStats({ hash: 'new-hash', files: 1, additions: 0, deletions: 0, file_hashes: { 'a.ts': 'h-new' } }, false);
        await Promise.resolve();
        await Promise.resolve();
        expect(getDiffFile).toHaveBeenCalledWith('proj-1', 'task-1', 'a.ts', expect.anything());
    });

    it('a delta fetch with no committed half leaves the key out instead of an empty array', async () => {
        const getDiffFile = vi.fn(async () => ({ rows: [{ lineNumber: 1, variant: 'added', text: 'x' }] }));
        const cache = new DiffCache(makeApis({ getDiffFile }), () => 'proj-1', () => 'task-1');
        cache.state.files = {
            'a.ts': { path: 'a.ts', rows: [], added: 0, removed: 0, committedRows: [{ lineNumber: 1, variant: 'added', text: 'stale' }] },
        };
        cache.fileHashes = { 'a.ts': 'h-old' };
        cache.tracker.hash = 'old-hash';
        cache.processStats({ hash: 'new-hash', files: 1, additions: 0, deletions: 0, file_hashes: { 'a.ts': 'h-new' } }, false);
        await vi.waitFor(() => expect(getDiffFile).toHaveBeenCalled());
        await vi.waitFor(() => expect(cache.state.files['a.ts'].committedRows).toBeUndefined());
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
