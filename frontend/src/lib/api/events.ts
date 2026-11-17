import type { StorageUsage, TaskSpaceFigure } from '$lib/types';

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
 * Reads a `storage_usage` frame into the filesystem numbers the Done column shows. One frame name
 * carries two shapes, and this reads the one with no project on it.
 */
export function storageUsageFromFrame(frame: AppStreamFrame): StorageUsage {
    return { total_bytes: frame.total_bytes, avail_bytes: frame.avail_bytes };
}

/**
 * Reads one project's task figure out of a `storage_usage` frame, or returns null when the frame is the
 * filesystem shape. The server leaves the figure off until its walk has measured the worktrees, and this
 * leaves it off too, because a zero would say the board is empty when nothing was measured.
 */
export function taskSpaceFromFrame(frame: AppStreamFrame): { projectId: string; figure: TaskSpaceFigure } | null {
    if (typeof frame.project_id !== 'string' || frame.project_id === '') return null;
    if (typeof frame.tasks_bytes !== 'number' || typeof frame.tasks_count !== 'number') return null;
    return {
        projectId: frame.project_id,
        figure: { tasks_bytes: frame.tasks_bytes, tasks_count: frame.tasks_count },
    };
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
