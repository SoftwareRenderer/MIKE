import type { ChatEntry } from '../types';

/** Whether the review bar should offer **Resume**: the last real row is a question with no answer after
 * it, and nobody is on their way to answer it. A send the offline queue is holding is the exception —
 * nothing is in flight then, and Resume is how it gets recovered. */
export function offersResume(args: {
    taskStatus: string;
    /** A turn is streaming, or the row says it is running. */
    loading: boolean;
    summarizing: boolean;
    awaitingResponse: boolean;
    /** The click that asked this question is still working. */
    startingTurn: boolean;
    lastEntry: ChatEntry | undefined;
}): boolean {
    if (args.taskStatus !== 'review' || args.loading || args.summarizing) return false;
    if (!args.lastEntry || args.lastEntry.role !== 'user') return false;
    if (args.startingTurn) return false;
    return !args.awaitingResponse || args.lastEntry.queued === true;
}
