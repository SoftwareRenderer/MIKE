import { createHash } from 'node:crypto';
import type { Plugin } from 'vite';

// The id digests the asset URLs the built document loads, not the file's bytes: the meta tag below is
// part of the file, so a hash of the bytes could never equal a hash including them.
//
// `GET /api/version` reads this tag rather than recomputing the digest, so the grammar has one home.
// What Go mirrors is the tag's shape — see `assetBuildIDPattern` in backend/server/handler_version.go.

/** The script and stylesheet URLs Vite injects into the built document. */
const ASSET_URL = /(?:src|href)=["'](\/assets\/[^"']+\.(?:js|css))["']/g;

/** `null` when the document loads no hashed asset — `vite dev`, and the reason the client fallback
 *  cannot fire against a dev server. */
export function buildIdOf(html: string): string | null {
    const urls = [...html.matchAll(ASSET_URL)].map((m) => m[1]).sort();
    if (urls.length === 0) return null;
    return createHash('sha256').update(urls.join('\n')).digest('hex').slice(0, 12);
}

/** Stamps the built document with its own id. Runs after Vite has injected the asset tags. */
export function buildIdPlugin(): Plugin {
    return {
        name: 'mike-build-id',
        apply: 'build',
        enforce: 'post',
        transformIndexHtml(html) {
            const id = buildIdOf(html);
            if (id === null) return html;
            const tag = `<meta name="app-build" content="${id}">`;
            return html.replace(/<head\b[^>]*>/i, (head) => `${head}${tag}`);
        },
    };
}
