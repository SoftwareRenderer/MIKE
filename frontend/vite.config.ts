import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { VitePWA } from 'vite-plugin-pwa';
import path from 'path';

export default defineConfig({
    plugins: [
        svelte(),
        VitePWA({
            registerType: 'autoUpdate',
            // Icons are in public/ — Vite auto-copies them to dist root
            includeAssets: [],
            manifest: {
                name: 'Kanban Board',
                short_name: 'Kanban',
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
                        // Cache the app shell (JS, CSS, assets) — skip API calls entirely
                        urlPattern: ({ url }) => {
                            const href = url.href;
                            // Never cache or intercept API requests — let them fail naturally
                            // so the offline queue in queue.ts takes over
                            if (href.includes('/api/')) return false;
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
