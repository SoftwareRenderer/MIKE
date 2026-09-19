import { describe, expect, it } from 'vitest';
import {
    memoryAlert,
    memoryAlertAckCovers,
    memoryAlertAction,
    memoryAlertPayloadOf,
    memoryDotSeverity,
    taskMemoryAlert,
    taskMemoryStats,
    visibleMemoryAlert,
} from '../memory-stats';
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

describe('memoryAlert', () => {
    it('fires when the peak reaches the limit', () => {
        expect(memoryAlert(stats({ memory_peak_mb: 1024 }))).toEqual({ peakMB: 1024, oomKills: 0, limitMB: 1024 });
    });

    it('stays silent while the peak is below the limit', () => {
        expect(memoryAlert(stats({ memory_peak_mb: 1023 }))).toBeNull();
    });

    it('fires on an OOM kill even with no enforced limit', () => {
        expect(memoryAlert(stats({ memory_limit_mb: 0, memory_peak_mb: 0, oom_kills: 2 }))).toEqual(
            { peakMB: 0, oomKills: 2, limitMB: 0 },
        );
    });

    it('answers null for a task with no stats at all', () => {
        expect(memoryAlert(null)).toBeNull();
        expect(memoryAlert(undefined)).toBeNull();
        expect(memoryAlert(stats())).toBeNull();
    });

    it('does not fire at full tilt below the limit — that is pressure, not an alert', () => {
        expect(memoryAlert(stats({ memory_current_mb: 1000, memory_peak_mb: 1000 }))).toBeNull();
    });
});

describe('memoryAlertAction', () => {
    it('advises a higher ceiling when the peak hit the limit', () => {
        expect(memoryAlertAction({ peakMB: 1024, oomKills: 2, limitMB: 1024 })).toContain('Raise or remove the limit');
    });

    it('advises nothing when the peak stayed under the limit', () => {
        // A higher ceiling would not have changed what the OOM killer took.
        expect(memoryAlertAction({ peakMB: 1776, oomKills: 5, limitMB: 2048 })).toBeNull();
    });

    it('advises nothing when no limit is enforced', () => {
        expect(memoryAlertAction({ peakMB: 1776, oomKills: 5, limitMB: 0 })).toBeNull();
    });
});

describe('memoryAlertAckCovers', () => {
    const alert = { peakMB: 1024, oomKills: 2, limitMB: 1024 };

    it('covers the alert it was taken at', () => {
        expect(memoryAlertAckCovers({ peakMB: 1024, oomKills: 2, limitMB: 1024 }, alert)).toBe(true);
    });

    it('is covering only for the limit it was taken against', () => {
        expect(memoryAlertAckCovers({ peakMB: 1024, oomKills: 2, limitMB: 2048 }, alert)).toBe(false);
    });

    it('re-arms on a fresh container that hits a smaller limit than the one dismissed', () => {
        const ack = { peakMB: 1024, oomKills: 0, limitMB: 1024 };
        expect(memoryAlertAckCovers(ack, { peakMB: 512, oomKills: 0, limitMB: 512 })).toBe(false);
    });

    it('re-arms when another process was killed', () => {
        expect(memoryAlertAckCovers({ peakMB: 1024, oomKills: 1, limitMB: 1024 }, alert)).toBe(false);
    });

    it('re-arms when the peak climbed past the acknowledged one', () => {
        expect(memoryAlertAckCovers({ peakMB: 900, oomKills: 2, limitMB: 1024 }, alert)).toBe(false);
    });

    it('re-arms on the same peak once the limit has been changed under it', () => {
        expect(memoryAlertAckCovers({ peakMB: 1024, oomKills: 2, limitMB: 512 }, alert)).toBe(false);
    });

    it('covers nothing when there is no acknowledgement', () => {
        expect(memoryAlertAckCovers(null, alert)).toBe(false);
        expect(memoryAlertAckCovers(undefined, alert)).toBe(false);
    });
});

describe('memoryDotSeverity', () => {
    it('warns at the ratio, in red only once the limit is reached', () => {
        expect(memoryDotSeverity(stats({ memory_current_mb: 820 }))).toBe('warning');
        expect(memoryDotSeverity(stats({ memory_current_mb: 819 }))).toBeNull();
        expect(memoryDotSeverity(stats({ memory_current_mb: 820, memory_peak_mb: 1024 }))).toBe('alert');
    });

    it('keeps alerting while the task sits idle with nowhere to go', () => {
        expect(memoryDotSeverity(stats({ running: false, memory_current_mb: 0, memory_peak_mb: 1024 }))).toBe('alert');
    });

    it('drops to no dot once the alert is acknowledged', () => {
        const ack = { peakMB: 1024, oomKills: 0, limitMB: 1024 };
        expect(memoryDotSeverity(stats({ memory_peak_mb: 1024 }), ack)).toBeNull();
    });

    it('shows live pressure under an acknowledged alert', () => {
        const ack = { peakMB: 1024, oomKills: 0, limitMB: 1024 };
        expect(memoryDotSeverity(stats({ memory_current_mb: 900, memory_peak_mb: 1024 }), ack)).toBe('warning');
    });

    it('answers null for a task that never ran sandboxed', () => {
        expect(memoryDotSeverity(null)).toBeNull();
    });
});

describe('taskMemoryStats', () => {
    it('prefers the live container over what the row remembers', () => {
        const live = stats({ memory_current_mb: 5 });
        expect(taskMemoryStats({ sandbox_memory_peak_mb: 999 }, 1024, live)).toBe(live);
    });

    it('falls back to the recorded peak once the container is gone', () => {
        expect(taskMemoryStats({ sandbox_memory_peak_mb: 1024 }, 1024, null)).toEqual({
            running: false,
            memory_current_mb: 0,
            memory_limit_mb: 1024,
            memory_peak_mb: 1024,
            oom_kills: 0,
        });
    });

    it('waits for the project before measuring the recorded peak against a limit', () => {
        expect(taskMemoryStats({ sandbox_memory_peak_mb: 1024 }, null, null)).toBeNull();
    });
});

describe('taskMemoryAlert', () => {
    it('reports the alert the row recorded, unacknowledged', () => {
        expect(taskMemoryAlert({ sandbox_memory_peak_mb: 1024 }, 1024, null)).toEqual({
            alert: { peakMB: 1024, oomKills: 0, limitMB: 1024 },
            acked: false,
        });
    });

    it('reports it acknowledged once the headroom on the row matches', () => {
        const task = { sandbox_memory_peak_mb: 1024, memory_alert_ack: { peak_mb: 1024, oom_kills: 0, limit_mb: 1024 } };
        expect(taskMemoryAlert(task, 1024, null)?.acked).toBe(true);
    });

    it('reports it unacknowledged when the ack belongs to another limit', () => {
        const task = { sandbox_memory_peak_mb: 1024, memory_alert_ack: { peak_mb: 1024, oom_kills: 0, limit_mb: 2048 } };
        expect(taskMemoryAlert(task, 1024, null)?.acked).toBe(false);
    });

    it('says nothing about a task that never touched its limit', () => {
        expect(taskMemoryAlert({ sandbox_memory_peak_mb: 512 }, 1024, null)).toBeNull();
        expect(taskMemoryAlert({}, 1024, stats())).toBeNull();
    });
});

describe('visibleMemoryAlert', () => {
    const reached = { sandbox_memory_peak_mb: 1024 };

    it('answers the alert nobody has dismissed', () => {
        expect(visibleMemoryAlert(reached, 1024, null)).toEqual({
            alert: { peakMB: 1024, oomKills: 0, limitMB: 1024 },
            acked: false,
        });
    });

    it('answers nothing once the task carries a dismissal that covers it, on any device', () => {
        const dismissed = { ...reached, memory_alert_ack: { peak_mb: 1024, oom_kills: 0, limit_mb: 1024 } };
        expect(visibleMemoryAlert(dismissed, 1024, null)).toBeNull();
    });

    it('answers nothing for a task with no alert to dismiss', () => {
        expect(visibleMemoryAlert({ sandbox_memory_peak_mb: 512 }, 1024, null)).toBeNull();
    });
});

describe('memoryAlertPayloadOf', () => {
    it('sends the alert\'s own three numbers back', () => {
        expect(memoryAlertPayloadOf({ peakMB: 1024, oomKills: 2, limitMB: 1024 }))
            .toEqual({ peak_mb: 1024, oom_kills: 2, limit_mb: 1024 });
    });
});
