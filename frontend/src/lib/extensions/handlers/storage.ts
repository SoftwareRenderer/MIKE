/**
 * `storage.*` — scoped key-value persistence for extensions.
 *
 * The sandboxed iframe has no localStorage of its own (opaque origin), so
 * storage is a host capability. Keys are namespaced by the injection context
 * (project/task) server-side, keeping the core feature-agnostic: an extension
 * stores what it wants (e.g. tags) without the host knowing what it means.
 */
import type { CapabilityDef, HostContext } from '../types';
import { apiFetch } from '$lib/api/helpers';

export const CAP_STORAGE_GET = 'storage.get';
export const CAP_STORAGE_SET = 'storage.set';
export const CAP_STORAGE_DELETE = 'storage.delete';
export const CAP_STORAGE_LIST = 'storage.list';

/** Build a namespace from the injection context; falls back to 'global'. */
export function buildStorageNamespace(context: any): string {
    const parts: string[] = [];
    if (context?.projectId) parts.push(`p:${context.projectId}`);
    if (context?.taskId) parts.push(`t:${context.taskId}`);
    return parts.length ? parts.join('/') : 'global';
}

function storageURL(ctx: HostContext): string {
    return `/api/extensions/${ctx.extensionId}/storage`;
}

/** Fetch a single value by key (resolves null if absent). */
async function get(ctx: HostContext, args: { key: string }): Promise<any> {
    if (!args?.key) throw new Error('key is required');
    const ns = buildStorageNamespace(ctx.context);
    const res = await apiFetch(`${storageURL(ctx)}?namespace=${encodeURIComponent(ns)}&key=${encodeURIComponent(args.key)}`);
    if (res.status === 404) return null;
    if (!res.ok) throw new Error(`storage.get failed (${res.status})`);
    const entry = await res.json();
    return entry?.value ?? null;
}

/** Write a JSON-serializable value under a key. */
async function set(ctx: HostContext, args: { key: string; value: any }): Promise<void> {
    if (!args?.key) throw new Error('key is required');
    const ns = buildStorageNamespace(ctx.context);
    const res = await apiFetch(storageURL(ctx), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ namespace: ns, key: args.key, value: args.value }),
    });
    if (!res.ok) throw new Error(`storage.set failed (${res.status})`);
}

/** Delete a key (no-op if absent). */
async function del(ctx: HostContext, args: { key: string }): Promise<void> {
    if (!args?.key) throw new Error('key is required');
    const ns = buildStorageNamespace(ctx.context);
    const res = await apiFetch(`${storageURL(ctx)}?namespace=${encodeURIComponent(ns)}&key=${encodeURIComponent(args.key)}`, {
        method: 'DELETE',
    });
    if (!res.ok) throw new Error(`storage.delete failed (${res.status})`);
}

/** List entries in the namespace, optionally filtered by key prefix. */
async function list(ctx: HostContext, args: { prefix?: string }): Promise<Record<string, any>> {
    const ns = buildStorageNamespace(ctx.context);
    const prefix = args?.prefix ? `&prefix=${encodeURIComponent(args.prefix)}` : '';
    const res = await apiFetch(`${storageURL(ctx)}?namespace=${encodeURIComponent(ns)}${prefix}`);
    if (!res.ok) throw new Error(`storage.list failed (${res.status})`);
    const entries: Array<{ key: string; value: any }> = await res.json();
    const out: Record<string, any> = {};
    for (const e of entries) out[e.key] = e.value;
    return out;
}

export const storageCapabilities: Record<string, CapabilityDef> = {
    [CAP_STORAGE_GET]: { kind: 'call', argsSchema: { key: 'string' }, handler: get },
    [CAP_STORAGE_SET]: { kind: 'call', argsSchema: { key: 'string', value: 'any' }, handler: set },
    [CAP_STORAGE_DELETE]: { kind: 'call', argsSchema: { key: 'string' }, handler: del },
    [CAP_STORAGE_LIST]: { kind: 'call', argsSchema: { prefix: 'string' }, handler: list },
};
