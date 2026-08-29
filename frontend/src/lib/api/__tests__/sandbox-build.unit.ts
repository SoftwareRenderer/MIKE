import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// The CSRF lookup hits the session endpoint — stub it, keep everything else.
const { ensureCsrfToken } = vi.hoisted(() => ({ ensureCsrfToken: vi.fn(async () => 'csrf-token') }));
vi.mock('../helpers', async (importOriginal) => {
    const actual = await importOriginal<typeof import('../helpers')>();
    return { ...actual, ensureCsrfToken };
});

import { buildSandboxImageStream } from '../queue';

/** Minimal SSE Response whose single chunk carries the given data frames. */
function sseResponse(frames: string[]): Response {
    const payload = new TextEncoder().encode(frames.map(f => `data: ${f}\n\n`).join(''));
    let delivered = false;
    return {
        ok: true,
        body: {
            getReader: () => ({
                read: async () => {
                    if (delivered) return { done: true, value: undefined };
                    delivered = true;
                    return { done: false, value: payload };
                },
            }),
        },
    } as unknown as Response;
}

const RESULT = '{"status":"built","image":"proj-img","project":"p1"}';

describe('buildSandboxImageStream no-cache modifier', () => {
    let fetchMock: ReturnType<typeof vi.fn>;

    beforeEach(() => {
        fetchMock = vi.fn(async () => sseResponse(['Building sandbox image from Dockerfile...', RESULT]));
        vi.stubGlobal('fetch', fetchMock);
    });

    afterEach(() => {
        vi.unstubAllGlobals();
    });

    function requestedUrl(): string {
        expect(fetchMock).toHaveBeenCalledTimes(1);
        return fetchMock.mock.calls[0][0] as string;
    }

    it('omits the no_cache param by default', async () => {
        const res = await buildSandboxImageStream('p1', () => { });
        expect(res.status).toBe('built');
        expect(requestedUrl()).not.toContain('no_cache');
    });

    it('omits the no_cache param when explicitly disabled', async () => {
        await buildSandboxImageStream('p1', () => { }, { noCache: false });
        expect(requestedUrl()).not.toContain('no_cache');
    });

    it('appends no_cache=1 when the cache is disabled', async () => {
        await buildSandboxImageStream('p1', () => { }, { noCache: true });
        expect(requestedUrl()).toMatch(/\/projects\/p1\/sandbox\/build\?no_cache=1$/);
    });

    it('still streams plain text lines to the callback', async () => {
        const lines: string[] = [];
        await buildSandboxImageStream('p1', (l) => lines.push(l), { noCache: true });
        expect(lines).toEqual(['Building sandbox image from Dockerfile...']);
    });

    it('posts with credentials and the CSRF header', async () => {
        await buildSandboxImageStream('p1', () => { }, { noCache: true });
        const init = fetchMock.mock.calls[0][1] as RequestInit;
        expect(init.method).toBe('POST');
        expect(init.credentials).toBe('include');
        expect((init.headers as Headers).get('X-CSRF-Token')).toBe('csrf-token');
    });
});
