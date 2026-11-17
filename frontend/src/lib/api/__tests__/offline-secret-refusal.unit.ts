/**
 * A request carrying a plaintext secret is refused rather than queued, because the queue is persisted: a
 * queued secret sits in IndexedDB after the tab closes. It is refused, never stripped — a stripped provider
 * key quietly drops what the person typed, and a stripped clone URL queues a clone of something else (a
 * public repo of the same name, or nothing).
 *
 * Two places a secret gets typed, one rule each side of the same gate: a provider key or MCP header on the
 * config save, and the userinfo of a clone URL. The forms that carry none keep queueing — the masked
 * placeholder, the scp-like ssh form, a local path.
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
import { createProject, cloneProject } from '$lib/api/projects';
import { setOfflineStatus, clearQueue } from '$lib/api/queue';
import type { Config } from '$lib/types';

// The server's masked placeholder (maskedAPIKey, backend/config/config.go).
const MASKED = '\u2022'.repeat(10);
const SECRET = 'sk-proj-0123456789abcdef';
const TOKEN_URL = 'https://user:ghp_0123456789abcdef@github.com/org/private.git';
const CLEAN_URL = 'https://github.com/org/public.git';
const SCP_URL = 'git@github.com:org/private.git';

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
        return { ok: true, status: 200, json: async () => ({ ok: true, id: 'p1', name: 'repo', path: '/srv/repo' }) };
    });
    return calls;
}

/** The write that reached the storage boundary, if any. */
function held(): Array<{ type: string; params: any }> {
    return (persisted.queue ?? []) as Array<{ type: string; params: any }>;
}

beforeEach(() => {
    setOfflineStatus(false);
    clearQueue();
    persisted.queue = null;
});

describe('a provider key offline', () => {
    it('refuses to queue a config that carries a real provider key', async () => {
        stubFetch();
        setOfflineStatus(true);

        await expect(updateConfig(configWithKey(SECRET))).rejects.toThrow(/offline/i);

        // Nothing reached the storage boundary.
        expect(held()).toEqual([]);
    });

    it('still queues a config whose keys are the masked placeholder', async () => {
        stubFetch();
        setOfflineStatus(true);

        await updateConfig(configWithKey(MASKED));

        const actions = held();
        expect(actions).toHaveLength(1);
        expect(actions[0].type).toBe('updateConfig');
        expect(actions[0].params.config.worker_model).toBe('acme-large');
        expect(actions[0].params.config.providers[0].api_key).toBe(MASKED);
    });

    it('still queues a config that carries no key at all', async () => {
        stubFetch();
        setOfflineStatus(true);

        await updateConfig(configWithKey(''));

        expect(held()).toHaveLength(1);
    });

    it('sends the key straight to the server when online', async () => {
        const calls = stubFetch();

        await updateConfig(configWithKey(SECRET));

        const put = calls.find(([url, init]) => String(url).includes('/api/config') && init?.method === 'PUT');
        expect(put).toBeTruthy();
        expect(JSON.parse(put![1].body).providers[0].api_key).toBe(SECRET);
        // …and never through the queue.
        expect(held()).toEqual([]);
    });
});

describe('a clone URL offline', () => {
    it.each([
        ['createProject', (url: string) => createProject('/srv/private', false, url)],
        ['cloneProject', (url: string) => cloneProject('/srv/private', url, false)],
    ])('%s refuses to queue a URL that carries credentials', async (_name, write) => {
        stubFetch();
        setOfflineStatus(true);

        await expect(write(TOKEN_URL)).rejects.toThrow(/offline/i);

        // Nothing reached the storage boundary — no token on disk.
        expect(held()).toEqual([]);
    });

    it('still queues a URL without credentials', async () => {
        stubFetch();
        setOfflineStatus(true);

        await createProject('/srv/public', false, CLEAN_URL);

        const actions = held();
        expect(actions).toHaveLength(1);
        expect(actions[0].params.gitUrl).toBe(CLEAN_URL);
    });

    it('still queues the scp-like ssh form, which carries no secret', async () => {
        stubFetch();
        setOfflineStatus(true);

        await createProject('/srv/private', false, SCP_URL);

        expect(held()).toHaveLength(1);
    });

    it('still queues a local-path project, which has no URL at all', async () => {
        stubFetch();
        setOfflineStatus(true);

        await createProject('/srv/checkout', true);

        expect(held()).toHaveLength(1);
    });

    it('sends the URL straight to the server when online', async () => {
        const calls = stubFetch();

        await createProject('/srv/private', false, TOKEN_URL);

        const post = calls.find(([url, init]) => String(url).includes('/api/projects') && init?.method === 'POST');
        expect(post).toBeTruthy();
        expect(JSON.parse(post![1].body).git_url).toBe(TOKEN_URL);
        expect(held()).toEqual([]);
    });

    it('queues a credential-free clone', async () => {
        stubFetch();
        setOfflineStatus(true);

        await cloneProject('/srv/public', CLEAN_URL, false);

        const actions = held();
        expect(actions[0].params.gitUrl).toBe(CLEAN_URL);
    });
});
