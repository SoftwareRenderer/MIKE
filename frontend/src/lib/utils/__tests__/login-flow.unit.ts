import { describe, it, expect } from 'vitest';

/**
 * The login form's branches, as data. Every one of these is a screen a person can end up on, and the two
 * that must never happen are the ones pinned first: a fresh install asking for a passkey it does not have,
 * and a browser that cannot run a ceremony being left holding a button that can only fail.
 */
import {
    initialState,
    next,
    normalizePairingCode,
    pairingCodeFromHash,
    pairingCodeReady,
} from '$lib/utils/login-flow';
import type { LoginState } from '$lib/utils/login-flow';

const ready = { stage: 'signin', hasPasskey: true, support: 'available', entry: 'signin', error: '', tone: 'error', code: '' } as LoginState;

describe('the body this browser starts on', () => {
    it('asks for a pairing code when there is no passkey to ask for', () => {
        expect(initialState(false, 'available').stage).toBe('code');
    });

    it('asks for a passkey when the install has one', () => {
        expect(initialState(true, 'available').stage).toBe('signin');
    });

    it('takes the passkey branch when the server could not say', () => {
        // `null` is "unknown", and the ceremony it starts answers the question for itself: an install with
        // nothing registered refuses the assertion, and the form offers the code from there.
        expect(initialState(null, 'available').stage).toBe('signin');
    });

    it('shows the reason instead of a button when the ceremony cannot run', () => {
        // Even on an install with a passkey: the prompt would not appear, so the code field would only
        // spend a pairing code on a prompt that never opens.
        expect(initialState(true, 'insecure-context').stage).toBe('unsupported');
        expect(initialState(false, 'no-api').stage).toBe('unsupported');
    });
});

describe('the machine', () => {
    it('opens the code field from a sign-in, and forgets what it said', () => {
        const state = next({ ...ready, error: 'the passkey did not verify' }, { type: 'pair' });
        expect(state.stage).toBe('code');
        expect(state.entry).toBe('code');
        expect(state.error).toBe('');
    });

    it('goes back to the body this install started on', () => {
        expect(next({ ...ready, stage: 'code' }, { type: 'back' }).stage).toBe('signin');
        // On a fresh install the code field is the form, so `back` is the identity — the form must not
        // render a button for it.
        const fresh = initialState(false, 'available');
        expect(next({ ...fresh, stage: 'signin' }, { type: 'back' }).stage).toBe('code');
    });

    it('clears the message as soon as the person types', () => {
        const state = next({ ...ready, stage: 'code', error: 'pairing code is not valid' }, { type: 'code', value: 'A' });
        expect(state).toMatchObject({ code: 'A', error: '' });
    });

    it('returns a failed ceremony to the body it started from', () => {
        const fromCode = next(next(ready, { type: 'pair' }), { type: 'start' });
        expect(fromCode.stage).toBe('ceremony');
        const failed = next(fromCode, { type: 'error', message: 'the passkey did not verify' });
        expect(failed).toMatchObject({ stage: 'code', error: 'the passkey did not verify', tone: 'error' });

        const fromSignIn = next(ready, { type: 'start' });
        expect(next(fromSignIn, { type: 'error', message: 'nope' }).stage).toBe('signin');
    });

    it('keeps the message when one arrives outside a ceremony', () => {
        // Nothing is in flight, so the stage the person is reading stays on screen with the words on it.
        const state = next({ ...ready, stage: 'code' }, { type: 'error', message: 'name must be 1 to 64 characters' });
        expect(state).toMatchObject({ stage: 'code', error: 'name must be 1 to 64 characters' });
    });

    it('shows a dismissed prompt without the error tone', () => {
        const ceremony = next(ready, { type: 'start' });
        expect(next(ceremony, { type: 'error', message: 'dismissed', tone: 'note' }))
            .toMatchObject({ stage: 'signin', tone: 'note' });
    });
});

describe('the code from a pairing link', () => {
    it('reads the fragment the server writes', () => {
        expect(pairingCodeFromHash('#pair=ABCD-2345')).toBe('ABCD-2345');
    });

    it('reads nothing else, including anything that is not a code', () => {
        expect(pairingCodeFromHash('')).toBe('');
        expect(pairingCodeFromHash('#pair=')).toBe('');
        expect(pairingCodeFromHash('#pair=ABCD 2345')).toBe('');
        expect(pairingCodeFromHash('?pair=ABCD2345')).toBe('');
        expect(pairingCodeFromHash('#token=ABCD2345')).toBe('');
        expect(pairingCodeFromHash(undefined as unknown as string)).toBe('');
    });
});

describe('the code field', () => {
    it('normalizes what a person types into what the server compares', () => {
        expect(normalizePairingCode(' abcd-2345 ')).toBe('ABCD2345');
        expect(normalizePairingCode('ABCD 2345')).toBe('ABCD2345');
        expect(normalizePairingCode('')).toBe('');
    });

    it('is ready at eight characters and at nothing else', () => {
        expect(pairingCodeReady('ABCD-2345')).toBe(true);
        expect(pairingCodeReady('abcd2345')).toBe(true);
        expect(pairingCodeReady('ABCD-234')).toBe(false);
        expect(pairingCodeReady('ABCD-2345-6')).toBe(false);
    });
});
