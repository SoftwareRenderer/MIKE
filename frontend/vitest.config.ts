import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import path from 'path';

export default defineConfig({
    plugins: [svelte()],
    resolve: {
        alias: {
            $lib: path.resolve('./src/lib'),
        },
        conditions: ['browser'],
    },
    test: {
        // Every unit file gets a browser. A file declares its way out above its imports
        // (`// @vitest-environment node`); an import graph that reaches a browser global through a `typeof`
        // guard runs the branch no browser takes. `docs/testing.md` owns the rule and its exceptions.
        environment: 'jsdom',
        // Worker threads rather than child processes: `isolate` stays on, so a file still gets a fresh module
        // registry and only the fork per file goes.
        pool: 'threads',
        include: ['src/**/*.unit.ts', 'src/**/*.unit.svelte.ts', 'src/**/*.unit.spec.ts'],
        setupFiles: ['src/lib/__tests__/vitest-setup.ts'],
    },
});
