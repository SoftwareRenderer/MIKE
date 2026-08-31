import { describe, it, expect } from 'vitest';
import { Text } from '@codemirror/state';
import {
    uriToPath,
    isInsideWorkspace,
    relativeToRoot,
    toLspPosition,
    identifierRange,
    pickDefinitionTarget,
} from '../lsp-definition';

/**
 * The go-to-definition affordance is only useful if the path rules hold:
 * a definition must never be treated as "in this workspace" when it is not,
 * and the jump path must arrive in the form the explorers open files by.
 */

const DOC = [
    'package main',
    '',
    'import "fmt"',
    '',
    'func main() {',
    '\tfmt.Println(hélloWorld)',
    '}',
].join('\n');

const text = (doc = DOC) => Text.of(doc.split('\n'));
/** Offset of the nth occurrence of `needle` (1-based occurrence index). */
function offsetOf(doc: string, needle: string, nth = 1): number {
    let idx = -1;
    for (let i = 0; i < nth; i++) idx = doc.indexOf(needle, idx + 1);
    return idx;
}

describe('uriToPath', () => {
    it('strips the file scheme', () => {
        expect(uriToPath('file:///home/u/proj/main.go')).toBe('/home/u/proj/main.go');
        expect(uriToPath('file:///c:/x/main.go')).toBe('/c:/x/main.go');
    });

    it('percent-decodes the path', () => {
        expect(uriToPath('file:///home/u/my%20proj/main.go')).toBe('/home/u/my proj/main.go');
    });

    it('accepts file://localhost/ as the local host', () => {
        expect(uriToPath('file://localhost/home/u/main.go')).toBe('/home/u/main.go');
    });

    it('returns nothing for a remote host or a non-file scheme', () => {
        expect(uriToPath('file://otherhost/home/u/main.go')).toBe('');
        expect(uriToPath('golang://pkg/fmt')).toBe('');
        expect(uriToPath('untitled:Untitled-1')).toBe('');
        expect(uriToPath('')).toBe('');
    });

    it('keeps a malformed escape rather than dropping the target', () => {
        expect(uriToPath('file:///home/u/%zz/main.go')).toBe('/home/u/%zz/main.go');
    });
});

describe('isInsideWorkspace', () => {
    it('accepts the root itself and anything below it', () => {
        expect(isInsideWorkspace('/home/u/proj', '/home/u/proj')).toBe(true);
        expect(isInsideWorkspace('/home/u/proj/a/b.go', '/home/u/proj')).toBe(true);
    });

    it('tolerates a trailing slash on the root', () => {
        expect(isInsideWorkspace('/home/u/proj/a/b.go', '/home/u/proj/')).toBe(true);
    });

    it('rejects a sibling whose name starts with the root name', () => {
        // The bug this replaces: a raw startsWith() said true for proj2.
        expect(isInsideWorkspace('/home/u/proj2/a/b.go', '/home/u/proj')).toBe(false);
    });

    it('rejects empty inputs', () => {
        expect(isInsideWorkspace('', '/home/u/proj')).toBe(false);
        expect(isInsideWorkspace('/home/u/proj/a.go', '')).toBe(false);
    });
});

describe('relativeToRoot', () => {
    it('gives the project-relative path both explorers open by', () => {
        expect(relativeToRoot('/home/u/proj/pkg/svc.go', '/home/u/proj')).toBe('pkg/svc.go');
    });

    it('returns null outside the root', () => {
        expect(relativeToRoot('/home/u/other/svc.go', '/home/u/proj')).toBe(null);
        expect(relativeToRoot('/home/u/proj2/svc.go', '/home/u/proj')).toBe(null);
    });

    it('maps the root itself to the empty path', () => {
        expect(relativeToRoot('/home/u/proj', '/home/u/proj')).toBe('');
    });
});

describe('toLspPosition', () => {
    it('converts an offset to a 0-based line/character pair', () => {
        const doc = text();
        const off = offsetOf(DOC, 'Println');
        expect(toLspPosition(doc, off)).toEqual({ line: 5, character: '\tfmt.'.length });
    });

    it('maps the document start to line 0', () => {
        expect(toLspPosition(text(), 0)).toEqual({ line: 0, character: 0 });
    });

    it('clamps a stale offset instead of throwing', () => {
        const doc = text();
        expect(toLspPosition(doc, doc.length + 500)).toEqual({ line: 6, character: 1 });
    });
});

describe('identifierRange', () => {
    it('spans the whole identifier around the offset', () => {
        const doc = text();
        const off = offsetOf(DOC, 'Println') + 2;
        expect(identifierRange(doc, off)).toEqual({
            from: offsetOf(DOC, 'Println'),
            to: offsetOf(DOC, 'Println') + 'Println'.length,
        });
    });

    it('still finds the word when the pointer is on its trailing edge', () => {
        const doc = text();
        const end = offsetOf(DOC, 'Println') + 'Println'.length;
        expect(identifierRange(doc, end)).toEqual({ from: offsetOf(DOC, 'Println'), to: end });
    });

    it('returns null off an identifier', () => {
        const doc = text();
        // The ')' between two non-identifier characters has no word on either side.
        expect(identifierRange(doc, DOC.indexOf('() {') + 1)).toBe(null);
        // The blank line between the package clause and the import has none either.
        expect(identifierRange(doc, DOC.indexOf('import') - 1)).toBe(null);
    });

    it('attributes the boundary after a word to that word, but not past the gap', () => {
        const doc = text();
        const afterFunc = DOC.indexOf('func main');
        expect(identifierRange(doc, afterFunc + 4)).toEqual({ from: afterFunc, to: afterFunc + 4 });
        // One further right is the start of the next word, and the space inside
        // '() {' belongs to no word at all.
        expect(identifierRange(doc, afterFunc + 5)).toEqual({ from: afterFunc + 5, to: afterFunc + 9 });
        expect(identifierRange(doc, DOC.indexOf('() {') + 2)).toBe(null);
    });

    it('handles non-ASCII and $ identifiers', () => {
        const doc = text();
        const off = offsetOf(DOC, 'hélloWorld') + 3;
        expect(identifierRange(doc, off)).toEqual({
            from: offsetOf(DOC, 'hélloWorld'),
            to: offsetOf(DOC, 'hélloWorld') + 'hélloWorld'.length,
        });
        expect(identifierRange(Text.of(['let $a1 = 1']), 6)).toEqual({ from: 4, to: 7 });
    });

    it('handles an empty document', () => {
        expect(identifierRange(Text.of(['']), 0)).toBe(null);
    });
});

describe('pickDefinitionTarget', () => {
    it('reads a plain Location', () => {
        const got = pickDefinitionTarget({
            uri: 'file:///home/u/proj/a.go',
            range: { start: { line: 41, character: 5 }, end: { line: 41, character: 10 } },
        });
        expect(got).toEqual({
            path: '/home/u/proj/a.go',
            line: 42,
        });
    });

    it('reads a LocationLink (targetUri, not target.uri)', () => {
        const got = pickDefinitionTarget([
            {
                targetUri: 'file:///home/u/proj/b.go',
                targetRange: { start: { line: 3, character: 0 }, end: { line: 9, character: 1 } },
                targetSelectionRange: { start: { line: 3, character: 5 }, end: { line: 3, character: 9 } },
            },
        ]);
        expect(got?.line).toBe(4);
        expect(got?.path).toBe('/home/u/proj/b.go');
    });

    it('reads the first entry of a Location array', () => {
        const got = pickDefinitionTarget([
            { uri: 'file:///home/u/proj/c.go', range: { start: { line: 7, character: 1 }, end: { line: 7, character: 2 } } },
            { uri: 'file:///home/u/proj/d.go', range: { start: { line: 8, character: 1 }, end: { line: 8, character: 2 } } },
        ]);
        expect(got?.path).toBe('/home/u/proj/c.go');
        expect(got?.line).toBe(8);
    });

    it('accepts the nested-target shim shape', () => {
        const got = pickDefinitionTarget({
            target: {
                uri: 'file:///home/u/proj/e.go',
                range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } },
            },
        });
        expect(got?.path).toBe('/home/u/proj/e.go');
        expect(got?.line).toBe(1);
    });

    it('returns null for no definition', () => {
        expect(pickDefinitionTarget(null)).toBe(null);
        expect(pickDefinitionTarget([])).toBe(null);
        expect(pickDefinitionTarget(undefined)).toBe(null);
        expect(pickDefinitionTarget({})).toBe(null);
        expect(pickDefinitionTarget({ uri: 'file:///a.go' })).toBe(null);
    });

    it('keeps a non-file URI with an empty path so the caller rejects it', () => {
        const got = pickDefinitionTarget({
            uri: 'golang://fmt.Println',
            range: { start: { line: 10, character: 0 }, end: { line: 10, character: 5 } },
        });
        expect(got?.path).toBe('');
        expect(isInsideWorkspace(got!.path, '/home/u/proj')).toBe(false);
    });
});
