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
 * True when the container's peak usage ever reached its memory limit. Peak
 * (cgroup memory.peak) is a high-water mark for the container's lifetime, so
 * once true it stays true for as long as the stats persist — including while
 * the task is idle. False when there is no enforced limit (0), the peak is
 * unknown (0 — e.g. kernels without memory.peak), or there are no stats.
 */
export function isMemoryLimitReached(stats: SandboxMemoryStats | null | undefined): boolean {
    if (!stats) return false;
    if (stats.memory_limit_mb <= 0 || stats.memory_peak_mb <= 0) return false;
    return stats.memory_peak_mb >= stats.memory_limit_mb;
}

/**
 * True when the sandbox button should show its warning dot: usage is near the
 * limit, the peak ever reached the limit, or any OOM kill happened in the
 * container (both the peak and the OOM counter are cumulative for the
 * container's lifetime, so the warning persists after usage drops).
 */
export function isSandboxMemoryWarning(stats: SandboxMemoryStats | null | undefined): boolean {
    if (!stats) return false;
    return isNearMemoryLimit(stats) || stats.oom_kills > 0 || isMemoryLimitReached(stats);
}

/**
 * One-line usage summary shared by all sandbox memory UIs (the editor-footer
 * LSP status tooltip and the task Sandbox panel): "N / L MB", "N MB (no
 * limit)", "Not running". null (fetch failed / no data) renders as "—".
 */
export function formatSandboxMemory(stats: SandboxMemoryStats | null | undefined): string {
    if (!stats) return '—';
    if (!stats.running) return 'Not running';
    if (stats.memory_limit_mb > 0) {
        return `${stats.memory_current_mb} / ${stats.memory_limit_mb} MB`;
    }
    return `${stats.memory_current_mb} MB (no limit)`;
}
