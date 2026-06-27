// =============================================================================
// Unified client-side storage module
// =============================================================================
// All localStorage / IndexedDB access for the Kanban app goes through this module.
// Components should NEVER access localStorage or IndexedDB directly.
//
// Storage keys:
//   localStorage:
//     kanban-collapsed-tasks   — Set<string> of collapsed parent task IDs
//     kanban:draft:<taskId>    — Draft chat text per task
//     kanban:editor-draft:<path> — Draft file content per file path (branch editor)
//     kanban-offline-queue     — Queued write actions (managed by api.ts)
//     kanban:terminal:height   — Terminal panel resize height
//     kanban:detail:boardRatio — CardDetail board/chat split ratio
//     kanban:detail:chatRatio  — CardDetail chat/stream split ratio
//     kanban:explorer:layout   — Explorer column widths and split ratio
//     diff_collapsed_files     — Set<string> of collapsed diff file paths
//   IndexedDB (kanban-drafts):
//     attachments store        — Draft image attachments per task (key: taskId)
//
// Garbage collection:
//   Call `gc(allTaskIds, allProjectPaths?)` after the board is loaded. It removes all stale
//   entries for task IDs that no longer exist (deleted tasks, etc.).
//   If `allProjectPaths` is provided, also cleans stale branch editor drafts.
// =============================================================================

// ─── Constants ───────────────────────────────────────────────────────────────

const COLLAPSED_TASKS_KEY = 'kanban-collapsed-tasks';
const QUEUE_KEY = 'kanban-offline-queue';
const DRAFT_KEY_PREFIX = 'kanban:draft:';
const EDITOR_DRAFT_KEY_PREFIX = 'kanban:editor-draft:';
const EXPLORER_LAYOUT_KEY = 'kanban:explorer:layout';

const DB_NAME = 'kanban-drafts';
const DB_VERSION = 1;
const ATTACHMENTS_STORE = 'attachments';

// ─── Draft text (localStorage) ───────────────────────────────────────────────

/** Get saved draft text for a task. Returns `null` if no draft exists. */
export function getDraftText(taskId: string): string | null {
    try {
        if (typeof localStorage === 'undefined') return null;
        return localStorage.getItem(DRAFT_KEY_PREFIX + taskId);
    } catch {
        return null;
    }
}

/** Save draft text for a task. Pass empty string to clear. */
export function setDraftText(taskId: string, text: string): void {
    try {
        if (typeof localStorage === 'undefined') return;
        if (text === null) {
            localStorage.removeItem(DRAFT_KEY_PREFIX + taskId);
        } else if (text === '') {
            localStorage.removeItem(DRAFT_KEY_PREFIX + taskId);
        } else {
            localStorage.setItem(DRAFT_KEY_PREFIX + taskId, text);
        }
    } catch {
        // localStorage full or unavailable — silently ignore
    }
}

/** Remove draft text for a task. */
export function clearDraftText(taskId: string): void {
    try {
        if (typeof localStorage === 'undefined') return;
        localStorage.removeItem(DRAFT_KEY_PREFIX + taskId);
    } catch { /* ignore */ }
}

// ─── Branch editor drafts (localStorage) ────────────────────────────────────

/** Get saved draft content for a file. Returns `null` if no draft exists. */
export function getEditorDraft(path: string): string | null {
    try {
        if (typeof localStorage === 'undefined') return null;
        return localStorage.getItem(EDITOR_DRAFT_KEY_PREFIX + path);
    } catch {
        return null;
    }
}

/** Save draft content for a file. */
export function setEditorDraft(path: string, text: string): void {
    try {
        if (typeof localStorage === 'undefined') return;
        localStorage.setItem(EDITOR_DRAFT_KEY_PREFIX + path, text);
    } catch {
        // localStorage full or unavailable — silently ignore
    }
}

/** Remove draft content for a file. */
export function clearEditorDraft(path: string): void {
    try {
        if (typeof localStorage === 'undefined') return;
        localStorage.removeItem(EDITOR_DRAFT_KEY_PREFIX + path);
    } catch { /* ignore */ }
}

// ─── Explorer layout (localStorage) ──────────────────────────────────────────

interface ExplorerLayout {
    leftWidth: number;
    rightWidth: number;
    splitRatio: number;
}

const DEFAULT_EXPLORER_LAYOUT: ExplorerLayout = {
    leftWidth: 250,
    rightWidth: 180,
    splitRatio: 0.5,
};

/** Load saved explorer column layout from localStorage. Returns defaults if none saved. */
export function loadExplorerLayout(): ExplorerLayout {
    try {
        if (typeof localStorage === 'undefined') return { ...DEFAULT_EXPLORER_LAYOUT };
        const raw = localStorage.getItem(EXPLORER_LAYOUT_KEY);
        if (raw) {
            const saved = JSON.parse(raw) as Partial<ExplorerLayout>;
            return {
                leftWidth: saved.leftWidth ?? DEFAULT_EXPLORER_LAYOUT.leftWidth,
                rightWidth: saved.rightWidth ?? DEFAULT_EXPLORER_LAYOUT.rightWidth,
                splitRatio: saved.splitRatio ?? DEFAULT_EXPLORER_LAYOUT.splitRatio,
            };
        }
    } catch { /* ignore */ }
    return { ...DEFAULT_EXPLORER_LAYOUT };
}

/** Save explorer column layout to localStorage. */
export function saveExplorerLayout(layout: ExplorerLayout): void {
    try {
        if (typeof localStorage === 'undefined') return;
        localStorage.setItem(EXPLORER_LAYOUT_KEY, JSON.stringify(layout));
    } catch { /* ignore */ }
}

/** Remove all stale editor draft entries for paths that no longer exist. */
export function gcEditorDrafts(allPaths: Set<string>): void {
    try {
        if (typeof localStorage === 'undefined') return;
        const prefix = EDITOR_DRAFT_KEY_PREFIX;
        const prefixLen = prefix.length;
        for (let i = 0; i < localStorage.length; i++) {
            const key = localStorage.key(i);
            if (key && key.startsWith(prefix)) {
                const path = key.slice(prefixLen);
                if (!allPaths.has(path)) {
                    localStorage.removeItem(key);
                }
            }
        }
    } catch { /* ignore */ }
}

// ─── Collapsed tasks (localStorage) ─────────────────────────────────────────

/** Load collapsed task IDs from localStorage. */
export function loadCollapsedTasks(): Set<string> {
    try {
        if (typeof localStorage === 'undefined') return new Set();
        const raw = localStorage.getItem(COLLAPSED_TASKS_KEY);
        if (raw) {
            const ids = JSON.parse(raw) as string[];
            return new Set(ids);
        }
    } catch { /* ignore */ }
    return new Set();
}

/** Save collapsed task IDs to localStorage. */
export function saveCollapsedTasks(ids: Set<string>): void {
    try {
        if (typeof localStorage === 'undefined') return;
        localStorage.setItem(COLLAPSED_TASKS_KEY, JSON.stringify([...ids]));
    } catch { /* ignore */ }
}

/** Remove collapsed state for a specific task. */
export function clearCollapsedTask(taskId: string): void {
    const collapsed = loadCollapsedTasks();
    collapsed.delete(taskId);
    saveCollapsedTasks(collapsed);
}

// ─── Offline queue (localStorage) ────────────────────────────────────────────

interface QueuedAction {
    id: string;
    type: string;
    params: Record<string, any>;
    timestamp: number;
}

/** Load offline queue from localStorage. */
export function loadQueue(): QueuedAction[] {
    try {
        if (typeof localStorage === 'undefined') return [];
        const raw = localStorage.getItem(QUEUE_KEY);
        if (raw) return JSON.parse(raw) as QueuedAction[];
    } catch { /* ignore */ }
    return [];
}

/** Save offline queue to localStorage. */
export function saveQueue(actions: QueuedAction[]): void {
    try {
        if (typeof localStorage === 'undefined') return;
        localStorage.setItem(QUEUE_KEY, JSON.stringify(actions));
    } catch { /* ignore */ }
}

// ─── Draft attachments (IndexedDB) ──────────────────────────────────────────

/** Serializable draft attachment (no File objects — only base64 data). */
export interface DraftAttachment {
    mimeType: string;
    data: string;   // base64-encoded image data
}

let dbPromise: Promise<IDBDatabase> | null = null;

/** Open (or create) the IndexedDB database for draft attachments. */
function openDraftDB(): Promise<IDBDatabase> {
    if (dbPromise) return dbPromise;

    dbPromise = new Promise((resolve, reject) => {
        if (typeof indexedDB === 'undefined') {
            reject(new Error('IndexedDB not available'));
            return;
        }

        const request = indexedDB.open(DB_NAME, DB_VERSION);

        request.onupgradeneeded = (event: any) => {
            const db = event.target.result as IDBDatabase;
            if (!db.objectStoreNames.contains(ATTACHMENTS_STORE)) {
                db.createObjectStore(ATTACHMENTS_STORE);
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

/** Save draft attachments for a task. Pass empty array to clear. */
export async function saveDraftAttachments(taskId: string, attachments: DraftAttachment[]): Promise<void> {
    try {
        const db = await openDraftDB();
        return new Promise((resolve, reject) => {
            const tx = db.transaction(ATTACHMENTS_STORE, 'readwrite');
            const store = tx.objectStore(ATTACHMENTS_STORE);
            if (attachments.length === 0) {
                store.delete(taskId);
            } else {
                store.put(attachments, taskId);
            }
            tx.oncomplete = () => resolve();
            tx.onerror = () => reject(tx.error);
        });
    } catch {
        // IndexedDB unavailable — silently ignore
    }
}

/** Load draft attachments for a task. Returns empty array if none exist. */
export async function loadDraftAttachments(taskId: string): Promise<DraftAttachment[]> {
    try {
        const db = await openDraftDB();
        return new Promise((resolve) => {
            const tx = db.transaction(ATTACHMENTS_STORE, 'readonly');
            const store = tx.objectStore(ATTACHMENTS_STORE);
            const request = store.get(taskId);
            request.onsuccess = () => resolve(request.result as DraftAttachment[] || []);
            tx.onerror = () => resolve([]);
        });
    } catch {
        return [];
    }
}

/** Remove draft attachments for a task. */
export async function clearDraftAttachments(taskId: string): Promise<void> {
    try {
        const db = await openDraftDB();
        return new Promise((resolve) => {
            const tx = db.transaction(ATTACHMENTS_STORE, 'readwrite');
            tx.objectStore(ATTACHMENTS_STORE).delete(taskId);
            tx.oncomplete = () => resolve();
        });
    } catch { /* ignore */ }
}

// ─── Garbage collection ─────────────────────────────────────────────────────

/**
 * Remove all stale entries for task IDs that no longer exist.
 * Call this after the board is loaded to clean up data for deleted tasks.
 *
 * @param allTaskIds  Set of currently existing task IDs
 * @param allProjectPaths  Optional set of valid project paths; if provided, also cleans stale editor drafts
 */
export async function gc(allTaskIds: Set<string>, allProjectPaths?: Set<string>): Promise<void> {
    // 1. Clean collapsed tasks
    cleanCollapsedTasks(allTaskIds);

    // 2. Clean draft texts
    cleanDraftTexts(allTaskIds);

    // 3. Clean offline queue
    cleanQueue(allTaskIds);

    // 4. Clean draft attachments (IndexedDB)
    await cleanDraftAttachments(allTaskIds);

    // 5. Clean editor drafts (if project paths provided)
    if (allProjectPaths) {
        cleanEditorDraftsByProject(allProjectPaths);
    }
}

/** Remove collapsed task entries for deleted tasks. */
function cleanCollapsedTasks(allTaskIds: Set<string>): void {
    const collapsed = loadCollapsedTasks();
    let changed = false;
    for (const id of collapsed) {
        if (!allTaskIds.has(id)) {
            collapsed.delete(id);
            changed = true;
        }
    }
    if (changed) {
        saveCollapsedTasks(collapsed);
    }
}

/** Remove draft text entries for deleted tasks. */
function cleanDraftTexts(allTaskIds: Set<string>): void {
    try {
        if (typeof localStorage === 'undefined') return;
        const prefix = DRAFT_KEY_PREFIX;
        const prefixLen = prefix.length;
        for (let i = 0; i < localStorage.length; i++) {
            const key = localStorage.key(i);
            if (key && key.startsWith(prefix)) {
                const taskId = key.slice(prefixLen);
                if (!allTaskIds.has(taskId)) {
                    localStorage.removeItem(key);
                }
            }
        }
    } catch { /* ignore */ }
}

/** Remove offline queue entries that reference deleted tasks. */
function cleanQueue(allTaskIds: Set<string>): void {
    const actions = loadQueue();
    const filtered = actions.filter(action => {
        const taskId = action.params?.taskId;
        return !taskId || allTaskIds.has(taskId);
    });
    if (filtered.length !== actions.length) {
        saveQueue(filtered);
    }
}

/** Remove draft attachments for deleted tasks (IndexedDB). */
async function cleanDraftAttachments(allTaskIds: Set<string>): Promise<void> {
    try {
        const db = await openDraftDB();
        return new Promise((resolve) => {
            const tx = db.transaction(ATTACHMENTS_STORE, 'readwrite');
            const store = tx.objectStore(ATTACHMENTS_STORE);
            const request = store.getAllKeys();
            request.onsuccess = () => {
                for (const key of request.result as string[]) {
                    if (!allTaskIds.has(key)) {
                        store.delete(key);
                    }
                }
            };
            tx.oncomplete = () => resolve();
            tx.onerror = () => resolve(); // best-effort
        });
    } catch { /* ignore */ }
}

/** Remove editor drafts for files in projects that no longer exist. */
function cleanEditorDraftsByProject(allProjectPaths: Set<string>): void {
    try {
        if (typeof localStorage === 'undefined') return;
        const prefix = EDITOR_DRAFT_KEY_PREFIX;
        const prefixLen = prefix.length;
        for (let i = 0; i < localStorage.length; i++) {
            const key = localStorage.key(i);
            if (key && key.startsWith(prefix)) {
                const path = key.slice(prefixLen);
                // Check if the file path belongs to any known project
                const belongsToProject = allProjectPaths.has(path) ||
                    [...allProjectPaths].some(p => path.startsWith(p + '/'));
                if (!belongsToProject) {
                    localStorage.removeItem(key);
                }
            }
        }
    } catch { /* ignore */ }
}
