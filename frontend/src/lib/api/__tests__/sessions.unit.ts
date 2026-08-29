import { describe, it, expect, vi, beforeEach } from 'vitest';

// Deterministic: fetch is stubbed, no network. The CSRF token is served by the
// stubbed /api/auth/session endpoint, exactly like the sibling suites do.

let mockFetch: ReturnType<typeof vi.fn>;

beforeEach(() => {
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
});

/** API calls only — drops the CSRF bootstrap call to /api/auth/session. */
function apiCalls(calls: any[][]): any[][] {
    return calls.filter((c) => typeof c[0] === 'string' && !c[0].includes('/api/auth/session'));
}

/** Stub a successful response; /api/auth/session always yields a CSRF token. */
function mockResponse(status: number, data: any) {
    mockFetch.mockImplementation(async (url: any) => {
        if (typeof url === 'string' && url.includes('/api/auth/session')) {
            return {
                ok: true,
                status: 200,
                json: async () => ({ authenticated: true, csrf_token: 'test-token' }),
            };
        }
        return {
            ok: status >= 200 && status < 300,
            status,
            json: async () => data,
        };
    });
}

/** Read one header of an API call (case-insensitively, via Headers). */
function headerOf(call: any[], name: string): string | null {
    return new Headers(call[1]?.headers).get(name);
}

describe('sessions API', () => {
    it('listSessions GETs /api/sessions and returns the rows', async () => {
        const sessions = [
            {
                id: 'sess-1',
                created_at: '2026-08-29T10:00:00Z',
                last_seen: '2026-08-29T10:05:00Z',
                expires_at: '2026-09-28T10:00:00Z',
                ip: '127.0.0.1',
                user_agent: 'Mozilla/5.0',
                current: true,
            },
        ];
        mockResponse(200, sessions);

        const { listSessions } = await import('$lib/api/sessions');
        const result = await listSessions();

        expect(result).toEqual(sessions);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls).toHaveLength(1);
        expect(calls[0][0]).toBe('/api/sessions');
        const init = calls[0][1] || {};
        expect(init.method ?? 'GET').not.toBe('POST');
        // Credentials ride apiFetch, and the CSRF header is attached.
        expect(init.credentials).toBe('include');
        expect(headerOf(calls[0], 'X-CSRF-Token')).toBe('test-token');
    });

    it('revokeSession DELETEs /api/sessions/{id} with the CSRF header', async () => {
        mockResponse(204, null);

        const { revokeSession } = await import('$lib/api/sessions');
        await expect(revokeSession('sess-2')).resolves.toBeUndefined();

        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toBe('/api/sessions/sess-2');
        expect(calls[0][1].method).toBe('DELETE');
        expect(calls[0][1].credentials).toBe('include');
        expect(headerOf(calls[0], 'X-CSRF-Token')).toBe('test-token');
    });

    it('revokeSession URL-encodes the id', async () => {
        mockResponse(204, null);

        const { revokeSession } = await import('$lib/api/sessions');
        await revokeSession('a b/c');

        expect(apiCalls(mockFetch.mock.calls)[0][0]).toBe('/api/sessions/a%20b%2Fc');
    });

    it('revokeSession throws the server error message on failure', async () => {
        mockResponse(404, { error: 'session not found' });

        const { revokeSession } = await import('$lib/api/sessions');
        await expect(revokeSession('gone')).rejects.toThrow('session not found');
    });

    it('revokeOtherSessions POSTs /api/sessions/revoke-others and returns the count', async () => {
        mockResponse(200, { revoked: 3 });

        const { revokeOtherSessions } = await import('$lib/api/sessions');
        await expect(revokeOtherSessions()).resolves.toEqual({ revoked: 3 });

        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toBe('/api/sessions/revoke-others');
        expect(calls[0][1].method).toBe('POST');
        expect(calls[0][1].credentials).toBe('include');
        expect(headerOf(calls[0], 'X-CSRF-Token')).toBe('test-token');
    });
});
