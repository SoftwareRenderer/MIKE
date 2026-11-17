/**
 * The asset path an extension is loaded from, and the one theme stylesheet the
 * injector applies from it.
 *
 * `extBaseURL`/`resolveAssetURL` are the fence: the served directory the server
 * reported wins, and an asset may never leave it. `registerFromExtensions` is
 * where that fence is applied to a manifest's `css_url`, and what it decides is
 * visible twice — the `<link>` in the document and the hint `index.html`
 * pre-paints from — so a rejected URL must be refused by both.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { extBaseURL, resolveAssetURL, UIInjector, THEME_CSS_KEY } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

const THEME_LINK_ID = 'kanban-theme-stylesheet';

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

function makeCSSExtension(cssUrl: string, enabled = true): Extension {
    return {
        ...makeExt('test-theme', 'Test'),
        id: 'test-ext-1',
        name: 'test-theme',
        type: 'css',
        enabled,
        manifest: {
            display_name: 'Test Theme',
            description: 'Test',
            author: 'Test',
            version: '1.0.0',
            capabilities: { css: { css_url: cssUrl } },
        },
    } as Extension;
}

function makeUIExtension(id: string, name: string, priority?: number): Extension {
    return {
        ...makeExt(name, 'Test'),
        id,
        name,
        manifest: {
            display_name: name,
            description: '',
            author: 'Test',
            version: '1.0.0',
            ...(priority === undefined ? {} : { priority }),
            capabilities: { ui: { injection_points: ['taskCard'], permissions: [] } },
        },
    } as Extension;
}

function themeLink(): HTMLLinkElement | null {
    return document.getElementById(THEME_LINK_ID) as HTMLLinkElement | null;
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

/** The mount URL the injector hands a ui extension — what the frame's src is built from. */
function entry(ext: Extension): string {
    const injector = new UIInjector();
    injector.registerFromExtensions([ext]);
    const [registration] = injector.registrations;
    if (!registration) throw new Error(`${ext.name}: registered nothing`);
    return registration.moduleUrl;
}

describe('the URL the extension iframe is mounted at', () => {
    it('is the served directory, never the index.html file', () => {
        // The file form is answered with a 301 by Go's FileServer, and the per-mount channel
        // token keeps that redirect out of every cache, so each mount would pay it again.
        const mounted = entry(makeExt('html-preview', 'SoftwareRenderer'));
        expect(mounted).toBe('/extensions/softwarerenderer/html-preview/');
        expect(mounted).not.toContain('index.html');
    });

    it('is the reported directory for a flat install', () => {
        expect(entry(withServed('probe-pack', 'acme', '/extensions/probe-pack')))
            .toBe('/extensions/probe-pack/');
    });

    it('leaves the entry document resolving its own assets inside its directory', () => {
        // Dropping index.html must not move the base the extension's <script src="Preview.js">
        // resolves against — the document already arrived at this URL through the redirect.
        const mounted = entry(makeExt('html-preview', 'test'));
        expect(new URL('Preview.js', 'https://app.local' + mounted).pathname)
            .toBe('/extensions/test/html-preview/Preview.js');
    });

    it('carries the manifest priority as the render order, defaulting to 0', () => {
        // Two rules the mount order depends on: an absent `priority` is 0, and the sort
        // is numeric — a negative priority loads before the default, the default before 10.
        const injector = new UIInjector();
        injector.registerFromExtensions([
            makeUIExtension('ext-ten', 'ext-ten', 10),
            makeUIExtension('ext-default', 'ext-default'),
            makeUIExtension('ext-neg', 'ext-neg', -5),
        ]);

        const regs = injector.getRegistrations('taskCard' as any);
        expect(regs.map((r) => [r.extensionName, r.order])).toEqual([
            ['ext-neg', -5],
            ['ext-default', 0],
            ['ext-ten', 10],
        ]);
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

describe('the theme stylesheet a CSS extension applies', () => {
    let injector: UIInjector;

    beforeEach(() => {
        injector = new UIInjector();
        themeLink()?.remove();
        localStorage.removeItem(THEME_CSS_KEY);
    });

    afterEach(() => {
        injector.clearCache();
        themeLink()?.remove();
        localStorage.removeItem(THEME_CSS_KEY);
    });

    it('injects a <link> and caches its URL for the pre-paint hint', () => {
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);

        expect(themeLink()?.getAttribute('rel')).toBe('stylesheet');
        expect(themeLink()?.getAttribute('href')).toBe('/extensions/test/test-theme/styles.css');
        expect(localStorage.getItem(THEME_CSS_KEY)).toBe('/extensions/test/test-theme/styles.css');
    });

    it.each(['styles.css', './styles.css', '/styles.css'])(
        'resolves a css_url against the extension base, never the server root: %s',
        (cssUrl) => {
            injector.registerFromExtensions([makeCSSExtension(cssUrl)]);
            expect(themeLink()?.getAttribute('href')).toBe('/extensions/test/test-theme/styles.css');
        },
    );

    it('refuses an external css_url — no stylesheet, and nothing handed to the pre-paint hint', () => {
        injector.registerFromExtensions([makeCSSExtension('https://evil.example/theme.css')]);

        expect(themeLink()).toBeFalsy();
        expect(localStorage.getItem(THEME_CSS_KEY)).toBeNull();
    });

    it('a rejected css_url does not block a subsequent valid CSS extension', () => {
        const bad = makeCSSExtension('https://evil.example/theme.css');
        const good = makeCSSExtension('styles.css');
        good.id = 'test-ext-2';
        good.name = 'good-theme';

        injector.registerFromExtensions([bad, good]);

        expect(themeLink()?.getAttribute('href')).toBe('/extensions/test/good-theme/styles.css');
    });

    it('first enabled CSS extension with css_url wins', () => {
        const ext1 = makeCSSExtension('theme/first.css');
        const ext2 = makeCSSExtension('theme/second.css');
        ext2.id = 'test-ext-2';
        ext2.name = 'second-theme';

        injector.registerFromExtensions([ext1, ext2]);

        expect(themeLink()?.getAttribute('href')).toBe('/extensions/test/test-theme/theme/first.css');
    });

    it('swaps the stylesheet when css_url changes', () => {
        injector.registerFromExtensions([makeCSSExtension('theme/dark.css')]);
        expect(themeLink()?.getAttribute('href')).toBe('/extensions/test/test-theme/theme/dark.css');

        const lightExt = makeCSSExtension('theme/light.css');
        lightExt.id = 'test-ext-2';
        lightExt.name = 'mike-light';
        injector.registerFromExtensions([lightExt]);

        expect(themeLink()?.getAttribute('href')).toBe('/extensions/test/mike-light/theme/light.css');
    });

    it('keeps the <link> element itself when nothing moved', () => {
        injector.registerFromExtensions([makeCSSExtension('theme/same.css')]);
        const first = themeLink();

        injector.registerFromExtensions([makeCSSExtension('theme/same.css')]);

        // A recreated element re-fetches the sheet on every extension list, which is
        // every `extension_change` broadcast.
        expect(themeLink()).toBe(first);
    });

    // The link and the hint are one decision: index.html trusts whatever is in storage,
    // so a sheet this module would not load must not be painted from it either.
    it.each([
        ['no CSS extension at all', () => [] as Extension[]],
        ['the CSS extension disabled', () => [makeCSSExtension('styles.css', false)]],
    ])('takes the sheet and the hint back for %s', (_label, list) => {
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);
        expect(themeLink()).toBeTruthy();
        expect(localStorage.getItem(THEME_CSS_KEY)).toBeTruthy();

        injector.registerFromExtensions(list());

        expect(themeLink()).toBeFalsy();
        expect(localStorage.getItem(THEME_CSS_KEY)).toBeNull();
    });

    it('clearCache removes the stylesheet', () => {
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);
        expect(themeLink()).toBeTruthy();

        injector.clearCache();

        expect(themeLink()).toBeFalsy();
    });

    it('a css extension registers no injection point, and a ui extension injects no sheet', () => {
        injector.registerFromExtensions([makeCSSExtension('theme/test.css')]);
        expect(injector.registrations.length).toBe(0);

        injector.registerFromExtensions([makeUIExtension('ui-ext', 'ui-ext')]);
        expect(themeLink()).toBeFalsy();
        expect(injector.registrations.length).toBe(1);
    });
});
