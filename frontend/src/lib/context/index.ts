import { getContext, setContext } from 'svelte';
import { writable } from 'svelte/store';
import type { Writable } from 'svelte/store';
import type { DiffStatsEntry, DiffEntry, CommitQueueEntry, TaskEventData } from '$lib/types';

// =============================================================================
// Streaming Task IDs Context
// =============================================================================
// Tracks which tasks are actively streaming tokens (frontend-only, real-time).
// =============================================================================

const STREAMING_TASK_IDS_KEY = Symbol('streamingTaskIds');

export function createStreamingContext(): Writable<Set<string>> {
    const store = writable<Set<string>>(new Set());
    setContext(STREAMING_TASK_IDS_KEY, store);
    return store;
}

export function getStreamingContext(): Writable<Set<string>> {
    return getContext(STREAMING_TASK_IDS_KEY);
}

// =============================================================================
// Task Diff Stats Context
// =============================================================================
// Diff stats per task ID (pushed via WebSocket).
// =============================================================================

const TASK_DIFF_STATS_KEY = Symbol('taskDiffStats');
const TASK_DIFF_KEY = Symbol('taskDiff');

export type { DiffStatsEntry, DiffEntry };

export function createTaskDiffContext() {
    const taskDiffStats = writable<Record<string, DiffStatsEntry>>({});
    const taskDiff = writable<Record<string, DiffEntry>>({});
    setContext(TASK_DIFF_STATS_KEY, taskDiffStats);
    setContext(TASK_DIFF_KEY, taskDiff);
    return { taskDiffStats, taskDiff };
}

export function getTaskDiffStatsContext(): Writable<Record<string, DiffStatsEntry>> {
    return getContext(TASK_DIFF_STATS_KEY);
}

// =============================================================================
// Commit Queue Status Context
// =============================================================================
// Tracks commit queue status per project: projectID → { taskID, status }.
// =============================================================================

const COMMIT_QUEUE_STATUS_KEY = Symbol('commitQueueStatus');

export type { CommitQueueEntry };

export function createCommitQueueContext(): Writable<Record<string, CommitQueueEntry>> {
    const store = writable<Record<string, CommitQueueEntry>>({});
    setContext(COMMIT_QUEUE_STATUS_KEY, store);
    return store;
}

export function getCommitQueueContext(): Writable<Record<string, CommitQueueEntry>> {
    return getContext(COMMIT_QUEUE_STATUS_KEY);
}

// =============================================================================
// Expanded Tasks Context
// =============================================================================
// Tracks which task cards are expanded in the board view.
// =============================================================================

const EXPANDED_TASKS_KEY = Symbol('expandedTasks');

export function createExpandedTasksContext(): Writable<Set<string>> {
    const store = writable<Set<string>>(new Set());
    setContext(EXPANDED_TASKS_KEY, store);
    return store;
}

export function getExpandedTasksContext(): Writable<Set<string>> {
    return getContext(EXPANDED_TASKS_KEY);
}

// =============================================================================
// Task Event Context
// =============================================================================
// Task event from the app stream, dispatched to TaskStream via this store.
// =============================================================================

const TASK_EVENT_KEY = Symbol('taskEvent');

export type { TaskEventData };

export function createTaskEventContext(): Writable<TaskEventData | null> {
    const store = writable<TaskEventData | null>(null);
    setContext(TASK_EVENT_KEY, store);
    return store;
}

export function getTaskEventContext(): Writable<TaskEventData | null> {
    return getContext(TASK_EVENT_KEY);
}
