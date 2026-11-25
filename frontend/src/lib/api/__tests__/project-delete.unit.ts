/**
 * The two shapes of a project deletion, and the offline mirror of the heavier one: a queued deletion that
 * lost its two fields would delete the rows and quietly keep the folder.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';

/** What `$lib/storage` would have written to IndexedDB. */
const persisted = vi.hoisted(() => ({ queue: null as unknown[] | null }));

vi.mock('$lib/storage', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/storage')>();
    return {
        ...actual,
        loadQueue: async () => persisted.queue ?? [],
        updateQueue: async (change: (held: unknown[]) => unknown[]) => {
            persisted.queue = change(persisted.queue ?? []);
            return persisted.queue as unknown[];
        },
    };
});

import { deleteProject } from '$lib/api/projects';
import { setOfflineStatus, clearQueue, waitForFlush } from '$lib/api/queue';

let calls: any[][];

beforeEach(() => {
    setOfflineStatus(false);
    clearQueue();
    persisted.queue = null;
    calls = [];
    (globalThis as any).fetch = vi.fn(async (url: any, init?: any) => {
        if (typeof url === 'string' && url.includes('/api/auth/session')) {
            return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'tok' }) };
        }
        calls.push([url, init]);
        return { ok: true, status: 204, json: async () => null };
    });
});

const DELETION = { delete_data: true, confirm_name: 'my-repo' };

describe('deleteProject', () => {
    it('sends no body at all when only the rows go', async () => {
        await deleteProject('p1');

        expect(calls).toHaveLength(1);
        const [url, init] = calls[0];
        expect(url).toBe('/api/projects/p1');
        expect(init.method).toBe('DELETE');
        expect(init.body).toBeUndefined();
    });

    it('carries the typed folder name when the folder goes too', async () => {
        await deleteProject('p1', DELETION);

        const [, init] = calls[0];
        expect(JSON.parse(init.body)).toEqual(DELETION);
        expect(new Headers(init.headers).get('content-type')).toBe('application/json');
    });

    it('queues the folder deletion with its name, and replays the request that was made', async () => {
        setOfflineStatus(true);
        await deleteProject('p1', DELETION);

        const held = (persisted.queue ?? []) as Array<{ type: string; params: any }>;
        expect(held.map((a) => a.type)).toEqual(['deleteProject']);
        expect(held[0].params).toEqual({ id: 'p1', deletion: DELETION });

        setOfflineStatus(false);
        await waitForFlush();

        const [url, init] = calls[0];
        expect(url).toBe('/api/projects/p1');
        expect(init.method).toBe('DELETE');
        expect(JSON.parse(init.body)).toEqual(DELETION);
    });
});
