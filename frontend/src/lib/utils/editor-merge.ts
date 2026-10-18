import { diffLines } from 'diff';

/**
 * Three-way merge of a file with the editor's buffer, so an unsaved edit is not the thing that gets
 * dropped. `base` is what the file held, `ours` the buffer, `theirs` the bytes the file holds now.
 */

export type ExternalChangeResult =
    | { kind: 'no-op' }
    | { kind: 'reload'; content: string }
    | { kind: 'merged'; content: string }
    /** The text holds both sides, between the markers, for a person to resolve. */
    | { kind: 'conflict'; content: string };

/** One decision for both paths: the file moved while the buffer was open, or while the browser was away. */
export function reconcileExternalChange(
    savedContent: string,
    ours: string,
    theirs: string,
): ExternalChangeResult {
    if (theirs === savedContent) return { kind: 'no-op' };
    if (ours === savedContent) return { kind: 'reload', content: theirs };
    const merged = mergeTextWithMarkers(savedContent, ours, theirs);
    if (merged.conflicts > 0) return { kind: 'conflict', content: merged.text };
    return { kind: 'merged', content: merged.text };
}

/** What a reopening editor starts from: the draft this browser left, updated for the file's bytes now.
 *  A draft with no recorded base has no merge partner, so the buffer stands alone. */
export function adoptDraft(
    draft: { text: string; base: string | null } | null,
    disk: string,
): { content: string; conflicted: boolean } {
    if (!draft) return { content: disk, conflicted: false };
    if (draft.base === null) return { content: draft.text, conflicted: false };
    const result = reconcileExternalChange(draft.base, draft.text, disk);
    if (result.kind === 'no-op') return { content: draft.text, conflicted: false };
    return { content: result.content, conflicted: result.kind === 'conflict' };
}

// The three lines a two-sided region is written with. They are fixed text, never catalog keys: they land
// inside the person's file, where the browser's language must not reach.
export const CONFLICT_OURS = '<<<<<<<unsaved';
/** @knipignore Named by the tests that assemble a conflict block by hand. */
export const CONFLICT_SEPARATOR = '=======';
/** @knipignore As above: a test names it to build a conflict block. */
export const CONFLICT_THEIRS = '>>>>>>>on-disk';

/** Which marker a line is. */
export type MarkerKind = 'ours' | 'separator' | 'theirs';

/** The marker a line carries, or null. Matched exactly, so a divider of `=` in prose is not one. */
export function markerOf(line: string): MarkerKind | null {
    if (line === CONFLICT_OURS) return 'ours';
    if (line === CONFLICT_SEPARATOR) return 'separator';
    if (line === CONFLICT_THEIRS) return 'theirs';
    return null;
}

/** One region both sides changed, spanning its marker lines. */
export interface ConflictBlock {
    /** 1-based, both ends inside the block. */
    startLine: number;
    endLine: number;
}

/** The complete `ours`/`separator`/`theirs` triples in `text`. A marker left open is not reported: it is
 *  somebody mid-edit, or a marker that was already in the file. */
export function conflictBlocks(text: string): ConflictBlock[] {
    return completeBlocks(text.split('\n').map(markerOf));
}

/** The blocks in a document's marker kinds, so a caller need not build them. */
export function completeBlocks(marks: (MarkerKind | null)[]): ConflictBlock[] {
    const blocks: ConflictBlock[] = [];
    let open = 0;
    let separated = false;
    for (let i = 0; i < marks.length; i++) {
        const mark = marks[i];
        // A second `ours` restarts the block: an unclosed one is nobody's conflict.
        if (mark === 'ours') { open = i + 1; separated = false; }
        else if (mark === 'separator' && open && !separated) separated = true;
        else if (mark === 'theirs' && open && separated) {
            blocks.push({ startLine: open, endLine: i + 1 });
            open = 0;
            separated = false;
        }
    }
    return blocks;
}

/** A single CodeMirror change that turns `before` into `after`. */
export interface TextReplace {
    from: number;
    to: number;
    insert: string;
}

/** The smallest single replace that turns `before` into `after`, by dropping what the two agree on at each
 *  end. Several edits collapse into one region, and the text out is still `after`. */
export function minimalReplace(before: string, after: string): TextReplace {
    let prefix = 0;
    const shared = Math.min(before.length, after.length);
    while (prefix < shared && before[prefix] === after[prefix]) prefix++;
    // Both suffix bounds stop at the prefix, so an insertion cannot overlap itself.
    let suffix = 0;
    while (
        suffix < before.length - prefix &&
        suffix < after.length - prefix &&
        before[before.length - 1 - suffix] === after[after.length - 1 - suffix]
    ) {
        suffix++;
    }
    return {
        from: prefix,
        to: before.length - suffix,
        insert: after.slice(prefix, after.length - suffix),
    };
}

/** Every line either side wrote survives, and a region both sides changed becomes a conflict block instead
 *  of a guess.
 *
 * @knipignore Tests merge three texts directly; `reconcileExternalChange` and `adoptDraft` decide what
 * the editor does with the answer.
 */
export function mergeTextWithMarkers(base: string, ours: string, theirs: string): { text: string; conflicts: number } {
    const baseLines = base.split('\n');
    const N = baseLines.length;
    const oChunks = toChunks(base, ours);
    const tChunks = toChunks(base, theirs);

    const out: string[] = [];
    let conflicts = 0;
    let pos = 0; // the base line the output is caught up to
    let oi = 0;
    let ti = 0;

    while (oi < oChunks.length || ti < tChunks.length) {
        const start = Math.min(oChunks[oi]?.start ?? Infinity, tChunks[ti]?.start ?? Infinity);
        for (; pos < start; pos++) out.push(baseLines[pos]);

        // Everything either side changed from `start` onward, taking a chunk that starts inside the span
        // so far: one span changed on both sides is one region, not two.
        const mine: Chunk[] = [];
        const yours: Chunk[] = [];
        let end = start;
        for (;;) {
            const oc = oChunks[oi];
            const tc = tChunks[ti];
            const opens = end === start;
            const takeOurs = !!oc && (oc.start < end || (opens && oc.start === start));
            const takeTheirs = !!tc && (tc.start < end || (opens && tc.start === start));
            if (!takeOurs && !takeTheirs) break;
            if (takeOurs && takeTheirs && oc.start > tc.start) {
                yours.push(tc); end = Math.max(end, tc.end); ti++;
            } else if (takeOurs) {
                mine.push(oc); end = Math.max(end, oc.end); oi++;
            } else {
                yours.push(tc); end = Math.max(end, tc.end); ti++;
            }
        }

        const ourText = mine.flatMap(c => c.text);
        const theirText = yours.flatMap(c => c.text);
        if (!yours.length) out.push(...ourText);
        else if (!mine.length) out.push(...theirText);
        else if (sameLines(ourText, theirText)) out.push(...ourText);
        else if (mine.some(c => c.end > c.start) && yours.some(c => c.end > c.start)) {
            // Each side replaced lines in one span: two claims on the same lines, and picking an order
            // between them would be a guess.
            conflicts++;
            out.push(CONFLICT_OURS, ...ourText, CONFLICT_SEPARATOR, ...theirText, CONFLICT_THEIRS);
        } else {
            // One side only inserted, so it displaced nothing the other touched: both stand, ours first
            // where they meet at one boundary.
            for (const c of inOrder(mine, yours)) {
                if (c.end === c.start && yours.includes(c) && mine.some(m => sameLines(m.text, c.text))) continue;
                out.push(...c.text);
            }
        }
        pos = end;
    }
    for (; pos < N; pos++) out.push(baseLines[pos]);

    return { text: out.join('\n'), conflicts };
}

// ─── internal helpers ───────────────────────────────────────────────────────

/** One side's change in base coordinates. A replacement spans the lines it took; an insertion
 *  (start === end) displaces none. */
interface Chunk {
    start: number;
    end: number;
    text: string[];
}

function linesOf(value: string): string[] {
    const parts = value.split('\n');
    if (parts[parts.length - 1] === '') parts.pop();
    return parts;
}

/** One side's diff (base -> text) as chunks, in ascending base order. */
function toChunks(base: string, text: string): Chunk[] {
    const chunks: Chunk[] = [];
    let pos = 0;
    let removed: string[] | null = null;
    const flush = (added: string[]) => {
        if (!removed) {
            chunks.push({ start: pos, end: pos, text: added });
            return;
        }
        const chunk = { start: pos, end: pos + removed.length, text: added };
        // A diff's window is wider than the change: lines it shares with what replaced them are
        // unchanged, and leaving them in would report an edit overlapping its neighbour's.
        let lead = 0;
        while (lead < removed.length && lead < added.length && removed[lead] === added[lead]) lead++;
        let trail = 0;
        while (
            trail < removed.length - lead &&
            trail < added.length - lead &&
            removed[removed.length - 1 - trail] === added[added.length - 1 - trail]
        ) trail++;
        chunk.start += lead;
        chunk.end -= trail;
        chunk.text = added.slice(lead, added.length - trail);
        chunks.push(chunk);
    };
    for (const part of diffLines(base, text)) {
        const lines = linesOf(part.value);
        if (part.removed) {
            removed = removed ? [...removed, ...lines] : lines;
        } else if (part.added) {
            flush(lines);
            removed = null;
        } else {
            if (removed) flush([]);
            removed = null;
            pos += lines.length;
        }
    }
    if (removed) flush([]);
    return chunks;
}

/** Two ascending lists of chunks as one, ours first where they start at the same base line. */
function inOrder(mine: Chunk[], yours: Chunk[]): Chunk[] {
    const all: Chunk[] = [];
    let i = 0;
    let j = 0;
    while (i < mine.length || j < yours.length) {
        if (j >= yours.length || (i < mine.length && mine[i].start <= yours[j].start)) all.push(mine[i++]);
        else all.push(yours[j++]);
    }
    return all;
}

function sameLines(a: string[], b: string[]): boolean {
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
    return true;
}
