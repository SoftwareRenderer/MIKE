import { t } from '$lib/i18n';
import type { MemoryAlertAckPayload, Project, SandboxMemoryStats, Task } from '$lib/types';

/** Fraction of the memory limit at which the sandbox is considered "close". */
const MEMORY_WARNING_RATIO = 0.8;

/** The limit a project predating the field runs with, which is what its settings show.
 *  Mirrors backend/types.DefaultSandboxMemoryLimitMB. */
export const DEFAULT_MEMORY_LIMIT_MB = 1024;

/** The limit a task is measured against: its project's, 0 for unlimited, null when the project is unknown. */
export function projectMemoryLimitMB(project: Pick<Project, 'sandbox_memory_limit'> | null | undefined): number | null {
    if (!project) return null;
    return parseMemoryLimitMB(project.sandbox_memory_limit ?? DEFAULT_MEMORY_LIMIT_MB);
}

/** The words for "no limit enforced" — a function, since a module value freezes the language. */
function noMemoryLimitLabel(): string {
    return t('memory.noLimit');
}

/** Canonical limit in MB; 0 means unlimited. Always sent, since the server reads an omitted field as no change. */
export function parseMemoryLimitMB(raw: string | number | null | undefined): number {
    if (raw === null || raw === undefined || raw === '') return 0;
    const n = typeof raw === 'number' ? raw : Number(raw);
    if (!Number.isFinite(n) || n <= 0) return 0;
    return Math.floor(n);
}

/** The value for a limit input: the MB number, or null (blank) when unlimited. */
export function memoryLimitToInput(mb: number | null | undefined): number | null {
    const parsed = parseMemoryLimitMB(mb);
    return parsed > 0 ? parsed : null;
}

/** Read-only rendering of a memory limit: "1024 MB", or "No limit". */
export function formatMemoryLimitMB(mb: number | null | undefined): string {
    const parsed = parseMemoryLimitMB(mb);
    return parsed > 0 ? `${parsed} MB` : noMemoryLimitLabel();
}

/** True when a running container is within MEMORY_WARNING_RATIO of its limit; false with no limit or no usage. */
export function isNearMemoryLimit(stats: SandboxMemoryStats | null | undefined): boolean {
    if (!stats || !stats.running) return false;
    if (stats.memory_limit_mb <= 0 || stats.memory_current_mb <= 0) return false;
    return stats.memory_current_mb / stats.memory_limit_mb >= MEMORY_WARNING_RATIO;
}

/** True once the peak reaches the limit. The peak is a lifetime high-water mark, so it stays true when idle. */
export function isMemoryLimitReached(stats: SandboxMemoryStats | null | undefined): boolean {
    if (!stats) return false;
    if (stats.memory_limit_mb <= 0 || stats.memory_peak_mb <= 0) return false;
    return stats.memory_peak_mb >= stats.memory_limit_mb;
}

/** True when the sandbox button shows its dot: near the limit, the peak reached it, or an OOM kill. */
export function isSandboxMemoryWarning(stats: SandboxMemoryStats | null | undefined): boolean {
    if (!stats) return false;
    return isNearMemoryLimit(stats) || stats.oom_kills > 0 || isMemoryLimitReached(stats);
}

/** What the user has to act on: the container reached its limit, or the OOM killer took a process. */
export interface MemoryAlert {
    peakMB: number;
    oomKills: number;
    limitMB: number;
}

/** The headroom an acknowledgement was taken with. */
export interface MemoryAlertAck {
    peakMB: number;
    oomKills: number;
    limitMB: number;
}

/** A task row's memory record: its last run's numbers and the alert dismissed. */
export type TaskMemoryRecord = Pick<Task, 'sandbox_memory_peak_mb' | 'sandbox_oom_kills' | 'memory_alert_ack'>;

function memoryAlertAckOf(task: TaskMemoryRecord | null | undefined): MemoryAlertAck | null {
    const stored = task?.memory_alert_ack;
    if (!stored) return null;
    return { peakMB: stored.peak_mb, oomKills: stored.oom_kills, limitMB: stored.limit_mb };
}

/** The numbers to send back when dismissing an alert, so the ack covers exactly what was shown. */
export function memoryAlertPayloadOf(alert: MemoryAlert): MemoryAlertAckPayload {
    return { peak_mb: alert.peakMB, oom_kills: alert.oomKills, limit_mb: alert.limitMB };
}

/** Peak and OOM count are cumulative, so the alert outlives the usage: only a person ends it. */
export function memoryAlert(stats: SandboxMemoryStats | null | undefined): MemoryAlert | null {
    if (!stats) return null;
    const oomKills = stats.oom_kills > 0 ? stats.oom_kills : 0;
    if (oomKills === 0 && !isMemoryLimitReached(stats)) return null;
    return {
        peakMB: stats.memory_peak_mb > 0 ? stats.memory_peak_mb : 0,
        oomKills,
        limitMB: parseMemoryLimitMB(stats.memory_limit_mb),
    };
}

/** Same limit and no worse counters. Those counters only rise, so numbers stand in for a timestamp. */
export function memoryAlertAckCovers(ack: MemoryAlertAck | null | undefined, alert: MemoryAlert): boolean {
    if (!ack) return false;
    return ack.limitMB === alert.limitMB && ack.oomKills >= alert.oomKills && ack.peakMB >= alert.peakMB;
}

/** An unacknowledged or acknowledged alert on a task, with the state it was acknowledged in. */
export interface TaskMemoryAlert {
    alert: MemoryAlert;
    acked: boolean;
}

/** The last run's numbers, for when no `sandbox_stats` frame will arrive again; null when neither was recorded. */
export function taskSandboxMemoryFallback(
    task: Pick<Task, 'sandbox_memory_peak_mb' | 'sandbox_oom_kills'> | null | undefined,
    limitMB: number,
): SandboxMemoryStats | null {
    const peakMB = task?.sandbox_memory_peak_mb ?? 0;
    const oomKills = task?.sandbox_oom_kills ?? 0;
    if (peakMB <= 0 && oomKills <= 0) return null;
    return {
        running: false,
        memory_current_mb: 0,
        memory_limit_mb: parseMemoryLimitMB(limitMB),
        memory_peak_mb: peakMB > 0 ? peakMB : 0,
        oom_kills: oomKills > 0 ? oomKills : 0,
    };
}

/** The live container's stats when it has them, otherwise what its last run left on the row. */
export function taskMemoryStats(
    task: Pick<Task, 'sandbox_memory_peak_mb' | 'sandbox_oom_kills'> | null | undefined,
    projectLimitMB: number | null,
    liveStats: SandboxMemoryStats | null | undefined,
): SandboxMemoryStats | null {
    if (liveStats) return liveStats;
    if (projectLimitMB === null) return null;
    return taskSandboxMemoryFallback(task, projectLimitMB);
}

/** Every surface reads a task's alert through here, so they cannot disagree. */
export function taskMemoryAlert(
    task: TaskMemoryRecord | null | undefined,
    projectLimitMB: number | null,
    liveStats: SandboxMemoryStats | null | undefined,
): TaskMemoryAlert | null {
    const alert = memoryAlert(taskMemoryStats(task, projectLimitMB, liveStats));
    if (!alert) return null;
    return { alert, acked: memoryAlertAckCovers(memoryAlertAckOf(task), alert) };
}

/** What a surface shows: null when there is none, and null once anyone has dismissed it. */
export function visibleMemoryAlert(
    task: TaskMemoryRecord | null | undefined,
    projectLimitMB: number | null,
    liveStats: SandboxMemoryStats | null | undefined,
): TaskMemoryAlert | null {
    const found = taskMemoryAlert(task, projectLimitMB, liveStats);
    return found && !found.acked ? found : null;
}

/** What the alert says, in the order it says it: the measurement, then what was lost. */
export function memoryAlertSentences(alert: MemoryAlert): string[] {
    const lines: string[] = [];
    if (alert.peakMB > 0) {
        lines.push(alert.limitMB > 0
            ? t('memory.alert.peakOfLimit', { peak: alert.peakMB, limit: alert.limitMB })
            : t('memory.alert.peakNoLimit', { peak: alert.peakMB }));
    }
    if (alert.oomKills > 0) {
        lines.push(t('memory.alert.kills', { count: alert.oomKills }));
    }
    return lines;
}

/** The advice, or nothing when raising the ceiling could not have changed the outcome: the limit is
 * only what stopped the task once the peak reached it. */
export function memoryAlertAction(alert: MemoryAlert): string | null {
    return alert.limitMB > 0 && alert.peakMB >= alert.limitMB ? t('memory.alert.action') : null;
}

/** One-line usage summary shared by the editor footer and the Sandbox panel; null renders as "—". */
export function formatSandboxMemory(stats: SandboxMemoryStats | null | undefined): string {
    if (!stats) return '—';
    if (!stats.running) return t('memory.notRunning');
    if (stats.memory_limit_mb > 0) {
        return `${stats.memory_current_mb} / ${stats.memory_limit_mb} MB`;
    }
    return t('memory.currentNoLimit', { mb: stats.memory_current_mb });
}
