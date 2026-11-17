/**
 * A channel belongs to one anchor element.
 *
 * One injection point (e.g. `fileEditor`) is mounted in many instances at once — one open
 * file each — so every instance's anchor gets its own independent channel: tearing one
 * down must leave the others mounted, and the (dead) anchor must not stay pinned in the
 * channel map once its last channel is gone. Both halves are the same key: an anchor that
 * is kept is an anchor whose DOM subtree can never be collected, and an anchor that is
 * shared is an instance whose Preview button disappears when a sibling closes.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { UIInjector } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

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
            capabilities: { ui: { injection_points: [point], permissions: [] } },
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

const FILE_EDITOR = 'fileEditor' as any;
const context = (filePath: string) => ({ component: 'FileEditor', filePath });

describe('one channel per instance', () => {
    beforeEach(() => {
        injector.registerFromExtensions([makeUIExtension('md-preview', 'md-preview', 'fileEditor')]);
    });

    it('renders an independent channel into each instance anchor', () => {
        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.refreshSlot(FILE_EDITOR, context('a.md'), anchor1);
        injector.refreshSlot(FILE_EDITOR, context('b.md'), anchor2);

        // Both anchors keep their own iframe — refreshing one must NOT remove
        // the other (this was the bug).
        expect(anchor1.querySelector('iframe')).toBeTruthy();
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });

    it('tearing down one instance leaves the others intact', () => {
        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.renderSlot(FILE_EDITOR, context('a.md'), anchor1);
        injector.renderSlot(FILE_EDITOR, context('b.md'), anchor2);

        // Close "file a" — only its channel is torn down.
        injector.teardownSlot(FILE_EDITOR, anchor1);
        expect(anchor1.querySelector('iframe')).toBeFalsy();
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });

    it('re-rendering into the same anchor replaces its own channel only', () => {
        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.renderSlot(FILE_EDITOR, context('a.md'), anchor1);
        injector.renderSlot(FILE_EDITOR, context('b.md'), anchor2);
        const firstIframe = anchor1.querySelector('iframe');

        // Refresh instance 1 (e.g. its file changed) — its own iframe is
        // replaced, instance 2 untouched.
        injector.refreshSlot(FILE_EDITOR, context('a-edit.md'), anchor1);

        const newIframe = anchor1.querySelector('iframe');
        expect(newIframe).toBeTruthy();
        expect(newIframe).not.toBe(firstIframe);
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });

    it('registerFromExtensions bump re-renders per instance (version change)', () => {
        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();
        injector.renderSlot(FILE_EDITOR, context('a.md'), anchor1);
        injector.renderSlot(FILE_EDITOR, context('b.md'), anchor2);

        // Simulate the extension list changing → version bump → FileEditor
        // $effects re-run renderSlot for each of their anchors. (registerFrom
        // extensions destroys existing channels; the $effect re-creates them.)
        injector.registerFromExtensions([makeUIExtension('md-preview', 'md-preview', 'fileEditor')]);
        expect(injector.getPointVersion(FILE_EDITOR)).toBe(2);

        injector.renderSlot(FILE_EDITOR, context('a.md'), anchor1);
        injector.renderSlot(FILE_EDITOR, context('b.md'), anchor2);
        expect(anchor1.querySelector('iframe')).toBeTruthy();
        expect(anchor2.querySelector('iframe')).toBeTruthy();
    });
});

describe('the anchor is dropped with its last channel', () => {
    beforeEach(() => {
        injector.registerFromExtensions([makeUIExtension('md', 'md', 'fileEditor')]);
    });

    it('drops an anchor once its last channel is torn down, and keeps one that still has others', () => {
        const anchor1 = anchorDiv();
        const anchor2 = anchorDiv();

        injector.renderSlot(FILE_EDITOR, context('a.md'), anchor1);
        injector.renderSlot(FILE_EDITOR, context('b.md'), anchor2);
        expect(channelsMap(injector).size).toBe(2);

        // Tear down instance 1 only → only its empty anchor entry is dropped.
        injector.teardownSlot(FILE_EDITOR, anchor1);
        expect(channelsMap(injector).has(anchor1)).toBe(false);
        expect(channelsMap(injector).has(anchor2)).toBe(true);
        expect(channelsMap(injector).size).toBe(1);

        // Bulk teardown (no anchor) empties the remaining instance too.
        injector.teardownSlot(FILE_EDITOR);
        expect(channelsMap(injector).has(anchor2)).toBe(false);
        expect(channelsMap(injector).size).toBe(0);
    });
});
