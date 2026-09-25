import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// The CSRF lookup hits the session endpoint — stub it, keep everything else.
const { ensureCsrfToken } = vi.hoisted(() => ({ ensureCsrfToken: vi.fn(async () => 'csrf-token') }));
vi.mock('../helpers', async (importOriginal) => {
    const actual = await importOriginal<typeof import('../helpers')>();
    return { ...actual, ensureCsrfToken };
});

import { buildSandboxImageStream } from '../raw/projects';

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
                releaseLock: () => { },
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

/**
 * The lines `POST /api/projects/{id}/sandbox/build` really writes — captured with `curl -N` against a
 * dev server whose podman cannot start. They arrive as plain text, and only the last frame of a
 * successful build is the JSON result.
 */
const BUILD_LINES = [
    'Building sandbox image from Dockerfile...',
    'time="2026-09-25T02:56:52Z" level=error msg="[graphdriver] prior storage driver overlay failed: \'overlay\' is not supported over overlayfs"',
    'Error: \'overlay\' is not supported over overlayfs, a mount_program is required',
    'error: build image localhost/sandbox-sse-src: exit status 125',
];

describe('buildSandboxImageStream reads the server frame stream', () => {
    function streamInChunks(...text: string[]): Response {
        const encoder = new TextEncoder();
        let next = 0;
        return {
            ok: true,
            body: {
                getReader: () => ({
                    read: async () =>
                        next < text.length
                            ? { done: false, value: encoder.encode(text[next++]) }
                            : { done: true, value: undefined },
                    releaseLock: () => { },
                }),
            },
        } as unknown as Response;
    }

    it('hands every build line to the callback, byte for byte', async () => {
        const body = BUILD_LINES.map((l) => `data: ${l}\n\n`).join('');
        // One chunk ends inside the second frame's payload.
        const cut = body.indexOf('msg="') + 10;
        const withResult = body + `data: ${RESULT}\n\n`;
        vi.stubGlobal('fetch', vi.fn(async () => streamInChunks(withResult.slice(0, cut), withResult.slice(cut))));

        const lines: string[] = [];
        const result = await buildSandboxImageStream('p1', (l) => lines.push(l));

        expect(lines).toEqual(BUILD_LINES);
        expect(result).toEqual({ status: 'built', image: 'proj-img', project: 'p1' });
    });

    it('reports a stream that ends without a result frame as incomplete', async () => {
        // The server's own failure path: it writes `error: …` as a log line and returns.
        vi.stubGlobal('fetch', vi.fn(async () =>
            streamInChunks(BUILD_LINES.map((l) => `data: ${l}\n\n`).join(''))));

        const lines: string[] = [];
        await expect(buildSandboxImageStream('p1', (l) => lines.push(l)))
            .rejects.toThrow('Build completed without result');
        expect(lines[lines.length - 1]).toContain('exit status 125');
    });
});
