import type { Project } from '$lib/types';
import { t } from '$lib/i18n';

/** How many of the server's lines the box shows; the newest is the caption, the rest sit above it. */
const SHOWN_LINES = 8;

/** What the backlog column shows while the project it belongs to has no row on the server yet. */
export interface BoardPending {
    /** The add stopped, and the box holds what git said. */
    failed: boolean;
    /** 0-100 while it measures; `null` draws the bar indeterminate. */
    percent: number | null;
    /** What the server last said, or the phrase that names the wait. */
    caption: string;
    /** The lines before the newest one, oldest first. */
    history: string[];
    /** Git's own lines; empty unless the add stopped. */
    detail: string;
}

/** The percentage the stream last reported, clamped into what a bar can be drawn at. */
function clamped(project: Project): number | null {
    const reported = project.cloneProgress;
    // A frame can carry a value that is not a number, and a bar has no width for one.
    return reported === undefined || !Number.isFinite(reported)
        ? null
        : Math.min(100, Math.max(0, Math.round(reported)));
}

/**
 * The number a clone's bar draws, or `null` for an indeterminate one.
 *
 * `git clone --progress` counts objects received out of what the remote announced and prints its 100%
 * before resolving deltas, checking out files or fetching submodules. So 0 is the remote still counting
 * and compressing, and 100 is a count already spent: drawing either as progress would be a number about
 * nothing. A stopped add keeps its number, because the bar then shows where it got to.
 */
export function cloneBarPercent(project: Project): number | null {
    const shown = clamped(project);
    if (project.cloneFailure) return shown;
    return shown !== null && shown > 0 && shown < 100 ? shown : null;
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
    const percent = cloneBarPercent(row);
    const failure = row.cloneFailure;
    const lines = (row.cloneLog ?? []).slice(-SHOWN_LINES);
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
