/**
 * The two caches behind syntax highlighting, with no grammar here, so invalidating them costs no
 * CodeMirror import. `frontend/scripts/boot-graph.ts` fails the build when the board's load path
 * reaches an editor grammar again.
 */
import type { Language } from '@codemirror/language';
import { storeBounded } from '$lib/utils/bounded-map';

const HIGHLIGHT_CACHE_MAX = 50;

/** Highlighted lines per path + content hash. */
const highlights = new Map<string, string[]>();

/** Grammar per file suffix (or basename when there is none); `null` is plain text, and
 * nothing evicts here. */
const grammars = new Map<string, Language | null>();

export function cachedHighlights(key: string): string[] | undefined {
    return highlights.get(key);
}

export function storeHighlights(key: string, lines: string[]): void {
    storeBounded(highlights, key, lines, HIGHLIGHT_CACHE_MAX);
}

/** `undefined` is no entry; `null` is a looked-up file with no grammar. */
export function cachedGrammar(key: string): Language | null | undefined {
    return grammars.get(key);
}

export function storeGrammar(key: string, lang: Language | null): void {
    grammars.set(key, lang);
}

/**
 * Forget both caches: the files they were keyed by are gone or rewritten. A grammar comes from an
 * extension manifest, and neither key holds the extension list, so a new list invalidates both maps.
 */
export function clearHighlightCache(): void {
    highlights.clear();
    grammars.clear();
}
