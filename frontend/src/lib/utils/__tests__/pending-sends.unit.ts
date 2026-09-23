import { describe, it, expect } from 'vitest';
import { PENDING_SEND_ID_PREFIX, pendingSendEntries } from '$lib/utils/pending-sends';
import type { PendingSend } from '$lib/utils/pending-sends';
import type { ChatEntry } from '$lib/types';

const copy = { queued: 'Connection lost — message queued', refused: 'MIKE refused the queued message' };

function send(actionId: string, content: string, timestamp: number, extra: Partial<PendingSend> = {}): PendingSend {
    return { actionId, content, timestamp, ...extra };
}

const rows = (entries: ChatEntry[]) => entries.filter(e => e.role === 'user');
const notices = (entries: ChatEntry[]) => entries.filter(e => e.role === 'error');

describe('the pending sends a transcript owes', () => {
    it('shows a waiting send with the clock and one notice beside it', () => {
        const entries = pendingSendEntries({
            pending: [send('a1', 'written on the train', 1_700_000_000_000)],
            refused: [],
            copy,
        });

        expect(rows(entries).map(e => [e.id, e.content, e.queued])).toEqual([
            [`${PENDING_SEND_ID_PREFIX}a1`, 'written on the train', true],
        ]);
        expect(notices(entries).map(e => e.error?.code)).toEqual(['send_queued']);
        expect(rows(entries)[0].createdAt).toBe(new Date(1_700_000_000_000).toISOString());
    });

    it('shows a refused send without the clock, under the refusal', () => {
        const entries = pendingSendEntries({
            pending: [],
            refused: [send('a2', 'deleted elsewhere', 1_700_000_000_000)],
            copy,
        });

        expect(rows(entries).map(e => e.queued)).toEqual([false]);
        expect(notices(entries).map(e => e.error?.code)).toEqual(['send_refused']);
    });

    it('keeps the order the person wrote them in, across both kinds', () => {
        const entries = pendingSendEntries({
            pending: [send('a2', 'second', 2)],
            refused: [send('a1', 'first', 1)],
            copy,
        });

        expect(rows(entries).map(e => e.content)).toEqual(['first', 'second']);
    });

    it('carries the attachments, so an image sent offline is still there after a reload', () => {
        const entries = pendingSendEntries({
            pending: [send('a1', 'look at this', 1, { attachments: [{ mimeType: 'image/png', data: 'AAA' }] })],
            refused: [],
            copy,
        });

        expect(rows(entries)[0].attachments).toEqual([{ mimeType: 'image/png', data: 'AAA' }]);
    });

    it('leaves a message this session already drew alone', () => {
        const entries = pendingSendEntries({
            pending: [send('a1', 'already on screen', 1)],
            refused: [],
            rendered: ['a1'],
            copy,
        });

        expect(entries).toHaveLength(0);
    });

    it('says nothing when the queue owes nothing', () => {
        expect(pendingSendEntries({ pending: [], refused: [], copy })).toHaveLength(0);
    });

    it('names the notice once, so a rebuild does not stack copies', () => {
        const entries = pendingSendEntries({
            pending: [send('a1', 'one', 1), send('a2', 'two', 2)],
            refused: [],
            copy,
        });

        expect(notices(entries)).toHaveLength(1);
    });
});
