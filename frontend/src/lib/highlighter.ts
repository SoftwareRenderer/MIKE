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
import { svelte } from '@replit/codemirror-lang-svelte';

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
const _svelte = svelte().language;

/**
 * Map file extensions to CodeMirror Language objects.
 * Returns null for unsupported languages (plain text fallback).
 */
function getLanguage(path: string): Language | null {
    const ext = path.split('.').pop()?.toLowerCase() || '';
    const base = path.split('/').pop()?.toLowerCase() || '';

    // Special basenames — no CodeMirror support, return null
    if (base === 'dockerfile' || base === 'makefile' || base === 'cmakelists.txt') return null;

    // Go templates — handled by custom regex-based highlighter (no Lezer grammar)
    if (ext === 'tmpl' || ext === 'gohtml') return null;

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
        svelte: _svelte,
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

/** Simple LRU cache for highlight results (keyed by path + content hash). */
const highlightCache = new Map<string, string[]>();
const HIGHLIGHT_CACHE_MAX = 50;

/** Compute a simple hash for cache key. */
function contentHash(lines: string[]): string {
    let hash = 0;
    for (const line of lines) {
        for (let i = 0; i < line.length; i++) {
            hash = ((hash << 5) - hash + line.charCodeAt(i)) | 0;
        }
    }
    return hash.toString(36);
}

/**
 * Go template action keywords.
 */
const goTemplateKeywords = new Set([
    'if', 'else', 'end', 'range', 'with',
    'template', 'define', 'block',
    'nil', 'true', 'false',
    'and', 'or', 'not',
    'eq', 'ne', 'lt', 'le', 'gt', 'ge',
    'printf', 'print', 'println',
    'len', 'index', 'html', 'js', 'urlquery', 'printf',
    'slice', 'substr', 'default', 'timeout', 'recover',
]);

/**
 * Highlight Go template lines using regex-based tokenization.
 * Recognizes template actions {{ ... }}, keywords, variables, strings, comments.
 * Base content (HTML) is left unstyled — the focus is on template action highlighting.
 */
function highlightGoTemplateLines(lines: string[]): string[] {
    const result: string[] = new Array(lines.length).fill('');
    const source = lines.join('\n');

    // Compute line boundaries
    const lineStarts: number[] = new Array(lines.length);
    const lineEnds: number[] = new Array(lines.length);
    let pos = 0;
    for (let i = 0; i < lines.length; i++) {
        lineStarts[i] = pos;
        lineEnds[i] = pos + lines[i].length;
        if (i < lines.length - 1) pos += lines[i].length + 1;
    }

    const styledSpans: { from: number; to: number; cls: string }[] = [];

    // Find all {{ ... }} actions in the source
    // Pattern: {{-? ... -?}} (non-greedy, but handle nested braces)
    let scanPos = 0;
    while (scanPos < source.length) {
        const actionStart = source.indexOf('{{', scanPos);
        if (actionStart === -1) break;

        // Find matching }}
        let depth = 0;
        let actionEnd = -1;
        for (let i = actionStart; i < source.length - 1; i++) {
            if (source[i] === '{' && source[i + 1] === '{') depth++;
            else if (source[i] === '}' && source[i + 1] === '}') {
                depth--;
                if (depth === 0) {
                    actionEnd = i + 2;
                    break;
                }
            }
        }
        if (actionEnd === -1) break; // unmatched {{

        // Extract action content (between {{ and }})
        let contentStart = actionStart + 2;
        while (contentStart < actionEnd && /\s|-/.test(source[contentStart])) contentStart++;
        let contentEnd = actionEnd - 2;
        while (contentEnd > contentStart && /\s|-/.test(source[contentEnd])) contentEnd--;
        const actionContent = source.substring(contentStart, contentEnd);

        // Check for comment action: {{/* ... */}}
        if (actionContent.startsWith('/*')) {
            styledSpans.push({ from: actionStart, to: actionEnd, cls: 'hl-comment' });
            scanPos = actionEnd;
            continue;
        }

        // Highlight the delimiters
        styledSpans.push({ from: actionStart, to: actionStart + 2, cls: 'hl-meta' });
        // Close delimiter
        let closeStart = actionEnd - 2;
        if (source[closeStart - 1] === '-') closeStart--;
        styledSpans.push({ from: closeStart, to: actionEnd, cls: 'hl-meta' });

        // Tokenize action content
        const tokens = tokenizeGoAction(actionContent, actionStart + (contentStart - actionStart));
        styledSpans.push(...tokens);

        scanPos = actionEnd;
    }

    // Build per-line output
    for (let i = 0; i < lines.length; i++) {
        const lStart = lineStarts[i];
        const lEnd = lineEnds[i];
        if (lStart === lEnd) continue;

        let cursor = lStart;
        let spanIdx = 0;

        while (spanIdx < styledSpans.length && styledSpans[spanIdx].to <= cursor) {
            spanIdx++;
        }

        while (cursor < lEnd && spanIdx < styledSpans.length) {
            const span = styledSpans[spanIdx];
            if (span.from >= lEnd) {
                if (span.from > cursor) {
                    result[i] += escapeHtml(source.substring(cursor, lEnd));
                }
                cursor = lEnd;
                break;
            }
            if (span.to <= cursor) { spanIdx++; continue; }

            if (span.from > cursor) {
                result[i] += escapeHtml(source.substring(cursor, Math.min(span.from, lEnd)));
            }
            const sFrom = Math.max(span.from, cursor);
            const sTo = Math.min(span.to, lEnd);
            if (sTo > sFrom) {
                result[i] += `<span class="${span.cls}">${escapeHtml(source.substring(sFrom, sTo))}</span>`;
            }
            cursor = Math.max(cursor, Math.min(span.to, lEnd));
            if (span.to >= lEnd) spanIdx++;
        }
        if (cursor < lEnd) {
            result[i] += escapeHtml(source.substring(cursor, lEnd));
        }
    }

    return result;
}

/**
 * Tokenize Go template action content into styled spans.
 */
function tokenizeGoAction(content: string, baseOffset: number): { from: number; to: number; cls: string }[] {
    const spans: { from: number; to: number; cls: string }[] = [];
    let pos = 0;

    while (pos < content.length) {
        // Strings
        const stringMatch = /^("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`[^`]*`)/.exec(content.slice(pos));
        if (stringMatch) {
            spans.push({ from: baseOffset + pos, to: baseOffset + pos + stringMatch[0].length, cls: 'hl-string' });
            pos += stringMatch[0].length;
            continue;
        }

        // Dot variables: .Field or .
        const dotMatch = /^(\.[a-zA-Z_][a-zA-Z0-9_]*)|(\.(?=\s|$|\||\}))/.exec(content.slice(pos));
        if (dotMatch) {
            spans.push({ from: baseOffset + pos, to: baseOffset + pos + dotMatch[0].length, cls: 'hl-variable' });
            pos += dotMatch[0].length;
            continue;
        }

        // Named variables: $var
        const varMatch = /^(\$[a-zA-Z_][a-zA-Z0-9_]*)/.exec(content.slice(pos));
        if (varMatch) {
            spans.push({ from: baseOffset + pos, to: baseOffset + pos + varMatch[0].length, cls: 'hl-variable' });
            pos += varMatch[0].length;
            continue;
        }

        // Keywords and identifiers
        const identMatch = /^([a-zA-Z_][a-zA-Z0-9_]*)/.exec(content.slice(pos));
        if (identMatch) {
            const word = identMatch[1];
            const cls = goTemplateKeywords.has(word) ? 'hl-keyword' : 'hl-function';
            spans.push({ from: baseOffset + pos, to: baseOffset + pos + word.length, cls });
            pos += word.length;
            continue;
        }

        // Numbers
        const numMatch = /^(\d+(?:\.\d+)?)/.exec(content.slice(pos));
        if (numMatch) {
            spans.push({ from: baseOffset + pos, to: baseOffset + pos + numMatch[1].length, cls: 'hl-number' });
            pos += numMatch[1].length;
            continue;
        }

        // Operators: | == != <= >= = < >
        const opMatch = /^(\||==|!=|<=|>=|<|>|=)/.exec(content.slice(pos));
        if (opMatch) {
            spans.push({ from: baseOffset + pos, to: baseOffset + pos + opMatch[1].length, cls: 'hl-operator' });
            pos += opMatch[1].length;
            continue;
        }

        // Punctuation: () [] , :
        if ('()[]:,'.includes(content[pos])) {
            spans.push({ from: baseOffset + pos, to: baseOffset + pos + 1, cls: 'hl-punctuation' });
            pos++;
            continue;
        }

        // Skip whitespace and other chars
        pos++;
    }

    return spans;
}

/**
 * Highlight multiple lines of code and return an array of HTML fragments (one per line).
 * Uses Lezer parsers from CodeMirror language packages for token-level highlighting.
 * Returns empty string for lines that should fall back to plain text rendering.
 * Results are cached per file path + content hash to avoid redundant parsing.
 */
export function highlightLines(lines: string[], path: string): string[] {
    if (lines.length === 0) return [];

    // Go templates use a custom regex-based highlighter (no Lezer grammar available)
    const ext = path.split('.').pop()?.toLowerCase() || '';
    if (ext === 'tmpl' || ext === 'gohtml') {
        return highlightGoTemplateLines(lines);
    }

    const lang = getLanguage(path);
    if (!lang) return lines.map(() => '');

    // Check cache
    const cacheKey = `${path}\x00${contentHash(lines)}`;
    const cached = highlightCache.get(cacheKey);
    if (cached) return cached;

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

        // Store in cache (evict oldest if full)
        if (highlightCache.size >= HIGHLIGHT_CACHE_MAX) {
            const firstKey = highlightCache.keys().next().value as string;
            highlightCache.delete(firstKey);
        }
        highlightCache.set(cacheKey, result);
        return result;
    } catch {
        return lines.map(() => '');
    }
}

/**
 * Extract symbols from Go template source using regex.
 * Finds: {{define "name"}}, {{block "name" .}}, $var := assignments.
 */
function extractGoTemplateSymbols(source: string, lineInfo: { lineAt: (pos: number) => { number: number } }): Array<{ name: string; kind: string; line: number; indent: number }> {
    const symbols: Array<{ name: string; kind: string; line: number; indent: number }> = [];

    // Match {{define "name"}} and {{block "name" ...}}
    const defineRegex = /\{\{-?\s*(define|block)\s+["']([^"']+)["']/g;
    let m;
    while ((m = defineRegex.exec(source)) !== null) {
        const line = lineInfo.lineAt(m.index).number;
        symbols.push({ name: m[2], kind: 'class', line, indent: 0 });
    }

    // Match $var := ... (variable assignments inside actions)
    const varRegex = /\{\{-?\s*(?:with\s+)?(\$[a-zA-Z_][a-zA-Z0-9_]*)\s*:=/g;
    while ((m = varRegex.exec(source)) !== null) {
        const name = m[1].substring(1); // strip $
        const line = lineInfo.lineAt(m.index).number;
        symbols.push({ name, kind: 'variable', line, indent: 0 });
    }

    // Match .Field references inside actions (e.g., {{ .TaskTitle }}, {{ .User.Name }})
    // Skip common built-in methods/fields
    const skipFields = new Set([
        'Page', 'Site', 'Root', 'Get', 'Set', 'Merge', 'String', 'Time', 'Date',
        'HTML', 'SafeHTML', 'SafeCSS', 'SafeJS', 'SafeURL', 'URL', 'Transform',
        'Fingerprint', 'Resources', 'Params', 'Weight', 'Title', 'Summary',
        'Content', 'Plain', 'Lang', 'Language', 'RelPermalink', 'Permalink',
        'File', 'OutputFormat', 'OutputFormats', 'Data', 'Sections', 'Ancestors',
        'Parent', 'CurrentSection', 'FirstSection', 'Next', 'Prev', 'NextSection',
        'PrevSection', 'Site', 'Sites', 'All', 'ByTitle', 'ByWeight', 'ByDate',
        'ByLang', 'DefaultContentLanguage', 'Translations', 'AllTranslations',
        'IsTranslated', 'Render', 'Resources', 'Store', 'Meta', 'Keywords',
        'Description', 'Description', 'Description',
    ]);
    const fieldRegex = /\.(([a-zA-Z_][a-zA-Z0-9_]*))(?=\s|\||\}|"|'|[,)])/g;
    let fm;
    while ((fm = fieldRegex.exec(source)) !== null) {
        const fieldName = fm[1];
        // Check it's inside a {{ }} action (look back for {{)
        const beforeAction = source.lastIndexOf('{{', fm.index);
        const afterAction = source.indexOf('}}', fm.index);
        if (beforeAction === -1 || afterAction === -1 || beforeAction > afterAction) continue;
        if (skipFields.has(fieldName)) continue;
        const line = lineInfo.lineAt(fm.index).number;
        symbols.push({ name: fieldName, kind: 'property', line, indent: 0 });
    }

    // Deduplicate by name (keep first occurrence)
    const seen = new Set<string>();
    const unique: typeof symbols = [];
    for (const s of symbols) {
        if (!seen.has(s.name)) {
            seen.add(s.name);
            unique.push(s);
        }
    }

    // Sort: by kind group, then alphabetically
    unique.sort((a, b) => {
        const kindOrder = { class: 0, variable: 1, property: 2 };
        const aOrder = kindOrder[a.kind] ?? 9;
        const bOrder = kindOrder[b.kind] ?? 9;
        if (aOrder !== bOrder) return aOrder - bOrder;
        return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
    });

    return unique;
}

/**
 * Strip inline markdown formatting from heading text.
 * Removes: **bold**, *italic*, __bold__, _italic_, `code`, [link](url), ![img](url).
 */
function stripMarkdownFormatting(text: string): string {
    return text
        .replace(/!\[([^"]]*)"]\([^)]*\)/g, '$1')  // ![alt](url) → alt
        .replace(/\[([^"]]*)"]\([^)]*\)/g, '$1')      // [text](url) → text
        .replace(/`([^`]*)`/g, '$1')                     // `code` → code
        .replace(/\*\*([^`]*)\*\*/g, '$1')              // **bold** → bold
        .replace(/__([^_]*)__/g, '$1')                   // __bold__ → bold
        .replace(/\*([^`]*)\*/g, '$1')                   // *italic* → italic
        .replace(/_([^_]*)_/g, '$1')                     // _italic_ → italic
        .trim();
}

/**
 * Extract markdown headings (ATX format) from source text.
 * Returns headings up to depth 3 (h1, h2, h3) with inline formatting stripped.
 */
function extractMarkdownHeadings(source: string, lineInfo: { lineAt: (pos: number) => { number: number } }): Array<{ name: string; kind: string; line: number; indent: number }> {
    const symbols: Array<{ name: string; kind: string; line: number; indent: number }> = [];
    const lines = source.split('\n');

    for (let i = 0; i < lines.length; i++) {
        const line = lines[i];
        const match = /^(#{1,3})\s+(.+?)\s*$/.exec(line);
        if (match) {
            const level = match[1].length; // 1, 2, or 3
            const rawText = match[2];
            const name = stripMarkdownFormatting(rawText);
            if (name) {
                symbols.push({
                    name,
                    kind: 'heading',
                    line: i + 1,
                    indent: level - 1, // 0 for h1, 1 for h2, 2 for h3
                });
            }
        }
    }

    return symbols;
}

/**
 * Extract symbols from a Svelte file.
 * Finds: component props ($props()), exported functions, $state declarations, and classes.
 * Skips template-level noise (block directives, component references, internal variables).
 */
function extractSvelteSymbols(source: string, lineInfo: { lineAt: (pos: number) => { number: number } }): Array<{ name: string; kind: string; line: number; indent: number }> {
    const symbols: Array<{ name: string; kind: string; line: number; indent: number }> = [];

    // Extract script-level symbols from <script>
    try {
        const scriptMatch = /<script[^>]*>([\s\S]*?)<\/script>/.exec(source);
        if (scriptMatch) {
            const scriptContent = scriptMatch[1];
            const scriptStartLine = source.substring(0, scriptMatch.index).split('\n').length;
            const scriptLines = scriptContent.split('\n');

            for (let i = 0; i < scriptLines.length; i++) {
                const sLine = scriptLines[i].trim();
                const absLine = scriptStartLine + i;

                // Props: let { name, name2 } = $props() or let { name = default } = $props()
                const propsMatch = /let\s*\{([^}]+)\}\s*=\s*\$props\s*\(/.exec(sLine);
                if (propsMatch) {
                    for (const prop of propsMatch[1].split(',')) {
                        const name = prop.trim().split(/[=:]/)[0].trim();
                        if (name && /^[a-zA-Z_$][a-zA-Z0-9_$]*$/.test(name)) {
                            symbols.push({ name, kind: 'property', line: absLine, indent: 0 });
                        }
                    }
                    continue;
                }

                // Function declarations: function name(, export function name(, async function name(
                const funcMatch = /(?:export\s+)?(?:async\s+)?function\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*\(/.exec(sLine);
                if (funcMatch) {
                    symbols.push({ name: funcMatch[1], kind: 'function', line: absLine, indent: 0 });
                    continue;
                }

                // Arrow/function assignments: const name = (...) => / const name = function(
                const arrowMatch = /(?:const|let)\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z_$][a-zA-Z0-9_$]*)\s*=>|(?:const|let)\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*(?:async\s+)?function\b/.exec(sLine);
                if (arrowMatch) {
                    const arrowName = arrowMatch[1] || arrowMatch[2];
                    if (arrowName) {
                        symbols.push({ name: arrowName, kind: 'function', line: absLine, indent: 0 });
                        continue;
                    }
                }

                // Class declarations: class name extends/implements/{
                const classMatch = /(?:export\s+)?class\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*(?:extends|implements|\{|$)/.exec(sLine);
                if (classMatch) {
                    symbols.push({ name: classMatch[1], kind: 'class', line: absLine, indent: 0 });
                    continue;
                }

                // $state declarations: const name = $state(...) or let name = $state(...)
                const stateMatch = /(?:const|let)\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*\$state/.exec(sLine);
                if (stateMatch) {
                    symbols.push({ name: stateMatch[1], kind: 'variable', line: absLine, indent: 0 });
                    continue;
                }

                // $derived declarations: const name = $derived(...)
                const derivedMatch = /const\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*\$derived/.exec(sLine);
                if (derivedMatch) {
                    symbols.push({ name: derivedMatch[1], kind: 'variable', line: absLine, indent: 0 });
                    continue;
                }
            }
        }
    } catch {
        // Ignore parsing errors in script section
    }

    // Sort: by kind group, then alphabetically
    symbols.sort((a, b) => {
        const kindOrder = { property: 0, function: 1, class: 2, variable: 3, constant: 4 };
        const aOrder = kindOrder[a.kind] ?? 9;
        const bOrder = kindOrder[b.kind] ?? 9;
        if (aOrder !== bOrder) return aOrder - bOrder;
        return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
    });

    // Deduplicate by name + line
    const seen = new Set<string>();
    const unique: typeof symbols = [];
    for (const s of symbols) {
        const key = `${s.name}:${s.line}`;
        if (!seen.has(key)) {
            seen.add(key);
            unique.push(s);
        }
    }

    return unique.slice(0, 200);
}

/**
 * Extract global symbols (functions, classes, variables) from a CodeMirror EditorView.
 * Walks the language syntax tree and collects top-level definitions.
 * For Go templates, uses regex-based extraction (StreamLanguage has no Lezer tree).
 * For markdown, extracts ATX headings as a document outline.
 * For Svelte, extracts components, blocks, and script-level declarations.
 * Returns an array of { name, kind, line, indent } sorted by line number.
 */
export function getLanguageTreeFromView(view: any, path?: string): Array<{ name: string; kind: string; line: number; indent: number }> {
    const symbols: Array<{ name: string; kind: string; line: number; indent: number }> = [];
    try {
        const doc = view.state.doc;

        // Go templates — use regex-based extraction
        if (path) {
            const ext = path.split('.').pop()?.toLowerCase() || '';
            if (ext === 'tmpl' || ext === 'gohtml') {
                return extractGoTemplateSymbols(doc.toString(), { lineAt: (pos: number) => doc.lineAt(pos) });
            }
            // Markdown — extract headings as outline
            if (ext === 'md' || ext === 'mdx') {
                return extractMarkdownHeadings(doc.toString(), { lineAt: (pos: number) => doc.lineAt(pos) });
            }
            // Svelte — extract components, blocks, and script declarations
            if (ext === 'svelte') {
                return extractSvelteSymbols(doc.toString(), { lineAt: (pos: number) => doc.lineAt(pos) });
            }
        }

        const tree = syntaxTree(view.state);

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
            // Go
            'FunctionDecl': 'function',
            'MethodDecl': 'function',
            'VarDecl': 'variable',
            'ImportSpec': 'variable',
            'TypeDecl': 'class',
            'InterfaceType': 'class',
            'StructType': 'class',
        };

        // Track parent stack during iteration
        const parentStack: string[] = [];
        const rootNodes = new Set(['SourceFile', 'Script', 'Program', 'Document', 'CompilationUnit', 'TranslationUnit']);

        tree.iterate({
            enter(node) {
                const name = node.type.name;

                // Check if this is an identifier name node
                const isIdent = name === 'Identifier' || name === 'TypeIdentifier' || name === 'FieldIdentifier' || name === 'VariableName' || name === 'PropertyName' || name === 'VariableDefinition' || name === 'PropertyDefinition' || name === 'DefName' || name === 'FieldName' || name === 'TypeName';

                // Only capture top-level (global) identifiers.
                // Uses parent stack length to distinguish global from local scope:
                // - stackLen 1: only functions/methods (Go: [FunctionDecl], [MethodDecl])
                //   local short-var declarations also have stackLen=1 [VarDecl], excluded by kind check
                // - stackLen 2: root+declaration (JS/C: [Script, FunctionDeclaration]),
                //   or declaration+child for non-functions (Go global: [VarDecl, VarSpec])
                // This rejects stacks like [Block, VarDecl] or [FunctionDecl, VarDecl] (local/nested)
                let topDeclKind: string | null = null;
                if (parentStack.length === 1) {
                    // Only accept functions and classes at stackLen 1
                    const k = kindMap[parentStack[0]];
                    if (k === 'function' || k === 'class') topDeclKind = k;
                } else if (parentStack.length === 2) {
                    // JS/C: root wrapper at index 0, declaration at index 1
                    if (rootNodes.has(parentStack[0])) {
                        topDeclKind = kindMap[parentStack[1]] ?? null;
                    }
                    // Go global: declaration at index 0 (e.g., [VarDecl, VarSpec])
                    // But NOT if index 0 is a function/method (those contain local vars)
                    else if (kindMap[parentStack[0]] && kindMap[parentStack[0]] !== 'function') {
                        topDeclKind = kindMap[parentStack[0]];
                    }
                }
                if (isIdent && parentStack.length <= 2 && topDeclKind) {
                    const text = doc.sliceString(node.from, node.to).trim();
                    if (!text || text.length > 60) return;
                    // Skip keywords
                    if (/^(if|for|while|return|static|const|struct|typedef|sizeof|void|int|char|bool|true|false|else|break|continue|case|default|switch|do|goto|enum|union|volatile|register|extern|auto|signed|unsigned|short|long|double|float|include|define|ifdef|ifndef|endif|undef|let|var|class|function|new|this|super|import|export|from|extends|implements|interface|type|async|await|yield|try|catch|finally|throw|delete|typeof|instanceof|in|of|with|debugger|null|undefined|constructor|get|set|then|catch|finally|finally|console|log|error|warn|info|func|package|chan|map|range|defer|fallthrough|iota|nil|string|rune|byte)$/.test(text)) return;

                    const declKind = topDeclKind;

                    const line = doc.lineAt(node.from).number;
                    if (!symbols.some(s => s.name === text && s.line === line)) {
                        symbols.push({ name: text, kind: declKind, line, indent: 0 });
                    }
                }

                parentStack.push(name);
            },
            leave(node) {
                parentStack.pop();
            },
        });

        // Sort: functions first, then classes, each group alphabetically
        symbols.sort((a, b) => {
            const kindOrder = { function: 0, class: 1, constant: 2, variable: 3 };
            const aOrder = kindOrder[a.kind] ?? 9;
            const bOrder = kindOrder[b.kind] ?? 9;
            if (aOrder !== bOrder) return aOrder - bOrder;
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


