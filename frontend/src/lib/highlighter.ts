import { highlightTree } from '@lezer/highlight';
import type { Language } from '@codemirror/language';
import { HIGHLIGHT_CACHE_MAX } from '$lib/constants';
import { escapeHtml } from '$lib/utils/format';
import { getLanguageSupport, resolveFile } from '$lib/utils/editor-language';
import { bracketLevelsForTree } from '$lib/syntax/bracket-pairs';
import { diffHighlighter } from '$lib/syntax/token-style';
import { fileBase, fileSuffix } from '$lib/utils/file-suffix';
import { extensionManager } from '$lib/services/extension-manager.svelte';

/** Grammar cache, keyed by file suffix (or by basename when the file has no suffix). */
const languageCache = new Map<string, Language | null>();

/** The extension list the cache was built against; a new list means the cache is stale. */
let grammarOwner: readonly unknown[] | null = null;

/** Grammar for a path, or null for plain text; the cache resets with the extension list, since a grammar comes from a manifest. */
async function loadLanguage(path: string): Promise<Language | null> {
    const extensions = extensionManager.state.extensions;
    if (grammarOwner !== extensions) {
        languageCache.clear();
        grammarOwner = extensions;
    }

    const key = fileSuffix(path) || fileBase(path);

    const cached = languageCache.get(key);
    if (cached !== undefined) return cached;

    const lang = getLanguageSupport(path)?.language ?? null;
    languageCache.set(key, lang);
    return lang;
}

/** Simple LRU cache for highlight results (keyed by path + content hash). */
const highlightCache = new Map<string, string[]>();


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
 * Merge two span lists, each already in document order, into one sorted stream for the line walk.
 * A bracket sits where no token is, so the two lists do not overlap in practice; if they ever do, the
 * span already collected wins and the bracket is dropped rather than emitting a nested `<span>`.
 */
function mergeSpans(
    a: { from: number; to: number; classes: string }[],
    b: { from: number; to: number; classes: string }[],
): { from: number; to: number; classes: string }[] {
    const out: { from: number; to: number; classes: string }[] = [];
    let i = 0;
    let j = 0;
    while (i < a.length || j < b.length) {
        const next = j >= b.length || (i < a.length && a[i].from <= b[j].from) ? a[i++] : b[j++];
        const last = out[out.length - 1];
        if (last && last.to > next.from) continue;
        out.push(next);
    }
    return out;
}

/**
 * Core highlighting logic — given a loaded Language object, parse and highlight.
 * Results are cached per file path + content hash to avoid redundant parsing.
 */
function _highlightWithLang(lines: string[], path: string, lang: Language): string[] {
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
        let styledSpans: { from: number; to: number; classes: string }[] = [];
        highlightTree(tree, diffHighlighter, (from, to, classes) => {
            styledSpans.push({ from, to, classes });
        });

        // Bracket pair colorization, at the level the editor gives the same bracket. Only a declared
        // `pairs` set makes this possible, so a file whose language declares none gets no brackets back.
        const { spec } = resolveFile(path);
        if (spec?.pairs?.length) {
            const brackets = bracketLevelsForTree(tree, spec.pairs, source).map((b) => ({
                from: b.from,
                to: b.to,
                classes: `hl-bracket-l${b.level}`,
            }));
            if (brackets.length) styledSpans = mergeSpans(styledSpans, brackets);
        }

        // Spans are in document order and the lines below run in document order, so one cursor serves
        // the whole file — restarting it per line made the walk quadratic in the span count.
        let spanIdx = 0;
        // For each line, interleave gaps (unstyled) and styled spans
        for (let i = 0; i < lines.length; i++) {
            const lStart = lineStarts[i];
            const lEnd = lineEnds[i];
            if (lStart === lEnd) continue; // empty line

            let cursor = lStart;

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
 * Async version of highlightLines — loads the language package on first use
 * (via dynamic import), caches it, then highlights synchronously.
 * Subsequent calls for the same extension are instant (language already cached).
 */
export async function highlightLinesAsync(lines: string[], path: string): Promise<string[]> {
    if (lines.length === 0) return [];

    const lang = await loadLanguage(path);
    if (!lang) return lines.map(() => '');

    return _highlightWithLang(lines, path, lang);
}

/**
 * Clear the module-level LRU highlight cache.
 * Call when switching tasks to avoid stale highlights.
 */
export function clearHighlightCache(): void {
    highlightCache.clear();
    languageCache.clear();
}
