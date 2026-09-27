import { describe, it, expect, vi, beforeEach } from 'vitest';

/**
 * The three auth routes are the only places the CSRF token is handed out, and the session check is the
 * one request in the app that gates first paint. Two readings are held here and nowhere else: an answer
 * this app did not expect is still an answer (an outage at boot offers the offline mirror, and a signed-out
 * browser gets the login form), and a logout whose session is already gone is a success.
 *
 * `fetch` is stubbed — no network. The module graph is reset per test, so each one starts with no token in
 * hand, exactly like a cold page.
 */

let mockFetch: ReturnType<typeof vi.fn>;
let reachabilityCalls: boolean[];

beforeEach(async () => {
    vi.resetModules();
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
    reachabilityCalls = [];
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

describe('checkSession', () => {
    it('answers from the body and records the CSRF token behind it', async () => {
        let sessionAnswers = 0;
        mockFetch.mockImplementation(async () => {
            sessionAnswers++;
            return res(200, { authenticated: true, csrf_token: 'session-token' });
        });

        const { checkSession } = await import('$lib/api/auth');
        const { ensureCsrfToken } = await import('$lib/api/helpers');

        await expect(checkSession()).resolves.toBe(true);
        // One GET for the probe. A safe method asks for no token, so nothing probes ahead of it.
        expect(sessionAnswers).toBe(1);
        // The token came from that answer, so reading it back spends no further request.
        await expect(ensureCsrfToken()).resolves.toBe('session-token');
        expect(sessionAnswers).toBe(1);
    });

    it('answers false for a signed-out browser without calling the server unreachable', async () => {
        mockFetch.mockResolvedValue(res(200, { authenticated: false }));

        const { checkSession } = await import('$lib/api/auth');
        const { ensureCsrfToken } = await import('$lib/api/helpers');

        await expect(checkSession()).resolves.toBe(false);
        await expect(ensureCsrfToken()).resolves.toBe('');
        // The answer is the evidence: a signed-out browser is a server that replied.
        expect(reachabilityCalls).toEqual([true]);
    });

    it('reads the flag off a status it did not expect, so first paint is not an outage', async () => {
        // The handler answers 200 whatever it knows, so a 401 or a 429 in front of it is still a
        // signed-out browser. The boot path's offline branch is for a server that never answered.
        mockFetch.mockResolvedValue(res(401, { error: 'not authenticated' }));

        const { checkSession } = await import('$lib/api/auth');

        await expect(checkSession()).resolves.toBe(false);
        expect(reachabilityCalls).toEqual([true]);
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

describe('login', () => {
    it('POSTs the password without a CSRF token and stores the one the response hands back', async () => {
        mockFetch.mockResolvedValue(res(200, { status: 'ok' }, { 'X-CSRF-Token': 'login-token' }));

        const { login } = await import('$lib/api/auth');
        const { ensureCsrfToken } = await import('$lib/api/helpers');

        await expect(login('hunter2')).resolves.toBeUndefined();
        expect(mockFetch).toHaveBeenCalledTimes(1);
        const [url, init] = mockFetch.mock.calls[0];
        expect(url).toBe('/api/auth/login');
        expect(init.method).toBe('POST');
        expect(init.credentials).toBe('include');
        expect(JSON.parse(init.body)).toEqual({ password: 'hunter2' });
        expect(headerOf(mockFetch.mock.calls[0], 'X-CSRF-Token')).toBeNull();
        await expect(ensureCsrfToken()).resolves.toBe('login-token');
    });

    it('throws the server\'s own words for a refused password', async () => {
        mockFetch.mockResolvedValue(res(401, { error: 'invalid password' }));

        const { login } = await import('$lib/api/auth');
        await expect(login('wrong')).rejects.toThrow('invalid password');
    });

    it('falls back to the form\'s own copy when a failure names nothing', async () => {
        mockFetch.mockResolvedValue(res(500, {}));

        const { login } = await import('$lib/api/auth');
        await expect(login('hunter2')).rejects.toThrow('Login failed');
    });

    it('answers a request that got no reply with the form\'s network copy, and reports the server unreachable', async () => {
        rejectConnection();

        const { login } = await import('$lib/api/auth');
        await expect(login('hunter2')).rejects.toThrow('Network error');
        expect(reachabilityCalls).toEqual([false]);
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
