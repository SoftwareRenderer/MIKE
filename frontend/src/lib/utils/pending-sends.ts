// A queued message has no row on the server, so `offline-queue` is its only record and these rows are
// derived from it rather than remembered in the mirror. A row is named for the queue action, so a
// rebuild draws the same row instead of a second one.

import type { ChatEntry } from '$lib/types';

/** A send waiting in the queue, or refused by the server, reduced to what a row shows. */
export interface PendingSend {
    actionId: string;
    content: string;
    attachments?: Array<{ mimeType: string; data: string }>;
    timestamp: number;
}

/** The notice's two sentences, passed in so this module stays free of the catalog. */
export interface PendingSendCopy {
    queued: string;
    refused: string;
}

/** Marks a row as derived from the queue, which is what a re-derive removes. */
export const PENDING_SEND_ID_PREFIX = 'queued:';

function pendingSendEntryId(actionId: string): string {
    return `${PENDING_SEND_ID_PREFIX}${actionId}`;
}

/** One row per send still owed a row, plus the notice that explains them. */
export function pendingSendEntries(args: {
    /** Still waiting to be applied, in the order they were written. */
    pending: PendingSend[];
    /** Dropped by the server — refused, not pending, not applied. */
    refused: PendingSend[];
    /** Action ids already on screen as an optimistic entry from this session. */
    rendered?: Iterable<string>;
    copy: PendingSendCopy;
    threadId?: string;
}): ChatEntry[] {
    const rendered = new Set(args.rendered ?? []);
    // A refusal has been answered, so it carries no clock.
    const row = (send: PendingSend, waiting: boolean): ChatEntry => ({
        id: pendingSendEntryId(send.actionId),
        role: 'user',
        content: send.content,
        attachments: send.attachments,
        threadId: args.threadId,
        isStreaming: false,
        createdAt: new Date(send.timestamp).toISOString(),
        queued: waiting,
    });

    const rows = [
        ...args.pending.filter(s => !rendered.has(s.actionId)).map(s => row(s, true)),
        ...args.refused.filter(s => !rendered.has(s.actionId)).map(s => row(s, false)),
    ].sort((a, b) => (a.createdAt || '').localeCompare(b.createdAt || ''));

    const notices: ChatEntry[] = [];
    const notice = (code: 'send_queued' | 'send_refused', message: string, newest: number): ChatEntry => ({
        id: `${PENDING_SEND_ID_PREFIX}notice:${code}`,
        role: 'error',
        content: message,
        isStreaming: false,
        error: { code, message },
        createdAt: new Date(newest).toISOString(),
    });
    if (args.pending.some(s => !rendered.has(s.actionId))) {
        notices.push(notice('send_queued', args.copy.queued, newestOf(args.pending, rendered)));
    }
    if (args.refused.some(s => !rendered.has(s.actionId))) {
        notices.push(notice('send_refused', args.copy.refused, newestOf(args.refused, rendered)));
    }
    return [...rows, ...notices];
}

function newestOf(sends: PendingSend[], rendered: Set<string>): number {
    const unseen = sends.filter(s => !rendered.has(s.actionId));
    return unseen.length ? Math.max(...unseen.map(s => s.timestamp)) : Date.now();
}
