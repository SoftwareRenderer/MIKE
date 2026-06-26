import { highlightTree, tagHighlighter, tags } from '@lezer/highlight';
import type { Language } from '@codemirror/language';
import { javascript } from '@codemirror/lang-javascript';
import { python } from '@codemirror/lang-python';
import { go } from '@codemirror/lang-go';
import { json } from '@codemirror/lang-json';
import { markdown } from '@codemirror/lang-markdown';
import { rust } from '@codemirror/lang-rust';
import { cpp } from '@codemirror/lang-cpp';
import { php } from '@codemirror/lang-php';
import { sql } from '@codemirror/lang-sql';
import { html } from '@codemirror/lang-html';
import { css } from '@codemirror/lang-css';
import { yaml } from '@codemirror/lang-yaml';

/**
 * GitHub Dark highlighter — matches the FileEditor's githubDarkHighlight colors.
 * Maps Lezer tags to CSS class names (hl-*).
 */
const githubDarkHighlighter = tagHighlighter([
    { tag: tags.keyword, class: 'hl-keyword' },
    { tag: tags.constant(tags.keyword), class: 'hl-constantkeyword' },
    { tag: tags.definition(tags.variableName), class: 'hl-definitionvariable' },
    { tag: tags.variableName, class: 'hl-variable' },
    { tag: tags.function(tags.variableName), class: 'hl-function' },
    { tag: tags.propertyName, class: 'hl-property' },
    { tag: tags.string, class: 'hl-string' },
    { tag: tags.number, class: 'hl-number' },
    { tag: tags.comment, class: 'hl-comment' },
    { tag: tags.operator, class: 'hl-operator' },
    { tag: tags.punctuation, class: 'hl-punctuation' },
    { tag: tags.className, class: 'hl-classname' },
    { tag: tags.typeName, class: 'hl-typename' },
    { tag: tags.tagName, class: 'hl-tagname' },
    { tag: tags.attributeName, class: 'hl-attribute' },
    { tag: tags.attributeValue, class: 'hl-attributevalue' },
    { tag: tags.regexp, class: 'hl-regexp' },
    { tag: tags.meta, class: 'hl-meta' },
    { tag: tags.invalid, class: 'hl-invalid' },
]);

// Cached Language objects — CodeMirror lang packages export functions
const _js = javascript().language;
const _py = python().language;
const _go = go().language;
const _cpp = cpp().language;
const _rust = rust().language;
const _json = json().language;
const _md = markdown().language;
const _php = php().language;
const _sql = sql().language;
const _html = html().language;
const _css = css().language;
const _yaml = yaml().language;

/**
 * Map file extensions to CodeMirror Language objects.
 * Returns null for unsupported languages (plain text fallback).
 */
function getLanguage(path: string): Language | null {
    const ext = path.split('.').pop()?.toLowerCase() || '';
    const base = path.split('/').pop()?.toLowerCase() || '';

    // Special basenames — no CodeMirror support, return null
    if (base === 'dockerfile' || base === 'makefile' || base === 'cmakelists.txt') return null;

    const extMap: Record<string, Language> = {
        js: _js, mjs: _js, cjs: _js, jsx: _js, mx: _js,
        ts: _js, tsx: _js,
        py: _py, pyw: _py,
        go: _go,
        c: _cpp, h: _cpp,
        cpp: _cpp, cc: _cpp, cxx: _cpp, hxx: _cpp, hpp: _cpp,
        cppm: _cpp, hppm: _cpp, cx: _cpp, cxxm: _cpp,
        rs: _rust,
        json: _json,
        md: _md, mdx: _md,
        php: _php, php3: _php, php4: _php, php5: _php, php7: _php, phps: _php, phpws: _php,
        sql: _sql,
        html: _html, htm: _html,
        css: _css, scss: _css, sass: _css, less: _css,
        yaml: _yaml, yml: _yaml,
    };

    return extMap[ext] ?? null;
}

/**
 * Escape HTML special characters in a string.
 */
function escapeHtml(text: string): string {
    return text
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;');
}

/**
 * Highlight multiple lines of code and return an array of HTML fragments (one per line).
 * Uses Lezer parsers from CodeMirror language packages for token-level highlighting.
 * Returns empty string for lines that should fall back to plain text rendering.
 */
export function highlightLines(lines: string[], path: string): string[] {
    if (lines.length === 0) return [];

    const lang = getLanguage(path);
    if (!lang) return lines.map(() => '');

    try {
        const source = lines.join('\n');
        const tree = lang.parser.parse(source);

        // Compute line boundaries: two arrays for start and end of each line's content.
        // For empty lines, lineStarts[i] == lineEnds[i] so they're skipped (no content).
        // source.substring(lineStarts[i], lineEnds[i]) == lines[i]
        const lineStarts: number[] = new Array(lines.length);
        const lineEnds: number[] = new Array(lines.length);
        let pos = 0;
        for (let i = 0; i < lines.length; i++) {
            lineStarts[i] = pos;
            lineEnds[i] = pos + lines[i].length;
            if (i < lines.length - 1) pos += lines[i].length + 1; // content + '\n'
        }

        const result: string[] = new Array(lines.length).fill('');

        /** Add an unstyled text segment to the current line */
        function addGap(lineIdx: number, from: number, to: number) {
            if (from >= to) return;
            const text = source.substring(from, to);
            const existing = result[lineIdx];
            result[lineIdx] = (existing ? existing : '') + escapeHtml(text);
        }

        /** Add a styled text segment to the current line */
        function addStyled(lineIdx: number, from: number, to: number, classes: string) {
            if (from >= to) return;
            const text = source.substring(from, to);
            const existing = result[lineIdx];
            result[lineIdx] = (existing ? existing : '') + `<span class="${classes}">${escapeHtml(text)}</span>`;
        }

        // Collect styled spans from the syntax tree
        const styledSpans: { from: number; to: number; classes: string }[] = [];
        highlightTree(tree, githubDarkHighlighter, (from, to, classes) => {
            styledSpans.push({ from, to, classes });
        });

        // For each line, interleave gaps (unstyled) and styled spans
        for (let i = 0; i < lines.length; i++) {
            const lStart = lineStarts[i];
            const lEnd = lineEnds[i];
            if (lStart === lEnd) continue; // empty line

            let cursor = lStart;
            let spanIdx = 0;

            // Advance spanIdx to the first span that overlaps or follows this line
            while (spanIdx < styledSpans.length && styledSpans[spanIdx].to <= cursor) {
                spanIdx++;
            }

            while (cursor < lEnd && spanIdx < styledSpans.length) {
                const span = styledSpans[spanIdx];

                if (span.from >= lEnd) {
                    // All remaining spans are beyond this line — rest is unstyled
                    addGap(i, cursor, lEnd);
                    cursor = lEnd;
                    break;
                }

                if (span.to <= cursor) {
                    spanIdx++;
                    continue;
                }

                // Fill unstyled gap before this span
                if (span.from > cursor) {
                    addGap(i, cursor, Math.min(span.from, lEnd));
                }

                // Add the styled portion (clamped to line boundary)
                const sFrom = Math.max(span.from, cursor);
                const sTo = Math.min(span.to, lEnd);
                if (sTo > sFrom) {
                    addStyled(i, sFrom, sTo, span.classes);
                }

                cursor = Math.max(cursor, Math.min(span.to, lEnd));
                if (span.to >= lEnd) spanIdx++;
            }

            // Remaining unstyled text after last span on this line
            if (cursor < lEnd) {
                addGap(i, cursor, lEnd);
            }
        }

        return result;
    } catch {
        return lines.map(() => '');
    }
}


