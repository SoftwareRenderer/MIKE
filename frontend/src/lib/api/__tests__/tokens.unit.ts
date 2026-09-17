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

describe('API tokens', () => {
    it('listAPITokens GETs /api/tokens and returns the rows', async () => {
        const tokens = [
            { id: 'tok-1', label: 'nightly importer', created_at: '2026-09-01T10:00:00Z', last_used_at: '' },
        ];
        mockResponse(200, tokens);

        const { listAPITokens } = await import('$lib/api/tokens');
        const result = await listAPITokens();

        expect(result).toEqual(tokens);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls).toHaveLength(1);
        expect(calls[0][0]).toBe('/api/tokens');
        expect(calls[0][1]?.method ?? 'GET').not.toBe('POST');
        expect(calls[0][1]?.credentials).toBe('include');
    });

    it('createAPIToken POSTs the label and hands back the secret the server just made', async () => {
        const created = {
            id: 'tok-1',
            label: 'nightly importer',
            created_at: '2026-09-01T10:00:00Z',
            last_used_at: '',
            token: 'mike_0123',
        };
        mockResponse(201, created);

        const { createAPIToken } = await import('$lib/api/tokens');
        await expect(createAPIToken('nightly importer')).resolves.toEqual(created);

        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toBe('/api/tokens');
        expect(calls[0][1].method).toBe('POST');
        expect(JSON.parse(calls[0][1].body)).toEqual({ label: 'nightly importer' });
        expect(headerOf(calls[0], 'X-CSRF-Token')).toBe('test-token');
    });

    it('revokeAPIToken DELETEs /api/tokens/{id}', async () => {
        mockResponse(204, null);

        const { revokeAPIToken } = await import('$lib/api/tokens');
        await expect(revokeAPIToken('tok-1')).resolves.toBeUndefined();

        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toBe('/api/tokens/tok-1');
        expect(calls[0][1].method).toBe('DELETE');
        expect(headerOf(calls[0], 'X-CSRF-Token')).toBe('test-token');
    });

    it('revokeAPIToken URL-encodes the id', async () => {
        mockResponse(204, null);

        const { revokeAPIToken } = await import('$lib/api/tokens');
        await revokeAPIToken('a b/c');

        expect(apiCalls(mockFetch.mock.calls)[0][0]).toBe('/api/tokens/a%20b%2Fc');
    });

    it('reports the server sentence rather than the transport status', async () => {
        mockResponse(400, { error: 'label is required' });

        const { createAPIToken } = await import('$lib/api/tokens');
        await expect(createAPIToken('')).rejects.toThrow('label is required');
    });
});
