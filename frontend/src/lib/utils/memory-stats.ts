import type { SandboxMemoryStats } from '$lib/types';

/** Fraction of the memory limit at which the sandbox is considered "close". */
export const MEMORY_WARNING_RATIO = 0.8;

/**
 * True when a running container's usage is within MEMORY_WARNING_RATIO of its
 * limit. False when the container is not running, has no known limit (0), or
 * the usage is unknown (0).
 */
export function isNearMemoryLimit(stats: SandboxMemoryStats | null | undefined): boolean {
    if (!stats || !stats.running) return false;
    if (stats.memory_limit_mb <= 0 || stats.memory_current_mb <= 0) return false;
    return stats.memory_current_mb / stats.memory_limit_mb >= MEMORY_WARNING_RATIO;
}

/**
 * True when the sandbox button should show its warning dot: usage is near the
 * limit, or any OOM kill happened in the container (the counter is cumulative
 * for the container's lifetime, so the warning persists after usage drops).
 */
export function isSandboxMemoryWarning(stats: SandboxMemoryStats | null | undefined): boolean {
    if (!stats) return false;
    return isNearMemoryLimit(stats) || stats.oom_kills > 0;
}
