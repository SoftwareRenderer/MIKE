import { writable } from 'svelte/store';
import type { Project, Board, Config, Task, StreamEvent } from './types';
import { loadCollapsedTasks, saveCollapsedTasks, loadAppSetting, saveAppSetting } from './storage';

export const projects = writable<Project[]>([]);
export const selectedProject = writable<Project | null>(null);
export const sidebarCollapsed = writable(false);

// Load sidebar collapsed state from IndexedDB on init
loadAppSetting('kanban-sidebar-collapsed', false).then(v => {
    sidebarCollapsed.set(v === true || v === 'true');
}).catch(() => {});

// Persist sidebar collapsed state whenever it changes
sidebarCollapsed.subscribe(v => {
    saveAppSetting('kanban-sidebar-collapsed', v).catch(() => {});
});
export const board = writable<Board | null>(null);
export const selectedTask = writable<Task | null>(null);
export const config = writable<Config | null>(null);
export const showCreateTask = writable(false);

export const creatingChildOf = writable<string | null>(null);
export const showConfig = writable(false);
export const showTerminal = writable(false);
export const showStats = writable(false);
export const showCardDetail = writable(false);
export const showSandboxConfig = writable(false);

// Edit task modal state
export interface EditTaskData {
    taskId: string;
    projectId: string;
    taskTitle: string;
    taskPrompt: string;
    editProviders: Array<any>; // Provider[]
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

export const showEditTask = writable(false);
export const editTaskData = writable<EditTaskData | null>(null);

/**
 * expandedTasks store — tracks which task cards are expanded in the board view.
 * Subtasks are expanded by default. Collapsed state is persisted in IndexedDB.
 * Use `toggleExpandedTask(id, allTaskIds)` to toggle and auto-clean stale entries.
 */

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

// Toast notification — empty string means no toast visible
export const toast = writable<{ message: string; type: 'error' | 'success' } | null>(null);

/** Show a toast message that auto-dismisses after `duration` ms. */
export function showToast(message: string, type: 'error' | 'success' = 'error', duration = 4000) {
    toast.set({ message, type });
    setTimeout(() => toast.set(null), duration);
}

// Confirmation dialog state
export interface ConfirmOptions {
    title: string;
    message: string;
    confirmText?: string;
    cancelText?: string;
    variant?: 'default' | 'destructive';
}

export const confirmation = writable<ConfirmOptions | null>(null);

/**
 * Show a confirmation dialog and return a Promise that resolves to true (confirmed) or false (cancelled).
 */
export function showConfirm(options: ConfirmOptions): Promise<boolean> {
    return new Promise((resolve) => {
        confirmation.set({
            title: options.title,
            message: options.message,
            confirmText: options.confirmText ?? 'Confirm',
            cancelText: options.cancelText ?? 'Cancel',
            variant: options.variant ?? 'destructive',
            _resolve: resolve,
        } as ConfirmOptions & { _resolve?: (value: boolean) => void });
    });
}