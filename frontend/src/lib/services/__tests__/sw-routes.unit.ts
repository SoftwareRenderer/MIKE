/**
 * The route table the generated service worker is built from.
 *
 * A URL the extension-asset matcher claims is answered `NetworkFirst`: the worker
 * tries the network for a few seconds and otherwise returns the body it stored
 * earlier. On an API answer that is not a stale file but a wrong list, so the
 * partition these tests pin is the one the app's own endpoints depend on —
 * `/api/extensions/...` reads like the extension tree and must not be taken for it.
 */
import { describe, it, expect } from 'vitest';
import { isAppShellAsset, isExtensionAsset } from '../../../../scripts/sw-routes';

/** The worker and the page share an origin, so every case below is written against it. */
function under(path: string): { url: URL } {
    return { url: new URL(path, self.location.origin) };
}

function crossOrigin(path: string): { url: URL } {
    return { url: new URL(path, 'https://elsewhere.example') };
}

describe('isExtensionAsset', () => {
    it('takes the file-server tree the extension assets are served from', () => {
        for (const path of ['/extensions/', '/extensions/acme/pack/index.js', '/extensions/acme/pack/vendor/x.css']) {
            expect(isExtensionAsset(under(path))).toBe(true);
        }
    });

    it('refuses every extension API endpoint, however the path is shaped', () => {
        for (const path of [
            '/api/extensions',
            '/api/extensions/registry',
            '/api/extensions/local',
            '/api/extensions/check-updates',
            '/api/extensions/ext-1',
            '/api/extensions/ext-1/storage',
            '/api/extensions/ext-1/proxy',
        ]) {
            expect(isExtensionAsset(under(path))).toBe(false);
        }
    });

    it('refuses another origin, which the worker has no business caching', () => {
        expect(isExtensionAsset(crossOrigin('/extensions/acme/pack/index.js'))).toBe(false);
    });
});

describe('isAppShellAsset', () => {
    it('takes the build output and nothing the server answers dynamically', () => {
        for (const path of ['/index.html', '/assets/index-abc123.js', '/pwa-icon-192.png']) {
            expect(isAppShellAsset(under(path))).toBe(true);
        }
        for (const path of ['/api/version', '/api/extensions/registry', '/extensions/acme/pack/index.js']) {
            expect(isAppShellAsset(under(path))).toBe(false);
        }
        expect(isAppShellAsset(crossOrigin('/assets/index-abc123.js'))).toBe(false);
    });
});

describe('the route table', () => {
    it('claims a URL at most once, because workbox answers with the first match', () => {
        for (const path of [
            '/index.html',
            '/assets/index-abc123.js',
            '/extensions/acme/pack/index.js',
            '/api/extensions',
            '/api/extensions/registry',
            '/api/extensions/ext-1/storage',
            '/api/tasks',
        ]) {
            const request = under(path);
            expect(isExtensionAsset(request) && isAppShellAsset(request)).toBe(false);
        }
    });
});
