// One saved queue is shared by every window on the profile, so "may this window send it" has to be one rule, not
// a guess made at each call site. docs/reconnection.md → "One window replays the queue, and one record holds it".
import { describe, it, expect } from 'vitest';
import { holdsReplay, shouldReplay } from '../queue-ownership';

describe('which window replays the queue', () => {
    it('is the window the record names', () => {
        const record = { windowId: 'w2', takenAt: 1000 };
        expect(holdsReplay(record, 'w2')).toBe(true);
        expect(holdsReplay(record, 'w1')).toBe(false);
    });

    it('is the only window there is when the claim could not be stored', () => {
        // No IndexedDB means no record to share, so a window that doubted itself here would send nothing at all.
        expect(holdsReplay(null, 'w1')).toBe(true);
    });
});

describe('when a queue is ready to go out', () => {
    const waiting = { hasWork: true, held: false, heldUntil: 0, now: 1000 };

    it('holds a queue with nothing in it', () => {
        expect(shouldReplay({ ...waiting, hasWork: false })).toBe(false);
    });

    it('holds a queue while a reason holds it, whatever the clock says', () => {
        expect(shouldReplay({ ...waiting, held: true })).toBe(false);
    });

    it('waits out the pace a failed replay asked for, then goes', () => {
        expect(shouldReplay({ ...waiting, heldUntil: 1001 })).toBe(false);
        expect(shouldReplay({ ...waiting, heldUntil: 1000 })).toBe(true);
    });
});
