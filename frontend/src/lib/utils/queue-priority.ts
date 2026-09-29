import type { LockWait } from '../types';

/** What the wait line knows about the task it speaks for. */
export interface SlotWaitState {
    queuePosition: number;
    lockWait?: LockWait | null;
    taskStatus: string;
    taskExecutionState: string;
}

/**
 * Whether the wait line may offer to put this task first in the queue it waits in.
 *
 * Only the queue for a provider running-task slot can be reordered, and the task's row cannot say a task
 * stands in it: `in_progress` + `idle` is also an accepted message, a parent waiting on its subtasks, an
 * unstarted subtask. A position above zero is that queue speaking — the server reports one only while a
 * slot is being waited for, so a running turn's wait (for the provider's request slot, which no click
 * reorders) never answers yes. A lock wait is not this task queued for anything either.
 */
export function canPrioritize(state: SlotWaitState): boolean {
    if (state.lockWait || state.queuePosition < 1) return false;
    return state.taskStatus === 'in_progress' && state.taskExecutionState === 'idle';
}
