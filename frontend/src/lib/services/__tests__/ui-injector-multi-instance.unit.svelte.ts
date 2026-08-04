import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { UIInjector } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

/**
 * Regression test: a point (e.g. `fileEditor`) can be mounted in MULTIPLE
 * instances at once — one open file per editor. Each instance must get its own
 * independent channel, so rendering/refreshing one instance must not tear down
 * the channels of the others (which previously left the Preview button on only
 * one of several open Markdown files).
 */
describe('UIInjector multi-instance channel scoping', () => {
    let injector: UIInjector;

    beforeEach(() => {
        injector = new UIInjector();
    });

    afterEach(() => {
        injector.clearCache();
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
                    ui: {
                        injection_points: [point],
                        permissions: [],
                    },
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

    it('renders an independent channel into each instance anchor', () => {
        injector.registerFromExtensions([makeUIExtension('md-preview', 'md-preview', 'fileEditor')]);

        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.refreshSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a.md' }, anchor1);
        injector.refreshSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'b.md' }, anchor2);

        // Both anchors keep their own iframe — refreshing one must NOT remove
        // the other (this was the bug).
        expect(anchor1.querySelector('iframe')).toBeTruthy();
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });

    it('tearing down one instance leaves the others intact', () => {
        injector.registerFromExtensions([makeUIExtension('md-preview', 'md-preview', 'fileEditor')]);

        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a.md' }, anchor1);
        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'b.md' }, anchor2);
        expect(anchor1.querySelector('iframe')).toBeTruthy();
        expect(anchor2.querySelector('iframe')).toBeTruthy();

        // Close "file a" — only its channel is torn down.
        injector.teardownSlot('fileEditor' as any, anchor1);
        expect(anchor1.querySelector('iframe')).toBeFalsy();
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });

    it('re-rendering into the same anchor replaces its own channel only', () => {
        injector.registerFromExtensions([makeUIExtension('md-preview', 'md-preview', 'fileEditor')]);

        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a.md' }, anchor1);
        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'b.md' }, anchor2);
        const firstIframe = anchor1.querySelector('iframe');

        // Refresh instance 1 (e.g. its file changed) — its own iframe is
        // replaced, instance 2 untouched.
        injector.refreshSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a-edit.md' }, anchor1);

        const newIframe = anchor1.querySelector('iframe');
        expect(newIframe).toBeTruthy();
        expect(newIframe).not.toBe(firstIframe);
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });

    it('registerFromExtensions bump re-renders per instance (version change)', () => {
        injector.registerFromExtensions([makeUIExtension('md-preview', 'md-preview', 'fileEditor')]);

        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();
        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a.md' }, anchor1);
        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'b.md' }, anchor2);

        // Simulate the extension list changing → version bump → FileEditor
        // $effects re-run renderSlot for each of their anchors. (registerFrom
        // extensions destroys existing channels; the $effect re-creates them.)
        injector.registerFromExtensions([makeUIExtension('md-preview', 'md-preview', 'fileEditor')]);
        expect(injector.getPointVersion('fileEditor' as any)).toBe(2);

        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'a.md' }, anchor1);
        injector.renderSlot('fileEditor' as any, { component: 'FileEditor', filePath: 'b.md' }, anchor2);
        expect(anchor1.querySelector('iframe')).toBeTruthy();
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });
});
