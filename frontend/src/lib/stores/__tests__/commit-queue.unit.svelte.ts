import { describe, it, expect, beforeEach } from 'vitest';
import { CommitQueueStore } from '$lib/stores/commit-queue.svelte';

describe('CommitQueueStore', () => {
    let store: CommitQueueStore;

    beforeEach(() => {
        store = new CommitQueueStore();
    });

    it('has an empty slot for every project', () => {
        expect(store.get('p1')).toBeNull();
        expect(store.statusOf('p1', 't1')).toBeNull();
    });

    it('keeps one slot per project', () => {
        store.set('p1', { taskID: 't1', status: 'queued' });
        store.set('p2', { taskID: 't9', status: 'processing' });
        expect(store.get('p1')?.taskID).toBe('t1');
        expect(store.get('p2')?.status).toBe('processing');
    });

    it('names a status only for the task holding the slot', () => {
        store.set('p1', { taskID: 't1', status: 'processing' });
        expect(store.statusOf('p1', 't1')).toBe('processing');
        expect(store.statusOf('p1', 'other')).toBeNull();
        expect(store.statusOf('p2', 't1')).toBeNull();
    });

    it('clears the slot the commit finished in', () => {
        store.set('p1', { taskID: 't1', status: 'queued' });
        store.remove('p1');
        expect(store.get('p1')).toBeNull();
    });

    it('remove is a no-op for an empty slot', () => {
        expect(() => store.remove('p1')).not.toThrow();
    });
});
