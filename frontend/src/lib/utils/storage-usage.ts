import { t } from '$lib/i18n';
import { activeLocale } from '$lib/i18n/runtime.svelte';
import type { StorageUsage } from '$lib/types';

/** Past this share of the filesystem the Done count warns. 0.8 is what memory-stats uses too. */
const STORAGE_PRESSURE_RATIO = 0.8;

/** The figures the badge shows, or null when there is no reading. A zero total means no reading. */
function storageReading(usage: StorageUsage | null | undefined): { used: number; total: number; fraction: number } | null {
    if (!usage || !(usage.total_bytes > 0)) return null;
    const used = Math.max(0, usage.total_bytes - usage.avail_bytes);
    return { used, total: usage.total_bytes, fraction: used / usage.total_bytes };
}

/** Past the ratio only — at exactly 80% there is still room. */
export function isStoragePressured(usage: StorageUsage | null | undefined): boolean {
    const reading = storageReading(usage);
    return reading !== null && reading.fraction > STORAGE_PRESSURE_RATIO;
}

/** A filesystem-sized byte count, grouped per the active locale: 209_715_200 → "200 MB".
 *  formatBytes is the ungrouped one. */
function formatStorageBytes(bytes: number): string {
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let value = bytes;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
        value /= 1024;
        unit++;
    }
    const grouped = new Intl.NumberFormat(activeLocale(), { maximumFractionDigits: unit === 0 ? 0 : 1 });
    return `${grouped.format(value)} ${units[unit]}`;
}

/** What the disclosure says, or null when there is no reading. The warning is in the words too. */
export function storageUsageSentence(usage: StorageUsage | null | undefined): string | null {
    const reading = storageReading(usage);
    if (reading === null) return null;
    const parts = {
        used: formatStorageBytes(reading.used),
        total: formatStorageBytes(reading.total),
        percent: new Intl.NumberFormat(activeLocale(), { style: 'percent', maximumFractionDigits: 0 }).format(reading.fraction),
    };
    return reading.fraction > STORAGE_PRESSURE_RATIO
        ? t('board.column.storageLow', parts)
        : t('board.column.storageUsed', parts);
}
