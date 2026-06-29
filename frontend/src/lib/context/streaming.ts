import { getContext, setContext } from 'svelte';
import { writable } from 'svelte/store';
import type { Writable } from 'svelte/store';

const STREAMING_TASK_IDS_KEY = Symbol('streamingTaskIds');

/**
 * Create a streaming task IDs store for a component tree.
 * Call this in App.svelte or a parent component.
 */
export function createStreamingContext(): Writable<Set<string>> {
    const store = writable<Set<string>>(new Set());
    setContext(STREAMING_TASK_IDS_KEY, store);
    return store;
}

/**
 * Get the streaming task IDs store from context.
 * Call this in child components (Card, CardDetail, TaskChat, etc.).
 */
export function getStreamingContext(): Writable<Set<string>> {
    return getContext(STREAMING_TASK_IDS_KEY);
}
