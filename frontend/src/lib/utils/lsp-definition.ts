/**
 * Pure helpers for the editor's go-to-definition flow: URI/path handling,
 * workspace containment, LSP position conversion, definition-response
 * normalization and identifier range lookup.
 *
 * Deliberately free of DOM and EditorView dependencies so the rules that used
 * to be duplicated across the hover tooltip and the click handler live in one
 * unit-tested place. See `lsp-hover-actions.ts` for the CodeMirror side.
 */

import type { Text } from '@codemirror/state';
import { isUnderPath } from './path';

/** LSP `Position` — 0-based line and character. */
export interface LspPosition {
    line: number;
    character: number;
}

/** LSP `Range`. */
interface LspRange {
    start: LspPosition;
    end: LspPosition;
}

/**
 * A single resolved definition target — only what a jump actually uses:
 * `path` is the decoded filesystem path (`''` for a non-`file:` URI, which the
 * caller then rejects), `line` is 1-based so it goes straight to the editor.
 */
export interface DefinitionTarget {
    path: string;
    /** 1-based. */
    line: number;
}

/** An inclusive document range in CodeMirror document offsets. */
export interface CharRange {
    from: number;
    to: number;
}

/**
 * Strip the scheme and decode a `file:` URI into a filesystem path.
 *
 * @knipignore The decoding and the refusals are asserted on URI text; `pickDefinitionTarget` answers for a whole payload.
 */
export function uriToPath(uri: string): string {
    if (!uri) return '';
    // Scheme is case-insensitive per the URI spec (git's transport check is
    // not — do not "unify" the two).
    const match = /^file:\/\/([^/]*)/i.exec(uri);
    if (!match) return '';
    let rest = uri.slice(match[0].length);
    // `file://localhost/x` names a host; a bare `file:///x` does not.
    if (match[1] && match[1] !== 'localhost') return '';
    try {
        rest = decodeURIComponent(rest);
    } catch {
        /* leave a malformed escape as-is rather than dropping the target */
    }
    return rest;
}

/** Normalize a filesystem path for comparison: no trailing slash, no `.` segments. */
function normalizeRoot(root: string): string {
    return (root || '').replace(/\/+$/, '');
}

/**
 * True when `path` is `root` or lives under it. The segment-aware comparison is
 * `isUnderPath`'s; this only strips the trailing slash an LSP root can carry.
 */
export function isInsideWorkspace(path: string, root: string): boolean {
    return isUnderPath(path, normalizeRoot(root));
}

/**
 * Path of `path` relative to `root`, or null when it is outside the root.
 * This is the form both explorers open files by (project-relative).
 */
export function relativeToRoot(path: string, root: string): string | null {
    const base = normalizeRoot(root);
    if (!isInsideWorkspace(path, base)) return null;
    return path === base ? '' : path.slice(base.length + 1);
}

/** Convert a document offset to an LSP position (0-based line/character). */
export function toLspPosition(doc: Text, pos: number): LspPosition {
    const safe = Math.max(0, Math.min(pos, doc.length));
    const line = doc.lineAt(safe);
    return { line: line.number - 1, character: safe - line.from };
}

/** Identifier-ish character: letters (any script), digits, `_` and `$`. */
const ID_CHAR = /[\p{L}\p{N}_$]/u;

/**
 * Range of the identifier surrounding `pos`, or null when `pos` is not on one.
 *
 * Hovering the trailing edge of a word reports the offset *after* its last
 * character, so a position that is not on an identifier character is retried
 * one character back. The range is what keeps an interactive tooltip open
 * while the pointer travels across the word it belongs to.
 */
export function identifierRange(doc: Text, pos: number): CharRange | null {
    if (doc.length === 0) return null;
    const at = (off: number) => (off >= 0 && off < doc.length ? ID_CHAR.test(doc.sliceString(off, off + 1)) : false);
    let anchor = -1;
    if (at(pos)) anchor = pos;
    else if (at(pos - 1)) anchor = pos - 1;
    if (anchor < 0) return null;
    let from = anchor;
    while (from > 0 && at(from - 1)) from--;
    let to = anchor + 1;
    while (to < doc.length && at(to)) to++;
    return { from, to };
}

interface RawLocationLike {
    uri?: string;
    range?: LspRange;
    // LocationLink (LSP 3.15+)
    targetUri?: string;
    targetRange?: LspRange;
    targetSelectionRange?: LspRange;
    // Defensive: some shims nest the target instead of flattening it.
    target?: { uri?: string; range?: LspRange };
}

function toTarget(loc: RawLocationLike | null | undefined): DefinitionTarget | null {
    if (!loc || typeof loc !== 'object') return null;
    const nested = loc.target && typeof loc.target === 'object' ? loc.target : null;
    const uri = loc.targetUri || loc.uri || nested?.uri || '';
    if (!uri) return null;
    const range =
        loc.targetSelectionRange ||
        loc.targetRange ||
        loc.range ||
        nested?.range ||
        undefined;
    if (!range || !range.start) return null;
    return {
        path: uriToPath(uri),
        line: range.start.line + 1,
    };
}

/**
 * First usable location out of a `textDocument/definition` response.
 *
 * The spec allows `Location`, `LocationLink[]`, `Location[]` and `null`, and
 * the two callers previously disagreed on how to unwrap them (the click path
 * read `loc.target.uri`, which is not the spec's `targetUri`), so a valid
 * definition could be found by hover and missed by the click.
 */
export function pickDefinitionTarget(result: unknown): DefinitionTarget | null {
    const stack: unknown[] = Array.isArray(result) ? [...result] : [result];
    while (stack.length) {
        const item = stack.shift();
        if (!item) continue;
        if (Array.isArray(item)) {
            stack.unshift(...item);
            continue;
        }
        const target = toTarget(item as RawLocationLike);
        if (target) return target;
    }
    return null;
}
