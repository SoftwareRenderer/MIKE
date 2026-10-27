/**
 * Whether a saved offset may move this pane. A line jump owns the scroll of the pane it names: the jump
 * writes an offset of its own, and a restore that writes first then holds would drag the jump back down.
 * Only explorer panes have a saved offset to restore.
 */
export function restoresSavedOffset(ask: { top: number; targetLine: number; mode: string }): boolean {
    return ask.top > 0 && ask.targetLine < 1 && ask.mode === 'explorer';
}

interface ScrollTarget {
    scrollTop: number;
    scrollHeight: number;
    clientHeight: number;
}

const SETTLE_FRAMES = 30;
const APPEAR_FRAMES = 600;

/**
 * Bring a reopened editor back to the offset it had when it was last closed. One write is not enough. The pane
 * may have no height yet, and an offset clamped against a height of zero is the top of the file, so such a
 * pane is waited out. CodeMirror then rewrites the offset from its own line estimates for a few frames, so
 * this holds the offset for a while and stops. The caller owns the pane and answers null once it is gone. Out
 * of frames, or as soon as anyone else moves the view, the reader has the scroll back.
 */
export function restoreScrollTop(readScroller: () => ScrollTarget | null, top: number): void {
    if (top <= 0) return;
    let settleLeft = SETTLE_FRAMES;
    let appearLeft = APPEAR_FRAMES;
    let wrote = false;

    const step = () => {
        const scroller = readScroller();
        if (!scroller) {
            if (--appearLeft <= 0) return;
        } else {
            if (!wrote && scroller.scrollTop > 0) return;
            const want = Math.min(top, scroller.scrollHeight - scroller.clientHeight);
            if (want > 0 && scroller.scrollTop !== want) scroller.scrollTop = want;
            if (scroller.scrollTop > 0) wrote = true;
            if (--settleLeft <= 0) return;
        }
        requestAnimationFrame(step);
    };
    step();
}
