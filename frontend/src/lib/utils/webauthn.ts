/**
 * The WebAuthn seam.
 *
 * The server hands back the ceremony's options as JSON, and every byte field in that JSON is a base64url
 * string; `navigator.credentials` takes and returns `ArrayBuffer`s, and refuses anything else. So the two
 * directions of that conversion, and nothing else, are most of this file — they are also the part that can
 * be tested without a browser, which is why they are here rather than inlined at the call site.
 *
 * What the browser is told to check is the server's decision, not this module's: `userVerification:
 * required` and the resident-key requirement arrive in the options and are passed through, so a change to
 * the ceremony is made in `backend/auth/passkey.go` and not here as well.
 */
import { t } from '$lib/i18n';

/**
 * The two option objects the browser is asked to act on, in the JSON form the `publicKey` of each
 * ceremony's `begin` answer carries.
 */
export type CredentialCreationJSON = {
    challenge: string;
    rp?: { id?: string; name?: string };
    user?: { id?: string; name?: string; displayName?: string };
    pubKeyCredParams?: { type: PublicKeyCredentialType; alg: number }[];
    authenticatorSelection?: Record<string, unknown>;
    excludeCredentials?: { type: PublicKeyCredentialType; id: string }[];
    attestation?: string;
    timeout?: number;
};

export type CredentialRequestJSON = {
    challenge: string;
    timeout?: number;
    rpId?: string;
    userVerification?: string;
    allowCredentials?: { type: PublicKeyCredentialType; id: string }[];
};

/**
 * base64url → bytes. Lenient about padding because a credential id is opaque bytes and a server that
 * padded one would still mean the same key; strict about the alphabet, because "not base64url" is a bug
 * worth throwing on rather than a wrong ceremony worth failing silently.
 *
 * @knipignore the encode/decode pair is one contract, and the unit test pins both directions against each
 * other: a decoder checked against a hand-written expectation can agree with a wrong encoder.
 */
export function base64UrlToBytes(value: string): Uint8Array {
    const normalized = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(value.length + ((4 - (value.length % 4)) % 4), '=');
    const binary = atob(normalized);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    return bytes;
}

/** bytes → base64url, no padding.
 * @knipignore the round-trip half of the codec the unit test checks `base64UrlToBytes` against. */
export function bytesToBase64Url(bytes: Uint8Array): string {
    let binary = '';
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function buffer(value: string): ArrayBuffer {
    const bytes = base64UrlToBytes(value);
    // Slice the backing buffer out of the pool: `challenge` handed to the browser must be exactly these
    // bytes, and a Uint8Array over a shared 8 KB pool is a BufferSource whose length the browser reads
    // from the buffer, not from the view.
    return bytes.slice().buffer as ArrayBuffer;
}

/** The creation options, in the shape `navigator.credentials.create` takes. */
export function creationOptions(json: CredentialCreationJSON): PublicKeyCredentialCreationOptions {
    const user = json.user ?? {};
    return {
        rp: { id: json.rp?.id, name: json.rp?.name ?? '' },
        user: {
            id: buffer(user.id ?? ''),
            name: user.name ?? '',
            displayName: user.displayName ?? '',
        },
        challenge: buffer(json.challenge),
        pubKeyCredParams: json.pubKeyCredParams ?? [],
        timeout: json.timeout,
        excludeCredentials: (json.excludeCredentials ?? []).map((entry) => ({
            id: buffer(entry.id),
            type: entry.type,
        })),
        authenticatorSelection: json.authenticatorSelection as AuthenticatorSelectionCriteria | undefined,
        attestation: (json.attestation ?? 'none') as AttestationConveyancePreference,
    };
}

/** The request options, in the shape `navigator.credentials.get` takes. */
export function requestOptions(json: CredentialRequestJSON): PublicKeyCredentialRequestOptions {
    return {
        challenge: buffer(json.challenge),
        timeout: json.timeout,
        rpId: json.rpId,
        userVerification: json.userVerification as UserVerificationRequirement | undefined,
        allowCredentials: (json.allowCredentials ?? []).map((entry) => ({
            id: buffer(entry.id),
            type: entry.type,
        })),
    };
}

/**
 * What to POST after a ceremony: the browser's own JSON, which is already the wire form the server
 * parses (`id`, `rawId`, and a `response` of base64url strings). The interface type has no `toJSON`, so
 * this is where that gap is closed — and where a browser that hands back something else becomes a thrown
 * error rather than a request with an empty body.
 */
export function credentialToJSON(credential: PublicKeyCredential): unknown {
    const toJSON = (credential as unknown as { toJSON?: () => unknown }).toJSON;
    if (typeof toJSON !== 'function') {
        throw new TypeError(t('app.login.unusableCredential'));
    }
    return toJSON.call(credential);
}

/**
 * Whether this browser, reached over this connection, can run a passkey ceremony at all.
 *
 * Each reason is a different sentence for the reader, because each has a different fix:
 *  - `insecure-context` — WebAuthn is a secure-context API, so MIKE over plain HTTP on a LAN address has
 *    no passkey prompt to show. Loopback is exempt in the browser, which is why development works and a
 *    phone pointed at `http://192.168.1.10:3000` does not.
 *  - `no-api` — no `navigator.credentials`, or no platform authenticator behind it (an old browser, a
 *    webview with the API cut).
 */
export type PasskeySupport = 'available' | 'insecure-context' | 'no-api';

export function passkeySupport(): PasskeySupport {
    if (typeof window === 'undefined' || typeof navigator === 'undefined') return 'no-api';
    // The browser's own answer, not a scheme check: loopback over plain HTTP is a secure context, and a
    // `location.protocol` test here would refuse the one address every developer uses.
    if (!window.isSecureContext) return 'insecure-context';
    if (typeof window.PublicKeyCredential !== 'function') return 'no-api';
    if (!navigator.credentials || typeof navigator.credentials.create !== 'function') return 'no-api';
    return 'available';
}
