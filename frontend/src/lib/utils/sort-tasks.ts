import type { Task } from '$lib/types';

/** Sort tasks chronologically by last activity (oldest first, most recent last).
 * Uses `updated_at` as the primary sort key and `column_order` as a tiebreaker
 * for stable ordering when timestamps are equal. */
export function sortByActivity(tasks: Task[]): Task[] {
    return tasks.sort((a, b) => {
        if (a.updated_at < b.updated_at) return -1;
        if (a.updated_at > b.updated_at) return 1;
        return a.column_order - b.column_order;
    });
}
