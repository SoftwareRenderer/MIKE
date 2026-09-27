/**
 * The service worker's extension-asset cache and the rule for dropping it.
 *
 * `vite.config.ts` serves `/extensions/` `NetworkFirst` with the cache as the
 * offline fallback, so the fallback is a body written the last time that file was
 * fetched — which is the extension's previous code, running in a frame the host
 * armed and granted. The fingerprint is what decides "the set moved", and the
 * injector is where a moved set is noticed.
 */
import { describe, it, expect, afterEach, vi } from 'vitest';
import { dropExtensionAssetCache, extensionSetFingerprint, EXTENSION_ASSET_CACHE } from '$lib/services/sw-cache';
import { uiInjector } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

function ext(over: Partial<Extension>): Extension {
    return {
        id: 'e1', name: 'pack', author: 'acme', version: '1.0.0', type: 'ui', enabled: true,
        served_url: '/extensions/acme/pack',
        manifest: { author: 'acme', capabilities: { ui: { injection_points: ['fileEditor'] } } },
        config: {}, container_config: {}, sandbox_config: { network_isolation: false, memory_limit: 0 },
        running: false, installed_at: '', built_at: '', install_source: 'registry',
        ...over,
    } as Extension;
}

/** A CacheStorage stand-in that records which caches were dropped. */
function stubCaches(): { delete: ReturnType<typeof vi.fn> } {
    const del = vi.fn().mockResolvedValue(true);
    vi.stubGlobal('caches', { delete: del });
    return { delete: del };
}

/** The drop is fire-and-forget — nothing in the app awaits it — so a test of it waits for
 *  the queue to drain instead of holding a handle on the promise. */
const drained = () => new Promise((resolve) => setTimeout(resolve, 0));

afterEach(() => {
    vi.unstubAllGlobals();
    uiInjector.clearCache();
});

describe('extensionSetFingerprint', () => {
    it('is stable across order and across a field set that did not move', () => {
        const a = [ext({ id: 'a' }), ext({ id: 'b', name: 'other' })];
        const b = [ext({ id: 'b', name: 'other' }), ext({ id: 'a' })];
        expect(extensionSetFingerprint(a)).toBe(extensionSetFingerprint(b));
        expect(extensionSetFingerprint([])).not.toBe(extensionSetFingerprint(a));
    });

    it('moves when the bytes the browser loads could have moved', () => {
        const base = [ext({})];
        const moved = [
            [ext({ commit: 'abc123' })],                       // an update: same path, new code
            [ext({ version: '2.0.0' })],                       // an update with no git pin
            [ext({ enabled: false })],                         // disabled: its assets must not load
            [ext({ served_url: '/extensions/other/pack' })],   // re-installed somewhere else
            [ext({ id: 'e2' })],                               // a different extension
            [],                                                // deleted
        ];
        for (const next of moved) {
            expect(extensionSetFingerprint(next), JSON.stringify(next[0] ?? null)).not.toBe(
                extensionSetFingerprint(base),
            );
        }
    });

    it('does not move on an edit that changes no served byte', () => {
        // A memory-limit or config edit is the ordinary case; dropping the cache for
        // it would throw away the offline fallback for nothing.
        expect(extensionSetFingerprint([ext({ config: { theme: 'dark' } })]))
            .toBe(extensionSetFingerprint([ext({})]));
    });
});

describe('dropExtensionAssetCache', () => {
    it('drops the cache vite.config.ts names, by that name', async () => {
        const caches = stubCaches();
        await dropExtensionAssetCache();
        expect(caches.delete).toHaveBeenCalledWith(EXTENSION_ASSET_CACHE);
    });

    it('is silent where there is no Cache Storage', async () => {
        // jsdom, and any browser before the worker takes control.
        vi.stubGlobal('caches', undefined);
        await expect(dropExtensionAssetCache()).resolves.toBeUndefined();
    });

    it('never throws: a cache that will not go away is not worth a failed extension load', async () => {
        vi.stubGlobal('caches', { delete: vi.fn().mockRejectedValue(new Error('quota')) });
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        await expect(dropExtensionAssetCache()).resolves.toBeUndefined();
        expect(warn).toHaveBeenCalled();
        warn.mockRestore();
    });
});

describe('the injector drops the asset cache when the installed set moved', () => {
    it('drops on a change and not on a repeat', async () => {
        const caches = stubCaches();
        const one = [ext({})];

        uiInjector.registerFromExtensions(one);
        await drained();
        expect(caches.delete).not.toHaveBeenCalled(); // the first list is the baseline

        uiInjector.registerFromExtensions(one);
        await drained();
        expect(caches.delete).not.toHaveBeenCalled();

        uiInjector.registerFromExtensions([ext({ commit: 'deadbeef' })]);
        await drained();
        expect(caches.delete).toHaveBeenCalledTimes(1);
    });

    it('drops when an extension is gone, which is the case that matters most', async () => {
        const caches = stubCaches();
        uiInjector.registerFromExtensions([ext({})]);
        await drained();
        uiInjector.registerFromExtensions([]);
        await drained();
        expect(caches.delete).toHaveBeenCalledWith(EXTENSION_ASSET_CACHE);
    });
});
