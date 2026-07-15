// Legacy compat — re-export from reactive store
// Prefer importing from $lib/stores (appStore, uiStore, dialogStore)
import { appStore } from './index';

export const projects = {
    get: () => appStore.projects,
    set: (v: typeof appStore.projects) => { appStore.projects = v; },
    update: (fn: (v: typeof appStore.projects) => typeof appStore.projects) => { appStore.projects = fn(appStore.projects); },
    subscribe: (fn: (v: typeof appStore.projects) => void) => { fn(appStore.projects); return () => {}; },
} as any;

export const selectedProject = {
    get: () => appStore.selectedProject,
    set: (v: typeof appStore.selectedProject) => { appStore.selectedProject = v; },
    subscribe: (fn: (v: typeof appStore.selectedProject) => void) => { fn(appStore.selectedProject); return () => {}; },
} as any;

export const board = {
    get: () => appStore.board,
    set: (v: typeof appStore.board) => { appStore.board = v; },
    update: (fn: (v: typeof appStore.board) => typeof appStore.board) => { appStore.board = fn(appStore.board); },
    subscribe: (fn: (v: typeof appStore.board) => void) => { fn(appStore.board); return () => {}; },
} as any;

export const selectedTask = {
    get: () => appStore.selectedTask,
    set: (v: typeof appStore.selectedTask) => { appStore.selectedTask = v; },
    subscribe: (fn: (v: typeof appStore.selectedTask) => void) => { fn(appStore.selectedTask); return () => {}; },
} as any;

export const config = {
    get: () => appStore.config,
    set: (v: typeof appStore.config) => { appStore.config = v; },
    subscribe: (fn: (v: typeof appStore.config) => void) => { fn(appStore.config); return () => {}; },
} as any;

export const projectTerminalPorts = {
    get: () => appStore.projectTerminalPorts,
    set: (v: typeof appStore.projectTerminalPorts) => { appStore.projectTerminalPorts = v; },
    subscribe: (fn: (v: typeof appStore.projectTerminalPorts) => void) => { fn(appStore.projectTerminalPorts); return () => {}; },
} as any;

export const sidebarCollapsed = {
    get: () => appStore.sidebarCollapsed,
    set: (v: typeof appStore.sidebarCollapsed) => { appStore.sidebarCollapsed = v; },
    subscribe: (fn: (v: typeof appStore.sidebarCollapsed) => void) => { fn(appStore.sidebarCollapsed); return () => {}; },
} as any;

export const showEditTask = {
    get: () => appStore.showEditTask,
    set: (v: typeof appStore.showEditTask) => { appStore.showEditTask = v; },
    subscribe: (fn: (v: typeof appStore.showEditTask) => void) => { fn(appStore.showEditTask); return () => {}; },
} as any;

export const editTaskData = {
    get: () => appStore.editTaskData,
    set: (v: typeof appStore.editTaskData) => { appStore.editTaskData = v; },
    subscribe: (fn: (v: typeof appStore.editTaskData) => void) => { fn(appStore.editTaskData); return () => {}; },
} as any;

export const pwaInstallPrompt = {
    get: () => appStore.pwaInstallPrompt,
    set: (v: typeof appStore.pwaInstallPrompt) => { appStore.pwaInstallPrompt = v; },
    subscribe: (fn: (v: typeof appStore.pwaInstallPrompt) => void) => { fn(appStore.pwaInstallPrompt); return () => {}; },
} as any;

export const pwaInstalled = {
    get: () => appStore.pwaInstalled,
    set: (v: typeof appStore.pwaInstalled) => { appStore.pwaInstalled = v; },
    subscribe: (fn: (v: typeof appStore.pwaInstalled) => void) => { fn(appStore.pwaInstalled); return () => {}; },
} as any;

// Re-export helper functions
export { persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks, type EditTaskData } from './app.svelte';
