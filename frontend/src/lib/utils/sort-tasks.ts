import type { Task } from '$lib/types';

/** Timestamp as epoch millis; unparseable or missing counts as oldest. */
function time(value: string | undefined): number {
    const t = value ? Date.parse(value) : NaN;
    return isNaN(t) ? 0 : t;
}

/** Sort by last column change. By default the newest move comes first (top of the
 * column); with `oldestFirst` the least recently moved card goes to the top.
 *
 * `moved_at` changes only when a task moves column, so edits and runs never re-rank a
 * card, and a task that was never moved keeps its creation time. Equal timestamps get no
 * tiebreaker in either direction: sort is stable, so they keep server order. */
export function sortByLastMoved(tasks: Task[], oldestFirst = false): Task[] {
    return [...tasks].sort((a, b) => (oldestFirst ? time(a.moved_at) - time(b.moved_at) : time(b.moved_at) - time(a.moved_at)));
}
