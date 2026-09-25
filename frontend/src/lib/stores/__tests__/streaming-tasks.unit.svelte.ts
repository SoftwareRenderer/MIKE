import { describe, it, expect, beforeEach } from 'vitest';
import { StreamingTaskStore } from '$lib/stores/streaming-tasks.svelte';

describe('StreamingTaskStore', () => {
    let store: StreamingTaskStore;

    beforeEach(() => {
        store = new StreamingTaskStore();
    });

    it('holds nothing at boot, so a reload shows execution_state alone', () => {
        expect(store.has('t1')).toBe(false);
    });

    it('adds and removes one task', () => {
        store.add('t1');
        expect(store.has('t1')).toBe(true);
        store.delete('t1');
        expect(store.has('t1')).toBe(false);
    });

    it('keeps other tasks while one turn ends', () => {
        store.add('t1');
        store.add('t2');
        store.delete('t1');
        expect(store.has('t2')).toBe(true);
    });

    it('is a no-op twice over: the second add and the delete of an absent task', () => {
        store.add('t1');
        const before = store.all;
        store.add('t1');
        store.delete('absent');
        expect(before.has('t1')).toBe(true);
        expect(store.has('t1')).toBe(true);
    });
});
