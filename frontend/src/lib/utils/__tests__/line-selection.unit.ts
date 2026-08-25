import { describe, it, expect } from 'vitest';
import { resolveGutterClickRange, toProjectRelative, formatLineSelection, buildLineSelectionPrompt } from '$lib/utils/line-selection';

describe('resolveGutterClickRange', () => {
    it('clicks without a text selection select exactly the clicked line', () => {
        expect(resolveGutterClickRange(7, null)).toEqual({ start: 7, end: 7 });
    });

    it('a single-line text selection does not extend the click', () => {
        expect(resolveGutterClickRange(7, { start: 7, end: 7 })).toEqual({ start: 7, end: 7 });
        expect(resolveGutterClickRange(4, { start: 4, end: 4 })).toEqual({ start: 4, end: 4 });
    });

    it('a click inside a multi-line selection selects the whole range', () => {
        expect(resolveGutterClickRange(5, { start: 5, end: 6 })).toEqual({ start: 5, end: 6 });
        expect(resolveGutterClickRange(6, { start: 5, end: 6 })).toEqual({ start: 5, end: 6 });
        expect(resolveGutterClickRange(4, { start: 3, end: 8 })).toEqual({ start: 3, end: 8 });
    });

    it('a click on the first or last highlighted line selects the whole range', () => {
        expect(resolveGutterClickRange(3, { start: 3, end: 8 })).toEqual({ start: 3, end: 8 });
        expect(resolveGutterClickRange(8, { start: 3, end: 8 })).toEqual({ start: 3, end: 8 });
    });

    it('a click outside a multi-line selection selects only the clicked line', () => {
        expect(resolveGutterClickRange(2, { start: 5, end: 6 })).toEqual({ start: 2, end: 2 });
        expect(resolveGutterClickRange(7, { start: 5, end: 6 })).toEqual({ start: 7, end: 7 });
    });
});

describe('buildLineSelectionPrompt', () => {
    it('prepends two newlines above the reference block', () => {
        const prompt = buildLineSelectionPrompt({ relativePath: 'src/main.go', startLine: 10, lines: ['a', 'b'] });
        expect(prompt).toBe('\n\n' + formatLineSelection('src/main.go', 10, ['a', 'b']));
        expect(prompt).toBe('\n\nsrc/main.go:10-11\n10: a\n11: b');
    });
});

describe('toProjectRelative', () => {
    it('strips the project root prefix', () => {
        expect(toProjectRelative('/home/u/proj/src/main.go', '/home/u/proj')).toBe('src/main.go');
        expect(toProjectRelative('/home/u/proj/main.go', '/home/u/proj')).toBe('main.go');
    });

    it('tolerates a trailing slash on the project path', () => {
        expect(toProjectRelative('/home/u/proj/main.go', '/home/u/proj/')).toBe('main.go');
    });

    it('does not confuse sibling project prefixes', () => {
        expect(toProjectRelative('/home/u/proj2/main.go', '/home/u/proj')).toBe('/home/u/proj2/main.go');
    });

    it('strips the sandbox mount prefix', () => {
        expect(toProjectRelative('/home/sandbox/proj/src/main.go', '/home/u/proj')).toBe('src/main.go');
    });

    it('falls back to the raw path when nothing matches', () => {
        expect(toProjectRelative('/etc/hosts', '/home/u/proj')).toBe('/etc/hosts');
        expect(toProjectRelative('/home/u/proj/src/a.go', '')).toBe('/home/u/proj/src/a.go');
    });

    it('returns empty string for an empty file path', () => {
        expect(toProjectRelative('', '/home/u/proj')).toBe('');
    });
});

describe('formatLineSelection', () => {
    it('formats a single line without a range', () => {
        expect(formatLineSelection('src/main.go', 10, ['func main() {'])).toBe(
            'src/main.go:10\n10: func main() {',
        );
    });

    it('formats a multi-line range with a start-end header', () => {
        expect(formatLineSelection('src/main.go', 10, ['func main() {', '\tfmt.Println("hi")', '}'])).toBe(
            'src/main.go:10-12\n10: func main() {\n11: \tfmt.Println("hi")\n12: }',
        );
    });

    it('numbers lines sequentially from startLine (reversed drags pre-normalized)', () => {
        expect(formatLineSelection('a.txt', 1, ['x', 'y'])).toBe('a.txt:1-2\n1: x\n2: y');
    });

    it('keeps empty lines intact', () => {
        expect(formatLineSelection('a.txt', 2, ['', 'z'])).toBe('a.txt:2-3\n2: \n3: z');
    });

    it('uses a fallback name when the relative path is empty', () => {
        expect(formatLineSelection('', 1, ['only'])).toBe('unknown-file:1\n1: only');
    });
});
