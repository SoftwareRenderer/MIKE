/**
 * `fs.getBytes` — one file's bytes, for an extension that renders a format the core knows nothing about.
 *
 * The mount context names the file; `side` is all the extension chooses. The handler returns a Blob, so
 * the channel answers it outside the JSON round-trip (`ExtensionHostChannel.postBytes`).
 */
import type { CapabilityDef, HostContext } from '../types';
import { apiFetch } from '$lib/api/helpers';

export const CAP_FS_GET_BYTES = 'fs.getBytes';

const SIDES = ['working', 'base'] as const;

async function getBytes(ctx: HostContext, args: { side: string }): Promise<Blob> {
    const side = String(args?.side ?? '');
    if (!SIDES.includes(side as (typeof SIDES)[number])) {
        throw new Error(`side must be 'working' or 'base' (got ${JSON.stringify(side)})`);
    }
    const context = (ctx.context ?? {}) as Record<string, unknown>;
    const project = typeof context.projectId === 'string' ? context.projectId : '';
    const task = typeof context.taskId === 'string' ? context.taskId : '';
    const path = typeof context.repoPath === 'string' ? context.repoPath : '';
    const baseRef = typeof context.baseRef === 'string' ? context.baseRef : '';
    if (!project || !path) {
        throw new Error('this frame was not mounted on a file');
    }
    if (side === 'base' && !baseRef) {
        throw new Error("this frame was not mounted on a diff, so it has no 'base' version to read");
    }

    const query = new URLSearchParams({ project, path });
    if (task) query.set('task', task);
    if (side === 'base') query.set('ref', baseRef);

    query.set('kind', 'bytes');
    const res = await apiFetch(`/api/fs/read?${query.toString()}`);
    if (!res.ok) {
        // Thrown, never rendered: `dispatch` posts a handler throw to the frame as an error, and the
        // route's own sentence names the reason, so it travels verbatim.
        const body = await res.json().catch(() => null);
        if (body?.error) throw new Error(String(body.error));
        throw new Error(`byte read failed (${res.status})`);
    }
    return res.blob();
}

export const fsCapabilities: Record<string, CapabilityDef> = {
    [CAP_FS_GET_BYTES]: {
        kind: 'call',
        argsSchema: { side: 'string' },
        handler: (ctx, args) => getBytes(ctx, args),
    },
};
