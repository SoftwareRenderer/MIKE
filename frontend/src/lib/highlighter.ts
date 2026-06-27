import { highlightTree, tagHighlighter, tags } from '@lezer/highlight';
import { syntaxTree, Language } from '@codemirror/language';
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

/**
 * Extract global symbols (functions, classes, variables) from a CodeMirror EditorView.
 * Walks the language syntax tree and collects top-level definitions.
 * Returns an array of { name, kind, line, indent } sorted by line number.
 */
export function getLanguageTreeFromView(view: any): Array<{ name: string; kind: string; line: number; indent: number }> {
    const symbols: Array<{ name: string; kind: string; line: number; indent: number }> = [];
    try {
        const tree = syntaxTree(view.state);
        const doc = view.state.doc;

        // Map from declaration node types to symbol kinds
        const kindMap: Record<string, string> = {
            // C/C++
            'FunctionDefinition': 'function',
            'FunctionDeclarator': 'function',
            'StructSpecifier': 'class',
            'TypeDefinition': 'class',
            'Declaration': 'variable',
            'FieldDeclaration': 'property',
            'PreprocDirective': 'constant',
            'ParameterDeclaration': 'variable',
            // JavaScript/TypeScript
            'FunctionDeclaration': 'function',
            'MethodDeclaration': 'function',
            'ClassDeclaration': 'class',
            'VariableDeclaration': 'variable',
            'VariableDefinition': 'variable',
            'PropertyDefinition': 'property',
            'PropertyName': 'property',
        };

        // Track parent stack as we iterate
        const parentStack: string[] = [];

        tree.iterate({
            enter(node) {
                const name = node.type.name;

                // Check if this is an identifier with a symbol-kind parent
                if (name === 'Identifier' || name === 'TypeIdentifier' || name === 'FieldIdentifier' || name === 'VariableName' || name === 'PropertyName' || name === 'VariableDefinition' || name === 'PropertyDefinition') {
                    const text = doc.sliceString(node.from, node.to).trim();
                    if (!text || text.length > 60) return;
                    // Skip keywords
                    if (/^(if|for|while|return|static|const|struct|typedef|sizeof|void|int|char|bool|true|false|else|break|continue|case|default|switch|do|goto|enum|union|volatile|register|extern|auto|signed|unsigned|short|long|double|float|include|define|ifdef|ifndef|endif|undef|let|var|class|function|new|this|super|import|export|from|extends|implements|interface|type|async|await|yield|try|catch|finally|throw|delete|typeof|instanceof|in|of|with|debugger|null|undefined|constructor|get|set|then|catch|finally|finally|console|log|error|warn|info)$/.test(text)) return;

                    // Find the nearest declaration parent
                    // Only keep functions and variables
                    let kind: string | null = null;
                    const priority: Record<string, number> = { 'function': 5, 'variable': 2 };
                    let bestPriority = 0;
                    for (let i = parentStack.length - 1; i >= 0; i--) {
                        const pKind = kindMap[parentStack[i]];
                        if (pKind && priority[pKind] && priority[pKind] > bestPriority) {
                            kind = pKind;
                            bestPriority = priority[pKind];
                        }
                    }
                    if (!kind) return;

                    // Only keep top-level (global) symbols
                    // The declaration must be at index 1 (direct child of Program/Script)
                    // and the identifier must be within 3 levels of it
                    if (!kindMap[parentStack[1]]) return;
                    const identDepth = parentStack.length;
                    if ((identDepth - 1) > 3) return;

                    const line = doc.lineAt(node.from).number;
                    if (!symbols.some(s => s.name === text && s.line === line)) {
                        symbols.push({ name: text, kind, line, indent: 0 });
                    }
                }

                parentStack.push(name);
            },
            leave(node) {
                parentStack.pop();
            },
        });

        // Sort: variables first, then functions, each group alphabetically
        symbols.sort((a, b) => {
            const kindOrder = a.kind === 'variable' ? 0 : 1;
            const bKindOrder = b.kind === 'variable' ? 0 : 1;
            if (kindOrder !== bKindOrder) return kindOrder - bKindOrder;
            return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
        });

        // Deduplicate by name (keep first occurrence)
        const seen = new Set<string>();
        const unique: typeof symbols = [];
        for (const s of symbols) {
            if (!seen.has(s.name)) {
                seen.add(s.name);
                unique.push(s);
            }
        }

        // Limit to reasonable number
        return unique.slice(0, 200);
    } catch {
        return symbols;
    }
}


