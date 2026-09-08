/**
 * index.html pre-paints the theme stylesheet from a URL in localStorage, and
 * ui-injector decides separately which URLs are loadable. Two copies of one rule,
 * so this runs the shipped script — extracted from index.html and executed, not
 * string-matched — over the same table of URLs as the predicate.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import {
    isPrepaintableThemeURL,
    THEME_CSS_KEY,
} from '$lib/services/ui-injector.svelte';

/** The inline theme step as `index.html` ships it. */
function prePaintScript(): string {
    // cwd is the frontend package (same assumption as bundled-profiles.unit.ts).
    const file = join(process.cwd(), 'index.html');
    if (!existsSync(file)) throw new Error(`no index.html at ${file}`);
    const html = readFileSync(file, 'utf8');
    const scripts = [...html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
    const theme = scripts.find((s) => s.includes(THEME_CSS_KEY));
    if (!theme) throw new Error('index.html no longer contains the theme pre-paint script');
    return theme;
}

function runPrePaint(stored: string | null): HTMLLinkElement | null {
    document.getElementById('kanban-theme-stylesheet')?.remove();
    if (stored === null) localStorage.removeItem(THEME_CSS_KEY);
    else localStorage.setItem(THEME_CSS_KEY, stored);
    new Function(prePaintScript())();
    return document.getElementById('kanban-theme-stylesheet') as HTMLLinkElement | null;
}

const ACCEPTED = [
    '/extensions/softwarerenderer/midnight/theme.css',
    '/extensions/software-renderer/test-theme/styles/dark.css',
    '/extensions/flat-local-install/theme.css',
];

const REJECTED = [
    '',
    'http://evil.test/theme.css',
    'https://evil.test/theme.css',
    '//evil.test/theme.css',       // protocol-relative
    '/\\evil.test/theme.css',      // browsers fold /\ into //
    'data:text/css,body{background:red}',
    '\\\\evil.test\\theme.css',
    'extensions/x/y/t.css',        // relative: resolves against the current page
    '/theme.css',                  // same-origin, but not the extension tree
    '/extensions/',                // a directory, not a file
    '/extensions//theme.css',
    // Dot segments: the browser normalizes these before it sends, so each of these
    // is a request for something outside the installed-extension tree — with this
    // session's cookie. Stated in both the resolved and the still-relative form,
    // because the value reaches here from storage, not from resolveAssetURL.
    '/extensions/acme/pack/../../../../api/config',
    '/extensions/acme/pack/../../other-pack/theme.css',
    '/extensions/../api/config',
    '/extensions/../../etc/passwd',
    '/extensions/./acme/pack/theme.css',
    '/extensions/acme/%2e%2e/theme.css',      // the server decodes once, then routes
    '/extensions/..%2Fapi/config',
    '/extensions/acme/pack/theme.css?v=2',    // an asset is a file, not a request
    '/extensions/acme/pack/theme.css#x',
    '/extensions/acme/pack/theme\\.css',   // a backslash is not a separator here
];

describe('theme pre-paint URL rule', () => {
    afterEach(() => {
        document.getElementById('kanban-theme-stylesheet')?.remove();
        localStorage.removeItem(THEME_CSS_KEY);
    });

    it.each(ACCEPTED)('accepts the shape resolveAssetURL produces: %s', (url) => {
        expect(isPrepaintableThemeURL(url)).toBe(true);
    });

    it.each(REJECTED)('refuses %s', (url) => {
        expect(isPrepaintableThemeURL(url)).toBe(false);
    });

    it.each([...ACCEPTED.map((u) => [u, true]), ...REJECTED.map((u) => [u, false])] as Array<[string, boolean]>)(
        'the shipped script agrees with the predicate on %s',
        (url, allowed) => {
            const link = runPrePaint(url);
            if (allowed) {
                expect(link).toBeTruthy();
                expect(link!.getAttribute('href')).toBe(url);
            } else {
                expect(link).toBeNull();
            }
        },
    );

    it('paints nothing when no theme was cached', () => {
        expect(runPrePaint(null)).toBeNull();
    });
});
