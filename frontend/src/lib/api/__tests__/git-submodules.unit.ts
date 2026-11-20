import { describe, it, expect, vi, beforeEach } from 'vitest';

/**
 * The two submodule routes as the notice's own code calls them. `fetch` is stubbed, so no request leaves the
 * process, and the bodies are what the routes answer: a listing under `submodules`, and the click's three
 * lists beside each other.
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

const ROWS = [
    { path: 'vendor/lib', url: 'https://example.invalid/lib.git', populated: false, note: 'its copy was never fetched' },
    { path: 'vendor/other', url: 'https://example.invalid/other.git', populated: true, note: '' },
];

describe('getTaskSubmodules', () => {
    it('reads the task route, which is the listing the task was refused on', async () => {
        const { getTaskSubmodules } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, { submodules: ROWS }));

        const rows = await getTaskSubmodules('p1', 't1');

        expect(rows).toEqual(ROWS);
        const urls = mockFetch.mock.calls.map((call: any[]) => call[0]);
        expect(urls).toContain('/api/projects/p1/tasks/t1/submodules');
    });

    it('answers no rows at all for an empty listing, rather than an undefined list', async () => {
        const { getTaskSubmodules } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, {}));

        await expect(getTaskSubmodules('p1', 't1')).resolves.toEqual([]);
    });

    it('says which project it could not read, in the caller\'s sentence and not the server\'s', async () => {
        const { getTaskSubmodules } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(500, { error: 'read the project\'s submodules failed' }));

        await expect(getTaskSubmodules('p1', 't1')).rejects.toThrow(/submodules/i);
    });
});

describe('initProjectSubmodules', () => {
    it('asks the project route once, by POST, and keeps the three answers apart', async () => {
        const { initProjectSubmodules } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, {
            populated: ['vendor/lib'],
            reported: ['submodule vendor/lib was fetched from https://example.invalid/lib.git'],
            missing: [],
        }));

        const result = await initProjectSubmodules('p1');

        expect(result.populated).toEqual(['vendor/lib']);
        expect(result.missing).toEqual([]);
        expect(result.reported[0]).toContain('vendor/lib');
        const call = mockFetch.mock.calls.find((c: any[]) => String(c[0]).endsWith('/submodules/init'));
        expect(call?.[1]?.method).toBe('POST');
    });

    it('answers an empty result for a project whose checkout records nothing', async () => {
        const { initProjectSubmodules } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, {}));

        await expect(initProjectSubmodules('p1')).resolves.toEqual({ populated: [], reported: [], missing: [] });
    });
});
