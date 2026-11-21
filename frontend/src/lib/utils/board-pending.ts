import type { Project } from '$lib/types';
import { t } from '$lib/i18n';

/** What the backlog column shows while the project it belongs to has no row on the server yet. */
export interface BoardPending {
    /** The add stopped, and the box holds what git said. */
    failed: boolean;
    /** 0-100; a percentage the stream never reported reads 0. */
    percent: number;
    /** What the server last said, or the phrase that names the wait. */
    caption: string;
    /** Git's own lines, kept apart; empty unless the add stopped. */
    detail: string;
}

/**
 * The board's reading of a project the server has never seen: `null` for an ordinary one, whose column
 * keeps its add button. While this answers something else, the caller must also refuse every other way
 * to create a task.
 */
export function projectAddPending(project: Project | null | undefined): BoardPending | null {
    if (!project?.cloning) return null;
    const reported = project.cloneProgress ?? 0;
    // A frame can carry a value that is not a number, and a bar has no width for one.
    const percent = Number.isFinite(reported) ? Math.min(100, Math.max(0, Math.round(reported))) : 0;
    const failure = project.cloneFailure;
    if (failure) {
        return { failed: true, percent, caption: failure.message, detail: failure.detail };
    }
    return { failed: false, percent, caption: project.cloneStatus || t('board.pending.adding'), detail: '' };
}
