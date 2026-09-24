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
    | 'sandbox_image_status'
    | 'task_event'
    | 'board_refresh'
    | 'project_counts'
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
 * The keepalive frame's name, mirroring `httputil.KeepaliveFrame`. It stays out of the list above, which
 * is the `backend/types/ws.go` vocabulary the `wsevents` rule checks, so the frame is matched by text.
 */
const keepaliveFrameType = 'ping';

/** Whether this frame is the keepalive rather than content a screen could paint. */
export function isKeepaliveFrame(frame: unknown): boolean {
    return (frame as { type?: unknown } | null)?.type === keepaliveFrameType;
}
