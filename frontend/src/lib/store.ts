import { writable } from 'svelte/store';
import type { Project, Board, Config, Task, StreamEvent } from './types';

export const projects = writable<Project[]>([]);
export const selectedProject = writable<Project | null>(null);
export const sidebarCollapsed = writable(false);
export const board = writable<Board | null>(null);
export const selectedTask = writable<Task | null>(null);
export const config = writable<Config | null>(null);
export const showCreateTask = writable(false);

/** Task event from the app stream, dispatched to TaskStream via this store. */
export const taskEvent = writable<{ task_id: string; type: string; data: any } | null>(null);
export const creatingChildOf = writable<string | null>(null);
export const showConfig = writable(false);
export const showTerminal = writable(false);
export const showStats = writable(false);
export const showCardDetail = writable(false);
export const showSandboxConfig = writable(false);

/** Tracks commit queue status per project: projectID → { taskID, status } */
export const commitQueueStatus = writable<Record<string, { taskID: string; status: 'queued' | 'processing' }>>({});
const COLLAPSED_TASKS_KEY = 'kanban-collapsed-tasks';

/** Load collapsed task IDs from localStorage. */
function loadCollapsedTasks(): Set<string> {
    try {
        const raw = localStorage.getItem(COLLAPSED_TASKS_KEY);
        if (raw) {
            const ids = JSON.parse(raw) as string[];
            return new Set(ids);
        }
    } catch {
        // ignore
    }
    return new Set();
}

/** Save collapsed task IDs to localStorage. */
function saveCollapsedTasks(ids: Set<string>): void {
    try {
        localStorage.setItem(COLLAPSED_TASKS_KEY, JSON.stringify([...ids]));
    } catch {
        // ignore
    }
}

/**
 * expandedTasks store — tracks which task cards are expanded in the board view.
 * Subtasks are expanded by default. Collapsed state is persisted in localStorage.
 * Use `toggleExpandedTask(id, allTaskIds)` to toggle and auto-clean stale entries.
 */
export const expandedTasks = writable<Set<string>>(new Set());

/**
 * Toggle a task's expanded state. Cleans up stale entries from localStorage.
 * @param taskId  The task ID to toggle.
 * @param allTaskIds  Set of all currently existing task IDs (for cleanup).
 */
export function toggleExpandedTask(taskId: string, allTaskIds?: Set<string>) {
    expandedTasks.update(expanded => {
        const next = new Set(expanded);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        // Note: we store collapsed tasks in localStorage, but the hold expanded in memory.
        // The collapsed set = all parent tasks that are NOT in the expanded set.
        // We persist the collapsed set derived from the expanded set.
        return next;
    });
}

/**
 * Save the current collapsed state to localStorage.
 * Collapsed = tasks with children that are NOT in the expanded set.
 */
export function persistCollapsedTasks(expanded: Set<string>, parentTaskIds: Set<string>) {
    const collapsed = new Set<string>();
    for (const id of parentTaskIds) {
        if (!expanded.has(id)) {
            collapsed.add(id);
        }
    }
    saveCollapsedTasks(collapsed);
}

/**
 * Clean stale entries from localStorage (task IDs that no longer exist).
 */
export function cleanupCollapsedTasks(allTaskIds: Set<string>) {
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
    return collapsed;
}

/**
 * Initialize expandedTasks from localStorage.
 * Returns the set of expanded task IDs (all parent IDs minus collapsed).
 */
export function initExpandedTasks(parentTaskIds: Set<string>): Set<string> {
    const collapsed = loadCollapsedTasks();
    // Clean stale entries
    for (const id of collapsed) {
        if (!parentTaskIds.has(id)) {
            collapsed.delete(id);
        }
    }
    saveCollapsedTasks(collapsed);
    // Expanded = all parent IDs minus collapsed
    const expanded = new Set<string>();
    for (const id of parentTaskIds) {
        if (!collapsed.has(id)) {
            expanded.add(id);
        }
    }
    return expanded;
}

// Task IDs that are actively streaming tokens (frontend-only, real-time)
export const streamingTaskIds = writable<Set<string>>(new Set());

// Diff stats per task ID (pushed via WebSocket)
export const taskDiffStats = writable<Record<string, { hash: string; files: number; additions: number; deletions: number }>>({});
export const taskDiff = writable<Record<string, { diff: string; untracked: Array<{ path: string; diff: string; file_size?: number }> }>>({});

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