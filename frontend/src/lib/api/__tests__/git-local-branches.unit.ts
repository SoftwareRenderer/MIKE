import { describe, it, expect, vi, beforeEach } from 'vitest';

/**
 * Two branch pickers read one rule: the choices are local branches, so a remote-tracking ref and the
 * detached `HEAD` line never reach a person. The order the server returned survives — a branch list
 * never follows the UI locale.
 *
 * `fetch` is stubbed — no network.
 */

let mockFetch: ReturnType<typeof vi.fn>;

beforeEach(() => {
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
});

function res(status: number, body: any) {
    return {
        ok: status >= 200 && status < 300,
        status,
        headers: new Headers(),
        json: async () => body,
    };
}

describe('getLocalBranches', () => {
    it('drops remote-tracking refs and HEAD and keeps the server order', async () => {
        const { getLocalBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, [
            'main', 'origin/main', 'feature/x', 'HEAD', 'origin/HEAD', 'zeta',
        ]));

        const branches = await getLocalBranches('p1');

        expect(branches).toEqual(['main', 'feature/x', 'zeta']);
    });

    it('asks the branches route of the project it was given', async () => {
        const { getLocalBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, []));

        await getLocalBranches('p1');

        // Importing the api layer may ask for a session first, so look for this request among the calls.
        const urls = mockFetch.mock.calls.map((call: any[]) => call[0]);
        expect(urls).toContain('/api/projects/p1/git/branches');
    });

    it('rejects when the route answers badly, so a caller keeps an empty picker', async () => {
        const { getLocalBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(500, { error: 'boom' }));

        await expect(getLocalBranches('p1')).rejects.toThrow();
    });
});
