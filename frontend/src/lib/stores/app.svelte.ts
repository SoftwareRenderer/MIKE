import type { Project, Board, Config, Task, Provider } from '$lib/types';
import { loadCollapsedTasks, saveCollapsedTasks, loadAppSetting, saveAppSetting } from '$lib/storage';

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

// ─── Domain State ───

export interface EditTaskData {
    taskId: string;
    projectId: string;
    taskTitle: string;
    taskPrompt: string;
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
    isAgentSelected: () => boolean;
}

export class AppStore {
    public projects = $state<Project[]>([]);
    public selectedProject = $state<Project | null>(null);
    public board = $state<Board | null>(null);
    public selectedTask = $state<Task | null>(null);
    public config = $state<Config | null>(null);

    // Active port mappings for project-level terminal
    public projectTerminalPorts = $state<Array<{ host_port: number; container_port: number }>>([]);

    // Sidebar
    public sidebarCollapsed = $state(false);

    // Edit task modal
    public showEditTask = $state(false);
    public editTaskData = $state<EditTaskData | null>(null);

    // PWA
    public pwaInstallPrompt = $state<Event | null>(null);
    public pwaInstalled = $state(false);

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

/** Clean stale entries from IndexedDB. */
export async function cleanupCollapsedTasks(allTaskIds: Set<string>): Promise<Set<string>> {
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
    return collapsed;
}

/** Init expanded task IDs from IndexedDB. */
export async function initExpandedTasks(parentTaskIds: Set<string>): Promise<Set<string>> {
    const collapsed = await loadCollapsedTasks();
    // Clean stale entries
    for (const id of collapsed) {
        if (!parentTaskIds.has(id)) {
            collapsed.delete(id);
        }
    }
    await saveCollapsedTasks(collapsed);
    // Expanded = all parent IDs minus collapsed
    const expanded = new Set<string>();
    for (const id of parentTaskIds) {
        if (!collapsed.has(id)) {
            expanded.add(id);
        }
    }
    return expanded;
}
