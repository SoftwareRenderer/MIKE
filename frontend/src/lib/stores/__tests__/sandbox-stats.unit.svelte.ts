import { describe, it, expect, beforeEach } from 'vitest';
import { SandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
import type { SandboxMemoryStats } from '$lib/types';

const sample: SandboxMemoryStats = {
    running: true,
    memory_current_mb: 412,
    memory_limit_mb: 1024,
    memory_peak_mb: 500,
    oom_kills: 0,
};

describe('SandboxStatsStore', () => {
    let store: SandboxStatsStore;

    beforeEach(() => {
        store = new SandboxStatsStore();
    });

    it('returns null for unknown tasks', () => {
        expect(store.get('nope')).toBeNull();
    });

    it('stores and returns stats per task', () => {
        store.set('task-1', sample);
        expect(store.get('task-1')).toEqual(sample);
        expect(store.get('task-2')).toBeNull();
    });

    it('keeps separate entries per task', () => {
        store.set('task-1', sample);
        store.set('task-2', { ...sample, memory_current_mb: 100 });
        expect(store.get('task-1')?.memory_current_mb).toBe(412);
        expect(store.get('task-2')?.memory_current_mb).toBe(100);
    });

    it('overwrites on update', () => {
        store.set('task-1', sample);
        store.set('task-1', { ...sample, memory_current_mb: 700 });
        expect(store.get('task-1')?.memory_current_mb).toBe(700);
    });

    it('removes a single entry, leaving others intact', () => {
        store.set('task-1', sample);
        store.set('task-2', { ...sample, memory_current_mb: 1 });
        store.remove('task-1');
        expect(store.get('task-1')).toBeNull();
        expect(store.get('task-2')).not.toBeNull();
    });

    it('remove is a no-op for unknown tasks', () => {
        expect(() => store.remove('nope')).not.toThrow();
    });
});
