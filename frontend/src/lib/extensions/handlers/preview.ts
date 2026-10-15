/**
 * `preview.serve` / `preview.serveScripts` / `preview.serveNetwork` →
 * { token, url } for a nested sandboxed preview iframe.
 * `preview.release(token)` frees the doc early (the server's TTL is the
 * backstop). `preview.navState(token)` returns the last page load under the
 * token; the extension uses it to decide whether an unexpected frame load
 * navigated within the tree.
 * A new serve from the same editor implicitly releases its previous doc.
 *
 *  - preview.serve:        static loads only — script-src 'none', a bare sandbox
 *  - preview.serveScripts: strict zero-egress CSP, paired with allow-scripts
 *  - preview.serveNetwork: permissive CSP (scripts + network)
 * The capability the extension dispatched is the posture the server serves, and an extension reaches
 * `network` only through a manifest grant. The policies live in backend/server/handler_preview.go.
 */
import type { CapabilityDef, HostContext } from '../types';
import { apiFetch } from '$lib/api/helpers';

export const CAP_PREVIEW_SERVE = 'preview.serve';
export const CAP_PREVIEW_SERVE_SCRIPTS = 'preview.serveScripts';
export const CAP_PREVIEW_SERVE_NETWORK = 'preview.serveNetwork';
export const CAP_PREVIEW_RELEASE = 'preview.release';
export const CAP_PREVIEW_NAV_STATE = 'preview.navState';

// Per editor (anchor element): last installed doc token + generation.
// A serve only applies if it's still the newest requested — otherwise it
// would release the doc a newer serve installed. WeakMap: entries die with
// the editor.
interface AnchorTracking {
    token?: string;
    gen: number;
}
const trackingByAnchor = new WeakMap<HTMLElement, AnchorTracking>();

interface ServeResult {
    token: string;
    url: string;
}

// seq only grows, so the extension can tell a fresh load from a stale record. `ok` is the server's
// `seq > 0`, and stays in the shape because an installed extension reads it.
interface NavState {
    seq: number;
    path: string;
    ok: boolean;
    isDoc: boolean;
}

// The posture names the host posts; previewPosture*Name in backend/server/handler_preview.go holds the
// matching names and the policies they select.
const POSTURE_NO_SCRIPT = 'no-script';
const POSTURE_SCRIPTS = 'scripts';
const POSTURE_NETWORK = 'network';

async function serve(ctx: HostContext, args: { content: string }, posture: string): Promise<ServeResult> {
    // Bump before the async POST; only the newest generation applies on
    // completion (a stale serve must not release a newer serve's doc).
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
            posture,
        }),
    });
    if (!res.ok) {
        throw new Error(`preview serve failed (${res.status})`);
    }
    const result = await res.json();
    if (ctx.anchor) {
        const cur = trackingByAnchor.get(ctx.anchor);
        if (cur && cur.gen === gen) {
            // Implicit release of this editor's previous doc (preview.release
            // covers close).
            const prev = cur.token;
            if (prev && prev !== result.token) {
                apiFetch(`/api/preview/${prev}`, { method: 'DELETE' }).catch(() => {});
            }
            cur.token = result.token;
        }
    }
    return result;
}

// Asked after a frame load the extension didn't start (the frame's URL is
// unreadable — opaque origin). Throws if the doc is gone; the extension
// treats that as an escape.
async function navState(_ctx: HostContext, args: { token: string }): Promise<NavState> {
    const token = String(args.token || '');
    if (!token) {
        throw new Error('token is required');
    }
    const res = await apiFetch(`/api/preview-nav/${token}`);
    if (!res.ok) {
        throw new Error(`preview nav state failed (${res.status})`);
    }
    return res.json();
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
    // The default posture: static loads, nothing runs.
    [CAP_PREVIEW_SERVE]: {
        kind: 'call',
        argsSchema: { content: 'string' },
        handler: (ctx, args) => serve(ctx, args, POSTURE_NO_SCRIPT),
    },
    // Zero-egress CSP with scripts allowed; paired with allow-scripts.
    [CAP_PREVIEW_SERVE_SCRIPTS]: {
        kind: 'call',
        argsSchema: { content: 'string' },
        handler: (ctx, args) => serve(ctx, args, POSTURE_SCRIPTS),
    },
    // Scripts and network egress; needs the extension's manifest permission.
    [CAP_PREVIEW_SERVE_NETWORK]: {
        kind: 'call',
        argsSchema: { content: 'string' },
        handler: (ctx, args) => serve(ctx, args, POSTURE_NETWORK),
    },
    [CAP_PREVIEW_RELEASE]: {
        kind: 'call',
        argsSchema: { token: 'string' },
        handler: release,
    },
    // Watchdog nav state (session-authed endpoint — the extension iframe is
    // opaque-origin and asks through the host).
    [CAP_PREVIEW_NAV_STATE]: {
        kind: 'call',
        argsSchema: { token: 'string' },
        handler: navState,
    },
};
