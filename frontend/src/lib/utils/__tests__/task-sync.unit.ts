import { describe, it, expect } from 'vitest';
import { heldFromDigest, resyncPlan, type HeldView } from '../task-sync';
import type { TaskSyncDigest } from '$lib/types';

// The verdict a returning window acts on: which part of the task view the server says has moved.

const conversation = { epoch: 'e1', revision: 7, messages: 42, thread: 'thread-1' };
const diff = { hash: 'diff-a', staged: 0 };
const row = { status: 'in_progress', executionState: 'running', updatedAt: '2026-09-26T10:00:00Z', branchTip: '' };

function held(over: Partial<HeldView> = {}): HeldView {
    return { conversation: { ...conversation }, row: { ...row }, diff: { ...diff }, ...over };
}

function digest(over: Partial<TaskSyncDigest> = {}): TaskSyncDigest {
    return {
        status: 'in_progress', execution_state: 'running', summarizing: false, has_merge_conflict: false,
        branch_tip: '', updated_at: '2026-09-26T10:00:00Z', thread: 'thread-1',
        conversation: { epoch: 'e1', revision: 7, messages: 42 },
        diff: { known: true, hash: 'diff-a', staged: 0 },
        ...over,
    };
}

describe('resyncPlan', () => {
    it('owes nothing when the server matches what this screen applied', () => {
        const verdict = resyncPlan(held(), digest());
        expect(verdict).toEqual({ conversation: false, diff: false, row: false });
    });

    it('names the conversation for each way it can have moved', () => {
        expect(resyncPlan(held(), digest({ conversation: { epoch: 'e1', revision: 8, messages: 42 } })).conversation).toBe(true);
        // Rows a fork copied into a new thread move no revision: the count is what sees them.
        expect(resyncPlan(held(), digest({ conversation: { epoch: 'e1', revision: 7, messages: 43 } })).conversation).toBe(true);
        expect(resyncPlan(held(), digest({ thread: 'thread-2' })).conversation).toBe(true);
    });

    it('names the conversation when the server restarted under it', () => {
        // A revision that restarted at zero is not "unchanged": a cursor the client cannot match is behind.
        expect(resyncPlan(held(), digest({ conversation: { epoch: 'e2', revision: 0, messages: 42 } })).conversation).toBe(true);
    });

    it('names the conversation for a screen that has applied no digest yet', () => {
        const never = { epoch: '', revision: 0, messages: 0, thread: '' };
        expect(resyncPlan(held({ conversation: never }), digest()).conversation).toBe(true);
    });

    it('names only the diff for the panel whose manifest moved', () => {
        const verdict = resyncPlan(held(), digest({ diff: { known: true, hash: 'diff-b', staged: 0 } }));
        expect(verdict).toEqual({ conversation: false, diff: true, row: false });
    });

    it('names the diff for the staged paths alone, which the review bar renders from', () => {
        expect(resyncPlan(held(), digest({ diff: { known: true, hash: 'diff-a', staged: 3 } })).diff).toBe(true);
    });

    it('treats an unknown diff as nothing, never as changed', () => {
        const verdict = resyncPlan(held(), digest({ diff: { known: false, staged: 0 } }));
        expect(verdict).toEqual({ conversation: false, diff: false, row: false });
    });

    it('names the diff for a panel that has drawn nothing while the server holds a manifest', () => {
        expect(resyncPlan(held({ diff: { hash: '', staged: 0 } }), digest()).diff).toBe(true);
    });

    it('names the row for the fields the header and the review bar render from', () => {
        expect(resyncPlan(held(), digest({ execution_state: 'failed' })).row).toBe(true);
        expect(resyncPlan(held(), digest({ status: 'review' })).row).toBe(true);
        // commit-and-done while away: the tip is what a done task's diff is computed against.
        expect(resyncPlan(held(), digest({ branch_tip: 'abc123' })).row).toBe(true);
    });

    it('compares only the row fields this view holds', () => {
        // The chat renders status and execution state and nothing else; a moved updated_at is no news to it.
        const chatHeld = held({ row: { status: 'in_progress', executionState: 'running' } });
        expect(resyncPlan(chatHeld, digest({ updated_at: '2026-09-26T11:00:00Z' })).row).toBe(false);
        expect(resyncPlan(chatHeld, digest({ execution_state: 'idle' })).row).toBe(true);
    });

    it('owes a view that holds one part no work for the parts it does not hold', () => {
        const diffOnly = resyncPlan({ diff: { ...diff } }, digest({
            status: 'review', execution_state: 'failed',
            conversation: { epoch: 'e9', revision: 99, messages: 99 },
        }));
        expect(diffOnly).toEqual({ conversation: false, diff: false, row: false });
    });
});

describe('heldFromDigest', () => {
    it('names the conversation the digest answered, so a screen can record what it drew', () => {
        expect(heldFromDigest(digest())).toEqual(conversation);
        expect(heldFromDigest(digest({ thread: undefined })).thread).toBe('');
    });
});
