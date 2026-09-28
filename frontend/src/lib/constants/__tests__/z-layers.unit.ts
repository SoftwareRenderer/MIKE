/**
 * The tier table exists twice on purpose — CSS custom properties for stylesheets, a TS object for
 * props and imperative DOM — so it must be checked against itself.
 *
 * Two failures are worth a test each. A value that drifts between the halves puts a component in a
 * rank the scale does not describe, which is invisible until something paints under something else.
 * And a numeric literal that survives in a component outranks its own tier silently: the grep that
 * used to be the review step is here instead, along with the ordering the whole scale assumes.
 */
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'fs';
import { join, relative } from 'path';
import { Z } from '../z-layers';

/** vitest runs with cwd=frontend. */
const SRC = join(process.cwd(), 'src');
const APP_CSS = join(SRC, 'app.css');

/** `--z-modal-stacked: 1100;` → ['modal-stacked', 1100]. The only numeric z-index allowed anywhere. */
function cssTiers(): Map<string, number> {
    const css = readFileSync(APP_CSS, 'utf8');
    const tiers = new Map<string, number>();
    for (const [, name, value] of css.matchAll(/--z-([a-z-]+):\s*(-?\d+);/g)) {
        expect(tiers.has(name), `app.css declares --z-${name} twice`).toBe(false);
        tiers.set(name, Number(value));
    }
    return tiers;
}

/** `Z.modalStacked` → 'modal-stacked': the two halves name the same rank in their own dialect. */
function tsTiers(): Map<string, number> {
    return new Map(Object.entries(Z).map(([key, value]) => [key.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`), value]));
}

/** Every source file that can declare a z-index. A test fixture is a string handed to the sanitizer,
 *  not a declaration, so `__tests__` is out — everything else in `src` is in. */
function sources(dir = SRC): string[] {
    return readdirSync(dir).flatMap((name) => {
        const path = join(dir, name);
        if (statSync(path).isDirectory()) return name === '__tests__' ? [] : sources(path);
        return /\.(svelte|css|ts)$/.test(name) ? [path] : [];
    });
}

describe('stacking tiers', () => {
    it('declares the same table in CSS and TypeScript', () => {
        expect(tsTiers()).toEqual(cssTiers());
    });

    it('keeps every tier distinct and in the order the names claim', () => {
        const values = Object.values(Z);
        expect(new Set(values).size).toBe(values.length);
        values.forEach((value, i) => {
            if (i > 0) expect(value, `${Object.keys(Z)[i]} must sit above the tier before it`).toBeGreaterThan(values[i - 1]);
        });
    });

    it('names a declared tier everywhere it is used', () => {
        const tiers = cssTiers();
        const undeclared = sources().flatMap((path) => Array.from(readFileSync(path, 'utf8').matchAll(/var\(--z-([a-z-]+)\)/g), (m) => `${relative(SRC, path)}: --z-${m[1]}`)
            .filter((entry) => !tiers.has(entry.split('--z-')[1])));
        expect(undeclared, `var(--z-…) with no token in app.css paints as z-index: auto\n${undeclared.join('\n')}`).toEqual([]);
    });

    it('owns every number: no z-index literal outside the token block in app.css', () => {
        const literal = /z-index:\s*-?\d|zIndex:\s*'?-?\d/;
        const offenders = sources().flatMap((path) => readFileSync(path, 'utf8').split('\n')
            .map((line, i) => ({ at: `${relative(SRC, path)}:${i + 1}`, line }))
            .filter(({ line }) => literal.test(line)));
        expect(offenders, offenders.map((o) => `${o.at} ${o.line.trim()}`).join('\n')).toEqual([]);
    });
});
