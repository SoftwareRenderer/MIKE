import { t, type MessageKey } from '$lib/i18n';

/**
 * Mirrors `types.SubtaskStatusFor` (Go), which is the authority. The frontend needs its
 * own copy so the optimistic card lands in the column the reply will use instead of
 * jumping across the board a moment later. Keep the two agreeing row for row.
 */
export function subtaskStatusFor(parentStatus: string | undefined): string {
    return parentStatus === 'in_progress' ? 'in_progress' : 'backlog';
}

/** The `execution_state` that goes with it: `idle` is the "Pending" badge, and a card
 *  the server has never run needs no state at all. */
export function subtaskExecutionStateFor(parentStatus: string | undefined): string {
    return subtaskStatusFor(parentStatus) === 'in_progress' ? 'idle' : '';
}

/** One row of a column table: the status id (data) and the key of that column's label. */
interface StatusColumn {
    id: string;
    labelKey: MessageKey;
}

/**
 * The visible label for a status id, from the table of the columns this view shows — the board's
 * columns and the task view's overview sections are different tables, so each passes its own.
 *
 * An id the table does not know is echoed as it came: calling an unknown status by a known
 * column's name would be a lie in every locale, and the id is at least the truth on screen.
 */
export function createStatusLabel(columns: readonly StatusColumn[]): (status: string) => string {
    return (status: string) => {
        const column = columns.find(c => c.id === status);
        return column ? t(column.labelKey) : status;
    };
}
