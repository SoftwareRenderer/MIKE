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
    pairingCodeInput,
    pairingCodeReady,
    pairingLinkFromHash,
} from '$lib/utils/login-flow';
import type { LoginState } from '$lib/utils/login-flow';

const ready = { stage: 'signin', hasPasskey: true, support: 'available', entry: 'signin', error: '', tone: 'error', code: '', name: '' } as LoginState;

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

    it('edits the device name without losing the code next to it', () => {
        const typed = next({ ...ready, stage: 'code', code: 'ABCD-2345' }, { type: 'name', value: 'Kitchen tablet' });
        expect(typed).toMatchObject({ name: 'Kitchen tablet', code: 'ABCD-2345', error: '' });
    });

    it('keeps the device name across a change of body', () => {
        // The name belongs to this device, not to the code, so a fresh code asks for no new name.
        const named = next({ ...ready, stage: 'code', code: 'ABCD-2345', name: 'Kitchen tablet' }, { type: 'back' });
        expect(named.name).toBe('Kitchen tablet');
        expect(next(named, { type: 'pair' })).toMatchObject({ code: '', name: 'Kitchen tablet' });
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
        expect(pairingLinkFromHash('#pair=ABCD-2345')).toEqual({ code: 'ABCD-2345', name: '' });
    });

    it('reads the name the minting device gave this one', () => {
        expect(pairingLinkFromHash('#pair=ABCD2345&name=Kitchen+tablet').name).toBe('Kitchen tablet');
    });

    it('keeps a name to the length a device label is', () => {
        // Text out of a URL has not met the rule the server applies at registration.
        expect(pairingLinkFromHash('#pair=ABCD2345&name=' + 'n'.repeat(200)).name).toHaveLength(64);
    });

    it('hands the code to the field whatever shape the link left it in', () => {
        // The field owns the shape of a code.
        const { code } = pairingLinkFromHash('#pair=ABCD 2345');
        expect(normalizePairingCode(code)).toBe('ABCD2345');
    });

    it('reads nothing else, including anything that is not a code', () => {
        expect(pairingLinkFromHash('').code).toBe('');
        expect(pairingLinkFromHash('#pair=').code).toBe('');
        expect(pairingLinkFromHash('?pair=ABCD2345').code).toBe('');
        expect(pairingLinkFromHash('#token=ABCD2345').code).toBe('');
        expect(pairingLinkFromHash('#name=Kitchen+tablet').code).toBe('');
        expect(pairingLinkFromHash(undefined as unknown as string).code).toBe('');
    });
});

describe('the device name the form opens with', () => {
    it('starts from the name the server read off this browser', () => {
        expect(initialState(false, 'available', 'Chrome 120 · Macintosh').name).toBe('Chrome 120 · Macintosh');
    });

    it('starts blank when the server read nothing', () => {
        // Blank asks the server to name the device.
        expect(initialState(false, 'available').name).toBe('');
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

    it('holds the two groups the code is printed in, whoever filled the field', () => {
        // The reducer answers for every path, so a fragment typed into the address bar by hand and a
        // keystroke end up in one shape.
        expect(next(ready, { type: 'code', value: 'abcd2345' }).code).toBe('ABCD-2345');
        expect(next(ready, { type: 'code', value: 'ABCD 2345' }).code).toBe('ABCD-2345');
    });
});

describe('typing into the code field', () => {
    it('breaks the code after four characters without moving what was typed', () => {
        expect(pairingCodeInput('ABCD', 4)).toEqual({ value: 'ABCD', caret: 4 });
        expect(pairingCodeInput('ABCDE', 5)).toEqual({ value: 'ABCD-E', caret: 6 });
    });

    it('keeps the caret after the character being typed, not after the break', () => {
        // A person mid-code: the next keystroke has to land where they are looking.
        expect(pairingCodeInput('ABXCD-EF', 3)).toEqual({ value: 'ABXC-DEF', caret: 3 });
        expect(pairingCodeInput('ABCD-EX', 7)).toEqual({ value: 'ABCD-EX', caret: 7 });
    });

    it('takes what was pasted in whatever shape it came, and drops what is beyond the code', () => {
        expect(pairingCodeInput(' abcd-2345 ', 10)).toEqual({ value: 'ABCD-2345', caret: 9 });
        expect(pairingCodeInput('ABCDEFGHJK', 10)).toEqual({ value: 'ABCD-EFGH', caret: 9 });
    });

    it('deletes a character when a backspace would only have removed the break', () => {
        // `ABCD-E` with the caret after the break: the browser removes the break, and putting it back
        // would leave the keystroke with nothing to show for it.
        expect(pairingCodeInput('ABCDE', 4, true)).toEqual({ value: 'ABCE', caret: 3 });
    });

    it('leaves a backspace alone when it removed a character', () => {
        expect(pairingCodeInput('ABCD-EF', 6, true)).toEqual({ value: 'ABCD-EF', caret: 6 });
        // A break the person typed themselves is theirs to delete.
        expect(pairingCodeInput('ABCD', 2, true)).toEqual({ value: 'ABCD', caret: 2 });
    });
});
