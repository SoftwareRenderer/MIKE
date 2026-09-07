import { SCROLL_BOTTOM_THRESHOLD } from '$lib/constants';

/**
 * A pin keeps re-pinning for up to this many frames and stops once the height has
 * held steady for this many of them. Entries use `content-visibility: auto`, so a
 * freshly rendered list reports placeholder heights: a one-shot pin lands far
 * above the real bottom (~1100px short on a 30-entry history) and only corrects
 * itself as the nearby entries grow to their real size.
 */
const PIN_SETTLE_FRAMES = 12;
const PIN_STABLE_FRAMES = 2;

/** Distance from the current scroll position to the very bottom, in px. */
export function distanceFromBottom(el: HTMLElement): number {
    return el.scrollHeight - el.scrollTop - el.clientHeight;
}

/** Whether the viewport is close enough to the bottom to keep following. */
export function isAtBottom(el: HTMLElement): boolean {
    return distanceFromBottom(el) < SCROLL_BOTTOM_THRESHOLD;
}

/** Public surface of the chat follow tracker. */
export interface ChatFollow {
    /** The scroll container (null until ChatEntries mounts it). */
    readonly el: HTMLElement | null;
    /** True while the viewport is within SCROLL_BOTTOM_THRESHOLD of the bottom. */
    readonly atBottom: boolean;
    /**
     * True while new content should keep the viewport pinned. Unlike atBottom it
     * survives the gap that growing content opens up; only the user scrolling away
     * clears it.
     */
    readonly following: boolean;
    /** True once the container has been attached and laid out at least once. */
    readonly ready: boolean;
    /** Register the container and the entry wrapper; both are watched for layout changes. */
    attach(scroller: HTMLElement | null, content: HTMLElement | null): void;
    /**
     * Pin the viewport to the bottom. Skipped when the user has scrolled away,
     * unless forced (a user-initiated send). Returns true if a pin was attempted.
     */
    pin(force?: boolean): boolean;
    /** Feed the container's scroll event in — this is who decides who is driving. */
    handleScroll(): void;
    /**
     * Record explicit user input (wheel, touch drag, navigation keys). A gap after
     * this is a takeover even when the layout changed at the same moment.
     */
    noteUserIntent(): void;
    /** Release observers and pending frames. */
    destroy(): void;
}

/**
 * Single owner of the chat's "follow the latest content" behaviour, so TaskChat and
 * ChatEntries share one notion of "the user is at the bottom" and one writer of scrollTop.
 *
 * Following is driven by layout, not only by scroll events: a ResizeObserver on the
 * container and on the content wrapper re-measures and re-pins, because lazy entries,
 * images and panel resizes fire no scroll event and would otherwise leave the viewport
 * above the bottom with the jump button hidden.
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
     * Auto-scroll intent, kept apart from atBottom. A scroll event with a gap is
     * not always the user scrolling away: while following, the document itself
     * moves (entries realize their height, the container resizes) and that moves
     * the viewport too. Only unexplained movement counts as a takeover — otherwise
     * growing content would silently switch auto-scroll off.
     */
    let following = true;
    /** Document and viewport height the last judgement was based on. */
    let knownHeight = -1;
    let knownClient = -1;
    /** The observer saw the layout change; the scroll event it causes may come later. */
    let layoutDirty = false;
    /**
     * Explicit user input, counted rather than timed so the logic stays
     * deterministic. A gap after new input is a takeover even if the layout changed.
     */
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

        // A new pin replaces any in-flight sequence.
        cancelPin();
        let framesLeft = PIN_SETTLE_FRAMES;
        let lastHeight = -1;
        let stableFrames = 0;

        const step = () => {
            pinRaf = null;
            // The user may have taken over since the previous frame — scroll events
            // run before rAF callbacks, so re-check instead of yanking them back.
            if (!force && !following) return;
            intentAtPin = intentSeq;
            const height = node.scrollHeight;
            node.scrollTop = height;
            noteLayout(node);
            // The scroll event this write produces arrives next frame; update the
            // state now so later decisions in this frame see the truth.
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
            // At the bottom, so we are following again. Any earlier gesture is spent.
            following = true;
            intentAtPin = intentSeq;
        } else if (following && moved && !userActed) {
            // The gap came from the layout itself (lazy rendering, reflow, resize) —
            // the user is still trying to read the latest content.
        } else {
            following = false;
        }
        atBottom = at;
        noteLayout(node);
        // Still following but no longer at the bottom: the document moved under us,
        // so close the gap instead of waiting for another event to tell us to.
        if (following && !at) pin(true);
    }

    /** Layout changed (container resized or content grew) — stay accurate. */
    function handleResize() {
        if (!el || destroyed) return;
        // Content can grow with no scroll event at all. Mark the layout as moved (so
        // the scroll event it may still produce is not read as a takeover) and
        // re-measure so atBottom stays truthful.
        layoutDirty = true;
        measure();
        if (following) pin(true);
    }

    function noteUserIntent(): void {
        intentSeq++;
    }

    /** Keys that scroll a container; anything else is not a takeover. */
    const SCROLL_KEYS = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' ']);

    function onIntentGesture() {
        noteUserIntent();
    }

    function onIntentKeydown(e: Event) {
        if (SCROLL_KEYS.has((e as KeyboardEvent).key)) noteUserIntent();
    }

    function setIntentListeners(node: HTMLElement | null, on: boolean) {
        if (!node) return;
        const method = on ? 'addEventListener' : 'removeEventListener';
        // Deliberately not pointerdown: clicking a message or selecting text is not
        // scrolling. Scrollbar drags need no listener — they move the viewport
        // without changing the layout, which already measures as a takeover.
        node[method]('wheel', onIntentGesture, { passive: true });
        node[method]('touchmove', onIntentGesture, { passive: true });
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
            // Reveal and pin next frame: layout exists by then, and this covers a
            // scroll request that arrived before the container mounted.
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
        attach,
        pin,
        handleScroll,
        noteUserIntent,
        destroy,
    };
}
