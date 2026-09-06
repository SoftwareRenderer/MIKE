// =============================================================================
// Unified client-side storage module
// =============================================================================
// All client-side storage for the Kanban app goes through this module.
// Components should NEVER access IndexedDB directly.
//
// Single IndexedDB database: `kanban-storage` (v2)
//
// Object stores:
//   collapsed-tasks      — Single key 'ids' → string[] of collapsed parent task IDs
//   collapsed-sections   — Single key 'ids' → string[] of collapsed board section IDs
//   drafts               — key: taskId → string (draft chat text)
//   editor-drafts        — key: file path → string (draft file content)
//   editor-history       — key: file path → EditorHistory (undo/redo history)
//   editor-tabs          — key: projectId → EditorTabState (open files, active file, pinned files)
//   unsaved-files        — key: projectId → string[] (unsaved file paths)
//   offline-queue        — Single key 'actions' → QueuedAction[]
//   explorer-layout      — Single key 'layout' → ExplorerLayout
//   collapsed-diff-files — Single key 'ids' → string[] of collapsed diff file paths
//   app-settings         — key: setting key → any (terminal height, panel ratios, sidebar, etc.)
//   attachments          — key: taskId → DraftAttachment[] (draft image attachments)
//   board-cache          — key: cache type → BoardCacheEntry (projects, board, config mirror,
//                           plus per-task offline content: messages:<pid>:<tid>,
//                           message-full:<pid>:<tid>:<mid>, diff:<pid>:<tid>)
//
// Garbage collection:
//   Call `gc(allTaskIds, allProjectPaths?)` after the board is loaded. It removes all stale
//   entries for task IDs that no longer exist (deleted tasks, etc.).
//   If `allProjectPaths` is provided, also cleans stale branch editor drafts.
//
// Sign-out:
//   `clearAll()` erases every store and the pre-paint `localStorage` hints. `gc()`
//   is not a substitute for it: it prunes only entries whose task no longer exists.
// =============================================================================

// `$lib/i18n` is cycle-free from here: the import surface is `index.ts` →
// `runtime.svelte.ts` → `en.ts`, and none of those reach this module. The pack
// *cache* (`i18n/cache.ts`) does import `$lib/storage`, and it is deliberately not
// part of what `import { t }` pulls in — routing this import through `cache.ts` or
// `boot.ts` would close the loop.
import { t } from '$lib/i18n';

// ─── Constants ───────────────────────────────────────────────────────────────

const DB_NAME = 'kanban-storage';
const DB_VERSION = 3;

// Object store names
const STORE_COLLAPSED_TASKS = 'collapsed-tasks';
const STORE_COLLAPSED_SECTIONS = 'collapsed-sections';
const STORE_DRAFTS = 'drafts';
const STORE_EDITOR_DRAFTS = 'editor-drafts';
const STORE_EDITOR_HISTORY = 'editor-history';
const STORE_UNSAVED_FILES = 'unsaved-files';
const STORE_OFFLINE_QUEUE = 'offline-queue';
const STORE_EXPLORER_LAYOUT = 'explorer-layout';
const STORE_COLLAPSED_DIFF_FILES = 'collapsed-diff-files';
const STORE_EDITOR_TABS = 'editor-tabs';
const STORE_APP_SETTINGS = 'app-settings';
const STORE_ATTACHMENTS = 'attachments';
const STORE_BOARD_CACHE = 'board-cache';

// Fixed keys for single-value stores
const KEY_IDS = 'ids';
const KEY_ACTIONS = 'actions';
const KEY_LAYOUT = 'layout';

const ALL_STORES = [
    STORE_COLLAPSED_TASKS,
    STORE_COLLAPSED_SECTIONS,
    STORE_DRAFTS,
    STORE_EDITOR_DRAFTS,
    STORE_EDITOR_HISTORY,
    STORE_EDITOR_TABS,
    STORE_UNSAVED_FILES,
    STORE_OFFLINE_QUEUE,
    STORE_EXPLORER_LAYOUT,
    STORE_COLLAPSED_DIFF_FILES,
    STORE_APP_SETTINGS,
    STORE_ATTACHMENTS,
    STORE_BOARD_CACHE,
];

// ─── Proxy-to-plain conversion ──────────────────────────────────────────────
// Svelte 5's $state() creates reactive proxies that IndexedDB cannot clone
// (structured clone algorithm rejects Proxy objects). Convert to plain values.

/** Convert a Svelte 5 $state proxy to a plain value for IndexedDB storage. */
function toPlain<T>(value: T): T {
    if (value === null || value === undefined) return value;
    const type = typeof value;
    if (type !== 'object') return value; // primitives (string, number, boolean)
    if (Array.isArray(value)) return value.map(toPlain) as unknown as T;
    if (value instanceof Set) return [...value].map(toPlain) as unknown as T;
    if (value instanceof Date) return value;
    // Plain object — recurse into values
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, toPlain(v)])) as T;
}

// ─── Database access ─────────────────────────────────────────────────────────

let dbPromise: Promise<IDBDatabase> | null = null;

/** Open (or create) the kanban-storage IndexedDB database. */
function openDB(): Promise<IDBDatabase> {
    if (dbPromise) return dbPromise;

    dbPromise = new Promise((resolve, reject) => {
        if (typeof indexedDB === 'undefined') {
            reject(new Error(t('storage.unavailable')));
            return;
        }

        const request = indexedDB.open(DB_NAME, DB_VERSION);

        request.onupgradeneeded = (event: any) => {
            const db = event.target.result as IDBDatabase;
            const oldVersion = event.oldVersion || 0;
            for (const store of ALL_STORES) {
                if (!db.objectStoreNames.contains(store)) {
                    db.createObjectStore(store);
                }
            }
            // Schema migrations
            if (oldVersion < 2) {
                // v2: add board-cache store
                if (!db.objectStoreNames.contains(STORE_BOARD_CACHE)) {
                    db.createObjectStore(STORE_BOARD_CACHE);
                }
            }
            if (oldVersion < 3) {
                // v3: add editor-tabs store
                if (!db.objectStoreNames.contains(STORE_EDITOR_TABS)) {
                    db.createObjectStore(STORE_EDITOR_TABS);
                }
            }
        };

        request.onsuccess = () => {
            resolve(request.result);
        };

        request.onerror = () => {
            reject(request.error);
            dbPromise = null; // Allow retry
        };
    });

    return dbPromise;
}

// ─── Generic IndexedDB helpers ───────────────────────────────────────────────

/** Get a raw value from an object store by key. Returns `null` if not found or on error. */
async function dbGetRaw(store: string, key: string): Promise<any> {
    try {
        const db = await openDB();
        return new Promise((resolve) => {
            const tx = db.transaction(store, 'readonly');
            const s = tx.objectStore(store);
            const req = s.get(key);
            req.onsuccess = () => resolve(req.result ?? null);
            req.onerror = () => resolve(null);
        });
    } catch {
        return null;
    }
}

/** Get a value typed as T. When `validate` is provided, returns `null` if the stored value
    doesn't pass validation (e.g., corrupted data). Without validator, returns raw value or null. */
async function dbGet<T>(store: string, key: string, validate?: (v: any) => v is T): Promise<T | null> {
    const raw = await dbGetRaw(store, key);
    if (raw === null) return null;
    if (validate && !validate(raw)) return null;
    return raw as T;
}

/** Type guard: value is a non-null array. */
function isArray(v: any): v is any[] {
    return Array.isArray(v);
}

/** Put a value into an object store by key. Converts Svelte 5 $state proxies to plain values. */
async function dbPut<T>(store: string, key: string, value: T): Promise<void> {
    try {
        const db = await openDB();
        return new Promise((resolve, reject) => {
            const tx = db.transaction(store, 'readwrite');
            tx.objectStore(store).put(toPlain(value), key);
            tx.oncomplete = () => resolve();
            tx.onerror = () => reject(tx.error);
        });
    } catch {
        // IndexedDB unavailable — silently ignore
    }
}

/** Delete a key from an object store. */
async function dbDelete(store: string, key: string): Promise<void> {
    try {
        const db = await openDB();
        return new Promise((resolve) => {
            const tx = db.transaction(store, 'readwrite');
            tx.objectStore(store).delete(key);
            tx.oncomplete = () => resolve();
        });
    } catch { /* ignore */ }
}

/** Get all keys from an object store. */
async function dbGetAllKeys(store: string): Promise<string[]> {
    try {
        const db = await openDB();
        return new Promise((resolve) => {
            const tx = db.transaction(store, 'readonly');
            const req = tx.objectStore(store).getAllKeys();
            req.onsuccess = () => resolve(req.result as string[]);
            req.onerror = () => resolve([]);
        });
    } catch {
        return [];
    }
}

/** Delete multiple keys from an object store. */
async function dbDeleteKeys(store: string, keys: string[]): Promise<void> {
    try {
        const db = await openDB();
        return new Promise((resolve) => {
            const tx = db.transaction(store, 'readwrite');
            const s = tx.objectStore(store);
            for (const key of keys) {
                s.delete(key);
            }
            tx.oncomplete = () => resolve();
        });
    } catch { /* ignore */ }
}

// ─── Draft text (IndexedDB: drafts store) ────────────────────────────────────

/** Get saved draft text for a task. Returns `null` if no draft exists. */
export async function getDraftText(taskId: string): Promise<string | null> {
    return dbGet<string>(STORE_DRAFTS, taskId);
}

/** Save draft text for a task. Pass empty string to clear. */
export async function setDraftText(taskId: string, text: string): Promise<void> {
    if (!text) {
        return dbDelete(STORE_DRAFTS, taskId);
    }
    return dbPut(STORE_DRAFTS, taskId, text);
}

/** Remove draft text for a task. */
export async function clearDraftText(taskId: string): Promise<void> {
    return dbDelete(STORE_DRAFTS, taskId);
}

// ─── Branch editor drafts (IndexedDB: editor-drafts store) ──────────────────

/** Get saved draft content for a file. Returns `null` if no draft exists. */
export async function getEditorDraft(path: string): Promise<string | null> {
    return dbGet<string>(STORE_EDITOR_DRAFTS, path);
}

/** Save draft content for a file. */
export async function setEditorDraft(path: string, text: string): Promise<void> {
    return dbPut(STORE_EDITOR_DRAFTS, path, text);
}

/** Remove draft content for a file. */
export async function clearEditorDraft(path: string): Promise<void> {
    return dbDelete(STORE_EDITOR_DRAFTS, path);
}

// ─── Editor undo/redo history (IndexedDB: editor-history store) ──────────────
// Stores a base snapshot + deltas (from/to/insert) so undo/redo survives refreshes.
// Max 1000 delta entries per file. FIFO. Entries older than 72h are discarded.

export interface EditorDelta {
    from: number;
    to: number;
    insert: string;
}

export interface EditorHistory {
    // Timestamp when this history was created (ms)
    ts: number;
    // Base document content (first snapshot)
    base: string;
    // Ordered list of deltas applied on top of base (and each other)
    deltas: EditorDelta[];
}

const MAX_HISTORY_DELTAS = 1000;
const HISTORY_MAX_AGE_MS = 72 * 60 * 60 * 1000; // 72 hours

/** Load persisted undo/redo history for a file. Returns `null` if none exists or expired. */
export async function loadEditorHistory(path: string): Promise<EditorHistory | null> {
    const history = await dbGet<EditorHistory>(STORE_EDITOR_HISTORY, path);
    if (history && Date.now() - history.ts > HISTORY_MAX_AGE_MS) {
        await clearEditorHistory(path);
        return null;
    }
    return history;
}

/** Save undo/redo history for a file. Appends a new delta. */
export async function saveEditorHistory(path: string, base: string, deltas: EditorDelta[]): Promise<void> {
    const trimmed = deltas.slice(-MAX_HISTORY_DELTAS);
    return dbPut(STORE_EDITOR_HISTORY, path, { ts: Date.now(), base, deltas: trimmed });
}

/** Clear undo/redo history for a file. */
export async function clearEditorHistory(path: string): Promise<void> {
    return dbDelete(STORE_EDITOR_HISTORY, path);
}

// ─── Unsaved files (IndexedDB: unsaved-files store) ─────────────────────────

/** Load unsaved file paths for a project. */
export async function loadUnsavedFiles(projectId: string): Promise<string[]> {
    const result = await dbGet<string[]>(STORE_UNSAVED_FILES, projectId, isArray);
    return result ?? [];
}

/** Save unsaved file paths for a project. */
export async function saveUnsavedFiles(projectId: string, paths: string[]): Promise<void> {
    return dbPut(STORE_UNSAVED_FILES, projectId, paths);
}

// ─── Explorer layout (IndexedDB: explorer-layout store) ─────────────────────

export interface ExplorerLayout {
    leftWidth: number;
    rightWidth: number;
    splitRatio: number;
}

const DEFAULT_EXPLORER_LAYOUT: ExplorerLayout = {
    leftWidth: 250,
    rightWidth: 180,
    splitRatio: 0.5,
};

/** Load saved explorer column layout. Returns defaults if none saved. */
export async function loadExplorerLayout(): Promise<ExplorerLayout> {
    const saved = await dbGet<Partial<ExplorerLayout>>(STORE_EXPLORER_LAYOUT, KEY_LAYOUT);
    if (saved) {
        return {
            leftWidth: saved.leftWidth ?? DEFAULT_EXPLORER_LAYOUT.leftWidth,
            rightWidth: saved.rightWidth ?? DEFAULT_EXPLORER_LAYOUT.rightWidth,
            splitRatio: saved.splitRatio ?? DEFAULT_EXPLORER_LAYOUT.splitRatio,
        };
    }
    return { ...DEFAULT_EXPLORER_LAYOUT };
}

/** Save explorer column layout. */
export async function saveExplorerLayout(layout: ExplorerLayout): Promise<void> {
    return dbPut(STORE_EXPLORER_LAYOUT, KEY_LAYOUT, layout);
}

// ─── Collapsed tasks (IndexedDB: collapsed-tasks store) ─────────────────────

/** Load collapsed task IDs. */
export async function loadCollapsedTasks(): Promise<Set<string>> {
    const ids = await dbGet<string[]>(STORE_COLLAPSED_TASKS, KEY_IDS, isArray);
    return ids ? new Set(ids) : new Set();
}

/** Save collapsed task IDs. */
export async function saveCollapsedTasks(ids: Set<string>): Promise<void> {
    return dbPut(STORE_COLLAPSED_TASKS, KEY_IDS, [...ids]);
}

// ─── Collapsed board sections (IndexedDB: collapsed-sections store) ─────────

/** Load collapsed board section IDs. */
export async function loadCollapsedSections(): Promise<Set<string>> {
    const ids = await dbGet<string[]>(STORE_COLLAPSED_SECTIONS, KEY_IDS, isArray);
    return ids ? new Set(ids) : new Set();
}

/** Save collapsed board section IDs. */
export async function saveCollapsedSections(ids: Set<string>): Promise<void> {
    return dbPut(STORE_COLLAPSED_SECTIONS, KEY_IDS, [...ids]);
}

// ─── Offline queue (IndexedDB: offline-queue store) ─────────────────────────

export interface QueuedAction {
    id: string;
    type: string;
    params: Record<string, any>;
    timestamp: number;
}

/** Load offline queue. */
export async function loadQueue(): Promise<QueuedAction[]> {
    const result = await dbGet<QueuedAction[]>(STORE_OFFLINE_QUEUE, KEY_ACTIONS, isArray);
    return result ?? [];
}

/** Save offline queue. */
export async function saveQueue(actions: QueuedAction[]): Promise<void> {
    return dbPut(STORE_OFFLINE_QUEUE, KEY_ACTIONS, actions);
}

// ─── Draft attachments (IndexedDB: attachments store) ───────────────────────

/** Serializable draft attachment (no File objects — only base64 data). */
export interface DraftAttachment {
    mimeType: string;
    data: string;   // base64-encoded image data
}

/** Save draft attachments for a task. Pass empty array to clear. */
export async function saveDraftAttachments(taskId: string, attachments: DraftAttachment[]): Promise<void> {
    if (attachments.length === 0) {
        return dbDelete(STORE_ATTACHMENTS, taskId);
    }
    return dbPut(STORE_ATTACHMENTS, taskId, attachments);
}

/** Load draft attachments for a task. Returns empty array if none exist. */
export async function loadDraftAttachments(taskId: string): Promise<DraftAttachment[]> {
    const result = await dbGet<DraftAttachment[]>(STORE_ATTACHMENTS, taskId, isArray);
    return result ?? [];
}

/** Remove draft attachments for a task. */
export async function clearDraftAttachments(taskId: string): Promise<void> {
    return dbDelete(STORE_ATTACHMENTS, taskId);
}

// ─── App settings (IndexedDB: app-settings store) ───────────────────────────
// Generic key-value store for component-level settings (terminal height, panel ratios, sidebar, etc.)

/** Load an app setting by key. Returns `defaultValue` if not found. */
export async function loadAppSetting<T>(key: string, defaultValue: T): Promise<T> {
    const value = await dbGet<T>(STORE_APP_SETTINGS, key);
    return value ?? defaultValue;
}

/** Save an app setting by key. */
export async function saveAppSetting<T>(key: string, value: T): Promise<void> {
    return dbPut(STORE_APP_SETTINGS, key, value);
}

/**
 * Remove an app setting by key.
 *
 * Deleting is not the same as saving `null`: a cache that must stop being
 * readable (a language pack that is no longer enabled) has to leave no entry
 * behind, or the next boot can read what the user already turned off.
 */
export async function deleteAppSetting(key: string): Promise<void> {
    return dbDelete(STORE_APP_SETTINGS, key);
}

// ─── Collapsed diff files (IndexedDB: collapsed-diff-files store) ────────────

/** Load collapsed diff file paths. */
export async function loadCollapsedDiffFiles(): Promise<Set<string>> {
    const ids = await dbGet<string[]>(STORE_COLLAPSED_DIFF_FILES, KEY_IDS, isArray);
    return ids ? new Set(ids) : new Set();
}

/** Save collapsed diff file paths. */
export async function saveCollapsedDiffFiles(ids: Set<string>): Promise<void> {
    return dbPut(STORE_COLLAPSED_DIFF_FILES, KEY_IDS, [...ids]);
}

// ─── Garbage collection ─────────────────────────────────────────────────────

/**
 * Remove all stale entries for task IDs that no longer exist.
 * Call this after the board is loaded to clean up data for deleted tasks.
 *
 * @param allTaskIds  Set of currently existing task IDs
 * @param allProjectPaths  Optional set of valid project paths; if provided, also cleans stale editor drafts
 * @param allProjectIds  Optional set of valid project IDs; if provided, also cleans stale board cache
 * @param projectId  Optional current project ID; if provided, also cleans stale message/diff caches for that project
 */
export async function gc(allTaskIds: Set<string>, allProjectPaths?: Set<string>, allProjectIds?: Set<string>, projectId?: string): Promise<void> {
    // 1. Clean collapsed tasks
    await cleanCollapsedTasks(allTaskIds);

    // 2. Clean draft texts
    await cleanDraftTexts(allTaskIds);

    // 3. Clean offline queue
    await cleanQueue(allTaskIds);

    // 4. Clean draft attachments
    await cleanDraftAttachments(allTaskIds);

    // 5. Clean editor drafts (if project paths provided)
    if (allProjectPaths) {
        await cleanEditorDraftsByProject(allProjectPaths);
    }

    // 6. Clean editor history (if project paths provided)
    if (allProjectPaths) {
        await cleanEditorHistoryByProject(allProjectPaths);
    }

    // 7. Clean board cache (if project IDs provided)
    if (allProjectIds) {
        await cleanBoardCacheByProject(allProjectIds);
    }

    // 8. Clean editor tabs (if project IDs provided)
    if (allProjectIds) {
        await cleanEditorTabsByProject(allProjectIds);
    }

    // 9. Clean message/diff caches for the current project (if project ID provided)
    if (projectId) {
        await cleanTaskContentCache(allTaskIds, projectId);
    }
}

/** Remove collapsed task entries for deleted tasks. */
async function cleanCollapsedTasks(allTaskIds: Set<string>): Promise<void> {
    const collapsed = await loadCollapsedTasks();
    let changed = false;
    for (const id of collapsed) {
        if (!allTaskIds.has(id)) {
            collapsed.delete(id);
            changed = true;
        }
    }
    if (changed) {
        await saveCollapsedTasks(collapsed);
    }
}

/** Remove draft text entries for deleted tasks. */
async function cleanDraftTexts(allTaskIds: Set<string>): Promise<void> {
    const keys = await dbGetAllKeys(STORE_DRAFTS);
    const toDelete = keys.filter(taskId => !allTaskIds.has(taskId));
    if (toDelete.length > 0) {
        await dbDeleteKeys(STORE_DRAFTS, toDelete);
    }
}

/** Remove offline queue entries that reference deleted tasks. */
async function cleanQueue(allTaskIds: Set<string>): Promise<void> {
    const actions = await loadQueue();
    const filtered = actions.filter(action => {
        const taskId = action.params?.taskId;
        return !taskId || allTaskIds.has(taskId);
    });
    if (filtered.length !== actions.length) {
        await saveQueue(filtered);
    }
}

/** Remove draft attachments for deleted tasks. */
async function cleanDraftAttachments(allTaskIds: Set<string>): Promise<void> {
    const keys = await dbGetAllKeys(STORE_ATTACHMENTS);
    const toDelete = keys.filter(key => !allTaskIds.has(key));
    if (toDelete.length > 0) {
        await dbDeleteKeys(STORE_ATTACHMENTS, toDelete);
    }
}

/** Remove editor drafts for files in projects that no longer exist. */
async function cleanEditorDraftsByProject(allProjectPaths: Set<string>): Promise<void> {
    const keys = await dbGetAllKeys(STORE_EDITOR_DRAFTS);
    const toDelete = keys.filter(path => {
        return !allProjectPaths.has(path) &&
            ![...allProjectPaths].some(p => path.startsWith(p + '/'));
    });
    if (toDelete.length > 0) {
        await dbDeleteKeys(STORE_EDITOR_DRAFTS, toDelete);
    }
}

/** Remove editor history for files in projects that no longer exist. */
async function cleanEditorHistoryByProject(allProjectPaths: Set<string>): Promise<void> {
    const keys = await dbGetAllKeys(STORE_EDITOR_HISTORY);
    const toDelete = keys.filter(path => {
        return !allProjectPaths.has(path) &&
            ![...allProjectPaths].some(p => path.startsWith(p + '/'));
    });
    if (toDelete.length > 0) {
        await dbDeleteKeys(STORE_EDITOR_HISTORY, toDelete);
    }
}

/** Remove editor tab entries for projects that no longer exist. */
async function cleanEditorTabsByProject(allProjectIds: Set<string>): Promise<void> {
    const keys = await dbGetAllKeys(STORE_EDITOR_TABS);
    const toDelete = keys.filter(key => !allProjectIds.has(key));
    if (toDelete.length > 0) {
        await dbDeleteKeys(STORE_EDITOR_TABS, toDelete);
    }
}

/** Remove board cache entries for projects that no longer exist. */
async function cleanBoardCacheByProject(allProjectIds: Set<string>): Promise<void> {
    const keys = await dbGetAllKeys(STORE_BOARD_CACHE);
    const toDelete = keys.filter(key => {
        if (key === 'projects' || key === 'config') return false;
        if (key.startsWith('board:')) {
            const projectId = key.slice(6);
            return !allProjectIds.has(projectId);
        }
        return false;
    });
    if (toDelete.length > 0) {
        await dbDeleteKeys(STORE_BOARD_CACHE, toDelete);
    }
}

/** Remove stale message/diff cache entries for a specific project.
 * Only touches `messages:`/`message-full:`/`diff:` keys whose project part matches `projectId`,
 * and only removes those whose task ID no longer exists in the current project's board.
 * Other projects' caches are left untouched. */
async function cleanTaskContentCache(allTaskIds: Set<string>, projectId: string): Promise<void> {
    const keys = await dbGetAllKeys(STORE_BOARD_CACHE);
    const toDelete = keys.filter(key => {
        const parts = key.split(':');
        // 'diff:<pid>:<tid>' (current worktree diff), 'diff:<pid>:<tid>:<headSHA>'
        // (done-task branch-tip diff), and 'done-stats:<pid>:<tid>' all carry the
        // task id at parts[2] and are swept when the task disappears.
        if (parts[0] !== 'messages' && parts[0] !== 'message-full' && parts[0] !== 'diff' && parts[0] !== 'done-stats') return false;
        if (parts[1] !== projectId) return false; // only current project
        const taskId = parts[2];
        return !allTaskIds.has(taskId);
    });
    if (toDelete.length > 0) {
        await dbDeleteKeys(STORE_BOARD_CACHE, toDelete);
    }
}

// ─── Board cache (IndexedDB: board-cache store) ─────────────────────────────
// Lightweight mirror of server state for offline rendering.
// Stores projects list, per-project board state, and app config.
// Also stores per-task content caches for offline viewing:
//   'messages:<projectId>:<taskId>',
//   'message-full:<projectId>:<taskId>:<messageId>',
//   'diff:<projectId>:<taskId>'

export interface BoardCacheEntry {
    data: unknown;
    cachedAt: number;  // epoch ms
}

/** Cache key for a project's board. */
export function boardCacheKey(projectId: string): string {
    return `board:${projectId}`;
}

/** Cache key for a task's message list. */
export function messagesCacheKey(projectId: string, taskId: string): string {
    return `messages:${projectId}:${taskId}`;
}

/** Cache key for a single message's full content. */
export function messageFullCacheKey(projectId: string, taskId: string, messageId: string): string {
    return `message-full:${projectId}:${taskId}:${messageId}`;
}

/** Cache key for a task's current worktree diff. */
export function diffCacheKey(projectId: string, taskId: string): string {
    return `diff:${projectId}:${taskId}`;
}

/** Cache key for a done task's immutable branch-tip diff (full REST response). */
export function doneDiffCacheKey(projectId: string, taskId: string, headRef: string): string {
    return `diff:${projectId}:${taskId}:${headRef}`;
}

/** Cache key for a done task's cached diff stats + the updated_at they were observed at. */
export function doneStatsCacheKey(projectId: string, taskId: string): string {
    return `done-stats:${projectId}:${taskId}`;
}

/** Save a value to the board cache. */
export async function setBoardCache(key: string, value: unknown): Promise<void> {
    return dbPut(STORE_BOARD_CACHE, key, { data: value, cachedAt: Date.now() });
}

/** Load a value from the board cache. Returns `null` if not found. */
export async function getBoardCache<T>(key: string): Promise<T | null> {
    const entry = await dbGet<BoardCacheEntry>(STORE_BOARD_CACHE, key);
    return entry ? (entry.data as T) : null;
}

/** Remove a value from the board cache. */
export async function removeBoardCache(key: string): Promise<void> {
    return dbDelete(STORE_BOARD_CACHE, key);
}

// ─── Editor tabs (IndexedDB: editor-tabs store) ──────────────────────────────
// Persists open file tabs, active file, and pinned files per project.

export interface EditorTabFile {
    path: string;
    targetLine: number;
}

export interface EditorTabState {
    openFiles: EditorTabFile[];
    activeFilePath: string;
    pinnedFiles: string[];
    scrollTops?: Record<string, number>;
    fileListScroll?: number;
}

/** Load persisted editor tab state for a project. Returns `null` if none exists. */
export async function loadEditorTabs(projectId: string): Promise<EditorTabState | null> {
    return dbGet<EditorTabState>(STORE_EDITOR_TABS, projectId);
}

/** Save editor tab state for a project. */
export async function saveEditorTabs(projectId: string, state: EditorTabState): Promise<void> {
    return dbPut(STORE_EDITOR_TABS, projectId, state);
}

// ─── Sign-out ────────────────────────────────────────────────────────────────

/**
 * Erase every store and the `kanban-*` localStorage hints, so a signed-out browser
 * keeps nothing: each store holds this session's work product — messages, diffs,
 * unsaved files, drafts, queued writes.
 *
 * The hints go by prefix, not by name, because this module must not import their
 * owners: `i18n/cache.ts` imports this one, and the loop would close.
 */
export async function clearAll(): Promise<void> {
    for (const store of ALL_STORES) {
        const keys = await dbGetAllKeys(store);
        if (keys.length > 0) await dbDeleteKeys(store, keys);
    }
    try {
        for (const key of Object.keys(localStorage)) {
            if (key.startsWith('kanban-')) localStorage.removeItem(key);
        }
    } catch { /* storage unavailable */ }
}
