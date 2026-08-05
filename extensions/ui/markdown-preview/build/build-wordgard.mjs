#!/usr/bin/env node
/**
 * Build the single-file Wordgard browser bundle used by this extension, from the
 * published `wordgard` npm package (declared in this extension's package.json).
 *
 * The extension iframe loads a plain <script> (sandboxed, same-origin, CSP
 * `script-src 'self'`), so it cannot import the ESM package directly. This
 * bundles the package's dist into an IIFE written to ../lib/wordgard.js.
 *
 * Steps:
 *   1. Copy node_modules/wordgard/dist into the transient build/.dist dir.
 *   2. Apply a small documented patch to the Link mark so its readElement reads
 *      the raw href attribute instead of the URL-resolved `a.href` (which adds
 *      trailing slashes / resolves relative links, breaking markdown fidelity).
 *   3. Run rollup (reused from the frontend workspace) over bundle-entry.js to
 *      emit the IIFE bundle.
 *
 * Requires `npm install` in this extension dir (installs wordgard + its deps).
 * Run: cd extensions/ui/markdown-preview && npm run build
 */
import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const extRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const wgPkg = resolve(extRoot, 'node_modules', 'wordgard');
const outFile = resolve(extRoot, 'lib', 'wordgard.js');
const buildDir = resolve(extRoot, 'build', '.dist');

if (!existsSync(resolve(wgPkg, 'package.json'))) {
    console.error('wordgard not installed. Run `npm install` in this extension dir first.');
    process.exit(1);
}

// --- 1. Fresh copy of the npm dist ---
rmSync(buildDir, { recursive: true, force: true });
mkdirSync(buildDir, { recursive: true });
cpSync(resolve(wgPkg, 'dist'), buildDir, { recursive: true });
console.log('[build-wordgard] copied dist →', buildDir);

// --- 2. Patch the Link mark readElement ---
const typesFile = resolve(buildDir, 'types.js');
const typesSrc = readFileSync(typesFile, 'utf8');
const from = 'readElement: dom => dom.href';
const to = 'readElement: dom => dom.getAttribute("href")';
if (!typesSrc.includes(from)) {
    console.error('Link readElement pattern not found — the wordgard patch target changed; please re-check.');
    process.exit(1);
}
writeFileSync(typesFile, typesSrc.split(from).join(to));
console.log('[build-wordgard] patched Link readElement → raw href attribute');

// --- 3. Copy bundle entry and run rollup ---
cpSync(resolve(extRoot, 'build', 'bundle-entry.js'), resolve(buildDir, 'bundle-entry.js'));
const rollup = resolve(extRoot, '..', '..', '..', 'frontend', 'node_modules', '.bin', 'rollup');
if (!existsSync(rollup)) {
    console.error('rollup not found in frontend workspace. Run `cd frontend && npm install` first.');
    process.exit(1);
}
console.log('[build-wordgard] bundling IIFE →', outFile);
execFileSync(rollup, ['-c', resolve(extRoot, 'build', 'rollup.config.mjs')], {
    env: { ...process.env, WORDGARD_BUILD_DIR: buildDir },
    stdio: 'inherit',
});

console.log('[build-wordgard] done.');
