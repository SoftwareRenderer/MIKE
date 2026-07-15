import { AppStore } from './app.svelte';
import { UiStore } from './ui.svelte';
import { DialogStore } from './dialogs.svelte';

// Singleton instances
export const appStore = new AppStore();
export const uiStore = new UiStore();
export const dialogStore = new DialogStore();

// Re-export types and helper functions
export type { EditTaskData } from './app.svelte';
export { persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks, updateTaskInBoard, removeTaskFromBoard } from './app.svelte';
