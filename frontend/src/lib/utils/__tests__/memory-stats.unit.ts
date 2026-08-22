import { describe, it, expect } from 'vitest';
import { MEMORY_WARNING_RATIO, isNearMemoryLimit, isSandboxMemoryWarning } from '$lib/utils/memory-stats';
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
});
