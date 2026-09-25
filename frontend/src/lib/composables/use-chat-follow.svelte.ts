import { SCROLL_BOTTOM_THRESHOLD } from '$lib/constants';

/**
 * Entries use `content-visibility: auto`, so a freshly rendered list reports placeholder heights
 * and a one-shot pin lands far above the real bottom. A pin re-pins until the height holds steady.
 */
const PIN_SETTLE_FRAMES = 12;
const PIN_STABLE_FRAMES = 2;

/**
 * Distance from the current scroll position to the very bottom, in px. Pass `totalHeight` only
 * where the element's own `scrollHeight` is smaller than its content, as a virtual list's is.
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
    /** The scroll container (null until ChatEntries mounts it). */
    readonly el: HTMLElement | null;
    /** True while the viewport is within SCROLL_BOTTOM_THRESHOLD of the bottom. */
    readonly atBottom: boolean;
    /**
     * True while new content should keep the viewport pinned. Unlike atBottom it survives the gap
     * that growing content opens up; only the user scrolling away clears it.
     */
    readonly following: boolean;
    /** True once the container has been attached and laid out at least once. */
    readonly ready: boolean;
    /** Register the container and the entry wrapper; both are watched for layout changes. */
    attach(scroller: HTMLElement | null, content: HTMLElement | null): void;
    /** Pin the viewport to the bottom, unless the user has scrolled away and this is not forced. */
    pin(force?: boolean): boolean;
    /** Feed the container's scroll event in — this is who decides who is driving. */
    handleScroll(): void;
    /** Record user input whose direction is not readable (a touch drag, a wheel without a delta).
     * A gap after this is a takeover even when the layout changed too. */
    noteUserIntent(): void;
    /** How many times the reader has shown they want to scroll. */
    readonly intent: number;
    /** The reader asked for what is above the viewport. Stops the pin loop so growth below does
     * not pull them back. */
    stopFollowing(): void;
    /** Release observers and pending frames. */
    destroy(): void;
}

/**
 * Single owner of the chat's "follow the latest content" behaviour, so TaskChat and ChatEntries
 * share one notion of "the user is at the bottom" and one writer of scrollTop. Following is driven
 * by layout as well as by scroll events, because a lazy entry or a panel resize fires no scroll
 * event of its own.
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

    /**
     * Auto-scroll intent, kept apart from atBottom. A gap is not always the reader scrolling away:
     * the document moving under the viewport opens one too, so only unexplained movement is a
     * takeover.
     */
    let following = true;
    /** Document and viewport height the last judgement was based on. */
    let knownHeight = -1;
    let knownClient = -1;
    /** The observer saw the layout change; the scroll event it causes may come later. */
    let layoutDirty = false;
    /** Explicit user input, counted rather than timed so the judgement stays deterministic. */
    let intentSeq = 0;
    let intentAtPin = 0;

    /** True when the document or the container changed since the last judgement. */
    function layoutMoved(node: HTMLElement): boolean {
        return layoutDirty || node.scrollHeight !== knownHeight || node.clientHeight !== knownClient;
    }

    /** Update atBottom from live layout (no intent judgements). */
    function measure() {
        if (!el) return;
        atBottom = isAtBottom(el);
    }

    /** Remember the current layout as "explained". */
    function noteLayout(node: HTMLElement) {
        knownHeight = node.scrollHeight;
        knownClient = node.clientHeight;
        layoutDirty = false;
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
        let lastHeight = -1;
        let stableFrames = 0;

        const step = () => {
            pinRaf = null;
            // A takeover ends the sequence, forced or not: forcing authorises this pin, not the
            // frames after someone else started moving the viewport.
            if (!following) return;
            intentAtPin = intentSeq;
            const height = node.scrollHeight;
            node.scrollTop = height;
            noteLayout(node);
            // This write's own scroll event arrives next frame, so update the state now.
            measure();
            if (height === lastHeight) {
                if (++stableFrames >= PIN_STABLE_FRAMES) return;
            } else {
                stableFrames = 0;
                lastHeight = height;
            }
            if (--framesLeft <= 0) return;
            pinRaf = requestAnimationFrame(step);
        };
        step();
        return true;
    }

    function handleScroll() {
        if (!el || destroyed) return;
        const node = el;
        const at = isAtBottom(node);
        const moved = layoutMoved(node);
        const userActed = intentSeq !== intentAtPin;
        if (at) {
            following = true;
            intentAtPin = intentSeq;
        } else if (following && moved && !userActed) {
            // The gap came from the layout itself, not from the reader.
        } else {
            following = false;
        }
        atBottom = at;
        noteLayout(node);
        if (following && !at) pin(true);
    }

    /** Content can grow with no scroll event: mark the layout moved, so the event it produces is
     * not read as a takeover. */
    function handleResize() {
        if (!el || destroyed) return;
        layoutDirty = true;
        measure();
        if (following) pin(true);
    }

    function noteUserIntent(): void {
        intentSeq++;
    }

    function stopFollowing(): void {
        following = false;
        cancelPin();
        noteUserIntent();
    }

    /** Whether anything sits above the viewport: with nothing there, a gesture upward asks for
     * nothing, and a wheel that could not move anything would end following. */
    function hasAbove(): boolean {
        return !!el && el.scrollTop > 0;
    }

    /** Keys that scroll a container; anything else is not a takeover. */
    const SCROLL_KEYS = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' ']);
    /** The subset that asks for what is above the viewport. */
    const ABOVE_KEYS = new Set(['ArrowUp', 'PageUp', 'Home']);

    /** A gesture upward ends following where it lands: its scroll event is dispatched a frame
     * later, by which time a pin frame has already overwritten the reader's position. */
    function onIntentWheel(e: Event) {
        if ((e as WheelEvent).deltaY < 0 && hasAbove()) {
            stopFollowing();
            return;
        }
        noteUserIntent();
    }

    function onIntentKeydown(e: Event) {
        const key = (e as KeyboardEvent).key;
        if (!SCROLL_KEYS.has(key)) return;
        if (ABOVE_KEYS.has(key) && hasAbove()) stopFollowing();
        else noteUserIntent();
    }

    function setIntentListeners(node: HTMLElement | null, on: boolean) {
        if (!node) return;
        const method = on ? 'addEventListener' : 'removeEventListener';
        // Not pointerdown: clicking a message or selecting text is not scrolling. A scrollbar drag
        // has no event of its own, so it is judged by the scroll event it produces.
        node[method]('wheel', onIntentWheel, { passive: true });
        node[method]('touchmove', noteUserIntent, { passive: true });
        node[method]('keydown', onIntentKeydown);
    }

    function attach(scroller: HTMLElement | null, content: HTMLElement | null): void {
        if (destroyed || (el === scroller && contentEl === content)) return;
        setIntentListeners(el, false);
        el = scroller;
        contentEl = content;
        setIntentListeners(el, true);

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
        noteUserIntent,
        stopFollowing,
        destroy,
    };
}
