import { describe, it, expect } from 'vitest';

/**
 * Which read of the passkey list says a device paired. The section re-reads the list while a pairing code is
 * live, so a wrong answer here is a wrong announcement: a rename reported as a new device, or a device that
 * paired while the countdown ran never named.
 */
import { newlyPairedPasskey } from '$lib/utils/passkeys';
import type { PasskeyInfo } from '$lib/types';

function row(id: string, created_at: string, name = id): PasskeyInfo {
    return { id, name, created_at, last_used: '' };
}

describe('the arrival a pairing poll is waiting for', () => {
    it('is nothing when the list is unchanged', () => {
        const held = [row('a', '2026-08-14T09:00:00Z'), row('b', '2026-08-14T09:05:00Z')];
        expect(newlyPairedPasskey(held, [...held])).toBeNull();
    });

    it('is nothing when a row only changed its label', () => {
        const before = [row('a', '2026-08-14T09:00:00Z', 'Chrome on Linux')];
        const after = [row('a', '2026-08-14T09:00:00Z', 'My laptop')];
        expect(newlyPairedPasskey(before, after)).toBeNull();
    });

    it('is nothing when the list emptied — a revoke is not a pairing', () => {
        expect(newlyPairedPasskey([row('a', '2026-08-14T09:00:00Z')], [])).toBeNull();
    });

    it('is nothing when there was nothing to read and still is not', () => {
        expect(newlyPairedPasskey([], [])).toBeNull();
    });

    it('is the device that joined, whatever the answer calls it', () => {
        const before = [row('a', '2026-08-14T09:00:00Z')];
        const joined = row('b', '2026-08-14T09:30:00Z', 'Firefox on Android');
        expect(newlyPairedPasskey(before, [before[0], joined])?.id).toBe('b');
    });

    it('names the newest when two devices joined between two reads', () => {
        const before = [row('a', '2026-08-14T09:00:00Z')];
        const older = row('b', '2026-08-14T09:30:00Z');
        const newer = row('c', '2026-08-14T09:31:00Z');
        // The server orders by created_at, but the answer is not allowed to depend on that.
        expect(newlyPairedPasskey(before, [before[0], newer, older])?.id).toBe('c');
    });

    it('is the arrival even when another device was revoked in the same window', () => {
        const before = [row('a', '2026-08-14T09:00:00Z'), row('b', '2026-08-14T09:05:00Z')];
        const joined = row('c', '2026-08-14T09:40:00Z');
        expect(newlyPairedPasskey(before, [joined])?.id).toBe('c');
    });
});
