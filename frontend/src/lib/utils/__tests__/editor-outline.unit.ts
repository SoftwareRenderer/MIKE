/**
 * The outline panel's fallback: the three file types that have no LSP-quality symbol source.
 *
 * These ran inside `highlighter.unit.ts` while the extractors lived there. A document is now a plain
 * `{ toString, lineAt }` (that is all an extractor reads), so the mock is the shape, not a fake view.
 */
import { describe, it, expect } from 'vitest';
import { getOutline } from '../editor-outline';

/** A document stand-in: the two members `OutlineDoc` asks for, computed the way CodeMirror computes them. */
function doc(source: string) {
    const lineStarts: number[] = [];
    let pos = 0;
    for (const line of source.split('\n')) {
        lineStarts.push(pos);
        pos += line.length + 1;
    }
    return {
        toString: () => source,
        lineAt: (p: number) => {
            let number = 1;
            for (let i = 0; i < lineStarts.length; i++) {
                if (p >= lineStarts[i]) number = i + 1;
                else break;
            }
            return { number };
        },
    };
}

describe('the markdown outline', () => {
    it('extracts ATX headings as outline entries', () => {
        const source = '# Title\n\n## Section\n\n### Subsection\n\nSome body text\n';
        const symbols = getOutline('README.md', doc(source));
        expect(symbols.map((s) => ({ name: s.name, kind: s.kind, line: s.line, indent: s.indent }))).toEqual([
            { name: 'Title', kind: 'heading', line: 1, indent: 0 },
            { name: 'Section', kind: 'heading', line: 3, indent: 1 },
            { name: 'Subsection', kind: 'heading', line: 5, indent: 2 },
        ]);
    });

    it('ignores bash comments inside a fenced code block', () => {
        const source = [
            '# Real Heading',
            '',
            '```bash',
            '# Build for Linux (CGO_ENABLED=0 for static build)',
            'CGO_ENABLED=0 go build',
            '```',
            '',
            '## Another Real Heading',
        ].join('\n');
        const symbols = getOutline('AGENTS.md', doc(source));
        expect(symbols.map((s) => s.name)).toEqual(['Real Heading', 'Another Real Heading']);
    });

    it('ignores comments inside tilde-fenced code blocks', () => {
        const source = ['# Top', '', '~~~bash', '# not a heading', 'echo hi', '~~~', '', '## Real'].join('\n');
        const symbols = getOutline('doc.md', doc(source));
        expect(symbols.map((s) => s.name)).toEqual(['Top', 'Real']);
    });

    it('detects headings again after a code block closes', () => {
        const source = ['# A', '', '```', '# comment', '```', '', '# B'].join('\n');
        const symbols = getOutline('doc.md', doc(source));
        expect(symbols.map((s) => s.name)).toEqual(['A', 'B']);
    });
});

describe('the go-template outline', () => {
    it('finds define/block names on the line they are on', () => {
        const source = 'package main\n\n{{define "header"}}h1{{end}}\n\n{{block "footer" .}}f{{end}}\n';
        const symbols = getOutline('page.tmpl', doc(source));
        // Grouped by kind, then alphabetical — the order the panel lists them in, not document order.
        expect(symbols.filter((s) => s.kind === 'class').map((s) => `${s.name}:${s.line}`)).toEqual([
            'footer:5',
            'header:3',
        ]);
    });
});

describe('a file nothing extracts gets no outline', () => {
    // The honest empty answer, and the reason the panel keeps asking LSP first: a declared grammar has
    // no definition nodes, so a tree walk over it would report nothing anyway.
    for (const path of ['main.go', 'app.ts', 'lib.py', 'no-suffix']) {
        it(`says nothing about ${path}`, () => {
            expect(getOutline(path, doc('func main() {}\n'))).toEqual([]);
        });
    }
});
