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
