/**
 * A clone URL that carries a credential never enters the persisted offline queue —
 * it would sit in IndexedDB after the tab closes, holding an access token. Refused,
 * not stripped: stripping would queue a URL that clones something else.
 *
 * The sibling rule for provider keys lives in `config-offline-key.unit.ts`; these
 * cases exist because the clone form is a second place a secret gets typed, and the
 * ssh forms must keep queueing.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';

/** What `$lib/storage` would have written to IndexedDB. */
const persisted = vi.hoisted(() => ({ queue: null as unknown[] | null }));

vi.mock('$lib/storage', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/storage')>();
    return {
        ...actual,
        loadQueue: async () => persisted.queue ?? [],
        saveQueue: async (actions: unknown[]) => { persisted.queue = actions; },
    };
});

import { createProject, cloneProject } from '$lib/api/projects';
import { setOfflineStatus, clearQueue } from '$lib/api/queue';

const TOKEN = 'https://user:ghp_0123456789abcdef@github.com/org/private.git';
const CLEAN = 'https://github.com/org/public.git';
const SCP = 'git@github.com:org/private.git';

function stubFetch() {
    const calls: any[][] = [];
    (globalThis as any).fetch = vi.fn(async (url: any, init?: any) => {
        calls.push([url, init]);
        if (typeof url === 'string' && url.includes('/api/auth/session')) {
            return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'tok' }) };
        }
        return { ok: true, status: 200, json: async () => ({ id: 'p1', name: 'repo', path: '/srv/repo' }) };
    });
    return calls;
}

beforeEach(() => {
    setOfflineStatus(false);
    clearQueue();
    persisted.queue = null;
});

describe('createProject from a git URL, offline', () => {
    it('refuses to queue a URL that carries credentials', async () => {
        stubFetch();
        setOfflineStatus(true);

        await expect(createProject('/srv/private', false, TOKEN)).rejects.toThrow(/offline/i);

        // Nothing reached the storage boundary — no token on disk.
        expect(persisted.queue ?? []).toEqual([]);
    });

    it('still queues a URL without credentials', async () => {
        stubFetch();
        setOfflineStatus(true);

        await createProject('/srv/public', false, CLEAN);

        const actions = persisted.queue as Array<{ type: string; params: { gitUrl: string } }>;
        expect(actions).toHaveLength(1);
        expect(actions[0].params.gitUrl).toBe(CLEAN);
    });

    it('still queues the scp-like ssh form, which carries no secret', async () => {
        stubFetch();
        setOfflineStatus(true);

        await createProject('/srv/private', false, SCP);

        expect(persisted.queue as unknown[]).toHaveLength(1);
    });

    it('still queues a local-path project, which has no URL at all', async () => {
        stubFetch();
        setOfflineStatus(true);

        await createProject('/srv/checkout', true);

        expect(persisted.queue as unknown[]).toHaveLength(1);
    });

    it('sends the URL straight to the server when online', async () => {
        const calls = stubFetch();

        await createProject('/srv/private', false, TOKEN);

        const post = calls.find(([url, init]) => String(url).includes('/api/projects') && init?.method === 'POST');
        expect(post).toBeTruthy();
        expect(JSON.parse(post![1].body).git_url).toBe(TOKEN);
        expect(persisted.queue ?? []).toEqual([]);
    });
});

describe('cloneProject, offline', () => {
    it('refuses to queue a URL that carries credentials', async () => {
        stubFetch();
        setOfflineStatus(true);

        await expect(cloneProject('/srv/private', TOKEN, false)).rejects.toThrow(/offline/i);

        expect(persisted.queue ?? []).toEqual([]);
    });

    it('still queues a credential-free URL, and names the project from it', async () => {
        stubFetch();
        setOfflineStatus(true);

        await cloneProject('/srv/public', CLEAN, false);

        const actions = persisted.queue as Array<{ type: string; params: { gitUrl: string } }>;
        expect(actions[0].params.gitUrl).toBe(CLEAN);
    });
});
