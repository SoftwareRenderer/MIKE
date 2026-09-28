import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createChatFollow, distanceFromBottom, isAtBottom } from '$lib/composables/use-chat-follow.svelte';
import { SCROLL_BOTTOM_THRESHOLD } from '$lib/constants';

/**
 * Behavioural tests for the follow tracker. jsdom has no layout, so the scroller
 * is a fake whose scrollTop clamps like a browser, rAF is a manual queue, and the
 * ResizeObserver is fired by hand — each test drives a real layout change and
 * asserts where the viewport ends up.
 */

interface FakeState {
    scrollHeight: number;
    clientHeight: number;
    top: number;
    writes: number;
}

function makeScroller(scrollHeight = 2000, clientHeight = 500) {
    const state: FakeState = { scrollHeight, clientHeight, top: 0, writes: 0 };
    // Stand-in for a scroll container: only the properties the tracker touches.
    const el = {} as unknown as HTMLElement & {
        scrollTop: number;
        addEventListener: (t: string, cb: EventListener, opts?: unknown) => void;
        removeEventListener: (t: string, cb: EventListener) => void;
    };
    Object.defineProperty(el, 'scrollHeight', { get: () => state.scrollHeight });
    Object.defineProperty(el, 'clientHeight', { get: () => state.clientHeight });
    Object.defineProperty(el, 'scrollTop', {
        get: () => state.top,
        set: (v: number) => {
            state.writes++;
            state.top = Math.max(0, Math.min(v, Math.max(0, state.scrollHeight - state.clientHeight)));
        },
    });
    // Keep the tracker's user-input listeners so a test can dispatch a real gesture.
    const listeners = new Map<string, EventListener[]>();
    el.addEventListener = (t: string, cb: EventListener) => { listeners.set(t, [...(listeners.get(t) ?? []), cb]); };
    el.removeEventListener = (t: string) => { listeners.set(t, []); };
    const fire = (t: string, ev: Partial<Event> = {}) => { for (const cb of listeners.get(t) ?? []) cb(ev as Event); };
    return { el, state, fire };
}

// --- manual rAF -------------------------------------------------------------
let frames: Map<number, FrameRequestCallback>;
let frameId = 0;
function flushFrames(count = 1) {
    for (let i = 0; i < count; i++) {
        const pending = [...frames.entries()];
        frames.clear();
        for (const [, cb] of pending) cb(performance.now());
    }
}

// --- controllable ResizeObserver -------------------------------------------
let roCallbacks: Array<() => void>;
class FakeResizeObserver {
    constructor(cb: () => void) { roCallbacks.push(cb); }
    observe() {}
    disconnect() {}
}
function fireResize() {
    for (const cb of roCallbacks) cb();
}

const realRaf = globalThis.requestAnimationFrame;
const realCancel = globalThis.cancelAnimationFrame;
const realRO = (globalThis as { ResizeObserver?: unknown }).ResizeObserver;

beforeEach(() => {
    frames = new Map();
    roCallbacks = [];
    frameId = 0;
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback) => { frames.set(++frameId, cb); return frameId; };
    globalThis.cancelAnimationFrame = (id: number) => { frames.delete(id); };
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = FakeResizeObserver;
});

afterEach(() => {
    globalThis.requestAnimationFrame = realRaf;
    globalThis.cancelAnimationFrame = realCancel;
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = realRO;
    vi.restoreAllMocks();
});

// More frames than a pin ever needs, so a settled pin has run out.
const pinFrames = () => flushFrames(20);

describe('createChatFollow — initial attach', () => {
    it('reveals the container and pins to the bottom on the frame after attach', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller();
        expect(follow.ready).toBe(false);

        follow.attach(el, null);
        flushFrames(1);

        expect(follow.ready).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);
        expect(state.top).toBe(1500);
        follow.destroy();
    });

    it('reveals the container even when the first frame finds the viewport away from the bottom', () => {
        const follow = createChatFollow();
        const { el } = makeScroller();
        follow.attach(el, null);
        // Something moved the viewport before the attach frame ran, and no reader was holding it.
        el.scrollTop = 0;
        follow.handleScroll();
        flushFrames(2);
        expect(follow.ready).toBe(true);
        expect(follow.following).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);
        follow.destroy();
    });
});

describe('createChatFollow — pin gating', () => {
    it('skips a non-forced pin once the user scrolled away', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller();
        follow.attach(el, null);
        flushFrames(3);
        expect(follow.atBottom).toBe(true);

        fire('wheel', { deltaY: -120 } as never);
        el.scrollTop = 0;
        follow.handleScroll();
        expect(follow.atBottom).toBe(false);

        const writes = state.writes;
        expect(follow.pin()).toBe(false);
        expect(state.writes).toBe(writes);
        expect(el.scrollTop).toBe(0);
        follow.destroy();
    });

    it('applies a forced pin regardless of position', () => {
        const follow = createChatFollow();
        const { el, fire } = makeScroller();
        follow.attach(el, null);
        flushFrames(3);
        fire('wheel', { deltaY: -120 } as never);
        el.scrollTop = 0;
        follow.handleScroll();

        expect(follow.pin(true)).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);
        expect(follow.atBottom).toBe(true);
        follow.destroy();
    });

    it('returns false when nothing is attached', () => {
        const follow = createChatFollow();
        expect(follow.pin(true)).toBe(false);
        follow.destroy();
    });
});

describe('createChatFollow — settling (content-visibility placeholder heights)', () => {
    it('keeps pinning while the document height grows instead of stopping at the placeholder bottom', () => {
        const follow = createChatFollow();
        // A freshly rendered long history: scrollHeight is built from 64px
        // placeholders and realizes its true height over the following frames.
        const { el, state } = makeScroller(2000, 532);
        follow.attach(el, null);

        flushFrames(1);
        // First pin landed on the placeholder bottom…
        expect(state.top).toBe(1468);

        // …then the entries near the viewport render at their real size.
        state.scrollHeight = 3134;
        flushFrames(1);
        state.scrollHeight = 3134;
        flushFrames(4);

        expect(distanceFromBottom(el)).toBe(0);
        expect(state.top).toBe(2602);
        follow.destroy();
    });

    it('stops re-pinning once the height has settled', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();
        const writes = state.writes;
        flushFrames(10);
        expect(state.writes).toBe(writes);
        expect(distanceFromBottom(el)).toBe(0);
        follow.destroy();
    });
});

describe('createChatFollow — never fights the user', () => {
    it('aborts an in-flight pin sequence when the user scrolls away mid-pin', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);

        // Growth is still pending, so a pin frame is queued…
        state.scrollHeight = 2600;
        flushFrames(1);
        // …and the user scrolls to the top in the same window (scroll events are
        // dispatched before rAF callbacks).
        fire('wheel', { deltaY: -120 } as never);
        el.scrollTop = 0;
        follow.handleScroll();

        const writes = state.writes;
        state.scrollHeight = 3000;
        flushFrames(6);

        expect(el.scrollTop).toBe(0);
        expect(state.writes).toBe(writes);
        expect(follow.atBottom).toBe(false);
        follow.destroy();
    });

    it('resumes following as soon as the user scrolls back to the bottom', () => {
        const follow = createChatFollow();
        const { el, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();
        fire('wheel', { deltaY: -120 } as never);
        el.scrollTop = 0;
        follow.handleScroll();
        expect(follow.atBottom).toBe(false);

        el.scrollTop = 999999;
        follow.handleScroll();
        expect(follow.atBottom).toBe(true);
        follow.pin();
        expect(distanceFromBottom(el)).toBe(0);
        follow.destroy();
    });
});

describe('createChatFollow — a takeover during an in-flight pin', () => {
    it('stops a forced settle loop when the reader drags the viewport away mid-sequence', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        // Growth re-pins, forced, and the sequence has frames left when the reader moves.
        state.scrollHeight += 400;
        fireResize();
        flushFrames(1);

        // A dragged scrollbar sends its pointer events to the scroller itself.
        fire('pointerdown', { clientX: 100, clientY: 400, buttons: 1 } as never);
        fire('pointermove', { clientX: 100, clientY: 340, buttons: 1 } as never);
        el.scrollTop = 0;
        follow.handleScroll();

        const writes = state.writes;
        flushFrames(8);
        expect(follow.following).toBe(false);
        expect(el.scrollTop).toBe(0);
        expect(state.writes).toBe(writes);
        follow.destroy();
    });

    it('stops the settle loop the moment the wheel goes up, without waiting for the scroll event', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        // Tokens are arriving, so a pin sequence is in flight.
        state.scrollHeight += 400;
        fireResize();
        flushFrames(1);

        // The scroll event for that gesture arrives after the next frame, too late to be trusted:
        // a pin frame has already written over the reader's position by then.
        fire('wheel', { deltaY: -120 } as never);
        const writes = state.writes;
        state.scrollHeight += 400;
        flushFrames(8);

        expect(follow.following).toBe(false);
        expect(state.writes).toBe(writes);
        expect(distanceFromBottom(el)).toBeGreaterThan(0);
        follow.destroy();
    });

    it('stops following on a key that asks for what is above', () => {
        for (const key of ['ArrowUp', 'PageUp', 'Home']) {
            const follow = createChatFollow();
            const { el, state, fire } = makeScroller(2000, 500);
            follow.attach(el, null);
            pinFrames();
            state.scrollHeight += 400;
            fireResize();
            flushFrames(1);

            fire('keydown', { key } as never);
            const writes = state.writes;
            flushFrames(6);

            expect(follow.following, `${key} is a request for what is above`).toBe(false);
            expect(state.writes, `${key} left a settle loop writing`).toBe(writes);
            follow.destroy();
        }
    });

    it('leaves a gesture that asks for the latest to the scroll event', () => {
        for (const gesture of [
            { event: 'wheel', args: { deltaY: 120 } },
            { event: 'keydown', args: { key: 'PageDown' } },
        ]) {
            const follow = createChatFollow();
            const { el, state, fire } = makeScroller(2000, 500);
            follow.attach(el, null);
            pinFrames();

            fire(gesture.event, gesture.args as never);
            state.scrollHeight += 400;
            fireResize();
            flushFrames(6);

            expect(follow.following, `${gesture.event} ${JSON.stringify(gesture.args)} stopped following`).toBe(true);
            expect(distanceFromBottom(el)).toBe(0);
            follow.destroy();
        }
    });

    it('re-engages following when a person forces a pin', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();
        fire('wheel', { deltaY: -120 } as never);
        el.scrollTop = 0;
        follow.handleScroll();
        expect(follow.following).toBe(false);

        // A send jumps to what was just sent, and the answer that streams after it is followed too.
        expect(follow.pin(true)).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);
        expect(follow.following).toBe(true);
        state.scrollHeight += 400;
        fireResize();
        flushFrames(6);
        expect(distanceFromBottom(el)).toBe(0);
        follow.destroy();
    });
});

describe('createChatFollow — layout changes with no scroll event', () => {
    it('re-pins when the container shrinks (panel/viewport/keyboard resize)', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();
        expect(distanceFromBottom(el)).toBe(0);

        // Viewport shrinks: scroll anchoring keeps the visible text, so the latest
        // message drops below the fold.
        state.clientHeight = 20;
        fireResize();

        expect(distanceFromBottom(el)).toBe(0);
        expect(follow.atBottom).toBe(true);
        follow.destroy();
    });

    it('re-pins when content grows below the fold (late image, async label)', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        state.scrollHeight += 400;
        fireResize();
        flushFrames(3);

        expect(distanceFromBottom(el)).toBe(0);
        follow.destroy();
    });

    it('leaves a scrolled-up reader alone when the layout changes', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();
        fire('wheel', { deltaY: -120 } as never);
        el.scrollTop = 100;
        follow.handleScroll();

        const writes = state.writes;
        state.scrollHeight += 400;
        fireResize();
        flushFrames(3);

        expect(el.scrollTop).toBe(100);
        expect(state.writes).toBe(writes);
        expect(follow.atBottom).toBe(false);
        follow.destroy();
    });
});

describe('createChatFollow — who is driving the viewport', () => {
    it('re-pins a scroll event caused by growth the observer already handled', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        // The document grew and the browser dispatched the scroll event after the
        // resize callback ran, so nothing else would close the gap.
        state.scrollHeight += 600; // scroll anchoring left the viewport where it was
        follow.handleScroll();
        flushFrames(3);

        expect(follow.following).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);
        follow.destroy();
    });

    it('treats a gap the reader drove as a takeover even when the layout grew too', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        fire('touchmove'); // a finger is driving the viewport
        state.scrollHeight += 600; // streaming content arrived in the same moment
        el.scrollTop = 0;
        follow.handleScroll();
        flushFrames(4);

        expect(follow.following).toBe(false);
        expect(el.scrollTop).toBe(0);
        follow.destroy();
    });

    it('leaves following on when nothing was driving the viewport upward', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        // The viewport moved with no reader holding it, so they are not the one who left the bottom.
        el.scrollTop = 0;
        follow.handleScroll();
        flushFrames(4);

        expect(follow.following).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);
        expect(state.writes).toBeGreaterThan(0);
        follow.destroy();
    });

    it('stops following on a fling that coasts on after the last touchmove', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        fire('touchmove');
        el.scrollTop = 600; // the coast carries the reader up; no further input events arrive
        follow.handleScroll();
        const writes = state.writes;
        flushFrames(4);

        expect(follow.following).toBe(false);
        expect(el.scrollTop).toBe(600);
        expect(state.writes).toBe(writes);
        follow.destroy();
    });

    it('leaves a press that never travelled alone: clicking a message is not scrolling', () => {
        const follow = createChatFollow();
        const { el, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        fire('pointerdown', { clientX: 100, clientY: 400, buttons: 1 } as never);
        el.scrollTop = 1200; // what the click expanded above the viewport moved the reader
        follow.handleScroll();

        expect(follow.following).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);
        follow.destroy();
    });

    it('ignores keystrokes that cannot scroll the container', () => {
        const follow = createChatFollow();
        const { el, state, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        fire('keydown', { key: 'a' } as never); // typing, not scrolling
        state.scrollHeight += 600;
        follow.handleScroll();
        flushFrames(4);

        expect(follow.following).toBe(true);
        expect(distanceFromBottom(el)).toBe(0);

        fire('keydown', { key: 'PageUp' } as never);
        el.scrollTop = 0;
        follow.handleScroll();
        expect(follow.following).toBe(false);
        follow.destroy();
    });

    it('re-engages when the user scrolls back to the bottom', () => {
        const follow = createChatFollow();
        const { el, fire } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();
        fire('wheel', { deltaY: -120 } as never);
        el.scrollTop = 0;
        follow.handleScroll();
        expect(follow.following).toBe(false);

        el.scrollTop = 999999; // dragged back to the bottom
        follow.handleScroll();
        expect(follow.following).toBe(true);
        expect(follow.atBottom).toBe(true);
        follow.destroy();
    });
});

describe('createChatFollow — atBottom truthfulness', () => {
    it('keeps the flag consistent with the real gap after growth with no scroll event', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller(2000, 500);
        follow.attach(el, null);
        pinFrames();

        // Content grows below the fold; scroll anchoring holds the visible text, so
        // scrollTop is unchanged and no scroll event fires at all.
        state.scrollHeight += 1000;
        fireResize();

        expect(distanceFromBottom(el)).toBe(0);
        expect(follow.atBottom).toBe(isAtBottom(el));
        follow.destroy();
    });

    it('uses the shared bottom threshold', () => {
        const { el, state } = makeScroller(2000, 500);
        state.top = state.scrollHeight - state.clientHeight - (SCROLL_BOTTOM_THRESHOLD - 1);
        expect(isAtBottom(el)).toBe(true);
        state.top = state.scrollHeight - state.clientHeight - SCROLL_BOTTOM_THRESHOLD;
        expect(isAtBottom(el)).toBe(false);
    });

    it('takes a threshold from the caller when its pane is not the chat', () => {
        const { el, state } = makeScroller(2000, 500);
        state.top = state.scrollHeight - state.clientHeight - (SCROLL_BOTTOM_THRESHOLD + 20);
        expect(isAtBottom(el)).toBe(false);
        expect(isAtBottom(el, { threshold: SCROLL_BOTTOM_THRESHOLD + 40 })).toBe(true);
    });

    it('measures an explicit total height when the element cannot see its own content', () => {
        // A virtual list: the spacer is rows × rowHeight, while the element's scrollHeight is only
        // the height of the rows it rendered. Measuring the element would report the bottom always.
        const { el, state } = makeScroller(280, 200);
        state.top = 80;
        expect(distanceFromBottom(el)).toBe(0);
        expect(isAtBottom(el)).toBe(true);

        const totalHeight = 5000;
        expect(distanceFromBottom(el, totalHeight)).toBe(4720);
        expect(isAtBottom(el, { totalHeight })).toBe(false);
        expect(isAtBottom(el, { totalHeight, threshold: 4800 })).toBe(true);
    });
});

describe('createChatFollow — teardown', () => {
    it('ignores pins and pending frames after destroy', () => {
        const follow = createChatFollow();
        const { el, state } = makeScroller(2000, 500);
        follow.attach(el, null);
        state.scrollHeight = 2600;
        flushFrames(1);
        follow.destroy();

        const writes = state.writes;
        flushFrames(5);
        expect(state.writes).toBe(writes);
        expect(follow.pin(true)).toBe(false);
        expect(follow.el).toBeNull();
    });
});
