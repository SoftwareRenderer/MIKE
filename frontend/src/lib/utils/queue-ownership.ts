/**
 * Which window replays the offline queue, and which one only reads it. The record is one per browser profile, so
 * the newest window takes the replay and older ones go quiet — still queueing, still showing what the record
 * holds — because two senders would send every queued write twice; when the owner closes, a reader takes it back.
 */

/** What the `queue-owner` record holds. */
export interface QueueOwner {
    windowId: string;
    /** When this window took the record, ms. Nothing times a claim out: it ends when the window closes. */
    takenAt: number;
}

/**
 * Whether the replay is this window's to run. No record at all means there was nowhere to write one, and then
 * this window's queue is the only queue.
 */
export function holdsReplay(record: QueueOwner | null, windowId: string): boolean {
    return record === null || record.windowId === windowId;
}

/** Whether the queue is ready to go out: work waiting, nothing holding it, its retry pace spent. */
export function shouldReplay(args: {
    hasWork: boolean;
    /** The socket is down, or this app's own requests stopped being answered. */
    held: boolean;
    /** When a replay that failed on the network may be tried again. */
    heldUntil: number;
    now: number;
}): boolean {
    if (!args.hasWork || args.held) return false;
    return args.now >= args.heldUntil;
}
