/** How a page notices that it was not running. */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { SUSPENDED_GAP_MS } from '$lib/constants';
import { isSuspensionGap, watchSuspension, SUSPENSION_TICK_MS } from '$lib/utils/page-suspension';

describe('isSuspensionGap', () => {
    it('reads an on-time tick as a running page', () => {
        expect(isSuspensionGap(SUSPENSION_TICK_MS)).toBe(false);
        expect(isSuspensionGap(SUSPENSION_TICK_MS + SUSPENDED_GAP_MS)).toBe(false);
    });

    it('reads a tick later than its own pace plus the slack as a stopped page', () => {
        expect(isSuspensionGap(SUSPENSION_TICK_MS + SUSPENDED_GAP_MS + 1)).toBe(true);
    });
});

describe('watchSuspension', () => {
    beforeEach(() => {
        vi.useFakeTimers();
        vi.setSystemTime(new Date('2026-09-24T00:00:00Z'));
    });
    afterEach(() => { vi.useRealTimers(); });

    it('says nothing while the ticks keep their pace', () => {
        let notices = 0;
        const stop = watchSuspension(() => { notices++; });
        vi.advanceTimersByTime(SUSPENSION_TICK_MS * 6);
        expect(notices).toBe(0);
        stop();
    });

    it('says it once, on the tick that comes back', () => {
        let notices = 0;
        const stop = watchSuspension(() => { notices++; });
        vi.advanceTimersByTime(SUSPENSION_TICK_MS);
        vi.setSystemTime(new Date(Date.now() + 40_000));     // the page stops; the clock does not
        vi.advanceTimersByTime(SUSPENSION_TICK_MS);
        expect(notices).toBe(1);
        vi.advanceTimersByTime(SUSPENSION_TICK_MS);
        expect(notices).toBe(1);
        stop();
    });

    it('shares one interval between its watchers, tells each once, and stops it with the last', () => {
        let shared = 0;
        const first = watchSuspension(() => { shared++; });
        const second = watchSuspension(() => { shared++; });
        expect(vi.getTimerCount()).toBe(1);
        first();
        expect(vi.getTimerCount()).toBe(1);
        second();
        expect(vi.getTimerCount()).toBe(0);
    });
});
