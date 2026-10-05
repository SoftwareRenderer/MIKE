import { describe, it, expect, vi, beforeEach } from 'vitest';

/**
 * The auth routes: the session check that gates first paint, the two ceremonies that open a session, and
 * the settings calls that manage the credentials behind them. Three readings are held here and nowhere
 * else:
 *
 *  - an answer this app did not expect is still an answer (an outage at boot offers the offline mirror, and
 *    a signed-out browser gets the login form);
 *  - a pairing code reaches the server exactly once, on the request that begins the registration — the
 *    ticket replaces it, so a browser that kept sending the code would be a browser that could pair twice;
 *  - a finished ceremony is the only thing that hands out the CSRF token outside the session check.
 *
 * `fetch` and the browser's own WebAuthn surface are stubbed — no network, no authenticator. The module
 * graph is reset per test, so each one starts with no token in hand, exactly like a cold page.
 */

let mockFetch: ReturnType<typeof vi.fn>;
let reachabilityCalls: boolean[];
let createCall: ReturnType<typeof vi.fn>;
let getCall: ReturnType<typeof vi.fn>;

/**
 * What this browser's passkey surface looks like.
 *
 * `passkeySupport` reads three browser facts, and each is a different branch of the login form, so they
 * are named here rather than inherited from whatever the test environment happens to provide.
 */
function setBrowser(options: { secure?: boolean; api?: boolean } = {}): void {
    const { secure = true, api = true } = options;
    Object.defineProperty(window, 'isSecureContext', { value: secure, configurable: true });
    Object.defineProperty(window, 'PublicKeyCredential', { value: api ? function () { } : undefined, configurable: true });
    Object.defineProperty(navigator, 'credentials', { value: api ? { create: createCall, get: getCall } : undefined, configurable: true });
}

beforeEach(async () => {
    vi.resetModules();
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
    reachabilityCalls = [];
    createCall = vi.fn();
    getCall = vi.fn();
    setBrowser();
    const helpers = await import('$lib/api/helpers');
    helpers.registerSetHttpReachable((v: boolean) => reachabilityCalls.push(v));
});

/** One answer, shaped like the `Response` `apiFetch` hands back. */
function res(status: number, body: any, headers: Record<string, string> = {}) {
    return {
        ok: status >= 200 && status < 300,
        status,
        headers: new Headers(headers),
        json: async () => body,
    };
}

/** A transport failure: the request never reached the server. */
function rejectConnection() {
    mockFetch.mockRejectedValue(Object.assign(new Error('Failed to fetch'), { name: 'TypeError' }));
}

function headerOf(call: any[], name: string): string | null {
    return new Headers(call[1]?.headers).get(name);
}

function bodyOf(call: any[]): any {
    return JSON.parse(call[1]?.body);
}

/** The call that went to one path, whatever else was asked for in between. */
function callTo(path: string): any[] {
    const found = mockFetch.mock.calls.find((c: any[]) => String(c[0]).endsWith(path));
    if (!found) throw new Error(`no request was made to ${path}`);
    return found;
}

/** The creation options the server sends, in the JSON form the ceremony routes answer with. */
const creationJSON = {
    challenge: 'AQID',
    rp: { id: 'localhost', name: 'MIKE' },
    user: { id: 'BAQFBg', name: 'mike', displayName: 'mike' },
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
    authenticatorSelection: { userVerification: 'required', residentKey: 'required' },
};

const requestJSON = {
    challenge: 'BAUG',
    rpId: 'localhost',
    userVerification: 'required',
    allowCredentials: [{ type: 'public-key', id: 'Zm9v' }],
};

/** What the browser hands back after a ceremony: the wire form, which is what gets POSTed verbatim. */
function credential(json: unknown) {
    return { toJSON: () => json } as unknown as Credential;
}

describe('checkSession', () => {
    it('answers from the body and records the CSRF token behind it', async () => {
        let sessionAnswers = 0;
        mockFetch.mockImplementation(async () => {
            sessionAnswers++;
            return res(200, { authenticated: true, has_passkey: true, tls_ca_available: true, csrf_token: 'session-token' });
        });

        const { checkSession } = await import('$lib/api/auth');
        const { ensureCsrfToken } = await import('$lib/api/helpers');

        // `tls_ca_available` is the pairing notice's other branch: whether the CA download would answer.
        await expect(checkSession()).resolves.toEqual({ authenticated: true, hasPasskey: true, caDownload: true, deviceName: '' });
        // One GET for the probe. A safe method asks for no token, so nothing probes ahead of it.
        expect(sessionAnswers).toBe(1);
        // The token came from that answer, so reading it back spends no further request.
        await expect(ensureCsrfToken()).resolves.toBe('session-token');
        expect(sessionAnswers).toBe(1);
    });

    it('answers false for a signed-out browser without calling the server unreachable', async () => {
        mockFetch.mockResolvedValue(res(200, { authenticated: false, has_passkey: false }));

        const { checkSession } = await import('$lib/api/auth');
        const { ensureCsrfToken } = await import('$lib/api/helpers');

        await expect(checkSession()).resolves.toEqual({ authenticated: false, hasPasskey: false, caDownload: false, deviceName: '' });
        await expect(ensureCsrfToken()).resolves.toBe('');
        // The answer is the evidence: a signed-out browser is a server that replied.
        expect(reachabilityCalls).toEqual([true]);
    });

    it('reads the flag off a status it did not expect, so first paint is not an outage', async () => {
        // The handler answers 200 whatever it knows, so a 401 or a 429 in front of it is still a
        // signed-out browser. The boot path's offline branch is for a server that never answered.
        mockFetch.mockResolvedValue(res(401, { error: 'not authenticated' }));

        const { checkSession } = await import('$lib/api/auth');

        await expect(checkSession()).resolves.toEqual({ authenticated: false, hasPasskey: null, caDownload: false, deviceName: '' });
        expect(reachabilityCalls).toEqual([true]);
    });

    it('carries the name this browser reads as, which the pairing form opens with', async () => {
        // The server reads the User-Agent of this very request, so the form's guess and its fallback are
        // one reading.
        mockFetch.mockResolvedValue(res(200, { authenticated: false, device_name: 'Chrome 120 · Macintosh' }));

        const { checkSession } = await import('$lib/api/auth');

        await expect(checkSession()).resolves.toMatchObject({ deviceName: 'Chrome 120 · Macintosh' });
    });

    it('says nothing about passkeys when the answer says nothing', async () => {
        // `has_passkey` decides whether the form asks for a passkey or for a pairing code. A body without
        // it is not "no passkeys" — the form takes the branch a failed ceremony corrects.
        mockFetch.mockResolvedValue(res(200, { authenticated: false }));

        const { checkSession } = await import('$lib/api/auth');

        // No flag is "this server offers no CA download", which is the safe reading: the notice then
        // says nothing rather than linking at a file that would not be served.
        await expect(checkSession()).resolves.toEqual({ authenticated: false, hasPasskey: null, caDownload: false, deviceName: '' });
    });

    it('throws on a body that is not the answer, the boot path\'s outage branch', async () => {
        mockFetch.mockResolvedValue({
            ok: true,
            status: 200,
            headers: new Headers(),
            json: async () => { throw new SyntaxError('Unexpected token < in JSON at position 0'); },
        });

        const { checkSession } = await import('$lib/api/auth');
        await expect(checkSession()).rejects.toThrow();
    });
});

describe('registerPasskeyWithCode', () => {
    it('spends the code on the registration, then hands the browser\'s credential back verbatim', async () => {
        createCall.mockResolvedValue(credential({ id: 'X', rawId: 'X' }));
        mockFetch
            .mockResolvedValueOnce(res(200, { ticket: 'ticket-1', options: { publicKey: creationJSON } }))
            .mockResolvedValueOnce(res(200, { status: 'ok' }, { 'X-CSRF-Token': 'ceremony-token' }));

        const { registerPasskeyWithCode } = await import('$lib/api/auth');
        const { ensureCsrfToken } = await import('$lib/api/helpers');

        await expect(registerPasskeyWithCode('ABCD-2345')).resolves.toBeUndefined();

        const begin = callTo('/auth/passkey/register/begin');
        expect(begin[1].method).toBe('POST');
        expect(bodyOf(begin)).toEqual({ code: 'ABCD-2345' });
        // Public routes with no session yet: no token, and no round trip asking for one.
        expect(headerOf(begin, 'X-CSRF-Token')).toBeNull();
        expect(mockFetch.mock.calls.filter((c: any[]) => String(c[0]).endsWith('/auth/session'))).toHaveLength(0);

        // The options are handed to the browser as bytes, not as the base64url text the server sent.
        const asked = createCall.mock.calls[0][0].publicKey;
        expect(new Uint8Array(asked.challenge)).toEqual(new Uint8Array([1, 2, 3]));
        expect(new Uint8Array(asked.user.id)).toEqual(new Uint8Array([4, 4, 5, 6]));
        expect(asked.authenticatorSelection.userVerification).toBe('required');

        const finish = callTo('/auth/passkey/register/finish');
        expect(bodyOf(finish)).toEqual({ ticket: 'ticket-1', name: '', response: { id: 'X', rawId: 'X' } });
        // The code is spent: the second half carries the ticket and never the code.
        expect(JSON.stringify(bodyOf(finish))).not.toContain('ABCD-2345');
        // A finished registration is the session, and the session's token rides the same response.
        await expect(ensureCsrfToken()).resolves.toBe('ceremony-token');
    });

    it('sends the name the person gave this device, and only to the registration', async () => {
        createCall.mockResolvedValue(credential({ id: 'X', rawId: 'X' }));
        mockFetch
            .mockResolvedValueOnce(res(200, { ticket: 'ticket-1', options: { publicKey: creationJSON } }))
            .mockResolvedValueOnce(res(200, { status: 'ok' }, { 'X-CSRF-Token': 'ceremony-token' }));

        const { registerPasskeyWithCode } = await import('$lib/api/auth');

        await registerPasskeyWithCode('ABCD-2345', 'Kitchen tablet');

        // The code half of the ceremony still carries only the code.
        expect(bodyOf(callTo('/auth/passkey/register/begin'))).toEqual({ code: 'ABCD-2345' });
        expect(bodyOf(callTo('/auth/passkey/register/finish'))).toEqual({
            ticket: 'ticket-1', name: 'Kitchen tablet', response: { id: 'X', rawId: 'X' },
        });
    });

    it('refuses a code the server refused without waking the authenticator', async () => {
        mockFetch.mockResolvedValueOnce(res(401, { error: 'pairing code is not valid' }));

        const { registerPasskeyWithCode } = await import('$lib/api/auth');

        await expect(registerPasskeyWithCode('XXXX-XXXX')).rejects.toThrow('pairing code is not valid');
        expect(createCall).not.toHaveBeenCalled();
        expect(mockFetch.mock.calls).toHaveLength(1);
    });

    it('reports a prompt the person dismissed as a dismissal, not as a failure', async () => {
        createCall.mockRejectedValue(Object.assign(new Error('The operation timed out.'), { name: 'NotAllowedError' }));
        mockFetch.mockResolvedValueOnce(res(200, { ticket: 'ticket-1', options: { publicKey: creationJSON } }));

        const { isPasskeyCancelled, registerPasskeyWithCode } = await import('$lib/api/auth');

        const err = await registerPasskeyWithCode('ABCD-2345').then(() => null, (e: any) => e);
        expect(isPasskeyCancelled(err)).toBe(true);
        expect(err.message).toBe('The passkey prompt was dismissed');
    });

    it('says what to do when the browser will not run the prompt, without quoting the browser', async () => {
        const refused = Object.assign(new Error('The operation is insecure.'), { name: 'NotSupportedError' });
        createCall.mockRejectedValue(refused);
        mockFetch.mockResolvedValueOnce(res(200, { ticket: 'ticket-1', options: { publicKey: creationJSON } }));
        const logged = vi.spyOn(console, 'error').mockImplementation(() => {});

        const { isPasskeyCancelled, registerPasskeyWithCode } = await import('$lib/api/auth');

        const err = await registerPasskeyWithCode('ABCD-2345').then(() => null, (e: any) => e);
        expect(isPasskeyCancelled(err)).toBe(false);
        expect(err.message).toContain('refused to run the passkey prompt');
        expect(err.message).not.toContain('insecure');
        expect(err.cause).toBe(refused);
        expect(logged).toHaveBeenCalled();
        logged.mockRestore();
    });

    it('asks nothing of a browser that cannot run a ceremony', async () => {
        // Plain HTTP on a LAN address has no prompt to show, and a pairing code spent there is a code
        // that has to be minted again once the person reaches https.
        setBrowser({ secure: false });

        const { registerPasskeyWithCode } = await import('$lib/api/auth');

        await expect(registerPasskeyWithCode('ABCD-2345')).rejects.toThrow('does not treat this connection as secure');
        expect(mockFetch).not.toHaveBeenCalled();
    });
});

describe('signInWithPasskey', () => {
    it('asserts against the options the server offered and opens the session it verified', async () => {
        getCall.mockResolvedValue(credential({ id: 'Y', rawId: 'Y' }));
        mockFetch
            .mockResolvedValueOnce(res(200, { ticket: 'ticket-2', options: { publicKey: requestJSON } }))
            .mockResolvedValueOnce(res(200, { status: 'ok' }, { 'X-CSRF-Token': 'assert-token' }));

        const { signInWithPasskey } = await import('$lib/api/auth');

        await expect(signInWithPasskey()).resolves.toBeUndefined();
        expect(callTo('/auth/passkey/assert/begin')[1].method).toBe('POST');

        const asked = getCall.mock.calls[0][0].publicKey;
        expect(new Uint8Array(asked.challenge)).toEqual(new Uint8Array([4, 5, 6]));
        expect(asked.rpId).toBe('localhost');
        expect(new Uint8Array(asked.allowCredentials[0].id)).toEqual(new Uint8Array([102, 111, 111]));

        expect(bodyOf(callTo('/auth/passkey/assert/finish'))).toEqual({
            ticket: 'ticket-2', name: '', response: { id: 'Y', rawId: 'Y' },
        });
    });

    it('throws the server\'s own words when the credential does not verify', async () => {
        getCall.mockResolvedValue(credential({ id: 'Y', rawId: 'Y' }));
        mockFetch
            .mockResolvedValueOnce(res(200, { ticket: 'ticket-2', options: { publicKey: requestJSON } }))
            .mockResolvedValueOnce(res(401, { error: 'the passkey did not verify' }));

        const { signInWithPasskey } = await import('$lib/api/auth');

        await expect(signInWithPasskey()).rejects.toThrow('the passkey did not verify');
    });

    it('says so when this browser has no passkey API at all', async () => {
        setBrowser({ api: false });

        const { signInWithPasskey } = await import('$lib/api/auth');

        await expect(signInWithPasskey()).rejects.toThrow('This browser cannot create a passkey');
        expect(mockFetch).not.toHaveBeenCalled();
    });

    it('answers a request that got no reply as a connection failure', async () => {
        rejectConnection();

        const { signInWithPasskey } = await import('$lib/api/auth');

        await expect(signInWithPasskey()).rejects.toThrow('Connection issue');
        expect(reachabilityCalls).toEqual([false]);
    });
});

describe('the device list', () => {
    it('reads the rows the settings section paints', async () => {
        const rows = [{ id: 'cred-1', name: 'Chrome · macOS', created_at: '2026-10-01T10:00:00Z', last_used: '' }];
        mockFetch.mockResolvedValue(res(200, rows));

        const { listPasskeys } = await import('$lib/api/auth');

        await expect(listPasskeys()).resolves.toEqual(rows);
    });

    it('sends the label and the token on a rename, and shows the server\'s reason when it is refused', async () => {
        mockFetch.mockResolvedValue(res(400, { error: 'name must be 1 to 64 characters' }));

        const { renamePasskey } = await import('$lib/api/auth');
        const { setCsrfToken } = await import('$lib/api/helpers');
        setCsrfToken('seed-token');

        await expect(renamePasskey('cred/1', 'My laptop')).rejects.toThrow('name must be 1 to 64 characters');
        const call = callTo('/auth/passkeys/cred%2F1');
        expect(call[1].method).toBe('PATCH');
        expect(bodyOf(call)).toEqual({ name: 'My laptop' });
        expect(headerOf(call, 'X-CSRF-Token')).toBe('seed-token');
    });

    it('surfaces the last-passkey refusal instead of hiding it', async () => {
        // The one refusal the person cannot act on by trying again, so it must reach the screen.
        mockFetch.mockResolvedValue(res(409, { error: 'this is the last passkey; pair another device first' }));

        const { revokePasskey } = await import('$lib/api/auth');

        await expect(revokePasskey('cred-1')).rejects.toThrow('pair another device first');
        expect(callTo('/auth/passkeys/cred-1')[1].method).toBe('DELETE');
    });

    it('hands back the minted code with everything a second device needs', async () => {
        const code = { code: 'ABCD-2345', url: 'https://mike.local:3000/#pair=ABCD-2345', qr_png: 'data:image/png;base64,AA', expires_at: '2026-10-03T10:05:00Z' };
        mockFetch.mockResolvedValue(res(200, code));

        const { mintPairingCode } = await import('$lib/api/auth');

        await expect(mintPairingCode()).resolves.toEqual(code);
        expect(callTo('/auth/pairing')[1].method).toBe('POST');
        // A mint with nothing typed sends an empty name.
        expect(bodyOf(callTo('/auth/pairing'))).toEqual({ name: '' });
    });

    it('sends the name for the device being paired, which rides the link it answers with', async () => {
        mockFetch.mockResolvedValue(res(200, { code: 'ABCD-2345', url: 'https://mike.local:3000/#pair=ABCD2345&name=Kitchen+tablet' }));

        const { mintPairingCode } = await import('$lib/api/auth');

        await mintPairingCode('Kitchen tablet');
        expect(bodyOf(callTo('/auth/pairing'))).toEqual({ name: 'Kitchen tablet' });
    });

    it('cancels every outstanding code on the verb the route declares', async () => {
        mockFetch.mockResolvedValue(res(200, { revoked: true }));

        const { revokePairingCodes } = await import('$lib/api/auth');

        await expect(revokePairingCodes()).resolves.toBeUndefined();
        expect(callTo('/auth/pairing')[1].method).toBe('DELETE');
    });
});

describe('logout', () => {
    it('POSTs with the CSRF token, then forgets it', async () => {
        mockFetch.mockImplementation(async (url: any) => typeof url === 'string' && url.includes('/api/auth/session')
            ? res(200, { authenticated: false })
            : res(200, { status: 'ok' }));

        const { logout } = await import('$lib/api/auth');
        const { setCsrfToken, ensureCsrfToken } = await import('$lib/api/helpers');
        setCsrfToken('seed-token');

        await expect(logout()).resolves.toBeUndefined();
        const call = mockFetch.mock.calls.find((c: any[]) => String(c[0]).endsWith('/api/auth/logout'));
        expect(call?.[0]).toBe('/api/auth/logout');
        expect(call?.[1]?.method).toBe('POST');
        expect(call?.[1]?.credentials).toBe('include');
        expect(headerOf(call!, 'X-CSRF-Token')).toBe('seed-token');
        // Cleared: reading the token now asks the server, which says nothing.
        await expect(ensureCsrfToken()).resolves.toBe('');
    });

    it('settles when the session is already gone, because the teardown still has to run', async () => {
        // Settings → About revokes the current session and then signs out, so this request 401s on
        // purpose. Throwing would skip the caller's clearQueue() and clearAll().
        mockFetch.mockResolvedValue(res(401, { error: 'invalid session' }));

        const { logout } = await import('$lib/api/auth');
        await expect(logout()).resolves.toBeUndefined();
    });

    it('propagates a request that got no reply and keeps the token it still needs', async () => {
        rejectConnection();

        const { logout } = await import('$lib/api/auth');
        const { setCsrfToken, ensureCsrfToken } = await import('$lib/api/helpers');
        setCsrfToken('seed-token');

        await expect(logout()).rejects.toThrow('Connection issue');
        await expect(ensureCsrfToken()).resolves.toBe('seed-token');
    });
});

describe('the CSRF token travels on unsafe methods only', () => {
    const probes = () => mockFetch.mock.calls
        .filter((c: any[]) => String(c[0]).includes('/api/auth/session')).length;

    it('asks for no token on a GET, so the request is the only one made', async () => {
        mockFetch.mockResolvedValue(res(200, []));

        const { apiFetch } = await import('$lib/api/helpers');
        await apiFetch('/api/projects');

        expect(mockFetch.mock.calls.length).toBe(1);
        expect(headerOf(mockFetch.mock.calls[0]!, 'X-CSRF-Token')).toBeNull();
        expect(probes()).toBe(0);
    });

    it('treats an explicit GET, a lowercase one and a HEAD as safe', async () => {
        mockFetch.mockResolvedValue(res(200, []));

        const { apiFetch } = await import('$lib/api/helpers');
        await apiFetch('/api/projects', { method: 'GET' });
        await apiFetch('/api/projects', { method: 'get' });
        await apiFetch('/api/projects', { method: 'HEAD' });

        expect(probes()).toBe(0);
        for (const call of mockFetch.mock.calls) expect(headerOf(call, 'X-CSRF-Token')).toBeNull();
    });

    it('probes once for a POST with no token in hand, and not again for the next', async () => {
        mockFetch.mockImplementation(async (url: any) => String(url).includes('/api/auth/session')
            ? res(200, { authenticated: true, csrf_token: 'session-token' })
            : res(200, { status: 'ok' }));

        const { apiFetch } = await import('$lib/api/helpers');
        await apiFetch('/api/projects/p1/tasks', { method: 'POST', body: '{}' });

        const write = mockFetch.mock.calls.find((c: any[]) => String(c[0]).endsWith('/tasks'))!;
        expect(headerOf(write, 'X-CSRF-Token')).toBe('session-token');
        expect(probes()).toBe(1);

        await apiFetch('/api/projects/p1/tasks', { method: 'POST', body: '{}' });
        expect(probes(), 'a write found no token already in hand').toBe(1);
    });

    it('carries the token on the streams that build their own headers', async () => {
        const { setCsrfToken, csrfHeaders } = await import('$lib/api/helpers');
        setCsrfToken('seed-token');

        // Every caller of this is a POST, so the default asks for the token; naming a safe method does not.
        expect((await csrfHeaders()).get('X-CSRF-Token')).toBe('seed-token');
        expect((await csrfHeaders({}, 'GET')).get('X-CSRF-Token')).toBeNull();
    });
});
