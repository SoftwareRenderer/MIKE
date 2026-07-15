import { writable } from 'svelte/store';
import type { Project, Board, Config, Task, Provider, EditTaskData } from '$lib/types';
import { loadCollapsedTasks, saveCollapsedTasks, loadAppSetting, saveAppSetting } from '$lib/storage';

// ─── Domain State ───

export const projects = writable<Project[]>([]);
export const selectedProject = writable<Project | null>(null);
export const board = writable<Board | null>(null);
export const selectedTask = writable<Task | null>(null);
export const config = writable<Config | null>(null);

// Active port mappings for the project-level terminal (broadcast via WebSocket)
export const projectTerminalPorts = writable<Array<{ host_port: number; container_port: number }>>([]);

// ─── Sidebar ───

export const sidebarCollapsed = writable(false);

// Load sidebar collapsed state from IndexedDB on init
loadAppSetting('kanban-sidebar-collapsed', false).then(v => {
    sidebarCollapsed.set(v === true || v === 'true');
}).catch(() => {});

// Persist sidebar collapsed state whenever it changes
sidebarCollapsed.subscribe(v => {
    saveAppSetting('kanban-sidebar-collapsed', v).catch(() => {});
});

// ─── Edit Task Modal ───

export type { EditTaskData };

export const showEditTask = writable(false);
export const editTaskData = writable<EditTaskData | null>(null);

// ─── Expanded Tasks ───

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

// ─── PWA ───

export const pwaInstallPrompt = writable<Event | null>(null);
export const pwaInstalled = writable(false);
