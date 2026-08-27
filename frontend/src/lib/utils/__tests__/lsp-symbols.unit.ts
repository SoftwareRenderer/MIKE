import { describe, it, expect, vi } from 'vitest';
import { requestLSPSymbols, flattenSymbols } from '$lib/utils/lsp-symbols';

/**
 * The LSP documentSymbol response is the ONLY source for sticky-scroll
 * declaration scopes, so these tests pin the range mapping that makes the
 * sticky bar accurate:
 *   line    = selectionRange.start.line + 1  (where the header text is)
 *   endLine = range.end.line + 1             (end of the FULL body)
 * A full-body endLine is what keeps the header pinned while the viewport
 * top is inside the body (e.g. header on line 10, body to line 40, viewport
 * top at line 12 → header still shown).
 */

// LSP fixture for a document like:
//   line 10: class Foo {                  (0-based line 9)
//   line 12:   method bar() { ... }       (0-based line 11)
//   line 15:   }                          (bar body ends, 0-based line 14)
//   line 30: }                            (Foo body ends, 0-based line 29)
function makeDocumentSymbolFixture() {
    return [
        {
            name: 'Foo',
            kind: 5, // class
            selectionRange: { start: { line: 9, character: 6 }, end: { line: 9, character: 9 } },
            range: { start: { line: 9, character: 0 }, end: { line: 29, character: 1 } },
            children: [
                {
                    name: 'bar',
                    kind: 6, // method
                    selectionRange: { start: { line: 11, character: 10 }, end: { line: 11, character: 13 } },
                    range: { start: { line: 11, character: 2 }, end: { line: 14, character: 5 } },
                    children: [],
                },
            ],
        },
    ];
}

function fakeClient(result: unknown) {
    return { sync: vi.fn(), request: vi.fn(async () => result) } as any;
}

describe('requestLSPSymbols — sync before request', () => {
    it('syncs pending edits to the server before asking for symbols', async () => {
        const client = fakeClient(makeDocumentSymbolFixture());
        await requestLSPSymbols(client, '/tmp/x.ts');
        expect(client.sync).toHaveBeenCalledTimes(1);
        // sync must happen before the documentSymbol request
        expect(client.sync.mock.invocationCallOrder[0]).toBeLessThan(
            client.request.mock.invocationCallOrder[0],
        );
    });
});

describe('requestLSPSymbols — DocumentSymbol (nested) mapping', () => {
    it('takes line from selectionRange and endLine from the full-body range', async () => {
        const symbols = await requestLSPSymbols(fakeClient(makeDocumentSymbolFixture()), '/tmp/x.ts');
        expect(symbols).not.toBeNull();
        expect(symbols).toHaveLength(1);
        const cls = symbols![0];
        // line: header text position (selectionRange.start.line + 1)
        expect(cls.line).toBe(10);
        // endLine: FULL body end (range.end.line + 1) — NOT the selectionRange
        // end (which is also line 10). This is what pins the sticky header
        // while scrolling through the class body.
        expect(cls.endLine).toBe(30);
        const method = cls.children![0];
        expect(method.line).toBe(12);
        expect(method.endLine).toBe(15);
    });

    it('keeps the nesting so parents contain their methods', async () => {
        const symbols = await requestLSPSymbols(fakeClient(makeDocumentSymbolFixture()), '/tmp/x.ts');
        const flat = flattenSymbols(symbols!);
        expect(flat.map((s) => s.name)).toEqual(['Foo', 'bar']);
        // bar is contained in Foo's range
        const foo = flat[0];
        const bar = flat[1];
        expect(bar.line).toBeGreaterThanOrEqual(foo.line);
        expect(bar.endLine).toBeLessThanOrEqual(foo.endLine);
    });

    it('maps LSP kinds to internal kind strings (class=5, method=6)', async () => {
        const symbols = await requestLSPSymbols(fakeClient(makeDocumentSymbolFixture()), '/tmp/x.ts');
        expect(symbols![0].kind).toBe('class');
        expect(symbols![0].children![0].kind).toBe('method');
    });
});

describe('requestLSPSymbols — availability & failure', () => {
    it('returns null when there is no LSP client', async () => {
        expect(await requestLSPSymbols(null, '/tmp/x.ts')).toBeNull();
    });

    it('returns null when the request times out', async () => {
        const hanging = { request: () => new Promise(() => {}) } as any;
        expect(await requestLSPSymbols(hanging, '/tmp/x.ts', 50)).toBeNull();
    });

    it('returns null when the request rejects', async () => {
        const failing = { request: () => Promise.reject(new Error('server dead')) } as any;
        expect(await requestLSPSymbols(failing, '/tmp/x.ts', 5000)).toBeNull();
    });
});

describe('requestLSPSymbols — SymbolInformation (flat) mapping', () => {
    // Flat responses (location.range, no children) are re-nested by range
    // containment.
    const flatFixture = [
        {
            name: 'Foo',
            kind: 5,
            location: {
                uri: 'file:///tmp/x.ts',
                range: { start: { line: 9, character: 0 }, end: { line: 29, character: 1 } },
            },
        },
        {
            name: 'bar',
            kind: 6,
            location: {
                uri: 'file:///tmp/x.ts',
                range: { start: { line: 11, character: 2 }, end: { line: 14, character: 5 } },
            },
        },
    ];

    it('re-nests a method inside its class from range containment', async () => {
        const symbols = await requestLSPSymbols(fakeClient(flatFixture), '/tmp/x.ts');
        expect(symbols).not.toBeNull();
        expect(symbols).toHaveLength(1);
        expect(symbols![0].name).toBe('Foo');
        expect(symbols![0].children).toHaveLength(1);
        expect(symbols![0].children![0].name).toBe('bar');
        expect(symbols![0].endLine).toBe(30); // full-body range
    });

    it('drops keyword-named symbols', async () => {
        const withKeyword = [
            ...flatFixture,
            {
                name: 'if',
                kind: 13,
                location: {
                    uri: 'file:///tmp/x.ts',
                    range: { start: { line: 3, character: 0 }, end: { line: 3, character: 1 } },
                },
            },
        ];
        const symbols = await requestLSPSymbols(fakeClient(withKeyword), '/tmp/x.ts');
        const names = symbols!.map((s) => s.name);
        expect(names).not.toContain('if');
    });
});
