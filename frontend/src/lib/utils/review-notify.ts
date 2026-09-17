/**
 * The rule for "a task entered Review", with no DOM in it. The signal is the `project_counts` frame,
 * which reaches every client with every project's counts. See docs/notifications.md.
 */
import type { Task } from '$lib/types';
import { sortByLastMoved } from './sort-tasks';

export const REVIEW_STATUS = 'review';

const REVIEW_VIEW_THRESHOLD = 0.5;

/** What this client last knew about a project's Review column. `ids: null` = the count alone. */
export interface ReviewBaseline {
    count: number;
    ids: string[] | null;
}

export function reviewBaseline(count: number, ids: string[] | null): ReviewBaseline {
    return { count, ids };
}

export function tasksInReview(tasks: Task[]): Task[] {
    return tasks.filter(t => t.status === REVIEW_STATUS);
}

/**
 * Which Review cards are new since `baseline`, which the caller takes only after the Review count went
 * up. That is what makes `moved_at` a usable tiebreak: the store restamps it on a column move.
 */
export function reviewArrivals(baseline: ReviewBaseline, current: Task[]): Task[] {
    if (baseline.ids) {
        const seen = new Set(baseline.ids);
        return current.filter(t => !seen.has(t.id));
    }
    if (current.length <= baseline.count) return [];
    if (baseline.count === 0) return current;
    const [newest] = sortByLastMoved(current);
    return newest ? [newest] : [];
}

export interface Span {
    start: number;
    end: number;
}

export function visibleFraction(viewport: Span, column: Span): number {
    const width = column.end - column.start;
    if (width <= 0) return 0;
    const inside = Math.min(viewport.end, column.end) - Math.max(viewport.start, column.start);
    return inside <= 0 ? 0 : Math.min(1, inside / width);
}

/** A majority of the column inside the board's scroll viewport, which on a wide board is simply on screen. */
export function columnInViewport(viewport: Span, column: Span, threshold = REVIEW_VIEW_THRESHOLD): boolean {
    return visibleFraction(viewport, column) >= threshold;
}

export interface AttentionState {
    documentVisible: boolean;
    documentFocused: boolean;
    reviewColumnInView: boolean;
}

export function isLookingAtReview(state: AttentionState): boolean {
    return state.documentVisible && state.documentFocused && state.reviewColumnInView;
}

export function shouldNotifyReview(enabled: boolean, state: AttentionState): boolean {
    return enabled && !isLookingAtReview(state);
}

export function notableArrivals(arrivals: Task[], openTaskId: string | null, selfMovedIds: Iterable<string>): Task[] {
    const selfMoved = new Set(selfMovedIds);
    return arrivals.filter(t => t.id !== openTaskId && !selfMoved.has(t.id));
}

/** A notification this client put up, and what it was standing for. */
export interface ShownNotice {
    tag: string;
    projectId: string;
    /** Null for one that speaks for the column as a whole rather than for a single task. */
    taskId: string | null;
}

/**
 * Which notifications the user has now seen: the column being in front of them retires every notice of
 * that project, batch included; an open task retires only the notice that names it, because the batch
 * also stands for the tasks still unread in it.
 */
export function dismissedTags(shown: ShownNotice[], openTaskId: string | null, reviewProject: string | null): string[] {
    if (!openTaskId && !reviewProject) return [];
    return shown
        .filter(n => (reviewProject !== null && n.projectId === reviewProject)
            || (openTaskId !== null && n.taskId === openTaskId))
        .map(n => n.tag);
}
