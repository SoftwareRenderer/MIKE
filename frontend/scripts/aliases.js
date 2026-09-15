// The one alias map. Two readers, on purpose: `vite.config.ts` resolves the build with it, and
// `svelte.config.js` hands it to the preprocessor that every consumer outside Vite uses (a language
// server, svelte-check). Paths are resolved against this file, never against `process.cwd()`: a
// language server's cwd is the workspace root, not `frontend/`, and a cwd-derived path silently
// points at `<workspace-root>/src/lib` from there.
import path from 'node:path';

export const alias = {
    $lib: path.resolve(import.meta.dirname, '../src/lib'),
};
