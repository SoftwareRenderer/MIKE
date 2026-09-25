import { describe, it, expect, vi, afterEach } from 'vitest';
import type { Project } from '$lib/types';

import { cloneProject } from '../projects';

/**
 * The body `POST /api/projects/clone` really writes — one JSON object per blank-line-terminated
 * frame, captured with `curl -N` against a dev server cloning a local repo over `file://`.
 */
const DONE_PROJECT: Project = {
    id: 'sse-src', name: 'sse-src', path: '/root/sse-clones/sse-src', sandbox_enabled: true,
    created_at: '2026-09-25T02:56:45.332579358Z', updated_at: '2026-09-25T02:56:45.332579358Z',
} as Project;

const SERVER_STREAM = [
    'data: {"message":"Cloning file:///root/sse-src into /root/sse-clones/sse-src...","type":"status"}',
    'data: {"percent":3,"type":"progress"}',
    'data: {"percent":50,"type":"progress"}',
    'data: {"percent":100,"type":"progress"}',
    `data: ${JSON.stringify({ project: DONE_PROJECT, type: 'done' })}`,
].map((f) => f + '\n\n').join('');

/** A `Response` whose body hands over `text` in the given byte ranges, so a frame can straddle chunks. */
function responseChunks(...text: string[]): Response {
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

function stubFetch(response: Response) {
    const calls: any[][] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: any, init: any) => {
        // A cold session costs the CSRF bootstrap its own request; only what follows it is under test.
        if (String(url).includes('/api/auth/session')) {
            return { ok: true, json: async () => ({ authenticated: true, csrf_token: 'csrf-token' }) };
        }
        calls.push([url, init]);
        return response;
    }));
    return calls;
}

afterEach(() => { vi.unstubAllGlobals(); });

describe('cloneProject reads the server frame stream', () => {
    it('reports every progress percentage and returns the cloned project', async () => {
        // Cut mid-frame, and mid-way through the last frame's JSON: a newline inside a frame is not
        // a boundary, and neither is a chunk edge.
        const cut = SERVER_STREAM.indexOf('{"percent":50');
        stubFetch(responseChunks(SERVER_STREAM.slice(0, cut), SERVER_STREAM.slice(cut, SERVER_STREAM.length - 2), SERVER_STREAM.slice(SERVER_STREAM.length - 2)));

        const percents: number[] = [];
        const project = await cloneProject('/root/sse-clones', 'file:///root/sse-src', true, (p) => percents.push(p));

        expect(percents).toEqual([3, 50, 100]);
        expect(project.id).toBe('sse-src');
    });

    it('reports the server error frame as a failure', async () => {
        stubFetch(responseChunks(
            'data: {"percent":100,"type":"progress"}\n\n',
            'data: {"message":"sandbox_network_isolation is required when sandbox_enabled is true","type":"error"}\n\n',
        ));
        await expect(cloneProject('/root/sse-clones', 'file:///root/sse-src', true))
            .rejects.toThrow('sandbox_network_isolation is required when sandbox_enabled is true');
    });

    it('fails when the stream ends without a done frame', async () => {
        // The build log of a clone the server stopped reporting on: progress arrived, no project did.
        stubFetch(responseChunks('data: {"percent":50,"type":"progress"}\n\n'));
        await expect(cloneProject('/root/sse-clones', 'file:///root/sse-src', true))
            .rejects.toThrow('Clone completed without result');
    });

    it('ignores a frame whose payload is not a clone event', async () => {
        stubFetch(responseChunks(
            'data: not json at all\n\n',
            'data: {"unexpected":"shape"}\n\n',
            `data: ${JSON.stringify({ project: DONE_PROJECT, type: 'done' })}\n\n`,
        ));
        await expect(cloneProject('/root/sse-clones', 'file:///root/sse-src', true))
            .resolves.toMatchObject({ id: 'sse-src' });
    });
});
