import { describe, it, expect } from 'vitest';
import { canPrioritize } from '../queue-priority';

// Which wait may offer "Prioritize this task": the one whose queue the server can reorder.

const slotWait = { queuePosition: 3, lockWait: null, taskStatus: 'in_progress', taskExecutionState: 'idle' };

describe('canPrioritize', () => {
    it('offers it while the task waits for a running-task slot', () => {
        expect(canPrioritize(slotWait)).toBe(true);
    });

    it('says nothing without a queue position — the Pending badge is four things, one of them a queue', () => {
        expect(canPrioritize({ ...slotWait, queuePosition: 0 })).toBe(false);
    });

    it('says nothing about a running turn, which waits for a request slot no click reorders', () => {
        expect(canPrioritize({ ...slotWait, taskExecutionState: 'running' })).toBe(false);
    });

    it('says nothing while a lock is held, when the task is not the one queued', () => {
        expect(canPrioritize({ ...slotWait, lockWait: { domain: 'worktree', seconds: 9 } })).toBe(false);
    });

    it('says nothing about a row that never started', () => {
        expect(canPrioritize({ ...slotWait, taskStatus: 'backlog' })).toBe(false);
    });

    it('says nothing about a paused or failed run that still carries a stale position', () => {
        expect(canPrioritize({ ...slotWait, taskExecutionState: 'paused' })).toBe(false);
        expect(canPrioritize({ ...slotWait, taskStatus: 'review' })).toBe(false);
    });
});
