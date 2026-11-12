import type { StorageUsage } from '$lib/types';

/**
 * The app-stream frame names, as one declared list. `backend/types/ws.go` owns them; the archgate
 * `wsevents` rule reads the declaration text below and compares the two lists in both directions, so a
 * name only one side has is either a dead broadcast or a dispatcher branch that cannot run.
 *
 * The `export` is that rule's locator, hence the tag on an export nothing imports.
 */
/** @knipignore */
export type AppStreamEventType =
   'token'
    | 'reasoning'
    | 'tool_call'
    | 'tool_progress'
    | 'tool_output'
    | 'tool_result'
    | 'status'
    | 'queue'
    | 'lock_wait'
    | 'summarization'
    | 'complete'
    | 'error'
    | 'user_message'
    | 'stats'
    | 'diff_changed'
    | 'thread_switch'
    | 'git_checkpoint_pending'
    | 'git_checkpoint_complete'
    | 'merge_resolution'
    | 'build_progress'
    | 'sandbox_stats'
    | 'prompt_progress'
    | 'hint'
    | 'advisory'
    | 'extension_change'
    | 'task_event'
    | 'board_refresh'
    | 'project_counts'
    | 'storage_usage'
    | 'project_branch'
    | 'project_ports'
    | 'task_updated'
    | 'task_deleted'
    | 'task_diff_stats'
    | 'commit_status'
    | 'fs_event'

/**
 * One frame off the app-stream WebSocket: a name from the vocabulary above, plus whatever it carries.
 * The payload is deliberately undeclared — each consumer knows its own shape, and the index signature is
 * what keeps that assumption out of every `switch`.
 */
export interface AppStreamFrame {
    type: AppStreamEventType;
    [key: string]: any;
}

/**
 * Reads a `storage_usage` frame into the numbers the Done column shows. The server leaves the task figures
 * off a frame until it has measured the worktrees, and this function leaves them off too, because a zero
 * would say the disk is empty when nothing was measured.
 */
export function storageUsageFromFrame(frame: AppStreamFrame): StorageUsage {
    const usage: StorageUsage = { total_bytes: frame.total_bytes, avail_bytes: frame.avail_bytes };
    if (typeof frame.tasks_bytes === 'number' && typeof frame.tasks_count === 'number') {
        usage.tasks_bytes = frame.tasks_bytes;
        usage.tasks_count = frame.tasks_count;
    }
    return usage;
}

/**
 * The keepalive frame's name, mirroring `httputil.KeepaliveFrame`. It stays out of the list above, which
 * is the `backend/types/ws.go` vocabulary the `wsevents` rule checks, so the frame is matched by text.
 */
const keepaliveFrameType = 'ping';

/** Whether this frame is the keepalive rather than content a screen could paint. */
export function isKeepaliveFrame(frame: unknown): boolean {
    return (frame as { type?: unknown } | null)?.type === keepaliveFrameType;
}
