import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { VitePWA } from 'vite-plugin-pwa';
import path from 'path';

export default defineConfig({
    plugins: [
        svelte(),
        VitePWA({
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
                runtimeCaching: [
                    {
                        // Extension assets (served from disk by the Go server, not part
                        // of the build) — shorter TTL so registry updates (incl. security
                        // fixes shipped in an extension) reach clients within a week.
                        urlPattern: ({ url }) => url.pathname.includes('/extensions/'),
                        handler: 'CacheFirst',
                        options: {
                            cacheName: 'kanban-extensions',
                            expiration: {
                                maxEntries: 50,
                                maxAgeSeconds: 7 * 24 * 60 * 60, // 7 days
                            },
                            cacheableResponse: {
                                statuses: [0, 200],
                            },
                        },
                    },
                    {
                        // Cache the app shell (JS, CSS, assets) — skip API calls entirely
                        urlPattern: ({ url }) => {
                            const href = url.href;
                            // Never cache or intercept API requests — let them fail naturally
                            // so the offline queue in queue.ts takes over
                            if (href.includes('/api/')) return false;
                            // Extension assets have their own (shorter-TTL) rule above
                            if (url.pathname.includes('/extensions/')) return false;
                            return true;
                        },
                        handler: 'CacheFirst',
                        options: {
                            cacheName: 'kanban-shell',
                            expiration: {
                                maxEntries: 50,
                                maxAgeSeconds: 60 * 60 * 24 * 30, // 30 days
                            },
                            cacheableResponse: {
                                statuses: [0, 200],
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
        alias: {
            $lib: path.resolve('./src/lib'),
        },
    },
    build: {
        // Performance budgets (gzipped sizes)
        chunkSizeWarningLimit: 500,
        rollupOptions: {
            output: {
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
