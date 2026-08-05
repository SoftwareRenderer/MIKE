import { describe, it, expect } from 'vitest';
import { getLanguageTreeFromView } from '$lib/highlighter';

// Mock a CodeMirror EditorView enough for the markdown fallback path,
// which only needs state.doc.toString() and state.doc.lineAt().
function mockView(source: string): any {
    const lines = source.split('\n');
    const lineStarts: number[] = [];
    let pos = 0;
    for (const l of lines) {
        lineStarts.push(pos);
        pos += l.length + 1;
    }
    return {
        state: {
            doc: {
                toString: () => source,
                lineAt: (p: number) => {
                    let number = 1;
                    for (let i = 0; i < lineStarts.length; i++) {
                        if (p >= lineStarts[i]) number = i + 1;
                        else break;
                    }
                    return { number };
                },
            },
        },
    };
}

describe('getLanguageTreeFromView markdown outline', () => {
    it('extracts ATX headings as outline entries', () => {
        const source = '# Title\n\n## Section\n\n### Subsection\n\nSome body text\n';
        const symbols = getLanguageTreeFromView(mockView(source), 'README.md');
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
        const symbols = getLanguageTreeFromView(mockView(source), 'AGENTS.md');
        expect(symbols.map((s) => s.name)).toEqual(['Real Heading', 'Another Real Heading']);
    });

    it('ignores comments inside tilde-fenced code blocks', () => {
        const source = ['# Top', '', '~~~bash', '# not a heading', 'echo hi', '~~~', '', '## Real'].join('\n');
        const symbols = getLanguageTreeFromView(mockView(source), 'doc.md');
        expect(symbols.map((s) => s.name)).toEqual(['Top', 'Real']);
    });

    it('detects headings again after a code block closes', () => {
        const source = ['# A', '', '```', '# comment', '```', '', '# B'].join('\n');
        const symbols = getLanguageTreeFromView(mockView(source), 'doc.md');
        expect(symbols.map((s) => s.name)).toEqual(['A', 'B']);
    });
});
