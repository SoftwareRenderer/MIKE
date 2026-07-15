import { describe, it, expect } from 'vitest';
import { isFromCache, markCached } from '$lib/api/cache';

describe('cache markers', () => {
    it('isFromCache returns false for unmarked objects', () => {
        expect(isFromCache({})).toBe(false);
        expect(isFromCache({ data: 'value' })).toBe(false);
        expect(isFromCache([])).toBe(false);
        expect(isFromCache(null)).toBe(false);
        expect(isFromCache(undefined)).toBe(false);
        expect(isFromCache('string')).toBe(false);
        expect(isFromCache(42)).toBe(false);
    });

    it('markCached marks and returns the same object', () => {
        const data = { projects: [], config: {} };
        const marked = markCached(data);
        expect(marked).toBe(data); // same reference
        expect(isFromCache(marked)).toBe(true);
        // Original data preserved
        expect(marked.projects).toEqual([]);
        expect(marked.config).toEqual({});
    });

    it('markCached works with arrays', () => {
        const arr = [1, 2, 3];
        const marked = markCached(arr);
        expect(marked).toBe(arr);
        expect(isFromCache(marked)).toBe(true);
    });

    it('markCached is idempotent', () => {
        const data = { key: 'value' };
        markCached(data);
        markCached(data);
        expect(isFromCache(data)).toBe(true);
    });
});
