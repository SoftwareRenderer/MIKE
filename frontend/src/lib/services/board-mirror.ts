// The mirror holds what this browser received: a board response it fetched, or the board its frames
// left it holding. Nothing here fetches to fill it, and a board read out of the mirror is never
// written back — `cachedAt` is what the age line counts from.

import { boardCacheKey, setBoardCache } from '$lib/storage';
import type { Board } from '$lib/types';

/**
 * A burst of frames is one board, and each write is the whole board, so snapshots coalesce.
 *
 * @knipignore The coalescing tests measure this window; nothing in the app reads it.
 */
export const MIRROR_COALESCE_MS = 1500;

/** Newest snapshot per project, waiting for the window to close. */
const pending = new Map<string, Board>();
let timer: ReturnType<typeof setTimeout> | null = null;

/** Record the board as the server last answered it. The argument is captured, not copied. */
export function mirrorBoard(projectId: string, board: Board): void {
    pending.set(projectId, board);
    if (timer) return;
    timer = setTimeout(() => {
        timer = null;
        void flushBoardMirror();
    }, MIRROR_COALESCE_MS);
}

/** Write every held snapshot now. Best-effort: an unwritable store loses the mirror, not the board. */
export async function flushBoardMirror(): Promise<void> {
    if (timer) {
        clearTimeout(timer);
        timer = null;
    }
    if (pending.size === 0) return;
    const batch = [...pending.entries()];
    pending.clear();
    await Promise.all(
        batch.map(([projectId, board]) => setBoardCache(boardCacheKey(projectId), board).catch(() => {})),
    );
}

/** @knipignore Names the unwritten snapshots, which is what the tests measure. */
export function pendingMirrorProjects(): string[] {
    return [...pending.keys()];
}
