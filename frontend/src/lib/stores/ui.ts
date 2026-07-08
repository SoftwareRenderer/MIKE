import { writable } from 'svelte/store';

// ─── UI Visibility State ───

export const showCreateTask = writable(false);
export const creatingChildOf = writable<string | null>(null);
export const showConfig = writable(false);
export const showTerminal = writable(false);
export const showStats = writable(false);
export const showCardDetail = writable(false);
export const showSandboxConfig = writable(false);
