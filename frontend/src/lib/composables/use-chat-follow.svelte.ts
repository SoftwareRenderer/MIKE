import { SCROLL_BOTTOM_THRESHOLD } from '$lib/constants';

/**
 * `content-visibility: auto` makes a freshly rendered list report placeholder heights, so a one-shot pin
 * lands well above the real bottom. A pin keeps re-pinning until the height holds steady.
 */
const PIN_SETTLE_FRAMES = 12;
const PIN_STABLE_FRAMES = 2;

/** How far a press travels inside the scroller before it is a drag rather than a click. */
const DRAG_THRESHOLD = 6;
/** Fractional layout (zoom, scaled rows) leaves a position a hair off the one that was written. */
const MOVE_SLACK = 2;

/**
 * Distance from the current scroll position to the very bottom, in px. Pass `totalHeight` only
 * where the element's own `scrollHeight` is smaller than its content, as a virtual list's is.
 *
 * @knipignore The follow tests measure where the viewport ended; `isAtBottom` answers yes-or-no.
 */
export function distanceFromBottom(el: HTMLElement, totalHeight: number = el.scrollHeight): number {
    return totalHeight - el.scrollTop - el.clientHeight;
}

export interface BottomOptions {
    /** How far above the bottom still counts as at it, in px. */
    threshold?: number;
    /** See {@link distanceFromBottom}. */
    totalHeight?: number;
}

/** Whether the viewport is close enough to the bottom to keep following. */
export function isAtBottom(el: HTMLElement, { threshold = SCROLL_BOTTOM_THRESHOLD, totalHeight }: BottomOptions = {}): boolean {
    return distanceFromBottom(el, totalHeight) < threshold;
}

/** Public surface of the chat follow tracker. */
export interface ChatFollow {
    /** The scroll container; null until ChatEntries mounts it. */
    readonly el: HTMLElement | null;
    /** True while the viewport is within SCROLL_BOTTOM_THRESHOLD of the bottom. */
    readonly atBottom: boolean;
    /**
     * True while new content should keep the viewport pinned. A move up that widens the gap under the
     * reader ends it; arriving at the bottom moving downward, or a forced pin, restarts it.
     */
    readonly following: boolean;
    /** True once the container has been attached and laid out at least once. */
    readonly ready: boolean;
    /** Register the container and the entry wrapper; both are watched for layout changes. */
    attach(scroller: HTMLElement | null, content: HTMLElement | null): void;
    /** Pin the viewport to the bottom, unless the reader has gone up and this is not forced. */
    pin(force?: boolean): boolean;
    /** Feed the container's scroll event in — this is where a takeover is judged. */
    handleScroll(): void;
    /** How many times the reader has shown they want to scroll. */
    readonly intent: number;
    /** The reader asked for what is above the viewport. */
    stopFollowing(): void;
    /** Release observers and pending frames. */
    destroy(): void;
}

/**
 * Owns the chat's "follow the latest content" behaviour, so TaskChat and ChatEntries share one idea of
 * "at the bottom" and one writer of scrollTop.
 *
 * Following ends when the viewport moves up *and* the gap under the reader widens. That pair is what only
 * a request for what is above produces: content arriving below the fold widens the gap without moving
 * `scrollTop`, and the document getting taller or shorter above the fold moves `scrollTop` with the
 * reader's row held, so their gap is what it was. A gesture whose direction is readable decides at once,
 * because its scroll event arrives a frame later, after a pin frame has already moved the viewport.
 *
 * Only a viewport that was being followed can be taken over, because the app moves it upward itself — the
 * sticky header's jump to an older prompt does — and that is not a reader asking for more of the same.
 * Misreading a takeover is the cheaper direction: auto-follow stops, and the way back is one scroll down
 * or the Latest button, whereas re-pinning a reader who did ask to go up fights them.
 */
export function createChatFollow(): ChatFollow {
    // Raw: node identity must stay exact, and a node's own properties are not reactive.
    let el: HTMLElement | null = $state.raw(null);
    let contentEl: HTMLElement | null = $state.raw(null);
    let atBottom = $state(true);
    let ready = $state(false);

    let pinRaf: number | null = null;
    let observer: ResizeObserver | null = null;
    let destroyed = false;

    let following = true;
    /** Counted so a caller can tell that the reader moved since it last looked. */
    let intentSeq = 0;
    /** Where the viewport was at the last observation, and how far it sat above the bottom then. */
    let lastTop = -1;
    let lastGap = -1;
    /** A touch is dragging this container. */
    let touchDrag = false;
    /** A pointer held down inside this container, and where it began. */
    let press: { x: number; y: number; dragging: boolean } | null = null;

    /** True while a reader holds the viewport: a touch drag (a fling coasting included), or a press-drag
     * inside the transcript. A scrollbar gesture is not in here: it reaches this element as scroll events
     * and nothing else — no `pointermove` while the thumb is dragged, no press at all for a track click. */
    function readerDriving(): boolean {
        return touchDrag || !!press?.dragging;
    }

    /** With nothing above the viewport, a gesture upward asks for nothing. */
    function hasAbove(): boolean {
        return !!el && el.scrollTop > 0;
    }

    function measure() {
        if (!el) return;
        atBottom = isAtBottom(el);
        lastTop = el.scrollTop;
        lastGap = distanceFromBottom(el);
    }

    function cancelPin() {
        if (pinRaf !== null) {
            cancelAnimationFrame(pinRaf);
            pinRaf = null;
        }
    }

    function pin(force = false): boolean {
        const node = el;
        if (!node || destroyed) return false;
        ready = true;
        if (!force && !following) return false;
        // Forcing is a person asking for the latest, so it also re-engages following.
        if (force) following = true;

        cancelPin();
        let framesLeft = PIN_SETTLE_FRAMES;
        let stableHeight = -1;
        let stableFrames = 0;

        const step = () => {
            pinRaf = null;
            // A takeover ends the sequence, forced or not: forcing authorises this pin, not later frames.
            if (!following) return;
            const height = node.scrollHeight;
            node.scrollTop = height;
            lastTop = node.scrollTop;
            // The scroll event for this write arrives next frame, so update the state now.
            measure();
            if (height === stableHeight) {
                if (++stableFrames >= PIN_STABLE_FRAMES) return;
            } else {
                stableFrames = 0;
                stableHeight = height;
            }
            if (--framesLeft <= 0) return;
            pinRaf = requestAnimationFrame(step);
        };
        step();
        return true;
    }

    function handleScroll(): void {
        if (!el || destroyed) return;
        const node = el;
        const top = node.scrollTop;
        const gap = distanceFromBottom(node);
        const at = isAtBottom(node);
        if (top < lastTop - MOVE_SLACK && gap > lastGap + MOVE_SLACK) {
            // The reader went up and the transcript below them grew: however the move arrived — a thumb
            // dragged with no pointer event behind it, a track click, a key the page-scroll handler
            // pressed for them — nothing in the layout does both.
            if (following) stopFollowing();
        } else if (at && top >= lastTop - MOVE_SLACK) {
            // Re-engaging needs the reader to have come down. A scroll event that merely finds the
            // viewport inside the bottom band is where a growth pin left it, and rows collapsing below
            // the fold move it there with no reader in sight.
            following = true;
        }
        lastTop = top;
        lastGap = gap;
        atBottom = at;
        if (following && !at) pin(true);
    }

    /** Content can grow with no scroll event, so a following reader stays pinned — unless they hold the
     * viewport, where a pin would speak over them. */
    function handleResize(): void {
        if (!el || destroyed) return;
        measure();
        if (following && !readerDriving()) pin(true);
    }

    function stopFollowing(): void {
        // Callers watch the `intent` count, so it moves even when `following` is already false.
        intentSeq++;
        if (!following) return;
        following = false;
        cancelPin();
    }

    /** Keys that scroll a container; anything else is not a takeover. */
    const SCROLL_KEYS = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' ']);
    /** The subset that asks for what is above the viewport. */
    const ABOVE_KEYS = new Set(['ArrowUp', 'PageUp', 'Home']);

    /** An upward wheel ends following at once: its scroll event arrives a frame later, after a pin
     * frame has already moved the viewport. */
    function onWheel(e: Event): void {
        if ((e as WheelEvent).deltaY < 0 && hasAbove()) stopFollowing();
    }

    function onScrollKey(e: Event): void {
        const key = (e as KeyboardEvent).key;
        if (!SCROLL_KEYS.has(key)) return;
        intentSeq++;
        if (ABOVE_KEYS.has(key) && hasAbove()) stopFollowing();
    }

    /** A touch drives the viewport, and keeps driving it while the fling coasts on past the last
     * touchmove. */
    function onTouchMove(): void {
        touchDrag = true;
        intentSeq++;
    }

    function onEndTouch(): void {
        touchDrag = false;
    }

    /** A press becomes a drag once it travels; one that never travels is a click on a message. Text
     * selected by dragging is the reader holding the viewport, so a pin keeps off it. */
    function onPointerDown(e: Event): void {
        const ev = e as PointerEvent;
        press = { x: ev.clientX, y: ev.clientY, dragging: false };
    }

    function onPointerMove(e: Event): void {
        if (!press) return;
        const ev = e as PointerEvent;
        // A press released outside the window never sends this element its pointerup, so a move with no
        // button held is how that press ends.
        if (!ev.buttons) { press = null; return; }
        if (Math.abs(ev.clientY - press.y) > DRAG_THRESHOLD || Math.abs(ev.clientX - press.x) > DRAG_THRESHOLD) {
            if (!press.dragging) intentSeq++;
            press.dragging = true;
        }
    }

    function onPointerEnd(): void {
        press = null;
    }

    function setIntentListeners(node: HTMLElement | null, on: boolean): void {
        if (!node) return;
        const method = on ? 'addEventListener' : 'removeEventListener';
        node[method]('wheel', onWheel, { passive: true });
        node[method]('touchmove', onTouchMove, { passive: true });
        node[method]('touchend', onEndTouch, { passive: true });
        node[method]('touchcancel', onEndTouch, { passive: true });
        node[method]('pointerdown', onPointerDown, { passive: true });
        node[method]('pointermove', onPointerMove, { passive: true });
        node[method]('pointerup', onPointerEnd, { passive: true });
        node[method]('pointercancel', onPointerEnd, { passive: true });
        node[method]('keydown', onScrollKey);
    }

    function attach(scroller: HTMLElement | null, content: HTMLElement | null): void {
        if (destroyed || (el === scroller && contentEl === content)) return;
        setIntentListeners(el, false);
        el = scroller;
        contentEl = content;
        setIntentListeners(el, true);
        touchDrag = false;
        press = null;
        lastTop = -1;
        lastGap = -1;

        if (observer) {
            observer.disconnect();
            if (scroller) observer.observe(scroller);
            if (content) observer.observe(content);
        }

        if (scroller) {
            // Pin next frame, once layout exists.
            requestAnimationFrame(() => {
                if (destroyed || el !== scroller) return;
                ready = true;
                pin();
            });
        }
    }

    function destroy(): void {
        destroyed = true;
        cancelPin();
        setIntentListeners(el, false);
        observer?.disconnect();
        observer = null;
        el = null;
        contentEl = null;
    }

    if (typeof ResizeObserver !== 'undefined') {
        observer = new ResizeObserver(() => handleResize());
    }

    return {
        get el() { return el; },
        get atBottom() { return atBottom; },
        get following() { return following; },
        get ready() { return ready; },
        get intent() { return intentSeq; },
        attach,
        pin,
        handleScroll,
        stopFollowing,
        destroy,
    };
}
