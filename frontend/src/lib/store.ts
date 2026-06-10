import { writable } from 'svelte/store';
import type { Project, Board, Config, Task } from './types';

export const projects = writable<Project[]>([]);
export const selectedProject = writable<Project | null>(null);
export const sidebarCollapsed = writable(false);
export const board = writable<Board | null>(null);
export const selectedTask = writable<Task | null>(null);
export const config = writable<Config | null>(null);
export const showCreateTask = writable(false);
export const showConfig = writable(false);
export const showTerminal = writable(false);
export const showStats = writable(false);
export const showCardDetail = writable(false);

// Task IDs that are actively streaming tokens (frontend-only, real-time)
export const streamingTaskIds = writable<Set<string>>(new Set());

// Toast notification — empty string means no toast visible
export const toast = writable<{ message: string; type: 'error' | 'success' } | null>(null);

/** Show a toast message that auto-dismisses after `duration` ms. */
export function showToast(message: string, type: 'error' | 'success' = 'error', duration = 4000) {
    toast.set({ message, type });
    setTimeout(() => toast.set(null), duration);
}