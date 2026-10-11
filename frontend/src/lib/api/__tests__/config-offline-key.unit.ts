/**
 * A provider API key never enters the persisted offline queue: a queued config
 * would sit in IndexedDB after the tab closes. It is refused, not stripped, so a
 * typed key is never quietly dropped.
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

import { updateConfig } from '$lib/api/config';
import { setOfflineStatus, clearQueue } from '$lib/api/queue';
import type { Config } from '$lib/types';

// The server's masked placeholder (maskedAPIKey, backend/config/config.go).
const MASKED = '\u2022'.repeat(10);
const SECRET = 'sk-proj-0123456789abcdef';

function configWithKey(apiKey: string): Config {
    return {
        workspace_dir: '/srv/work',
        providers: [
            { name: 'acme', kind: 'llm', url: 'https://acme.test/v1', models: [], api_key: apiKey },
        ],
        worker_provider: 'acme',
        worker_model: 'acme-large',
    } as unknown as Config;
}

/** Stub fetch: serve the CSRF bootstrap, record everything else. */
function stubFetch() {
    const calls: any[][] = [];
    (globalThis as any).fetch = vi.fn(async (url: any, init?: any) => {
        calls.push([url, init]);
        if (typeof url === 'string' && url.includes('/api/auth/session')) {
            return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'tok' }) };
        }
        return { ok: true, status: 200, json: async () => ({ ok: true }) };
    });
    return calls;
}

beforeEach(() => {
    setOfflineStatus(false);
    clearQueue();
    persisted.queue = null;
});

describe('updateConfig while offline', () => {
    it('refuses to queue a config that carries a real provider key', async () => {
        stubFetch();
        setOfflineStatus(true);

        await expect(updateConfig(configWithKey(SECRET))).rejects.toThrow(/offline/i);

        // Nothing reached the storage boundary.
        expect(persisted.queue ?? []).toEqual([]);
    });

    it('still queues a config whose keys are the masked placeholder', async () => {
        stubFetch();
        setOfflineStatus(true);

        await updateConfig(configWithKey(MASKED));

        const actions = persisted.queue as Array<{ type: string; params: { config: Config } }>;
        expect(actions).toHaveLength(1);
        expect(actions[0].type).toBe('updateConfig');
        expect(actions[0].params.config.worker_model).toBe('acme-large');
        expect(actions[0].params.config.providers[0].api_key).toBe(MASKED);
    });

    it('still queues a config that carries no key at all', async () => {
        stubFetch();
        setOfflineStatus(true);

        await updateConfig(configWithKey(''));

        expect(persisted.queue as unknown[]).toHaveLength(1);
    });

    it('sends the key straight to the server when online', async () => {
        const calls = stubFetch();

        await updateConfig(configWithKey(SECRET));

        const put = calls.find(([url, init]) => String(url).includes('/api/config') && init?.method === 'PUT');
        expect(put).toBeTruthy();
        expect(JSON.parse(put![1].body).providers[0].api_key).toBe(SECRET);
        // …and never through the queue.
        expect(persisted.queue ?? []).toEqual([]);
    });
});
