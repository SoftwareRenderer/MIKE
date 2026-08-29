/**
 * Extension asset URLs — the same-origin path an extension's assets are loaded
 * from must stay inside /extensions/<author>/<name>/ no matter what the
 * installed record says, and external assets must be rejected outright.
 */
import { describe, it, expect } from 'vitest';
import { extBaseURL, resolveAssetURL } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

function makeExt(name: string, author: string): Extension {
    return {
        id: 'ext-1',
        name,
        version: '1.0.0',
        type: 'ui',
        enabled: true,
        manifest: {
            display_name: name,
            description: 'Test',
            author,
            version: '1.0.0',
            capabilities: { ui: { injection_points: ['fileEditor'] } },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: false, memory_limit: 0 },
        running: false,
        installed_at: '',
        built_at: '',
        install_source: 'registry',
    } as Extension;
}

describe('extBaseURL', () => {
    it('builds /extensions/<author-slug>/<name>', () => {
        expect(extBaseURL(makeExt('html-preview', 'SoftwareRenderer')))
            .toBe('/extensions/softwarerenderer/html-preview');
    });

    it('cannot be reshaped by a traversal name — it stays under /extensions/', () => {
        const base = extBaseURL(makeExt('../../../etc', 'test'));
        // Percent-encoded, so the browser never sees a `..` segment to resolve.
        expect(base).toBe('/extensions/test/..%2F..%2F..%2Fetc');
        const resolved = new URL(base, 'https://app.local').pathname;
        expect(resolved.startsWith('/extensions/test/')).toBe(true);
    });

    it('percent-encodes characters that would otherwise change the path', () => {
        expect(extBaseURL(makeExt('my theme', 'test'))).toBe('/extensions/test/my%20theme');
        expect(extBaseURL(makeExt('a?b#c', 'test'))).toBe('/extensions/test/a%3Fb%23c');
    });
});

describe('resolveAssetURL', () => {
    it('resolves relative assets against the extension base', () => {
        const ext = makeExt('html-preview', 'test');
        expect(resolveAssetURL(ext, './Preview.js')).toBe('/extensions/test/html-preview/Preview.js');
        // A leading slash is the extension's own dir, never the server root.
        expect(resolveAssetURL(ext, '/Preview.js')).toBe('/extensions/test/html-preview/Preview.js');
    });

    it('rejects external and protocol-relative assets', () => {
        const ext = makeExt('html-preview', 'test');
        expect(resolveAssetURL(ext, 'https://evil.example/x.css')).toBe('');
        expect(resolveAssetURL(ext, 'data:text/css,body{}')).toBe('');
        expect(resolveAssetURL(ext, '//evil.example/x.css')).toBe('');
    });
});
