import { describe, it, expect } from 'vitest';
import { singleSvgMarkup } from '$lib/utils/sanitize';

// `icon-svg-guard.unit.svelte.ts` proves `singleSvgMarkup` refuses what is not an icon; this is the other
// half — that every shipped icon passes it. `Icon` renders a refused shape as nothing at all, so a bad
// edit silences every button naming that file, which is why the whole folder is asserted.
const assets = import.meta.glob('../../assets/icons/*.svg', {
    query: 'raw',
    import: 'default',
    eager: true,
}) as Record<string, string>;

describe('the shipped icon assets', () => {
    it('are found by the glob at all — an emptied folder passes vacuously', () => {
        expect(Object.keys(assets).length).toBeGreaterThan(0);
    });

    it.each(Object.entries(assets))('%s renders as one <svg> root', (_path, markup) => {
        const out = singleSvgMarkup(markup);
        expect(out).toMatch(/^<svg\b/);
        expect(out).toContain('viewBox');
    });
});
