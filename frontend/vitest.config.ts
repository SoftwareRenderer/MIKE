import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import path from 'path';

export default defineConfig({
    plugins: [svelte()],
    resolve: {
        alias: {
            $lib: path.resolve('./src/lib'),
        },
    },
    test: {
        environment: 'jsdom',
        include: ['src/**/*.unit.ts', 'src/**/*.unit.svelte.ts', 'src/**/*.unit.spec.ts'],
    },
});
