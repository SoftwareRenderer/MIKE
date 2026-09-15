import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { alias } from './scripts/aliases.js';

export default {
    // The alias is declared here as well as in vite.config.ts because a language server and
    // svelte-check run this preprocessor outside Vite, so no Vite config is injected into it.
    // `configFile: false` keeps that path deterministic: without it the preprocessor loads whatever
    // Vite config sits at the process cwd, which for a language server is the workspace root.
    preprocess: vitePreprocess({ style: { configFile: false, resolve: { alias } } }),
    compilerOptions: {
        runes: true,
    },
};
