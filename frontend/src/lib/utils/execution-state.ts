/** The task's `execution_state` a screen should decide from.
 *
 * The board's copy of a task is only as fresh as the frame that last moved it, so the task's own row
 * answers until that copy moves on its own; after a move the copy is the fresher reading.
 *
 * @param row         the task's own `execution_state` as last read, or nothing if unread
 * @param readAgainst the board's copy when that row was read
 * @param boardCopy   the board's copy now
 */
export function trustedExecutionState(row: string | null | undefined, readAgainst: string, boardCopy: string): string {
    return row && readAgainst === boardCopy ? row : boardCopy;
}
