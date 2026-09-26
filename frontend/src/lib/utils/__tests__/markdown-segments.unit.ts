import { describe, it, expect } from 'vitest';
import { splitMarkdownSegments, type MarkdownSegment } from '$lib/utils/markdown-segments';
import { markdownToSafeHTML } from '$lib/utils/sanitize';

/** A fence delimiter line: the only text the split may leave between one segment and the next. */
const FENCE_DELIMITER = /^[ \t]*(?:`{3,}|~{3,})[^\n]*$/gm;

/** The text lying between consecutive segments, each segment located after the one before it. */
function gaps(source: string, segments: MarkdownSegment[]): string[] {
    const between: string[] = [];
    let pos = 0;
    for (const segment of segments) {
        const body = segment.kind === 'markdown' ? segment.source : segment.text;
        const at = source.indexOf(body, pos);
        expect(at, `${JSON.stringify(body)} comes after the segment before it`).toBeGreaterThanOrEqual(pos);
        between.push(source.slice(pos, at));
        pos = at + body.length;
    }
    between.push(source.slice(pos));
    return between;
}

/** Whitespace is not content: block separators, and a fence body's trailing newline. */
function collapse(text: string): string {
    return text.replace(/\s+/g, ' ').trim();
}

/** The markup two parses of the same prose agree on: no code blocks, no whitespace between tags. */
function proseMarkup(html: string): string {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    doc.querySelectorAll('pre').forEach(pre => pre.remove());
    return doc.body.innerHTML.replace(/>\s+</g, '><').trim();
}

const CORPUS = [
    'Intro **paragraph** with `inline` code.',
    '',
    '```go',
    'package main',
    '',
    '// a comment',
    '```',
    '',
    '- a list item',
    '- an item holding a fence:',
    '',
    '  ```sh',
    '  ls -la',
    '  ```',
    '',
    '> a quote',
    '>',
    '> ```sh',
    '> ls',
    '> ```',
    '',
    '| a | b |',
    '| --- | --- |',
    '| 1 | 2 |',
    '',
    '# Heading',
    '',
    '```markdown',
    '## Rendered',
    '',
    '- **not** source',
    '```',
    '',
    '```',
    'no language tag',
    '```',
    '',
    '    indented code block',
    '',
    'Outro paragraph.',
].join('\n');

describe('splitMarkdownSegments', () => {
    it('splits a fence out of the prose around it', () => {
        expect(splitMarkdownSegments('before\n\n```go\npackage main\n```\n\nafter')).toEqual([
            { kind: 'markdown', source: 'before\n\n' },
            { kind: 'code', lang: 'go', text: 'package main', open: false },
            { kind: 'markdown', source: '\n\nafter' },
        ]);
    });

    it.each([
        ['a language tag', '```go\ncode\n```', 'go'],
        ['an info string with parameters', '```go title=main.go\ncode\n```', 'go'],
        ['no tag at all', '```\ncode\n```', ''],
    ])('names the fence carrying %s as %j', (_name, source, lang) => {
        const segments = splitMarkdownSegments(source);
        expect(segments).toEqual([{ kind: 'code', lang, text: 'code', open: false }]);
    });

    it.each([
        ['a rendered markdown document', ['```markdown', '# Plan', '```'].join('\n')],
        ['a fence inside a list item', ['- item', '', '  ```sh', '  ls', '  ```'].join('\n')],
        ['a fence inside a blockquote', ['> quote', '>', '> ```sh', '> ls', '> ```'].join('\n')],
        ['a fence with nothing to copy', 'p\n\n```go\n```\n\nmore'],
    ])('leaves %s to the prose parse', (_name, source) => {
        expect(splitMarkdownSegments(source)).toEqual([{ kind: 'markdown', source }]);
    });

    it('yields a code segment for a fence that is still open', () => {
        expect(splitMarkdownSegments('here:\n\n```python\nx =')).toEqual([
            { kind: 'markdown', source: 'here:\n\n' },
            { kind: 'code', lang: 'python', text: 'x =', open: true },
        ]);
    });

    it.each([
        ['a closed fence', '```go\ncode\n```', false],
        ['a closed fence with a trailing space', '```go\ncode\n```  \n', false],
        ['a tilde fence', '~~~go\ncode\n~~~', false],
        ['a fence closed by fewer markers', '````go\ncode\n```', true],
        ['a fence closed by the other marker', '```go\ncode\n~~~', true],
        ['an indented code block, which has no fence', '    code\n', false],
    ])('reads %s as %j', (_name, source, open) => {
        const [segment] = splitMarkdownSegments(source);
        expect(segment.kind === 'code' && segment.open).toBe(open);
    });

    it('splits an indented code block too', () => {
        expect(splitMarkdownSegments('text\n\n    flagged = True\n')).toEqual([
            { kind: 'markdown', source: 'text\n\n' },
            { kind: 'code', lang: '', text: 'flagged = True\n', open: false },
        ]);
    });

    it('emits no empty segment', () => {
        for (const source of ['', '\n\n', '```go\nx\n```\n', '```go\nx\n````']) {
            for (const segment of splitMarkdownSegments(source)) {
                expect(collapse(segment.kind === 'markdown' ? segment.source : segment.text)).not.toBe('');
            }
        }
    });

    it.each([
        ['two fences and prose', CORPUS],
        ['a fence alone', '```go\nx\n```'],
        ['adjacent fences', '```go\nx\n```\n```sh\nls\n```'],
        ['an open fence', 'p\n\n```go\nunfinished'],
        ['an empty fence', 'p\n\n```go\n```\n\nmore'],
    ])('covers %s with slices in order, fence markers being all that lies between them', (_name, source) => {
        for (const gap of gaps(source, splitMarkdownSegments(source))) {
            expect(collapse(gap.replace(FENCE_DELIMITER, ''))).toBe('');
        }
    });

    it('renders the prose runs as the whole message did', () => {
        // The claim the split rests on: a fence between two paragraphs cannot change either of them.
        const split = splitMarkdownSegments(CORPUS)
            .map(s => (s.kind === 'markdown' ? markdownToSafeHTML(s.source) : '<pre></pre>'))
            .join('');
        expect(proseMarkup(split)).toBe(proseMarkup(markdownToSafeHTML(CORPUS)));
    });

    it('keeps the code of a rendered markdown document out of the split', () => {
        // A `markdown` fence is a document, so the fences inside it are its own prose.
        const source = ['```markdown', '## Plan', '', '```go', 'inner()', '```', '```'].join('\n');
        expect(splitMarkdownSegments(source)).toEqual([{ kind: 'markdown', source }]);
    });
});
