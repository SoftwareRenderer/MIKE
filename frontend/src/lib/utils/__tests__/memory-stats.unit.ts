import { describe, it, expect } from 'vitest';
import { MEMORY_WARNING_RATIO, isNearMemoryLimit, isMemoryLimitReached, isSandboxMemoryWarning, formatSandboxMemory } from '$lib/utils/memory-stats';
import type { SandboxMemoryStats } from '$lib/types';

function stats(overrides: Partial<SandboxMemoryStats> = {}): SandboxMemoryStats {
    return {
        running: true,
        memory_current_mb: 100,
        memory_limit_mb: 1024,
        memory_peak_mb: 0,
        oom_kills: 0,
        ...overrides,
    };
}

describe('isNearMemoryLimit', () => {
    it('is false for null/undefined', () => {
        expect(isNearMemoryLimit(null)).toBe(false);
        expect(isNearMemoryLimit(undefined)).toBe(false);
    });

    it('is false when not running', () => {
        expect(isNearMemoryLimit(stats({ running: false, memory_current_mb: 1024 }))).toBe(false);
    });

    it('is false when the limit is unknown (0)', () => {
        expect(isNearMemoryLimit(stats({ memory_limit_mb: 0, memory_current_mb: 999999 }))).toBe(false);
    });

    it('is false when the usage is unknown (0)', () => {
        expect(isNearMemoryLimit(stats({ memory_current_mb: 0 }))).toBe(false);
    });

    it('is false below the threshold', () => {
        expect(isNearMemoryLimit(stats({ memory_current_mb: 700, memory_limit_mb: 1024 }))).toBe(false);
    });

    it('is true at and above the threshold (integer-MB boundary)', () => {
        // 1024 MB limit → threshold is 819.2 MB: 819 MB is below, 820 MB is at/above.
        const atThreshold = Math.ceil(1024 * MEMORY_WARNING_RATIO);
        expect(isNearMemoryLimit(stats({ memory_current_mb: atThreshold - 1 }))).toBe(false);
        expect(isNearMemoryLimit(stats({ memory_current_mb: atThreshold }))).toBe(true);
        expect(isNearMemoryLimit(stats({ memory_current_mb: 1024 }))).toBe(true);
    });
});

describe('isMemoryLimitReached', () => {
    it('is false for null/undefined', () => {
        expect(isMemoryLimitReached(null)).toBe(false);
        expect(isMemoryLimitReached(undefined)).toBe(false);
    });

    it('is false when there is no enforced limit (0)', () => {
        expect(isMemoryLimitReached(stats({ memory_limit_mb: 0, memory_peak_mb: 999999 }))).toBe(false);
    });

    it('is false when the peak is unknown (0)', () => {
        expect(isMemoryLimitReached(stats({ memory_peak_mb: 0 }))).toBe(false);
    });

    it('is false just below the limit', () => {
        expect(isMemoryLimitReached(stats({ memory_peak_mb: 1023, memory_limit_mb: 1024 }))).toBe(false);
    });

    it('is true when the peak reached the limit', () => {
        expect(isMemoryLimitReached(stats({ memory_peak_mb: 1024, memory_limit_mb: 1024 }))).toBe(true);
    });

    it('is true when the peak exceeds the limit', () => {
        expect(isMemoryLimitReached(stats({ memory_peak_mb: 2048, memory_limit_mb: 1024 }))).toBe(true);
    });

    it('is true while not running (peak persists for the most recent idle stats)', () => {
        expect(isMemoryLimitReached(stats({ running: false, memory_current_mb: 0, memory_peak_mb: 1024, memory_limit_mb: 1024 }))).toBe(true);
    });
});

describe('isSandboxMemoryWarning', () => {
    it('is false for null/undefined', () => {
        expect(isSandboxMemoryWarning(null)).toBe(false);
        expect(isSandboxMemoryWarning(undefined)).toBe(false);
    });

    it('is false for healthy running usage', () => {
        expect(isSandboxMemoryWarning(stats({ memory_current_mb: 100 }))).toBe(false);
    });

    it('is true near the limit', () => {
        expect(isSandboxMemoryWarning(stats({ memory_current_mb: 1024 }))).toBe(true);
    });

    it('is true on OOM kills even at low usage (cumulative counter persists)', () => {
        expect(isSandboxMemoryWarning(stats({ memory_current_mb: 10, oom_kills: 1 }))).toBe(true);
        expect(isSandboxMemoryWarning(stats({ memory_current_mb: 10, oom_kills: 3 }))).toBe(true);
    });

    it('is true on OOM kills while not running (most recent stats after the task went idle)', () => {
        expect(isSandboxMemoryWarning(stats({ running: false, memory_current_mb: 0, oom_kills: 1 }))).toBe(true);
    });

    it('is true when the peak reached the limit even at low usage (persistent)', () => {
        expect(isSandboxMemoryWarning(stats({ memory_current_mb: 10, memory_peak_mb: 1024, memory_limit_mb: 1024 }))).toBe(true);
    });

    it('is true when the peak reached the limit while not running (most recent idle stats)', () => {
        expect(isSandboxMemoryWarning(stats({ running: false, memory_current_mb: 0, memory_peak_mb: 1024, memory_limit_mb: 1024 }))).toBe(true);
    });

    it('is false when the peak is just below the limit and nothing else triggers', () => {
        expect(isSandboxMemoryWarning(stats({ memory_current_mb: 100, memory_peak_mb: 1023, memory_limit_mb: 1024 }))).toBe(false);
    });
});

describe('formatSandboxMemory', () => {
    it('renders an em dash for null/undefined (no data / fetch failed)', () => {
        expect(formatSandboxMemory(null)).toBe('—');
        expect(formatSandboxMemory(undefined)).toBe('—');
    });

    it('renders "Not running" when the sandbox is not running', () => {
        expect(formatSandboxMemory(stats({ running: false }))).toBe('Not running');
    });

    it('renders "current / limit MB" when a limit is known', () => {
        expect(formatSandboxMemory(stats({ memory_current_mb: 128, memory_limit_mb: 256 }))).toBe('128 / 256 MB');
        expect(formatSandboxMemory(stats({ memory_current_mb: 0, memory_limit_mb: 256 }))).toBe('0 / 256 MB');
    });

    it('renders "current MB (no limit)" when the limit is unknown (0)', () => {
        expect(formatSandboxMemory(stats({ memory_current_mb: 42, memory_limit_mb: 0 }))).toBe('42 MB (no limit)');
    });
});
