import { describe, it, expect } from 'vitest';
import { isFromCache, markCached } from '$lib/api/cache';

describe('cache markers', () => {
    // The banner that says "Viewing cached data" reads this one flag, so a live read answering true would
    // be a lie — and a read of something with no properties at all (a bare `null` from an empty store) may
    // not throw on the way to answering false.
    it('answers false for anything never marked, whatever it holds', () => {
        for (const value of [{}, { data: 'value' }, [], null, undefined, 'string', 42]) {
            expect(isFromCache(value)).toBe(false);
        }
    });

    it('marks in place and hands the same object back, array or not', () => {
        const data = { projects: [], config: {} };
        const marked = markCached(data);
        expect(marked).toBe(data); // same reference
        expect(isFromCache(marked)).toBe(true);
        // Original data preserved
        expect(marked.projects).toEqual([]);
        expect(marked.config).toEqual({});

        // A mirrored board is an array of tasks, and it is marked the same way.
        const arr = [1, 2, 3];
        expect(markCached(arr)).toBe(arr);
        expect(isFromCache(arr)).toBe(true);
    });
});
