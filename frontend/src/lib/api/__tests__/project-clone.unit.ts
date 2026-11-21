import { describe, it, expect, vi, afterEach } from 'vitest';
import type { Project } from '$lib/types';

import { cloneProject, CloneFailure } from '../projects';
import { streamBody } from './fake-stream-body';

/**
 * `POST /api/projects/clone` writes one JSON object per blank-line-terminated frame, and this is the whole
 * reading of it: the frame grammar (which payload is an event at all, and what a missing field means) and the
 * decisions the reader makes over the assembled events. The body below was captured with `curl -N` against a
 * dev server cloning a local repo over `file://`.
 *
 * Everything is driven through `cloneProject` — a frame the reader does not recognise must cost the person
 * nothing, and that is only observable at the end of the clone.
 */
const DONE_PROJECT: Project = {
    id: 'sse-src', name: 'sse-src', path: '/root/sse-clones/sse-src', sandbox_enabled: true,
    created_at: '2026-09-25T02:56:45.332579358Z', updated_at: '2026-09-25T02:56:45.332579358Z',
} as Project;

const doneFrame = `data: ${JSON.stringify({ project: DONE_PROJECT, type: 'done' })}`;

/** Stub fetch: serve the CSRF bootstrap, answer the clone with these chunks, and record the request. */
function stubCloneResponse(...chunks: (string | Error)[]) {
    const calls: any[][] = [];
    const { res } = streamBody(chunks);
    vi.stubGlobal('fetch', vi.fn(async (url: any, init: any) => {
        // A cold session costs the CSRF bootstrap its own request; only what follows it is under test.
        if (String(url).includes('/api/auth/session')) {
            return { ok: true, json: async () => ({ authenticated: true, csrf_token: 'csrf-token' }) };
        }
        calls.push([url, init]);
        return res;
    }));
    return calls;
}

/** What the reader hands a caller: a percentage, or a line. */
type CloneUpdate = { percent?: number; status?: string };

/** Drive one clone through the reader, reporting whatever it says. */
const clone = (onUpdate?: (update: CloneUpdate) => void) =>
    cloneProject('/root/sse-clones', 'file:///root/sse-src', true, onUpdate);

/** The percentages alone: a line is not a percentage. */
const percentCollector = () => {
    const percents: number[] = [];
    return { percents, onUpdate: (u: CloneUpdate) => { if (u.percent !== undefined) percents.push(u.percent); } };
};

afterEach(() => { vi.unstubAllGlobals(); });

describe('cloneProject reads the server frame stream', () => {
    it('reports every progress percentage and returns the cloned project', async () => {
        const stream = [
            'data: {"message":"Cloning file:///root/sse-src into /root/sse-clones/sse-src...","type":"status"}',
            'data: {"percent":3,"type":"progress"}',
            'data: {"percent":50,"type":"progress"}',
            'data: {"percent":100,"type":"progress"}',
            doneFrame,
        ].map(f => f + '\n\n').join('');

        // Cut mid-frame, and mid-way through the last frame's JSON: a newline inside a frame is not
        // a boundary, and neither is a chunk edge.
        const cut = stream.indexOf('{"percent":50');
        stubCloneResponse(stream.slice(0, cut), stream.slice(cut, stream.length - 2), stream.slice(stream.length - 2));

        const { percents, onUpdate } = percentCollector();
        await expect(clone(onUpdate)).resolves.toEqual(DONE_PROJECT);
        expect(percents).toEqual([3, 50, 100]);
    });

    it('reports a progress frame that names no percent as 0, so the bar never reads NaN', async () => {
        stubCloneResponse('data: {"type":"progress"}\n\n', `${doneFrame}\n\n`);

        const { percents, onUpdate } = percentCollector();
        await clone(onUpdate);
        expect(percents).toEqual([0]);
    });

    it('reports a status line as a line, never as a percentage or a failure', async () => {
        stubCloneResponse(
            'data: {"message":"Cloning into /root/sse-clones/sse-src...","type":"status"}\n\n',
            'data: {"message":"Receiving objects...","type":"status"}\n\n',
            'data: {"percent":10,"type":"progress"}\n\n',
            `${doneFrame}\n\n`,
        );

        const said: (string | number)[] = [];
        await expect(clone(u => said.push(u.status ?? u.percent ?? ''))).resolves.toEqual(DONE_PROJECT);
        expect(said).toEqual(['Cloning into /root/sse-clones/sse-src...', 'Receiving objects...', 10]);
    });

    it("carries git's own lines on the failure, for the board to show", async () => {
        stubCloneResponse('data: {"detail":"fatal: no such repository\\nfatal: could not read","message":"git clone failed","type":"error"}\n\n');

        const failure = await clone().catch((e) => e);
        expect(failure).toBeInstanceOf(CloneFailure);
        expect((failure as CloneFailure).message).toBe('git clone failed');
        expect((failure as CloneFailure).detail).toBe('fatal: no such repository\nfatal: could not read');
    });

    it('reports the server error frame as a failure', async () => {
        stubCloneResponse(
            'data: {"percent":100,"type":"progress"}\n\n',
            'data: {"message":"sandbox_network_isolation is required when sandbox_enabled is true","type":"error"}\n\n',
        );
        await expect(clone()).rejects.toThrow('sandbox_network_isolation is required when sandbox_enabled is true');
    });

    it('names the default failure when an error frame carries no message', async () => {
        stubCloneResponse('data: {"type":"error"}\n\n');
        await expect(clone()).rejects.toThrow('clone failed');
    });

    it('fails when the stream ends without a done frame', async () => {
        // The build log of a clone the server stopped reporting on: progress arrived, no project did.
        stubCloneResponse('data: {"percent":50,"type":"progress"}\n\n');
        await expect(clone()).rejects.toThrow('Clone completed without result');
    });

    it.each([
        ['a legacy text line', 'Building sandbox image...'],
        ['an empty payload', ''],
        ['truncated JSON', '{"type":"progress","percent":'],
        ['an unknown event type', '{"type":"something-else"}'],
    ])('ignores a frame whose payload is not a clone event: %s', async (_name, payload) => {
        stubCloneResponse(
            `data: ${payload}\n\n`,
            'data: {"unexpected":"shape"}\n\n',
            `${doneFrame}\n\n`,
        );
        await expect(clone()).resolves.toMatchObject({ id: 'sse-src' });
    });
});
