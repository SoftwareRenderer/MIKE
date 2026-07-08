import type { DiffFile, DiffRow } from '$lib/types/diff';

/** Metadata for untracked files. */
export interface UntrackedMeta {
    fileSize?: number;
    isBinary?: boolean;
    truncated?: boolean;
    origAdded?: number;
    loading?: boolean;
}

/** Diff stats from WebSocket push. */
export interface DiffStats {
    hash: string;
    files: number;
    additions: number;
    deletions: number;
    file_hashes?: Record<string, string>;
    removed_files?: string[];
    staged_paths?: string[];
}

/** API callbacks the component must provide. */
export interface DiffCacheApis {
    getTaskDiff: (projectId: string, taskId: string, options?: { headRef?: string; baseRef?: string; full?: boolean }) => Promise<DiffApiResponse>;
    getDiffFile: (projectId: string, taskId: string, path: string, options?: { signal?: AbortSignal; full?: boolean }) => Promise<DiffFileApiResponse>;
    stageFile: (projectId: string, taskId: string, path: string, stage: boolean) => Promise<void>;
    enrichRows: (rows: DiffRow[]) => DiffRow[];
}

/** Response from getTaskDiff API. */
export interface DiffApiResponse {
    files?: Record<string, {
        hash: string;
        type: string;
        added?: number;
        removed?: number;
        truncated?: boolean;
        origAdded?: number;
        origRemoved?: number;
        line_count?: number;
        file_size?: number;
        is_binary?: boolean;
        staged?: boolean;
    }>;
    short_stat?: string;
}

/** Response from getDiffFile API. */
export interface DiffFileApiResponse {
    rows?: DiffRow[];
    lines?: string[];
    truncated?: boolean;
    origAdded?: number;
    origRemoved?: number;
    fileSize?: number;
    isBinary?: boolean;
}

/**
 * Reactive class that encapsulates all diff cache logic.
 * Uses Svelte 5 $state() for deep reactivity — no version bumper needed.
 *
 * Usage:
 *   const cache = new DiffCache(apis, projectId, taskId);
 *   // Read directly from cache.state in templates:
 *   {#each Object.values(cache.state.files) as file}
 */
export class DiffCache {
    /** Reactive state — deeply tracked by Svelte 5. */
    state = $state({
        files: {} as Record<string, DiffFile>,
        untracked: {} as Record<string, UntrackedMeta>,
        loading: false,
    });

    /** Per-file hash tracker for incremental updates. */
    fileHashes: Record<string, string> = {};

    /** Gate: delta processing only starts after initial load completes. */
    initialLoaded = false;

    /** Generation counter — incremented on each full loadDiff(). */
    loadGen = 0;

    /** Per-file AbortControllers to cancel stale in-flight fetches. */
    abortControllers: Record<string, AbortController> = {};

    /** Tracker for WebSocket diff stats effect. */
    tracker = { hash: null as string | null, stagedPathsKey: '' };

    private _apis: DiffCacheApis;
    private _projectId: string;
    private _taskId: string;

    constructor(apis: DiffCacheApis, projectId: string, taskId: string) {
        this._apis = apis;
        this._projectId = projectId;
        this._taskId = taskId;
    }

    /** Revert a stage/unstage operation after server failure. */
    revertStageFile(path: string, stage: boolean) {
        if (this.state.files[path]) {
            this.state.files = { ...this.state.files, [path]: { ...this.state.files[path], staged: !stage } };
        }
    }

    /** Update a single file in the cache (e.g. expand truncated). */
    setFile(path: string, file: DiffFile) {
        this.state.files = { ...this.state.files, [path]: file };
    }

    /** Update metadata for an untracked file. */
    setUntrackedMeta(path: string, meta: UntrackedMeta) {
        this.state.untracked = { ...this.state.untracked, [path]: meta };
    }

    /** Serialize staged_paths for deterministic change detection. */
    static stagedPathsKey(paths: string[] | null | undefined): string {
        return paths ? [...paths].sort().join('\n') : '';
    }

    /** Update staged flags on every cached file to match the given staged_paths list. */
    applyStagedPaths(paths: string[] | null | undefined) {
        if (Object.keys(this.state.files).length === 0) return;
        const stagedSet = paths ? new Set(paths) : new Set();
        const updated: Record<string, DiffFile> = {};
        for (const [path, entry] of Object.entries(this.state.files)) {
            updated[path] = { ...entry, staged: stagedSet.has(path) };
        }
        this.state.files = updated;
    }

    /** Process a diff stats update from WebSocket. Called by component's $effect. */
    processStats(stat: DiffStats | undefined, diffLoading: boolean) {
        const currentHash = stat?.hash || null;
        const stagedPaths = stat?.staged_paths;
        const stagedKey = DiffCache.stagedPathsKey(stagedPaths);
        const hashChanged = currentHash !== this.tracker.hash;
        const stagedChanged = stagedKey !== this.tracker.stagedPathsKey;

        // Skip processing while loadDiff() is in progress.
        if (diffLoading) return;

        if (hashChanged) {
            this.tracker.hash = currentHash;
            const fileHashes = stat?.file_hashes;
            const removedFiles = stat?.removed_files || [];

            // Remove deleted files from cache
            if (removedFiles.length > 0) {
                const newDiff = { ...this.state.files };
                const newUntracked = { ...this.state.untracked };
                const newHashes = { ...this.fileHashes };
                for (const path of removedFiles) {
                    delete newDiff[path];
                    delete newUntracked[path];
                    delete newHashes[path];
                }
                this.state.files = newDiff;
                this.state.untracked = newUntracked;
                this.fileHashes = newHashes;
            }

            // Empty diff — clear cache
            if (!currentHash) {
                this.state.files = {};
                this.state.untracked = {};
                this.fileHashes = {};
                this.initialLoaded = false;
                this.tracker.stagedPathsKey = '';
                return;
            }

            // Update staged status
            this.applyStagedPaths(stagedPaths);
            this.tracker.stagedPathsKey = stagedKey;

            if (!fileHashes || Object.keys(fileHashes).length === 0) {
                return;
            }

            // Delta fetch for changed files
            if (fileHashes && !diffLoading) {
                const oldHashes = { ...this.fileHashes };
                this.fileHashes = { ...this.fileHashes, ...fileHashes };
                const stagedSet = stagedPaths ? new Set(stagedPaths) : null;
                this.fetchDeltaFiles(fileHashes, oldHashes, stagedSet);
            }
        } else if (stagedChanged) {
            this.applyStagedPaths(stagedPaths);
            this.tracker.stagedPathsKey = stagedKey;
        }
    }

    /** Update cache from a full diff manifest response. */
    updateDiff(data: DiffApiResponse) {
        if (!data.files || Object.keys(data.files).length === 0) {
            this.state.files = {};
            this.state.untracked = {};
            this.fileHashes = {};
            return;
        }
        const ignoreSwp = (p: string) => !p.split('/').pop()?.match(/\..*\.swp$/);
        const newCache: Record<string, DiffFile> = {};
        const newUntracked: Record<string, UntrackedMeta> = {};

        for (const [path, entry] of Object.entries(data.files)) {
            if (!ignoreSwp(path) && entry.type === 'untracked') continue;
            if (entry.type === 'tracked') {
                const existing = this.state.files[path];
                newCache[path] = {
                    path,
                    rows: existing?.rows || [],
                    added: entry.added ?? 0,
                    removed: entry.removed ?? 0,
                    fileSize: entry.file_size,
                    isBinary: entry.is_binary,
                    truncated: !!entry.truncated,
                    origAdded: entry.origAdded,
                    origRemoved: entry.origRemoved,
                    staged: entry.staged,
                };
            } else if (entry.type === 'untracked') {
                const existing = this.state.files[path];
                newUntracked[path] = {
                    fileSize: entry.file_size,
                    isBinary: entry.is_binary,
                    truncated: !!entry.truncated,
                    origAdded: entry.origAdded ?? entry.line_count,
                    loading: false,
                };
                newCache[path] = {
                    path,
                    rows: existing?.rows || [],
                    added: entry.line_count ?? 0,
                    removed: 0,
                    staged: false,
                };
            }
        }
        this.state.files = newCache;
        this.state.untracked = newUntracked;
        const newHashes: Record<string, string> = {};
        for (const [path, entry] of Object.entries(data.files)) {
            newHashes[path] = entry.hash;
        }
        this.fileHashes = newHashes;
    }

    /** Reload the full diff. Aborts in-flight delta fetches and resets state. */
    async loadDiff(onResetState: () => void) {
        // Abort any in-flight delta fetches
        for (const controller of Object.values(this.abortControllers)) {
            controller.abort();
        }
        this.abortControllers = {};
        // Clear caches
        this.state.files = {};
        this.state.untracked = {};
        this.fileHashes = {};
        this.initialLoaded = false;
        // Bump generation
        this.loadGen++;
        // Reset tracker
        this.tracker.hash = null;
        this.tracker.stagedPathsKey = '';
        // Call external callback to reset overlay/comparison state
        onResetState();
        this.state.loading = true;

        try {
            const result = await this._apis.getTaskDiff(this._projectId, this._taskId);
            const oldHashes = { ...this.fileHashes };
            this.updateDiff(result);
            // For manifest format, fetch individual files
            if (result.files) {
                await this.fetchChangedFiles(result.files, oldHashes);
            }
        } catch (e) {
            this.state.files = {};
            this.state.untracked = {};
            this.fileHashes = {};
        } finally {
            this.state.loading = false;
            this.initialLoaded = true;
        }
    }

    /** Fetch individual files from the manifest. */
    private async fetchChangedFiles(
        manifest: Record<string, { hash: string; type: string; added?: number; removed?: number; line_count?: number; file_size?: number; is_binary?: boolean; staged?: boolean }>,
        oldHashes?: Record<string, string>,
    ) {
        const updatedDiff: Record<string, DiffFile> = {};
        const updatedUntracked: Record<string, boolean> = {};
        const fetches: Promise<void>[] = [];

        for (const [path, entry] of Object.entries(manifest)) {
            if (entry.type === 'untracked' && (entry.line_count ?? 0) === 0) continue;

            const cachedEntry = this.state.files[path];
            const stagedMatch = cachedEntry?.staged === (entry.staged ?? false);
            if (oldHashes && oldHashes[path] === entry.hash && cachedEntry?.rows && cachedEntry.rows.length > 0 && stagedMatch) continue;

            // Untracked → tracked with same content
            if (entry.type === 'tracked' && this.state.untracked[path] && cachedEntry?.rows && cachedEntry.rows.length > 0) {
                const cachedRows = cachedEntry.rows;
                if (cachedRows.length === (entry.added ?? 0) && cachedRows.every((r: DiffRow) => r.variant === 'added')) {
                    updatedDiff[path] = { ...cachedEntry, added: entry.added ?? 0, removed: entry.removed ?? 0, truncated: !!entry.truncated, origAdded: entry.origAdded, origRemoved: entry.origRemoved, staged: entry.staged };
                    const newUntracked = { ...this.state.untracked };
                    delete newUntracked[path];
                    this.state.untracked = newUntracked;
                    continue;
                }
            }

            fetches.push((async () => {
                try {
                    const data = await this._apis.getDiffFile(this._projectId, this._taskId, path);
                    if (data.rows) {
                        updatedDiff[path] = { path, rows: this._apis.enrichRows(data.rows), added: entry.added ?? 0, removed: entry.removed ?? 0, truncated: data.truncated, origAdded: data.origAdded, origRemoved: data.origRemoved, staged: entry.staged };
                    } else if (data.lines) {
                        updatedDiff[path] = { path, rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })), added: data.lines.length, removed: 0, truncated: !!data.truncated, origAdded: data.origAdded, staged: entry.staged ?? false };
                        updatedUntracked[path] = true;
                    } else if (data.isBinary || data.fileSize) {
                        updatedDiff[path] = { path, rows: [], added: entry.added ?? 0, removed: entry.removed ?? 0, fileSize: data.fileSize ?? entry.file_size, isBinary: data.isBinary ?? entry.is_binary, truncated: false, staged: entry.staged };
                        if (entry.type === 'untracked') updatedUntracked[path] = true;
                    }
                } catch (e) {
                    console.error(`[diff] Failed to fetch file ${path}:`, e);
                }
            })());
        }

        await Promise.all(fetches);
        this.state.files = { ...this.state.files, ...updatedDiff };
        if (Object.keys(updatedUntracked).length > 0) {
            const newUntracked = { ...this.state.untracked };
            for (const path of Object.keys(updatedUntracked)) {
                newUntracked[path] = { ...(this.state.untracked[path] || {}), loading: false };
            }
            this.state.untracked = newUntracked;
        }
    }

    /** Fetch changed files directly (no manifest). Used for delta updates. */
    async fetchDeltaFiles(changedHashes: Record<string, string>, oldHashes?: Record<string, string>, stagedSet?: Set<string>) {
        const gen = this.loadGen;
        const updatedDiff: Record<string, DiffFile> = {};
        const updatedUntracked: Record<string, UntrackedMeta> = {};
        const fetches: Promise<void>[] = [];

        for (const path of Object.keys(changedHashes)) {
            if (oldHashes && oldHashes[path] === changedHashes[path] && this.state.files[path]?.rows && this.state.files[path].rows.length > 0) continue;

            // Untracked → tracked transition
            if (this.state.untracked[path] && this.state.files[path]?.rows && this.state.files[path].rows.length > 0) {
                const cachedRows = this.state.files[path].rows;
                if (cachedRows.every((r: DiffRow) => r.variant === 'added')) {
                    const existing = this.state.files[path];
                    updatedDiff[path] = { ...existing, staged: existing?.staged };
                    const newUntracked = { ...this.state.untracked };
                    delete newUntracked[path];
                    this.state.untracked = newUntracked;
                    continue;
                }
            }

            if (this.abortControllers[path]) {
                this.abortControllers[path].abort();
            }
            const controller = new AbortController();
            this.abortControllers[path] = controller;

            fetches.push((async () => {
                try {
                    const existing = this.state.files[path];
                    const data = await this._apis.getDiffFile(this._projectId, this._taskId, path, { signal: controller.signal });
                    const isStaged = stagedSet?.has(path) ?? existing?.staged ?? false;
                    if (data.rows) {
                        const rows = this._apis.enrichRows(data.rows);
                        updatedDiff[path] = { path, rows, added: rows.filter((r: DiffRow) => r.variant === 'added').length, removed: rows.filter((r: DiffRow) => r.variant === 'removed').length, truncated: data.truncated, origAdded: data.origAdded, origRemoved: data.origRemoved, staged: isStaged };
                    } else if (data.lines) {
                        updatedDiff[path] = { path, rows: data.lines.map((line: string, i: number) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })), added: data.lines.length, removed: 0, truncated: !!data.truncated, origAdded: data.origAdded, staged: isStaged };
                        updatedUntracked[path] = { ...(this.state.untracked[path] || {}), loading: false };
                    } else if (data.isBinary || data.fileSize) {
                        updatedDiff[path] = { path, rows: [], added: existing?.added ?? 0, removed: existing?.removed ?? 0, fileSize: data.fileSize, isBinary: data.isBinary, truncated: false, staged: isStaged };
                        if (this.state.untracked[path]) {
                            updatedUntracked[path] = { ...(this.state.untracked[path] || {}), loading: false, fileSize: data.fileSize };
                        }
                    }
                } catch (e) {
                    if (e instanceof DOMException && e.name === 'AbortError') return;
                    console.error(`[diff] Failed to fetch file ${path}:`, e);
                }
            })());
        }

        await Promise.all(fetches);
        if (gen !== this.loadGen) return;

        // Merge, preserving newer staged values
        const currentStaged = new Map(Object.entries(this.state.files).map(([p, f]) => [p, f.staged]));
        this.state.files = { ...this.state.files, ...updatedDiff };
        for (const path of Object.keys(updatedDiff)) {
            const staged = currentStaged.get(path);
            if (staged !== undefined) {
                this.state.files[path] = { ...this.state.files[path], staged };
            }
        }
        for (const path of Object.keys(changedHashes)) {
            delete this.abortControllers[path];
        }
        if (Object.keys(updatedUntracked).length > 0) {
            this.state.untracked = { ...this.state.untracked, ...updatedUntracked };
        }
    }

    /** Stage or unstage a single file. Optimistic update + server call. */
    async stageFile(path: string, stage: boolean, onRevert: (path: string, stage: boolean) => void) {
        if (this.state.files[path]) {
            this.state.files = { ...this.state.files, [path]: { ...this.state.files[path], staged: stage } };
        }
        try {
            await this._apis.stageFile(this._projectId, this._taskId, path, stage);
        } catch (e) {
            onRevert(path, stage);
            throw e;
        }
    }

    /** Stage all unstaged tracked and untracked files. */
    async stageAll() {
        const paths = [
            ...Object.keys(this.state.files).filter(p => !this.state.files[p].staged && !this.state.untracked[p]),
            ...Object.keys(this.state.untracked),
        ];
        for (const path of paths) {
            try {
                await this._apis.stageFile(this._projectId, this._taskId, path, true);
            } catch { /* skip */ }
        }
    }

    /** Unstage all staged files. */
    async unstageAll() {
        const paths = Object.keys(this.state.files).filter(p => this.state.files[p].staged);
        for (const path of paths) {
            try {
                await this._apis.stageFile(this._projectId, this._taskId, path, false);
            } catch { /* skip */ }
        }
    }

    /** Full reset — called when task changes. */
    reset() {
        this.tracker.hash = null;
        this.tracker.stagedPathsKey = '';
        this.state.files = {};
        this.state.untracked = {};
        this.fileHashes = {};
        this.initialLoaded = false;
    }
}
