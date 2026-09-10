/**
 * The app-stream frame names, as one declared list.
 *
 * `backend/types/ws.go` owns these and the archgate `wsevents` rule compares the two lists in both
 * directions: a name Go broadcasts that nothing reads is dead code or a panel that stopped updating, and a
 * name in this union that Go does not define is a branch of the dispatcher below that can never run. The
 * union exists so that second direction has an answer at all — the browser compares `.type` against dozens
 * of literals spread over three sockets (the app stream, the terminal, the extension channel) plus the
 * clone stream's SSE events and the offline queue's action names, and against that pile "does the frontend
 * invent an event?" is not a question with a clean answer. This file is the part of it that is.
 *
 * `AppStreamFrame` is what the app-stream socket hands its dispatcher, so a misspelled comparison is a
 * compile error rather than a silent no-op.
 */
export type AppStreamEventType =
   'token'
    | 'token'
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
 * One frame off the app-stream WebSocket: a name from the vocabulary above, plus whatever that event
 * carries beside it. The payload is deliberately not declared here — each frame's shape is known to the
 * callback that consumes it, and marking the shared keys optional only moves the assumption into five
 * places that then have to pretend to check it. What this type does hold is the name, because a
 * comparison against a name the server never sends is a dispatcher branch that cannot run.
 */
export interface AppStreamFrame {
    type: AppStreamEventType;
    [key: string]: any;
}
