/**
 * Scroll an element to a line inside a scroller whose entries skip their layout
 * while off-screen.
 *
 * One write is not enough: entries between here and the target may never have
 * rendered, so their heights are `contain-intrinsic-size` placeholders and the
 * offset measured from here is an estimate — the jump lands elsewhere once they
 * realize their real size. A smooth scroll cannot recover either; it keeps writing
 * positions derived from the stale numbers. So: write, measure, write again for a
 * few frames. `use-chat-follow` solves the same problem for the bottom of the chat.
 */

/** Frames to keep correcting — enough for the entries around the target to render. */
const SETTLE_FRAMES = 8;
/** Fractional layout (zoom, scaled rows) leaves an offset a hair off the line. */
const SETTLE_SLACK = 1;

/** Offset of `target`'s top edge from `scroller`'s top edge, in viewport space. */
function offsetFromTop(scroller: HTMLElement, target: HTMLElement): number {
    return target.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
}

/**
 * Scroll `target` until its top edge sits `line` pixels below the top of `scroller`.
 * Instant, and only `scroller` is written — `scrollIntoView` would move ancestor
 * scrollers too.
 *
 * Landing on the line matters beyond looks: a message left above it reads as
 * "scrolled past" and keeps the sticky header naming it, so the next tap lands it
 * again instead of going back a turn.
 */
export function scrollToLine(scroller: HTMLElement, target: HTMLElement, line: number): void {
    let framesLeft = SETTLE_FRAMES;
    let lastHeight = -1;

    const step = () => {
        // The list can be replaced mid-settle (thread switch, task change).
        if (!scroller.isConnected || !target.isConnected) return;
        const height = scroller.scrollHeight;
        const offset = offsetFromTop(scroller, target);
        // Settled only when nothing grew behind it either: entries can realize late.
        if (Math.abs(offset - line) <= SETTLE_SLACK && height === lastHeight) return;
        const want = scroller.scrollTop + offset - line;
        scroller.scrollTop = want;
        // At or above the content top (the first message): clamped, nothing to scroll.
        if (want <= 0 && scroller.scrollTop <= 0) return;
        lastHeight = scroller.scrollHeight;
        if (--framesLeft <= 0) return;
        requestAnimationFrame(step);
    };
    step();
}
