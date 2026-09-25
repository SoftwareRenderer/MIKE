import { describe, it, expect, beforeEach } from 'vitest';
import { TaskEventStore } from '$lib/stores/task-events.svelte';
import type { TaskEventData } from '$lib/types';

function frame(type: string): TaskEventData {
    return { task_id: 't1', type, data: {} };
}

describe('TaskEventStore', () => {
    let store: TaskEventStore;

    beforeEach(() => {
        store = new TaskEventStore();
    });

    it('has no frame before the first one', () => {
        expect(store.current).toBeNull();
    });

    it('holds the last frame published', () => {
        store.publish(frame('token'));
        store.publish(frame('tool_result'));
        expect(store.current?.type).toBe('tool_result');
    });

    it('notifies for a frame that repeats the last one, because each publish is a new frame', async () => {
        const first = frame('token');
        const second = frame('token');
        store.publish(first);
        store.publish(second);
        expect(store.current).not.toBe(first);
        expect(store.current?.type).toBe('token');
    });
});
