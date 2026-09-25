import { untrack } from 'svelte';
import type { TaskEventData } from '$lib/types';
import { taskEventStore } from '$lib/stores/task-events.svelte';

/**
 * The frames `App.svelte` dispatches for the selected or floating task, handed to the chat that
 * owns `getTaskId()`.
 *
 * Nothing is handed over until `arm()` runs. A frame published before the chat has read its history
 * is not this screen's to act on — handled early it lands ahead of the rows it belongs between.
 * Arming hands over the frame published last, which is the frame a subscriber used to be handed the
 * instant it subscribed.
 *
 * Each frame reaches the handler once, and the handler runs untracked: it writes the transcript this
 * effect would otherwise read through it, and a frame that re-delivers itself restarts the turn.
 */
export function useTaskEvents(
    getTaskId: () => string,
    onEvent: (event: TaskEventData) => void,
): { arm: () => void } {
    let armed = $state(false);
    let delivered: TaskEventData | null = null;

    $effect(() => {
        if (!armed) return;
        const event = taskEventStore.current;
        if (!event || event === delivered) return;
        delivered = event;
        if (event.task_id === getTaskId()) untrack(() => onEvent(event));
    });

    return { arm: () => { armed = true; } };
}
