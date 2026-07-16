import { describe, it, expect } from 'vitest';
import { formatRelativeTime, formatTimestamp } from '$lib/utils/format-time';

describe('formatRelativeTime', () => {
    it('returns empty string for empty input', () => {
        expect(formatRelativeTime('')).toBe('');
    });

    it('returns empty string for invalid date', () => {
        expect(formatRelativeTime('not-a-date')).toBe('');
    });

    it('returns "just now" for very recent dates', () => {
        const now = new Date().toISOString();
        expect(formatRelativeTime(now)).toBe('just now');
    });

    it('returns minutes ago for recent dates', () => {
        const fiveMinAgo = new Date(Date.now() - 5 * 60 * 1000).toISOString();
        expect(formatRelativeTime(fiveMinAgo)).toBe('5m ago');
    });

    it('returns hours ago for dates within 24h', () => {
        const threeHoursAgo = new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString();
        expect(formatRelativeTime(threeHoursAgo)).toBe('3h ago');
    });

    it('returns days ago for dates within 7 days', () => {
        const twoDaysAgo = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString();
        expect(formatRelativeTime(twoDaysAgo)).toBe('2d ago');
    });

    it('returns date string for dates older than 7 days', () => {
        const tenDaysAgo = new Date(Date.now() - 10 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatRelativeTime(tenDaysAgo);
        expect(result).not.toBe('');
        expect(result).not.toContain('ago');
    });
});

describe('formatTimestamp', () => {
    it('returns empty for empty input', () => {
        const result = formatTimestamp('');
        expect(result.display).toBe('');
        expect(result.tooltip).toBe('');
    });

    it('returns empty for invalid date', () => {
        const result = formatTimestamp('not-a-date');
        expect(result.display).toBe('');
        expect(result.tooltip).toBe('');
    });

    it('returns time only for today', () => {
        const now = new Date().toISOString();
        const result = formatTimestamp(now);
        expect(result.display).toMatch(/\d+:\d+ (AM|PM)/);
        expect(result.tooltip).toBe('');
    });

    it('returns "Yesterday at ..." for yesterday', () => {
        const yesterday = new Date(Date.now() - 1 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatTimestamp(yesterday);
        expect(result.display).toMatch(/^Yesterday at \d+:\d+ (AM|PM)$/);
    });

    it('returns "Last WEEKDAY at ..." for 2-7 days ago', () => {
        const threeDaysAgo = new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatTimestamp(threeDaysAgo);
        expect(result.display).toMatch(/^Last \w+ at \d+:\d+ (AM|PM)$/);
    });

    it('returns "Mmm D, YYYY at ..." for older dates', () => {
        const tenDaysAgo = new Date(Date.now() - 10 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatTimestamp(tenDaysAgo);
        expect(result.display).toMatch(/\w+ \d+, \d{4} at \d+:\d+ (AM|PM)/);
    });
});

