import { describe, expect, it } from 'vitest';
import {
    memoryAlertAction,
    memoryAlertPayloadOf,
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

/** The shape a dismissal is stored in. */
type StoredAck = { peak_mb: number; oom_kills: number; limit_mb: number };

/** A row carrying the dismissal someone gave, or nothing when nobody has answered the alert. */
const dismissedBy = (ack: StoredAck | null) => (ack ? { memory_alert_ack: ack } : {});

/** A container at its limit with a process killed. */
const hit = stats({ memory_limit_mb: 1024, memory_peak_mb: 1024, oom_kills: 2 });

describe('the alert a container earns', () => {
    it('fires when the peak reaches the limit', () => {
        expect(visibleMemoryAlert({}, null, stats({ memory_peak_mb: 1024 })))
            .toEqual({ alert: { peakMB: 1024, oomKills: 0, limitMB: 1024 }, acked: false });
    });

    it('stays silent while the peak is below the limit', () => {
        expect(visibleMemoryAlert({}, null, stats({ memory_peak_mb: 1023 }))).toBeNull();
    });

    it('fires on an OOM kill even with no enforced limit', () => {
        expect(visibleMemoryAlert({}, null, stats({ memory_limit_mb: 0, memory_peak_mb: 0, oom_kills: 2 })))
            .toEqual({ alert: { peakMB: 0, oomKills: 2, limitMB: 0 }, acked: false });
    });

    it('answers null for a task with no stats at all', () => {
        expect(visibleMemoryAlert({}, null, null)).toBeNull();
        expect(visibleMemoryAlert({}, null, undefined)).toBeNull();
        expect(visibleMemoryAlert({}, null, stats())).toBeNull();
    });

    it('does not fire at full tilt below the limit — that is pressure, not an alert', () => {
        expect(visibleMemoryAlert({}, null, stats({ memory_current_mb: 1000, memory_peak_mb: 1000 }))).toBeNull();
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

describe('what a dismissal covers', () => {
    it('covers the alert it was taken at', () => {
        expect(visibleMemoryAlert(dismissedBy({ peak_mb: 1024, oom_kills: 2, limit_mb: 1024 }), null, hit)).toBeNull();
    });

    it('is covering only for the limit it was taken against', () => {
        expect(visibleMemoryAlert(dismissedBy({ peak_mb: 1024, oom_kills: 2, limit_mb: 2048 }), null, hit)).not.toBeNull();
    });

    it('re-arms on a fresh container that hits a smaller limit than the one dismissed', () => {
        const smaller = stats({ memory_limit_mb: 512, memory_peak_mb: 512 });
        expect(visibleMemoryAlert(dismissedBy({ peak_mb: 1024, oom_kills: 0, limit_mb: 1024 }), null, smaller)).not.toBeNull();
    });

    it('re-arms when another process was killed', () => {
        expect(visibleMemoryAlert(dismissedBy({ peak_mb: 1024, oom_kills: 1, limit_mb: 1024 }), null, hit)).not.toBeNull();
    });

    it('re-arms when the peak climbed past the acknowledged one', () => {
        expect(visibleMemoryAlert(dismissedBy({ peak_mb: 900, oom_kills: 2, limit_mb: 1024 }), null, hit)).not.toBeNull();
    });

    it('re-arms on the same peak once the limit has been changed under it', () => {
        expect(visibleMemoryAlert(dismissedBy({ peak_mb: 1024, oom_kills: 2, limit_mb: 512 }), null, hit)).not.toBeNull();
    });

    it('covers nothing when there is no acknowledgement', () => {
        expect(visibleMemoryAlert(dismissedBy(null), null, hit)).not.toBeNull();
        expect(visibleMemoryAlert({ memory_alert_ack: undefined }, null, hit)).not.toBeNull();
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

describe('the alert a row remembers after its container', () => {
    it('shows it again when the ack belongs to another limit', () => {
        const task = { sandbox_memory_peak_mb: 1024, memory_alert_ack: { peak_mb: 1024, oom_kills: 0, limit_mb: 2048 } };
        expect(visibleMemoryAlert(task, 1024, null)).not.toBeNull();
    });

    it('says nothing when the row never recorded a peak at the limit', () => {
        expect(visibleMemoryAlert({}, 1024, stats())).toBeNull();
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
