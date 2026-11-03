import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

import { buildSandboxImageStream } from '../raw/projects';

/** A `Response` whose body delivers the given text in the given chunks. */
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

/** Minimal SSE Response whose single chunk carries the given data frames. */
function sseResponse(frames: string[]): Response {
    return streamInChunks(frames.map(f => `data: ${f}\n\n`).join(''));
}

// The frame `buildResultFrame` writes — `sandbox.BuildResult` flattened beside the `status` the reader picks the
// result out by.
const RESULT =
    '{"status":"built","image":"localhost/mike-p1:1f2e3d","commit":"1f2e3d4c5b6a","from":"docker.io/library/node:22",' +
    '"bases":["docker.io/library/node:22"],"cache_key":"9f8e7d6c","files":2}';

/**
 * The lines `POST /api/projects/{id}/sandbox/build` really writes: the handler's opener, the guest's own
 * failure tail, and the handler's `error: …` line. They arrive as plain text, and only the last frame of a
 * successful build is the JSON result.
 */
const BUILD_LINES = [
    'Building the sandbox image…',
    'time="2026-09-25T02:56:52Z" level=error msg="[graphdriver] prior storage driver overlay failed: \'overlay\' is not supported over overlayfs"',
    'Error: \'overlay\' is not supported over overlayfs, a mount_program is required',
    'error: build docker.io/library/node:22: exit status 125',
];

describe('buildSandboxImageStream starts the build', () => {
    let fetchMock: ReturnType<typeof vi.fn>;

    beforeEach(() => {
        // A cold session costs the CSRF bootstrap its own request, so the session endpoint is answered
        // here too and only the build request counts as the call under test.
        fetchMock = vi.fn(async (url: any) =>
            String(url).includes('/api/auth/session')
                ? ({ ok: true, json: async () => ({ authenticated: true, csrf_token: 'csrf-token' }) } as Response)
                : sseResponse([BUILD_LINES[0], RESULT]));
        vi.stubGlobal('fetch', fetchMock);
    });

    afterEach(() => {
        vi.unstubAllGlobals();
    });

    function buildCall(): [string, RequestInit] {
        const calls = fetchMock.mock.calls.filter(([url]) => !String(url).includes('/api/auth/session'));
        expect(calls).toHaveLength(1);
        return calls[0] as [string, RequestInit];
    }

    it('posts to the build route with credentials and the CSRF header', async () => {
        const res = await buildSandboxImageStream('p1', () => { });
        expect(res.status).toBe('built');
        expect(buildCall()[0]).toMatch(/\/projects\/p1\/sandbox\/build$/);
        const init = buildCall()[1];
        expect(init.method).toBe('POST');
        expect(init.credentials).toBe('include');
        expect((init.headers as Headers).get('X-CSRF-Token')).toBe('csrf-token');
    });
});

describe('buildSandboxImageStream reads the server frame stream', () => {
    it('hands every build line to the callback, byte for byte', async () => {
        const body = BUILD_LINES.map((l) => `data: ${l}\n\n`).join('');
        // One chunk ends inside the second frame's payload.
        const cut = body.indexOf('msg="') + 10;
        const withResult = body + `data: ${RESULT}\n\n`;
        vi.stubGlobal('fetch', vi.fn(async () => streamInChunks(withResult.slice(0, cut), withResult.slice(cut))));

        const lines: string[] = [];
        const result = await buildSandboxImageStream('p1', (l) => lines.push(l));

        expect(lines).toEqual(BUILD_LINES);
        expect(result.image).toBe('localhost/mike-p1:1f2e3d');
        expect(result.status).toBe('built');
        expect(result.files).toBe(2);
    });

    it('keeps a JSON line that names no result in the log', async () => {
        // A `{`-shaped frame is not thereby the result: only a `status` string is. Reading any object as the
        // result would end the log at the first line that happens to look like JSON.
        vi.stubGlobal('fetch', vi.fn(async () =>
            streamInChunks('data: {"note":"a line that looks like JSON"}\n\ndata: ' + RESULT + '\n\n')));

        const lines: string[] = [];
        const result = await buildSandboxImageStream('p1', (l) => lines.push(l));

        expect(lines).toEqual(['{"note":"a line that looks like JSON"}']);
        expect(result.status).toBe('built');
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
