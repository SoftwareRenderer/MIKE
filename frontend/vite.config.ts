import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { VitePWA } from 'vite-plugin-pwa';
// The extension is load-bearing: `configLoader: 'native'` resolves specifiers verbatim.
import { buildIdPlugin } from './scripts/build-id.ts';
import { isMermaidChunk, mermaidChunksPlugin, MERMAID_CHUNK_PREFIX, MERMAID_PRECACHE_DENY } from './scripts/mermaid-chunks.ts';
import { alias } from './scripts/aliases.js';
import { isAppShellAsset, isExtensionAsset } from './scripts/sw-routes.ts';

export default defineConfig({
    plugins: [
        svelte(),
        buildIdPlugin(),
        mermaidChunksPlugin(),
        VitePWA({
            // No `autoUpdate`: that would set skipWaiting/clientsClaim and let the browser pick when a
            // new build takes over. services/app-update.ts releases a waiting worker itself.
            injectRegister: null,
            // Icons are in public/ — Vite auto-copies them to dist root
            includeAssets: [],
            manifest: {
                name: 'MIKE: Integrated Kanban Environment',
                short_name: 'MIKE',
                description: 'AI-powered project management with task automation',
                theme_color: '#141414',
                background_color: '#141414',
                display: 'standalone',
                orientation: 'any',
                scope: '/',
                start_url: '/',
                icons: [
                    {
                        src: 'pwa-icon-192.png',
                        sizes: '192x192',
                        type: 'image/png',
                        purpose: 'any',
                    },
                    {
                        src: 'pwa-icon-512.png',
                        sizes: '512x512',
                        type: 'image/png',
                        purpose: 'any',
                    },
                ],
            },
            workbox: {
                globPatterns: ['**/*.{js,css,html,ico,png,svg}'],
                // The diagram renderer is fetched the first time a diagram appears; precaching it would
                // put several megabytes into every install, whatever that install's chats contain.
                globIgnores: [MERMAID_PRECACHE_DENY],
                // The generated worker has no click handler of its own; this adds the one that opens what
                // a notification was about.
                importScripts: ['/sw-notifications.js'],
                runtimeCaching: [
                    {
                        // Extension assets (served from disk by the Go server, not part
                        // of the build). Same-origin only. NetworkFirst so a security fix
                        // shipped in an extension update reaches the client immediately,
                        // with the cache as the offline fallback (not the other way round:
                        // CacheFirst would pin a revoked/fixed extension for the whole TTL).
                        urlPattern: isExtensionAsset,
                        handler: 'NetworkFirst',
                        options: {
                            cacheName: 'kanban-extensions',
                            networkTimeoutSeconds: 3,
                            expiration: {
                                maxEntries: 50,
                                maxAgeSeconds: 7 * 24 * 60 * 60, // 7 days
                            },
                            // 200 only: never cache an opaque (status 0) response.
                            cacheableResponse: {
                                statuses: [200],
                            },
                        },
                    },
                    {
                        // Cache the app shell (JS, CSS, assets) — skip API calls entirely.
                        // Same-origin only, and no opaque responses: a CacheFirst entry
                        // keyed by URL would otherwise serve a cross-origin opaque body to
                        // any later request for the same URL, for the full TTL.
                        urlPattern: isAppShellAsset,
                        handler: 'CacheFirst',
                        options: {
                            cacheName: 'kanban-app-shell',
                            expiration: {
                                maxEntries: 50,
                                maxAgeSeconds: 60 * 60 * 24 * 30, // 30 days
                            },
                            cacheableResponse: {
                                statuses: [200],
                            },
                        },
                    },
                ],
                navigateFallback: '/index.html',
                navigateFallbackDenylist: [/^\/api/],
            },
        }),
    ],
    resolve: {
        alias,
    },
    build: {
        // Performance budgets (gzipped sizes)
        chunkSizeWarningLimit: 500,
        rollupOptions: {
            output: {
                // Renderer chunks get one name so the precache deny list has one pattern to refuse; the
                // rule that decides is in scripts/mermaid-chunks.ts.
                chunkFileNames(info: unknown) {
                    const chunk = (info as { chunk?: { moduleIds?: string[] } }).chunk ?? (info as { moduleIds?: string[] });
                    return isMermaidChunk(chunk.moduleIds ?? [])
                        ? `assets/${MERMAID_CHUNK_PREFIX}[hash].js`
                        : 'assets/[name]-[hash].js';
                },
                manualChunks(id) {
                    // Split CodeMirror language packages into separate chunks
                    // so they're only loaded when a diff of that language is viewed.
                    if (id.includes('/@codemirror/lang-') || id.includes('/@replit/codemirror-lang-svelte')) {
                        return 'codemirror-langs';
                    }
                    // CodeMirror core + theme — only needed by FileEditor / BranchEditor
                    if (
                        id.includes('/codemirror/') ||
                        id.includes('/@codemirror/') ||
                        id.includes('/@lezer/')
                    ) {
                        return 'codemirror-core';
                    }
                    // xterm — only needed by TerminalPanel
                    if (id.includes('/@xterm/')) {
                        return 'xterm';
                    }
                    // marked — only needed for chat message rendering
                    if (id.includes('/marked/')) {
                        return 'marked';
                    }
                    // diff — only needed for diff viewers
                    if (id.includes('/diff/') || id.includes('diff-parser') || id.includes('diff-gutter')) {
                        return 'diff';
                    }
                },
            },
        },
    },
    server: {
        port: 5173,
        proxy: {
            '/api': {
                target: 'http://localhost:3000',
                changeOrigin: true,
            },
        },
    },
});
