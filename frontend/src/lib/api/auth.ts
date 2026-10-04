import { API_BASE, apiFetch, apiError, isAborted, setCsrfToken, clearCsrfToken } from './helpers';
import { t } from '$lib/i18n';
import { creationOptions, requestOptions, credentialToJSON, passkeySupport } from '$lib/utils/webauthn';
import type { CredentialCreationJSON, CredentialRequestJSON } from '$lib/utils/webauthn';
import type { PairingCode, PasskeyInfo } from '$lib/types';

/**
 * Sign-in, and the passkeys that make it.
 *
 * A pairing code buys one **registration** and nothing else, so the first visit a browser makes is always
 * `register/begin` + `register/finish`, and every visit after it is an assertion. A session cookie is what
 * a *finished ceremony* earns — never the code, and never this module's word for "the code looked right".
 *
 * The session answer and the finished ceremony are the only two places the CSRF token is handed out, so it
 * is stored here: a component that had to remember it would break every later write, not just this screen.
 */

/** What `/api/auth/session` answers: this browser's own state, plus whether the install is paired. */
export interface SessionAnswer {
    authenticated: boolean;
    /** `null` when the server could not read it — the form then shows the sign-in branch, and the
     *  ceremony it starts answers for itself. */
    hasPasskey: boolean | null;
}

/** A ceremony the person stopped (they closed the prompt, or let it time out). Not a fault. */
class PasskeyCancelled extends Error {
    constructor(options?: ErrorOptions) {
        super(t('app.login.cancelled'), options);
    }
}

/** Whether a failure is "the person said no to the prompt", which is shown as a fact, never as an error. */
export function isPasskeyCancelled(err: unknown): boolean {
    return err instanceof PasskeyCancelled || (err as any)?.name === 'PasskeyCancelled';
}

/**
 * Whether this browser can run a ceremony at all, checked before anything is asked of the authenticator:
 * a prompt that cannot appear is not a failure the person can act on, and a pairing code spent on one is
 * a code that has to be minted again.
 */
function requirePasskeys(): void {
    const support = passkeySupport();
    if (support === 'insecure-context') throw new Error(t('app.login.insecureContext'));
    if (support !== 'available') throw new Error(t('app.login.notSupported'));
}

/**
 * Whether this browser's cookie names a live session, recording the CSRF token the answer carries.
 *
 * The flags in the body are the answer and the status is not — `handleAuthSession` answers 200 either way
 * — so an unexpected status is read rather than raised. First paint shows the login form for a signed-out
 * browser and keeps its offline branch for a body that is no answer at all.
 */
export async function checkSession(): Promise<SessionAnswer> {
    const res = await apiFetch(`${API_BASE}/auth/session`);
    const data = await res.json();
    const authenticated = data?.authenticated === true;
    if (authenticated && data.csrf_token) setCsrfToken(data.csrf_token);
    return { authenticated, hasPasskey: typeof data?.has_passkey === 'boolean' ? data.has_passkey : null };
}

/** One ceremony's first half: the browser's options, and the ticket the second half must carry back. */
interface CeremonyBegin {
    ticket: string;
    /** go-webauthn answers with the same envelope `navigator.credentials` takes: the options sit under
     *  `publicKey`, and only the bytes inside it are base64url. */
    options: { publicKey?: unknown };
}

/** The options of one ceremony, or the ceremony's own failure copy — an answer without them is a server
 *  that did not answer the contract, and the person needs a sentence, not a `TypeError`. */
function publicKeyOf(begin: CeremonyBegin, failure: string): unknown {
    const publicKey = begin?.options?.publicKey;
    if (typeof publicKey !== 'object' || publicKey === null) throw new Error(failure);
    return publicKey;
}

async function beginCeremony(path: string, body: unknown): Promise<CeremonyBegin> {
    const res = await apiFetch(`${API_BASE}${path}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body ?? {}),
    });
    if (!res.ok) throw await apiError(res, t('app.login.failed'));
    return res.json();
}

/**
 * One ceremony's second half: the browser's credential, verbatim. A finished one is what opens the
 * session, so the CSRF token the response header carries is stored here exactly as it was on the old
 * password login.
 */
async function finishCeremony(path: string, ticket: string, credential: PublicKeyCredential, name?: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}${path}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ticket, name: name ?? '', response: credentialToJSON(credential) }),
    });
    if (!res.ok) throw await apiError(res, t('app.login.failed'));
    const token = res.headers.get('X-CSRF-Token');
    if (token) setCsrfToken(token);
}

/**
 * Pair this browser: spend a pairing code on a new passkey, and open the session that registration
 * proves. The code is sent once, to `register/begin`, and never again — the ticket replaces it.
 */
export async function registerPasskeyWithCode(code: string, name = ''): Promise<void> {
    requirePasskeys();
    let begun: CeremonyBegin;
    try {
        begun = await beginCeremony('/auth/passkey/register/begin', { code });
    } catch (e) {
        if (isAborted(e)) throw e;
        throw new Error((e as Error)?.message || t('app.login.codeRejected'), { cause: e });
    }
    const created = await runCeremony(
        navigator.credentials.create({ publicKey: creationOptions(publicKeyOf(begun, t('app.login.registerFailed')) as CredentialCreationJSON) }),
        t('app.login.registerFailed'),
    );
    await finishCeremony('/auth/passkey/register/finish', begun.ticket, created, name);
}

/**
 * Sign in with a passkey this browser already holds. The server offers only the credentials minted for
 * this host, so a key from another address is never what the prompt asks for.
 */
export async function signInWithPasskey(): Promise<void> {
    requirePasskeys();
    const begun = await beginCeremony('/auth/passkey/assert/begin', {});
    const asserted = await runCeremony(
        navigator.credentials.get({ publicKey: requestOptions(publicKeyOf(begun, t('app.login.signInFailed')) as CredentialRequestJSON) }),
        t('app.login.signInFailed'),
    );
    await finishCeremony('/auth/passkey/assert/finish', begun.ticket, asserted);
}

/**
 * Hand the browser's options to the authenticator and wait. A prompt the person dismissed is
 * `PasskeyCancelled`, which the caller shows as a sentence rather than a fault; anything else is the
 * server's or the platform's own words, because both describe something the person can act on.
 */
async function runCeremony(pending: Promise<Credential | null>, failure: string): Promise<PublicKeyCredential> {
    try {
        const credential = await pending;
        if (!credential) throw new PasskeyCancelled();
        return credential as PublicKeyCredential;
    } catch (e) {
        if (e instanceof PasskeyCancelled || isAborted(e)) throw e;
        if ((e as any)?.name === 'NotAllowedError' || (e as any)?.name === 'AbortError') throw new PasskeyCancelled({ cause: e });
        throw new Error((e as Error)?.message || failure, { cause: e });
    }
}

/**
 * Close this browser's session and forget its CSRF token. A status that is not OK is not raised:
 * Settings → About revokes the current session and then runs this same path, and the caller's teardown
 * of this browser's own stores must not be skipped by the 401 that follows.
 */
export async function logout(): Promise<void> {
    await apiFetch(`${API_BASE}/auth/logout`, { method: 'POST' });
    clearCsrfToken();
}

/** Every device this install will sign in, oldest first. Labels and instants, no credential bytes. */
export async function listPasskeys(): Promise<PasskeyInfo[]> {
    const res = await apiFetch(`${API_BASE}/auth/passkeys`);
    if (!res.ok) throw await apiError(res, t('api.fetchPasskeys'));
    return res.json();
}

/** Relabel one device. The label is the only thing a person can recognise a row by. */
export async function renamePasskey(id: string, name: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/auth/passkeys/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
    });
    if (!res.ok) throw await apiError(res, t('api.renamePasskey'));
}

/**
 * Revoke one device. The last remaining passkey is refused with 409 and the server's reason — a GUI that
 * could reach zero credentials could lock a running server out of itself.
 */
export async function revokePasskey(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/auth/passkeys/${encodeURIComponent(id)}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.revokePasskey'));
}

/**
 * Mint a code that lets one more device register. It lives a few minutes, works once, and is answered
 * with everything the second device needs to be shown on screen or scanned off it.
 */
export async function mintPairingCode(): Promise<PairingCode> {
    const res = await apiFetch(`${API_BASE}/auth/pairing`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.mintPairingCode'));
    return res.json();
}

/** Stop every outstanding code. Registered passkeys are untouched: this is the "someone else may be
 *  about to use that code" action, not the emergency exit. */
export async function revokePairingCodes(): Promise<void> {
    const res = await apiFetch(`${API_BASE}/auth/pairing`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.revokePairingCodes'));
}
