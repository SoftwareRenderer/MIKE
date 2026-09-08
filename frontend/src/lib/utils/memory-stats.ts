import { t } from '$lib/i18n';
import type { SandboxMemoryStats, Task } from '$lib/types';

/** Fraction of the memory limit at which the sandbox is considered "close". */
export const MEMORY_WARNING_RATIO = 0.8;

/**
 * Text used wherever a memory limit is shown read-only but none is enforced.
 *
 * A function rather than the exported constant it was: the words come from the
 * active catalog, and a module-scope value would freeze whatever language the
 * page booted in — the same trap as the old `usdSymbol` cache.
 */
export function noMemoryLimitLabel(): string {
    return t('memory.noLimit');
}

/**
 * Normalizes a memory-limit input (MB) to the canonical form used across the
 * app: a positive integer of MB, or **0 for unlimited**.
 *
 * Blank, non-numeric, zero and negative all collapse to 0 — the same convention
 * the sandbox layer already speaks (`--memory` omitted at create, cgroup limit
 * cleared on a running container, `memory_limit_mb: 0` in live stats). Never
 * returns null: the settings payloads must send an explicit 0, because an
 * omitted/null field means "don't change the limit" to the server.
 */
export function parseMemoryLimitMB(raw: string | number | null | undefined): number {
    if (raw === null || raw === undefined || raw === '') return 0;
    const n = typeof raw === 'number' ? raw : Number(raw);
    if (!Number.isFinite(n) || n <= 0) return 0;
    return Math.floor(n);
}

/**
 * The value to bind a memory-limit input to: the MB number, or null (a blank
 * field) when unlimited. Callers that have a default for an *absent* field
 * apply it first — e.g. a project created without a limit defaults to 1024 MB,
 * while a project explicitly saved unlimited reports 0 and renders blank.
 */
export function memoryLimitToInput(mb: number | null | undefined): number | null {
    const parsed = parseMemoryLimitMB(mb);
    return parsed > 0 ? parsed : null;
}

/** Read-only rendering of a memory limit: "1024 MB", or "No limit". */
export function formatMemoryLimitMB(mb: number | null | undefined): string {
    const parsed = parseMemoryLimitMB(mb);
    return parsed > 0 ? `${parsed} MB` : noMemoryLimitLabel();
}

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
 * The memory record for a task whose sandbox container is gone — the state where no
 * `sandbox_stats` event will ever arrive. Only the peak recorded as the container was removed
 * survives, on the task row; null when none was recorded, which is not a run that used no memory.
 */
export function taskSandboxMemoryFallback(
    task: Pick<Task, 'sandbox_memory_peak_mb'> | null | undefined,
    limitMB: number,
): SandboxMemoryStats | null {
    const peakMB = task?.sandbox_memory_peak_mb ?? 0;
    if (peakMB <= 0) return null;
    return {
        running: false,
        memory_current_mb: 0,
        memory_limit_mb: parseMemoryLimitMB(limitMB),
        memory_peak_mb: peakMB,
        oom_kills: 0,
    };
}

/**
 * One-line usage summary shared by all sandbox memory UIs (the editor-footer
 * LSP status tooltip and the task Sandbox panel): "N / L MB", "N MB (no
 * limit)", "Not running". null (fetch failed / no data) renders as "—".
 */
export function formatSandboxMemory(stats: SandboxMemoryStats | null | undefined): string {
    if (!stats) return '—';
    if (!stats.running) return t('memory.notRunning');
    if (stats.memory_limit_mb > 0) {
        return `${stats.memory_current_mb} / ${stats.memory_limit_mb} MB`;
    }
    return t('memory.currentNoLimit', { mb: stats.memory_current_mb });
}
