import { describe, it, expect } from 'vitest';
import { escapeHtml } from '$lib/utils/format';

// Test the highlightMatch logic extracted from FileSearchPanel.svelte
// These are pure functions so we can test them without rendering the component.

function highlightMatch(line: string, query: string, useRegex: boolean, matchCase: boolean): string {
    if (!query || !line) return escapeHtml(line);

    if (useRegex) {
        try {
            const flags = matchCase ? 'g' : 'gi';
            const re = new RegExp(query, flags);
            const parts: string[] = [];
            let lastIdx = 0;
            let m: RegExpExecArray | null;
            // eslint-disable-next-line no-cond-assign
            while ((m = re.exec(line)) !== null && m[0].length > 0) {
                parts.push(escapeHtml(line.slice(lastIdx, m.index)));
                parts.push(`<mark>${escapeHtml(m[0])}</mark>`);
                lastIdx = m.index + m[0].length;
            }
            parts.push(escapeHtml(line.slice(lastIdx)));
            return parts.join('');
        } catch {
            return escapeHtml(line);
        }
    }

    const searchLine = matchCase ? line : line.toLowerCase();
    const searchQuery = matchCase ? query : query.toLowerCase();
    const parts: string[] = [];
    let lastIdx = 0;
    let idx = searchLine.indexOf(searchQuery);

    while (idx !== -1) {
        parts.push(escapeHtml(line.slice(lastIdx, idx)));
        parts.push(`<mark>${escapeHtml(line.slice(idx, idx + query.length))}</mark>`);
        lastIdx = idx + query.length;
        idx = searchLine.indexOf(searchQuery, lastIdx);
    }
    parts.push(escapeHtml(line.slice(lastIdx)));
    return parts.join('');
}

describe('highlightMatch', () => {
    it('escapes HTML in plain text', () => {
        const result = highlightMatch('<script>alert("xss")', 'script', false, true);
        expect(result).toContain('&lt;');
        expect(result).toContain('&gt;');
        expect(result).toContain('<mark>script</mark>');
    });

    it('highlights a single match (case insensitive)', () => {
        const result = highlightMatch('Hello world', 'hello', false, false);
        expect(result).toContain('<mark>Hello</mark>');
    });

    it('highlights multiple matches', () => {
        const result = highlightMatch('foo bar foo baz foo', 'foo', false, false);
        const matches = result.match(/<mark>foo<\/mark>/g);
        expect(matches).toHaveLength(3);
    });

    it('respects case sensitivity', () => {
        const result = highlightMatch('Hello hello HELLO', 'hello', false, true);
        expect(result).toContain('<mark>hello</mark>');
        expect(result).not.toContain('<mark>Hello</mark>');
        expect(result).not.toContain('<mark>HELLO</mark>');
    });

    it('handles regex matches', () => {
        const result = highlightMatch('export function foo', 'export\\s+function', true, false);
        expect(result).toContain('<mark>export function</mark>');
    });

    it('handles invalid regex gracefully', () => {
        const result = highlightMatch('some text', '[invalid', true, false);
        expect(result).toBe('some text');
    });

    it('returns empty string for empty input', () => {
        expect(highlightMatch('', 'query', false, false)).toBe('');
    });

    it('escapes HTML inside mark tags', () => {
        const result = highlightMatch('<b>test</b>', '<b>', false, true);
        expect(result).toBe('<mark>&lt;b&gt;</mark>test&lt;/b&gt;');
    });
});

describe('groupedResults logic', () => {
    interface SearchResult {
        path: string;
        line?: number;
        text?: string;
    }

    function groupResults(results: SearchResult[]) {
        const map = new Map<string, SearchResult[]>();
        for (const r of results) {
            const key = r.path;
            const group = map.get(key);
            if (group) {
                group.push(r);
            } else {
                map.set(key, [r]);
            }
        }
        const groups: { path: string; line: number; results: SearchResult[] }[] = [];
        const sortedPaths = [...map.keys()].sort((a, b) => a.localeCompare(b));
        for (const path of sortedPaths) {
            const fileResults = map.get(path)!;
            fileResults.sort((a, b) => (a.line || 0) - (b.line || 0));
            groups.push({ path, line: fileResults[0].line || 0, results: fileResults });
        }
        return groups;
    }

    it('groups results by file path', () => {
        const results: SearchResult[] = [
            { path: 'src/b.ts', line: 5, text: '' },
            { path: 'src/a.ts', line: 10, text: '' },
            { path: 'src/a.ts', line: 3, text: '' },
        ];
        const groups = groupResults(results);
        expect(groups).toHaveLength(2);
        expect(groups[0].path).toBe('src/a.ts');
        expect(groups[0].results).toHaveLength(2);
        expect(groups[1].path).toBe('src/b.ts');
        expect(groups[1].results).toHaveLength(1);
    });

    it('sorts files alphabetically', () => {
        const results: SearchResult[] = [
            { path: 'z.ts', line: 1, text: '' },
            { path: 'a.ts', line: 1, text: '' },
            { path: 'm.ts', line: 1, text: '' },
        ];
        const groups = groupResults(results);
        expect(groups.map(g => g.path)).toEqual(['a.ts', 'm.ts', 'z.ts']);
    });

    it('sorts results within each file by line number', () => {
        const results: SearchResult[] = [
            { path: 'app.ts', line: 20, text: '' },
            { path: 'app.ts', line: 5, text: '' },
            { path: 'app.ts', line: 12, text: '' },
        ];
        const groups = groupResults(results);
        expect(groups[0].results.map(r => r.line)).toEqual([5, 12, 20]);
    });

    it('handles single result', () => {
        const results: SearchResult[] = [
            { path: 'only.ts', line: 1, text: '' },
        ];
        const groups = groupResults(results);
        expect(groups).toHaveLength(1);
        expect(groups[0].path).toBe('only.ts');
        expect(groups[0].results).toHaveLength(1);
    });

    it('handles empty results', () => {
        const groups = groupResults([]);
        expect(groups).toHaveLength(0);
    });

    it('handles results without line numbers', () => {
        const results: SearchResult[] = [
            { path: 'file.ts', text: '' },
            { path: 'file.ts', text: '' },
        ];
        const groups = groupResults(results);
        expect(groups[0].line).toBe(0);
        expect(groups[0].results).toHaveLength(2);
    });
});
