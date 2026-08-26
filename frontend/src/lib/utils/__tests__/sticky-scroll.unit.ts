import { describe, it, expect, afterEach } from 'vitest';
import { EditorView } from '@codemirror/view';
import { EditorState } from '@codemirror/state';
import {
    findLineElement,
    findStickyHeader,
    getHeaderLineHtml,
    sectionHeadersField,
    stickyScroll,
    updateHeadersEffect,
    type SectionHeader,
} from '../sticky-scroll';

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

describe('stickyScroll (real EditorView in jsdom)', () => {
    // Two function-like sections; section 1 spans lines 1–6, so the detach
    // margin (topLine < endLine − 3) lets the bar stick at the doc top.
    const DOC = [
        'function foo() {',
        '  const a = 1;',
        '  const b = 2;',
        '  const c = 3;',
        '  return a + b + c;',
        '}',
        'function bar() {',
        '  return 42;',
        '',
    ].join('\n');

    let parent: HTMLDivElement | undefined;
    let view: EditorView | undefined;

    /** Mount a minimal editor with the sticky-scroll plugin (git-gutter pattern). */
    function mount() {
        parent = document.createElement('div');
        document.body.appendChild(parent);
        view = new EditorView({
            state: EditorState.create({
                doc: DOC,
                extensions: [sectionHeadersField, stickyScroll()],
            }),
            parent,
        });
    }

    // Let any async re-renders settle (the bar insertion itself is synchronous
    // here — the parent is attached before the EditorView is constructed).
    const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

    const bar = () => view!.dom.querySelector('.cm-sticky-scroll') as HTMLElement | null;

    function setHeaders(headers: SectionHeader[]) {
        view!.dispatch({ effects: updateHeadersEffect.of(headers) });
    }

    afterEach(() => {
        view?.destroy();
        view = undefined;
        parent?.remove();
        parent = undefined;
    });

    it('shows the sticky bar with the header line text', async () => {
        mount();
        await flush();
        setHeaders([{ line: 1, endLine: 6 }]);
        await flush();

        const b = bar();
        expect(b).toBeTruthy();
        expect(b!.style.display).not.toBe('none');
        expect(b!.textContent).toContain('function foo');
    });

    it('re-renders when the header line is edited in place (same line number)', async () => {
        mount();
        await flush();
        setHeaders([{ line: 1, endLine: 6 }]);
        await flush();

        const b = bar()!;
        expect(b.textContent).toContain('function foo');

        // Rename the function identifier on the header line — line number is
        // unchanged, so the old (line-number-only) comparison missed this.
        const line1 = view!.state.doc.line(1);
        const from = line1.from + line1.text.indexOf('foo');
        view!.dispatch({ changes: { from, to: from + 3, insert: 'baz' } });
        await flush();

        expect(b.textContent).toContain('function baz');
        expect(b.textContent).not.toContain('foo');
    });

    it('does not re-render when an unrelated line changes (render cache hit)', async () => {
        mount();
        await flush();
        setHeaders([{ line: 1, endLine: 6 }]);
        await flush();

        const b = bar()!;
        const before = b.innerHTML;

        // Edit line 8 (inside the second function, not the header line).
        const line8 = view!.state.doc.line(8);
        const at = line8.from + line8.text.indexOf('42');
        view!.dispatch({ changes: { from: at, to: at + 2, insert: '7' } });
        await flush();

        expect(b.innerHTML).toBe(before);
    });

    it('ignores headers whose line is outside the document (no RangeError)', async () => {
        mount();
        await flush();
        // line 0 sits below the document (doc.line(0) would throw RangeError
        // in getHeaderLineHtml); line 999 sits beyond the last line.
        setHeaders([{ line: 0, endLine: 10 }, { line: 999, endLine: 999 }]);
        await flush();

        const b = bar();
        expect(b === null || (b.style.display === 'none' && b.innerHTML === '')).toBe(true);
    });
});
