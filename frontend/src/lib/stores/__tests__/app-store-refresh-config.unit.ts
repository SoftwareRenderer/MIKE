import { describe, it, expect, vi, beforeEach } from 'vitest';

// The config fetch and the cache marker are the two things refreshConfig() decides on,
// so both are mocked at the module boundary.
vi.mock('$lib/storage', () => ({
    loadCollapsedTasks: vi.fn(async () => new Set<string>()),
    saveCollapsedTasks: vi.fn(async () => {}),
    loadAppSetting: vi.fn(async () => false),
    saveAppSetting: vi.fn(async () => {}),
}));
vi.mock('$lib/api/config', () => ({
    getConfig: vi.fn(),
}));
vi.mock('$lib/api/cache', () => ({
    isFromCache: vi.fn(() => false),
}));

import { appStore } from '$lib/stores';
import { getConfig } from '$lib/api/config';
import { isFromCache } from '$lib/api/cache';
import type { Config, Provider } from '$lib/types';

const mockedGetConfig = vi.mocked(getConfig);
const mockedIsFromCache = vi.mocked(isFromCache);

function makeConfig(providers: Provider[]): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers,
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
    };
}

const llm: Provider = { id: 1, url: 'http://llm:8080/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0, stream_idle_seconds: 0 };
const pi: Provider = { name: 'pi', kind: 'agent', url: 'agent://pi', display_name: 'Pi Coding Agent', concurrency: 1, max_requests_per_second: 0, models: [], stream_idle_seconds: 0, };

describe('AppStore.refreshConfig', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mockedIsFromCache.mockReturnValue(false);
        appStore.config = null;
    });

    it('picks up a provider the server derives per request (an installed agent extension)', async () => {
        appStore.config = makeConfig([llm]);
        mockedGetConfig.mockResolvedValue(makeConfig([llm, pi]));

        await appStore.refreshConfig();

        const providers = appStore.config?.providers || [];
        expect(providers.some(p => p.url === 'agent://pi')).toBe(true);
    });

    it('drops a provider the server no longer reports (agent disabled or removed)', async () => {
        appStore.config = makeConfig([llm, pi]);
        mockedGetConfig.mockResolvedValue(makeConfig([llm]));

        await appStore.refreshConfig();

        expect(appStore.config?.providers).toEqual([llm]);
    });

    it('keeps the config in hand when the fetch fails', async () => {
        appStore.config = makeConfig([llm, pi]);
        mockedGetConfig.mockRejectedValue(new Error('offline'));

        await expect(appStore.refreshConfig()).resolves.toBeUndefined();

        expect(appStore.config?.providers).toEqual([llm, pi]);
    });

    it('does not apply an offline-cache copy over a live config', async () => {
        // getConfig() falls back to the IndexedDB copy, which by definition predates the
        // extension change this refresh exists to pick up.
        appStore.config = makeConfig([llm, pi]);
        mockedGetConfig.mockResolvedValue(makeConfig([llm]));
        mockedIsFromCache.mockReturnValue(true);

        await appStore.refreshConfig();

        expect(appStore.config?.providers).toEqual([llm, pi]);
    });

    it('takes the cached copy when there is nothing live to lose', async () => {
        mockedGetConfig.mockResolvedValue(makeConfig([llm]));
        mockedIsFromCache.mockReturnValue(true);

        await appStore.refreshConfig();

        expect(appStore.config?.providers).toEqual([llm]);
    });

    it('coalesces concurrent refreshes into a single request', async () => {
        // Installing/toggling extensions broadcasts one event per operation; each handler
        // calls refreshConfig(), but only one fetch should leave the browser.
        let resolveFetch: (c: Config) => void = () => {};
        mockedGetConfig.mockReturnValue(new Promise(r => { resolveFetch = r; }));

        const a = appStore.refreshConfig();
        const b = appStore.refreshConfig();
        expect(mockedGetConfig).toHaveBeenCalledTimes(1);

        resolveFetch(makeConfig([llm, pi]));
        await Promise.all([a, b]);

        expect(a).toBe(b);
        expect(appStore.config?.providers.length).toBe(2);
        // The slot is released: a later change fetches again.
        mockedGetConfig.mockResolvedValue(makeConfig([llm]));
        await appStore.refreshConfig();
        expect(mockedGetConfig).toHaveBeenCalledTimes(2);
    });
});
