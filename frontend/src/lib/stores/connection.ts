import { writable } from 'svelte/store';

/**
 * Reactive WebSocket state for the app-level stream (/api/stream).
 *
 * Set by App.svelte. Consumers (e.g. TaskChat) use the false→true
 * transition to refresh data that changed while the connection was down.
 */
export const wsConnection = writable<boolean>(false);
