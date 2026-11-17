import { describe, it, expect, beforeEach } from 'vitest';
import { SandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
import { CommitQueueStore } from '$lib/stores/commit-queue.svelte';
import { DiffStatsStore } from '$lib/stores/diff-stats.svelte';
import type { DiffStatsEntry, SandboxMemoryStats } from '$lib/types';

/**
 * The three per-task/per-project record stores the app stream feeds: sandbox memory stats,
 * the commit queue's one slot per project, and diff stats. They are one shape and one rule:
 * the last frame the server sent survives (it stops pushing when a task goes idle, and the
 * panel keeps showing what it last heard), a change **replaces the record** so Svelte sees a
 * new reference rather than an in-place edit, and a change that changes nothing leaves the
 * record alone so a stray teardown wakes no reader.
 *
 * What each store means by membership is its own test below: `statusOf` for the commit
 * queue, `fillAbsent` for the mirror that must not outrank a pushed frame.
 */

interface StoreCase {
    label: string;
    make: () => object;
    /** The private `$state` record, read to judge reference identity. */
    field: string;
    seed: (store: any, key: string) => void;
    /** A different frame for the same key — what an overwrite must land as. */
    other: (store: any, key: string) => void;
    seedValue: unknown;
    otherValue: unknown;
    read: (store: any, key: string) => unknown;
    remove: (store: any, key: string) => void;
}

const STATS: SandboxMemoryStats = {
    running: true, memory_current_mb: 412, memory_limit_mb: 1024, memory_peak_mb: 500, oom_kills: 0,
};
const stats = (hash: string): DiffStatsEntry => ({ hash, files: 1, additions: 2, deletions: 3 });

const STORES: StoreCase[] = [
    {
        label: 'sandbox stats',
        make: () => new SandboxStatsStore(),
        field: 'stats',
        seed: (s, k) => s.set(k, STATS),
        other: (s, k) => s.set(k, { ...STATS, memory_current_mb: 100 }),
        seedValue: STATS,
        otherValue: { ...STATS, memory_current_mb: 100 },
        read: (s, k) => s.get(k),
        remove: (s, k) => s.remove(k),
    },
    {
        label: 'the commit queue',
        make: () => new CommitQueueStore(),
        field: 'slots',
        seed: (s, k) => s.set(k, { taskID: `${k}-task`, status: 'queued' }),
        other: (s, k) => s.set(k, { taskID: `${k}-task`, status: 'processing' }),
        seedValue: { taskID: 'k1-task', status: 'queued' },
        otherValue: { taskID: 'k2-task', status: 'processing' },
        read: (s, k) => s.get(k),
        remove: (s, k) => s.remove(k),
    },
    {
        label: 'diff stats',
        make: () => new DiffStatsStore(),
        field: 'entries',
        seed: (s, k) => s.set(k, stats('a')),
        other: (s, k) => s.set(k, stats('b')),
        seedValue: stats('a'),
        otherValue: stats('b'),
        read: (s, k) => s.get(k),
        remove: (s, k) => s.remove(k),
    },
];

const recordOf = (store: object, field: string) =>
    (store as unknown as Record<string, unknown>)[field] as Record<string, unknown>;

describe.each(STORES)('the $label record', (testCase) => {
    let store: any;

    beforeEach(() => {
        store = testCase.make();
    });

    it('holds nothing for a key it never heard of', () => {
        expect(testCase.read(store, 'absent')).toBeNull();
    });

    it('keeps one entry per key, and the last frame written for it', () => {
        testCase.seed(store, 'k1');
        testCase.other(store, 'k2');
        testCase.seed(store, 'k1');

        expect(testCase.read(store, 'k1')).toEqual(testCase.seedValue);
        expect(testCase.read(store, 'k2')).toEqual(testCase.otherValue);
    });

    it('removes one entry, leaves the others, and hands readers a new record', () => {
        testCase.seed(store, 'k1');
        testCase.other(store, 'k2');
        const before = recordOf(store, testCase.field);

        testCase.remove(store, 'k1');

        expect(testCase.read(store, 'k1')).toBeNull();
        expect(testCase.read(store, 'k2')).not.toBeNull();
        expect(recordOf(store, testCase.field)).not.toBe(before);
    });

    it('writes nothing when the key it was asked to remove is not there', () => {
        testCase.seed(store, 'k1');
        const before = recordOf(store, testCase.field);

        testCase.remove(store, 'absent');

        expect(recordOf(store, testCase.field)).toBe(before);
        expect(testCase.read(store, 'k1')).not.toBeNull();
    });
});

describe('the commit queue badge is the slot holder\'s alone', () => {
    let store: CommitQueueStore;
    beforeEach(() => { store = new CommitQueueStore(); });

    it('names a status only for the task holding that project\'s slot', () => {
        store.set('p1', { taskID: 't1', status: 'processing' });

        expect(store.statusOf('p1', 't1')).toBe('processing');
        expect(store.statusOf('p1', 'other')).toBeNull();
        expect(store.statusOf('p2', 't1')).toBeNull();
    });
});

describe('the diff-stats mirror', () => {
    let store: DiffStatsStore;
    beforeEach(() => { store = new DiffStatsStore(); });

    it('fills only the tasks with nothing live, so a pushed frame wins over the mirror', () => {
        store.set('t1', stats('live'));

        store.fillAbsent({ t1: stats('cached'), t2: stats('cached') });

        expect(store.get('t1')?.hash).toBe('live');
        expect(store.get('t2')?.hash).toBe('cached');
    });

    it('has() answers for what the store holds, live or mirrored', () => {
        expect(store.has('t1')).toBe(false);
        store.fillAbsent({ t1: stats('cached') });
        expect(store.has('t1')).toBe(true);
    });
});
