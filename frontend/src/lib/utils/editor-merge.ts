import { diffLines } from 'diff';

/**
 * Three-way merge utilities for reconciling an externally-updated file with the
 * editor's in-memory buffer without losing unsaved user edits.
 *
 * Model:
 *   - savedContent (`base`): the file content the editor last loaded from / saved
 *     to disk. This is the merge base.
 *   - ours: the current editor buffer (includes the user's unsaved edits).
 *   - theirs: freshly read disk content after an external change (`fs_event`).
 *
 * The merge is line-based. Non-overlapping changes from both sides are combined;
 * when both sides change the same region differently, the merge fails (returns
 * `null`) so the caller can keep the local buffer and warn instead of silently
 * dropping user content.
 */

/** Result of reconciling an external change against the editor buffer. */
export type ExternalChangeResult =
    | { kind: 'no-op' } // disk unchanged since we knew it (e.g. our own save echo)
    | { kind: 'reload'; content: string } // no unsaved edits -> adopt disk content
    | { kind: 'merged'; content: string } // clean 3-way merge (user edits preserved)
    | { kind: 'conflict' }; // overlapping edits -> keep local buffer, warn user

/**
 * Decide how the editor should react to an external change on disk.
 */
export function reconcileExternalChange(
    savedContent: string,
    ours: string,
    theirs: string,
): ExternalChangeResult {
    // Disk hasn't changed since the editor last loaded/saved it — nothing to do.
    if (theirs === savedContent) return { kind: 'no-op' };
    // No unsaved edits: safe to adopt the disk content wholesale.
    if (ours === savedContent) return { kind: 'reload', content: theirs };
    // Unsaved edits AND a disk change: attempt a three-way merge.
    const merged = mergeText(savedContent, ours, theirs);
    if (merged === null) return { kind: 'conflict' };
    return { kind: 'merged', content: merged };
}

/**
 * Line-based three-way merge of `base` with two derived texts (`ours`, `theirs`).
 * Returns the merged text, or `null` if `ours` and `theirs` edit the same region
 * of `base` differently (a conflict).
 */
export function mergeText(base: string, ours: string, theirs: string): string | null {
    const baseLines = base.split('\n');
    const N = baseLines.length;

    const oOps = toOps(base, ours);
    const tOps = toOps(base, theirs);

    // Per-base-line replacements from each side (base line index -> covering op).
    const oDels = buildDels(oOps);
    const tDels = buildDels(tOps);

    // Pure insertions keyed by base boundary index (0..N).
    const adds = new Map<number, { lines: string[]; side: 'o' | 't' }[]>();
    const addOp = (key: number, entry: { lines: string[]; side: 'o' | 't' }) => {
        const arr = adds.get(key) ?? [];
        // Skip an insertion already made identically by the other side (dedupe).
        if (!arr.some((e) => e.side !== entry.side && sameLines(e.lines, entry.lines))) {
            arr.push(entry);
            adds.set(key, arr);
        }
    };
    for (const op of oOps) if (op.op === 'add') addOp(op.pos, { lines: op.lines, side: 'o' });
    for (const op of tOps) if (op.op === 'add') addOp(op.pos, { lines: op.lines, side: 't' });

    const out: string[] = [];
    let i = 0;
    while (i < N) {
        const at = adds.get(i);
        if (at) for (const a of at) out.push(...a.lines);

        const od = findDel(oDels, i);
        const td = findDel(tDels, i);
        if (od && td) {
            // Both sides changed this region. Identical edits are fine; otherwise conflict.
            if (sameLines(od.lines, td.lines)) {
                out.push(...od.lines);
                i += od.count;
            } else {
                return null;
            }
        } else if (od) {
            out.push(...od.lines);
            i += od.count;
        } else if (td) {
            out.push(...td.lines);
            i += td.count;
        } else {
            out.push(baseLines[i]);
            i++;
        }
    }
    const atEnd = adds.get(N);
    if (atEnd) for (const a of atEnd) out.push(...a.lines);

    return out.join('\n');
}

// ─── internal helpers ───────────────────────────────────────────────────────

interface Op {
    op?: 'add';
    pos: number;
    count: number;
    lines: string[];
}

function linesOf(value: string): string[] {
    const parts = value.split('\n');
    if (parts[parts.length - 1] === '') parts.pop();
    return parts;
}

function countLines(value: string): number {
    return linesOf(value).length;
}

/** Convert a single diff (base -> text) into operations in base coordinates. */
function toOps(base: string, text: string): Op[] {
    const d = diffLines(base, text);
    const ops: Op[] = [];
    let basePos = 0;
    let pending: Op | null = null;
    for (const part of d) {
        const n = countLines(part.value);
        if (part.removed) {
            pending = { pos: basePos, count: n, lines: [] };
            basePos += n;
        } else if (part.added) {
            if (pending) {
                pending.lines = linesOf(part.value);
                ops.push(pending);
                pending = null;
            } else {
                ops.push({ op: 'add', pos: basePos, count: 0, lines: linesOf(part.value) });
            }
        } else {
            if (pending) {
                ops.push(pending);
                pending = null;
            }
            basePos += n;
        }
    }
    if (pending) ops.push(pending);
    return ops;
}

/** Map each base line index covered by a replacement op to that op. */
function buildDels(ops: Op[]): Map<number, Op> {
    const m = new Map<number, Op>();
    for (const op of ops) {
        if (op.op !== 'add') {
            for (let k = op.pos; k < op.pos + op.count; k++) m.set(k, op);
        }
    }
    return m;
}

/** Find the replacement op covering base line index `i`, if any. */
function findDel(m: Map<number, Op>, i: number): Op | null {
    for (const [k, v] of m) {
        if (i >= k && i < k + v.count) return v;
    }
    return null;
}

function sameLines(a: string[], b: string[]): boolean {
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
    return true;
}
