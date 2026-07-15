import type { Project, Board, Config, Task, Provider } from '$lib/types';
import { loadCollapsedTasks, saveCollapsedTasks, loadAppSetting, saveAppSetting } from '$lib/storage';

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
    editPlanMode: boolean;
    editAutoMerge: boolean;
    editSubtasksAutoMerge: boolean;
    taskHasParent: boolean;
    targetBranch: string;
    isAgentSelected: () => boolean;
}

export class AppStore {
    public projects = $state<Project[]>([]);
    public selectedProject = $state<Project | null>(null);
    public board = $state<Board | null>(null);
    public selectedTask = $state<Task | null>(null);
    public config = $state<Config | null>(null);

    // Active port mappings for the project-level terminal (broadcast via WebSocket)
    public projectTerminalPorts = $state<Array<{ host_port: number; container_port: number }>>([]);

    // Sidebar
    public sidebarCollapsed = $state(false);

    // Edit Task Modal
    public showEditTask = $state(false);
    public editTaskData = $state<EditTaskData | null>(null);

    // PWA
    public pwaInstallPrompt = $state<Event | null>(null);
    public pwaInstalled = $state(false);

    constructor() {
        // Load sidebar collapsed state from IndexedDB on init
        loadAppSetting('kanban-sidebar-collapsed', false).then((v: unknown) => {
            this.sidebarCollapsed = v === true || v === 'true';
        }).catch(() => {});

        // Persist sidebar collapsed state whenever it changes
        $effect(() => {
            const v = this.sidebarCollapsed;
            saveAppSetting('kanban-sidebar-collapsed', v).catch(() => {});
        });
    }
}

// ─── Expanded Tasks Helpers ───

/**
 * Save the current collapsed state to IndexedDB.
 * Collapsed = tasks with children that are NOT in the expanded set.
 */
export async function persistCollapsedTasks(expanded: Set<string>, parentTaskIds: Set<string>): Promise<void> {
    const collapsed = new Set<string>();
    for (const id of parentTaskIds) {
        if (!expanded.has(id)) {
            collapsed.add(id);
        }
    }
    await saveCollapsedTasks(collapsed);
}

/**
 * Clean stale entries from IndexedDB (task IDs that no longer exist).
 */
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

/**
 * Initialize expandedTasks from IndexedDB.
 * Returns the set of expanded task IDs (all parent IDs minus collapsed).
 */
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
