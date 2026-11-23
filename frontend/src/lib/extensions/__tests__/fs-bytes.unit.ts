/**
 * `fs.getBytes` — who decides which file's bytes leave the server.
 *
 * The frame is third-party code with a granted capability, so the one thing that must hold is that it
 * can choose between two versions of a file and nothing else: the project, the task, the path and the
 * base ref are what the mount context says.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { SUPPORTED_CAPABILITIES, CAP_FS_GET_BYTES } from '../capabilities';
import type { HostContext } from '../types';
import { apiFetch } from '$lib/api/helpers';

vi.mock('$lib/api/helpers', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api/helpers')>();
    return { ...actual, apiFetch: vi.fn() };
});

const apiFetchMock = vi.mocked(apiFetch);

const handler = () => {
    const def = SUPPORTED_CAPABILITIES[CAP_FS_GET_BYTES];
    if (!def?.handler) throw new Error('fs.getBytes left the catalog');
    return def.handler;
};

function ctx(overrides: Record<string, unknown> = {}): HostContext {
    return {
        extensionId: 'ext-1',
        extensionName: 'viewer',
        point: 'fileEditor' as any,
        config: {},
        context: {
            component: 'FileEditor',
            projectId: 'p1',
            taskId: 't1',
            repoPath: 'mesh/part.zbin',
            ...overrides,
        },
        anchor: null,
        sendEvent: () => {},
        ui: { setLoading: () => {}, setError: () => {}, hide: () => {}, setSize: () => {}, setFill: () => {} },
    } as unknown as HostContext;
}

function fakeResponse(body: Blob | string, status = 200): Response {
    const text = typeof body === 'string' ? body : '';
    return {
        ok: status >= 200 && status < 300,
        status,
        headers: new Headers({ 'content-type': typeof body === 'string' ? 'application/json' : 'application/octet-stream' }),
        blob: async () => (body instanceof Blob ? body : new Blob([text])),
        json: async () => JSON.parse(text),
        text: async () => text,
    } as unknown as Response;
}

function fakeJsonResponse(payload: unknown, status: number): Response {
    const text = JSON.stringify(payload);
    return {
        ok: status >= 200 && status < 300,
        status,
        headers: new Headers({ 'content-type': 'application/json' }),
        json: async () => JSON.parse(text),
        text: async () => text,
    } as unknown as Response;
}

function respondWith(blob: Blob, ok = true, status = 200) {
    apiFetchMock.mockResolvedValue(ok ? fakeResponse(blob, status) : fakeResponse(JSON.stringify({ error: 'no such file' }), status));
}

/** The query the handler actually sent, from the n-th call (0-based). */
function sentQuery(n = 0): URLSearchParams {
    const url = apiFetchMock.mock.calls[n]?.[0] as string;
    return new URLSearchParams(url.split('?')[1] ?? '');
}

function sentUrl(n = 0): string {
    return apiFetchMock.mock.calls[n]?.[0] as string;
}

beforeEach(() => {
    apiFetchMock.mockReset();
});

describe('fs.getBytes', () => {
    it('asks for the file the frame was mounted on', async () => {
        const blob = new Blob([new Uint8Array([0, 1, 2])]);
        respondWith(blob);

        const got = await handler()(ctx(), { side: 'working' });

        expect(got).toBe(blob);
        expect(sentUrl(0)).toContain('/api/fs/read?');
        expect(sentQuery(0).get('kind')).toBe('bytes');
        expect(sentQuery(0).get('project')).toBe('p1');
        expect(sentQuery(0).get('task')).toBe('t1');
        expect(sentQuery(0).get('path')).toBe('mesh/part.zbin');
        expect(sentQuery(0).get('ref')).toBe(null);
    });

    it('ignores a path, a project or a ref the frame supplied', async () => {
        respondWith(new Blob(['x']));

        await handler()(ctx(), {
            side: 'working',
            path: '../../../.ssh/id_ed25519',
            project: 'other-project',
            task: 'other-task',
            ref: 'HEAD~5',
        } as any);

        const q = sentQuery(0);
        expect(q.get('path')).toBe('mesh/part.zbin');
        expect(q.get('project')).toBe('p1');
        expect(q.get('task')).toBe('t1');
        expect(q.get('ref')).toBe(null);
        expect(sentUrl(0)).not.toContain('ssh');
    });

    it('reads the version a diff compares against by that ref alone', async () => {
        respondWith(new Blob(['old']));

        await handler()(ctx({ baseRef: 'abc123' }), { side: 'base' });

        expect(sentQuery(0).get('ref')).toBe('abc123');
        expect(sentQuery(0).get('path')).toBe('mesh/part.zbin');
    });

    it('refuses a base version for a file that is not in a diff, and asks the server nothing', async () => {
        await expect(handler()(ctx(), { side: 'base' })).rejects.toThrow(/base/);
        expect(apiFetchMock).not.toHaveBeenCalled();
    });

    it('refuses any side but the two it knows', async () => {
        for (const side of ['HEAD', '', 'working ', 'original']) {
            await expect(handler()(ctx(), { side })).rejects.toThrow(/side must be/);
        }
        expect(apiFetchMock).not.toHaveBeenCalled();
    });

    it('refuses when the frame was mounted on nothing, rather than guessing a file', async () => {
        await expect(handler()(ctx({ repoPath: undefined }), { side: 'working' })).rejects.toThrow(/not mounted on a file/);
        await expect(handler()(ctx({ projectId: undefined }), { side: 'working' })).rejects.toThrow(/not mounted on a file/);
        expect(apiFetchMock).not.toHaveBeenCalled();
    });

    it('asks the server again for a file the agent may have rewritten', async () => {
        respondWith(new Blob(['first']));
        await handler()(ctx(), { side: 'working' });

        respondWith(new Blob(['second']));
        const again = await handler()(ctx(), { side: 'working' });

        expect(apiFetchMock).toHaveBeenCalledTimes(2);
        expect(await again.text()).toBe('second');
    });

    it('carries the server\'s reason to the frame, where the extension can show it', async () => {
        apiFetchMock.mockResolvedValue(fakeJsonResponse({ error: 'no enabled extension declares .zbin' }, 403));

        await expect(handler()(ctx(), { side: 'working' })).rejects.toThrow(/no enabled extension declares/);
    });
});
