/**
 * `network.proxy` — the ONLY network capability available to extensions.
 *
 * The sandboxed iframe has no session and cannot call `/api` itself. Instead
 * it asks the host to perform a request through the existing server-side proxy
 * endpoint `/api/extensions/{id}/proxy`, which enforces the manifest's domain
 * allowlist. This keeps all outbound traffic inside the trusted server gate.
 */
import type { CapabilityDef } from '../types';
import { apiFetch } from '$lib/api/helpers';

export const CAP_NETWORK_PROXY = 'network.proxy';

/** Perform a proxied HTTP request on behalf of the extension. */
async function proxyRequest(
    ctx: { extensionId: string },
    args: { url: string; method: string; body?: string }
): Promise<any> {
    if (!args.url) throw new Error('url is required');
    const method = args.method || 'GET';
    const res = await apiFetch(`/api/extensions/${ctx.extensionId}/proxy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: args.url, method, body: args.body || '' }),
    });
    if (!res.ok) {
        // Thrown, never rendered: `dispatch` catches a handler throw and posts it to the
        // extension frame as a JSON-RPC error, so this is a protocol reply rather than
        // copy — which is why the reason is thrown inline instead of assembled in a
        // `msg` local (a local makes it read like a string the UI might display).
        const body = await res.json().catch(() => null);
        if (body?.error) throw new Error(String(body.error));
        throw new Error(`proxy request failed (${res.status})`);
    }
    const contentType = res.headers.get('content-type') || '';
    if (contentType.includes('application/json')) {
        return res.json();
    }
    return res.text();
}

export const networkCapabilities: Record<string, CapabilityDef> = {
    [CAP_NETWORK_PROXY]: {
        kind: 'call',
        argsSchema: { url: 'string', method: 'string', body: 'string' },
        handler: proxyRequest,
    },
};
