import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { UIInjector } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

/**
 * Regression test for a DOM/heap leak: `teardownSlot(point, anchor)` destroyed
 * the channels and removed them from the inner per-anchor map, but never removed
 * the (now-empty) anchor entry from the outer `channels: Map<HTMLElement, Map>`.
 * Each anchor that ever hosted a UI-extension channel therefore stayed
 * referenced — pinning the dead DOM element (and its subtree) in memory for the
 * life of the app. After the fix, an anchor with no remaining channels is
 * dropped from the map so the element can be garbage-collected.
 */
describe('UIInjector anchor leak (channels map retention)', () => {
    let injector: UIInjector;

    beforeEach(() => {
        injector = new UIInjector();
    });

    afterEach(() => {
        injector.clearCache();
        document.body.innerHTML = '';
    });

    function makeUIExtension(id: string, name: string, point: string): Extension {
        return {
            id,
            name,
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: name,
                description: '',
                author: '',
                version: '1.0.0',
                capabilities: {
                    ui: { injection_points: [point], permissions: [] },
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };
    }

    function anchorDiv(): HTMLDivElement {
        const el = document.createElement('div');
        document.body.appendChild(el);
        return el;
    }

    // `private` is compile-time only; at runtime the map is a plain property.
    function channelsMap(i: UIInjector): Map<HTMLElement, unknown> {
        return (i as any).channels;
    }

    it('drops the anchor from the channels map once its last channel is torn down', () => {
        injector.registerFromExtensions([makeUIExtension('md', 'md', 'fileEditor')]);
        const anchor = anchorDiv();

        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a.md' }, anchor);
        // While mounted, the anchor is tracked in the map.
        expect(channelsMap(injector).has(anchor)).toBe(true);

        injector.teardownSlot('fileEditor' as any, anchor);
        // After teardown the anchor has no channels → its entry is removed, so
        // the (dead) DOM element is no longer pinned in the map.
        expect(channelsMap(injector).has(anchor)).toBe(false);
    });

    it('keeps an anchor that still has other channels, and prunes empty ones on bulk teardown', () => {
        injector.registerFromExtensions([makeUIExtension('md', 'md', 'fileEditor')]);
        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a.md' }, anchor1);
        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'b.md' }, anchor2);
        expect(channelsMap(injector).size).toBe(2);

        // Tear down instance 1 only → only its empty anchor entry is dropped.
        injector.teardownSlot('fileEditor' as any, anchor1);
        expect(channelsMap(injector).has(anchor1)).toBe(false);
        expect(channelsMap(injector).has(anchor2)).toBe(true);
        expect(channelsMap(injector).size).toBe(1);

        // Bulk teardown (no anchor) empties the remaining instance too.
        injector.teardownSlot('fileEditor' as any);
        expect(channelsMap(injector).has(anchor2)).toBe(false);
        expect(channelsMap(injector).size).toBe(0);
    });
});
