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
export const showCardDetail = writable(false);