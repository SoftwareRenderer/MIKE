/**
 * Reading a passkey list that changed underneath the reader.
 *
 * Settings → About re-reads `GET /api/auth/passkeys` while a pairing code is live, and the one question that
 * read has to answer is whether a credential *arrived*. It is answered by id and not by length: two devices
 * can register between two reads, a rename changes a row without changing the count, and the row that leaves
 * is a revoke, which is not a pairing.
 */
import type { PasskeyInfo } from '$lib/types';

/**
 * The newest row `before` does not hold, or `null` when nothing new arrived.
 *
 * `created_at` is fixed-width `RFC3339` UTC (`formatAuthTime`, `backend/server/handler_sessions.go`), so
 * comparing the text compares the instants — the same comparison the section's own row order makes.
 */
export function newlyPairedPasskey(before: readonly PasskeyInfo[], after: readonly PasskeyInfo[]): PasskeyInfo | null {
    const known = new Set(before.map(row => row.id));
    let newest: PasskeyInfo | null = null;
    for (const row of after) {
        if (known.has(row.id)) continue;
        if (!newest || row.created_at >= newest.created_at) newest = row;
    }
    return newest;
}
