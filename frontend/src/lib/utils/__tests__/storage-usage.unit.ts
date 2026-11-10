import { describe, it, expect, afterEach } from 'vitest';
import { setLocale } from '$lib/i18n/runtime.svelte';
import { isStoragePressured, storageTasksSentence, storageUsageSentence } from '../storage-usage';
import type { StorageUsage } from '$lib/types';

/** A `storage_usage` reading of `used` of `total` bytes. */
const reading = (used: number, total = 1000): StorageUsage => ({ total_bytes: total, avail_bytes: total - used });
/** The same reading once the server has walked the worktrees. */
const walked = (bytes: number, count: number): StorageUsage => ({ ...reading(430, 1024), tasks_bytes: bytes, tasks_count: count });
const GiB = 1024 ** 3;

afterEach(() => {
    setLocale('en');
});

describe('isStoragePressured', () => {
    it('warns past four fifths of the filesystem, and not at it', () => {
        expect(isStoragePressured(reading(800))).toBe(false);
        expect(isStoragePressured(reading(801))).toBe(true);
        expect(isStoragePressured(reading(0))).toBe(false);
    });

    it('never warns without a reading', () => {
        expect(isStoragePressured(null)).toBe(false);
        // A zero total is no reading, not a tiny denominator.
        expect(isStoragePressured({ total_bytes: 0, avail_bytes: 0 })).toBe(false);
        // avail above total is an empty disk, not a used one.
        expect(isStoragePressured({ total_bytes: 1000, avail_bytes: 1200 })).toBe(false);
    });
});

describe('storageUsageSentence', () => {
    it('says what is used, of how much, and the share', () => {
        expect(storageUsageSentence(reading(430, 1024))).toBe('430 B of 1 KB used (42%)');
    });

    it('counts a filesystem in the units it is sized in', () => {
        expect(storageUsageSentence(reading(400 * GiB, 494 * GiB)))
            .toBe('Storage: 400 GB of 494 GB used (81%)');
    });

    it('keeps the Storage prefix on the reading the warning is for', () => {
        expect(storageUsageSentence(reading(900, 1024))).toBe('Storage: 900 B of 1 KB used (88%)');
    });

    it('has nothing to say without a reading', () => {
        expect(storageUsageSentence(null)).toBeNull();
        expect(storageUsageSentence({ total_bytes: 0, avail_bytes: 0 })).toBeNull();
    });

    it('prices the numbers in the active locale and the words in the catalogue', () => {
        setLocale('de');
        const percent = new Intl.NumberFormat('de', { style: 'percent', maximumFractionDigits: 0 }).format(1.5 / 4);
        const sentence = storageUsageSentence(reading(1.5 * GiB, 4 * GiB));
        expect(sentence).toContain('1,5 GB');
        expect(sentence).toContain(percent);
    });
});

describe('storageTasksSentence', () => {
    it('names the bytes and the checkouts they are spread over', () => {
        expect(storageTasksSentence(walked(12 * GiB, 8))).toBe('Tasks: 12 GB in 8 worktrees');
    });

    it('is one worktree when it is one worktree', () => {
        expect(storageTasksSentence(walked(512, 1))).toBe('Tasks: 512 B in 1 worktree');
    });

    it('says so in words when no task has a worktree', () => {
        expect(storageTasksSentence(walked(0, 0))).toBe('Tasks: 0 B in 0 worktrees');
    });

    it('has nothing to say before the worktrees have been walked', () => {
        // Absent, not zero: a server that could not price a checkout sends no figure.
        expect(storageTasksSentence(reading(430, 1024))).toBeNull();
        expect(storageTasksSentence(null)).toBeNull();
        expect(storageTasksSentence({ ...reading(430, 1024), tasks_count: 3 })).toBeNull();
    });

    it('prices the size in the active locale', () => {
        setLocale('de');
        expect(storageTasksSentence(walked(1.5 * GiB, 3))).toContain('1,5 GB');
    });
});
