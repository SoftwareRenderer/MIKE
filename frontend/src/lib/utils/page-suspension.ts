import { SUSPENDED_GAP_MS } from '$lib/constants';

export const SUSPENSION_TICK_MS = 5_000;

/** A tick later than this was not a slow frame: the page was not running between the two. Every engine stops
 * a page's timer ticks when it stops the page, which is why the clock works where `visibilitychange` and the
 * page-lifecycle `resume` event do not. */
export function isSuspensionGap(tickGapMs: number, tickMs: number = SUSPENSION_TICK_MS): boolean {
    return tickGapMs > tickMs + SUSPENDED_GAP_MS;
}

type SuspensionListener = () => void;

const listeners = new Set<SuspensionListener>();
let timer: ReturnType<typeof setInterval> | null = null;
let lastTickAt = 0;

function tick(): void {
    const now = Date.now();
    const gap = now - lastTickAt;
    lastTickAt = now;
    if (!isSuspensionGap(gap)) return;
    for (const notify of [...listeners]) notify();
}

/** Call `onSuspension` whenever the page finds it has not been running. One interval serves every caller and
 * stops when the last one leaves, and a watch's first tick is never a suspension. */
export function watchSuspension(onSuspension: SuspensionListener): () => void {
    listeners.add(onSuspension);
    if (!timer) {
        lastTickAt = Date.now();
        timer = setInterval(tick, SUSPENSION_TICK_MS);
    }
    return () => {
        listeners.delete(onSuspension);
        if (listeners.size === 0 && timer) {
            clearInterval(timer);
            timer = null;
        }
    };
}
