/**
 * Identifiers and tokens minted in the browser.
 *
 * The CSPRNG is asked for directly, not through `crypto.randomUUID`: that one is missing on a
 * plain-HTTP origin, where an ID minted here still becomes the identity of a stored row.
 */

function randomBytes(length: number): Uint8Array {
    const bytes = new Uint8Array(length);
    crypto.getRandomValues(bytes);
    return bytes;
}

function toHex(bytes: Uint8Array): string {
    let out = '';
    for (const b of bytes) out += b.toString(16).padStart(2, '0');
    return out;
}

/** `length` random bytes as lowercase hex. */
export function randomHex(length: number): string {
    return toHex(randomBytes(length));
}

/**
 * A v4 UUID — the shape `crypto.randomUUID` answers with, assembled here so it is available on
 * a non-secure origin too.
 */
export function randomUUIDv4(): string {
    const bytes = randomBytes(16);
    bytes[6] = (bytes[6] & 0x0f) | 0x40; // version 4
    bytes[8] = (bytes[8] & 0x3f) | 0x80; // RFC 4122 variant
    const hex = toHex(bytes);
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
