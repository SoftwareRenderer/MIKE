import type { TaskSyncDigest } from '$lib/types';

/**
 * What a view of a task holds, in the shape the sync route answers. A view may only report what it *applied*:
 * a match then means this screen and the server agree, which a response-body ETag cannot say.
 * `docs/reconnection.md → The tab that comes back`.
 */

/** The conversation this screen painted. The empty value matches nothing, so a screen holding it asks. */
export interface HeldConversation {
    epoch: string;
    revision: number;
    messages: number;
    thread: string;
}

/** The diff manifest a panel drew. An empty `hash` is a panel that has drawn none. */
interface HeldDiff {
    hash: string;
    staged: number;
}

/** The task-row fields a view renders from. A field left out compares as no difference: this view does not
 * answer for it. */
export interface HeldRow {
    status?: string;
    executionState?: string;
    updatedAt?: string;
    branchTip?: string;
    summarizing?: boolean;
    mergeConflict?: boolean;
}

export interface HeldView {
    conversation?: HeldConversation;
    row?: HeldRow;
    diff?: HeldDiff;
}

/** What a digest says each view must do about. */
export interface SyncVerdict {
    /** The transcript on screen is not the conversation the server holds. */
    conversation: boolean;
    /** The diff manifest on screen is not the one the server last described. */
    diff: boolean;
    /** A row field this view renders from has moved. */
    row: boolean;
}

/** The conversation the digest names, ready to be recorded as applied. */
export function heldFromDigest(digest: TaskSyncDigest): HeldConversation {
    return {
        epoch: digest.conversation.epoch,
        revision: digest.conversation.revision,
        messages: digest.conversation.messages,
        thread: digest.thread ?? '',
    };
}

/**
 * Which parts of the task view fell behind, for the parts this view holds. An unknown diff is no difference,
 * and a cursor this client cannot match reads as behind — the direction that costs a re-read rather than
 * leaving a stale screen.
 */
export function resyncPlan(held: HeldView, digest: TaskSyncDigest): SyncVerdict {
    return {
        conversation: held.conversation ? conversationBehind(held.conversation, digest) : false,
        row: held.row ? rowBehind(held.row, digest) : false,
        diff: !!held.diff && digest.diff.known
            && ((digest.diff.hash ?? '') !== held.diff.hash || digest.diff.staged !== held.diff.staged),
    };
}

function conversationBehind(held: HeldConversation, digest: TaskSyncDigest): boolean {
    return digest.conversation.epoch !== held.epoch
        || digest.conversation.revision !== held.revision
        || digest.conversation.messages !== held.messages
        || (digest.thread ?? '') !== held.thread;
}

/** Rows a fork copied into a new thread move no revision; the count and the thread are what see them. */
function rowBehind(held: HeldRow, digest: TaskSyncDigest): boolean {
    if (held.status !== undefined && held.status !== digest.status) return true;
    if (held.executionState !== undefined && held.executionState !== digest.execution_state) return true;
    if (held.updatedAt !== undefined && held.updatedAt !== digest.updated_at) return true;
    if (held.branchTip !== undefined && held.branchTip !== (digest.branch_tip ?? '')) return true;
    if (held.summarizing !== undefined && held.summarizing !== digest.summarizing) return true;
    if (held.mergeConflict !== undefined && held.mergeConflict !== digest.has_merge_conflict) return true;
    return false;
}
