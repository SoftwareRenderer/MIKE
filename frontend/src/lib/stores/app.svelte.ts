import type { Project, Board, Config, Task, Provider } from '$lib/types';
import { saveCollapsedTasks, pruneCollapsedTasks, loadAppSetting, saveAppSetting } from '$lib/storage';
import { getConfig } from '$lib/api/config';
import { isFromCache } from '$lib/api/cache';

// ─── Board Helpers ───

/** Replace a task in the board. No-op if board is null or task not found. */
export function updateTaskInBoard(appStore: AppStore, taskId: string, updated: Task): void {
    const board = appStore.board;
    if (!board) return;
    const idx = board.tasks.findIndex(t => t.id === taskId);
    if (idx === -1) return;
    const newTasks = [...board.tasks];
    newTasks[idx] = updated;
    appStore.board = { ...board, tasks: newTasks };
}

/** Remove a task from the board. No-op if board is null or task not found. */
export function removeTaskFromBoard(appStore: AppStore, taskId: string): void {
    const board = appStore.board;
    if (!board) return;
    appStore.board = { ...board, tasks: board.tasks.filter(t => t.id !== taskId) };
}

/** Add a task to the board. No-op when no board is on screen: an optimistic card belongs to the
 * board this browser is looking at, and a card with no board under it would be a list nothing owns. */
export function appendTaskToBoard(appStore: AppStore, task: Task): void {
    const board = appStore.board;
    if (!board) return;
    appStore.board = { ...board, tasks: [...(board.tasks || []), task] };
}

/**
 * Land the answers of one queue flush on the board: an optimistic id becomes the task the server
 * created, and a real id takes the newer state a later queued action produced. Tasks the flush did
 * not answer keep what this browser holds.
 */
export function applyResolvedTasksToBoard(appStore: AppStore, resolved: Map<string, Task>): void {
    const board = appStore.board;
    if (!board) return;
    const tasks = (board.tasks || []).map(t => {
        const created = resolved.get(t.id);
        if (!created) return t;
        return resolved.get(created.id) || created;
    });
    appStore.board = { ...board, tasks };
}

// ─── Domain State ───

export interface EditTaskData {
    taskId: string;
    projectId: string;
    taskTitle: string;
    editProviders: Provider[];
    editModel: string;
    editUrl: string;
    editReasoningEffort: string;
    editOperatingMode: string;
    editAutoMerge: boolean;
    editSubtasksAutoMerge: boolean;
    taskHasParent: boolean;
    targetBranch: string;
    baseRef: string;
}

export class AppStore {
    public projects = $state<Project[]>([]);
    public selectedProject = $state<Project | null>(null);
    public board = $state<Board | null>(null);
    public selectedTask = $state<Task | null>(null);
    public config = $state<Config | null>(null);

    // Active port mappings for project-level terminal
    public projectTerminalPorts = $state<Array<{ host_port: number; container_port: number }>>([]);

    /** The task list on screen. Derived from the board, so the board a panel reads and the list the
     * board draws are the same value; there is no second stored task list. */
    get tasks(): Task[] {
        return this.board?.tasks || [];
    }

    // Sidebar
    public sidebarCollapsed = $state(false);

    // Edit task modal
    public showEditTask = $state(false);
    public editTaskData = $state<EditTaskData | null>(null);

    // PWA
    public pwaInstallPrompt = $state<Event | null>(null);
    public pwaInstalled = $state(false);

    // One in-flight config refresh — see refreshConfig(). Not reactive state: nothing
    // renders it, and making it $state would give every reader a dependency on a
    // bookkeeping field.
    private configRefresh: Promise<void> | null = null;

    constructor() {
        // Load sidebar state from IndexedDB
        loadAppSetting('kanban-sidebar-collapsed', false).then((v: unknown) => {
            this.sidebarCollapsed = v === true || v === 'true';
        }).catch(() => {});
    }

    /** Persist sidebar state to IndexedDB. Call from a component $effect. */
    persistSidebarCollapsed(): void {
        saveAppSetting('kanban-sidebar-collapsed', this.sidebarCollapsed).catch(() => {});
    }

    /**
     * Re-read the server config so providers the server derives per request — an agent
     * extension IS a provider (`agent://<name>`) from the moment it is installed and
     * enabled — reach every provider list in the UI without a page reload.
     *
     * Best-effort: a failed fetch leaves the config in hand untouched (the reconnect
     * refresh retries), and a copy served from the offline cache is never applied over a
     * live one — that copy predates the very change this call exists to pick up.
     *
     * Concurrent calls share one request: installing or toggling extensions broadcasts an
     * extension_change per operation, and each would otherwise fetch.
     */
    refreshConfig(): Promise<void> {
        this.configRefresh ??= (async () => {
            try {
                const c = await getConfig();
                if (!isFromCache(c) || !this.config) this.config = c;
            } catch {
                // Offline with nothing cached — keep showing the config we already have.
            } finally {
                this.configRefresh = null;
            }
        })();
        return this.configRefresh;
    }
}

// ─── Expanded Tasks Helpers ───

/** Save collapsed state to IndexedDB. */
export async function persistCollapsedTasks(expanded: Set<string>, parentTaskIds: Set<string>): Promise<void> {
    const collapsed = new Set<string>();
    for (const id of parentTaskIds) {
        if (!expanded.has(id)) {
            collapsed.add(id);
        }
    }
    await saveCollapsedTasks(collapsed);
}

/** Init expanded task IDs from IndexedDB. */
export async function initExpandedTasks(parentTaskIds: Set<string>): Promise<Set<string>> {
    const collapsed = await pruneCollapsedTasks(parentTaskIds);
    // Expanded = all parent IDs minus collapsed
    const expanded = new Set<string>();
    for (const id of parentTaskIds) {
        if (!collapsed.has(id)) {
            expanded.add(id);
        }
    }
    return expanded;
}
