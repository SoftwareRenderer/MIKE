import { describe, it, expect, afterEach } from 'vitest';

/**
 * The WebAuthn codecs — the only part of a ceremony that is testable without an authenticator, and the
 * part that fails silently. A challenge handed over as text instead of bytes is a prompt the browser
 * refuses; a credential id decoded one byte short is a sign-in that verifies nothing; and a `Uint8Array`
 * left over a shared pool is a BufferSource whose length the browser reads from the pool rather than from
 * the view, which is a wrong ceremony that looks like a correct one.
 */
import {
    base64UrlToBytes,
    bytesToBase64Url,
    creationOptions,
    credentialToJSON,
    passkeySupport,
    requestOptions,
} from '$lib/utils/webauthn';
import type { CredentialCreationJSON, CredentialRequestJSON } from '$lib/utils/webauthn';

describe('base64url ↔ bytes', () => {
    it('decodes the URL alphabet, padded or not', () => {
        expect(Array.from(base64UrlToBytes('AQID'))).toEqual([1, 2, 3]);
        expect(Array.from(base64UrlToBytes('-_A'))).toEqual(Array.from(base64UrlToBytes('-_A=')));
        // 0xFB 0xFF is the pair the two alphabets disagree about: `+/` in base64, `-_` on the wire.
        expect(Array.from(base64UrlToBytes('-_8'))).toEqual([0xfb, 0xff]);
    });

    it('round-trips the bytes a credential id is', () => {
        const bytes = new Uint8Array(Array.from({ length: 64 }, (_, i) => (i * 7 + 3) % 256));
        const encoded = bytesToBase64Url(bytes);
        expect(encoded).not.toMatch(/[+/=]/);
        expect(Array.from(base64UrlToBytes(encoded))).toEqual(Array.from(bytes));
    });

    it('refuses bytes that are not base64url at all', () => {
        // "Not base64url" is a bug in the caller, and a ceremony silently built on garbage is worse.
        expect(() => base64UrlToBytes('not base64!')).toThrow();
    });
});

describe('creationOptions', () => {
    const json: CredentialCreationJSON = {
        challenge: 'AQID',
        rp: { id: 'mike.example', name: 'MIKE' },
        user: { id: 'BAQFBg', name: 'mike', displayName: 'mike' },
        pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
        authenticatorSelection: { userVerification: 'required', residentKey: 'required' },
        excludeCredentials: [{ type: 'public-key', id: 'Zm9v' }],
        timeout: 60000,
    };

    it('hands the browser buffers, not the text the server sent', () => {
        const options = creationOptions(json);
        expect(options.challenge).toBeInstanceOf(ArrayBuffer);
        // The exact length: an ArrayBuffer that is a view over a longer pool is not this challenge.
        expect((options.challenge as ArrayBuffer).byteLength).toBe(3);
        expect(Array.from(new Uint8Array(options.challenge as ArrayBuffer))).toEqual([1, 2, 3]);
        expect(Array.from(new Uint8Array(options.user.id as ArrayBuffer))).toEqual([4, 4, 5, 6]);
        expect(Array.from(new Uint8Array(options.excludeCredentials![0].id as ArrayBuffer))).toEqual([102, 111, 111]);
    });

    it('keeps the server\'s ceremony policy as it was sent', () => {
        // The requirement to verify the person, and to store the key on the device, are the server's
        // decision. Copying them through is the whole contract; restating them here would be a second
        // place to change.
        const options = creationOptions(json);
        expect(options.authenticatorSelection?.userVerification).toBe('required');
        expect(options.authenticatorSelection?.residentKey).toBe('required');
        expect(options.rp).toEqual({ id: 'mike.example', name: 'MIKE' });
        expect(options.timeout).toBe(60000);
        expect(options.attestation).toBe('none');
    });

    it('fills the fields a minimal answer leaves out with empties the browser accepts', () => {
        const options = creationOptions({ challenge: 'AQID' });
        expect(options.user).toEqual({ id: new ArrayBuffer(0), name: '', displayName: '' });
        expect(options.pubKeyCredParams).toEqual([]);
        expect(options.excludeCredentials).toEqual([]);
        expect(options.attestation).toBe('none');
    });
});

describe('requestOptions', () => {
    it('offers only the credentials the server named', () => {
        const options = requestOptions(<CredentialRequestJSON>{
            challenge: 'BAUG',
            rpId: 'mike.example',
            userVerification: 'required',
            allowCredentials: [{ type: 'public-key', id: 'Zm9v' }],
        });
        expect(Array.from(new Uint8Array(options.challenge as ArrayBuffer))).toEqual([4, 5, 6]);
        expect(options.rpId).toBe('mike.example');
        expect(options.userVerification).toBe('required');
        expect(options.allowCredentials ?? []).toHaveLength(1);
        expect(Array.from(new Uint8Array(options.allowCredentials![0].id as ArrayBuffer))).toEqual([102, 111, 111]);
    });

    it('offers nothing when the server named none, so the browser picks a discoverable key', () => {
        const options = requestOptions({ challenge: 'BAUG' });
        expect(options.allowCredentials).toEqual([]);
        expect(options.rpId).toBeUndefined();
    });
});

describe('credentialToJSON', () => {
    it('sends the browser\'s own wire form', () => {
        const json = { id: 'abc', rawId: 'abc', response: { clientDataJSON: 'e30' } };
        expect(credentialToJSON({ toJSON: () => json } as unknown as PublicKeyCredential)).toEqual(json);
    });

    it('throws on a credential it cannot serialize, rather than POSTing an empty body', () => {
        expect(() => credentialToJSON({} as unknown as PublicKeyCredential)).toThrow(TypeError);
    });
});

describe('passkeySupport', () => {
    const restore = {
        secure: Object.getOwnPropertyDescriptor(window, 'isSecureContext'),
        pck: Object.getOwnPropertyDescriptor(window, 'PublicKeyCredential'),
        credentials: Object.getOwnPropertyDescriptor(navigator, 'credentials'),
    };

    afterEach(() => {
        Object.defineProperty(window, 'isSecureContext', restore.secure ?? { value: false, configurable: true });
        if (restore.pck) Object.defineProperty(window, 'PublicKeyCredential', restore.pck);
        else delete (window as any).PublicKeyCredential;
        if (restore.credentials) Object.defineProperty(navigator, 'credentials', restore.credentials);
        else delete (navigator as any).credentials;
    });

    function surface(secure: boolean, api: boolean): void {
        Object.defineProperty(window, 'isSecureContext', { value: secure, configurable: true });
        Object.defineProperty(window, 'PublicKeyCredential', { value: api ? function () { } : undefined, configurable: true });
        Object.defineProperty(navigator, 'credentials', {
            value: api ? { create: async () => null } : undefined,
            configurable: true,
        });
    }

    it('accepts a secure context with an API behind it', () => {
        surface(true, true);
        expect(passkeySupport()).toBe('available');
    });

    it('names the insecure context as its own reason', () => {
        // The fix is a different address, not a different browser, and the form says so.
        surface(false, true);
        expect(passkeySupport()).toBe('insecure-context');
    });

    it('names a missing API as its own reason', () => {
        surface(true, false);
        expect(passkeySupport()).toBe('no-api');
        Object.defineProperty(window, 'PublicKeyCredential', { value: function () { }, configurable: true });
        Object.defineProperty(navigator, 'credentials', { value: {}, configurable: true });
        expect(passkeySupport()).toBe('no-api');
    });
});
