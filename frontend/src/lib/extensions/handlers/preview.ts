/**
 * `preview.serve(content)` → { token, url } for a nested sandboxed preview
 * iframe; `preview.release(token)` frees the doc early (TTL is the backstop).
 * A new serve from the same editor implicitly releases that editor's
 * previous doc. Security: project/path/task come from the host's own
 * injection context, never from extension arguments.
 */
import type { CapabilityDef, HostContext } from '../types';
import { apiFetch } from '$lib/api/helpers';

export const CAP_PREVIEW_SERVE = 'preview.serve';
export const CAP_PREVIEW_RELEASE = 'preview.release';

// Per editor instance (anchor element): the last-served doc token plus a
// serve generation. A serve applies (implicit release + tracking) only if it
// is still the latest requested — a stale completion must not release the doc
// a newer serve already installed. WeakMap — entries vanish when the editor
// unmounts, no cleanup hook.
interface AnchorTracking {
    token?: string;
    gen: number;
}
const trackingByAnchor = new WeakMap<HTMLElement, AnchorTracking>();

interface ServeResult {
    token: string;
    url: string;
}

async function serve(ctx: HostContext, args: { content: string }): Promise<ServeResult> {
    // Order overlapping serves by request time: bump the generation before the
    // async POST; only the latest generation applies on completion (a stale
    // completion orphans its doc — the server TTL reclaims it — instead of
    // releasing the doc a newer serve installed).
    let gen = 0;
    if (ctx.anchor) {
        const t = trackingByAnchor.get(ctx.anchor);
        gen = (t?.gen ?? 0) + 1;
        trackingByAnchor.set(ctx.anchor, { token: t?.token, gen });
    }
    const content = String(args.content ?? '');
    const context = (ctx.context ?? {}) as Record<string, unknown>;
    const res = await apiFetch('/api/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            content,
            project: typeof context.projectId === 'string' ? context.projectId : '',
            path: typeof context.filePath === 'string' ? context.filePath : '',
            task: typeof context.taskId === 'string' ? context.taskId : '',
        }),
    });
    if (!res.ok) {
        throw new Error(`preview serve failed (${res.status})`);
    }
    const result = await res.json();
    if (ctx.anchor) {
        const cur = trackingByAnchor.get(ctx.anchor);
        if (cur && cur.gen === gen) {
            // Implicit release: supersede this editor's previous doc (explicit
            // preview.release is still used for immediate reclaim on close).
            const prev = cur.token;
            if (prev && prev !== result.token) {
                apiFetch(`/api/preview/${prev}`, { method: 'DELETE' }).catch(() => {});
            }
            cur.token = result.token;
        }
    }
    return result;
}

async function release(ctx: HostContext, args: { token: string }): Promise<{ ok: boolean }> {
    const token = String(args.token || '');
    if (ctx.anchor) {
        const t = trackingByAnchor.get(ctx.anchor);
        if (t && t.token === token) t.token = undefined;
    }
    const res = await apiFetch(`/api/preview/${token}`, { method: 'DELETE' });
    if (!res.ok) {
        throw new Error(`preview release failed (${res.status})`);
    }
    return { ok: true };
}

export const previewCapabilities: Record<string, CapabilityDef> = {
    [CAP_PREVIEW_SERVE]: {
        kind: 'call',
        argsSchema: { content: 'string' },
        handler: serve,
    },
    [CAP_PREVIEW_RELEASE]: {
        kind: 'call',
        argsSchema: { token: 'string' },
        handler: release,
    },
};
