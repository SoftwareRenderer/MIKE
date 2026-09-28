import type { TaskEventData } from '$lib/types';

/**
 * The single-slot dispatch of a task event to the chats that are open. `App.svelte`
 * publishes only for the selected or floating task; every `TaskChat` reads it and
 * keeps the frames naming its own task.
 *
 * The last frame stays readable, which is what lets a chat that opens mid-turn catch
 * the frame it missed; the replay buffer is what refills a turn already in flight.
 *
 * @knipignore The tests build their own instance.
 */
export class TaskEventStore {
    // `raw`: the payload goes to the stream handler untouched, and every publish is a
    // fresh object, so nothing here needs a deep proxy.
    private last = $state.raw<TaskEventData | null>(null);

    get current(): TaskEventData | null {
        return this.last;
    }

    publish(event: TaskEventData): void {
        this.last = event;
    }
}

export const taskEventStore = new TaskEventStore();
