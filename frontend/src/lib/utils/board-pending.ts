import type { Project } from '$lib/types';
import { t } from '$lib/i18n';

/** How many of the server's lines the box shows; the newest is the caption, the rest sit above it. */
const SHOWN_LINES = 8;

/** What the backlog column shows while the project it belongs to has no row on the server yet. */
export interface BoardPending {
    /** The add stopped, and the box holds what git said. */
    failed: boolean;
    percent: number;
    /** What the server last said, or the phrase that names the wait. */
    caption: string;
    /** The lines before the newest one, oldest first. */
    history: string[];
    /** Git's own lines; empty unless the add stopped. */
    detail: string;
}

/**
 * Whether the backlog column owes its add button to an add in flight. It reads the row the list
 * holds, not the selected object, which the clone's own frames never touch — a bar built from the
 * selection would sit at zero for the whole clone. While this answers something other than `null`,
 * every other way to create a task must be refused too: its project id is one the server has never seen.
 */
export function addInProgress(projects: Project[], selectedId: string | undefined): BoardPending | null {
    if (!selectedId) return null;
    const row = projects.find(p => p.id === selectedId);
    if (!row?.cloning) return null;
    const reported = row.cloneProgress ?? 0;
    // A frame can carry a value that is not a number, and a bar has no width for one.
    const percent = Number.isFinite(reported) ? Math.min(100, Math.max(0, Math.round(reported))) : 0;
    const lines = (row.cloneLog ?? []).slice(-SHOWN_LINES);
    const failure = row.cloneFailure;
    if (failure) {
        return { failed: true, percent, caption: failure.message, history: lines, detail: failure.detail };
    }
    return {
        failed: false,
        percent,
        caption: lines.length ? lines[lines.length - 1] : t('board.pending.adding'),
        history: lines.slice(0, -1),
        detail: '',
    };
}
