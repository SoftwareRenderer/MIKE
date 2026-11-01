import { describe, it, expect, afterEach } from 'vitest';
import { setLocale } from '$lib/i18n/runtime.svelte';
import { isStoragePressured, storageUsageSentence } from '../storage-usage';
import type { StorageUsage } from '$lib/types';

/** A `storage_usage` reading of `used` of `total` bytes. */
const reading = (used: number, total = 1000): StorageUsage => ({ total_bytes: total, avail_bytes: total - used });
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
            .toBe('Nearly out of storage: 400 GB of 494 GB used (81%)');
    });

    it('says the disk is nearly full on the reading the warning is for', () => {
        expect(storageUsageSentence(reading(900, 1024))).toBe('Nearly out of storage: 900 B of 1 KB used (88%)');
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
