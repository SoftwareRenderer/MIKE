/**
 * The login form's states, as data.
 *
 * Kept out of the component because the interesting part of this screen is not its markup: it is that a
 * fresh install must not ask anyone to sign in with a credential that does not exist yet, that a pairing
 * code is only ever an entrance to a **registration**, and that a browser which cannot run a ceremony must
 * be told so instead of sitting on a button that can only fail. Those are branches, and a branch this file
 * can answer is a branch a unit test can pin — which is more than a `.svelte` file can promise.
 */
import type { PasskeySupport } from './webauthn';

/** Which of the form's bodies is on screen. */
type LoginStage =
    /** This install has a passkey: ask the browser for one. */
    | 'signin'
    /** Type the pairing code. The only entrance a fresh install has. */
    | 'code'
    /** A ceremony is running. Nothing else is clickable: two concurrent prompts on one authenticator is
     *  one prompt, and the person cannot tell which one they answered. */
    | 'ceremony'
    /** This browser cannot run a ceremony over this connection. */
    | 'unsupported';

/** The body a finished ceremony returns to, so a refused passkey lands the person back where they were. */
type LoginEntry = 'signin' | 'code';

/** How a message is shown: a dismissed prompt is a fact about what just happened, a refusal is a fault.
 *  Both are one line in the same place, and only the colour tells them apart. */
export type LoginTone = 'error' | 'note';

export interface LoginState {
    stage: LoginStage;
    /** Whether this install has any passkey. It decides the wording and the entrance, nothing else: the
     *  server answers the pairing question, and `null` means the session check could not read it. */
    hasPasskey: boolean | null;
    support: PasskeySupport;
    /** The body a ceremony returns to, kept so a failure is not a dead end. */
    entry: LoginEntry;
    /** The server's or the browser's own words, shown once. Cleared by every move. */
    error: string;
    tone: LoginTone;
    /** What is typed in the code field. */
    code: string;
}

export type LoginEvent =
    | { type: 'pair' }
    | { type: 'back' }
    | { type: 'code'; value: string }
    | { type: 'start' }
    | { type: 'error'; message: string; tone?: LoginTone };

/**
 * The body this browser starts on. `signin` is never the answer when there is no credential to offer, and
 * a pairing link is never answered by a sign-in: the code it carries can only buy a registration. A link is
 * the one arrival at the code body on a browser that cannot run the ceremony, so that body carries the
 * notice about the connection.
 */
function initialStage(hasPasskey: boolean | null, support: PasskeySupport, codeFromLink = ''): LoginStage {
    if (codeFromLink) return 'code';
    if (support !== 'available') return 'unsupported';
    return hasPasskey === false ? 'code' : 'signin';
}

/**
 * The form as it opens. `codeFromLink` is the code a pairing link carried, already read out of the address
 * (`pairingCodeFromHash`), empty when nobody arrived from a link.
 */
export function initialState(hasPasskey: boolean | null, support: PasskeySupport, codeFromLink = ''): LoginState {
    const stage = initialStage(hasPasskey, support, codeFromLink);
    const entry: LoginEntry = stage === 'code' ? 'code' : 'signin';
    return { stage, hasPasskey, support, entry, error: '', tone: 'error', code: formatPairingCode(codeFromLink) };
}

/**
 * One step of the machine. Total on purpose: every event is legal in every stage, and what it does is
 * either a move or nothing, so an event that arrives after a cancelled ceremony cannot strand the form.
 */
export function next(state: LoginState, event: LoginEvent): LoginState {
    switch (event.type) {
        case 'pair':
            return { ...state, stage: 'code', entry: 'code', error: '', tone: 'error', code: '' };
        case 'back':
            // On a fresh install the code field IS the form, and this is how a person who arrived from a
            // pairing link gets out of it: back to the sign-in body, where "I have a code" is the way back.
            return {
                ...state,
                stage: initialStage(state.hasPasskey, state.support),
                entry: initialStage(state.hasPasskey, state.support) === 'code' ? 'code' : 'signin',
                error: '',
                tone: 'error',
                code: '',
            };
        case 'code':
            // One shape in the state however the field was filled; a keystroke arrives grouped from
            // `pairingCodeInput`, and a pairing link carries the server's grouping already.
            return { ...state, code: formatPairingCode(event.value), error: '' };
        case 'start':
            return { ...state, stage: 'ceremony', entry: state.stage === 'code' ? 'code' : state.entry, error: '' };
        case 'error': {
            const tone = event.tone ?? 'error';
            if (state.stage !== 'ceremony') return { ...state, error: event.message, tone };
            return { ...state, stage: state.entry, error: event.message, tone };
        }
    }
}

/** How long a pairing code is once it is normalized. The server's `pairingCodeLength` is the source. */
const PAIRING_CODE_LENGTH = 8;

/** Where its two groups split. `FormatPairingCode` (`backend/auth/pairing.go`) prints the code the same
 *  way, so the field groups what the person is reading off the terminal. */
const PAIRING_CODE_GROUP = 4;

/**
 * What a typed code becomes before it is sent: upper case, spaces and dashes gone.
 *
 * `NormalizePairingCode` (`backend/auth/pairing.go`) owns this rule and applies it again on the way in, so
 * a browser that sent the bytes as typed would still be understood. The form normalizes for its own
 * reasons — the button lights when the eight characters are there, not when they are typed with a stray
 * space — and the two must agree, or the form sends something its own button called incomplete.
 */
export function normalizePairingCode(value: string): string {
    return (value ?? '').toUpperCase().replace(/[\s-]/g, '');
}

/** Whether the code field holds something worth spending a ceremony on. */
export function pairingCodeReady(value: string): boolean {
    return normalizePairingCode(value).length === PAIRING_CODE_LENGTH;
}

/** The one place the group break is written. */
function groupPairingCode(chars: string): string {
    return chars.length > PAIRING_CODE_GROUP
        ? `${chars.slice(0, PAIRING_CODE_GROUP)}-${chars.slice(PAIRING_CODE_GROUP)}`
        : chars;
}

/** What the code field holds for any text it was given. Display only: `normalizePairingCode` still
 *  decides what counts as a code, and the server applies its own rule again on the way in. */
function formatPairingCode(value: string): string {
    return groupPairingCode(normalizePairingCode(value).slice(0, PAIRING_CODE_LENGTH));
}

/**
 * The field's answer to a keystroke: the text it ends up holding, and where the caret sits in it.
 *
 * `value` and `caret` are the DOM's own, after the browser applied the edit. The caret is counted in code
 * characters rather than in positions, so the break the field inserts never sits under it. `backward` says
 * the keystroke was a backspace.
 */
export function pairingCodeInput(value: string, caret: number, backward = false): { value: string; caret: number } {
    const text = value ?? '';
    const at = Math.max(0, Math.min(caret ?? text.length, text.length));
    let chars = normalizePairingCode(text);
    let kept = Math.min(normalizePairingCode(text.slice(0, at)).length, chars.length);
    // A backspace whose only casualty is the break the field itself inserted: the break comes straight
    // back, so that keystroke takes the character before it instead of appearing to do nothing.
    if (backward && kept === PAIRING_CODE_GROUP && chars.length > PAIRING_CODE_GROUP) {
        chars = chars.slice(0, kept - 1) + chars.slice(kept);
        kept -= 1;
    }
    chars = chars.slice(0, PAIRING_CODE_LENGTH);
    kept = Math.min(kept, chars.length);
    return { value: groupPairingCode(chars), caret: kept + (kept > PAIRING_CODE_GROUP ? 1 : 0) };
}

/**
 * The code a pairing link carries, if this page was opened from one.
 *
 * The code rides in the **fragment** (`#pair=CODE`) because a query string reaches a proxy's access log
 * and a Referer header, and this is a credential until the ceremony spends it. That also means the server
 * never sees it here: only this module reads it, and the caller strips it from the address as soon as the
 * field holds it.
 */
export function pairingCodeFromHash(hash: string): string {
    const match = /^#pair=([A-Za-z0-9-]+)$/.exec(hash ?? '');
    return match ? match[1] : '';
}
