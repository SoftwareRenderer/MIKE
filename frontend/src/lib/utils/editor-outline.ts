/**
 * The outline for an open file, for the cases LSP document symbols cannot answer.
 *
 * Three file types, and each is here for the same reason: its grammar has no definition nodes for a
 * symbol walk to find. A Go template is a stream grammar with nothing but tokens; a Markdown outline
 * *is* its ATX headings; a Svelte component's is the declarations in its `<script>` block. Everything
 * else gets its outline from the language server, and a file no server owns gets an empty outline —
 * which is the honest answer, not a missing feature.
 *
 * What used to sit here (inside `highlighter.ts`, next to code that had nothing to do with it) was a
 * tree walk hunting `FunctionDecl` / `ClassDeclaration` / `FunctionItem` and friends. Those node names
 * come from the `@codemirror/lang-*` grammars, which are deliberately out of the dependency set
 * (docs/languages.md): every language an extension covers is compiled from a declared `syntax{}`
 * profile into a `StreamLanguage`, whose tree is token names. Measured against every grammar this app
 * can load, the walk returned zero symbols for all of them — these three extractors and LSP symbols
 * produced every entry the panel ever showed. No extension can bring it back either: a manifest
 * declares data, never code.
 */
import { fileSuffix } from './file-suffix';

/** A document, read the way an extractor needs it: its text, and the line a position falls on. */
export interface OutlineDoc {
    toString(): string;
    lineAt(pos: number): { number: number };
}

/**
 * One entry of the editor's outline panel: a name, what kind of thing it is, and the lines it spans.
 * `kind` is the vocabulary the panel filters on (`visibleKinds` in FileEditor), which is also the
 * vocabulary LSP document symbols use — the panel shows these only when no server answered.
 */
export interface OutlineSymbol {
    name: string;
    kind: string;
    line: number;
    endLine: number;
    indent: number;
}

/**
 * Extract symbols from Go template source using regex.
 * Finds: {{define "name"}}, {{block "name" .}}, $var := assignments.
 */
function extractGoTemplateSymbols(source: string, doc: OutlineDoc): OutlineSymbol[] {
    const symbols: OutlineSymbol[] = [];

    // Match {{define "name"}} and {{block "name" ...}}
    const defineRegex = /\{\{-?\s*(define|block)\s+["']([^"']+)["']/g;
    let m;
    while ((m = defineRegex.exec(source)) !== null) {
        const line = doc.lineAt(m.index).number;
        symbols.push({ name: m[2], kind: 'class', line, endLine: line, indent: 0 });
    }

    // Match $var := ... (variable assignments inside actions)
    const varRegex = /\{\{-?\s*(?:with\s+)?(\$[a-zA-Z_][a-zA-Z0-9_]*)\s*:=/g;
    while ((m = varRegex.exec(source)) !== null) {
        const name = m[1].substring(1); // strip $
        const line = doc.lineAt(m.index).number;
        symbols.push({ name, kind: 'variable', line, endLine: line, indent: 0 });
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
        'Description',
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
        const line = doc.lineAt(fm.index).number;
        symbols.push({ name: fieldName, kind: 'property', line, endLine: line, indent: 0 });
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
        const kindOrder: Record<string, number> = { class: 0, variable: 1, property: 2 };
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
function extractMarkdownHeadings(source: string): OutlineSymbol[] {
    const symbols: OutlineSymbol[] = [];
    const lines = source.split('\n');

    // Track fenced code blocks (``` or ~~~) so that lines like bash comments
    // ("# comment") inside a code block are not mistaken for ATX headings.
    let inCodeBlock = false;
    let fenceChar: string | null = null;
    let fenceLength = 0;

    for (let i = 0; i < lines.length; i++) {
        const line = lines[i];

        // Fenced code block delimiter: ``` or ~~~ (3+ chars), optionally with an info string
        const fenceMatch = /^(`{3,}|~{3,})(.*)$/.exec(line);
        if (fenceMatch) {
            const char = fenceMatch[1][0];
            const len = fenceMatch[1].length;
            if (!inCodeBlock) {
                // Opening fence
                inCodeBlock = true;
                fenceChar = char;
                fenceLength = len;
            } else if (char === fenceChar && len >= fenceLength) {
                // Closing fence (same char, at least as long)
                inCodeBlock = false;
                fenceChar = null;
                fenceLength = 0;
            }
            continue;
        }

        // Skip heading detection inside code blocks
        if (inCodeBlock) continue;

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
                    endLine: i + 1,
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
function extractSvelteSymbols(source: string): OutlineSymbol[] {
    const symbols: OutlineSymbol[] = [];

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
                            symbols.push({ name, kind: 'property', line: absLine, endLine: absLine, indent: 0 });
                        }
                    }
                    continue;
                }

                // Function declarations: function name(, export function name(, async function name(
                const funcMatch = /(?:export\s+)?(?:async\s+)?function\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*\(/.exec(sLine);
                if (funcMatch) {
                    symbols.push({ name: funcMatch[1], kind: 'function', line: absLine, endLine: absLine, indent: 0 });
                    continue;
                }

                // Arrow/function assignments: const name = (...) => / const name = function(
                const arrowMatch = /(?:const|let)\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z_$][a-zA-Z0-9_$]*)\s*=>|(?:const|let)\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*(?:async\s+)?function\b/.exec(sLine);
                if (arrowMatch) {
                    const arrowName = arrowMatch[1] || arrowMatch[2];
                    if (arrowName) {
                        symbols.push({ name: arrowName, kind: 'function', line: absLine, endLine: absLine, indent: 0 });
                        continue;
                    }
                }

                // Class declarations: class name extends/implements/{
                const classMatch = /(?:export\s+)?class\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*(?:extends|implements|\{|$)/.exec(sLine);
                if (classMatch) {
                    symbols.push({ name: classMatch[1], kind: 'class', line: absLine, endLine: absLine, indent: 0 });
                    continue;
                }

                // $state declarations: const name = $state(...) or let name = $state(...)
                const stateMatch = /(?:const|let)\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*\$state/.exec(sLine);
                if (stateMatch) {
                    symbols.push({ name: stateMatch[1], kind: 'variable', line: absLine, endLine: absLine, indent: 0 });
                    continue;
                }

                // $derived declarations: const name = $derived(...)
                const derivedMatch = /const\s+([a-zA-Z_$][a-zA-Z0-9_$]*)\s*=\s*\$derived/.exec(sLine);
                if (derivedMatch) {
                    symbols.push({ name: derivedMatch[1], kind: 'variable', line: absLine, endLine: absLine, indent: 0 });
                    continue;
                }
            }
        }
    } catch {
        // Ignore parsing errors in script section
    }

    // Sort: by kind group, then alphabetically
    symbols.sort((a, b) => {
        const kindOrder: Record<string, number> = { property: 0, function: 1, class: 2, variable: 3, constant: 4 };
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
 * The outline of an open file: what the panel shows when no language server answered.
 */
export function getOutline(path: string, doc: OutlineDoc): OutlineSymbol[] {
    switch (fileSuffix(path)) {
        case 'tmpl':
        case 'gohtml':
            return extractGoTemplateSymbols(doc.toString(), doc);
        case 'md':
        case 'mdx':
            return extractMarkdownHeadings(doc.toString());
        case 'svelte':
            return extractSvelteSymbols(doc.toString());
        default:
            return [];
    }
}
