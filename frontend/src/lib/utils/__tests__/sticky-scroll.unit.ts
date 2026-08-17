import { describe, it, expect } from 'vitest';
import { findLineElement, findStickyHeader, getHeaderLineHtml, type SectionHeader } from '../sticky-scroll';

describe('findLineElement', () => {
    it('returns the nearest ancestor with the exact cm-line class', () => {
        const content = document.createElement('div');
        content.className = 'cm-content cm-lineWrapping';
        const line = document.createElement('div');
        line.className = 'cm-line';
        const span = document.createElement('span');
        span.className = 'ͼo';
        line.appendChild(span);
        content.appendChild(line);
        document.body.appendChild(content);
        try {
            expect(findLineElement(span)).toBe(line);
            expect(findLineElement(line)).toBe(line);
        } finally {
            content.remove();
        }
    });

    it('does NOT match the content element carrying cm-lineWrapping (wrap-on false positive)', () => {
        const content = document.createElement('div');
        content.className = 'cm-content cm-lineWrapping';
        // Simulate a culled-line (gap) node inside the content element —
        // what view.domAtPos lands on when the header line is above the viewport.
        const gap = document.createElement('div');
        gap.className = 'cm-gap';
        gap.style.height = '100px';
        content.appendChild(gap);
        document.body.appendChild(content);
        try {
            // Old behavior: includes('cm-line') matched cm-lineWrapping here,
            // so the entire content element was cloned into the sticky header.
            expect(findLineElement(gap)).toBeNull();
        } finally {
            content.remove();
        }
    });

    it('returns null when no line element is reachable within the walk depth', () => {
        const orphan = document.createElement('div');
        orphan.className = 'cm-gap';
        document.body.appendChild(orphan);
        try {
            expect(findLineElement(orphan)).toBeNull();
        } finally {
            orphan.remove();
        }
    });
});

describe('findStickyHeader', () => {
    const headers: SectionHeader[] = [
        { line: 10, endLine: 20 },
        { line: 21, endLine: 31 },
        { line: 32, endLine: 120 },
    ];

    // Fake view: the top visible line is resolved from the document position
    // at the viewport top — so it is exact regardless of (wrapped) line heights.
    function fakeView(topLineNumber: number) {
        return {
            state: { doc: { lineAt: () => ({ number: topLineNumber }) } },
            viewport: { from: 0, to: 1 },
        } as any;
    }

    it('resolves the section by the document line at the viewport top', () => {
        expect(findStickyHeader(fakeView(15), headers)?.line).toBe(10);
        expect(findStickyHeader(fakeView(25), headers)?.line).toBe(21);
        expect(findStickyHeader(fakeView(99), headers)?.line).toBe(32);
    });

    it('returns undefined for empty headers or positions beyond the last section', () => {
        expect(findStickyHeader(fakeView(5), [])).toBeUndefined();
        expect(findStickyHeader(fakeView(5), headers)).toBeUndefined();
        expect(findStickyHeader(fakeView(120), headers)).toBeUndefined();
    });
});

describe('getHeaderLineHtml', () => {
    it('falls back to escaped plain text when no cm-line element is reachable (no content-clone bloat)', () => {
        const content = document.createElement('div');
        content.className = 'cm-content cm-lineWrapping';
        const gap = document.createElement('div');
        gap.className = 'cm-gap';
        content.appendChild(gap);
        // Sibling line that must NOT leak into the header via a content clone.
        const sibling = document.createElement('div');
        sibling.className = 'cm-line';
        sibling.innerHTML = '<span class="ͼo">func</span> somethingElse';
        content.appendChild(sibling);
        document.body.appendChild(content);

        const view = {
            state: { doc: { line: () => ({ text: 'func Foo(x int) {}', from: 0 }) } },
            domAtPos: () => ({ node: gap, offset: 0 }),
        } as any;
        try {
            const html = getHeaderLineHtml(view, { line: 1, endLine: 2 });
            expect(html).toBe('func Foo(x int) {}');
            expect(html).not.toContain('cm-line');
            expect(html).not.toContain('somethingElse');
        } finally {
            content.remove();
        }
    });

    it('uses the highlighted line clone when the header line is in the DOM', () => {
        const line = document.createElement('div');
        line.className = 'cm-line';
        line.innerHTML = '<span class="ͼo">func</span> <span class="ͼr">Foo</span>(x int) {}';
        document.body.appendChild(line);
        const view = {
            state: { doc: { line: () => ({ text: 'func Foo(x int) {}', from: 0 }) } },
            domAtPos: () => ({ node: line, offset: 0 }),
        } as any;
        try {
            const html = getHeaderLineHtml(view, { line: 1, endLine: 2 });
            expect(html).toContain('class="ͼo"');
            expect(html).toContain('func');
            // The wrapper element itself must not be part of the header HTML.
            expect(html).not.toContain('cm-line');
        } finally {
            line.remove();
        }
    });

    it('preserves leading indentation in the plain fallback', () => {
        const view = {
            state: { doc: { line: () => ({ text: '\tfunc Bar() {}', from: 0 }) } },
            domAtPos: () => null,
        } as any;
        const html = getHeaderLineHtml(view, { line: 1, endLine: 2 });
        expect(html.startsWith('\tfunc Bar() {}')).toBe(true);
    });
});
