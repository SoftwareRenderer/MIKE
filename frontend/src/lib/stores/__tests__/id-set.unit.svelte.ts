import { describe, it, expect, beforeEach } from 'vitest';
import { IdSet } from '$lib/stores/id-set.svelte';

describe('IdSet', () => {
    let ids: IdSet;

    beforeEach(() => {
        ids = new IdSet();
    });

    it('holds nothing at boot, so a reload shows what the server said alone', () => {
        expect(ids.has('t1')).toBe(false);
        expect([...ids.all]).toEqual([]);
    });

    it('adds and removes one id', () => {
        ids.add('t1');
        expect(ids.has('t1')).toBe(true);
        ids.delete('t1');
        expect(ids.has('t1')).toBe(false);
    });

    it('keeps the other ids while one goes', () => {
        ids.add('t1');
        ids.add('t2');
        ids.delete('t1');
        expect(ids.has('t2')).toBe(true);
    });

    it('leaves the reference alone when nothing changed, so a repeated call wakes no reader', () => {
        ids.add('t1');
        const before = ids.all;
        ids.add('t1');
        ids.delete('absent');
        expect(ids.all).toBe(before);
    });

    it('replaces the Set on every change, which is what a $derived reader depends on', () => {
        const empty = ids.all;
        ids.add('t1');
        const withOne = ids.all;
        expect(withOne).not.toBe(empty);
        expect(empty.size).toBe(0);

        ids.delete('t1');
        expect(ids.all).not.toBe(withOne);
        expect(withOne.has('t1')).toBe(true);
    });
});
