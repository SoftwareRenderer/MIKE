import { AppStore } from './app.svelte';
import { UiStore } from './ui.svelte';
import { DialogStore } from './dialogs.svelte';

// Singleton instances
export const appStore = new AppStore();
export const uiStore = new UiStore();
export const dialogStore = new DialogStore();

export { updateTaskInBoard, removeTaskFromBoard, appendTaskToBoard, applyResolvedTasksToBoard } from './app.svelte';
export type { DialogVariant } from './dialogs.svelte';
