import { describe, it, expect, afterEach } from 'vitest';
import { EditorView } from '@codemirror/view';
import { EditorState, type Extension } from '@codemirror/state';
import { javascript } from '@codemirror/lang-javascript';
import {
    buildSectionHeaders,
    findLineElement,
    findLoopScopes,
    findStickyStack,
    getHeaderLineHtml,
    sectionHeadersField,
    stickyScroll,
    updateHeadersEffect,
    type SectionHeader,
} from '../sticky-scroll';

describe('findLineElement', () => {
    it('returns the nearest ancestor with the .cm-line class', () => {
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

    it('does NOT match .cm-lineWrapping (avoids false positives)', () => {
        const content = document.createElement('div');
        content.className = 'cm-content cm-lineWrapping';
        // Simulate a gap node (common when header is above viewport).
        const gap = document.createElement('div');
        gap.className = 'cm-gap';
        gap.style.height = '100px';
        content.appendChild(gap);
        document.body.appendChild(content);
        try {
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

describe('findStickyStack', () => {
    // Fake view: the top visible line is resolved from the document position
    // at the viewport top — so it is exact regardless of (wrapped) line heights.
    function fakeView(topLineNumber: number) {
        return {
            state: { doc: { lineAt: () => ({ number: topLineNumber }) } },
            viewport: { from: 0, to: 1 },
        } as any;
    }

    it('returns the chain of enclosing scopes outermost → innermost', () => {
        const headers: SectionHeader[] = [
            { line: 1, endLine: 20, depth: 0 }, // class Foo
            { line: 2, endLine: 15, depth: 1 }, // method bar
            { line: 3, endLine: 8, depth: 2 },  // for loop
        ];
        expect(findStickyStack(fakeView(4), headers).map((h) => h.line)).toEqual([1, 2, 3]);
    });

    it('shows the function header above the loop header (stacked)', () => {
        const headers: SectionHeader[] = [
            { line: 1, endLine: 12, depth: 0 }, // function foo
            { line: 2, endLine: 9, depth: 1 },  // for loop
        ];
        const stack = findStickyStack(fakeView(4), headers);
        expect(stack.map((h) => h.line)).toEqual([1, 2]); // function bar above loop bar
    });

    it('returns [] when the top line is above any header', () => {
        const headers: SectionHeader[] = [{ line: 5, endLine: 10, depth: 0 }];
        expect(findStickyStack(fakeView(1), headers)).toEqual([]);
    });

    it('returns [] for empty headers or positions beyond the last section', () => {
        const headers: SectionHeader[] = [{ line: 5, endLine: 10, depth: 0 }];
        expect(findStickyStack(fakeView(5), [])).toEqual([]);
        // topLine beyond the last section's end → nothing pins.
        expect(findStickyStack(fakeView(11), headers)).toEqual([]);
    });

    it('detaches a flat section within the margin before the next header', () => {
        const sections: SectionHeader[] = [
            { line: 10, endLine: 24, depth: 0 }, // detach margin 3 → sticks while topLine < 21
            { line: 25, endLine: 40, depth: 0 },
        ];
        expect(findStickyStack(fakeView(20), sections).map((h) => h.line)).toEqual([10]); // within margin
        expect(findStickyStack(fakeView(21), sections)).toEqual([]); // in the gap
        expect(findStickyStack(fakeView(24), sections)).toEqual([]);
        expect(findStickyStack(fakeView(25), sections).map((h) => h.line)).toEqual([25]); // next section pins
    });

    it('detaches only the innermost bar near its end, keeping ancestors', () => {
        const headers: SectionHeader[] = [
            { line: 1, endLine: 20, depth: 0 }, // function foo
            { line: 2, endLine: 5, depth: 1 },  // for loop (ends line 5)
        ];
        // topLine 4 is within the detach margin (endLine 5 − 3 = 2 → detach when >= 2),
        // so the loop bar yields but the enclosing function remains.
        expect(findStickyStack(fakeView(4), headers).map((h) => h.line)).toEqual([1]);
    });

    it('caps the stack at MAX_STICKY_DEPTH (outermost dropped)', () => {
        const headers: SectionHeader[] = [
            { line: 1, endLine: 100, depth: 0 }, // class
            { line: 2, endLine: 90, depth: 1 },  // fn
            { line: 3, endLine: 80, depth: 2 },  // loop
            { line: 4, endLine: 70, depth: 3 },  // loop
            { line: 5, endLine: 60, depth: 4 },  // loop
            { line: 6, endLine: 50, depth: 5 },  // loop
        ];
        const stack = findStickyStack(fakeView(20), headers);
        expect(stack.length).toBe(4);
        expect(stack[0].line).toBe(3); // innermost 4 kept, outermost 1 & 2 dropped
        expect(stack[3].line).toBe(6);
    });

    it('handles the function → loop → nested loop case', () => {
        const headers: SectionHeader[] = [
            { line: 1, endLine: 30, depth: 0 }, // function
            { line: 2, endLine: 20, depth: 1 },  // outer loop
            { line: 3, endLine: 10, depth: 2 },  // inner loop
        ];
        const stack = findStickyStack(fakeView(5), headers);
        expect(stack.map((h) => h.line)).toEqual([1, 2, 3]);
        // When the inner loop ends, the stack reduces to the outer scopes.
        expect(findStickyStack(fakeView(15), headers).map((h) => h.line)).toEqual([1, 2]);
        expect(findStickyStack(fakeView(25), headers).map((h) => h.line)).toEqual([1]);
    });
});

describe('buildSectionHeaders', () => {
    it('merges declarations and loops with containment depth and full endLine', () => {
        const headers = buildSectionHeaders(
            [
                { line: 1, endLine: 20 }, // function foo (full body)
                { line: 6, endLine: 15 }, // method inside foo
            ],
            [
                { line: 2, endLine: 5 },  // for loop inside foo, before method
                { line: 7, endLine: 9 },  // loop inside method
            ],
        );
        expect(headers).toEqual([
            { line: 1, endLine: 20, depth: 0 }, // foo
            { line: 2, endLine: 5, depth: 1 },  // loop
            { line: 6, endLine: 15, depth: 1 }, // method (sibling of loop)
            { line: 7, endLine: 9, depth: 2 },  // nested loop
        ]);
    });

    it('does not truncate a parent to its first child (keeps full body range)', () => {
        const headers = buildSectionHeaders(
            [{ line: 1, endLine: 30 }], // function spans 1..30
            [{ line: 2, endLine: 5 }],  // loop early in the body
        );
        expect(headers[0].endLine).toBe(30); // NOT truncated to 1
    });
});

describe('findLoopScopes', () => {
    function loopView(code: string, language: () => Extension) {
        const parent = document.createElement('div');
        document.body.appendChild(parent);
        const view = new EditorView({
            state: EditorState.create({ doc: code, extensions: [language()] }),
            parent,
        });
        return { view, parent };
    }

    afterEach(() => {
        document.body.querySelectorAll('div').forEach((d) => d.remove());
    });

    it('finds for/while/do loops in JavaScript', () => {
        const { view, parent } = loopView(
            'function f() {\n' +
            '  for (let i = 0; i < 3; i++) { x(); }\n' +
            '  while (a) { y(); }\n' +
            '  do { z(); } while (b);\n' +
            '  for (const k in o) {}\n' +
            '}',
            javascript,
        );
        try {
            const scopes = findLoopScopes(view);
            expect(scopes).toEqual([
                { line: 2, endLine: 2 },
                { line: 3, endLine: 3 },
                { line: 4, endLine: 4 },
                { line: 5, endLine: 5 },
            ]);
        } finally {
            view.destroy();
            parent.remove();
        }
    });

    it('returns the full loop body extent (multi-line)', () => {
        const { view, parent } = loopView(
            'function f() {\n' +
            '  for (let i = 0; i < 3; i++) {\n' +
            '    console.log(i);\n' +
            '  }\n' +
            '}',
            javascript,
        );
        try {
            const scopes = findLoopScopes(view);
            expect(scopes).toEqual([{ line: 2, endLine: 4 }]);
        } finally {
            view.destroy();
            parent.remove();
        }
    });

    it('does not record loop sub-clauses (ForSpec/ForInSpec) as separate scopes', () => {
        const { view, parent } = loopView(
            'function f() {\n' +
            '  for (let i = 0; i < 3; i++) {}\n' +
            '  for (const k in o) {}\n' +
            '}',
            javascript,
        );
        try {
            // Only two statement-level loops, not 4 (no ForSpec/ForInSpec dupes).
            expect(findLoopScopes(view)).toHaveLength(2);
        } finally {
            view.destroy();
            parent.remove();
        }
    });

    it('returns [] when there are no loops', () => {
        const { view, parent } = loopView('function f() { return 1 + 1; }', javascript);
        try {
            expect(findLoopScopes(view)).toEqual([]);
        } finally {
            view.destroy();
            parent.remove();
        }
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
            const html = getHeaderLineHtml(view, { line: 1, endLine: 2, depth: 0 });
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
            const html = getHeaderLineHtml(view, { line: 1, endLine: 2, depth: 0 });
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
        const html = getHeaderLineHtml(view, { line: 1, endLine: 2, depth: 0 });
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
        setHeaders([{ line: 1, endLine: 6, depth: 0 }]);
        await flush();

        const b = bar();
        expect(b).toBeTruthy();
        expect(b!.style.display).not.toBe('none');
        expect(b!.textContent).toContain('function foo');
    });

    it('re-renders when the header line is edited in place (same line number)', async () => {
        mount();
        await flush();
        setHeaders([{ line: 1, endLine: 6, depth: 0 }]);
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
        setHeaders([{ line: 1, endLine: 6, depth: 0 }]);
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
        setHeaders([{ line: 0, endLine: 10, depth: 0 }, { line: 999, endLine: 999, depth: 0 }]);
        await flush();

        const b = bar();
        expect(b === null || (b.style.display === 'none' && b.innerHTML === '')).toBe(true);
    });
});
