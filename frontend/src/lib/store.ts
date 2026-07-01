import { writable } from 'svelte/store';
import type { Project, Board, Config, Task, StreamEvent } from './types';
import { loadCollapsedTasks, saveCollapsedTasks } from './storage';

export const projects = writable<Project[]>([]);
export const selectedProject = writable<Project | null>(null);
export const sidebarCollapsed = writable(false, () => {
    // Initialize from localStorage
    const stored = localStorage.getItem('kanban-sidebar-collapsed');
    if (stored === 'true') {
        sidebarCollapsed.set(true);
    }
    // Subscribe to save on change
    return () => {};
});

// Persist sidebar collapsed state whenever it changes
sidebarCollapsed.subscribe(v => {
    localStorage.setItem('kanban-sidebar-collapsed', String(v));
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

/**
 * expandedTasks store — tracks which task cards are expanded in the board view.
 * Subtasks are expanded by default. Collapsed state is persisted in localStorage.
 * Use `toggleExpandedTask(id, allTaskIds)` to toggle and auto-clean stale entries.
 */

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