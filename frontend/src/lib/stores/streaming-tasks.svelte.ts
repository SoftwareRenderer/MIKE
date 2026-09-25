import { IdSet } from './id-set.svelte';

/**
 * Tasks this browser has seen tokens for and has not seen the end of — the
 * frontend-only half of "is it running". `TaskChat` adds on the first event of a
 * turn and removes when the turn ends or the task is known to be idle.
 *
 * A task the server stopped keeps `status = in_progress` and only moves `execution_state`
 * to `paused`, so the answer to "is it still running" is `taskIsInFlight`
 * (`$lib/utils/task-utils.ts`), not membership in this set.
 */
export const streamingTaskStore = new IdSet();
