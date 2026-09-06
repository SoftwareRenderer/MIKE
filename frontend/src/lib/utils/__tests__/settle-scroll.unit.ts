import { describe, it, expect, afterEach } from 'vitest';
import { scrollToLine } from '../settle-scroll';

/**
 * A scroller with the layout the chat has: geometry is a function of `scrollTop`,
 * and until the viewport comes near the target the entries above it are still
 * placeholders, so the target reports a position 1000px short of its real one.
 */
function makeScroller() {
    const scroller = document.createElement('div');
    const target = document.createElement('div');
    scroller.appendChild(target);
    document.body.appendChild(scroller);

    const CLIENT = 500;
    const PLACEHOLDER = { top: 4000, height: 5000 };
    const REAL = { top: 5000, height: 6000 };
    const state = { scrollTop: 0, writes: 0, realized: false };
    const geometry = () => (state.realized ? REAL : PLACEHOLDER);

    // Like the browser: clamped to the content.
    Object.defineProperty(scroller, 'scrollTop', {
        configurable: true,
        get: () => state.scrollTop,
        set: (value: number) => {
            state.writes++;
            state.scrollTop = Math.max(0, Math.min(value, geometry().height - CLIENT));
            if (state.scrollTop >= PLACEHOLDER.top - 1000) state.realized = true;
        },
    });
    Object.defineProperty(scroller, 'scrollHeight', { configurable: true, get: () => geometry().height });
    Object.defineProperty(scroller, 'clientHeight', { configurable: true, get: () => CLIENT });
    Object.defineProperty(scroller, 'getBoundingClientRect', {
        configurable: true,
        value: () => rect(100, CLIENT),
    });
    Object.defineProperty(target, 'getBoundingClientRect', {
        configurable: true,
        value: () => rect(100 + geometry().top - state.scrollTop, 40),
    });

    return { scroller, target, state, realTop: REAL.top };
}

function rect(top: number, height: number) {
    return { top, bottom: top + height, left: 0, right: 0, width: 0, height, x: 0, y: top, toJSON: () => ({}) };
}

async function frames(n: number) {
    for (let i = 0; i < n; i++) await new Promise((r) => requestAnimationFrame(() => r(null)));
}

let mounted: HTMLElement[] = [];
afterEach(() => {
    mounted.forEach((el) => el.remove());
    mounted = [];
});

function mount() {
    const made = makeScroller();
    mounted.push(made.scroller);
    return made;
}

describe('scrollToLine', () => {
    it('lands the target on the line, then confirms nothing grew behind it', async () => {
        const { scroller, target, state } = mount();
        state.realized = true; // the target really is at content top 5000

        scrollToLine(scroller, target, 54);

        expect(state.scrollTop).toBe(4946); // 5000 - 54
        await frames(3);
        // The follow-up frame only checks; nothing grew, so it writes once.
        expect(state.scrollTop).toBe(4946);
        expect(state.writes).toBe(1);
    });

    it('re-lands the target when entries realize late and shift it', async () => {
        const { scroller, target, state, realTop } = mount();
        state.realized = true;
        // The first write is exact, then an entry above renders and pushes the
        // target 60px down.
        Object.defineProperty(target, 'getBoundingClientRect', {
            configurable: true,
            value: () => rect(100 + realTop - (state.writes >= 1 ? 60 : 0) - state.scrollTop, 40),
        });

        scrollToLine(scroller, target, 54);
        await frames(4);

        expect(state.scrollTop).toBe(realTop - 60 - 54);
    });

    it('keeps correcting until the lazily rendered entries realize their height', async () => {
        const { scroller, target, state } = mount();

        scrollToLine(scroller, target, 54);
        expect(state.scrollTop).toBe(3946); // the first write chased the placeholder
        expect(state.realized).toBe(true);

        await frames(4);

        expect(state.scrollTop).toBe(5000 - 54);
        const settled = state.writes;
        await frames(8);
        expect(state.writes).toBe(settled); // it stops by itself
    });

    it('does not scroll past the top of the content', () => {
        const { scroller, target, state } = mount();
        Object.defineProperty(target, 'getBoundingClientRect', {
            configurable: true,
            value: () => rect(100 + 20 - state.scrollTop, 40), // 20px from the content top
        });

        scrollToLine(scroller, target, 54);

        expect(state.scrollTop).toBe(0);
        expect(state.writes).toBe(1);
    });

    it('stops when the target leaves the document mid-settle', async () => {
        const { scroller, target, state } = mount();

        scrollToLine(scroller, target, 54); // mid-settle: still correcting
        const writes = state.writes;
        target.remove();
        await frames(6);

        expect(state.writes).toBe(writes);
    });
});
