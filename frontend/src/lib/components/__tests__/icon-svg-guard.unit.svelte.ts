import { describe, it, expect, vi, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';
import Icon from '../Icon.svelte';
import shieldIcon from '$lib/assets/icons/shield.svg?raw';
import { singleSvgMarkup } from '$lib/utils/sanitize';
import { htmlHazards } from '$lib/utils/__tests__/html-hazards';

// Icon.svelte renders its `src` prop with `{@html}`. The reason that was ever safe
// is a fact about its call sites (26 of them, across 13 files, each one an
// `*.svg?raw` import), not about the component — so the component now checks the shape
// itself, and this file is what proves the check bites. `html-sinks.py` records the
// guard by name; renaming `singleSvgMarkup` there is a gate failure, and so is dropping
// it here.

describe('singleSvgMarkup', () => {
    it('accepts a real icon file, untouched', () => {
        const out = singleSvgMarkup(shieldIcon);
        expect(out).toMatch(/^<svg\b/);
        expect(out).toContain('</svg>');
        expect(out).toContain('viewBox');
        // The attributes an icon is drawn with survive; only `style` would not.
        expect(out).toContain('stroke="currentColor"');
    });

    it.each([
        ['an svg plus a second element', '<svg></svg><img src=x onerror=alert(1)>'],
        ['a script pretending to be an icon', '<script>alert(1)</script>'],
        ['plain html', '<div>not an icon</div>'],
        ['text beside the svg', '<svg></svg>oops'],
        ['two svg roots', '<svg></svg><svg></svg>'],
        ['empty', ''],
        ['whitespace', '   '],
    ])('refuses %s — it is not one <svg> root', (_name, src) => {
        expect(singleSvgMarkup(src)).toBe('');
    });

    it.each([
        ['an svg carrying a handler', '<svg onload="alert(1)"><path d="M1 1"/></svg>'],
        ['an svg wrapping a foreignObject', '<svg><foreignObject><b>x</b></foreignObject></svg>'],
        ['an svg carrying a style', '<svg style="position:fixed"><path d="M1 1"/></svg>'],
    ])('keeps %s only after the payload is gone', (_name, src) => {
        const out = singleSvgMarkup(src);
        expect(out).toMatch(/^<svg\b/);
        expect(out).not.toMatch(/onload|foreignObject|style/i);
    });

    it('keeps only what the sanitizer kept', () => {
        expect(singleSvgMarkup('<svg><style>*{position:fixed}</style></svg>')).not.toContain('position:fixed');
    });

    // The refusal above is about what sits *beside* the root. An icon is free to draw
    // its own glyphs — `<text>` is ordinary SVG — and a guard that blanked it would
    // fail an icon file rather than a payload, with only a dev-console line to show.
    it.each([
        ['a <text> glyph', '<svg xmlns="http://www.w3.org/2000/svg"><text x="1" y="2">AB</text></svg>'],
        ['a <tspan> inside a group', '<svg xmlns="http://www.w3.org/2000/svg"><g><tspan>hi</tspan></g></svg>'],
    ])('accepts %s — the text is the icon’s own', (_name, src) => {
        const out = singleSvgMarkup(src);
        expect(out).toMatch(/^<svg\b/);
        expect(htmlHazards(out)).toEqual([]);
    });

    it('still refuses the text beside the root even when the icon itself has text', () => {
        expect(singleSvgMarkup('<svg xmlns="http://www.w3.org/2000/svg"><text>AB</text></svg>oops')).toBe('');
    });
});

describe('Icon', () => {
    // Spy hygiene: vi.spyOn on an already-spied console.warn returns the same mock,
    // so without the restore the refusal logged by one case is still "pending" when
    // the next case asserts that a real icon says nothing.
    afterEach(() => vi.restoreAllMocks());

    it('renders a build-time icon import', () => {
        const { container } = render(Icon, { src: shieldIcon });
        expect(container.querySelector('span.icon svg')).toBeTruthy();
    });

    it('renders nothing for markup that is not a lone svg', () => {
        const { container } = render(Icon, { src: '<svg></svg><img src=x onerror=alert(1)>' });
        expect(container.querySelector('span.icon')?.innerHTML).toBe('');
    });

    it('renders nothing for a script', () => {
        const { container } = render(Icon, { src: '<script>steal()</script>' });
        expect(container.querySelector('span.icon')?.innerHTML).toBe('');
    });

    it('tells the developer why an icon is missing', () => {
        // Failing closed is only kind if it is also loud. Under vitest DEV is true, so
        // a refused src is reported once per mount.
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        render(Icon, { src: '<div>not an icon</div>' });
        expect(warn.mock.calls.map(c => String(c[0])).join(' '))
            .toContain('[Icon] src is not a single <svg> element');
    });

    it('says nothing about a real icon', () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        render(Icon, { src: shieldIcon });
        expect(warn).not.toHaveBeenCalled();
    });
});
