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

/** The same record with a served directory the server reported. */
function withServed(name: string, author: string, served: unknown): Extension {
    return { ...makeExt(name, author), served_url: served } as Extension;
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

describe('extBaseURL — the served directory the server reported', () => {

    it('uses served_url for the author-namespaced layout', () => {
        expect(extBaseURL(withServed('zh', 'SoftwareRenderer', '/extensions/softwarerenderer/zh')))
            .toBe('/extensions/softwarerenderer/zh');
    });

    it('fetches a flat-installed extension from where its files actually are', () => {
        // The regression: an unpublished pack placed at extensions/<name> was
        // fetched from /extensions/<manifest author>/<name>, which does not exist,
        // so the pack — installed and enabled — left the UI in English.
        const ext = withServed('probe-pack', 'Probe Author', '/extensions/probe-pack');
        expect(extBaseURL(ext)).toBe('/extensions/probe-pack');
        expect(resolveAssetURL(ext, 'messages.json')).toBe('/extensions/probe-pack/messages.json');
    });

    it('keeps a reported path that only needed encoding', () => {
        expect(extBaseURL(withServed('my theme', 'acme', '/extensions/acme/my%20theme')))
            .toBe('/extensions/acme/my%20theme');
    });

    it('falls back to the derivation for a reported path that is not an extension directory', () => {
        const fallback = '/extensions/acme/pack';
        const notAnExtensionDir = [
            '/etc/passwd',            // outside the tree entirely
            'https://evil.example/x', // a different origin
            '//evil.example/x',       // protocol-relative
            '/extensions/../etc',     // climbs out
            '/extensions/%2e%2e/etc', // …after decoding
            '/extensions/a/b/c',      // deeper than <author>/<name>
            '/extensions/',           // the root itself
            '/extensions/bad%zz',     // not a valid escape
            '/extensions/a\\b',       // a Windows separator is not a path separator here
        ];
        for (const served of notAnExtensionDir) {
            expect(extBaseURL(withServed('pack', 'acme', served)), served).toBe(fallback);
        }
        // Absent, or the wrong type, is not a path either.
        expect(extBaseURL(withServed('pack', 'acme', undefined))).toBe(fallback);
        expect(extBaseURL(withServed('pack', 'acme', 42))).toBe(fallback);
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

    it('refuses an asset that leaves the directory it belongs to', () => {
        // The browser normalizes a dot segment before it sends, so each of these is
        // a request for something else — with this session's cookie. Only the
        // answer is asserted, never the shape: a URL the caller must then check is
        // a URL the caller may forget to check.
        const ext = makeExt('pack', 'acme');
        const escaping = [
            '../../../../api/config',
            '../../other-pack/theme.css',
            '/../../../../etc/passwd',
            './sub/../../other-pack/theme.css',
            'sub/%2e%2e/%2e%2e/api/config',   // the server decodes once, then routes
            'theme.css?v=2',                  // an asset is a file, not a request
            'theme.css#x',
            'theme\\.css',
        ];
        for (const asset of escaping) {
            expect(resolveAssetURL(ext, asset), asset).toBe('');
        }
    });

    it('keeps an asset inside the directory, however it is spelled', () => {
        const ext = makeExt('pack', 'acme');
        expect(resolveAssetURL(ext, 'styles/theme.css')).toBe('/extensions/acme/pack/styles/theme.css');
        expect(resolveAssetURL(ext, './styles/theme.css')).toBe('/extensions/acme/pack/styles/theme.css');
        expect(resolveAssetURL(ext, 'sub/../theme.css')).toBe('/extensions/acme/pack/theme.css');
        expect(resolveAssetURL(ext, 'a/../b/./c.css')).toBe('/extensions/acme/pack/b/c.css');
        // A doubled separator is the same path once resolved, so it is answered, not
        // refused — the rule is where the path lands, not how it was typed.
        expect(resolveAssetURL(ext, 'sub//theme.css')).toBe('/extensions/acme/pack/sub/theme.css');
    });

    it('refuses an asset of an extension whose own directory is not a directory', () => {
        // `encodeURIComponent` leaves dots alone, so a record whose name is `..` really
        // does produce '/extensions/acme/..' as its base. There is no directory to be
        // inside, and answering '/extensions/theme.css' — the tree root's file — is an
        // answer that belongs to nobody.
        expect(resolveAssetURL(makeExt('..', 'acme'), 'theme.css')).toBe('');
    });
});
