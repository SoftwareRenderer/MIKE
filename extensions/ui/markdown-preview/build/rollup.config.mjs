// Rollup config for the Wordgard browser bundle. Run by build/build-wordgard.mjs
// with WORDGARD_BUILD_DIR set to the build dir (a copy of the published
// `wordgard` package's dist plus bundle-entry.js). Reuses the rollup binaries
// from the frontend workspace (generic dev tooling); Wordgard itself and its
// dependencies resolve from this extension's own node_modules.
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

const buildDir = process.env.WORDGARD_BUILD_DIR;
if (!buildDir) { console.error('WORDGARD_BUILD_DIR is not set'); process.exit(1); }

// Extension root (parent of the build dir).
const extRoot = resolve(buildDir, '..', '..');
// Frontend workspace for the rollup binaries/plugins.
const frontendNodeModules = resolve(extRoot, '..', '..', '..', 'frontend', 'node_modules');
const require = createRequire(import.meta.url);
const nodeResolve = require(resolve(frontendNodeModules, '@rollup', 'plugin-node-resolve'));
const terser = require(resolve(frontendNodeModules, '@rollup', 'plugin-terser'));

/** Resolve Wordgard's internal cross-package imports to the build-dir dist. */
function wordgardResolver() {
    return {
        name: 'wordgard-resolver',
        resolveId(source) {
            const m = /^wordgard\/([\w]+)$/.exec(source);
            if (m) return resolve(buildDir, m[1] + '.js');
            return null;
        },
    };
}

export default {
    input: resolve(buildDir, 'bundle-entry.js'),
    output: {
        file: resolve(extRoot, 'lib', 'wordgard.js'),
        format: 'iife',
        sourcemap: false,
    },
    plugins: [
        wordgardResolver(),
        nodeResolve({ exportConditions: ['import', 'module', 'browser'] }),
        terser({ format: { comments: false } }),
    ],
};
